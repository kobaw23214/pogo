package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
)

// A real local conversation drives the seven host tools; only model planning
// is scripted. Normal workflow-node fixtures keep their existing model.
type workflowAuthoringFixtureClient struct {
	normal  llm.Client
	builder *authoringLoopClient
}

func (c *workflowAuthoringFixtureClient) Model() string { return c.normal.Model() }
func (c *workflowAuthoringFixtureClient) Stream(ctx context.Context, system string, history []llm.Message, schema []llm.ToolSchema, delta func(llm.Delta)) (llm.Response, error) {
	if strings.Contains(system, "WORKFLOW_AUTHOR_BUILDER") {
		return c.builder.Stream(ctx, system, history, schema, delta)
	}
	return c.normal.Stream(ctx, system, history, schema, delta)
}
func prepareWorkflowAuthoringUIFixture(t *testing.T, m *Manager, origin, token string) map[string]any {
	t.Helper()
	selected := []string{"workflow_discover", "workflow_read", "workflow_validate", "workflow_edit", "workflow_test", "workflow_run", "workflow_publish"}
	empty := []string{}
	a, err := m.studio.put("", studioInput{Name: "自然语言工作流搭建助手（独立验收）", Instructions: "WORKFLOW_AUTHOR_BUILDER 仅用于隔离页面验收。通过工作流专用工具落实用户需求，运行等待人工时明确说明。", Model: "k3", Tools: &selected, Skills: &empty, MCPServers: &empty, Status: "ready"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	status, raw := workflowServiceRequest(t, origin, token, http.MethodPost, "/api/apps/"+a.ID+"/sessions", "{}", "")
	if status != http.StatusCreated {
		t.Fatalf("builder conversation: %d %s", status, raw)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	sess, err := m.Get(result.ID)
	if err != nil {
		t.Fatal(err)
	}
	sess.SetPolicy(ModeAuto, 0)
	return map[string]any{"agent_id": a.ID, "session_id": sess.ID, "path": "/a/" + a.ID + "?c=" + sess.ID, "model_planning": "scripted", "approval_mode": "auto_for_isolated_fixture_only", "production_data": false}
}
