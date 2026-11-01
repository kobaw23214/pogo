package web

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpclient"
	"github.com/yosukeno/pogo/mcpobserve"
)

// observeRecorder is the mcpobserve.Recorder fake for this package: it
// collects snapshots synchronously, so no flush/poll is involved and an
// assertion right after the call is exact.
type observeRecorder struct {
	mu    sync.Mutex
	calls []mcpobserve.Call
}

func (r *observeRecorder) Record(c mcpobserve.Call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

func (r *observeRecorder) snapshot() []mcpobserve.Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mcpobserve.Call(nil), r.calls...)
}

// finals collapses the snapshots to one completed record per call id.
func (r *observeRecorder) finals() []mcpobserve.Call {
	byID := map[string]mcpobserve.Call{}
	var order []string
	for _, c := range r.snapshot() {
		if _, seen := byID[c.ID]; !seen {
			order = append(order, c.ID)
		}
		byID[c.ID] = c
	}
	out := make([]mcpobserve.Call, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

func (r *observeRecorder) finalForTool(tool string) *mcpobserve.Call {
	for _, c := range r.finals() {
		if c.Tool == tool {
			copy := c
			return &copy
		}
	}
	return nil
}

func TestManagerMCPObserveRecorderLifecycle(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("ok")))
	if h.mgr.MCPRecorder() == nil {
		t.Fatal("a Web server must observe outbound calls even without MCP serving")
	}
	rec := &observeRecorder{}
	h.mgr.SetMCPRecorder(rec)
	if h.mgr.MCPRecorder() != rec {
		t.Fatal("installed recorder not returned")
	}
	// Concurrent readers while a swap happens — the crash this guards is a
	// torn pointer, so the test only needs to race, not to assert values.
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				_ = h.mgr.MCPRecorder()
			}
		}
	}()
	for i := 0; i < 200; i++ {
		h.mgr.SetMCPRecorder(rec)
		h.mgr.SetMCPRecorder(nil)
	}
	close(done)
	if h.mgr.MCPRecorder() != nil {
		t.Fatal("nil install must turn observation off")
	}
}

// connectInMemory runs a real SDK client against the layer's server over the
// loopback transport: the middleware under test sees exactly the request
// shapes a remote client produces.
func connectInMemory(t *testing.T, srv *mcp.Server, clientName string) *mcp.ClientSession {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: clientName}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestMCPObserveInboundMiddleware(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("ok")))
	rec := &observeRecorder{}
	h.mgr.SetMCPRecorder(rec)
	l := newMCPLayer(h.mgr, MCPOptions{}, false, &mcpclient.ServeConfig{Enabled: true})
	cs := connectInMemory(t, l.server(), "observe-client")

	ctx := context.Background()
	// Protocol housekeeping is not an execution and must not be recorded.
	if err := cs.Ping(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.ListTools(ctx, nil); err != nil {
		t.Fatal(err)
	}

	// A normal call: the client name comes from the handshake.
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "agent_task_list", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("list is always a result: %s", resultText(res))
	}

	// A tool-level failure naming an unknown task: its untrusted argument must
	// not become local correlation metadata.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "agent_task_status", Arguments: map[string]any{"task_id": "sess-x"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("unknown task must be a tool error")
	}

	// Missing required argument: also a tool error, not a protocol error.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "agent_task_status", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("missing task_id must be a tool error")
	}

	// An unknown name: protocol error from the SDK's dispatch.
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "no_such_tool", Arguments: map[string]any{}}); err == nil {
		t.Fatal("unknown tool must fail")
	}

	calls := rec.finals()
	if len(calls) != 4 {
		t.Fatalf("recorded %d calls, want exactly the 4 tools/call (no ping/list): %+v", len(calls), calls)
	}

	ok := calls[0]
	if ok.Direction != "inbound" || ok.Server != "pogo" || ok.Tool != "agent_task_list" || ok.WireTool != "agent_task_list" {
		t.Fatalf("identity = %+v", ok)
	}
	if ok.Status != "ok" || ok.ErrorKind != "" {
		t.Fatalf("status = %+v, want ok", ok)
	}
	if ok.Client != "observe-client" {
		t.Fatalf("client = %q, want observe-client", ok.Client)
	}
	if ok.StartedAt <= 0 || ok.FinishedAt < ok.StartedAt {
		t.Fatalf("bad timing: %+v", ok)
	}

	toolErr := calls[1]
	if toolErr.Status != "error" || toolErr.ErrorKind != "tool_error" {
		t.Fatalf("tool error = %+v, want error/tool_error", toolErr)
	}
	if toolErr.TaskID != "" || toolErr.SessionID != "" {
		t.Fatalf("unknown task was falsely attributed: %+v", toolErr)
	}

	missing := calls[2]
	if missing.Status != "error" || missing.ErrorKind != "tool_error" {
		t.Fatalf("missing-arg error = %+v, want error/tool_error", missing)
	}

	unknown := calls[3]
	if unknown.Tool != "no_such_tool" || unknown.Status != "error" || unknown.ErrorKind != "protocol_error" {
		t.Fatalf("unknown tool = %+v, want error/protocol_error", unknown)
	}
	for _, c := range calls {
		if c.TraceID == "" || c.ID == "" {
			t.Fatalf("record without ids: %+v", c)
		}
	}
}

func TestMCPObserveInboundAllowlistRejected(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("ok")))
	rec := &observeRecorder{}
	h.mgr.SetMCPRecorder(rec)
	l := newMCPLayer(h.mgr, MCPOptions{}, false, &mcpclient.ServeConfig{
		Enabled: true, IncludeTools: []string{"agent_task_list"},
	})
	cs := connectInMemory(t, l.server(), "observe-client")
	ctx := context.Background()

	// Refused by the include_tools whitelist — and still recorded, because the
	// observe middleware wraps the whitelist, not vice versa.
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "agent_task_status", Arguments: map[string]any{"task_id": "x"}}); err == nil {
		t.Fatal("whitelisted-out tool must be refused")
	}
	// The allowed one works.
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "agent_task_list", Arguments: map[string]any{}}); err != nil {
		t.Fatal(err)
	}

	rejected := rec.finalForTool("agent_task_status")
	if rejected == nil {
		t.Fatal("whitelist refusal was not recorded")
	}
	if rejected.Status != "error" || rejected.ErrorKind != "rejected" {
		t.Fatalf("refusal = %+v, want error/rejected", *rejected)
	}
	allowed := rec.finalForTool("agent_task_list")
	if allowed == nil || allowed.Status != "ok" {
		t.Fatalf("allowed call = %+v, want ok", allowed)
	}
}

// ctxCaptureClient records the observation metadata and recorder reachability
// of the exact context the run drives the model with — the context the run's
// MCP tools will derive theirs from.
type ctxCaptureClient struct {
	model string
	mu    sync.Mutex
	mds   []mcpobserve.Metadata
	probe *observeRecorder
}

func (c *ctxCaptureClient) Model() string { return c.model }

func (c *ctxCaptureClient) Stream(ctx context.Context, _ string, _ []llm.Message, _ []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	c.mu.Lock()
	c.mds = append(c.mds, mcpobserve.MetadataFrom(ctx))
	c.mu.Unlock()
	// Reaching the recorder from the run context is the property the runner
	// owes MCP tools; a one-off probe span proves it without a pool.
	if c.probe != nil {
		_, span := mcpobserve.Start(ctx, mcpobserve.Call{Direction: "outbound", Tool: "ctx_probe"})
		span.End("ok", "")
	}
	return textTurn("hi"), nil
}

func (c *ctxCaptureClient) last() mcpobserve.Metadata {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mds[len(c.mds)-1]
}

func newObserveManager(t *testing.T, client llm.Client) *Manager {
	t.Helper()
	t.Setenv("KIMI_API_KEY", "test-key")
	installTestCatalog(t)
	mgr, err := NewManager(Config{
		Cwd: t.TempDir(), SessionDir: t.TempDir(), Model: "k3", MaxTurns: 5,
		GateTimeout: 5 * time.Second, RunTimeout: time.Minute, IdleTimeout: time.Hour,
		NewClient: func(config.Resolved, func(llm.RetryInfo)) llm.Client { return client },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	return mgr
}

func waitRunDone(t *testing.T, sess *Session) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for sess.Active() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if sess.Active() {
		t.Fatal("run did not finish")
	}
}

func TestSessionStartObservedMetadata(t *testing.T) {
	probe := &observeRecorder{}
	client := &ctxCaptureClient{model: "k3", probe: probe}
	mgr := newObserveManager(t, client)
	mgr.SetMCPRecorder(probe)

	sess, err := mgr.Create("", "")
	if err != nil {
		t.Fatal(err)
	}
	// Forged caller claims about session/run/owner must lose to the session's
	// authoritative values; trace/parent/task are the caller's to continue.
	runID, err := sess.StartObserved("hi", nil, "", mcpobserve.Metadata{
		TraceID: "trace-x", ParentID: "inbound-1", TaskID: "task-1",
		SessionID: "FORGED", RunID: "FORGED", OwnerID: "FORGED",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRunDone(t, sess)

	md := client.last()
	if md.TraceID != "trace-x" || md.ParentID != "inbound-1" || md.TaskID != "task-1" {
		t.Fatalf("inbound correlation not continued: %+v", md)
	}
	if md.SessionID != sess.ID || md.RunID != runID {
		t.Fatalf("authoritative ids forged: session=%q run=%q, want %q/%q", md.SessionID, md.RunID, sess.ID, runID)
	}
	if md.OwnerID != "" {
		t.Fatalf("owner must come from the local session (unowned here), got %q", md.OwnerID)
	}
	if len(probe.finals()) == 0 {
		t.Fatal("run context does not reach the manager's recorder")
	}

	// A plain Start opens a fresh trace and keeps working with no recorder.
	mgr.SetMCPRecorder(nil)
	runID2, err := sess.Start("again", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waitRunDone(t, sess)
	md2 := client.last()
	if md2.TraceID == "" || md2.TraceID == "trace-x" {
		t.Fatalf("plain Start must open a fresh trace: %+v", md2)
	}
	if md2.ParentID != "" || md2.TaskID != "" {
		t.Fatalf("plain Start must not inherit caller correlation: %+v", md2)
	}
	if md2.SessionID != sess.ID || md2.RunID != runID2 || runID2 == runID {
		t.Fatalf("second run ids wrong: %+v (run %q, first %q)", md2, runID2, runID)
	}
}

// The MCP submit path, end to end over HTTP: the inbound record must carry the
// session/run/task the submission allocated, and a deduplicated replay must
// correlate to the existing task without starting anything.
func TestMCPObserveSubmitCorrelation(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("done")))
	// CallsOff keeps NewServer from installing its store recorder; the fake
	// takes its place so assertions read snapshots, not a database.
	srv, err := NewServer(h.mgr, ServerOptions{
		Token:  h.token,
		MCP:    &MCPOptions{},
		MCPLog: &MCPLogOptions{CallsOff: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	rec := &observeRecorder{}
	h.mgr.SetMCPRecorder(rec)

	cs := connectStreamable(t, ts.URL, h.token)
	res := callTool(t, cs, "agent_submit_task", map[string]any{"prompt": "do it", "client_ref": "obs-dedup"})
	sc := structured(t, res)
	sid, _ := sc["session_id"].(string)
	runID, _ := sc["run_id"].(string)
	if sid == "" || runID == "" {
		t.Fatalf("submit ids missing: %#v", sc)
	}
	pollStatus(t, cs, sid)

	// The replay resolves to the same task and starts no second run.
	again := structured(t, callTool(t, cs, "agent_submit_task", map[string]any{"prompt": "do it", "client_ref": "obs-dedup"}))
	if again["deduplicated"] != true || again["task_id"] != sid {
		t.Fatalf("dedup = %#v", again)
	}

	var submits []mcpobserve.Call
	for _, c := range rec.finals() {
		if c.Tool == "agent_submit_task" {
			submits = append(submits, c)
		}
	}
	if len(submits) != 2 {
		t.Fatalf("submit records = %d, want 2 (submit + dedup replay)", len(submits))
	}
	first, replay := submits[0], submits[1]
	if first.Status != "ok" || first.SessionID != sid || first.RunID != runID || first.TaskID != sid {
		t.Fatalf("submit correlation = %+v, want session/run/task of the new task", first)
	}
	if first.TraceID == "" || first.Client != "mcp-test" || first.MCPSessionID == "" {
		t.Fatalf("submit inbound metadata incomplete: %+v", first)
	}
	if replay.Status != "ok" || replay.TaskID != sid {
		t.Fatalf("dedup correlation = %+v, want ok with the existing task id", replay)
	}
	if replay.RunID != runID || replay.SessionID != sid {
		t.Fatalf("dedup must reference the original run/session without allocating another: %+v", replay)
	}

	status := rec.finalForTool("agent_task_status")
	if status == nil || status.TaskID != sid || status.Status != "ok" {
		t.Fatalf("status poll correlation = %+v", status)
	}
}
