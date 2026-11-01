package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/sentinelexec"
	"github.com/yosukeno/pogo/sentinelsdk"
	"github.com/yosukeno/pogo/tools"
)

func backgroundScriptSession(t *testing.T, h *harness) *Session {
	t.Helper()
	skillID := ""
	for _, sk := range h.mgr.Skills() {
		if sk.Name == "background-tasks" {
			skillID = skillIDOf(sk)
		}
	}
	if skillID == "" {
		t.Fatal("trusted background-tasks Skill is unavailable")
	}
	a := postStudioAgent(t, h, studioAgentCapsBody("Background setup", "ready", map[string]any{
		"instructions":        "Use the permitted background task entry.",
		"skills":              []string{skillID},
		"tools":               []string{tools.BackgroundTaskName},
		"skill_tool_bindings": []string{"background-tasks"},
	}), 201)
	return h.session(startStudioConversation(t, h, a.ID))
}

func TestBackgroundTaskScriptPrepareAndCheckNeedOnlyItsNativeGrant(t *testing.T) {
	h, calls := newSentinelHarness(t, nil)
	s := backgroundScriptSession(t, h)
	backend := h.mgr.backgroundTaskBackend(s)
	script := "watchers/skill-source/main.py"
	prepared := backgroundTestRequest(t, backend, map[string]any{"action": "prepare", "source": "sentinel", "script_path": script, "mode": "poll"})
	if prepared.Registered || prepared.Enabled || prepared.Status != "prepared" || prepared.Verification != "not_run" {
		t.Fatalf("preparation looked like execution or registration: %+v", prepared)
	}
	sdkPath := filepath.Join(s.cwd, "watchers/skill-source", sentinelsdk.FileName)
	sdk, err := os.ReadFile(sdkPath)
	if err != nil || !bytes.Equal(sdk, sentinelsdk.Source()) {
		t.Fatal("preparation did not use the trusted shared SDK", err)
	}
	entry := filepath.Join(s.cwd, filepath.FromSlash(script))
	if err := os.WriteFile(entry, []byte("# source-owned business implementation\n"), 0600); err != nil {
		t.Fatal(err)
	}
	again := backgroundTestRequest(t, backend, map[string]any{"action": "prepare", "source": "sentinel", "script_path": script, "mode": "poll"})
	data, err := os.ReadFile(entry)
	if err != nil || !bytes.Contains(data, []byte("source-owned")) || again.Result.(map[string]any)["entry_created"] != false {
		t.Fatal("repeated preparation overwrote business code", err)
	}
	checks := 0
	h.mgr.sentinels.runner = func(ctx context.Context, cfg sentinelexec.Config, accept func(sentinel.Frame) error) error {
		checks++
		if cfg.Workspace != s.cwd || cfg.Mode != "poll" || cfg.Network || cfg.Timeout <= 0 || cfg.Timeout > 30*time.Second {
			t.Fatalf("wrong check execution scope: %+v", cfg)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("check is not bounded")
		}
		return accept(sentinel.Frame{V: 1, ID: "preview", Events: []sentinel.InputEvent{{Key: "source:1", Summary: "Source observation"}}})
	}
	checked := backgroundTestRequest(t, backend, map[string]any{"action": "check", "source": "sentinel", "script_path": script, "mode": "poll", "timeout_seconds": 2})
	view := checked.Result.(map[string]any)
	if checked.Registered || checked.Enabled || checked.Status != "checked" || checked.Verification != "protocol_frame" || view["event_count"] != 1 || view["protocol_frame_verified"] != true || view["script_executed"] != true || view["events_enqueued"] != false || view["cursor_committed"] != false || view["agent_woken"] != false || view["business_conditions_verified"] != false || view["stream_ack_cycle_verified"] != false || checks != 1 {
		t.Fatalf("misleading script-check receipt: %+v", checked)
	}
	// The new entry has its own explicit capability; it does not grant the
	// separately configurable compatibility tool to this Agent.
	_, err = (&tools.WatchTest{Backend: h.mgr.sentinelBackend(s)}).Execute(context.Background(), json.RawMessage(studioBody(map[string]any{"script_path": script, "mode": "poll"})))
	if err == nil || checks != 1 {
		t.Fatal("background grant implicitly enabled the legacy check", err)
	}
	sentinelSDKAssertNoWatching(t, h, calls)
}

func TestBackgroundTaskSavedScriptCheckPreservesAcceptedStateAndRejectsForeignAccess(t *testing.T) {
	h, calls := newSentinelHarness(t, nil)
	s := backgroundScriptSession(t, h)
	script := sentinelTestBundle(t, s)
	backend := h.mgr.backgroundTaskBackend(s)
	created := backgroundTestRequest(t, backend, map[string]any{"action": "create", "source": "sentinel", "title": "Saved source", "task": "Investigate new source records", "script_path": script, "mode": "poll"})
	db, err := h.mgr.sentinels.database(true)
	if err != nil {
		t.Fatal(err)
	}
	w, err := db.Get(created.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	w, err = db.SetEnabled(w.Spec.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Accept(w.Spec.ID, w.Spec.Generation, sentinel.Frame{V: 1, ID: "accepted-before-check", Events: []sentinel.InputEvent{{Key: "before", Summary: "Already accepted"}}, State: json.RawMessage(`{"accepted":7}`), Cursor: json.RawMessage(`{"sequence":7}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.SetEnabled(w.Spec.ID, false); err != nil {
		t.Fatal(err)
	}
	before, err := db.Get(w.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	h.mgr.sentinels.runner = func(_ context.Context, cfg sentinelexec.Config, accept func(sentinel.Frame) error) error {
		checks++
		if cfg.ScriptDir != before.Spec.ScriptDir || !bytes.Equal(cfg.State, before.State) || !bytes.Equal(cfg.Cursor, before.Cursor) {
			t.Fatal("saved check did not use the sealed source and accepted checkpoint")
		}
		return accept(sentinel.Frame{V: 1, ID: "proposed-only", Events: []sentinel.InputEvent{{Key: "next", Summary: "Do not commit"}}, State: json.RawMessage(`{"accepted":8}`), Cursor: json.RawMessage(`{"sequence":8}`)})
	}
	checked := backgroundTestRequest(t, backend, map[string]any{"action": "check", "source": "sentinel", "task_id": created.TaskID})
	if checked.Status != "checked" || checked.Registered || checked.Enabled || checked.TaskID != created.TaskID || checked.SourceID != created.SourceID || checks != 1 {
		t.Fatal("saved check changed task activation or did not run", checked)
	}
	after, err := db.Get(w.Spec.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("script check changed the saved definition, runtime or accepted state", err)
	}
	if batches, err := db.ListBatches(w.Spec.ID, 10); err != nil || len(batches) != 0 {
		t.Fatal("a check admitted a background event batch", batches, err)
	}
	// Foreign Agents cannot use a known task ID to inspect its script/state.
	other := backgroundScriptSession(t, h)
	_, err = h.mgr.backgroundTaskBackend(other).Task(context.Background(), json.RawMessage(studioBody(map[string]any{"action": "check", "source": "sentinel", "task_id": created.TaskID})))
	if !errors.Is(err, sentinel.ErrNotFound) || checks != 1 {
		t.Fatal("foreign Agent inspected a saved source", err)
	}
	for _, body := range []map[string]any{
		{"action": "check", "source": "sentinel", "task_id": created.TaskID, "script_path": script},
		{"action": "check", "source": "sentinel", "task_id": created.TaskID, "network": false},
		{"action": "check", "source": "schedule", "task_id": created.TaskID},
	} {
		if _, err = backend.Task(context.Background(), json.RawMessage(studioBody(body))); err == nil || checks != 1 {
			t.Fatal("invalid check reached the script runner", body, err)
		}
	}
	a, _ := h.mgr.studio.get(s.studioID)
	patchStudioPolicy(t, h, a, map[string]any{"skills": []string{}, "tools": []string{tools.WatchTestName}})
	if _, err = backend.Task(context.Background(), json.RawMessage(studioBody(map[string]any{"action": "check", "source": "sentinel", "task_id": created.TaskID}))); err == nil || checks != 1 {
		t.Fatal("captured background capability survived revocation", err)
	}
	if sentinelModelCalls(calls) != 0 {
		t.Fatal("a preview woke a model")
	}
}

func TestBackgroundTaskScriptCheckQuietStreamIsExplicitlyUnverified(t *testing.T) {
	h, calls := newSentinelHarness(t, nil)
	s := backgroundScriptSession(t, h)
	script := sentinelTestBundle(t, s)
	h.mgr.sentinels.runner = func(ctx context.Context, _ sentinelexec.Config, _ func(sentinel.Frame) error) error {
		<-ctx.Done()
		return ctx.Err()
	}
	r := backgroundTestRequest(t, h.mgr.backgroundTaskBackend(s), map[string]any{"action": "check", "source": "sentinel", "script_path": script, "mode": "stream", "timeout_seconds": 1})
	view := r.Result.(map[string]any)
	if r.Registered || r.Enabled || r.Status != "unverified" || r.Verification != "unverified" || view["protocol_frame_verified"] != false || view["script_execution_attempted"] != true || view["business_conditions_verified"] != false || view["stream_ack_cycle_verified"] != false || view["agent_woken"] != false || !strings.Contains(view["reason"].(string), "without a valid protocol frame") {
		t.Fatalf("quiet stream was certified as ready: %+v", r)
	}
	if _, claimed := view["script_executed"]; claimed {
		t.Fatal("silence was presented as verified script execution")
	}
	sentinelSDKAssertNoWatching(t, h, calls)
}

func TestBackgroundTaskScriptCheckPreservesActualFailuresAndCancellation(t *testing.T) {
	for _, mode := range []string{"poll", "stream"} {
		t.Run(mode, func(t *testing.T) {
			h, calls := newSentinelHarness(t, nil)
			s := backgroundScriptSession(t, h)
			script := sentinelTestBundle(t, s)
			backend := h.mgr.backgroundTaskBackend(s)
			boom := errors.New("business source script exited unsuccessfully")
			h.mgr.sentinels.runner = func(context.Context, sentinelexec.Config, func(sentinel.Frame) error) error { return boom }
			r, err := backend.Task(context.Background(), json.RawMessage(studioBody(map[string]any{"action": "check", "source": "sentinel", "script_path": script, "mode": mode})))
			if err != boom || r.Result != nil {
				t.Fatal("actual script failure became an unverified/success receipt", r, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err = backend.Task(ctx, json.RawMessage(studioBody(map[string]any{"action": "check", "source": "sentinel", "script_path": script, "mode": mode}))); !errors.Is(err, context.Canceled) {
				t.Fatal("parent cancellation was swallowed", err)
			}
			sentinelSDKAssertNoWatching(t, h, calls)
		})
	}
}
