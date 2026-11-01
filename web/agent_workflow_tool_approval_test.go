package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
)

type awApprovalClient struct {
	mu            sync.Mutex
	user          string
	phase, serial int
}

func (*awApprovalClient) Model() string { return "k3" }
func (c *awApprovalClient) Stream(_ context.Context, _ string, history []llm.Message, _ []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	user := ""
	for _, message := range history {
		if message.Role == llm.RoleUser {
			for _, block := range message.Content {
				if block.Type == llm.BlockText && block.Text != "" {
					user = block.Text
				}
			}
		}
	}
	if user != c.user {
		c.user = user
		c.phase = 0
	}
	c.phase++
	c.serial++
	var response llm.Response
	switch c.phase {
	case 1:
		response = toolTurn("write", `{"path":"mode-proof.txt","content":"isolated workflow review-mode check"}`)
	case 2:
		response = toolTurn("bash", `{"command":"printf 'workflow-review-mode-check'"}`)
	default:
		return textTurn("The isolated write and Bash calls have completed."), nil
	}
	response.Message.Content[0].ID = fmt.Sprintf("review_mode_%d", c.serial)
	return response, nil
}

func awApprovalRunUntil(t *testing.T, h *muHarness, a studioAgent, d af.Definition, id, status, mode string, approved map[string]string) af.Run {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		run := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "run", "workflow_id": d.ID, "run_id": id}, 200), "run")
		if run.Status == af.Failed || run.Status == af.Interrupted || run.Status == af.Cancelled {
			t.Fatalf("review Run unexpectedly settled %s: %s", run.Status, run.Error)
		}
		ids := map[string]bool{}
		if run.ExecutionSessionID != "" {
			ids[run.ExecutionSessionID] = true
		}
		for _, step := range run.Steps {
			if step.SessionID != "" {
				ids[step.SessionID] = true
			}
			if step.Repeat != nil {
				for _, item := range step.Repeat.Items {
					if item.SessionID != "" {
						ids[item.SessionID] = true
					}
				}
			}
		}
		for sid := range ids {
			worker := h.session(sid)
			if worker == nil {
				continue
			}
			for _, gate := range worker.Hub().Snapshot().Live.PendingGates {
				if approved[gate.GateID] != "" {
					continue
				}
				if mode == "auto" {
					t.Fatalf("fully authorized workflow unexpectedly requested %s", gate.Tool)
				}
				if gate.Tool != "bash" && !(gate.Tool == "write" && mode == "strict") {
					t.Fatalf("unexpected tool review in %s: %s", mode, gate.Tool)
				}
				response := h.doAs(aliceToken, http.MethodPost, "/api/sessions/"+sid+"/control", studioBody(map[string]any{"action": "gate_decide", "gate_id": gate.GateID, "allow": true}))
				body, _ := io.ReadAll(response.Body)
				response.Body.Close()
				if response.StatusCode != 200 {
					t.Fatalf("isolated gate decision %d: %s", response.StatusCode, body)
				}
				approved[gate.GateID] = gate.Tool
			}
		}
		if run.Status == status {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("workflow did not reach %s under %s", status, mode)
	return af.Run{}
}

func TestAgentWorkflowToolApprovalModesAreFrozenAcrossNodesAndConfirmation(t *testing.T) {
	for _, shape := range []string{"sequence", "resource-sequence", "graph", "iteration"} {
		for _, stored := range []string{"", "standard", "strict", "auto"} {
			t.Run(shape+"/"+stored, func(t *testing.T) {
				h := newMultiuserHarness(t, nil, func(c *Config) {
					c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return &awApprovalClient{} }
				})
				a := awTestAgent(t, h, "隔离审批继承助手", "write", "bash")
				source := h.studioConversationAs(aliceToken, a.ID)
				skillID := ""
				if shape == "resource-sequence" {
					a, source, skillID, _ = awSeedResourceAgent(t, h, aliceToken)
					body := studioAgentBody(a.Name, a.Instructions, "ready")
					body["revision"], body["tools"], body["skills"], body["mcp_servers"] = a.Revision, append(append([]string{}, (*a.Tools)...), "bash"), *a.Skills, []string{}
					response := h.doAs(h.token, http.MethodPatch, "/api/manage/agents/"+a.ID, studioBody(body))
					defer response.Body.Close()
					if response.StatusCode != 200 || json.NewDecoder(response.Body).Decode(&a) != nil {
						t.Fatal("could not authorize isolated resource Agent Bash")
					}
				}
				effective := stored
				if effective == "" {
					effective = "standard"
				}
				sourceMode := ModeAuto
				if effective == "auto" {
					sourceMode = ModeStrict
				}
				h.session(source).SetPolicy(sourceMode, 0)
				first := af.Step{ID: "first", Kind: "agent", Task: "Execute the independent synthetic write and Bash checks."}
				review := af.Step{ID: "review", Kind: "confirm", Prompt: "本次测试必须等待本人确认"}
				tail := af.Step{ID: "tail", Kind: "agent", Task: "Execute the independent synthetic checks after the actual confirmation."}
				if skillID != "" {
					first.Skills, tail.Skills = []string{skillID}, []string{skillID}
				}
				steps := []af.Step{first, review, tail}
				var graph *af.Graph
				if shape == "graph" {
					right := first
					right.ID = "right"
					steps = []af.Step{first, right, review, tail}
					graph = &af.Graph{Version: 1, Edges: []af.Edge{{Source: af.GraphInput, Target: "first"}, {Source: af.GraphInput, Target: "right"}, {Source: "first", Target: "review"}, {Source: "right", Target: "review"}, {Source: "review", Target: "tail"}, {Source: "tail", Target: af.GraphOutput}}}
				}
				if shape == "iteration" {
					steps = []af.Step{{ID: "each", Kind: "iteration", Iteration: &af.IterationSpec{Items: af.CodeBinding{Type: "array", Value: json.RawMessage(`[1,2]`)}, Body: &first, Concurrency: 2}}, review, tail}
				}
				create := map[string]any{"action": "create", "request_id": "mode-create", "name": "独立审批继承检查", "steps": steps}
				if graph != nil {
					create["graph"] = graph
				}
				if stored != "" {
					create["tool_approval_mode"] = stored
				}
				d := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", create, 200), "workflow")
				if d.ToolApprovalMode != stored {
					t.Fatal("saved default was normalized or lost")
				}
				if h.session(source).policy.State().Mode != string(sourceMode) {
					t.Fatal("workflow edit changed source conversation policy")
				}
				accepted := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "request_id": "mode-trial", "workflow_id": d.ID, "source_session_id": source, "expected_revision": d.Revision, "expected_hash": d.DefinitionHash}, 200), "run")
				if shape == "resource-sequence" && accepted.Resources == nil {
					t.Fatal("resource sequence was not exercised")
				}
				approved := map[string]string{}
				wait := awApprovalRunUntil(t, h, a, d, accepted.ID, af.WaitingConfirmation, effective, approved)
				if wait.Definition.ToolApprovalMode != stored {
					t.Fatal("Run lost frozen review mode")
				}
				next := "auto"
				if effective == "auto" {
					next = "strict"
				}
				draft := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "patch", "request_id": "new-default", "workflow_id": d.ID, "expected_revision": d.Revision, "operations": []af.Operation{{Op: "set_metadata", ToolApprovalMode: &next}}}, 200), "workflow")
				if draft.ToolApprovalMode != next {
					t.Fatal("new default not saved")
				}
				if shape == "sequence" && stored == "auto" {
					awTestSegments(t, h, a.ID, d.ID, wait.ID, 1)
					awTestRestart(t, h)
					restored := awTestRun(t, h, aliceToken, a.ID, d.ID, wait.ID, af.WaitingConfirmation)
					if restored.Definition.ToolApprovalMode != "auto" || restored.ExecutionSessionID != wait.ExecutionSessionID {
						t.Fatal("restart changed frozen approval mode or node identity")
					}
				}
				awTestCall(t, h, aliceToken, a.ID, "confirm", map[string]any{"workflow_id": d.ID, "run_id": wait.ID, "step_id": "review", "request_id": "human-confirm", "feedback": "独立测试本人确认"}, 200)
				done := awApprovalRunUntil(t, h, a, d, wait.ID, af.Completed, effective, approved)
				if done.ID != accepted.ID || done.Definition.ToolApprovalMode != stored || done.Definition.DefinitionHash != d.DefinitionHash {
					t.Fatal("confirmation or draft edit replaced frozen Run")
				}
				writes, bashes := 0, 0
				for _, tool := range approved {
					if tool == "write" {
						writes++
					}
					if tool == "bash" {
						bashes++
					}
				}
				if effective == "auto" && (writes+bashes) != 0 || effective == "standard" && (writes != 0 || bashes < 2) || effective == "strict" && (writes < 2 || bashes < 2) {
					t.Fatal("incorrect inherited review behavior", effective, writes, bashes)
				}
			})
		}
	}
}

func TestAgentWorkflowToolApprovalAPIRejectsInvalidAndForeignEdits(t *testing.T) {
	h := newMultiuserHarness(t, nil)
	a := awTestAgent(t, h, "审批设置隔离检查")
	capabilities := awTestValue[agentWorkflowCaps](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "capabilities"}, 200), "capabilities")
	if strings.Join(capabilities.ToolApprovalModes, ",") != "standard,strict,auto" || capabilities.DefaultToolApprovalMode != "standard" {
		t.Fatal("actual review capability discovery missing", capabilities)
	}
	create := map[string]any{"action": "create", "request_id": "approval-create", "name": "test", "steps": []af.Step{{ID: "one", Kind: "confirm", Prompt: "confirm"}}}
	for _, mode := range []any{"", nil, "full", true} {
		create["tool_approval_mode"] = mode
		awTestCall(t, h, aliceToken, a.ID, "edit", create, 400)
	}
	create["tool_approval_mode"] = "strict"
	d := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", create, 200), "workflow")
	for _, mode := range []any{"", nil, "full", true} {
		awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "patch", "request_id": "invalid-patch", "workflow_id": d.ID, "expected_revision": d.Revision, "operations": []map[string]any{{"op": "set_metadata", "tool_approval_mode": mode}}}, 400)
	}
	patch := map[string]any{"action": "patch", "request_id": "foreign-patch", "workflow_id": d.ID, "expected_revision": d.Revision, "operations": []map[string]any{{"op": "set_metadata", "tool_approval_mode": "auto"}}}
	awTestCall(t, h, bobToken, a.ID, "edit", patch, 404)
	got := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "get", "workflow_id": d.ID}, 200), "workflow")
	if got.ToolApprovalMode != "strict" {
		t.Fatal("rejected edit changed workflow review mode")
	}
	for _, mode := range []string{"", "standard", "strict", "auto", "corrupt"} {
		want := ModeStrict
		if mode == "" || mode == "standard" {
			want = ModeStandard
		}
		if mode == "auto" {
			want = ModeAuto
		}
		if got := awWorkflowToolApprovalMode(af.Definition{ToolApprovalMode: mode}); got != want {
			t.Fatal("unsafe runtime mode fallback", mode, got)
		}
	}
	if strings.Contains(string(studioBody(got)), `"owner_id":"bob"`) {
		t.Fatal("foreign ownership leaked")
	}
}
