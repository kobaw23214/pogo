package web

// Compatibility acceptance for an existing Agent that explicitly authorizes
// watch, but has never granted the newer background_task capability. This is
// opt-in real-provider coverage; it neither widens that grant nor waits for the
// configured ten-minute lifetime.
import (
	"context"
	"encoding/json"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/tools"
)

func TestBackgroundTaskLiveWatchOnlyHandoff(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("activating a real watch requires the Linux execution boundary")
	}
	dir := scheduleEnv(t)
	h := newSentinelLiveHarness(t, sentinelLiveModel())
	binding, ok := h.mgr.studio.binding(h.creator.ID)
	if !ok {
		t.Fatal("isolated creator has no trusted Agent binding")
	}
	input := binding.Agent.studioInput
	selected := []string{"bash", "write", "edit", "watch", "watch_test", "watch_decide", "todo", "read", "ls", "find", "grep", "tool_result"}
	input.Tools = &selected
	if _, err := h.mgr.studio.put(binding.Agent.ID, input, binding.Agent.Revision); err != nil {
		t.Fatalf("set isolated existing-Agent tool selection: %v", err)
	}
	if _, err := h.creator.SyncStudioToolPolicy(); err != nil {
		t.Fatalf("sync isolated existing-Agent tool selection: %v", err)
	}
	names := h.creator.agent.NativeToolNames()
	if slices.Contains(names, tools.BackgroundTaskName) || !slices.Contains(names, tools.WatchName) || !slices.Contains(names, "todo") {
		t.Fatalf("watch-only compatibility selection was widened or lost: %v", names)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	receipt := &scheduleRunReceipt{}
	observer := &backgroundTaskLiveObserver{pending: make(map[string]backgroundTaskLivePending)}
	var active *activeRun
	started := time.Now()
	id, err := h.creator.startPreparedInput(backgroundTaskLiveCreationPrompt, nil, nil, "", "", mcpobserve.Metadata{}, "", ctx, nil, func(run *activeRun) {
		active = run
		run.observe = func(e Event) { receipt.observe(e); observer.observe(e) }
	})
	if err != nil || active == nil {
		t.Fatalf("start watch-only real-model creation: active=%t err=%v", active != nil, err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	interruption := ""
	finished := false
	for !finished {
		select {
		case <-active.done:
			finished = true
		case now := <-ticker.C:
			if reason := observer.safeguard(now); reason != "" {
				interruption = reason
				cancel()
				active.cancel()
				finished = true
			}
		case <-ctx.Done():
			interruption = "bounded watch-only creation did not complete: " + ctx.Err().Error()
			active.cancel()
			finished = true
		}
	}
	select {
	case <-active.done:
	case <-time.After(5 * time.Second):
		interruption += "; creator did not drain after cancellation"
	}

	jobs, storeErr := schedule.NewStore(dir).Load()
	var watches []sentinel.Watch
	var watchErr error
	if db, err := h.mgr.sentinels.database(false); err != nil {
		watchErr = err
	} else if db != nil {
		watches, watchErr = db.ListAll()
	}
	snapshot := h.creator.Hub().Snapshot()
	h.creator.mu.Lock()
	creatorHandleActive := h.creator.active != nil
	h.creator.mu.Unlock()
	result := map[string]any{
		"scope":  "real-model watch-only foreground setup, trusted registration and saved cadence/deadline only; future samples and reaching expiry are not exercised here",
		"prompt": backgroundTaskLiveCreationPrompt, "run_id": id, "elapsed_ms": time.Since(started).Milliseconds(),
		"selected_tools": selected, "background_task_granted": false,
		"ended": receipt.ended, "end_reason": receipt.endReason, "error": receipt.err, "summary": receipt.summary,
		"creator_active": snapshot.Run.Active, "creator_run_handle_active": creatorHandleActive,
		"interruption": interruption, "handoff": observer.result(),
		"saved_watches": watches, "extra_schedule_definitions": jobs,
		"model_tool_calls": h.capture.ToolCalls(), "semantic_review": "not_assessed",
		"future_execution_tested": false, "expiry_reached_tested": false, "production_data_used": false,
	}
	if storeErr != nil {
		result["schedule_store_error"] = storeErr.Error()
	}
	if watchErr != nil {
		result["watch_store_error"] = watchErr.Error()
	}
	h.evidence(t, "background-task-watch-only-result", result)
	if interruption != "" {
		t.Fatalf("live watch-only handoff was not accepted: %s", interruption)
	}
	if active.persistErr != nil {
		t.Fatalf("watch-only transcript persistence: %v", active.persistErr)
	}
	if !receipt.ended || receipt.endReason != "completed" || receipt.err != "" || snapshot.Run.Active || creatorHandleActive {
		t.Fatalf("foreground did not complete after watch registration: ended=%t reason=%s active=%t handle=%t", receipt.ended, receipt.endReason, snapshot.Run.Active, creatorHandleActive)
	}
	if storeErr != nil || len(jobs) != 0 {
		t.Fatalf("watch-only registration created an unauthorized extra schedule, jobs=%d err=%v", len(jobs), storeErr)
	}
	if watchErr != nil || len(watches) != 1 {
		t.Fatalf("expected one persisted watch, count=%d err=%v", len(watches), watchErr)
	}
	w := watches[0]
	if w.Spec.OwnerID != sessionResultOwnerKey(h.creator.owner) || w.Spec.AgentID != h.creator.studioID || w.Spec.Workspace != h.creator.cwd {
		t.Fatal("watch-only task escaped the trusted account, Agent or workspace binding")
	}
	if !w.Spec.Enabled || w.Spec.Mode != "poll" || w.Spec.IntervalSeconds != 120 {
		t.Fatalf("watch-only task did not save the requested activated cadence: %+v", w.Spec)
	}
	if lifetime := w.Spec.ExpiresAt - w.Spec.CreatedAt; lifetime < 599_000 || lifetime > 601_000 {
		t.Fatalf("watch-only task did not save a ten-minute host deadline: created=%d expires=%d", w.Spec.CreatedAt, w.Spec.ExpiresAt)
	}
	if w.Spec.MaxTurns <= 0 || w.Spec.MaxTurns > h.mgr.effectiveMaxTurns() {
		t.Fatal("watch-only task widened the host model turn budget")
	}
	createdThroughWatch := false
	for _, call := range h.capture.ToolCalls() {
		if call.Name == tools.BackgroundTaskName {
			t.Fatal("watch-only creator invoked the ungranted background_task tool")
		}
		if call.Name == tools.WatchName {
			var args tools.WatchRequest
			_ = json.Unmarshal(call.Input, &args)
			createdThroughWatch = createdThroughWatch || args.Action == "create"
		}
	}
	if !createdThroughWatch {
		t.Fatal("saved watch was not registered through the authorized native watch tool")
	}
	observer.mu.Lock()
	registered := !observer.registeredAt.IsZero() && observer.registration["verification"] == "registration_only" &&
		observer.registration["task_id"] == "sentinel:"+w.Spec.ID && observer.registration["enabled"] == true
	pending := len(observer.pending)
	observer.mu.Unlock()
	if !registered || pending != 0 {
		t.Fatalf("trusted watch registration handoff missing or foreground tools still pending: registered=%t pending=%d", registered, pending)
	}
}
