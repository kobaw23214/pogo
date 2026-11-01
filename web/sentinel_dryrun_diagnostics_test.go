package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/sentinelexec"
	"github.com/yosukeno/pogo/tools"
)

func sentinelDryRunDiagnosticBackend(t *testing.T, mode string) (*backgroundTaskBackend, json.RawMessage) {
	t.Helper()
	h, _ := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	script := sentinelTestBundle(t, s)
	return h.mgr.backgroundTaskBackend(s).(*backgroundTaskBackend), json.RawMessage(studioBody(map[string]any{"action": "check", "source": "sentinel", "script_path": script, "mode": mode, "timeout_seconds": 1}))
}

func sentinelDryRun(b *backgroundTaskBackend, ctx context.Context, raw json.RawMessage) (any, error) {
	receipt, err := b.Task(ctx, raw)
	if err != nil {
		return nil, err
	}
	return receipt.Result, nil
}

func TestSentinelDryRunQuietStreamDeadlineIsUnverified(t *testing.T) {
	b, raw := sentinelDryRunDiagnosticBackend(t, "stream")
	b.m.sentinels.runner = func(ctx context.Context, _ sentinelexec.Config, _ func(sentinel.Frame) error) error {
		<-ctx.Done()
		return ctx.Err()
	}
	receipt, err := b.Task(context.Background(), raw)
	if err != nil || receipt.Status != "unverified" || receipt.Verification != "unverified" || receipt.Registered || receipt.Enabled {
		t.Fatalf("quiet deadline was not classified as unverified: receipt=%+v err=%v", receipt, err)
	}
	result, ok := receipt.Result.(map[string]any)
	if !ok || result["protocol_frame_verified"] != false || result["script_execution_attempted"] != true {
		t.Fatalf("quiet deadline produced an invalid receipt: %+v", receipt.Result)
	}
	reason, _ := result["reason"].(string)
	for _, text := range []string{"quiet source does not mean", "events:[] checkpoint", "accepted ACK", "explicitly authorized test event", "Do not claim this test passed"} {
		if !strings.Contains(reason, text) {
			t.Errorf("missing diagnostic %q: %v", text, reason)
		}
	}
}

func TestSentinelDryRunPreservesParentCancellationAndDeadline(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline=%v", deadline), func(t *testing.T) {
			b, raw := sentinelDryRunDiagnosticBackend(t, "stream")
			var parent context.Context
			var cancel context.CancelFunc
			if deadline {
				parent, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			} else {
				parent, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			var original error
			b.m.sentinels.runner = func(ctx context.Context, _ sentinelexec.Config, _ func(sentinel.Frame) error) error {
				if !deadline {
					cancel()
				}
				<-ctx.Done()
				original = fmt.Errorf("source interrupted: %w", ctx.Err())
				return original
			}
			result, err := sentinelDryRun(b, parent, raw)
			if result != nil || err != original || errors.Is(err, errSentinelTestNoFrame) {
				t.Fatalf("parent error changed: result=%v err=%v original=%v", result, err, original)
			}
		})
	}
}

func TestSentinelDryRunPreservesPollFailures(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprintf("timeout=%v", timeout), func(t *testing.T) {
			b, raw := sentinelDryRunDiagnosticBackend(t, "poll")
			original := errors.New("poll exited 7 before producing its required frame")
			b.m.sentinels.runner = func(ctx context.Context, _ sentinelexec.Config, _ func(sentinel.Frame) error) error {
				if timeout {
					<-ctx.Done()
					original = ctx.Err()
				}
				return original
			}
			result, err := sentinelDryRun(b, context.Background(), raw)
			if result != nil || err != original || errors.Is(err, errSentinelTestNoFrame) {
				t.Fatalf("poll failure changed: result=%v err=%v original=%v", result, err, original)
			}
		})
	}
}

func TestSentinelDryRunFirstQuietFrameSucceeds(t *testing.T) {
	b, raw := sentinelDryRunDiagnosticBackend(t, "stream")
	b.m.sentinels.runner = func(_ context.Context, _ sentinelexec.Config, accept func(sentinel.Frame) error) error {
		return accept(sentinel.Frame{V: 1, ID: "checkpoint-content-hash", Events: []sentinel.InputEvent{}, Cursor: json.RawMessage(`{"sequence":12}`)})
	}
	result, err := sentinelDryRun(b, context.Background(), raw)
	if err != nil || result == nil {
		t.Fatalf("valid quiet frame failed: result=%v err=%v", result, err)
	}
	out := result.(map[string]any)
	frame, ok := out["frame"].(*sentinel.Frame)
	if !ok || frame == nil || frame.ID != "checkpoint-content-hash" || len(frame.Events) != 0 || out["agent_woken"] != false || out["cursor_committed"] != false {
		t.Fatalf("invalid first-frame receipt: %#v", out)
	}
	if out["business_conditions_verified"] != false || out["stream_ack_cycle_verified"] != false {
		t.Fatalf("a first quiet frame must not certify source truth or an ACK cycle: %#v", out)
	}
}

func TestSentinelDryRunNoFrameExitNeverSucceeds(t *testing.T) {
	b, raw := sentinelDryRunDiagnosticBackend(t, "stream")
	b.m.sentinels.runner = func(context.Context, sentinelexec.Config, func(sentinel.Frame) error) error { return nil }
	result, err := sentinelDryRun(b, context.Background(), raw)
	if result != nil || err == nil || !strings.Contains(err.Error(), "without a valid protocol frame") {
		t.Fatalf("empty stream exit accepted: result=%v err=%v", result, err)
	}
}

func TestSentinelDryRunDescriptionsExplainQuietValidationAndFrameIdentity(t *testing.T) {
	for _, tool := range []tools.Tool{&tools.Watch{}, &tools.WatchTest{}} {
		for _, text := range []string{"valid frame", "events:[] checkpoint", "authorized test event", "complete immutable payload", "events, state or cursor", "event-to-quiet transition", "canonical JSON content hash of events, state and cursor (excluding the frame id)", "event key", "deduplication"} {
			if !strings.Contains(tool.Description(), text) {
				t.Errorf("%s description omits %q", tool.Name(), text)
			}
		}
	}
}
