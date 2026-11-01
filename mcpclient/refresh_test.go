package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	pogotools "github.com/yosukeno/pogo/tools"
)

// Notifications are deliberately disabled: all pulls in these tests must come
// from Refresh, using the real HTTP transport and SDK pagination.
type refreshFixture struct {
	srv      *mcp.Server
	url      string
	requests atomic.Int64
	calls    atomic.Int64
	mu       sync.Mutex
	failPage bool
	cycle    bool
	entered  chan struct{}
	release  chan struct{}
}

func newRefreshFixture(t *testing.T) *refreshFixture {
	t.Helper()
	f := &refreshFixture{srv: mcp.NewServer(&mcp.Implementation{Name: "refresh-fixture"}, &mcp.ServerOptions{
		PageSize: 1, Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: false}},
	})}
	f.srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/list" {
				return next(ctx, method, req)
			}
			f.requests.Add(1)
			params := req.(*mcp.ListToolsRequest).Params
			f.mu.Lock()
			fail, cycle, entered, release := f.failPage, f.cycle, f.entered, f.release
			f.mu.Unlock()
			if release != nil {
				select {
				case entered <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			if fail && params.Cursor != "" {
				return nil, fmt.Errorf("temporary second-page failure")
			}
			if cycle {
				return &mcp.ListToolsResult{Tools: []*mcp.Tool{}, NextCursor: "same"}, nil
			}
			return next(ctx, method, req)
		}
	})
	f.add("a", "original", "text")
	f.add("b", "stable", "text")
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return f.srv }, nil))
	f.url = ts.URL
	t.Cleanup(ts.Close)
	return f
}

func (f *refreshFixture) add(name, description, arg string) {
	f.srv.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{arg: map[string]any{"type": "string"}}, "required": []string{arg},
	}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		f.calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: description + ":" + string(req.Params.Arguments)}}}, nil
	})
}

func (f *refreshFixture) pool(t *testing.T) *Pool {
	t.Helper()
	p := newPool(t)
	p.Reconcile(context.Background(), Config{Servers: map[string]*ServerConfig{"fx": {URL: f.url, Required: true}}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	return p
}

func expireCatalog(p *Pool) {
	for _, c := range p.conns {
		c.mu.Lock()
		c.lastAttempt = time.Now().Add(-CatalogRefreshTTL)
		c.mu.Unlock()
	}
}

func requireRefresh(t *testing.T, p *Pool) {
	t.Helper()
	expireCatalog(p)
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func projectedTool(t *testing.T, p *Pool, name string) pogotools.Tool {
	t.Helper()
	ts, _, _ := p.Snapshot()
	for _, tool := range ts {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("missing tool %s in %v", name, toolNames(ts))
	return nil
}

func TestPoolRefreshPaginationFailureAndEmpty(t *testing.T) {
	f := newRefreshFixture(t)
	p := f.pool(t)
	old := projectedTool(t, p, "mcp_fx_a")
	if f.requests.Load() != 2 {
		t.Fatalf("initial pagination: %d requests", f.requests.Load())
	}
	f.add("a", "changed", "value")
	f.add("c", "added", "value")
	f.mu.Lock()
	f.failPage = true
	f.mu.Unlock()
	expireCatalog(p)
	if err := p.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "second-page") {
		t.Fatalf("refresh = %v", err)
	}
	ts, _, st := p.Snapshot()
	if len(ts) != 2 || projectedTool(t, p, "mcp_fx_a") != old || st[0].State != StateConnected || !st[0].CatalogStale || st[0].LastError == "" {
		t.Fatalf("failed pull replaced valid catalog or lost status: %v %+v", toolNames(ts), st)
	}
	count := f.requests.Load()
	_ = p.Refresh(context.Background())
	if f.requests.Load() != count {
		t.Fatal("failed attempt did not consume TTL")
	}
	f.mu.Lock()
	f.failPage = false
	f.mu.Unlock()
	requireRefresh(t, p)
	ts, _, st = p.Snapshot()
	if len(ts) != 3 || st[0].CatalogStale || st[0].LastError != "" {
		t.Fatalf("successful retry = %v %+v", toolNames(ts), st)
	}
	if _, err := old.Execute(context.Background(), json.RawMessage(`{"text":"old"}`)); err == nil || !strings.Contains(err.Error(), "not sent") {
		t.Fatalf("stale binding call = %v", err)
	}
	if f.calls.Load() != 0 {
		t.Fatal("stale binding reached the server")
	}
	f.srv.RemoveTools("a", "b", "c")
	requireRefresh(t, p)
	ts, trusted, st := p.Snapshot()
	if len(ts) != 0 || len(trusted) != 0 || st[0].Tools != 0 || st[0].CatalogStale || st[0].State != StateConnected {
		t.Fatalf("successful empty list = %v %v %+v", ts, trusted, st)
	}
}

func TestPoolRefreshCoalescesAndPublishesAtomically(t *testing.T) {
	f := newRefreshFixture(t)
	p := f.pool(t)
	old := projectedTool(t, p, "mcp_fx_a")
	for range 4 {
		if err := p.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if f.requests.Load() != 2 {
		t.Fatal("fresh pool was polled")
	}
	f.add("c", "added", "text")
	f.mu.Lock()
	f.entered, f.release = make(chan struct{}, 1), make(chan struct{})
	entered, release := f.entered, f.release
	f.mu.Unlock()
	expireCatalog(p)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Go(func() { errs <- p.Refresh(context.Background()) })
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not start")
	}
	if ts, _, _ := p.Snapshot(); len(ts) != 2 {
		t.Fatal("partial catalog was published")
	}
	// A waiter has its own cancellation budget, independent of the pull owner.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	if err := p.Refresh(ctx); err == nil {
		t.Fatal("blocked refresh ignored waiter deadline")
	}
	cancel()
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.requests.Load() != 5 {
		t.Fatalf("concurrent refreshes fetched %d pages, want 2 initial + 3 once", f.requests.Load())
	}
	if projectedTool(t, p, "mcp_fx_a") != old {
		t.Fatal("unchanged tool lost its execution binding")
	}
	if _, err := old.Execute(context.Background(), json.RawMessage(`{"text":"safe"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestPoolRefreshDeadlineAndMalformedPagination(t *testing.T) {
	f := newRefreshFixture(t)
	p := f.pool(t)
	old := projectedTool(t, p, "mcp_fx_a")
	f.mu.Lock()
	f.entered, f.release = make(chan struct{}, 1), make(chan struct{})
	f.mu.Unlock()
	expireCatalog(p)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	start := time.Now()
	err := p.Refresh(ctx)
	cancel()
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("deadline: %v after %s", err, time.Since(start))
	}
	if projectedTool(t, p, "mcp_fx_a") != old {
		t.Fatal("timeout discarded valid catalog")
	}
	f.mu.Lock()
	close(f.release)
	f.release = nil
	f.cycle = true
	f.mu.Unlock()
	expireCatalog(p)
	if err := p.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "repeated cursor") {
		t.Fatalf("cursor cycle = %v", err)
	}
	if projectedTool(t, p, "mcp_fx_a") != old {
		t.Fatal("invalid pagination replaced valid catalog")
	}
}

func TestPoolRefreshDefinitionAndConnectionBindings(t *testing.T) {
	f := newRefreshFixture(t)
	p := f.pool(t)
	old := projectedTool(t, p, "mcp_fx_a")
	stable := projectedTool(t, p, "mcp_fx_b")
	f.add("a", "revised", "value")
	requireRefresh(t, p)
	next := projectedTool(t, p, "mcp_fx_a")
	if old.Description() != "original" || next.Description() != "revised" || next.InputSchema()["properties"].(map[string]any)["value"] == nil {
		t.Fatal("definition snapshot was not replaced correctly")
	}
	if projectedTool(t, p, "mcp_fx_b") != stable {
		t.Fatal("unrelated change invalidated stable tool")
	}
	if _, err := old.Execute(context.Background(), json.RawMessage(`{"text":"x"}`)); err == nil {
		t.Fatal("old definition was executed")
	}
	if _, err := next.Execute(context.Background(), json.RawMessage(`{"value":"x"}`)); err != nil {
		t.Fatal(err)
	}
	// Force a real session reconnect, without reconfiguring or restarting pogo.
	c := p.conns["fx"]
	oldSession := c.session.Load()
	_ = oldSession.Close()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if c.session.Load() != nil && c.session.Load() != oldSession && c.status().State == StateConnected {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c.session.Load() == nil || c.session.Load() == oldSession {
		t.Fatal("keeper did not reconnect")
	}
	if _, err := next.Execute(context.Background(), json.RawMessage(`{"value":"x"}`)); err == nil || !strings.Contains(err.Error(), "connection was replaced") {
		t.Fatalf("old connection call = %v", err)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("old call was replayed or redirected: %d", f.calls.Load())
	}
	if _, err := projectedTool(t, p, "mcp_fx_a").Execute(context.Background(), json.RawMessage(`{"value":"new"}`)); err != nil {
		t.Fatal(err)
	}
}
