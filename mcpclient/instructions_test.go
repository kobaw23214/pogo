package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	pogotools "github.com/yosukeno/pogo/tools"
)

// newInstructedFixture starts a neutral MCP server whose handshake declares
// session instructions, behind a streamable-HTTP endpoint. Same cleanup
// ordering as newFixture: pool first, then the server.
func newInstructedFixture(t *testing.T, instructions string) string {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "notes-fixture", Version: "v0.1"},
		&mcp.ServerOptions{Instructions: instructions})
	srv.AddTool(&mcp.Tool{
		Name: "ping", Description: "answers a ping",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil
	})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	return ts.URL
}

// The handshake's instructions must freeze onto every catalog wrapper, so the
// inspection tool can show them for exactly the snapshot they arrived with.
func TestPoolAttachesServerInstructionsToSnapshot(t *testing.T) {
	url := newInstructedFixture(t, "Fixture session instructions: prose for display only.")
	p := newPool(t)
	p.Reconcile(context.Background(), Config{Servers: map[string]*ServerConfig{"fx": {URL: url}}})
	if err := p.WaitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	toolsOut, _, status := p.Snapshot()
	if len(toolsOut) != 1 {
		t.Fatalf("tools = %d, want 1", len(toolsOut))
	}
	mt, ok := toolsOut[0].(*pogotools.MCPTool)
	if !ok {
		t.Fatalf("tool is %T", toolsOut[0])
	}
	if got := mt.ServerInstructions(); got != "Fixture session instructions: prose for display only." {
		t.Fatalf("instructions = %q", got)
	}
	// Status keeps its own copy, unchanged behavior.
	if status[0].Instructions != mt.ServerInstructions() {
		t.Fatalf("status instructions = %q", status[0].Instructions)
	}

	// A pre-run refresh against the same session keeps the frozen snapshot.
	requireRefresh(t, p) // expire the TTL so this exercises a real tools/list pull
	again, _, _ := p.Snapshot()
	if again[0] != mt {
		t.Fatal("unchanged definition lost its captured execution binding")
	}
	if again[0].(*pogotools.MCPTool).ServerInstructions() != mt.ServerInstructions() {
		t.Fatal("refresh dropped the instructions snapshot")
	}
}

// A server without instructions must yield wrappers with an empty snapshot —
// and the tool must still work.
func TestPoolNoInstructionsStaysEmpty(t *testing.T) {
	url := newFixture(t) // declares no instructions
	p := newPool(t)
	p.Reconcile(context.Background(), Config{Servers: map[string]*ServerConfig{"fx": {URL: url, Trust: true}}})
	if err := p.WaitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	toolsOut, _, _ := p.Snapshot()
	for _, tl := range toolsOut {
		if got := tl.(*pogotools.MCPTool).ServerInstructions(); got != "" {
			t.Fatalf("unexpected instructions %q on %s", got, tl.Name())
		}
	}
	res, err := toolsOut[0].Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil || res.Text == "" {
		t.Fatalf("call failed: %v %q", err, res.Text)
	}
}
