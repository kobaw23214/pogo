package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/tools"
)

func awTestCall(t *testing.T, h *muHarness, token, aid, group string, body any, want int) map[string]json.RawMessage {
	t.Helper()
	r := h.doAs(token, http.MethodPost, "/api/apps/"+aid+"/agent-workflows/"+group, studioBody(body))
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != want {
		t.Fatalf("internal flow %s: %d want %d: %s", group, r.StatusCode, want, b)
	}
	var value map[string]json.RawMessage
	if err = json.Unmarshal(b, &value); err != nil {
		t.Fatal(err, string(b))
	}
	return value
}

func awTestValue[T any](t *testing.T, value map[string]json.RawMessage, key string) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(value[key], &out); err != nil {
		t.Fatalf("decode %s: %v (%s)", key, err, value[key])
	}
	return out
}

func awTestAgent(t *testing.T, h *muHarness, name string, extra ...string) studioAgent {
	t.Helper()
	body := studioAgentBody(name, "Use actual tools for the specified step only; report real outputs and input sources.", "ready")
	selected := []string{tools.AgentWorkflowReadName, tools.AgentWorkflowEditName, tools.AgentWorkflowEnableName, tools.AgentWorkflowRunName}
	loaded := h.mgr.Skills()
	if !slices.ContainsFunc(loaded, func(sk skills.Skill) bool { return sk.Name == "agent-workflows" && sk.Source == "embedded" }) {
		loaded = bindingTestSkills(t)
		h.mgr.SetSkills(loaded)
	}
	selectedSkills := []string{bindingSkillID(t, loaded, "agent-workflows")}
	for _, name := range extra {
		group := tools.SkillBindingForTool(name)
		if group != "" && !slices.Contains(selectedSkills, bindingSkillID(t, loaded, group)) {
			selectedSkills = append(selectedSkills, bindingSkillID(t, loaded, group))
		}
	}
	body["tools"], body["skills"], body["mcp_servers"] = append(selected, extra...), selectedSkills, []string{}
	r := h.doAs(h.token, http.MethodPost, "/api/manage/agents", studioBody(body))
	defer r.Body.Close()
	if r.StatusCode != 201 {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("create test Agent: %d %s", r.StatusCode, b)
	}
	var a studioAgent
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		t.Fatal(err)
	}
	return a
}

func awTestRun(t *testing.T, h *muHarness, token, aid, wid, rid, status string) af.Run {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	var latest af.Run
	for time.Now().Before(deadline) {
		latest = awTestValue[af.Run](t, awTestCall(t, h, token, aid, "read", map[string]any{"action": "run", "workflow_id": wid, "run_id": rid}, 200), "run")
		if latest.Status == status {
			return latest
		}
		if latest.Status == af.Failed || latest.Status == af.Interrupted || latest.Status == af.Cancelled {
			t.Fatalf("Run settled %s while waiting for %s: %s", latest.Status, status, latest.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waiting for Run %s, last %+v", status, latest)
	return latest
}

func awTestApproveActualTool(t *testing.T, h *muHarness, token string, worker *Session) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		for _, g := range worker.Hub().Snapshot().Live.PendingGates {
			if g.Tool != "bash" || !strings.Contains(string(g.Args), "approval-check") {
				t.Fatalf("unexpected owned execution approval: %+v", g)
			}
			r := h.doAs(token, http.MethodPost, "/api/sessions/"+worker.ID+"/control", studioBody(map[string]any{"action": "gate_decide", "gate_id": g.GateID, "allow": true}))
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			if r.StatusCode != 200 {
				t.Fatalf("actual tool approval failed: %d %s", r.StatusCode, b)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("workflow lost the real per-call tool approval boundary")
}

func awTestSegments(t *testing.T, h *muHarness, aid, wid, rid string, count int) {
	t.Helper()
	owner := identity.LocalScope("alice")
	deadline := time.Now().Add(2 * time.Second)
	for {
		segments, err := taskRunsDB(t, h.mgr).AWExecutions(sessionResultOwnerKey(&owner), aid, wid, rid)
		if err != nil {
			t.Fatal(err)
		}
		complete := len(segments) == count
		for _, segment := range segments {
			complete = complete && segment.Status == taskqueue.Completed
		}
		if complete {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("confirmation duplicated or failed queue segments: %+v", segments)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func awTestRestart(t *testing.T, h *muHarness) {
	t.Helper()
	cfg := h.mgr.cfg
	h.srv.Close()
	h.psrv.Close()
	h.mgr.Close()
	mgr, err := NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	server, err := NewServer(mgr, ServerOptions{Token: h.token})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	h.mgr, h.psrv = mgr, server
	h.armUsers(map[string]userRecord{"alice": {Username: "alice", Token: aliceToken}, "bob": {Username: "bob", Token: bobToken}})
	h.srv = httptest.NewServer(server.Handler())
	t.Cleanup(h.srv.Close)
}

// This deterministic model performs real read/write/bash tools through the
// ordinary Agent runtime. The test never creates output files on its behalf.
type awReportClient struct {
	mu           sync.Mutex
	calls        int
	readMaterial bool
	topic        bool
	feedback     bool
	gateCheck    bool
}

func (c *awReportClient) Model() string { return "k3" }
func (c *awReportClient) Stream(_ context.Context, _ string, history []llm.Message, schemas []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	for _, schema := range schemas {
		if strings.HasPrefix(schema.Name, "agent_workflow_") || strings.HasPrefix(schema.Name, "workflow_") || schema.Name == "background_task" || schema.Name == "subagent" || schema.Name == "subagent_tasks" || schema.Name == "watch" || schema.Name == "watch_test" || schema.Name == "watch_decide" {
			return llm.Response{}, fmt.Errorf("workflow execution exposed management tool %s", schema.Name)
		}
	}
	var content strings.Builder
	for _, m := range history {
		for _, b := range m.Content {
			content.WriteString(b.Text)
		}
	}
	all := content.String()
	c.topic = c.topic || strings.Contains(all, "独立测试主题")
	response := func(tool, args string) llm.Response {
		r := toolTurn(tool, args)
		r.Message.Content[0].ID = fmt.Sprintf("aw_call_%d", c.calls)
		return r
	}
	switch c.calls {
	case 1:
		return response("read", `{"path":"inputs/01-source.md"}`), nil
	case 2:
		c.readMaterial = strings.Contains(all, "QA-SOURCE-731")
		if !c.readMaterial {
			return llm.Response{}, fmt.Errorf("selected source was not actually read")
		}
		return response("write", `{"path":"notes.md","content":"QA-SOURCE-731：合成测试资料，预算1000元。来源 inputs/01-source.md。"}`), nil
	case 3:
		return textTurn("QA-SOURCE-731 要点和来源已写入 notes.md。"), nil
	case 4:
		c.feedback = strings.Contains(all, "确认提纲，保留来源")
		if !c.feedback {
			return llm.Response{}, fmt.Errorf("actual human feedback missing from resumed step")
		}
		c.gateCheck = true
		return response("bash", `{"command":"printf 'approval-check'"}`), nil
	case 5:
		return response("write", `{"path":"report.md","content":"# 测试资料报告\n\nQA-SOURCE-731，预算1000元。\n\n已根据本人意见确认提纲，保留来源。\n来源：inputs/01-source.md。"}`), nil
	case 6:
		return textTurn("报告已实际写入 report.md，保留测试来源。"), nil
	}
	return llm.Response{}, fmt.Errorf("unexpected model execution %d", c.calls)
}

func TestAgentWorkflowPrivateReportLifecycleAndExecutionBoundary(t *testing.T) {
	model := &awReportClient{}
	h := newMultiuserHarness(t, nil, func(c *Config) { c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return model } })
	a := awTestAgent(t, h, "资料整理助手", "write", "bash")
	other := awTestAgent(t, h, "另一个助手", "write")
	source := h.studioConversationAs(aliceToken, a.ID)
	second := h.studioConversationAs(aliceToken, a.ID)
	if err := os.WriteFile(filepath.Join(h.session(source).cwd, "source.md"), []byte("QA-SOURCE-731：这是一份独立测试资料，预算1000元。"), 0600); err != nil {
		t.Fatal(err)
	}
	platformBefore := studioBody(h.mgr.workflows.list(true))
	create := map[string]any{"action": "create", "request_id": "report-create", "name": "资料整理与报告", "inputs": []af.Input{{Key: "topic", Required: true}}, "steps": []af.Step{
		{ID: "collect", Kind: "agent", Task: "阅读选定材料，整理要点和来源。", OutputFile: "notes.md"},
		{ID: "review", Kind: "confirm", Prompt: "核对要点：{{ steps.collect.summary }}"},
		{ID: "report", Kind: "agent", Task: "根据 {{ steps.collect.artifact }} 和本人确认意见生成报告。", OutputFile: "report.md"},
	}}
	d := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", create, 200), "workflow")
	if repeated := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", create, 200), "workflow"); repeated.ID != d.ID {
		t.Fatal("duplicate edit created another flow")
	}
	// The second conversation discovers the same saved private flow; Bob and a
	// different Agent cannot retrieve or approve it even with guessed IDs.
	b := h.mgr.agentWorkflowToolBackend(h.session(second))
	raw, e := b.AgentWorkflowCall(context.Background(), "read", json.RawMessage(studioBody(map[string]any{"action": "get", "workflow_id": d.ID})))
	if e != nil || !strings.Contains(string(raw), d.ID) {
		t.Fatal("flow was tied to its creation chat", string(raw), e)
	}
	awTestCall(t, h, bobToken, a.ID, "read", map[string]any{"action": "get", "workflow_id": d.ID}, 404)
	awTestCall(t, h, aliceToken, other.ID, "read", map[string]any{"action": "get", "workflow_id": d.ID}, 404)
	patched := map[string]any{"action": "patch", "workflow_id": d.ID, "expected_revision": d.Revision, "request_id": "report-patch", "operations": []any{map[string]any{"op": "update_step", "step_id": "collect", "patch": map[string]any{"task": "阅读选定材料，整理要点、金额和出处。"}}}}
	d = awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", patched, 200), "workflow")
	if d.Revision != 2 || d.Steps[0].ID != "collect" || d.Steps[1].ID != "review" {
		t.Fatal("patch changed stable step identity", d)
	}
	stale := map[string]any{"action": "patch", "workflow_id": d.ID, "expected_revision": 1, "request_id": "report-stale", "operations": []any{map[string]any{"op": "set_metadata", "name": "stale"}}}
	awTestCall(t, h, aliceToken, a.ID, "edit", stale, 409)
	awTestCall(t, h, aliceToken, a.ID, "enable", map[string]any{"action": "enable", "workflow_id": d.ID, "expected_revision": d.Revision, "expected_hash": strings.Repeat("0", 64)}, 409)
	trial := map[string]any{"action": "trial", "workflow_id": d.ID, "request_id": "report-trial", "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "source_session_id": source, "inputs": map[string]string{"topic": "独立测试主题"}, "files": []string{"source.md"}}
	accepted := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", trial, 200), "run")
	if again := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", trial, 200), "run"); again.ID != accepted.ID {
		t.Fatal("repeated trial created another Run")
	}
	if err := os.WriteFile(filepath.Join(h.session(source).cwd, "source.md"), []byte("CHANGED-LIVE-SOURCE：不能影响已受理运行。"), 0600); err != nil {
		t.Fatal(err)
	}
	waiting := awTestRun(t, h, aliceToken, a.ID, d.ID, accepted.ID, af.WaitingConfirmation)
	if !waiting.Trial || waiting.Definition.Revision != 2 || waiting.CurrentStep != 1 || waiting.Steps[0].Status != af.Completed || waiting.Steps[0].Artifact != "notes.md" {
		t.Fatal("trial did not freeze real outputs", waiting)
	}
	if waiting.OwnerID != "" || waiting.AgentSnapshot != nil || waiting.Workspace != "" {
		t.Fatal("public receipt leaked private authority/runtime", waiting)
	}
	if h.listIDs(aliceToken)[waiting.ExecutionSessionID] {
		t.Fatal("execution session leaked into ordinary chats")
	}
	worker := h.session(waiting.ExecutionSessionID)
	marker := h.mgr.agentWorkflowMarker(worker.ID)
	if marker == nil || marker.RunID != waiting.ID || marker.AgentID != a.ID || marker.SourceSessionID != source || h.mgr.agentWorkflowToolBackend(worker) != nil {
		t.Fatal("untrusted or recursively capable execution session", marker)
	}
	for _, path := range []string{"messages", "control"} {
		body := map[string]any{"prompt": "do unrelated work"}
		if path == "control" {
			body = map[string]any{"action": "set_policy", "mode": "auto"}
		}
		r := h.doAs(aliceToken, http.MethodPost, "/api/sessions/"+worker.ID+"/"+path, studioBody(body))
		r.Body.Close()
		if r.StatusCode != 409 {
			t.Fatal("execution session accepted free control", path, r.StatusCode)
		}
	}
	// Restart the real host at the durable confirmation checkpoint. Completed
	// model steps must not run again and the execution session must stay bound.
	awTestSegments(t, h, a.ID, d.ID, waiting.ID, 1)
	awTestRestart(t, h)
	restored := awTestRun(t, h, aliceToken, a.ID, d.ID, waiting.ID, af.WaitingConfirmation)
	if restored.ExecutionSessionID != worker.ID || restored.CurrentStep != 1 || restored.Steps[0].Status != af.Completed {
		t.Fatal("restart lost the durable confirmation checkpoint", restored)
	}
	worker = h.session(restored.ExecutionSessionID)
	if h.listIDs(aliceToken)[worker.ID] {
		t.Fatal("restored execution session leaked into ordinary chats")
	}
	confirm := map[string]any{"workflow_id": d.ID, "run_id": waiting.ID, "step_id": "review", "request_id": "human-one", "feedback": "确认提纲，保留来源"}
	awTestCall(t, h, bobToken, a.ID, "confirm", confirm, 404)
	awTestCall(t, h, aliceToken, other.ID, "confirm", confirm, 404)
	awTestCall(t, h, aliceToken, a.ID, "confirm", confirm, 200)
	if reused := awTestValue[bool](t, awTestCall(t, h, aliceToken, a.ID, "confirm", confirm, 200), "reused"); !reused {
		t.Fatal("same confirmation was not deduplicated")
	}
	awTestApproveActualTool(t, h, aliceToken, worker)
	completed := awTestRun(t, h, aliceToken, a.ID, d.ID, waiting.ID, af.Completed)
	if completed.ID != waiting.ID || completed.ExecutionSessionID != worker.ID || completed.Steps[1].Feedback != "确认提纲，保留来源" || completed.Steps[2].Artifact != "report.md" {
		t.Fatal("confirmation lost logical Run/session/feedback", completed)
	}
	artifact := awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "artifact", "workflow_id": d.ID, "run_id": completed.ID, "step_id": "report"}, 200)
	text := awTestValue[string](t, artifact, "text")
	if !strings.Contains(text, "QA-SOURCE-731") || !strings.Contains(text, "来源") || !strings.Contains(text, "1000元") {
		t.Fatal("real report output missing expected material", text)
	}
	awTestCall(t, h, bobToken, a.ID, "read", map[string]any{"action": "artifact", "workflow_id": d.ID, "run_id": completed.ID, "step_id": "report"}, 404)
	awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "artifact", "workflow_id": d.ID, "run_id": completed.ID, "step_id": "report", "path": "../../secret"}, 400)
	enabled := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "enable", map[string]any{"action": "enable", "workflow_id": d.ID, "expected_revision": d.Revision, "expected_hash": d.DefinitionHash}, 200), "workflow")
	if enabled.EnabledVersion != d.DefinitionHash {
		t.Fatal("enabled a different draft", enabled)
	}
	if studioBody(h.mgr.workflows.list(true)) != platformBefore {
		t.Fatal("private internal flow changed platform workflows")
	}
	model.mu.Lock()
	calls, read, feedback, gate, topic := model.calls, model.readMaterial, model.feedback, model.gateCheck, model.topic
	model.mu.Unlock()
	if calls != 6 || !read || !feedback || !gate || !topic {
		t.Fatal("model/tool lifecycle incomplete", calls, read, feedback, gate)
	}
	awTestSegments(t, h, a.ID, d.ID, completed.ID, 2)
}

func TestAgentWorkflowCancelRetryAndLiveGrantRevocation(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return textTurn("unused"), nil })
	a := awTestAgent(t, h, "个人确认助手")
	d := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "create", "request_id": "confirm-create", "name": "确认测试", "steps": []af.Step{{ID: "review", Kind: "confirm", Prompt: "请本人核对"}}}, 200), "workflow")
	r := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "workflow_id": d.ID, "request_id": "confirm-trial", "expected_revision": d.Revision, "expected_hash": d.DefinitionHash}, 200), "run")
	awTestRun(t, h, aliceToken, a.ID, d.ID, r.ID, af.WaitingConfirmation)
	cancelled := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "cancel", "workflow_id": d.ID, "run_id": r.ID, "request_id": "confirm-stop"}, 200), "run")
	if cancelled.Status != af.Cancelled {
		t.Fatal("waiting Run did not cancel", cancelled)
	}
	retryBody := map[string]any{"action": "retry", "workflow_id": d.ID, "run_id": r.ID, "request_id": "confirm-retry"}
	retry := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", retryBody, 200), "run")
	if retry.ID == r.ID || retry.RetryOf != r.ID || !retry.Trial {
		t.Fatal("retry lost truthful new Run lineage", retry)
	}
	if again := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", retryBody, 200), "run"); again.ID != retry.ID {
		t.Fatal("retry request duplicated admission")
	}
	awTestRun(t, h, aliceToken, a.ID, d.ID, retry.ID, af.WaitingConfirmation)
	body := studioAgentBody(a.Name, a.Instructions, "ready")
	body["revision"], body["tools"], body["skills"], body["mcp_servers"] = a.Revision, []string{tools.AgentWorkflowReadName}, []string{}, []string{}
	response := h.doAs(h.token, http.MethodPatch, "/api/manage/agents/"+a.ID, studioBody(body))
	defer response.Body.Close()
	if response.StatusCode != 200 {
		b, _ := io.ReadAll(response.Body)
		t.Fatalf("revoke flow grant %d: %s", response.StatusCode, b)
	}
	awTestCall(t, h, aliceToken, a.ID, "confirm", map[string]any{"workflow_id": d.ID, "run_id": retry.ID, "step_id": "review", "request_id": "denied-confirm", "feedback": "继续"}, 403)
	awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "start", "workflow_id": d.ID, "request_id": "denied-start"}, 403)
	retained := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "run", "workflow_id": d.ID, "run_id": retry.ID}, 200), "run")
	if retained.Status != af.WaitingConfirmation {
		t.Fatal("grant revocation invented a confirmation", retained)
	}
}

func TestAgentWorkflowOmittedOptionalInputRendersEmpty(t *testing.T) {
	h := newMultiuserHarness(t, nil)
	a := awTestAgent(t, h, "可选输入助手")
	source := h.studioConversationAs(aliceToken, a.ID)
	d := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "create", "request_id": "optional-create", "name": "可选参数", "inputs": []af.Input{{Key: "note"}}, "steps": []af.Step{{ID: "review", Kind: "confirm", Prompt: "补充说明：{{ inputs.note }}"}}}, 200), "workflow")
	r := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "workflow_id": d.ID, "request_id": "optional-trial", "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "source_session_id": source, "inputs": map[string]string{}}, 200), "run")
	waiting := awTestRun(t, h, aliceToken, a.ID, d.ID, r.ID, af.WaitingConfirmation)
	value, ok := waiting.Inputs["note"]
	if !ok || value != "" || waiting.Steps[0].Summary != "补充说明：" {
		t.Fatal("optional input was not normalized and rendered", waiting)
	}
}
