package web

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/tools"
)

func approvalVisibilityRequest(t *testing.T, events <-chan Event) Event {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.Type == EvGateRequest {
				return event
			}
		case <-timer.C:
			t.Fatal("approval was not exposed to the event stream")
		}
	}
}

// Every reviewed host entry point must produce a usable gate even when there
// is no model tool_use message yet (browser arm, Office and direct tool calls).
func TestApprovalVisibilityReviewedEntryPointMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, tool, callID, origin, raw string
		mode                            Mode
	}{
		{"shell", "bash", "own-command", "", `{"command":"printf hello"}`, ModeStandard},
		{"dangerous shell", "bash", "own-danger", "", `{"command":"rm -rf ./generated"}`, ModeStandard},
		{"browser task", BrowserArmRequest, "", "", `{"action":"navigate","url":"https://example.invalid/"}`, ModeStandard},
		{"untrusted MCP", "untrusted_mcp_update", "mcp-call", "", `{"title":"change"}`, ModeStandard},
		{"manifest Skill", "custom_skill_operation", "skill-call", "", `{"operation":"generate"}`, ModeStandard},
		{"file write strict", "write", "write-call", "", `{"path":"memo.txt","content":"memo"}`, ModeStrict},
		{"file edit strict", "edit", "edit-call", "", `{"path":"memo.txt","old_string":"old","new_string":"new"}`, ModeStrict},
		{"shortcut delete", tools.ShortcutDeleteName, "delete-call", "", `{"ids":["shortcut"],"expected_revision":1,"request_id":"review-delete"}`, ModeStandard},
		{"shortcut merge apply", tools.ShortcutMergeName, "merge-call", "", `{"apply":true,"plan_id":"plan","expected_revision":1,"request_id":"review-merge"}`, ModeStandard},
		{"Skill save strict", tools.SkillSaveName, "save-skill", "", `{"request_id":"review-skill","files":[{"path":"SKILL.md","content":"---\nname: memo\ndescription: Review a memo\n---\nSummarize the supplied memo."}]}`, ModeStrict},
		{"Office strict", tools.OfficeToolName, "office-edit", "", `{"action":"close","document_session_id":"document-session"}`, ModeStrict},
		{"Office Python", "bash", "office_script_captured", "", `{"command":"exec python3 -I -B /staging/script.py"}`, ModeStandard},
		{"sentinel lifecycle", tools.WatchName, "watch-pause", "", `{"action":"pause","watch_id":"w_1"}`, ModeStandard},
		{"sentinel script test", tools.WatchTestName, "watch-test", "", `{"script_path":"sentinels/example/main.py","mode":"poll"}`, ModeStandard},
		{"background child", "bash", "child:command", "child", `{"command":"printf child"}`, ModeStandard},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub, policy := NewHub(), NewPolicy()
			policy.Set(tc.mode, 0)
			gate := NewWebGate(hub, policy, time.Second*5)
			hub.Publish(Event{Type: EvRunStart, RunID: "approval-run"})
			_, events, unsubscribe := hub.Subscribe(hub.Seq())
			defer unsubscribe()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan agent.GateDecision, 1)
			go func() {
				done <- gate.Review(ctx, agent.GateRequest{CallID: tc.callID, ToolName: tc.tool, Args: json.RawMessage(tc.raw), Origin: tc.origin})
			}()
			request := approvalVisibilityRequest(t, events)
			if request.GateID == "" || request.Name != tc.tool || request.CallID != tc.callID || request.Origin != tc.origin || request.Deadline <= time.Now().UnixMilli() || string(request.Args) != tc.raw {
				t.Fatalf("incomplete approval request: %+v", request)
			}
			if tc.name == "dangerous shell" && len(request.Danger) == 0 {
				t.Fatal("danger annotation was lost")
			}
			snapshot := hub.Snapshot()
			if len(snapshot.Live.PendingGates) != 1 || snapshot.Live.PendingGates[0].GateID != request.GateID || snapshot.Live.PendingGates[0].Origin != tc.origin {
				t.Fatalf("pending approval disappeared from reconnect state: %+v", snapshot.Live)
			}
			backlog, _, stopReconnect := hub.Subscribe(-1)
			stopReconnect()
			if len(backlog) != 1 || backlog[0].Snapshot == nil || len(backlog[0].Snapshot.Live.PendingGates) != 1 {
				t.Fatal("new subscriber cannot recover the approval")
			}
			if encoded, err := json.Marshal(backlog[0]); err != nil || !json.Valid(encoded) {
				t.Fatalf("approval snapshot cannot reach the frontend: %v", err)
			}
			if err := gate.Decide(request.GateID, Verdict{Allow: true}); err != nil {
				t.Fatal(err)
			}
			select {
			case decision := <-done:
				if !decision.Allow {
					t.Fatalf("approved operation remained blocked: %+v", decision)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("decision did not release the waiting operation")
			}
			if pending := hub.Snapshot().Live.PendingGates; len(pending) != 0 {
				t.Fatalf("resolved approval remained visible: %+v", pending)
			}
		})
	}
}

func TestApprovalVisibilityBackgroundGateSurvivesRunBoundaries(t *testing.T) {
	hub, policy := NewHub(), NewPolicy()
	gate := NewWebGate(hub, policy, time.Second*5)
	hub.Publish(Event{Type: EvRunStart, RunID: "first"})
	_, events, unsubscribe := hub.Subscribe(hub.Seq())
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan agent.GateDecision, 1)
	go func() {
		done <- gate.Review(ctx, agent.GateRequest{CallID: "retained-child:command", ToolName: "bash", Origin: "retained-child", Args: json.RawMessage(`{"command":"printf retained"}`)})
	}()
	request := approvalVisibilityRequest(t, events)
	for _, event := range []Event{{Type: EvRunEnd}, {Type: EvRunStart, RunID: "second"}} {
		hub.Publish(event)
		pending := hub.Snapshot().Live.PendingGates
		if len(pending) != 1 || pending[0].GateID != request.GateID || pending[0].Origin != "retained-child" {
			t.Fatalf("%s hid a live child approval while its operation still waits: %+v", event.Type, pending)
		}
	}
	if err := gate.Decide(request.GateID, Verdict{Allow: false, Reason: "decline delegated operation"}); err != nil {
		t.Fatal(err)
	}
	select {
	case decision := <-done:
		if decision.Allow || decision.Reason != "decline delegated operation" {
			t.Fatalf("child decision was lost: %+v", decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("child did not receive its decision across runs")
	}
}

func TestApprovalVisibilityQueuedCallHonorsNewAutoMode(t *testing.T) {
	hub, policy := NewHub(), NewPolicy()
	checked := make(chan struct{})
	policy.MarkReadOnly("queued_manifest_tool")
	policy.sandboxActive = func() bool { close(checked); return false }
	gate := NewWebGate(hub, policy, time.Second*5)
	gate.serialMain.Lock()
	locked := true
	defer func() {
		if locked {
			gate.serialMain.Unlock()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan agent.GateDecision, 1)
	go func() {
		done <- gate.Review(ctx, agent.GateRequest{CallID: "queued", ToolName: "queued_manifest_tool", Args: json.RawMessage(`{}`)})
	}()
	select {
	case <-checked:
	case <-time.After(2 * time.Second):
		t.Fatal("queued operation did not reach its initial permission check")
	}
	policy.Set(ModeAuto, 0)
	gate.ApproveAll()
	gate.serialMain.Unlock()
	locked = false
	select {
	case decision := <-done:
		if !decision.Allow {
			t.Fatalf("new auto-mode grant was ignored after queueing: %+v", decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued operation opened a new invisible approval after switching to full access")
	}
	if pending := hub.Snapshot().Live.PendingGates; len(pending) != 0 {
		t.Fatalf("auto-granted queued call left a stale pending card: %+v", pending)
	}
}

func TestApprovalVisibilityFrozenEditorSurvivesReconnectAndOriginalDeadline(t *testing.T) {
	hub, policy := NewHub(), NewPolicy()
	gate := NewWebGate(hub, policy, 300*time.Millisecond)
	hub.Publish(Event{Type: EvRunStart, RunID: "edit-approval"})
	_, events, unsubscribe := hub.Subscribe(hub.Seq())
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan agent.GateDecision, 1)
	go func() {
		done <- gate.Review(ctx, agent.GateRequest{CallID: "editable-command", ToolName: "bash", Args: json.RawMessage(`{"command":"printf original"}`)})
	}()
	request := approvalVisibilityRequest(t, events)
	if err := gate.Freeze(request.GateID); err != nil {
		t.Fatal(err)
	}
	waitDeadline := func(frozen bool) Event {
		t.Helper()
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		for {
			select {
			case event := <-events:
				if event.Type == EvGateDeadline && event.GateID == request.GateID && event.Frozen != nil && *event.Frozen == frozen {
					return event
				}
			case <-timer.C:
				t.Fatalf("frozen=%v state was not sent to the editing/reconnected frontend", frozen)
			}
		}
	}
	frozen := waitDeadline(true)
	if frozen.RemainingMS <= 0 {
		t.Fatal("paused approval lost its remaining duration")
	}
	backlog, _, stopReconnect := hub.Subscribe(-1)
	stopReconnect()
	if len(backlog) != 1 || backlog[0].Snapshot == nil || len(backlog[0].Snapshot.Live.PendingGates) != 1 {
		t.Fatal("reconnected editor lost its approval")
	}
	paused := backlog[0].Snapshot.Live.PendingGates[0]
	if !paused.Frozen || paused.RemainingMS != frozen.RemainingMS {
		t.Fatalf("reconnect resumed or changed a paused approval: %+v", paused)
	}
	// The original wall-clock deadline must not make the editing UI stale while
	// the backend is intentionally waiting with its countdown paused.
	select {
	case result := <-done:
		t.Fatalf("frozen approval resolved without a decision: %+v", result)
	case <-time.After(350 * time.Millisecond):
	}
	if current := hub.Snapshot().Live.PendingGates; len(current) != 1 || !current[0].Frozen {
		t.Fatalf("paused gate disappeared after its old deadline: %+v", current)
	}
	if err := gate.Thaw(request.GateID); err != nil {
		t.Fatal(err)
	}
	resumed := waitDeadline(false)
	if resumed.Deadline <= time.Now().UnixMilli() {
		t.Fatalf("resuming editing did not rebase the actual deadline: %+v", resumed)
	}
	if err := gate.Decide(request.GateID, Verdict{Allow: false}); err != nil {
		t.Fatal(err)
	}
	select {
	case decision := <-done:
		if decision.Allow {
			t.Fatal("editing/freeze state granted permission without approval")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("resumed gate did not accept the user decision")
	}
}
