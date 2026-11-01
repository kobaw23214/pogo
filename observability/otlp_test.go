package observability

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/skillobserve"
)

func otlpFixtureCall() mcpobserve.Call {
	now := time.Now().UnixMilli()
	return mcpobserve.Call{ID: strings.Repeat("1", 32), TraceID: strings.Repeat("2", 32), SpanID: strings.Repeat("3", 16), ParentSpanID: strings.Repeat("4", 16), TraceFlags: "01", Direction: "outbound", Server: "fixture", Tool: "lookup", WireTool: "fixture_lookup", Transport: "streamable", SessionID: "session-fixture", RunID: "run-fixture", ClientID: "client-fixture", Entrypoint: "cli", StartedAt: now, FinishedAt: now + 5, DurationMs: 5.125, Status: "ok", OwnerID: "synthetic-private-owner"}
}

func otlpShutdown(t *testing.T, x *OTLPExporter) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := x.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestOTLPStandardJSONAndMetadataBoundary(t *testing.T) {
	data := make(chan []byte, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" || r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("wrong OTLP request shape")
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-private-token" {
			t.Errorf("missing configured collector credential")
		}
		raw, _ := io.ReadAll(r.Body)
		data <- raw
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer ts.Close()
	x, err := NewOTLPExporter(OTLPOptions{Endpoint: ts.URL + "/v1/traces", BatchSize: 2, Headers: map[string]string{"Authorization": "Bearer synthetic-private-token"}})
	if err != nil {
		t.Fatal(err)
	}
	c := otlpFixtureCall()
	running := c
	running.Status = "running"
	running.FinishedAt = 0
	x.RecordMCP(running)
	x.RecordMCP(c)
	x.RecordSkill(skillobserve.Event{ID: strings.Repeat("5", 32), TraceID: c.TraceID, SpanID: strings.Repeat("6", 16), ParentSpanID: c.SpanID, TraceFlags: "01", SkillID: "skill-fixture", SkillName: "fixture", Stage: "execute", Caller: "internal", Entrypoint: "cli", Mechanism: "manifest", Tool: "fixture.run", StartedAt: c.StartedAt, FinishedAt: c.FinishedAt, DurationMs: 2, Status: "error", ErrorKind: "execution_error", OwnerID: "synthetic-private-owner"})
	unsampled := c
	unsampled.TraceFlags = "00"
	x.RecordMCP(unsampled)
	otlpShutdown(t, x)
	raw := <-data
	for _, secret := range []string{"synthetic-private-token", "synthetic-private-owner", "arguments", "input.value", "output.value", "trace_id", "span_id"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("non-contract data in OTLP: %q", secret)
		}
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	rs := body["resourceSpans"].([]any)
	ss := rs[0].(map[string]any)["scopeSpans"].([]any)
	spans := ss[0].(map[string]any)["spans"].([]any)
	if len(spans) != 2 {
		t.Fatalf("expected completed MCP+Skill spans only, got %d", len(spans))
	}
	mcp := spans[0].(map[string]any)
	if mcp["traceId"] != c.TraceID || mcp["spanId"] != c.SpanID || mcp["parentSpanId"] != c.ParentSpanID || mcp["kind"] != float64(3) || mcp["flags"] != float64(1) {
		t.Fatalf("OTLP identity/kind: %+v", mcp)
	}
	if _, ok := mcp["startTimeUnixNano"].(string); !ok {
		t.Fatal("64-bit OTLP timestamps must be decimal strings")
	}
	if mcp["status"].(map[string]any)["code"] != float64(1) || spans[1].(map[string]any)["status"].(map[string]any)["code"] != float64(2) {
		t.Fatal("status enums must be numeric and reflect actual outcomes")
	}
	st := x.Stats()
	if st.Exported != 2 || st.SampledOut != 1 || st.Failed != 0 || st.Invalid != 0 || st.LastSuccessAt == 0 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestOTLPRetryAndPartialResponse(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		status                     int
		response                   string
		attempts                   int
		exported, failed, rejected int64
	}{
		{"transient", 503, `{}`, 3, 2, 0, 0},
		{"bad request", 400, `synthetic-private-response`, 1, 0, 2, 0},
		{"partial string", 200, `{"partialSuccess":{"rejectedSpans":"1","errorMessage":"synthetic-private-response"}}`, 1, 1, 0, 1},
		{"partial number", 200, `{"partialSuccess":{"rejectedSpans":1}}`, 1, 1, 0, 1},
		{"invalid response", 200, `null`, 1, 0, 2, 0},
		{"impossible partial", 200, `{"partialSuccess":{"rejectedSpans":"3"}}`, 1, 0, 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int64
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				n := attempts.Add(1)
				status := tc.status
				if tc.name == "transient" && n == 3 {
					status = 200
				}
				w.WriteHeader(status)
				io.WriteString(w, tc.response)
			}))
			defer ts.Close()
			x, err := NewOTLPExporter(OTLPOptions{Endpoint: ts.URL, BatchSize: 2})
			if err != nil {
				t.Fatal(err)
			}
			x.RecordMCP(otlpFixtureCall())
			x.RecordMCP(otlpFixtureCall())
			otlpShutdown(t, x)
			st := x.Stats()
			if attempts.Load() != int64(tc.attempts) || st.Exported != tc.exported || st.Failed != tc.failed || st.Rejected != tc.rejected {
				t.Fatalf("attempts=%d stats=%+v", attempts.Load(), st)
			}
			js, _ := json.Marshal(st)
			if strings.Contains(string(js), "synthetic-private-response") || strings.Contains(string(js), ts.URL) {
				t.Fatal("collector response or URL leaked through health")
			}
		})
	}
}

func TestOTLPDoesNotForwardCredentialsOnRedirect(t *testing.T) {
	var reached atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1); io.WriteString(w, `{}`) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	x, err := NewOTLPExporter(OTLPOptions{Endpoint: source.URL, Headers: map[string]string{"Authorization": "Bearer synthetic-private-token"}})
	if err != nil {
		t.Fatal(err)
	}
	x.RecordMCP(otlpFixtureCall())
	otlpShutdown(t, x)
	if reached.Load() != 0 || x.Stats().Failed != 1 || x.Stats().LastErrorKind != "http_307" {
		t.Fatalf("redirect followed: %+v", x.Stats())
	}
}

func TestOTLPQueueAndShutdownStayBounded(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-started:
		default:
			close(started)
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer ts.Close()
	defer close(release)
	x, err := NewOTLPExporter(OTLPOptions{Endpoint: ts.URL, QueueSize: 2, BatchSize: 1, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	x.RecordMCP(otlpFixtureCall())
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("export did not start")
	}
	begin := time.Now()
	for i := 0; i < 100; i++ {
		x.RecordMCP(otlpFixtureCall())
	}
	if time.Since(begin) > time.Second || x.Stats().Dropped < 98 {
		t.Fatalf("recorder blocked or queue unbounded: %+v", x.Stats())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if x.Shutdown(ctx) == nil {
		t.Fatal("expected bounded flush deadline")
	}
	select {
	case <-x.done:
	case <-time.After(time.Second):
		t.Fatal("export worker ignored cancellation")
	}
	before := x.Stats().Dropped
	x.RecordMCP(otlpFixtureCall())
	if x.Stats().Dropped != before+1 {
		t.Fatal("late recording was not rejected")
	}
	otlpShutdown(t, x)
}

func TestOTLPRejectsInvalidIdentityAndOutcome(t *testing.T) {
	x, err := NewOTLPExporter(OTLPOptions{Endpoint: "http://127.0.0.1:1/v1/traces"})
	if err != nil {
		t.Fatal(err)
	}
	c := otlpFixtureCall()
	c.TraceID = strings.Repeat("0", 32)
	x.RecordMCP(c)
	c = otlpFixtureCall()
	c.Status = "arbitrary"
	x.RecordMCP(c)
	c = otlpFixtureCall()
	c.DurationMs = -1
	x.RecordMCP(c)
	otlpShutdown(t, x)
	if x.Stats().Invalid != 3 || x.Stats().Exported != 0 || x.Stats().Failed != 0 {
		t.Fatalf("invalid events were sent: %+v", x.Stats())
	}
}

func TestOTLPEnvironmentIsExplicitAndHeadersAreReferencesOnly(t *testing.T) {
	get := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	if o, err := OTLPOptionsFromEnv(get(nil)); err != nil || o.Endpoint != "" {
		t.Fatal("export must default off")
	}
	values := map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "https://example.test/otel/", "OTEL_EXPORTER_OTLP_HEADERS": "Authorization=Basic%20synthetic%2Bvalue,X-Client=pogo", "OTEL_EXPORTER_OTLP_TIMEOUT": "1234", "OTEL_SERVICE_NAME": "fixture"}
	o, err := OTLPOptionsFromEnv(get(values))
	if err != nil || o.Endpoint != "https://example.test/otel/v1/traces" || o.Headers["Authorization"] != "Basic synthetic+value" || o.Timeout != 1234*time.Millisecond {
		t.Fatalf("env mapping: %+v %v", o, err)
	}
	values["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] = "https://example.test/custom"
	o, err = OTLPOptionsFromEnv(get(values))
	if err != nil || o.Endpoint != "https://example.test/custom" {
		t.Fatal("signal endpoint must override general endpoint")
	}
	values["OTEL_EXPORTER_OTLP_HEADERS"] = "Authorization=one,authorization=two"
	if _, err := OTLPOptionsFromEnv(get(values)); err == nil {
		t.Fatal("case-insensitive duplicate headers accepted")
	}
	values["POGO_OTLP"] = "0"
	if o, err := OTLPOptionsFromEnv(get(values)); err != nil || o.Endpoint != "" {
		t.Fatal("off switch must bypass exporter configuration")
	}
	for _, bad := range []OTLPOptions{{Endpoint: "file:///tmp/otel"}, {Endpoint: "https://user:synthetic-secret@example.test/"}, {Endpoint: "http://example.test", Headers: map[string]string{"Authorization": "secret\nheader"}}, {Endpoint: "http://example.test", Headers: map[string]string{"Content-Type": "text/plain"}}} {
		if _, err := NewOTLPExporter(bad); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatal("bad transport configuration accepted or credential echoed")
		}
	}
}
