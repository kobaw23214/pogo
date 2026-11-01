package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func authoringConversation(t *testing.T, h *muHarness, token string) *Session {
	t.Helper()
	body := studioAgentBody("工作流搭建助手", "使用工作流专用工具创建和检查流程，禁止猜测资源ID。", "ready")
	names := []string{"workflow_discover", "workflow_read", "workflow_validate", "workflow_edit", "workflow_test", "workflow_run", "workflow_publish"}
	body["tools"], body["skills"], body["mcp_servers"] = names, []string{}, []string{}
	resp := h.doAs(h.token, http.MethodPost, "/api/manage/agents", studioBody(body))
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("create builder: %d", resp.StatusCode)
	}
	var a studioAgent
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		t.Fatal(err)
	}
	return h.session(h.studioConversationAs(token, a.ID))
}
func authoringBackendCall(t *testing.T, b tools.WorkflowBackend, action string, body any) map[string]any {
	t.Helper()
	raw, err := b.WorkflowCall(context.Background(), action, json.RawMessage(studioBody(body)))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("bad tool receipt %s: %v", raw, err)
	}
	return out
}
func TestWorkflowAuthoringToolsTrustedSessionAndRevocation(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return textTurn("unused"), nil })
	authoringBuilder(t, h, "alice")
	for _, token := range []string{aliceToken, h.token} {
		sess := authoringConversation(t, h, token)
		if h.mgr.workflowToolBackend(sess) != nil || h.mgr.platformWorkflowBackendForConversation(sess) != nil {
			t.Fatal("an Agent received the retired platform workflow host")
		}
		// A stale host instance must deny calls even if a historical Agent
		// grant or the requesting account previously allowed the action.
		backend := &workflowToolBackend{m: h.mgr, s: sess, owner: sess.owner, agentID: sess.studioID, workspace: sess.cwd}
		for _, action := range []string{"discover", "read", "validate", "edit", "test", "run", "publish"} {
			value := authoringBackendCall(t, backend, action, map[string]any{})
			err, _ := value["error"].(map[string]any)
			if value["ok"] != false || err["code"] != "PLATFORM_WORKFLOW_UNAVAILABLE_TO_AGENT" || err["status"] != float64(http.StatusForbidden) {
				t.Fatalf("retired platform action escaped its boundary: %s %+v", action, value)
			}
		}
	}
	if len(h.mgr.workflows.list(true)) != 0 {
		t.Fatal("Agent changed the platform workflow store")
	}
}

// The scripted model uses the real conversation, tool registry, authenticated
// backend, stores, queue and Run projections. It never contacts a real model.
type authoringLoopClient struct {
	t    *testing.T
	mu   sync.Mutex
	step int
	h    **muHarness
}

func (c *authoringLoopClient) Model() string { return "k3" }
func (c *authoringLoopClient) Stream(_ context.Context, system string, history []llm.Message, schemas []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	step := c.step
	c.step++
	if step == 0 {
		found := map[string]bool{}
		for _, s := range schemas {
			found[s.Name] = true
		}
		for _, name := range []string{"workflow_discover", "workflow_read", "workflow_validate", "workflow_edit", "workflow_test", "workflow_run", "workflow_publish"} {
			if found[name] {
				return llm.Response{}, fmt.Errorf("platform tool exposed to Agent: %s", name)
			}
		}
	}
	h := *c.h
	var wf Workflow
	for _, w := range h.mgr.workflows.list(true) {
		if w.Name == "采购审批草稿" {
			wf = w
		}
	}
	hash := ""
	if wf.ID != "" {
		_, _, hash, _ = h.mgr.workflows.canonicalDraft(wf)
	}
	action := ""
	var body any
	switch step {
	case 0:
		action = "discover"
		body = map[string]any{}
	case 1:
		action = "edit"
		body = map[string]any{"action": "create", "request_id": "natural-create", "name": "采购审批草稿", "nodes": []any{map[string]any{"id": "review", "kind": "human", "prompt": "审核 {{ inputs.order_id }}", "approval_policy": map[string]any{"prevent_self_approval": true, "assignment": "fixed", "approvers": []string{"bob"}}}}}
	case 2:
		action = "read"
		body = map[string]any{"action": "get", "workflow_id": wf.ID}
	case 3:
		action = "edit"
		body = map[string]any{"action": "patch", "workflow_id": wf.ID, "expected_revision": wf.Revision, "request_id": "natural-local-edit", "operations": []any{map[string]any{"op": "update_node", "node_id": "review", "patch": map[string]any{"prompt": "请 Bob 审核 {{ inputs.order_id }}"}}}}
	case 4:
		action = "validate"
		body = map[string]any{"workflow_id": wf.ID, "expected_revision": wf.Revision, "inputs": map[string]string{"order_id": "独立样例-A"}}
	case 5:
		action = "test"
		body = map[string]any{"workflow_id": wf.ID, "expected_revision": wf.Revision, "expected_hash": hash, "request_id": "natural-trial", "inputs": map[string]string{"order_id": "独立样例-A"}}
	case 6:
		action = "publish"
		body = map[string]any{"workflow_id": wf.ID, "expected_revision": wf.Revision, "expected_hash": hash, "name": "已审阅版本"}
	case 7:
		action = "run"
		body = map[string]any{"workflow_id": wf.ID, "request_id": "natural-published", "inputs": map[string]string{"order_id": "独立正式样例-B"}}
	case 8:
		action = "read"
		body = map[string]any{"action": "runs", "workflow_id": wf.ID}
	default:
		return textTurn("平台工作流只能通过管理页面操作；Agent 对话使用当前 Agent 的内部工作流。"), nil
	}
	response := toolTurn("workflow_"+action, studioBody(body))
	response.Message.Content[0].ID = fmt.Sprintf("authoring_%d", step)
	return response, nil
}
func TestWorkflowAuthoringNaturalLanguageConversationToolLoop(t *testing.T) {
	var h *muHarness
	client := &authoringLoopClient{t: t, h: &h}
	h = newMultiuserHarness(t, func(int) (llm.Response, error) { return textTurn("unused"), nil }, func(c *Config) {
		c.MaxTurns = 16
		c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return client }
	})
	sess := authoringConversation(t, h, h.token)
	sess.SetPolicy(ModeAuto, 0)
	if _, err := sess.Start("帮我搭建采购单审批流程，输入订单号，自动分配 Bob，禁止发起人自批；把审批提示补充清楚，用独立样例试跑，再发布我确认的版本。", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitRunDone(t, sess)
	snapshot := sess.hub.Snapshot()
	if len(snapshot.Results) != 9 {
		t.Fatalf("scripted stale calls were not all answered: %d", len(snapshot.Results))
	}
	for id, result := range snapshot.Results {
		if !result.IsError {
			t.Fatalf("%s reached a retired platform tool: %s", id, result.Text)
		}
	}
	if len(h.mgr.workflows.list(true)) != 0 {
		t.Fatal("stale model tool calls created or mutated a platform workflow")
	}
}
