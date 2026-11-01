package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	pogotools "github.com/yosukeno/pogo/tools"
)

func newLargeCatalogFixture(t *testing.T, names []string) string {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "large-catalog", Version: "v1"}, nil)
	for _, name := range names {
		server.AddTool(&mcp.Tool{
			Name: name, Description: "Synthetic tool " + name,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
		}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: name + ":" + string(request.Params.Arguments)}}}, nil
		})
	}
	host := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	t.Cleanup(host.Close)
	return host.URL
}

func catalogNames(prefix string, count int) []string {
	names := make([]string, count)
	for i := range names {
		names[i] = fmt.Sprintf("%s%03d", prefix, i)
	}
	return names
}

func requireCatalogToolCall(t *testing.T, catalog []pogotools.Tool, server, name string) {
	t.Helper()
	wire := pogotools.WireNameFor(server, name)
	for _, tool := range catalog {
		if tool.Name() != wire {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result, err := tool.Execute(ctx, json.RawMessage(`{"tail":true}`))
		if err != nil || !strings.Contains(result.Text, name+":") || !strings.Contains(result.Text, `"tail":true`) {
			t.Fatalf("late catalog call %s failed: %v, %s", wire, err, result.Text)
		}
		details, ok := result.Details.(pogotools.MCPDetails)
		if !ok || details.Server != server || details.Tool != name {
			t.Fatalf("call was not dispatched to the captured server/tool: %#v", result.Details)
		}
		return
	}
	t.Fatalf("late catalog tool %s missing from %d accepted tools", wire, len(catalog))
}

func TestPoolSingleServerRetainsAndExecutesToolsBeyondFifty(t *testing.T) {
	names := catalogNames("tool_", 75)
	url := newLargeCatalogFixture(t, names)
	pool := newPool(t)
	pool.Reconcile(context.Background(), Config{Servers: map[string]*ServerConfig{"large": {URL: url, Trust: true}}})
	if err := pool.WaitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	catalog, trusted, status := pool.Snapshot()
	if len(catalog) != len(names) || len(trusted) != len(names) || len(status) != 1 || status[0].State != StateConnected || status[0].Tools != len(names) {
		t.Fatalf("large catalog truncated or lost trust/status: tools=%d trusted=%d status=%#v", len(catalog), len(trusted), status)
	}
	if !slices.Contains(trusted, pogotools.WireNameFor("large", "tool_074")) {
		t.Fatal("tool after the former cutoff lost its server's explicit trust")
	}
	requireCatalogToolCall(t, catalog, "large", "tool_074")
	if got := pool.Catalogs(nil); len(got) != 1 || len(got[0].Tools) != len(names) {
		t.Fatal("display and execution catalogs disagree")
	}
}

func TestPoolMultipleServersRetainTailTrustAndSelectedScope(t *testing.T) {
	first := newLargeCatalogFixture(t, catalogNames("tool_", 35))
	last := newLargeCatalogFixture(t, catalogNames("tool_", 40))
	pool := newPool(t)
	pool.Reconcile(context.Background(), Config{Servers: map[string]*ServerConfig{
		"aa": {URL: first}, "zz": {URL: last, Trust: true},
	}})
	if err := pool.WaitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	catalog, trusted, status := pool.Snapshot()
	if len(catalog) != 75 || len(trusted) != 40 || len(status) != 2 || status[0].Name != "aa" || status[1].Name != "zz" {
		t.Fatalf("cross-server tail truncated or trust broadened: tools=%d trusted=%v status=%#v", len(catalog), trusted, status)
	}
	for _, name := range trusted {
		if !strings.HasPrefix(name, "mcp_zz_") {
			t.Fatalf("untrusted first server inherited trust: %s", name)
		}
	}
	requireCatalogToolCall(t, catalog, "zz", "tool_039")
	only, onlyTrusted, onlyStatus := pool.SnapshotServers(map[string]bool{"zz": true})
	if len(only) != 40 || len(onlyTrusted) != 40 || len(onlyStatus) != 1 || onlyStatus[0].Name != "zz" {
		t.Fatalf("selected scope changed: tools=%d trusted=%v status=%#v", len(only), onlyTrusted, onlyStatus)
	}
	for _, selection := range []map[string]bool{{}, {"not_selected": true}} {
		tools, trust, states := pool.SnapshotServers(selection)
		if len(tools) != 0 || len(trust) != 0 || len(states) != 0 {
			t.Fatal("empty or unknown selection leaked catalog or trust")
		}
	}

	// A disconnected server's last catalog remains diagnostic-only, even when
	// removing the count cap makes many more live tools available elsewhere.
	pool.mu.Lock()
	disconnected := pool.conns["zz"]
	pool.mu.Unlock()
	disconnected.mu.Lock()
	disconnected.state = StateDisconnected
	disconnected.mu.Unlock()
	catalog, trusted, status = pool.Snapshot()
	if len(catalog) != 35 || len(trusted) != 0 || len(status) != 2 || status[1].State != StateDisconnected {
		t.Fatal("disconnected catalog was executable or retained trust")
	}
	if diagnostic := pool.Catalogs(map[string]bool{"zz": true}); len(diagnostic) != 1 || len(diagnostic[0].Tools) != 40 {
		t.Fatal("disconnected diagnostic catalog disappeared")
	}
}

func TestPoolLargeCatalogCollisionDoesNotBorrowOtherServerTrust(t *testing.T) {
	first := newLargeCatalogFixture(t, catalogNames("bb_tool_", 60))
	last := newLargeCatalogFixture(t, append(catalogNames("tool_", 30), "zz_tail"))
	pool := newPool(t)
	pool.Reconcile(context.Background(), Config{Servers: map[string]*ServerConfig{
		"aa": {URL: first}, "aa_bb": {URL: last, Trust: true},
	}})
	if err := pool.WaitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	catalog, trusted, _ := pool.Snapshot()
	tail := pogotools.WireNameFor("aa_bb", "zz_tail")
	if len(catalog) != 61 || len(trusted) != 1 || trusted[0] != tail {
		t.Fatalf("collision or trust policy changed: tools=%d trusted=%v", len(catalog), trusted)
	}
	requireCatalogToolCall(t, catalog, "aa_bb", "zz_tail")
	requireCatalogToolCall(t, catalog, "aa", "bb_tool_000")
	selected, selectedTrusted, _ := pool.SnapshotServers(map[string]bool{"aa_bb": true})
	if len(selected) != 31 || len(selectedTrusted) != 31 {
		t.Fatalf("unselected server caused collisions or lost trusted tools: tools=%d trust=%d", len(selected), len(selectedTrusted))
	}
	requireCatalogToolCall(t, selected, "aa_bb", "tool_000")
}
