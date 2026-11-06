package schedule

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
	"strconv"
	"strings"
	"time"

	"github.com/yosukeno/pogo/tools"
)

// DefaultJobTimeout bounds one child run. Two hours is generous for a
// max-turns-capped headless run and finite enough that a wedged child cannot
// pin the job's lock forever.
const DefaultJobTimeout = 2 * time.Hour

// MaxJobTurns is the historical CLI/implicit Agent limit. Explicit Agent
// turn_limit_mode definitions have their own finite or unlimited semantics.
const MaxJobTurns = 50

// LogPaths are the two files a run's output lands in.
type LogPaths struct {
	Out string
	Err string
}

// RunOutcome is what a Runner reports about one execution.
type RunOutcome struct {
	// SessionID is the child's session (the transcript basename), parsed out
	// of the JSONL header. Empty when the child never got that far.
	SessionID     string
	Summary       string
	Model         string
	AgentRevision int
	// EndReason is the child's run_end.end_reason ("completed", "turn_limit",
	// ...), empty when no run_end frame arrived.
	EndReason string
	// Err is the failure description for the history record. Empty means the
	// run is considered successful as far as ExitCode is concerned.
	Err      string
	ExitCode int
}

// Runner executes one job. It is a parameter of RunOnce so tests can drive
// the bookkeeping without spawning a pogo child; the production
// implementation is ChildRunner.
type Runner func(ctx context.Context, job Job, logs LogPaths, mirror io.Writer) RunOutcome

// RunResult is RunOnce's report.
type RunResult struct {
	ExitCode  int
	Status    string // StatusOK, StatusError, StatusSkipped
	Skipped   bool
	SessionID string
	EndReason string
	Duration  time.Duration
}

// RunScheduled is the entry point the OS backends invoke (`pogo
// -schedule-exec <id>`): lock, run, bookkeep. Returns the process exit code.
func RunScheduled(ctx context.Context, dir, id, trigger string) int {
	res, err := NewStore(dir).RunOnce(ctx, id, trigger, nil, ChildRunner)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pogo -schedule-exec:", err)
	}
	return res.ExitCode
}

// RunOnce is the testable core of RunScheduled: everything except how the
// child is spawned.
//
// The per-job flock is what makes an overlapping trigger (a previous run
// still going when the next fires, or run-now during a scheduled run) a skip
// rather than two agents editing the same workdir at once. A skipped trigger
// is recorded — silence there reads as "the schedule died".
func (s *Store) RunOnce(ctx context.Context, id, trigger string, mirror io.Writer, run Runner) (RunResult, error) {
	return s.runOnce(ctx, id, trigger, "", mirror, run)
}

// RunOnceSealed rechecks the host's admitted definition while holding the
// same execution lock as CLI and manual runs. Queue payloads cannot silently
// acquire a newer prompt, owner or approval mode.
func (s *Store) RunOnceSealed(ctx context.Context, id, trigger, expectedHash string, mirror io.Writer, run Runner) (RunResult, error) {
	if expectedHash == "" {
		return RunResult{ExitCode: 1}, ErrConflict
	}
	return s.runOnce(ctx, id, trigger, expectedHash, mirror, run)
}

func (s *Store) runOnce(ctx context.Context, id, trigger, expectedHash string, mirror io.Writer, run Runner) (RunResult, error) {
	if err := os.MkdirAll(filepath.Join(s.dir, "locks"), 0o700); err != nil {
		return RunResult{ExitCode: 1}, err
	}
	lf, err := os.OpenFile(s.lockPath(id), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return RunResult{ExitCode: 1}, err
	}
	defer lf.Close()
	if err := lockFile(lf.Fd()); err != nil {
		fmt.Fprintf(os.Stderr, "pogo -schedule-exec: job %s already running, skipping\n", id)
		rec := RunRecord{TS: time.Now(), Trigger: trigger, Status: StatusSkipped, Error: "already running"}
		if aerr := s.AppendRun(id, rec); aerr != nil {
			fmt.Fprintln(os.Stderr, "pogo -schedule-exec: recording skip:", aerr)
		}
		return RunResult{ExitCode: 1, Status: StatusSkipped, Skipped: true}, nil
	}
	defer unlockFile(lf.Fd())

	job, err := s.Find(id)
	if err != nil {
		return RunResult{ExitCode: 1}, err
	}
	if expectedHash != "" && DefinitionHash(job) != expectedHash {
		return RunResult{ExitCode: 1}, ErrConflict
	}
	if !job.Enabled && !(job.AgentID != "" && trigger == TriggerManual) {
		return RunResult{ExitCode: 1}, fmt.Errorf("scheduled job %q is disabled", id)
	}
	if job.DefHash == "" {
		if job.AgentID != "" || job.Owner != nil || job.Parts != nil || job.TurnLimitMode != "" || job.ApprovalMode != "" {
			return RunResult{ExitCode: 1}, fmt.Errorf("Agent scheduled job is missing its trusted definition seal")
		}

		// A job from before sealing existed: bless it once by computing and
		// persisting, so upgrades do not break existing schedules. From this
		// run on, the tamper check below applies to it like any other.
		job.DefHash = DefinitionHash(job)
		if err := s.Update(id, func(j *Job) { j.DefHash = job.DefHash }); err != nil {
			return RunResult{ExitCode: 1}, err
		}
	}
	if got := DefinitionHash(job); got != job.DefHash {
		// Refusing, not re-sealing: the whole point is that a changed
		// definition means someone other than the creator touched the job,
		// and the run must not quietly proceed on the new text. The refusal
		// goes into the run history, or the tampering is invisible.
		rec := RunRecord{TS: time.Now(), Trigger: trigger, Status: StatusError,
			Error: "definition changed since creation; refused to run"}
		if aerr := s.AppendRun(id, rec); aerr != nil {
			fmt.Fprintln(os.Stderr, "pogo -schedule-exec: recording refusal:", aerr)
		}
		return RunResult{ExitCode: 1}, fmt.Errorf(
			"scheduled job %q: definition changed since creation; refusing to run — edit its definition or re-create it with a new name if the change was intended", id)
	}
	if err := ValidateAgentRunOptions(job); err != nil {
		return RunResult{ExitCode: 1}, err
	}
	if job.ExpiresAt != "" {
		exp, err := time.Parse(time.RFC3339, job.ExpiresAt)
		if err != nil {
			return RunResult{ExitCode: 1}, fmt.Errorf("scheduled job %q: expires_at %q does not parse: %w", id, job.ExpiresAt, err)
		}
		if !exp.After(time.Now()) {
			// Expired jobs disable rather than silently keep firing: an
			// unattended loop with no sunset is a persistence mechanism, and
			// renewing one should be a decision someone makes.
			_ = s.Update(id, func(j *Job) { j.Enabled = false })
			rec := RunRecord{TS: time.Now(), Trigger: trigger, Status: StatusError,
				Error: "expired and disabled; re-create with a later expiry to renew"}
			if aerr := s.AppendRun(id, rec); aerr != nil {
				fmt.Fprintln(os.Stderr, "pogo -schedule-exec: recording expiry:", aerr)
			}
			return RunResult{ExitCode: 1}, fmt.Errorf(
				"scheduled job %q expired at %s and was disabled; edit its expiry or re-create it with a new name and later expiry to renew", id, job.ExpiresAt)
		}
	}

	start := time.Now()
	outcome := run(ctx, job, LogPaths{Out: s.OutLogPath(id), Err: s.ErrLogPath(id)}, mirror)
	dur := time.Since(start)

	status := StatusOK
	if outcome.ExitCode != 0 || outcome.Err != "" {
		status = StatusError
	}
	var disabledWhy string
	if err := s.Update(id, func(j *Job) {
		j.LastRunAt = start
		j.LastRunStatus = status
		j.LastRunDurationSec = dur.Seconds()
		j.SessionID = outcome.SessionID
		j.LastError, j.LastSummary = outcome.Err, outcome.Summary
		if status == StatusOK {
			j.ConsecutiveFailures = 0
			// A one-shot that fired is done; it stays listed, disarmed.
			if j.Kind == KindAt {
				j.Enabled = false
				disabledWhy = "one-shot job completed"
			}
		} else {
			j.ConsecutiveFailures++
			if j.ConsecutiveFailures >= MaxConsecutiveFailures {
				j.Enabled = false
				disabledWhy = fmt.Sprintf("%d consecutive failures", j.ConsecutiveFailures)
			}
		}
	}); err != nil {
		return RunResult{ExitCode: 1}, err
	}
	if disabledWhy != "" {
		fmt.Fprintf(os.Stderr, "pogo -schedule-exec: job %s disabled: %s\n", id, disabledWhy)
	}

	rec := RunRecord{
		TS: start, Trigger: trigger, SessionID: outcome.SessionID,
		Status: status, DurationSec: dur.Seconds(),
		EndReason: outcome.EndReason, Error: outcome.Err, Summary: outcome.Summary, Model: outcome.Model, AgentRevision: outcome.AgentRevision,
	}
	if err := s.AppendRun(id, rec); err != nil {
		fmt.Fprintln(os.Stderr, "pogo -schedule-exec: recording run:", err)
	}

	code := outcome.ExitCode
	if status == StatusError && code == 0 {
		code = 1
	}
	return RunResult{
		ExitCode: code, Status: status,
		SessionID: outcome.SessionID, EndReason: outcome.EndReason, Duration: dur,
	}, nil
}

// childArgs builds the headless invocation a job runs as: the same argv shape
// the subagent tool uses, plus the job's own caps. Zero knobs stay out so the
// child inherits the CLI defaults instead of pinned zeroes.
func childArgs(job Job) []string {
	args := []string{"-mode", "json", "-C", job.Workdir, "-p", job.Prompt}
	if job.MaxTurns > 0 {
		// Clamped, not trusted: the value lands in an unattended child's
		// budget, so a job cannot name a number no human would have set.
		args = append(args, "-max-turns", strconv.Itoa(min(job.MaxTurns, MaxJobTurns)))
	}
	if job.BudgetUSD > 0 {
		args = append(args, "-cost-budget", strconv.FormatFloat(job.BudgetUSD, 'g', -1, 64))
	}
	if job.Model != "" {
		args = append(args, "-model", job.Model)
	}
	return args
}

// ChildRunner is the production Runner: it spawns this same binary in
// headless JSON mode — the exact precedent the subagent tool sets — and tees
// the child's event stream into the job's log files, into a buffer for
// frame parsing, and onto mirror when the caller wants a live view (run-now).
func ChildRunner(ctx context.Context, job Job, logs LogPaths, mirror io.Writer) RunOutcome {
	if job.WorkflowID != "" {
		return RunOutcome{Err: ErrRetiredWorkflow.Error(), ExitCode: 1}
	}
	if job.AgentID != "" || job.Timezone != "" || job.TurnLimitMode != "" || job.ApprovalMode != "" {
		return RunOutcome{Err: "Agent schedules require the web Agent runtime; refusing bare CLI execution", ExitCode: 1}
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultJobTimeout)
	defer cancel()

	exe, err := os.Executable()
	if err != nil {
		return RunOutcome{Err: err.Error(), ExitCode: 1}
	}
	args := childArgs(job)

	if err := os.MkdirAll(filepath.Dir(logs.Out), 0o700); err != nil {
		return RunOutcome{Err: err.Error(), ExitCode: 1}
	}
	outF, err := os.OpenFile(logs.Out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return RunOutcome{Err: err.Error(), ExitCode: 1}
	}
	defer outF.Close()
	errF, err := os.OpenFile(logs.Err, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return RunOutcome{Err: err.Error(), ExitCode: 1}
	}
	defer errF.Close()

	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, exe, args...)
	// An unattended run must never get *less* containment than the operator's
	// own interactive runs. When this process's sandbox is on, the child is
	// pinned to enforce: a missing or broken bubblewrap then refuses the
	// command rather than quietly running it bare where no human is watching
	// (Go's env handling keeps the last duplicate, so this wins over any
	// inherited POGO_SANDBOX value). When this process has no working sandbox
	// — a degrade platform — the child inherits that same state, honestly,
	// rather than pretending at a boundary the operator does not have either.
	if tools.InitSandbox().Active() {
		cmd.Env = append(os.Environ(), "POGO_SANDBOX=enforce")
	}
	stdoutW := []io.Writer{outF, &buf}
	stderrW := []io.Writer{errF}
	if mirror != nil {
		stdoutW = append(stdoutW, mirror)
		stderrW = append(stderrW, mirror)
	}
	cmd.Stdout = io.MultiWriter(stdoutW...)
	cmd.Stderr = io.MultiWriter(stderrW...)

	runErr := cmd.Run()

	code := 0
	if runErr != nil {
		code = 1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) && exitErr.ExitCode() > 0 {
			code = exitErr.ExitCode()
		}
	}
	sessionID, endReason, frameErr := parseStream(buf.Bytes())

	out := RunOutcome{SessionID: sessionID, EndReason: endReason, ExitCode: code}
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		out.Err = fmt.Sprintf("timed out after %s", DefaultJobTimeout)
		out.ExitCode = codeOr(code, 1)
	case runErr != nil:
		out.Err = frameErr
		if out.Err == "" {
			out.Err = runErr.Error()
		}
	case frameErr != "":
		// The child exited 0 but reported a run-level error on the stream:
		// trust the stream, because -mode json flattens run errors into it.
		out.Err = frameErr
		out.ExitCode = 1
	}
	return out
}

func codeOr(code, fallback int) int {
	if code != 0 {
		return code
	}
	return fallback
}

// streamFrame is the subset of the JSONL contract the executor reads: the
// header's session path, and run_end's end_reason/error. Everything else the
// stream carries is for the log files, not for the bookkeeping.
type streamFrame struct {
	Type      string `json:"type"`
	Session   string `json:"session"`
	EndReason string `json:"end_reason"`
	Error     string `json:"error"`
}

// parseStream scans the captured stdout for the frames that carry facts.
// Session id is the transcript basename; the session header names a path,
// not an id, and basename-without-extension is the id everywhere in pogo.
func parseStream(data []byte) (sessionID, endReason, errText string) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<20), 4<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var f streamFrame
		if json.Unmarshal(line, &f) != nil {
			continue
		}
		switch f.Type {
		case "session":
			sessionID = strings.TrimSuffix(filepath.Base(f.Session), ".jsonl")
		case "run_end":
			endReason, errText = f.EndReason, f.Error
		}
	}
	return sessionID, endReason, errText
}
