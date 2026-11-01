package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/mcpclient"
	"github.com/yosukeno/pogo/mcpobserve"
)

// A real inbound MCP submission runs a scripted agent, which calls an actual
// upstream MCP server. The two observations must share the local trace and
// task/run IDs without storing tool payloads or relying on sampled HTTP text.
func TestMCPMonitorInboundRunOutboundTrace(t *testing.T) {
	for _, key := range []string{"POGO_MCP", "POGO_MCP_TOKEN", "POGO_MCP_POLICY"} {
		t.Setenv(key, "")
	}
	upstream := mcp.NewServer(&mcp.Implementation{Name: "observe-fixture", Version: "1"}, nil)
	upstream.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("synthetic-observation-payload", 500)}}}, nil
		})
	remote := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upstream }, nil))
	t.Cleanup(remote.Close)
	configPath := filepath.Join(t.TempDir(), "mcp.json")
	if err := mcpclient.Save(configPath, mcpclient.Config{Servers: map[string]*mcpclient.ServerConfig{"fixture": {URL: remote.URL, Trust: true}}}); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, scriptedTurns(toolTurn("mcp_fixture_echo", `{"q":"synthetic-observation-payload"}`), textTurn("complete")), func(cfg *Config) { cfg.MCPClientStore = configPath })
	s, err := NewServer(h.mgr, ServerOptions{Token: h.token, MCP: &MCPOptions{Token: h.token, Policy: "auto"}, MCPClientStore: configPath, MCPLog: &MCPLogOptions{DBPath: filepath.Join(t.TempDir(), "calls.db"), PayloadOff: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.mgr.Close(); s.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.mgr.mcpPool.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	endpoint := httptest.NewServer(s.Handler())
	t.Cleanup(endpoint.Close)
	client := connectStreamable(t, endpoint.URL, h.token)
	result := callTool(t, client, "agent_submit_task", map[string]any{"prompt": "use fixture", "client_ref": "phase2-trace"})
	data := structured(t, result)
	sid, _ := data["session_id"].(string)
	runID, _ := data["run_id"].(string)
	if sid == "" || runID == "" {
		t.Fatalf("submission did not allocate IDs: %+v", data)
	}
	var outgoing mcpobserve.Call
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := s.mcpCalls.Flush(ctx); err != nil {
			t.Fatal(err)
		}
		page, err := s.mcpCalls.Query(ctx, mcpobserve.Filter{Direction: "outbound", SessionID: sid, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Calls) == 1 && page.Calls[0].Status == "ok" {
			outgoing = page.Calls[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if outgoing.ID == "" {
		t.Fatal("agent's upstream call was not recorded")
	}
	if outgoing.RunID != runID || outgoing.TaskID != sid || outgoing.ToolCallID == "" || outgoing.Server != "fixture" || outgoing.Tool != "echo" || outgoing.Transport != "streamable" {
		t.Fatalf("outgoing correlation incomplete: %+v", outgoing)
	}
	parent, err := s.mcpCalls.Detail(ctx, outgoing.ParentID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Direction != "inbound" || parent.Tool != "agent_submit_task" || parent.TraceID != outgoing.TraceID || parent.SessionID != sid || parent.RunID != runID || parent.Status != "ok" {
		t.Fatalf("inbound/outbound association incorrect: parent=%+v child=%+v", parent, outgoing)
	}
	encoded, _ := json.Marshal([]mcpobserve.Call{*parent, outgoing})
	if strings.Contains(string(encoded), "synthetic-observation-payload") {
		t.Fatal("tool payload leaked into execution metadata")
	}
}
