package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
	"github.com/yosukeno/pogo/workflow"
)

// A bundle's readonly claim is not host authorization. Exercise both the
// authoring admission and the actual captured node registry without calling
// InitSandbox or probing a command runner from a read-only validation path.
func TestWorkflowDraftTrialSynthReadOnlyHintCannotBypassReview(t *testing.T) {
	t.Setenv("POGO_SANDBOX", "off")
	sk := workspaceManifestSkill(t)
	var modelCalls atomic.Int32
	h := newWorkflowHarness(t, func(context.Context, string, string) (llm.Response, error) {
		modelCalls.Add(1)
		return toolTurn("fixture_read", `{"file":"must-not-be-read"}`), nil
	}, func(c *Config) { c.Skills = []skills.Skill{sk} })
	ids := []string{}
	for id, actual := range h.mgr.skillCatalogIDs() {
		if actual.Name == sk.Name {
			ids = append(ids, id)
		}
	}
	if len(ids) != 1 {
		t.Fatalf("isolated Synth fixture missing: %v", ids)
	}
	empty := []string{}
	a, err := h.mgr.studio.put("", studioInput{Name: "声明只读的 Skill", Instructions: "call fixture", Model: "k3", Status: "ready", Tools: &empty, Skills: &ids, MCPServers: &empty}, 0)
	if err != nil {
		t.Fatal(err)
	}
	v := h.mgr.skillViews()
	selected, _ := h.mgr.studioSelection(a)
	v = h.mgr.filterSkillViews(v, selected)
	if len(v.synthTools) != 1 || !slices.Contains(v.synthRO, v.synthTools[0].Name()) {
		t.Fatalf("fixture did not retain the old annotation-derived list: %+v", v.synthRO)
	}
	if hinted, ok := v.synthTools[0].(tools.ReadOnlyCapable); !ok || !hinted.IsReadOnly() {
		t.Fatal("fixture must carry a real readonlyHint")
	}
	h.mgr.mu.Lock()
	s := h.mgr.workflowAuthoringHost
	h.mgr.mu.Unlock()
	if s == nil {
		t.Fatal("authoring host missing")
	}
	request := withWorkflowPrincipal(httptest.NewRequest(http.MethodPost, "/", nil), identity.SingleUserPrincipal())
	node := workflow.Node{ID: "inspect", Agent: a.ID, Task: "read fixture", Tools: &empty}
	validation := s.workflowAuthoringValidate(request, workflowInput{Name: "只读声明不授予运行权", Nodes: []workflow.Node{node}}, nil)
	if validation.ReadyForTest {
		t.Fatalf("bundle hint exempted a Synth tool without a real gate/sandbox: %+v", validation)
	}
	wf, err := h.mgr.workflows.put("", workflowInput{Name: "禁止声明绕过试跑", Nodes: []workflow.Node{node}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	trialStart(t, h.srv.URL, h.token, wf, trialHash(t, h.mgr, wf), "synth-hint", nil, http.StatusBadRequest)
	// Reaching executor() directly must not bypass the same boundary after
	// admission. This also proves a later capability change cannot invoke it.
	run := &wfRun{id: "run_no_synth_hint_bypass", record: workflow.RunRecord{TestRun: true}, hub: NewHub()}
	defer run.hub.Close()
	_, err = h.mgr.wfruns.executor(run)(context.Background(), workflow.NodeInput{Node: node, Workspace: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "Skill") || modelCalls.Load() != 0 {
		t.Fatalf("actual test executor accepted a readonly hint: err=%v model_calls=%d", err, modelCalls.Load())
	}
	// Even passing its old hint-derived name as approved cannot authorize a
	// bundle command. Only the separate MCP projection may use host trust.
	registry := tools.New(tools.Options{Cwd: t.TempDir(), Enabled: &empty, Synth: v.synthTools})
	if err := workflowTrialNodeExecutionError(run, a, node, registry, v.synthTools, nil, v.synthRO); err == nil {
		t.Fatal("annotation-derived preapproval list bypassed the captured-node guard")
	}
}
