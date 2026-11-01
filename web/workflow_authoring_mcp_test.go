package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpclient"
	"github.com/yosukeno/pogo/tools"
	"github.com/yosukeno/pogo/workflow"
)

// A remote annotation is scheduling metadata, not authority to bypass the
// ordinary tool-review boundary. Test this through a real isolated MCP catalog
// so the absence of a projected tool cannot accidentally make the check pass.
func TestWorkflowAuthoringMCPReadOnlyHintNeedsHostTrust(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		name := "untrusted_readonly"
		if trusted {
			name = "explicitly_trusted"
		}
		t.Run(name, func(t *testing.T) {
			for _, key := range []string{"POGO_MCP", "POGO_MCP_TOKEN", "POGO_MCP_POLICY"} {
				t.Setenv(key, "")
			}
			var calls atomic.Int32
			upstream := mcp.NewServer(&mcp.Implementation{Name: "workflow-permission-fixture", Version: "1"}, nil)
			upstream.AddTool(&mcp.Tool{Name: "inspect", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				calls.Add(1)
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "synthetic fixture only"}}}, nil
			})
			remote := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upstream }, nil))
			t.Cleanup(remote.Close)
			store := filepath.Join(t.TempDir(), "mcp.json")
			if err := mcpclient.Save(store, mcpclient.Config{Servers: map[string]*mcpclient.ServerConfig{"fixture": {URL: remote.URL, Trust: trusted, StartupTimeoutMs: 5000}}}); err != nil {
				t.Fatal(err)
			}
			h := newHarness(t, func(int) (llm.Response, error) {
				t.Fatal("resource validation invoked a model")
				return textTurn("unused"), nil
			}, func(c *Config) { c.MCPClientStore = store })
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			if err := h.mgr.mcpPool.WaitReady(ctx); err != nil {
				t.Fatal(err)
			}
			empty, servers := []string{}, []string{"fixture"}
			a, err := h.mgr.studio.put("", studioInput{Name: "MCP permission fixture", Model: "k3", Status: "ready", Tools: &empty, Skills: &empty, MCPServers: &servers}, 0)
			if err != nil {
				t.Fatal(err)
			}
			h.mgr.mu.Lock()
			s := h.mgr.workflowAuthoringHost
			h.mgr.mu.Unlock()
			if s == nil {
				t.Fatal("authoring host not bound")
			}
			r := withWorkflowPrincipal(httptest.NewRequest(http.MethodPost, "/", nil), identity.SingleUserPrincipal())
			node := workflow.Node{ID: "inspect", Agent: a.ID, Task: "Read the synthetic fixture", Tools: &empty}
			caps := s.workflowAuthoringCapabilities(r, a, node)
			if !slices.Contains(caps.ExternalTools, "mcp_fixture_inspect") {
				t.Fatalf("fixture did not project its remote tool: %+v", caps)
			}
			projected, _, _ := h.mgr.mcpPool.Snapshot()
			if len(projected) != 1 {
				t.Fatalf("unexpected fixture projection: %d", len(projected))
			}
			if hint, ok := projected[0].(tools.ReadOnlyCapable); !ok || !hint.IsReadOnly() {
				t.Fatal("fixture did not preserve the untrusted read-only annotation")
			}
			validation := s.workflowAuthoringValidate(r, workflowInput{Name: "Synthetic permission check", Nodes: []workflow.Node{node}}, nil)
			blocked := false
			for _, diagnostic := range validation.Diagnostics {
				if diagnostic.Code == "tool_review_unavailable" && diagnostic.Severity == "error" {
					blocked = true
				}
			}
			if blocked == trusted || validation.ReadyForTest != trusted {
				t.Fatalf("server hint substituted for host trust=%v: %+v", trusted, validation)
			}
			if calls.Load() != 0 {
				t.Fatal("read-only validation executed an external business tool")
			}
		})
	}
}

type workflowReadonlyClaimFixture struct{}

func (workflowReadonlyClaimFixture) Name() string { return "fixture_readonly_claim" }
func (workflowReadonlyClaimFixture) Description() string {
	return "Synthetic manifest-style readonly claim"
}
func (workflowReadonlyClaimFixture) IsReadOnly() bool                   { return true }
func (workflowReadonlyClaimFixture) ExecutionMode() tools.ExecutionMode { return tools.Parallel }
func (workflowReadonlyClaimFixture) InputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (workflowReadonlyClaimFixture) Execute(context.Context, json.RawMessage) (tools.Result, error) {
	panic("validation must not execute the claim")
}

func TestWorkflowTrialReadonlySkillHintCannotGrantExecution(t *testing.T) {
	empty := []string{}
	claim := workflowReadonlyClaimFixture{}
	registry := tools.New(tools.Options{Cwd: t.TempDir(), Enabled: &empty, Synth: []tools.Tool{claim}})
	a := studioAgent{studioInput: studioInput{Tools: &empty}}
	node := workflow.Node{Tools: &empty}
	trial := &wfRun{record: workflow.RunRecord{TestRun: true}}
	if err := workflowTrialNodeExecutionError(trial, a, node, registry, []tools.Tool{claim}, nil, []string{claim.Name()}); err == nil {
		t.Fatal("a bundle readonly hint or annotation-derived approved name granted unsupported execution")
	}
	// The new guard is restricted to the explicitly marked draft-test path;
	// it must not silently change historical published workflow behavior.
	legacy := &wfRun{record: workflow.RunRecord{}}
	if err := workflowTrialNodeExecutionError(legacy, a, node, registry, []tools.Tool{claim}, nil, nil); err != nil {
		t.Fatal("draft-trial capability boundary changed a legacy execution", err)
	}
}
