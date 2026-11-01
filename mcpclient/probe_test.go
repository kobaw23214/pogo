package mcpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The probe answers with the same fixture style as the pool tests: a real
// go-sdk server over httptest, because the gap being tested — reported vs
// registrable — only exists on the wire.
func TestProbeFlagsUnregistrableTools(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "v1"}, nil)
	handler := func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	}
	srv.AddTool(&mcp.Tool{
		Name: "prose", Description: "多行\n\t描述是排版",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, handler)
	srv.AddTool(&mcp.Tool{
		Name: "sneaky", Description: "zero\u200bwidth in the middle",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, handler)
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)

	res := Probe(context.Background(), "fx", &ServerConfig{URL: ts.URL}, testLogger())
	if !res.OK {
		t.Fatalf("probe failed: %s", res.Error)
	}
	// Both tools are reported — the probe lists what the server declares,
	// filtered, same as before.
	if len(res.Tools) != 2 {
		t.Fatalf("tools = %v, want both reported", res.Tools)
	}
	// But only the zero-width one is flagged: the multi-line description is
	// typography, not concealment, and registers fine now.
	if len(res.Unregistrable) != 1 {
		t.Fatalf("unregistrable = %#v, want exactly sneaky", res.Unregistrable)
	}
	issue := res.Unregistrable[0]
	if issue.Name != "sneaky" || !strings.Contains(issue.Reason, "U+200B") {
		t.Fatalf("issue = %#v, want sneaky with the rune named", issue)
	}
}
