// Package sentinelexec supervises source-agnostic sentinel scripts. Scripts
// produce protocol frames; only the host callback can durably accept them.
package sentinelexec

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/sentinelsdk"
	"github.com/yosukeno/pogo/tools"
)

const (
	MaxFrameBytes = 64 << 10
	MaxStateBytes = 16 << 10
	MaxLogBytes   = 64 << 10
	writeTimeout  = 10 * time.Second
)

// Config contains host-granted execution facts, never raw tool arguments.
// Network grants the parent network namespace, not a destination allowlist.
// Memory/CPU/process limits are sampled supervision, not cgroup hard quotas.
type Config struct {
	Command                    []string
	ScriptDir, Workspace, Mode string
	Network                    bool
	Timeout                    time.Duration
	State, Cursor              json.RawMessage
	MaxOutputBytes             int
	MemoryMiB, MaxProcesses    int
	CPUMilliCores              int
}

type Runner struct {
	// OnLog receives bounded, untrusted stderr chunks; it must return promptly.
	OnLog func(string)
	// Test seams are deliberately private: production cannot opt out of OS
	// confinement through script configuration or model arguments.
	wrap    func(*exec.Cmd, *tools.CommandScope) error
	monitor func(context.Context, int, Config) error
}

type boot struct {
	V         int             `json:"v"`
	Kind      string          `json:"kind"`
	Mode      string          `json:"mode"`
	Workspace string          `json:"workspace"`
	State     json.RawMessage `json:"state"`
	Cursor    json.RawMessage `json:"cursor"`
}

func normalize(c Config) (Config, error) {
	if c.Mode != "poll" && c.Mode != "stream" {
		return c, errors.New("sentinel script mode must be poll or stream")
	}
	if len(c.Command) == 0 || len(c.Command) > 64 || !filepath.IsAbs(c.Command[0]) || !filepath.IsAbs(c.ScriptDir) || !filepath.IsAbs(c.Workspace) {
		return c, errors.New("sentinel script requires an absolute executable, sealed directory and workspace")
	}
	n := 0
	for _, a := range c.Command {
		n += len(a)
		if strings.ContainsRune(a, 0) {
			return c, errors.New("sentinel command contains NUL")
		}
	}
	if n > 16<<10 {
		return c, errors.New("sentinel command is too large")
	}
	for _, v := range []*json.RawMessage{&c.State, &c.Cursor} {
		if len(*v) == 0 {
			*v = json.RawMessage("null")
		}
		if len(*v) > MaxStateBytes || !json.Valid(*v) {
			return c, errors.New("sentinel boot state or cursor is invalid or too large")
		}
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	if c.Timeout < time.Second || c.Timeout > 10*time.Minute {
		return c, errors.New("sentinel poll timeout must be between 1 and 600 seconds")
	}
	if c.MaxOutputBytes == 0 {
		c.MaxOutputBytes = MaxFrameBytes
	}
	if c.MaxOutputBytes < 256 || c.MaxOutputBytes > MaxFrameBytes {
		return c, errors.New("sentinel frame limit must be between 256 and 65536 bytes")
	}
	if c.MemoryMiB == 0 {
		c.MemoryMiB = 256
	}
	if c.MaxProcesses == 0 {
		c.MaxProcesses = 64
	}
	if c.CPUMilliCores == 0 {
		c.CPUMilliCores = 250
	}
	if c.MemoryMiB < 16 || c.MemoryMiB > 1024 || c.MaxProcesses < 4 || c.MaxProcesses > 128 || c.CPUMilliCores < 50 || c.CPUMilliCores > 1000 {
		return c, errors.New("sentinel resource limits exceed host ceilings")
	}
	return c, nil
}

func decodeFrame(raw []byte) (sentinel.Frame, error) {
	var f sentinel.Frame
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&f); err != nil {
		// Decoder errors can quote an arbitrary unknown key or field path.
		// These are source bytes, potentially credentials, not diagnostics
		// safe to persist or return to a model.
		return f, errors.New("invalid sentinel frame: malformed JSON, unknown fields or invalid field types")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return f, errors.New("sentinel frame must contain exactly one JSON value")
	}
	return f, sentinel.ValidateFrame(f)
}

// Run returns after a poll exits or a stream stops. A poll result is accepted
// only after exit zero. A stream frame is acknowledged on stdin only after
// accept returns nil. Errors (including callback errors) cancel the full tree.
func (r *Runner) Run(parent context.Context, cfg Config, accept func(sentinel.Frame) error) error {
	cfg, err := normalize(cfg)
	if err != nil {
		return err
	}
	if accept == nil {
		return errors.New("sentinel acceptance callback is required")
	}
	var ctx context.Context
	var cancel context.CancelFunc
	if cfg.Mode == "poll" {
		ctx, cancel = context.WithTimeout(parent, cfg.Timeout)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.Command[0], cfg.Command[1:]...)
	cmd.Dir = cfg.ScriptDir
	cmd.Env = os.Environ() // strictWrap scrubs this before launching.
	scope := &tools.CommandScope{Cwd: cfg.ScriptDir, ReadOnlyWorkspace: true, Network: cfg.Network}
	if filepath.Clean(cfg.Workspace) != filepath.Clean(cfg.ScriptDir) {
		scope.ReadOnlyRoots = []string{cfg.Workspace}
	}
	wrap := r.wrap
	if wrap == nil {
		wrap = strictWrap
	}
	if err := wrap(cmd, scope); err != nil {
		return fmt.Errorf("sentinel sandbox unavailable: %w", err)
	}
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return tools.KillGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer stdinR.Close()
	defer stdinW.Close()
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer stdoutR.Close()
	defer stdoutW.Close()
	cmd.Stdin, cmd.Stdout = stdinR, stdoutW
	logs := &logCapture{cancel: cancel, onLog: r.OnLog}
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("sentinel start failed: %w", err)
	}
	stdinR.Close()
	stdoutW.Close()
	defer tools.KillGroup(cmd)
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	monitor := r.monitor
	if monitor == nil {
		monitor = monitorResources
	}
	mon := make(chan error, 1)
	go func() {
		err := monitor(ctx, cmd.Process.Pid, cfg)
		mon <- err
		if err != nil {
			cancel()
		}
	}()
	// Cancelling closes control pipes as well as the process tree, so an
	// uncooperative child cannot strand a protocol read or acknowledgement.
	pipeDone := make(chan struct{})
	defer close(pipeDone)
	go func() {
		select {
		case <-ctx.Done():
			stdinW.Close()
			stdoutR.Close()
		case <-pipeDone:
		}
	}()
	if err := writeControl(ctx, stdinW, boot{1, "boot", cfg.Mode, cfg.Workspace, cfg.State, cfg.Cursor}); err != nil {
		cancel()
		<-wait
		select {
		case resourceErr := <-mon:
			if resourceErr != nil {
				return resourceErr
			}
		default:
		}
		return fmt.Errorf("sentinel boot failed: %w", err)
	}
	if cfg.Mode == "poll" {
		stdinW.Close()
	}
	type readResult struct {
		frame *sentinel.Frame
		err   error
	}
	read := make(chan readResult, 1)
	go func() {
		scan := bufio.NewScanner(stdoutR)
		scan.Buffer(make([]byte, 4096), cfg.MaxOutputBytes+1)
		var last *sentinel.Frame
		for scan.Scan() {
			if len(scan.Bytes()) > cfg.MaxOutputBytes {
				read <- readResult{err: errors.New("sentinel frame exceeds output limit")}
				return
			}
			f, err := decodeFrame(scan.Bytes())
			if err != nil {
				read <- readResult{err: err}
				return
			}
			if cfg.Mode == "poll" {
				if last != nil {
					read <- readResult{err: errors.New("poll must produce exactly one frame")}
					return
				}
				last = &f
				continue
			}
			if err := accept(f); err != nil {
				read <- readResult{err: err}
				return
			}
			if err := writeControl(ctx, stdinW, map[string]any{"v": 1, "kind": "accepted", "id": f.ID}); err != nil {
				read <- readResult{err: fmt.Errorf("sentinel acknowledgement failed: %w", err)}
				return
			}
		}
		read <- readResult{frame: last, err: scan.Err()}
	}()
	var rr readResult
	var waitErr, primary error
	readDone, waitDone := false, false
	for !readDone || !waitDone {
		select {
		case rr = <-read:
			readDone = true
			if rr.err != nil && primary == nil {
				primary = rr.err
				if ctx.Err() != nil {
					primary = ctx.Err()
				}
				cancel()
			}
		case waitErr = <-wait:
			waitDone = true
			// Always retire descendants, including a daemonized child that
			// kept stdout/stderr open after its entrypoint exited.
			_ = tools.KillGroup(cmd)
			stdinW.Close()
			if waitErr != nil && primary == nil {
				primary = sentinelScriptExitError(waitErr, logs)
				if ctx.Err() != nil {
					primary = ctx.Err()
				}
				cancel()
			}
		case err := <-mon:
			if err != nil && primary == nil {
				primary = err
				cancel()
			}
			mon = nil
		case <-ctx.Done():
			if primary == nil {
				primary = ctx.Err()
			}
			// Do not repeatedly select an already-cancelled context.
			if !waitDone {
				waitErr = <-wait
				waitDone = true
			}
			if !readDone {
				rr = <-read
				readDone = true
			}
		}
	}
	if primary == nil && ctx.Err() != nil {
		primary = ctx.Err()
	}
	cancel()
	if errors.Is(primary, context.Canceled) {
		select {
		case resourceErr := <-mon:
			if resourceErr != nil {
				primary = resourceErr
			}
		default:
		}
		if err := logs.failure(); err != nil {
			primary = err
		}
	}
	if primary != nil {
		return primary
	}
	if cfg.Mode == "poll" {
		if rr.frame == nil {
			return errors.New("poll exited without a frame")
		}
		return accept(*rr.frame)
	}
	return nil
}

func sentinelScriptExitError(err error, logs *logCapture) error {
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		if diagnostic, ok := sentinelsdk.ExitDiagnostic(exited.ExitCode()); ok {
			return fmt.Errorf("sentinel script reported %s: %w%s", diagnostic, err, logs.suffix())
		}
	}
	return fmt.Errorf("sentinel script failed: %w%s", err, logs.suffix())
}

func writeControl(ctx context.Context, w *os.File, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	done := make(chan error, 1)
	go func() { _, err := w.Write(raw); done <- err }()
	timer := time.NewTimer(writeTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		w.Close()
		<-done
		return ctx.Err()
	case <-timer.C:
		w.Close()
		<-done
		return errors.New("script stopped reading its control pipe")
	}
}

type logCapture struct {
	mu     sync.Mutex
	total  int64
	window time.Time
	bytes  int
	err    error
	cancel context.CancelFunc
	onLog  func(string)
}

func (w *logCapture) Write(p []byte) (int, error) {
	w.mu.Lock()
	if w.window.IsZero() || time.Since(w.window) >= time.Second {
		w.window, w.bytes = time.Now(), 0
	}
	w.bytes += len(p)
	w.total += int64(len(p))
	if w.bytes > MaxLogBytes {
		w.err = errors.New("sentinel stderr rate exceeds limit")
		w.mu.Unlock()
		w.cancel()
		return 0, w.failure()
	}
	w.mu.Unlock()
	if w.onLog != nil {
		w.onLog(string(p[:min(len(p), 4096)]))
	}
	return len(p), nil
}

func (w *logCapture) failure() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *logCapture) suffix() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.total == 0 {
		return ""
	}
	// OnLog is an explicit, untrusted diagnostic sink. Public errors must
	// not silently become another sink for arbitrary script stderr.
	return fmt.Sprintf(" (stderr withheld: %d bytes; script diagnostics may contain sensitive data)", w.total)
}

// Keep io imported near the protocol decoder: a second JSON value is invalid.
var _ io.Writer = (*logCapture)(nil)
