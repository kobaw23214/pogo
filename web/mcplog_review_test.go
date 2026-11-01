package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type unreadLogBody struct{ t *testing.T }

func (b *unreadLogBody) Read([]byte) (int, error) {
	b.t.Error("logging must not read a body the handler rejected without reading")
	return 0, io.EOF
}

func TestMCPLogRedactsEscapedAndPartialCredentials(t *testing.T) {
	for _, payload := range []string{
		`{"password":"before\"synthetic-secret-after"}`,
		`{"password":"synthetic-secret-cutoff`,
	} {
		if got := redactLogPayload(payload); strings.Contains(got, "synthetic-secret") {
			t.Fatalf("credential survived redaction: %s", got)
		}
	}
}
func (b *unreadLogBody) Close() error { return nil }

func TestMCPLogDoesNotDrainRejectedBody(t *testing.T) {
	st := newTestStore(t)
	h := st.wrapTransport("streamable", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Body = &unreadLogBody{t: t}
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got := st.snapshot(1, "", "", ""); len(got) != 1 || got[0].Status != 400 {
		t.Fatalf("rejected request not recorded: %+v", got)
	}
}

func TestMCPLogResponseMatchingAndUnknown(t *testing.T) {
	e := &MCPCallLog{JSONRPCMethod: "tools/call", hasID: true, reqJSONID: `"request"`}
	for _, tc := range []struct{ name, contentType, body, proto, tool string }{
		{"wrong JSON id", "application/json", `{"jsonrpc":"2.0","id":"other","result":{"content":[]}}`, mcpOutcomeUnknown, mcpOutcomeUnknown},
		{"missing JSON id", "application/json", `{"jsonrpc":"2.0","result":{"content":[]}}`, mcpOutcomeUnknown, mcpOutcomeUnknown},
		{"null tool result", "application/json", `{"jsonrpc":"2.0","id":"request","result":null}`, mcpOutcomeOK, mcpOutcomeUnknown},
		{"empty response", "application/json", "", mcpOutcomeUnknown, mcpOutcomeUnknown},
		{"unrecognized content", "text/plain", "accepted", mcpOutcomeUnknown, mcpOutcomeUnknown},
		{"multiline SSE", "text/event-stream; charset=utf-8", "data: {\"jsonrpc\":\"2.0\",\"id\":\"request\",\n" + "data: \"result\":{\"content\":[],\"isError\":true}}\n\n", mcpOutcomeOK, mcpOutcomeError},
		{"partial SSE event", "text/event-stream", "data: {\"jsonrpc\":\"2.0\",\"id\":\"request\",\"result\":{\"content\":[]}}\n", mcpOutcomeUnknown, mcpOutcomeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, result := classifyResponse(e, tc.contentType, tc.body)
			if p != tc.proto || result != tc.tool {
				t.Fatalf("outcome %s/%s, want %s/%s", p, result, tc.proto, tc.tool)
			}
		})
	}
}
