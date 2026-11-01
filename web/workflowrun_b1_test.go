package web

// Wave B1 end-to-end tests: orchestrator dispatch and the human/approval
// state machine over the real HTTP surface.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/workflow"
)

func waitNodeStatus(t *testing.T, h *harness, wid, rid, nid, want string) workflow.RunRecord {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		rec := getWorkflowRun(t, h, wid, rid)
		if rec.Nodes[nid].Status == want {
			return rec
		}
		if workflow.IsTerminal(rec.Status) {
			t.Fatalf("run settled %q before %s reached %s: %+v", rec.Status, nid, want, rec.Nodes[nid])
		}
		if time.Now().After(deadline) {
			t.Fatalf("node %s never reached %s: %+v", nid, want, rec.Nodes[nid])
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func resumeNode(t *testing.T, h *harness, wid, rid, nid, body string, want int) {
	t.Helper()
	resp := h.do(http.MethodPost, fmt.Sprintf("/api/workflows/%s/runs/%s/nodes/%s/resume", wid, rid, nid), body)
	defer resp.Body.Close()
	if resp.StatusCode != want {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("resume %s: %d, want %d (%s)", nid, resp.StatusCode, want, msg)
	}
}

// --- orchestrator ------------------------------------------------------------

func TestWorkflowOrchestratorDispatches(t *testing.T) {
	var mu sync.Mutex
	var candIDs struct{ one, two string }
	orchTurns := 0
	stream := func(ctx context.Context, system, user string) (llm.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(system, "You are Orchestrator."):
			orchTurns++
			switch orchTurns {
			case 1:
				return toolTurn("dispatch_to_"+candIDs.one, `{"task":"subtask one"}`), nil
			case 2:
				return toolTurn("dispatch_to_"+candIDs.two, `{"task":"subtask two"}`), nil
			default:
				return textTurn("both dispatched"), nil
			}
		case strings.Contains(system, "You are Candidate One."):
			return textTurn("candidate one result"), nil
		default:
			return textTurn("candidate two result"), nil
		}
	}
	h := newWorkflowHarness(t, stream)
	orch := createStudioAgent(t, h, "Orchestrator", "delegate work", "ready")
	c1 := createStudioAgent(t, h, "Candidate One", "worker one", "ready")
	c2 := createStudioAgent(t, h, "Candidate Two", "worker two", "ready")
	mu.Lock()
	candIDs.one, candIDs.two = c1.ID, c2.ID
	mu.Unlock()

	body := map[string]any{
		"name": "orchestrated", "description": "d", "status": "ready",
		"nodes": []map[string]any{
			{"id": "lead", "agent": orch.ID, "task": "Delegate the analysis.", "orchestrate": map[string]any{
				"candidates": []map[string]any{{"agent": c1.ID, "hint": "deep analysis"}, {"agent": c2.ID, "hint": "wide sweep"}},
			}},
		},
	}
	wf := createWorkflow(t, h, body)
	rid := startWorkflowRun(t, h, wf.ID)
	rec := waitWorkflowRun(t, h, wf.ID, rid)
	if rec.Status != workflow.RunCompleted {
		t.Fatalf("run = %q (%q)", rec.Status, rec.Error)
	}
	lead := rec.Nodes["lead"]
	if lead.Status != workflow.NodeSuccess || lead.Summary != "both dispatched" {
		t.Fatalf("lead = %+v", lead)
	}
	if len(lead.Dispatches) != 2 {
		t.Fatalf("dispatches = %+v", lead.Dispatches)
	}
	byCand := map[string]workflow.DispatchRecord{}
	for _, d := range lead.Dispatches {
		byCand[d.Candidate] = d
		if d.Status != workflow.NodeSuccess || d.StartedAt == "" || d.EndedAt == "" {
			t.Fatalf("dispatch = %+v", d)
		}
	}
	if !strings.Contains(byCand[c1.ID].Summary, "candidate one result") || !strings.Contains(byCand[c2.ID].Summary, "candidate two result") {
		t.Fatalf("dispatch summaries = %+v", lead.Dispatches)
	}
}

// --- orchestrator budget, failure, validation -----------------------------------

func TestWorkflowOrchestratorBudgetAndFailure(t *testing.T) {
	var mu sync.Mutex
	var candID string
	var candCalls int
	stream := func(ctx context.Context, system, user string) (llm.Response, error) {
		if strings.Contains(system, "You are Orchestrator.") {
			mu.Lock()
			candCalls++
			n := candCalls
			mu.Unlock()
			if n <= 2 {
				return toolTurn("dispatch_to_"+candID, fmt.Sprintf(`{"task":"subtask %d"}`, n)), nil
			}
			return textTurn("done delegating"), nil
		}
		return textTurn("worker says hi"), nil
	}
	h := newWorkflowHarness(t, stream)
	orch := createStudioAgent(t, h, "Orchestrator", "delegate", "ready")
	cand := createStudioAgent(t, h, "Worker", "work", "ready")
	mu.Lock()
	candID = cand.ID
	mu.Unlock()
	body := map[string]any{
		"name": "budgeted", "description": "d", "status": "ready",
		"nodes": []map[string]any{
			{"id": "lead", "agent": orch.ID, "task": "Delegate twice.", "orchestrate": map[string]any{
				"candidates": []map[string]any{{"agent": cand.ID}}, "max_dispatches": 1,
			}},
		},
	}
	wf := createWorkflow(t, h, body)
	rid := startWorkflowRun(t, h, wf.ID)
	rec := waitWorkflowRun(t, h, wf.ID, rid)
	// The second dispatch is refused (budget 1), the model sees the error and
	// still finishes: the node succeeds with exactly one record.
	if rec.Status != workflow.RunCompleted || rec.Nodes["lead"].Status != workflow.NodeSuccess {
		t.Fatalf("run = %q lead = %+v", rec.Status, rec.Nodes["lead"])
	}
	if len(rec.Nodes["lead"].Dispatches) != 1 || rec.Nodes["lead"].Dispatches[0].Status != workflow.NodeSuccess {
		t.Fatalf("dispatches = %+v", rec.Nodes["lead"].Dispatches)
	}

	// A dispatch whose candidate went unavailable fails the delegation, not
	// the node: the model reads the error and carries on.
	updateStudioAgent(t, h, cand.ID, 1, "Worker", "work", "draft")
	mu.Lock()
	candCalls = 0
	mu.Unlock()
	rid2 := startWorkflowRun(t, h, wf.ID)
	rec2 := waitWorkflowRun(t, h, wf.ID, rid2)
	if rec2.Status != workflow.RunCompleted || rec2.Nodes["lead"].Status != workflow.NodeSuccess {
		t.Fatalf("run2 = %q (%q) lead = %+v", rec2.Status, rec2.Error, rec2.Nodes["lead"])
	}
	ds := rec2.Nodes["lead"].Dispatches
	if len(ds) != 1 || ds[0].Status != workflow.NodeFailed || !strings.Contains(ds[0].Error, "unavailable") {
		t.Fatalf("failed dispatch = %+v", ds)
	}
}

func TestWorkflowOrchestratorUnknownCandidateRejected(t *testing.T) {
	h := newWorkflowHarness(t, func(context.Context, string, string) (llm.Response, error) {
		return textTurn("ok"), nil
	})
	orch := createStudioAgent(t, h, "Orchestrator", "delegate", "ready")
	body := map[string]any{
		"name": "bad", "description": "d", "status": "ready",
		"nodes": []map[string]any{
			{"id": "lead", "agent": orch.ID, "task": "t", "orchestrate": map[string]any{
				"candidates": []map[string]any{{"agent": "ag_ghost"}},
			}},
		},
	}
	if code := studioStatus(t, h, http.MethodPost, "/api/manage/workflows", studioBody(body)); code != http.StatusBadRequest {
		t.Fatalf("unknown candidate: %d, want 400", code)
	}
}

// --- human nodes ---------------------------------------------------------------

func humanGateBody(agentA, agentC string) map[string]any {
	return map[string]any{
		"name": "gated", "description": "d", "status": "ready",
		"nodes": []map[string]any{
			{"id": "analyze", "agent": agentA, "task": "Analyze.", "expect": map[string]any{"path": "a.md"}},
			{"id": "gate", "kind": "human", "prompt": "Review {{ nodes.analyze.artifact }} please.",
				"fields": []map[string]any{{"key": "note", "label": "Note", "required": true}},
				"after":  []string{"analyze"}},
			{"id": "report", "agent": agentC, "task": "Report.", "after": []string{"gate"}},
		},
	}
}

func TestWorkflowHumanSuspendApproveResume(t *testing.T) {
	sessionDir := t.TempDir()
	var mu sync.Mutex
	turns := map[string]int{}
	stream := func(ctx context.Context, system, user string) (llm.Response, error) {
		if strings.Contains(system, "You are Node One.") {
			mu.Lock()
			turns["one"]++
			n := turns["one"]
			mu.Unlock()
			if n == 1 {
				return toolTurn("write", `{"path":"a.md","content":"# report\n"}`), nil
			}
			return textTurn("analysis done"), nil
		}
		return textTurn("report done"), nil
	}
	h := newWorkflowHarness(t, stream, func(c *Config) { c.SessionDir = sessionDir })
	a := createStudioAgent(t, h, "Node One", "one", "ready")
	c := createStudioAgent(t, h, "Node Three", "three", "ready")
	wf := createWorkflow(t, h, humanGateBody(a.ID, c.ID))

	rid := startWorkflowRun(t, h, wf.ID)
	rec := waitNodeStatus(t, h, wf.ID, rid, "gate", workflow.NodeWaitingHuman)
	if rec.Status != workflow.RunWorking {
		t.Fatalf("run = %q, want suspended working", rec.Status)
	}
	gate := rec.Nodes["gate"]
	if gate.Human == nil || gate.Human.Prompt != "Review a.md please." {
		t.Fatalf("human = %+v", gate.Human)
	}
	if len(gate.Human.Actions) != 2 || gate.Human.Actions[0] != "approve" || gate.Human.Actions[1] != "reject" {
		t.Fatalf("actions = %v", gate.Human.Actions)
	}
	if gate.Human.Deadline == "" || gate.Human.OnTimeout != "reject" {
		t.Fatalf("deadline contract = %+v", gate.Human)
	}
	if rec.Nodes["report"].Status != workflow.NodeWaiting {
		t.Fatalf("report = %+v", rec.Nodes["report"])
	}

	// Validation: a bogus action, a missing required field, an unknown field.
	resumeNode(t, h, wf.ID, rid, "gate", `{"action":"maybe"}`, 400)
	resumeNode(t, h, wf.ID, rid, "gate", `{"action":"approve"}`, 400)
	resumeNode(t, h, wf.ID, rid, "gate", `{"action":"approve","fields":{"note":"ok","smuggled":"x"}}`, 400)
	resumeNode(t, h, wf.ID, rid, "analyze", `{"action":"approve"}`, 409)

	// Approve: the run continues to completion, downstream released.
	resumeNode(t, h, wf.ID, rid, "gate", `{"action":"approve","fields":{"note":"ship it"}}`, 200)
	final := waitWorkflowRun(t, h, wf.ID, rid)
	if final.Status != workflow.RunCompleted {
		t.Fatalf("run = %q (%q)", final.Status, final.Error)
	}
	gate = final.Nodes["gate"]
	if gate.Status != workflow.NodeSuccess || gate.Artifact != "_human/gate.json" || gate.Human.Action != "approve" || gate.Human.Values["note"] != "ship it" {
		t.Fatalf("settled gate = %+v", gate)
	}
	if final.Nodes["report"].Status != workflow.NodeSuccess {
		t.Fatalf("report = %+v", final.Nodes["report"])
	}
	// The decision file is an ordinary artifact.
	resp := h.do(http.MethodGet, "/api/workflows/"+wf.ID+"/runs/"+rid+"/nodes/gate/artifact", "")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "ship it") {
		t.Fatalf("decision artifact: %d %q", resp.StatusCode, raw)
	}

	// Double resume is a conflict — the wait is over.
	resumeNode(t, h, wf.ID, rid, "gate", `{"action":"approve","fields":{"note":"again"}}`, 409)
}

func TestWorkflowHumanRejectReleasesRejectedBranch(t *testing.T) {
	h := newWorkflowHarness(t, func(ctx context.Context, system, user string) (llm.Response, error) {
		return textTurn("done"), nil
	})
	a := createStudioAgent(t, h, "Yes Bot", "yes", "ready")
	b := createStudioAgent(t, h, "No Bot", "no", "ready")
	body := map[string]any{
		"name": "branch", "description": "d", "status": "ready",
		"nodes": []map[string]any{
			{"id": "gate", "kind": "human", "prompt": "Ship it?"},
			{"id": "on_yes", "agent": a.ID, "task": "Celebrate.", "after": []string{"gate"}},
			{"id": "on_no", "agent": b.ID, "task": "Regroup.", "after": []map[string]any{{"node": "gate", "when": "rejected"}}},
		},
	}
	wf := createWorkflow(t, h, body)
	rid := startWorkflowRun(t, h, wf.ID)
	waitNodeStatus(t, h, wf.ID, rid, "gate", workflow.NodeWaitingHuman)
	resumeNode(t, h, wf.ID, rid, "gate", `{"action":"reject"}`, 200)
	final := waitWorkflowRun(t, h, wf.ID, rid)

	// rejected is neutral: run completed, only the rejected edge released.
	if final.Status != workflow.RunCompleted {
		t.Fatalf("run = %q (%q)", final.Status, final.Error)
	}
	if final.Nodes["gate"].Status != workflow.NodeRejected || final.Nodes["gate"].Human.Action != "reject" {
		t.Fatalf("gate = %+v", final.Nodes["gate"])
	}
	if final.Nodes["on_no"].Status != workflow.NodeSuccess || final.Nodes["on_no"].Branch != workflow.WhenRejected {
		t.Fatalf("on_no = %+v", final.Nodes["on_no"])
	}
	if final.Nodes["on_yes"].Status != workflow.NodeSkipped || final.Nodes["on_yes"].SkippedBy != "gate" {
		t.Fatalf("on_yes = %+v", final.Nodes["on_yes"])
	}
}

func TestWorkflowApprovalNodeFlow(t *testing.T) {
	var mu sync.Mutex
	turns := 0
	h := newWorkflowHarness(t, func(ctx context.Context, system, user string) (llm.Response, error) {
		mu.Lock()
		turns++
		n := turns
		mu.Unlock()
		if strings.Contains(system, "You are Drafter.") && n == 1 {
			return toolTurn("write", `{"path":"draft.md","content":"# draft\n"}`), nil
		}
		return textTurn("draft ready"), nil
	})
	a := createStudioAgent(t, h, "Drafter", "draft", "ready")
	b := createStudioAgent(t, h, "Publisher", "publish", "ready")
	body := map[string]any{
		"name": "approval", "description": "d", "status": "ready",
		"nodes": []map[string]any{
			{"id": "draft", "agent": a.ID, "task": "Draft it.", "expect": map[string]any{"path": "draft.md"}, "approval": true},
			{"id": "publish", "agent": b.ID, "task": "Publish it.", "after": []string{"draft"}},
		},
	}
	wf := createWorkflow(t, h, body)
	rid := startWorkflowRun(t, h, wf.ID)
	rec := waitNodeStatus(t, h, wf.ID, rid, "draft", workflow.NodeWaitingHuman)

	// The agent's result is stashed; the review card carries it.
	draft := rec.Nodes["draft"]
	if draft.Human == nil || !strings.Contains(draft.Human.Prompt, "draft ready") || !strings.Contains(draft.Human.Prompt, "draft.md") {
		t.Fatalf("approval prompt = %+v", draft.Human)
	}
	if draft.Summary != "draft ready" || draft.Artifact != "draft.md" || rec.Nodes["publish"].Status != workflow.NodeWaiting {
		t.Fatalf("stashed = %+v publish = %+v", draft, rec.Nodes["publish"])
	}

	resumeNode(t, h, wf.ID, rid, "draft", `{"action":"approve"}`, 200)
	final := waitWorkflowRun(t, h, wf.ID, rid)
	if final.Status != workflow.RunCompleted {
		t.Fatalf("run = %q (%q)", final.Status, final.Error)
	}
	if final.Nodes["draft"].Status != workflow.NodeSuccess || final.Nodes["draft"].Artifact != "draft.md" {
		t.Fatalf("draft = %+v", final.Nodes["draft"])
	}
	if final.Nodes["publish"].Status != workflow.NodeSuccess {
		t.Fatalf("publish = %+v", final.Nodes["publish"])
	}
}

// --- timeout sweep, cancellation and restart ------------------------------------

func TestWorkflowHumanTimeoutSweep(t *testing.T) {
	mk := func(onTimeout string) (*harness, string, string) {
		h := newWorkflowHarness(t, func(ctx context.Context, system, user string) (llm.Response, error) {
			return textTurn("done"), nil
		})
		a := createStudioAgent(t, h, "After Bot", "after", "ready")
		gate := map[string]any{"id": "gate", "kind": "human", "prompt": "Quick!", "timeout": "1ms"}
		if onTimeout != "" {
			gate["on_timeout"] = onTimeout
		}
		body := map[string]any{
			"name": "sweep", "description": "d", "status": "ready",
			"nodes": []map[string]any{
				gate,
				{"id": "after", "agent": a.ID, "task": "After.", "after": []map[string]any{{"node": "gate", "when": "rejected"}}},
			},
		}
		wf := createWorkflow(t, h, body)
		rid := startWorkflowRun(t, h, wf.ID)
		waitNodeStatus(t, h, wf.ID, rid, "gate", workflow.NodeWaitingHuman)
		time.Sleep(5 * time.Millisecond) // let the 1ms deadline lapse
		h.mgr.wfruns.sweepOnce()
		return h, wf.ID, rid
	}

	t.Run("default reject continues rejected branches", func(t *testing.T) {
		h, wid, rid := mk("")
		final := waitWorkflowRun(t, h, wid, rid)
		if final.Status != workflow.RunCompleted {
			t.Fatalf("run = %q (%q)", final.Status, final.Error)
		}
		if final.Nodes["gate"].Status != workflow.NodeRejected || final.Nodes["gate"].Human.Action != "timeout" {
			t.Fatalf("gate = %+v", final.Nodes["gate"])
		}
		if final.Nodes["after"].Status != workflow.NodeSuccess || final.Nodes["after"].Branch != workflow.WhenRejected {
			t.Fatalf("after = %+v", final.Nodes["after"])
		}
	})

	t.Run("fail contract fails the run", func(t *testing.T) {
		h := newWorkflowHarness(t, func(ctx context.Context, system, user string) (llm.Response, error) {
			return textTurn("done"), nil
		})
		body := map[string]any{
			"name": "sweep-fail", "description": "d", "status": "ready",
			"nodes": []map[string]any{
				{"id": "gate", "kind": "human", "prompt": "Quick!", "timeout": "1ms", "on_timeout": "fail"},
			},
		}
		wf := createWorkflow(t, h, body)
		rid := startWorkflowRun(t, h, wf.ID)
		waitNodeStatus(t, h, wf.ID, rid, "gate", workflow.NodeWaitingHuman)
		time.Sleep(5 * time.Millisecond)
		h.mgr.wfruns.sweepOnce()
		final := waitWorkflowRun(t, h, wf.ID, rid)
		if final.Status != workflow.RunFailed || final.Nodes["gate"].Status != workflow.NodeFailed {
			t.Fatalf("run = %q gate = %+v", final.Status, final.Nodes["gate"])
		}
	})
}

func TestWorkflowSuspendedRunCancelAndRestart(t *testing.T) {
	sessionDir := t.TempDir()
	h := newWorkflowHarness(t, func(ctx context.Context, system, user string) (llm.Response, error) {
		return textTurn("downstream done"), nil
	}, func(c *Config) { c.SessionDir = sessionDir })
	a := createStudioAgent(t, h, "After Bot", "after", "ready")
	body := map[string]any{
		"name": "suspend", "description": "d", "status": "ready",
		"nodes": []map[string]any{
			{"id": "gate", "kind": "human", "prompt": "ok?"},
			{"id": "after", "agent": a.ID, "task": "Go.", "after": []string{"gate"}},
		},
	}
	wf := createWorkflow(t, h, body)

	t.Run("cancel a suspended run", func(t *testing.T) {
		rid := startWorkflowRun(t, h, wf.ID)
		waitNodeStatus(t, h, wf.ID, rid, "gate", workflow.NodeWaitingHuman)
		resp := h.do(http.MethodPost, "/api/workflows/"+wf.ID+"/runs/"+rid+"/cancel", "")
		var out struct {
			Cancelled bool `json:"cancelled"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if !out.Cancelled {
			t.Fatal("suspended run reported not cancelled")
		}
		rec := getWorkflowRun(t, h, wf.ID, rid)
		if rec.Status != workflow.RunCancelled || rec.Nodes["gate"].Status != workflow.NodeCancelled || rec.Nodes["after"].Status != workflow.NodeCancelled {
			t.Fatalf("cancelled record = %+v", rec)
		}
	})

	t.Run("suspended run survives a restart and resumes", func(t *testing.T) {
		rid := startWorkflowRun(t, h, wf.ID)
		waitNodeStatus(t, h, wf.ID, rid, "gate", workflow.NodeWaitingHuman)

		// Release the persistent dispatcher's controller lease, as a real
		// restart does, before opening the same data directory again.
		h.mgr.Close()
		// A second manager over the same directory: recovery keeps the wait.
		h2 := newWorkflowHarness(t, func(ctx context.Context, system, user string) (llm.Response, error) {
			return textTurn("downstream done"), nil
		}, func(c *Config) { c.SessionDir = sessionDir })
		rec := getWorkflowRun(t, h2, wf.ID, rid)
		if rec.Status != workflow.RunWorking || rec.Nodes["gate"].Status != workflow.NodeWaitingHuman {
			t.Fatalf("recovered record = %+v", rec)
		}
		resumeNode(t, h2, wf.ID, rid, "gate", `{"action":"approve"}`, 200)
		final := waitWorkflowRun(t, h2, wf.ID, rid)
		if final.Status != workflow.RunCompleted || final.Nodes["after"].Status != workflow.NodeSuccess {
			t.Fatalf("run = %q (%q) after = %+v", final.Status, final.Error, final.Nodes["after"])
		}
	})
}

// --- dispatch events on the live stream ------------------------------------------

func TestWorkflowDispatchEventsOnStream(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var candID string
	var orchestratorTurns int
	stream := func(ctx context.Context, system, user string) (llm.Response, error) {
		if strings.Contains(system, "You are Orchestrator.") {
			mu.Lock()
			id := candID
			orchestratorTurns++
			turn := orchestratorTurns
			mu.Unlock()
			if turn > 1 {
				return textTurn("delegation complete"), nil
			}
			select {
			case <-release:
				return toolTurn("dispatch_to_"+id, `{"task":"sub"}`), nil
			case <-ctx.Done():
				return llm.Response{StopReason: llm.StopAborted}, nil
			}
		}
		return textTurn("worker done"), nil
	}
	h := newWorkflowHarness(t, stream)
	orch := createStudioAgent(t, h, "Orchestrator", "delegate", "ready")
	cand := createStudioAgent(t, h, "Worker", "work", "ready")
	mu.Lock()
	candID = cand.ID
	mu.Unlock()
	body := map[string]any{
		"name": "events", "description": "d", "status": "ready",
		"nodes": []map[string]any{
			{"id": "lead", "agent": orch.ID, "task": "Delegate.", "orchestrate": map[string]any{
				"candidates": []map[string]any{{"agent": cand.ID, "hint": "work"}},
			}},
			{"id": "gate", "kind": "human", "prompt": "hold", "after": []string{"lead"}},
		},
	}
	wf := createWorkflow(t, h, body)
	rid := startWorkflowRun(t, h, wf.ID)

	resp := h.do(http.MethodGet, "/api/workflows/"+wf.ID+"/runs/"+rid+"/stream", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream: %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	frames := make(chan Event, 128)
	go func() {
		r := bufio.NewReader(resp.Body)
		for {
			e, ok, err := readFrame(r)
			if err != nil {
				close(frames)
				return
			}
			if ok {
				frames <- e
			}
		}
	}()
	once.Do(func() { close(release) })

	var sawStart, sawEnd, sawSuspended bool
	deadline := time.After(20 * time.Second)
	for !sawEnd || !sawSuspended {
		select {
		case e, open := <-frames:
			if !open {
				t.Fatal("stream ended early")
			}
			switch e.Type {
			case EvWorkflowDispatchStart:
				if e.NodeID == "lead" && e.Dispatch != nil && e.Dispatch.Candidate == cand.ID {
					sawStart = true
				}
			case EvWorkflowDispatchEnd:
				if e.NodeID == "lead" && e.Dispatch != nil && e.Dispatch.Status == workflow.NodeSuccess {
					sawEnd = true
				}
			case EvWorkflowNodeStart:
				if e.NodeID == "gate" && e.NodeStatus == workflow.NodeWaitingHuman {
					sawSuspended = true
				}
			}
		case <-deadline:
			t.Fatalf("missing events: start=%v end=%v suspended=%v", sawStart, sawEnd, sawSuspended)
		}
	}
	if !sawStart {
		t.Fatal("no dispatch_start on the stream")
	}
}
