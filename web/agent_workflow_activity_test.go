package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
)

func TestAgentWorkflowHubActivityScopedAndContentFree(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "first"})
	h.Publish(Event{Type: EvUserMessage, RunID: "first", Text: "PRIVATE-PROMPT"})
	h.Publish(Event{Type: EvTurnStart, Turn: 2})
	h.Publish(Event{Type: EvThinking, Text: "PRIVATE-THINKING"})
	check := func(phase string, count int) awNodeActivity {
		t.Helper()
		v, ok := h.agentWorkflowActivity("first")
		if !ok || v.node.Phase != phase || v.node.Turn != 2 || v.node.ToolCount == nil || *v.node.ToolCount != count {
			t.Fatalf("wrong activity: %+v / %v", v, ok)
		}
		raw, _ := json.Marshal(v.node)
		if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "arguments") || strings.Contains(string(raw), "content") {
			t.Fatal("card leaked content", string(raw))
		}
		return v.node
	}
	check("thinking", 0)
	h.Publish(Event{Type: EvToolArgs, CallID: "one", Name: "read", Text: "PRIVATE-ARGS"})
	check("tool_input", 0)
	h.Publish(Event{Type: EvMessage, Content: []llm.Block{{Type: llm.BlockToolUse, ID: "one", Name: "read"}, {Type: llm.BlockToolUse, ID: "two", Name: "write"}}})
	h.Publish(Event{Type: EvToolStart, CallID: "one", Name: "read"})
	h.Publish(Event{Type: EvToolStart, CallID: "two", Name: "write"})
	v := check("tool", 2)
	if v.PendingToolCount != 2 || v.ToolName != "read" {
		t.Fatal("parallel tool facts lost", v)
	}
	h.Publish(Event{Type: EvGateRequest, GateID: "approval", CallID: "one", Name: "read"})
	if v = check("approval", 2); v.ApprovalCount != 1 {
		t.Fatal("approval not projected immediately", v)
	}
	h.Publish(Event{Type: EvGateResolved, GateID: "approval"})
	check("tool", 2)
	h.Publish(Event{Type: EvRunStart, RunID: "second"})
	h.Publish(Event{Type: EvUserMessage, RunID: "second", Text: "next step"})
	h.Publish(Event{Type: EvTurnStart, Turn: 1})
	if _, ok := h.agentWorkflowActivity("first"); ok {
		t.Fatal("legacy node received another step's activity")
	}
	second, ok := h.agentWorkflowActivity("second")
	if !ok || second.node.ToolCount == nil || *second.node.ToolCount != 0 || second.node.Turn != 1 {
		t.Fatal("legacy step inherited previous tool count", second)
	}
	h.Publish(Event{Type: EvGateRequest, GateID: "old-background", Origin: "old-child", Name: "bash"})
	second, _ = h.agentWorkflowActivity("second")
	if second.node.ApprovalCount != 0 || second.node.Phase == "approval" {
		t.Fatal("legacy step inherited background approval", second)
	}
	h.Close()
	if _, ok := h.agentWorkflowActivity("second"); ok {
		t.Fatal("closed hub claimed live observation")
	}
}

type awActivityTestClient struct {
	release <-chan struct{}
}

type awActivitySequenceClient struct {
	calls   int
	release <-chan struct{}
}

func (c *awActivitySequenceClient) Model() string { return "k3" }
func (c *awActivitySequenceClient) Stream(ctx context.Context, _ string, _ []llm.Message, _ []llm.ToolSchema, emit func(llm.Delta)) (llm.Response, error) {
	c.calls++
	if c.calls == 1 {
		return textTurn("首个顺序步骤已完成"), nil
	}
	if emit != nil {
		emit(llm.Delta{Kind: llm.DeltaThinking, Text: "当前只属于第二个顺序步骤"})
	}
	select {
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	case <-c.release:
		return textTurn("第二个顺序步骤完成"), nil
	}
}

func TestAgentWorkflowActivityRealSequenceSessionFallbackIsScoped(t *testing.T) {
	release := make(chan struct{})
	h := newMultiuserHarness(t, nil, func(c *Config) {
		c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client {
			return &awActivitySequenceClient{release: release}
		}
	})
	t.Cleanup(func() { close(release) })
	a := awTestAgent(t, h, "顺序活动兼容测试")
	d := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "create", "request_id": "sequence-activity-create", "name": "顺序活动", "steps": []af.Step{{ID: "first", Kind: "agent", Task: "完成第一个步骤"}, {ID: "second", Kind: "agent", Task: "完成第二个步骤"}}}, 200), "workflow")
	source := h.studioConversationAs(aliceToken, a.ID)
	run := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "workflow_id": d.ID, "request_id": "sequence-activity-trial", "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "source_session_id": source}, 200), "run")
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		run = awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "run", "workflow_id": d.ID, "run_id": run.ID}, 200), "run")
		if run.Steps[0].Status == af.Completed && run.Steps[1].ActiveRunID != "" {
			hub := h.session(run.ExecutionSessionID).Hub()
			if sample, ok := hub.agentWorkflowActivity(run.Steps[1].ActiveRunID); ok && sample.node.Phase == "thinking" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Steps[0].Status != af.Completed || run.Steps[1].ActiveRunID == "" || run.Steps[0].SessionID != run.ExecutionSessionID || run.Steps[1].SessionID != run.ExecutionSessionID {
		t.Fatal("real sequential execution not established", run)
	}
	owner := identity.LocalScope("alice")
	run.OwnerID = sessionResultOwnerKey(&owner)
	// Exercise an old record shape in the read projection; leave its real row
	// untouched, and retain distinct ActiveRunIDs for the shared Session.
	run.Steps[0].SessionID, run.Steps[1].SessionID = "", ""
	activity := h.psrv.agentWorkflowActivity(run, map[string]awActivitySample{}, time.Now().UnixMilli())
	if activity.Nodes[0].Observed || activity.Nodes[0].Turn != 0 || !activity.Nodes[1].Observed || activity.Nodes[1].Phase != "thinking" || activity.Nodes[1].SessionID != run.ExecutionSessionID {
		t.Fatal("sequence fallback missing or attributed to an old node", activity)
	}
	if _, ok := h.session(run.ExecutionSessionID).Hub().agentWorkflowActivity(run.Steps[0].ActiveRunID); ok {
		t.Fatal("shared Session exposed the later node to the first step")
	}
	retained := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "run", "workflow_id": d.ID, "run_id": run.ID}, 200), "run")
	if retained.Revision != run.Revision || retained.Steps[1].SessionID != run.ExecutionSessionID {
		t.Fatal("compatibility projection rewrote the Run", retained)
	}
}

func (c *awActivityTestClient) Model() string { return "k3" }
func (c *awActivityTestClient) Stream(ctx context.Context, _ string, _ []llm.Message, _ []llm.ToolSchema, emit func(llm.Delta)) (llm.Response, error) {
	if emit != nil {
		emit(llm.Delta{Kind: llm.DeltaThinking, Text: "PRIVATE-NODE-THINKING"})
	}
	select {
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	case <-c.release:
		return textTurn("独立测试节点完成"), nil
	}
}

func awActivityHarness(t *testing.T) (*muHarness, studioAgent, af.Definition, af.Run, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	h := newMultiuserHarness(t, nil, func(c *Config) {
		c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return &awActivityTestClient{release: release} }
	})
	// Cleanup releases model goroutines before the ordinary harness closes.
	t.Cleanup(func() { close(release) })
	a := awTestAgent(t, h, "节点活动独立测试")
	d := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "create", "request_id": "activity-create", "name": "活动并行测试", "steps": []af.Step{{ID: "fork", Kind: "parallel"}, {ID: "left", Kind: "agent", Task: "整理左侧资料"}, {ID: "right", Kind: "agent", Task: "核对右侧资料"}, {ID: "join", Kind: "join"}}, "graph": af.Graph{Version: 1, Edges: []af.Edge{{Source: af.GraphInput, Target: "fork"}, {Source: "fork", Target: "left"}, {Source: "fork", Target: "right"}, {Source: "left", Target: "join"}, {Source: "right", Target: "join"}, {Source: "join", Target: af.GraphOutput}}}}, 200), "workflow")
	source := h.studioConversationAs(aliceToken, a.ID)
	run := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "workflow_id": d.ID, "request_id": "activity-trial", "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "source_session_id": source, "inputs": map[string]string{}}, 200), "run")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run = awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "run", "workflow_id": d.ID, "run_id": run.ID}, 200), "run")
		if run.Steps[1].ActiveRunID != "" && run.Steps[2].ActiveRunID != "" {
			left := h.session(run.Steps[1].SessionID).Hub()
			right := h.session(run.Steps[2].SessionID).Hub()
			if l, ok := left.agentWorkflowActivity(run.Steps[1].ActiveRunID); ok && l.node.Phase == "thinking" {
				if r, ok := right.agentWorkflowActivity(run.Steps[2].ActiveRunID); ok && r.node.Phase == "thinking" {
					return h, a, d, run, release
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("independent parallel nodes did not enter the model")
	return nil, a, d, run, release
}

type awActivityFrame struct {
	kind string
	raw  []byte
}

func awActivityURL(aid, wid, rid string) string {
	return "/api/apps/" + aid + "/agent-workflows/" + wid + "/runs/" + rid + "/activity"
}

func awActivityOpen(t *testing.T, h *muHarness, aid, wid, rid string) (*http.Response, <-chan awActivityFrame) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.srv.URL+awActivityURL(aid, wid, rid), nil)
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal("activity stream rejected", res.StatusCode, res.Header)
	}
	frames := make(chan awActivityFrame, 32)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(res.Body)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		kind := ""
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				kind = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				frame := awActivityFrame{kind, []byte(strings.TrimPrefix(line, "data: "))}
				select {
				case frames <- frame:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return res, frames
}

func awActivityNext(t *testing.T, frames <-chan awActivityFrame) awActivityFrame {
	t.Helper()
	select {
	case frame, ok := <-frames:
		if !ok {
			t.Fatal("activity stream closed before its update")
		}
		return frame
	case <-time.After(3 * time.Second):
		t.Fatal("activity update timed out")
	}
	return awActivityFrame{}
}

func awActivitySnapshot(t *testing.T, frame awActivityFrame) awRunActivity {
	t.Helper()
	var activity awRunActivity
	if frame.kind != "snapshot" || json.Unmarshal(frame.raw, &activity) != nil {
		t.Fatalf("wrong activity frame %s: %s", frame.kind, frame.raw)
	}
	if strings.Contains(string(frame.raw), "PRIVATE") {
		t.Fatal("model content leaked to activity card", string(frame.raw))
	}
	return activity
}

func TestAgentWorkflowActivityHTTPParallelUpdatesOwnershipAndDisconnect(t *testing.T) {
	h, a, d, run, _ := awActivityHarness(t)
	for _, route := range []struct{ token, aid, wid, rid string }{{bobToken, a.ID, d.ID, run.ID}, {aliceToken, "otheragent", d.ID, run.ID}, {aliceToken, a.ID, "otherworkflow", run.ID}, {aliceToken, a.ID, d.ID, "otherrun"}} {
		res := h.doAs(route.token, http.MethodGet, awActivityURL(route.aid, route.wid, route.rid), "")
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatal("foreign activity was not hidden", route, res.StatusCode)
		}
	}
	res, frames := awActivityOpen(t, h, a.ID, d.ID, run.ID)
	first := awActivitySnapshot(t, awActivityNext(t, frames))
	if first.AgentID != a.ID || first.WorkflowID != d.ID || first.RunID != run.ID || len(first.Nodes) != 4 {
		t.Fatal("activity identity changed", first)
	}
	for _, i := range []int{1, 2} {
		if first.Nodes[i].SessionID != run.Steps[i].SessionID || first.Nodes[i].ActiveRunID != run.Steps[i].ActiveRunID || first.Nodes[i].Phase != "thinking" || !first.Nodes[i].Observed {
			t.Fatal("parallel node activity misattributed", first.Nodes[i], run.Steps[i])
		}
	}
	// Opening observations must not write a Run revision or alter its frozen
	// definition. The model is deliberately held, so no execution advances.
	unchanged := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "run", "workflow_id": d.ID, "run_id": run.ID}, 200), "run")
	beforeBytes, _ := json.Marshal(run)
	afterBytes, _ := json.Marshal(unchanged)
	if string(beforeBytes) != string(afterBytes) {
		t.Fatal("observing activity rewrote a durable Run")
	}
	// A forged source-chat binding cannot provide a node's live activity.
	foreignBinding := run
	owner := identity.LocalScope("alice")
	foreignBinding.OwnerID = sessionResultOwnerKey(&owner)
	foreignBinding.Steps = append([]af.StepRun(nil), run.Steps...)
	foreignBinding.Steps[1].SessionID = run.SourceSessionID
	foreign := h.psrv.agentWorkflowActivity(foreignBinding, map[string]awActivitySample{}, time.Now().UnixMilli())
	if foreign.Nodes[1].Observed || foreign.Nodes[1].ToolCount != nil || !foreign.Nodes[2].Observed {
		t.Fatal("unbound conversation supplied node activity", foreign)
	}
	left := h.session(run.Steps[1].SessionID).Hub()
	left.Publish(Event{Type: EvGateRequest, GateID: "activity-approval", CallID: "activity-tool", Name: "read"})
	for {
		update := awActivitySnapshot(t, awActivityNext(t, frames))
		if update.Nodes[1].Phase == "approval" {
			if update.Nodes[1].ApprovalCount != 1 || update.Nodes[2].Phase != "thinking" || update.Nodes[2].ApprovalCount != 0 {
				t.Fatal("approval changed the sibling card", update)
			}
			break
		}
	}
	left.Publish(Event{Type: EvGateResolved, GateID: "activity-approval"})
	res.Body.Close()
	select {
	case <-frames:
	case <-time.After(time.Second):
		t.Fatal("disconnected activity reader did not exit")
	}
	// Closing observation neither cancels nor restarts the durable Run.
	same := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "run", "workflow_id": d.ID, "run_id": run.ID}, 200), "run")
	if same.Status != af.Running || same.CancelRequested || same.Steps[1].ActiveRunID != run.Steps[1].ActiveRunID {
		t.Fatal("disconnect changed the execution", same)
	}
	_, reconnect := awActivityOpen(t, h, a.ID, d.ID, run.ID)
	healed := awActivitySnapshot(t, awActivityNext(t, reconnect))
	if healed.Nodes[1].Phase != "thinking" || healed.Nodes[1].ApprovalCount != 0 {
		t.Fatal("reconnect replayed a stale approval", healed)
	}
	awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "cancel", "workflow_id": d.ID, "run_id": run.ID, "request_id": "activity-cancel"}, 200)
	for {
		update := awActivitySnapshot(t, awActivityNext(t, reconnect))
		if update.Status == af.Cancelled {
			break
		}
	}
	select {
	case _, ok := <-reconnect:
		if ok {
			t.Fatal("unexpected activity after terminal snapshot")
		}
	case <-time.After(time.Second):
		t.Fatal("terminal activity stream remained open")
	}
}

func TestAgentWorkflowActivityHistoricAndLargeRunsDoNotInventMetrics(t *testing.T) {
	// Historic Run facts are available without loading or copying any sessions.
	s := &Server{}
	run := af.Run{AgentID: "agent", WorkflowID: "workflow", ID: "run", Status: af.Completed, UpdatedAt: 99}
	for i := 0; i < af.MaxSteps; i++ {
		run.Steps = append(run.Steps, af.StepRun{StepID: fmt.Sprintf("node_%d", i), SessionID: fmt.Sprintf("historic_%d", i), ActiveRunID: fmt.Sprintf("turn_%d", i), Status: af.Completed, FinishedAt: 90})
	}
	activity := s.agentWorkflowActivity(run, map[string]awActivitySample{}, 100)
	if len(activity.Nodes) != 200 {
		t.Fatal("large Run was truncated", len(activity.Nodes))
	}
	for _, node := range activity.Nodes {
		if node.Observed || node.Turn != 0 || node.ToolCount != nil || node.Phase != af.Completed {
			t.Fatal("historical process metrics invented", node)
		}
	}
	run.Status, run.Steps[0].Status = af.WaitingConfirmation, af.WaitingConfirmation
	activity = s.agentWorkflowActivity(run, map[string]awActivitySample{}, 100)
	if activity.Nodes[0].Phase != "approval" || activity.Nodes[0].ApprovalCount != 1 {
		t.Fatal("durable owner confirmation not projected", activity.Nodes[0])
	}
}

func TestAgentWorkflowActivityContinuousAuthorizationAndMaintenance(t *testing.T) {
	for _, mode := range []string{"account", "maintenance", "credential"} {
		t.Run(mode, func(t *testing.T) {
			h, a, d, run, _ := awActivityHarness(t)
			_, frames := awActivityOpen(t, h, a.ID, d.ID, run.ID)
			awActivitySnapshot(t, awActivityNext(t, frames))
			want := http.StatusForbidden
			switch mode {
			case "account":
				var denied atomic.Bool
				h.mgr.mu.Lock()
				original := h.mgr.scheduleOwnerValid
				h.mgr.scheduleOwnerValid = func(owner *identity.Scope) bool { return !denied.Load() && original(owner) }
				h.mgr.mu.Unlock()
				denied.Store(true)
				t.Cleanup(func() { denied.Store(false) })
			case "maintenance":
				want = http.StatusServiceUnavailable
				path := h.mgr.releaseMaintenancePath()
				if err := os.WriteFile(path, []byte(`{"token":"`+strings.Repeat("a", 64)+`"}`), 0600); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Remove(path) })
			case "credential":
				want = http.StatusUnauthorized
				h.psrv.auth.mu.Lock()
				user := h.psrv.auth.users["alice"]
				user.Token = "rotated-token"
				h.psrv.auth.users["alice"] = user
				h.psrv.auth.mu.Unlock()
			}
			for {
				frame := awActivityNext(t, frames)
				if frame.kind == "error" {
					var issue struct {
						Code int `json:"code"`
					}
					if err := json.Unmarshal(frame.raw, &issue); err != nil || issue.Code != want {
						t.Fatal("wrong revocation result", string(frame.raw), want, err)
					}
					break
				}
			}
			select {
			case _, ok := <-frames:
				if ok {
					t.Fatal("revoked activity stream continued")
				}
			case <-time.After(time.Second):
				t.Fatal("revoked activity stream did not close", fmt.Sprint(want))
			}
		})
	}
}
