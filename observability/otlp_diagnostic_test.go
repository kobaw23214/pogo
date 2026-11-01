package observability

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOTLPDoesNotExportLocalFailureDiagnostics(t *testing.T) {
	output := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		output <- string(raw)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer server.Close()
	exporter, err := NewOTLPExporter(OTLPOptions{Endpoint: server.URL + "/v1/traces", BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	call := otlpFixtureCall()
	call.Status = "error"
	call.ErrorKind = "tool_error"
	call.ErrorSummary = "Local-private-diagnostic-canary"
	call.ErrorCode = "LOCAL_PRIVATE_CODE"
	exporter.RecordMCP(call)
	otlpShutdown(t, exporter)
	raw := <-output
	for _, secret := range []string{call.ErrorSummary, call.ErrorCode, "error_summary", "error_code"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("local diagnostics escaped into OTLP: %q", secret)
		}
	}
	if !strings.Contains(raw, "tool_error") {
		t.Fatal("safe error category disappeared")
	}
}
