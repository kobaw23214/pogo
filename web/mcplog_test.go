package web

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *mcpLogStore {
	t.Helper()
	st, err := newMCPLogStore(MCPLogOptions{Ring: 8}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// The middleware must record every verdict with the right classification —
// the SDK's rejection strings are the anchors the diagnosis matches on, so
// each family gets its own case.
func TestMCPLogMiddlewareClassifiesRejections(t *testing.T) {
	st := newTestStore(t)
	cases := []struct {
		name        string
		handler     http.HandlerFunc
		body        string
		wantVerdict string
		wantStage   string
		wantCode    string
	}{
		{
			name: "ok", wantVerdict: mcpVerdictOK,
			handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) },
			body:    `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"agent_task_status"}}`,
		},
		{
			name: "accept header", wantVerdict: mcpVerdictRejected, wantStage: mcpStageHeaders, wantCode: mcpDiagAcceptHeaderMissing,
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "Accept must contain both 'application/json' and 'text/event-stream'", 400)
			},
		},
		{
			name: "protocol version", wantVerdict: mcpVerdictRejected, wantStage: mcpStageProto, wantCode: mcpDiagProtocolVersion,
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "Bad Request: Unsupported protocol version (supported versions: 2025-06-18)", 400)
			},
		},
		{
			name: "invalid json", wantVerdict: mcpVerdictRejected, wantStage: mcpStageBody, wantCode: mcpDiagInvalidJSON,
			handler: func(w http.ResponseWriter, r *http.Request) {
				// The go-sdk's actual wrapper text, captured live from v1.7.0.
				http.Error(w, `malformed payload: unmarshaling jsonrpc message: json: expected '"' at the beginning of a value`, 400)
			},
		},
		{
			name: "server error", wantVerdict: mcpVerdictError, wantStage: mcpStageHandler, wantCode: mcpDiagHandlerError,
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "boom", 500)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := st.wrapTransport("streamable", tc.handler)
			req := httptest.NewRequest("POST", "/mcp", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = "9.9.9.9:1"
			h.ServeHTTP(httptest.NewRecorder(), req)
			got := st.snapshot(1, "", "", "")
			if len(got) != 1 {
				t.Fatalf("snapshot: got %d entries", len(got))
			}
			e := got[0]
			if e.Verdict != tc.wantVerdict || e.Stage != tc.wantStage || e.DiagCode != tc.wantCode {
				t.Fatalf("classification = %s/%s/%s, want %s/%s/%s", e.Verdict, e.Stage, e.DiagCode, tc.wantVerdict, tc.wantStage, tc.wantCode)
			}
			if e.Transport != "streamable" {
				t.Fatalf("transport = %s", e.Transport)
			}
			if e.Status >= 400 && e.RespBody == "" {
				t.Fatal(">=400 did not capture the rejection body")
			}
		})
		st = newTestStore(t) // isolate cases
	}
}

// The probe must pull the JSON-RPC classification fields out of the request,
// and the payload knob must gate the body head — sizes always, contents only
// when payload logging is on.
func TestMCPLogProbesAndPayloadToggle(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"acme-platform","version":"2.1"}}}`
	secret := `{"prompt":"key sk-ABCDEFGHIJKLMNOP123456 and 34b6b4203a5a5d10222717fa18e0317 here"}`

	st := newTestStore(t) // payload on
	h := st.wrapTransport("sse", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(202)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/sse?sessionid=abc", strings.NewReader(body)))
	e := st.snapshot(1, "", "", "")[0]
	if e.JSONRPCMethod != "initialize" || e.ProtocolVersion != "2025-11-25" || e.ClientName != "acme-platform 2.1" {
		t.Fatalf("probe fields: %+v", e)
	}
	if e.SessionID != "abc" {
		t.Fatalf("session from query: %q", e.SessionID)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/sse", strings.NewReader(secret)))
	e = st.snapshot(1, "", "", "")[0]
	if strings.Contains(e.ReqHead, "sk-ABCDEFGHIJKLMNOP123456") || strings.Contains(e.ReqHead, "34b6b4203a5a5d10222717fa18e0317") {
		t.Fatalf("secret leaked into req_head: %q", e.ReqHead)
	}
	if !strings.Contains(e.ReqHead, "[redacted]") {
		t.Fatalf("redaction marker missing: %q", e.ReqHead)
	}

	stOff, err := newMCPLogStore(MCPLogOptions{Ring: 4, PayloadOff: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h = stOff.wrapTransport("sse", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(202)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/sse", strings.NewReader(secret)))
	e = stOff.snapshot(1, "", "", "")[0]
	if e.ReqHead != "" {
		t.Fatalf("payload off still captured: %q", e.ReqHead)
	}
	if e.ReqBytes != len(secret) {
		t.Fatalf("size always recorded: %d", e.ReqBytes)
	}
}

// Gate rejections never reach the transport middleware, so withToken records
// them via recordGate — 401/404/503 each carry their own diagnosis.
func TestMCPLogRecordGate(t *testing.T) {
	st := newTestStore(t)
	for _, tc := range []struct {
		status int
		code   string
	}{
		{401, mcpDiagAuthFailed},
		{404, mcpDiagDisabled},
		{503, mcpDiagAuthMisconfigured},
	} {
		req := httptest.NewRequest("GET", "/mcp", nil)
		req.RemoteAddr = "10.0.0.5:2"
		st.recordGate(req, tc.status, tc.code)
	}
	got := st.snapshot(10, "", "", "")
	if len(got) != 3 {
		t.Fatalf("gate records: %d", len(got))
	}
	if got[0].DiagCode != mcpDiagAuthMisconfigured || got[2].DiagCode != mcpDiagAuthFailed {
		t.Fatalf("newest-first order broken: %+v", got)
	}
}

func TestMCPLogRingBoundsFiltersAndClear(t *testing.T) {
	st := newTestStore(t) // ring 8
	for i := 0; i < 12; i++ {
		v := mcpVerdictOK
		if i%3 == 0 {
			v = mcpVerdictRejected
		}
		st.record(&MCPCallLog{Verdict: v, JSONRPCMethod: "tools/call",
			Tool: "agent_submit_task", Transport: "streamable"})
	}
	if got := st.snapshot(100, "", "", ""); len(got) != 8 {
		t.Fatalf("ring bound: %d", len(got))
	}
	// Ring kept entries 4..11; rejected among those: i=6 and i=9.
	if got := st.snapshot(100, mcpVerdictRejected, "", ""); len(got) != 2 {
		t.Fatalf("verdict filter: %d", len(got))
	}
	if got := st.snapshot(100, "", "sse", ""); len(got) != 0 {
		t.Fatalf("transport filter: %d", len(got))
	}
	if got := st.snapshot(100, "", "", "submit"); len(got) != 8 {
		t.Fatalf("text filter: %d", len(got))
	}
	st.clear("screen")
	if got := st.snapshot(100, "", "", ""); len(got) != 0 {
		t.Fatalf("clear screen: %d", len(got))
	}
}

// History persistence: the async writer must drain into SQLite and the
// dual-bound sweeper must enforce the row ceiling.
func TestMCPLogPersistsAndSweeps(t *testing.T) {
	db := filepath.Join(t.TempDir(), "mcplog.db")
	st, err := newMCPLogStore(MCPLogOptions{Ring: 16, MaxRows: 3, RetainDays: 0}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.close()
	st, err = newMCPLogStore(MCPLogOptions{Ring: 16, MaxRows: 3, RetainDays: 0, DBPath: db}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	for i := 0; i < 5; i++ {
		st.record(&MCPCallLog{Verdict: mcpVerdictOK, Transport: "streamable",
			HTTPMethod: "POST", Status: 200, ReqID: "r", DurationMs: 1})
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st.stats().Total == 5 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	st.sweepOnce(nil) // the 6h ticker will not fire inside a test
	got := st.stats()
	if got.Total != 3 {
		t.Fatalf("row ceiling: total=%d (want 3 after sweep)", got.Total)
	}
	if got.ByVerdict[mcpVerdictOK] != 3 || got.ByTransport["streamable"] != 3 {
		t.Fatalf("aggregates: %+v", got)
	}
	if n := st.clear("all"); n != 3 {
		t.Fatalf("clear all: %d", n)
	}
	if st.stats().Total != 0 {
		t.Fatal("clear all did not empty history")
	}
}

// The 405 shim: a GET without a session must be answered 405 (the spec's
// menu for "no sessionless push stream") and classified in the log, while a
// sessioned GET passes through to the real handler untouched.
func TestSessionlessGETCompatibilityShim(t *testing.T) {
	st := newTestStore(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := st.wrapTransport("streamable", sessionlessGETCompatibility(next))

	// 1. Bare GET (the platform's opening move) -> 405 with Allow, logged.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/mcp", nil)
	req.Header.Set("Accept", "text/event-stream")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("bare GET: status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow == "" {
		t.Fatal("405 without an Allow header")
	}
	e := st.snapshot(1, "", "", "")[0]
	if e.Status != 405 || e.DiagCode != mcpDiagGetWithoutSession || e.Stage != mcpStageSession {
		t.Fatalf("classification: status=%d code=%s stage=%s", e.Status, e.DiagCode, e.Stage)
	}

	// 2. Sessioned GET -> passes through (200 from next).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/mcp", nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", "abc123")
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("sessioned GET: status = %d, want passthrough 200", rec.Code)
	}

	// 3. POST is never the shim's business.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)))
	if rec.Code != 200 {
		t.Fatalf("POST: status = %d, want passthrough", rec.Code)
	}
}

// Response capture is for every verdict now, success included: a plain JSON
// 200 must show its body, an SSE stream must yield its opening frames, and a
// body past the cap must stop buffering without breaking the response.
func TestMCPLogCapturesResponseBodyOnSuccess(t *testing.T) {
	st := newTestStore(t)

	json200 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`))
	})
	h := st.wrapTransport("streamable", json200)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`)))
	e := st.snapshot(1, "", "", "")[0]
	if e.Verdict != mcpVerdictOK || !strings.Contains(e.RespBody, "protocolVersion") {
		t.Fatalf("success body not captured: verdict=%s resp=%q", e.Verdict, e.RespBody)
	}
	if e.RespTruncated {
		t.Fatal("small body marked truncated")
	}

	stream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte("event: message\ndata: {\"answer\":1}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		w.Write([]byte("event: message\ndata: {\"answer\":2}\n\n"))
	})
	h = st.wrapTransport("streamable", stream)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/mcp", strings.NewReader(`{}`)))
	e = st.snapshot(1, "", "", "")[0]
	if !strings.Contains(e.RespBody, "answer") {
		t.Fatalf("stream opening frame not captured: %q", e.RespBody)
	}

	big := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write(make([]byte, mcpLogRespCap*4))
	})
	h = st.wrapTransport("streamable", big)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/mcp", strings.NewReader(`{}`)))
	if rec.Body.Len() != mcpLogRespCap*4 {
		t.Fatalf("response was mangled by the capture: %d bytes", rec.Body.Len())
	}
	e = st.snapshot(1, "", "", "")[0]
	if !e.RespTruncated || len(e.RespBody) > mcpLogRespCap+64 {
		t.Fatalf("cap not enforced: truncated=%v len=%d", e.RespTruncated, len(e.RespBody))
	}
}

// The log layer samples; it must never cap the request. A >300KiB tools/call
// body (well past mcpLogReadCap) has to reach the handler byte-for-byte, and
// the entry records the full size with the head marked truncated.
func TestMCPLogLargeBodyPassesThroughIntact(t *testing.T) {
	st := newTestStore(t)

	// A legal JSON-RPC tools/call whose argument is a ~300KiB string.
	big := strings.Repeat("a", 300<<10)
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"echo","arguments":{"blob":"%s"}}}`, big)

	var got []byte
	h := st.wrapTransport("streamable", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		got, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("handler read: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":7,"result":{"content":[{"type":"text","text":"ok"}],"isError":false}}`))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/mcp", strings.NewReader(body)))

	if !bytes.Equal(got, []byte(body)) {
		t.Fatalf("body mangled by the log layer: got %d bytes, equal=%v", len(got), bytes.Equal(got, []byte(body)))
	}
	if rec.Body.Len() == 0 {
		t.Fatal("no response written")
	}
	e := st.snapshot(1, "", "", "")[0]
	if e.ReqBytes != len(body) {
		t.Fatalf("req_bytes = %d, want full %d", e.ReqBytes, len(body))
	}
	if !e.reqTruncated {
		t.Fatal("oversized sample not marked truncated")
	}
	if e.Verdict != mcpVerdictOK || e.ToolOutcome != mcpOutcomeOK {
		t.Fatalf("large call should still classify as tool ok: verdict=%s tool=%s", e.Verdict, e.ToolOutcome)
	}
}

// Close and read errors belong to the handler: the wrapper must delegate
// Close to the original body exactly once, and surface read errors
// unchanged instead of swallowing them.
func TestMCPLogBodyCloseAndReadError(t *testing.T) {
	st := newTestStore(t)

	trk := &trackingBody{Reader: strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)}
	h := st.wrapTransport("streamable", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if err := r.Body.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
		w.WriteHeader(200)
	}))
	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Body = trk
	h.ServeHTTP(httptest.NewRecorder(), req)
	if trk.closed != 1 {
		t.Fatalf("underlying Close called %d times, want exactly 1", trk.closed)
	}

	st2 := newTestStore(t)
	boom := fmt.Errorf("boom")
	fail := &failingBody{err: boom}
	var handlerErr error
	h2 := st2.wrapTransport("streamable", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, handlerErr = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	req2 := httptest.NewRequest("POST", "/mcp", nil)
	req2.Body = fail
	h2.ServeHTTP(httptest.NewRecorder(), req2)
	if handlerErr != boom {
		t.Fatalf("read error = %v, want it surfaced unchanged", handlerErr)
	}
	if len(st2.snapshot(1, "", "", "")) != 1 {
		t.Fatal("errored request was not logged")
	}
}

type trackingBody struct {
	io.Reader
	closed int
}

func (b *trackingBody) Close() error { b.closed++; return nil }

type failingBody struct{ err error }

func (b *failingBody) Read([]byte) (int, error) { return 0, b.err }
func (b *failingBody) Close() error             { return nil }

// Semantic classification: a 2xx is transport success only. JSON-RPC error
// objects and isError:true results flip the verdict, JSON and SSE answers
// alike; notifications and the handshake are never tool successes; a
// truncated head admits unknown.
func TestMCPLogSemanticClassification(t *testing.T) {
	callBody := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"agent_task_status","arguments":{}}}`
	cases := []struct {
		name        string
		reqBody     string
		contentType string
		resp        string
		wantVerdict string
		wantCode    string
		wantProto   string
		wantTool    string
	}{
		{
			name: "json rpc error", reqBody: callBody, contentType: "application/json",
			resp:        `{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"tool not found: agent_task_status"}}`,
			wantVerdict: mcpVerdictError, wantCode: mcpDiagJSONRPCError,
			wantProto: mcpOutcomeError, wantTool: mcpOutcomeError,
		},
		{
			name: "tool isError", reqBody: callBody, contentType: "application/json",
			resp:        `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"task exploded"}],"isError":true}}`,
			wantVerdict: mcpVerdictError, wantCode: mcpDiagToolError,
			wantProto: mcpOutcomeOK, wantTool: mcpOutcomeError,
		},
		{
			name: "tool ok", reqBody: callBody, contentType: "application/json",
			resp:        `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"running"}],"isError":false}}`,
			wantVerdict: mcpVerdictOK,
			wantProto:   mcpOutcomeOK, wantTool: mcpOutcomeOK,
		},
		{
			name: "sse answer among frames", reqBody: callBody, contentType: "text/event-stream",
			resp: "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n" +
				"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"isError\":true,\"content\":[{\"type\":\"text\",\"text\":\"no\"}]}}\n\n",
			wantVerdict: mcpVerdictError, wantCode: mcpDiagToolError,
			wantProto: mcpOutcomeOK, wantTool: mcpOutcomeError,
		},
		{
			name: "sse without matching id", reqBody: callBody, contentType: "text/event-stream",
			resp:        "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":99,\"result\":{}}\n\n",
			wantVerdict: mcpVerdictOK,
			wantProto:   mcpOutcomeUnknown, wantTool: mcpOutcomeUnknown,
		},
		{
			name: "truncated head is unknown", reqBody: callBody, contentType: "application/json",
			resp:        `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"` + strings.Repeat("x", mcpLogRespCap) + ``,
			wantVerdict: mcpVerdictOK,
			wantProto:   mcpOutcomeUnknown, wantTool: mcpOutcomeUnknown,
		},
		{
			name: "notification is not a tool success", reqBody: `{"jsonrpc":"2.0","method":"notifications/initialized"}`,
			contentType: "text/plain", resp: "",
			wantVerdict: mcpVerdictOK,
		},
		{
			name: "handshake is not a tool success", reqBody: `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`,
			contentType: "application/json",
			resp:        `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"pogo","version":"dev"}}}`,
			wantVerdict: mcpVerdictOK,
			wantProto:   mcpOutcomeOK, // protocol answered; tool stays empty
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			h := st.wrapTransport("streamable", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.WriteHeader(200)
				io.WriteString(w, tc.resp)
			}))
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/mcp", strings.NewReader(tc.reqBody)))
			e := st.snapshot(1, "", "", "")[0]
			if e.Verdict != tc.wantVerdict || e.DiagCode != tc.wantCode ||
				e.ProtoOutcome != tc.wantProto || e.ToolOutcome != tc.wantTool {
				t.Fatalf("got verdict=%s code=%s proto=%s tool=%s, want %s/%s/%s/%s",
					e.Verdict, e.DiagCode, e.ProtoOutcome, e.ToolOutcome,
					tc.wantVerdict, tc.wantCode, tc.wantProto, tc.wantTool)
			}
		})
	}
}

// PayloadOff silences BOTH directions but keeps the structured diagnosis:
// verdict/diag/outcome fields are computed from the in-memory capture, and
// an error.message that echoes caller data never reaches the log.
func TestMCPLogPayloadOffSuppressesBothDirections(t *testing.T) {
	st, err := newMCPLogStore(MCPLogOptions{Ring: 4, PayloadOff: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	secret := "attacker-controlled-text-xyz"
	h := st.wrapTransport("streamable", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":3,"error":{"code":-32602,"message":"bad argument near ` + secret + `"}}`))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"t","arguments":{"q":"`+secret+`"}}}`)))
	e := st.snapshot(1, "", "", "")[0]
	if e.ReqHead != "" || e.RespBody != "" {
		t.Fatalf("payload off leaked raw text: req=%q resp=%q", e.ReqHead, e.RespBody)
	}
	// The machine classification survives without any payload.
	if e.Verdict != mcpVerdictError || e.DiagCode != mcpDiagJSONRPCError ||
		e.ProtoOutcome != mcpOutcomeError || e.ToolOutcome != mcpOutcomeError {
		t.Fatalf("classification lost with payload off: %+v", e)
	}
	if e.ReqBytes == 0 {
		t.Fatal("size accounting lost with payload off")
	}
}

// The history schema gains the outcome columns on an existing database.
func TestMCPLogMigrationAddsOutcomeColumns(t *testing.T) {
	db := filepath.Join(t.TempDir(), "mcp.db")
	st, err := newMCPLogStore(MCPLogOptions{Ring: 4, DBPath: db}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.close()
	// Reproduce a database from before the two outcome columns existed.
	legacy, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`ALTER TABLE mcp_calls DROP COLUMN proto_outcome; ALTER TABLE mcp_calls DROP COLUMN tool_outcome;`); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	legacy.Close()
	st, err = newMCPLogStore(MCPLogOptions{Ring: 4, DBPath: db}, nil)
	if err != nil {
		t.Fatalf("opening legacy schema: %v", err)
	}
	st.record(&MCPCallLog{Verdict: mcpVerdictError, Transport: "streamable",
		HTTPMethod: "POST", Status: 200, ReqID: "r", DiagCode: mcpDiagToolError,
		ProtoOutcome: mcpOutcomeOK, ToolOutcome: mcpOutcomeError})
	// The writer is async: wait for the row to land before closing.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM mcp_calls`).Scan(&n); err == nil && n == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	st.close()
	// Reopen: the second open runs the migration against an existing table.
	st2, err := newMCPLogStore(MCPLogOptions{Ring: 4, DBPath: db}, nil)
	if err != nil {
		t.Fatalf("reopen after migration: %v", err)
	}
	defer st2.close()
	var tool string
	if err := st2.db.QueryRow(`SELECT tool_outcome FROM mcp_calls LIMIT 1`).Scan(&tool); err != nil {
		t.Fatalf("outcome column not queryable: %v", err)
	}
	if tool != mcpOutcomeError {
		t.Fatalf("tool_outcome = %q, want persisted %q", tool, mcpOutcomeError)
	}
}
