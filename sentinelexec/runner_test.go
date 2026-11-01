package sentinelexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/tools"
)

func fixture(t *testing.T, mode string) (Runner, Config) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := Runner{wrap: func(c *exec.Cmd, scope *tools.CommandScope) error {
		if !scope.ReadOnlyWorkspace || len(scope.ReadOnlyRoots) != 1 {
			return errors.New("missing readonly scope")
		}
		c.Env = append(os.Environ(), "GO_SENTINEL_EXEC_FIXTURE=1", "GORACE=atexit_sleep_ms=0")
		return nil
	}, monitor: func(ctx context.Context, _ int, _ Config) error { <-ctx.Done(); return nil }}
	c := Config{Command: []string{exe, "-test.run=^TestExecutorFixture$", "--", mode}, ScriptDir: t.TempDir(), Workspace: t.TempDir(), Mode: "poll", Timeout: 3 * time.Second}
	return r, c
}

func TestExecutorFixture(t *testing.T) {
	if os.Getenv("GO_SENTINEL_EXEC_FIXTURE") != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	d := json.NewDecoder(os.Stdin)
	var boot map[string]any
	if d.Decode(&boot) != nil || boot["kind"] != "boot" || boot["workspace"] == "" {
		os.Exit(8)
	}
	frame := func(id string) {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"v": 1, "id": id, "events": []any{map[string]any{"key": "event-" + id, "summary": "observed"}}, "state": boot["state"], "cursor": boot["cursor"]})
	}
	switch mode {
	case "poll", "pollfail", "multipoll":
		// Poll EOF is part of its finite input protocol.
		if _, err := d.Token(); err != io.EOF {
			os.Exit(9)
		}
		frame("frame1")
		if mode == "multipoll" {
			frame("frame2")
		}
		if mode == "pollfail" {
			os.Exit(7)
		}
	case "stream":
		frame("frame1")
		var ack map[string]any
		if d.Decode(&ack) != nil || ack["kind"] != "accepted" || ack["id"] != "frame1" {
			os.Exit(10)
		}
		fmt.Fprintln(os.Stderr, "ACK-SEEN")
		frame("frame2")
		_ = d.Decode(&ack)
	case "quiet":
		var ack any
		_ = d.Decode(&ack)
	case "flood":
		fmt.Fprintln(os.Stdout, strings.Repeat("x", MaxFrameBytes+100))
	case "stderrflood":
		fmt.Fprintln(os.Stderr, strings.Repeat("x", MaxLogBytes+100))
		var ack any
		_ = d.Decode(&ack)
	case "sleep":
		time.Sleep(10 * time.Second)
	}
	os.Exit(0)
}

func TestPollAcceptsAfterSuccessfulExit(t *testing.T) {
	r, c := fixture(t, "poll")
	c.State, c.Cursor = json.RawMessage(`{"version":2}`), json.RawMessage(`"cursor2"`)
	count := 0
	err := r.Run(context.Background(), c, func(f sentinel.Frame) error {
		count++
		if string(f.State) != `{"version":2}` || string(f.Cursor) != `"cursor2"` {
			t.Errorf("boot state not delivered: %#v", f)
		}
		return nil
	})
	if err != nil || count != 1 {
		t.Fatalf("poll: count=%d err=%v", count, err)
	}
}

func TestPollFailureAndMultipleFramesNeverAccept(t *testing.T) {
	for _, mode := range []string{"pollfail", "multipoll", "flood", "stderrflood"} {
		t.Run(mode, func(t *testing.T) {
			r, c := fixture(t, mode)
			count := 0
			err := r.Run(context.Background(), c, func(sentinel.Frame) error { count++; return nil })
			if err == nil || count != 0 {
				t.Fatalf("invalid poll accepted: count=%d err=%v", count, err)
			}
		})
	}
}

func TestStreamAcknowledgesOnlyAfterDurableAcceptance(t *testing.T) {
	r, c := fixture(t, "stream")
	c.Mode = "stream"
	entered, release, ack := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	r.OnLog = func(s string) {
		if strings.Contains(s, "ACK-SEEN") {
			select {
			case ack <- struct{}{}:
			default:
			}
		}
	}
	stop := errors.New("dry run complete")
	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- r.Run(context.Background(), c, func(f sentinel.Frame) error {
			if calls.Add(1) == 1 {
				close(entered)
				<-release
				return nil
			}
			return stop
		})
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not produce frame")
	}
	select {
	case <-ack:
		t.Error("ACK escaped before durable callback completed")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, stop) || calls.Load() != 2 {
			t.Fatalf("stream result: %v calls=%d", err, calls.Load())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("callback failure did not stop script")
	}
	select {
	case <-ack:
	default:
		t.Fatal("durably accepted first frame was never acknowledged")
	}
}

func TestQuietStreamWaitsForParentCancellation(t *testing.T) {
	r, c := fixture(t, "quiet")
	c.Mode, c.Timeout = "stream", time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := r.Run(ctx, c, func(sentinel.Frame) error { t.Error("quiet source accepted a frame"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("quiet stream cancellation: %v", err)
	}
}

func TestPollDeadline(t *testing.T) {
	r, c := fixture(t, "sleep")
	c.Timeout = time.Second
	started := time.Now()
	if err := r.Run(context.Background(), c, func(sentinel.Frame) error { t.Error("timed out poll accepted"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("poll deadline: %v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("poll was not promptly cancelled")
	}
}

func TestInvalidProtocolAndResourceGrants(t *testing.T) {
	for _, raw := range []string{`{"v":1,"id":"x","events":[],"unknown":true}`, `{"v":2,"id":"x","events":[]}`, `{"v":1,"id":"x","events":[]} {}`, `{"v":1,"id":"x","events":[{"key":"","summary":"bad"}]}`} {
		if _, err := decodeFrame([]byte(raw)); err == nil {
			t.Errorf("accepted malformed frame: %s", raw)
		}
	}
	_, c := fixture(t, "poll")
	c.MemoryMiB = 4096
	if _, err := normalize(c); err == nil {
		t.Fatal("accepted excessive memory grant")
	}
	c.MemoryMiB, c.Command = 0, []string{"python3", "source.py"}
	if _, err := normalize(c); err == nil {
		t.Fatal("accepted unpinned executable")
	}
}

func TestResourceFailureCancelsScript(t *testing.T) {
	r, c := fixture(t, "quiet")
	c.Mode = "stream"
	want := errors.New("synthetic resource limit")
	r.monitor = func(context.Context, int, Config) error { return want }
	if err := r.Run(context.Background(), c, func(sentinel.Frame) error { return nil }); !errors.Is(err, want) {
		t.Fatalf("resource failure not reported: %v", err)
	}
}
