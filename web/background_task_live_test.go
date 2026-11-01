package web

// Opt-in live-provider coverage for creation and foreground handoff only. This
// test does not wait for a two-minute trigger or a ten-minute deadline, and it
// does not claim that future task execution or expiry was tested here.
import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/tools"
)

const backgroundTaskLiveCreationPrompt = "新建一个测试目录。创建定时任务，每2分钟统计一次这个目录的文件数量，把结果保存在Pogo中，10分钟后停止。"

type backgroundTaskLivePending struct {
	Name      string    `json:"name"`
	StartedAt time.Time `json:"started_at"`
}

// These are test-execution safeguards, not production command filters. A
// misplaced ten-minute wait is cancelled promptly without matching a shell
// command or pretending that it completed. A triggered safeguard is a failure
// requiring review of the actual trace; it can never make this test pass.
type backgroundTaskLiveObserver struct {
	mu              sync.Mutex
	registeredAt    time.Time
	registration    map[string]any
	pending         map[string]backgroundTaskLivePending
	statusAfterSave int
	events          []map[string]any
}

func (o *backgroundTaskLiveObserver) observe(e Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	switch e.Type {
	case EvToolStart:
		o.pending[e.CallID] = backgroundTaskLivePending{Name: e.Name, StartedAt: time.Now()}
		if !o.registeredAt.IsZero() && (e.Name == tools.BackgroundTaskName || e.Name == tools.WatchName) {
			var args map[string]any
			_ = json.Unmarshal(e.Args, &args)
			if args["action"] == "status" || args["action"] == "list" {
				o.statusAfterSave++
			}
		}
		o.events = append(o.events, map[string]any{"type": e.Type, "name": e.Name, "call_id": e.CallID, "args": e.Args, "ts": e.TS})
	case EvToolEnd:
		delete(o.pending, e.CallID)
		if !e.IsError && (e.Name == tools.BackgroundTaskName || e.Name == tools.WatchName) {
			// Only trusted native-tool completion supplies the handoff signal.
			// Model text saying a task exists is deliberately not enough.
			raw, _ := json.Marshal(e.Details)
			var details map[string]any
			_ = json.Unmarshal(raw, &details)
			if receipt, ok := details["background_handoff"].(map[string]any); ok && receipt["registered"] == true {
				if o.registeredAt.IsZero() {
					o.registeredAt = time.Now()
				}
				// Preserve the original handoff budget, but retain the latest
				// confirmed activation (a paused create may precede resume).
				o.registration = receipt
			}
		}
		o.events = append(o.events, map[string]any{"type": e.Type, "name": e.Name, "call_id": e.CallID, "is_error": e.IsError, "details": e.Details, "ts": e.TS})
	}
}

func (o *backgroundTaskLiveObserver) safeguard(now time.Time) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.registeredAt.IsZero() {
		return ""
	}
	if now.Sub(o.registeredAt) > time.Minute {
		return "foreground did not hand off within 60 seconds of trusted registration; classify the captured provider and tool trace before attributing the cause"
	}
	if o.statusAfterSave > 1 {
		return "foreground repeatedly queried background status after registration instead of handing off"
	}
	for _, pending := range o.pending {
		if now.Sub(pending.StartedAt) > 2*time.Second {
			return fmt.Sprintf("post-registration tool %s exceeded the two-second foreground safeguard; inspect the captured operation instead of waiting for the whole background lifetime", pending.Name)
		}
	}
	return ""
}

func (o *backgroundTaskLiveObserver) result() map[string]any {
	o.mu.Lock()
	defer o.mu.Unlock()
	value := map[string]any{"registered_at": o.registeredAt, "registration": o.registration, "pending": o.pending, "status_queries_after_registration": o.statusAfterSave, "events": o.events}
	// Failure evidence must also be race-free if a cancelled operation is slow
	// to drain. Never return the observer's mutable pending map to the encoder.
	raw, _ := json.Marshal(value)
	var copy map[string]any
	_ = json.Unmarshal(raw, &copy)
	return copy
}

func TestBackgroundTaskObserverRetainsActivationWithoutExtendingBudget(t *testing.T) {
	o := &backgroundTaskLiveObserver{pending: make(map[string]backgroundTaskLivePending)}
	for i, enabled := range []bool{false, true} {
		first := o.registeredAt
		o.observe(Event{Type: EvToolEnd, Name: tools.BackgroundTaskName, Details: map[string]any{"background_handoff": map[string]any{"registered": true, "enabled": enabled, "verification": "registration_only"}}})
		if o.registration["enabled"] != enabled || i > 0 && !o.registeredAt.Equal(first) {
			t.Fatal("paused registration followed by activation lost the latest receipt or reset its handoff budget")
		}
	}
}

func TestBackgroundTaskLiveCreationHandoff(t *testing.T) {
	// newSentinelLiveHarness owns the opt-in/real-provider boundary. Set the
	// schedule directory first so not even a constructor can reach host jobs.
	dir := scheduleEnv(t)
	h := newSentinelLiveHarness(t, sentinelLiveModel())
	binding, ok := h.mgr.studio.binding(h.creator.ID)
	if !ok {
		t.Fatal("isolated creator has no trusted Agent binding")
	}
	input := binding.Agent.studioInput
	selected := slices.Clone(*input.Tools)
	// Include the ordinary planning/read surfaces present in the reported
	// conversation, rather than proving handoff only with a tool-minimal Agent.
	selected = append(selected, tools.BackgroundTaskName, "todo", "read", "ls", "find", "grep", "tool_result")
	input.Tools = &selected
	if _, err := h.mgr.studio.put(binding.Agent.ID, input, binding.Agent.Revision); err != nil {
		t.Fatalf("enable isolated background-task capability: %v", err)
	}
	if _, err := h.creator.SyncStudioToolPolicy(); err != nil {
		t.Fatalf("sync isolated background-task capability: %v", err)
	}
	if !slices.Contains(h.creator.agent.NativeToolNames(), tools.BackgroundTaskName) {
		t.Fatal("the actual model-facing registry does not offer background_task")
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
		t.Fatalf("start real-model creation: active=%t err=%v", active != nil, err)
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
			interruption = "bounded live-model creation did not complete: " + ctx.Err().Error()
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
	var extraSentinels []sentinel.Watch
	if h.mgr.sentinels != nil {
		if db, err := h.mgr.sentinels.database(false); err != nil {
			t.Errorf("inspect isolated sentinel store: %v", err)
		} else if db != nil {
			extraSentinels, err = db.ListAll()
			if err != nil {
				t.Errorf("inspect extra sentinel definitions: %v", err)
			}
		}
	}
	snapshot := h.creator.Hub().Snapshot()
	h.creator.mu.Lock()
	creatorHandleActive := h.creator.active != nil
	h.creator.mu.Unlock()
	result := map[string]any{
		"scope":  "real-model foreground creation, trusted registration, timing and prompt return only; future execution/expiry requires separate deterministic acceptance",
		"prompt": backgroundTaskLiveCreationPrompt, "run_id": id, "elapsed_ms": time.Since(started).Milliseconds(),
		"ended": receipt.ended, "end_reason": receipt.endReason, "error": receipt.err, "summary": receipt.summary,
		"creator_active": snapshot.Run.Active, "creator_run_handle_active": creatorHandleActive,
		"interruption": interruption, "handoff": observer.result(),
		"saved_jobs": jobs, "extra_sentinel_definitions": extraSentinels,
		"model_tool_calls": h.capture.ToolCalls(), "semantic_review": "not_assessed",
		"future_execution_tested": false, "expiry_reached_tested": false, "production_data_used": false,
	}
	if storeErr != nil {
		result["store_error"] = storeErr.Error()
	}
	h.evidence(t, "background-task-creation-result", result)
	if interruption != "" {
		t.Fatalf("live creation/handoff was not accepted: %s", interruption)
	}
	if active.persistErr != nil {
		t.Fatalf("creation transcript persistence: %v", active.persistErr)
	}
	if !receipt.ended || receipt.endReason != "completed" || receipt.err != "" || snapshot.Run.Active || creatorHandleActive {
		t.Fatalf("foreground did not complete after registration: ended=%t reason=%s active=%t handle=%t", receipt.ended, receipt.endReason, snapshot.Run.Active, creatorHandleActive)
	}
	if storeErr != nil || len(jobs) != 1 {
		t.Fatalf("expected exactly one persisted schedule, jobs=%d err=%v", len(jobs), storeErr)
	}
	if len(extraSentinels) != 0 {
		t.Fatalf("one periodic task also registered %d unnecessary sentinel definitions", len(extraSentinels))
	}
	job := jobs[0]
	if job.AgentID != h.creator.studioID || !reflect.DeepEqual(job.Owner, h.creator.owner) || job.Workdir != h.creator.cwd {
		t.Fatal("background task escaped the trusted Agent, account or workspace binding")
	}
	if job.Kind != schedule.KindEvery || job.EverySec != 120 || !job.Enabled || !job.LastRunAt.IsZero() {
		t.Fatalf("wrong saved timer or a premature execution: kind=%s every=%d enabled=%t ran=%t", job.Kind, job.EverySec, job.Enabled, !job.LastRunAt.IsZero())
	}
	expires, err := time.Parse(time.RFC3339, job.ExpiresAt)
	if err != nil || expires.Sub(job.CreatedAt) < 599*time.Second || expires.Sub(job.CreatedAt) > 601*time.Second {
		t.Fatalf("host deadline does not implement ten minutes: expiry=%q created=%s err=%v", job.ExpiresAt, job.CreatedAt, err)
	}
	if job.ScheduleFrom == nil || !job.ScheduleFrom.Equal(job.CreatedAt) {
		t.Fatal("initial interval is not anchored at host registration time")
	}
	next := nextFire(job, job.CreatedAt)
	if next == nil || !next.Equal(job.CreatedAt.Add(120*time.Second)) {
		t.Fatalf("first execution is not one interval after creation: %v", next)
	}
	if nextFire(job, expires) != nil {
		t.Fatal("a new execution is still scheduled at the exclusive expiry boundary")
	}
	createdThroughNativeTool := false
	for _, call := range h.capture.ToolCalls() {
		if call.Name != tools.BackgroundTaskName {
			continue
		}
		var args map[string]any
		_ = json.Unmarshal(call.Input, &args)
		if args["action"] == "create" && args["source"] == "schedule" {
			createdThroughNativeTool = true
		}
	}
	if !createdThroughNativeTool {
		t.Fatal("saved schedule was not created through the host-bound background_task tool")
	}
	observer.mu.Lock()
	registered := !observer.registeredAt.IsZero() && observer.registration["verification"] == "registration_only" &&
		observer.registration["task_id"] == "schedule:"+job.ID && observer.registration["enabled"] == true
	observer.mu.Unlock()
	if !registered {
		t.Fatal("no trusted registration_only handoff receipt was observed")
	}
}
