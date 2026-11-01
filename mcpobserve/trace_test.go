package mcpobserve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const (
	testTraceID = "0123456789abcdef0123456789abcdef"
	testSpanID  = "0123456789abcdef"
	testParent  = "00-" + testTraceID + "-" + testSpanID + "-01"
)

func TestSpanIDFor(t *testing.T) {
	if got := SpanIDFor(testTraceID); got != testSpanID {
		t.Fatalf("SpanIDFor = %q", got)
	}
	for _, bad := range []string{"", "abc", strings.Repeat("g", 32),
		"0000000000000000" + testSpanID, strings.Repeat("0", 32), testTraceID + "ff"} {
		if got := SpanIDFor(bad); got != "" {
			t.Fatalf("SpanIDFor(%q) = %q, want empty", bad, got)
		}
	}
	// Local ids map even when mixed case; output is normalized lowercase.
	if got := SpanIDFor(strings.ToUpper(testTraceID)); got != testSpanID {
		t.Fatalf("mixed case mapping = %q", got)
	}
}

func TestParseTraceparentBoundaries(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid", testParent, true},
		{"unsampled", "00-" + testTraceID + "-" + testSpanID + "-00", true},
		{"future version", "01-" + testTraceID + "-" + testSpanID + "-01", true},
		{"future version extra field", "01-" + testTraceID + "-" + testSpanID + "-01-extra", true},
		{"zero trace id", "00-00000000000000000000000000000000-" + testSpanID + "-01", false},
		{"zero span id", "00-" + testTraceID + "-0000000000000000-01", false},
		{"short", "00-" + testTraceID + "-" + testSpanID, false},
		{"long version 00", testParent + "-extra", false},
		{"bad hex trace", "00-zz3456789abcdef0123456789abcdef-" + testSpanID + "-01", false},
		{"bad hex flags", "00-" + testTraceID + "-" + testSpanID + "-0x", false},
		{"uppercase", "00-" + strings.ToUpper(testTraceID) + "-" + testSpanID + "-01", false},
		{"version ff", "ff-" + testTraceID + "-" + testSpanID + "-01", false},
		{"bad version", "0-" + testTraceID + "-" + testSpanID + "-01", false},
		{"short span", "00-" + testTraceID + "-0123456789abcde-01", false},
		{"empty", "", false},
		{"not a traceparent", "hello-world", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, ok := parseTraceparent(tc.in)
			if ok != tc.ok {
				t.Fatalf("parseTraceparent(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			}
		})
	}
}

func TestValidTracestateBoundaries(t *testing.T) {
	valid := []string{
		"", " \t", "congo=one, rojo=two", "congo=one, ,\trojo=two ", "key= spaced ",
		"congo=t61rcWkgMzE",
		"congo=t61rcWkgMzE,rojo=00f067aa0ba902b7",
		"tenant@system=value",
		"a-b_c*d/e=f",
	}
	for _, s := range valid {
		if !validTracestate(s) {
			t.Fatalf("validTracestate(%q) = false, want true", s)
		}
	}
	many := make([]string, 33)
	for i := range many {
		many[i] = strings.Repeat("a", 1) + string(rune('a'+i%26)) + string(rune('0'+i/26)) + "=v"
	}
	invalid := []string{
		"_key=value", "-tenant@system=value", "tenant@/system=value", "key=tab\tvalue",
		"congo=t61rcWkgMzE,congo=duplicate",                         // duplicate key
		strings.Join(many, ","),                                     // 33 members
		strings.Repeat("a", 250) + "=v," + strings.Repeat("b", 260), // too long total
		"UPPER=value",
		"key=",       // empty value
		"key =value", // space in key
		"key=va,lue", // comma splits members; the leftover member is invalid
		"noequals",
	}
	for _, s := range invalid {
		if validTracestate(s) {
			t.Fatalf("validTracestate(%q) = true, want false", s)
		}
	}
	// Multi-tenant: digit-starting tenant is legal, digit-starting system is not.
	if !validTracestate("1tenant@sys=v") {
		t.Fatal("digit tenant must be accepted")
	}
	if validTracestate("tenant@1sys=v") {
		t.Fatal("digit system must be rejected")
	}
}

func TestWithIncomingTraceResetsStaleAndPreservesIdentity(t *testing.T) {
	ctx := WithMetadata(context.Background(), Metadata{
		TraceID: "staletrace", ParentID: "staleparent", ParentSpanID: "stalespan",
		TraceFlags: "00", TraceState: "stale=1",
		SessionID: "sess", RunID: "run", TaskID: "task", OwnerID: "owner",
		ClientID: "local-caller", Entrypoint: "mcp",
	})
	// A call with no traceparent drops all stale tracing but keeps identity.
	md := MetadataFrom(WithIncomingTrace(ctx, nil))
	if md.TraceID != "" || md.ParentID != "" || md.ParentSpanID != "" || md.TraceFlags != "" || md.TraceState != "" {
		t.Fatalf("stale trace survived: %+v", md)
	}
	if md.SessionID != "sess" || md.RunID != "run" || md.TaskID != "task" || md.OwnerID != "owner" || md.ClientID != "local-caller" || md.Entrypoint != "mcp" {
		t.Fatalf("trusted identity lost: %+v", md)
	}

	// A valid traceparent continues the trace; valid tracestate rides along.
	ctx = WithIncomingTrace(ctx, map[string]any{
		"traceparent": testParent,
		"tracestate":  "congo=t61rcWkgMzE",
	})
	md = MetadataFrom(ctx)
	if md.TraceID != testTraceID || md.ParentSpanID != testSpanID || md.TraceFlags != "01" || md.TraceState != "congo=t61rcWkgMzE" {
		t.Fatalf("incoming trace not applied: %+v", md)
	}
	if md.ParentID != "" || md.ClientID != "local-caller" {
		t.Fatalf("local parent or identity wrong: %+v", md)
	}

	// Invalid tracestate is dropped without invalidating the traceparent.
	md = MetadataFrom(WithIncomingTrace(context.Background(), map[string]any{
		"traceparent": testParent,
		"tracestate":  "dup=1,dup=2",
	}))
	if md.TraceID != testTraceID || md.TraceState != "" {
		t.Fatalf("invalid tracestate handling: %+v", md)
	}

	// An invalid traceparent starts fresh: nothing is continued.
	md = MetadataFrom(WithIncomingTrace(ctx, map[string]any{
		"traceparent": "ff-" + testTraceID + "-" + testSpanID + "-01",
		"tracestate":  "congo=t61rcWkgMzE",
	}))
	if md.TraceID != "" || md.ParentSpanID != "" || md.TraceState != "" {
		t.Fatalf("invalid traceparent reused: %+v", md)
	}
}

func TestWithIncomingTraceIgnoresNonTraceKeys(t *testing.T) {
	ctx := WithIncomingTrace(context.Background(), map[string]any{
		"traceparent": testParent,
		"baggage":     "user_id=evil",
		"session_id":  "forged", "run_id": "forged", "task_id": "forged",
		"client_id": "forged", "owner_id": "forged", "identity": "forged",
	})
	md := MetadataFrom(ctx)
	if md.SessionID != "" || md.RunID != "" || md.TaskID != "" || md.OwnerID != "" || md.ClientID != "" {
		t.Fatalf("meta identity claims accepted: %+v", md)
	}
	if md.TraceID != testTraceID {
		t.Fatalf("traceparent lost among ignored keys: %+v", md)
	}
}

func TestTraceMetaEmission(t *testing.T) {
	if TraceMeta(context.Background()) != nil {
		t.Fatal("no active parent span must emit nothing")
	}
	ctx := WithIncomingTrace(context.Background(), map[string]any{
		"traceparent": testParent, "tracestate": "congo=t61rcWkgMzE",
	})
	meta := TraceMeta(ctx)
	if meta["traceparent"] != testParent || meta["tracestate"] != "congo=t61rcWkgMzE" {
		t.Fatalf("round trip = %#v", meta)
	}
	// Unsampled parents stay unsampled; garbage flags normalize to 01.
	ctx = WithMetadata(context.Background(), Metadata{TraceID: testTraceID, ParentSpanID: testSpanID, TraceFlags: "00"})
	if meta = TraceMeta(ctx); meta["traceparent"] != "00-"+testTraceID+"-"+testSpanID+"-00" {
		t.Fatalf("unsampled dropped: %#v", meta)
	}
	ctx = WithMetadata(context.Background(), Metadata{TraceID: testTraceID, ParentSpanID: testSpanID, TraceFlags: "zz"})
	if meta = TraceMeta(ctx); meta["traceparent"] != testParent {
		t.Fatalf("bad flags not normalized: %#v", meta)
	}
	// Non-hex local correlation ids never leak onto the wire as traceparent.
	ctx = WithMetadata(context.Background(), Metadata{TraceID: "trace-x", ParentSpanID: "inbound-1"})
	if TraceMeta(ctx) != nil {
		t.Fatal("non-W3C local ids must not emit")
	}
	// The returned map is the caller's own; mutating it changes nothing.
	meta["traceparent"] = "mutated"
	if TraceMeta(WithMetadata(context.Background(), Metadata{TraceID: testTraceID, ParentSpanID: testSpanID}))["traceparent"] != testParent {
		t.Fatal("TraceMeta shared state between calls")
	}
}

func TestStartSetsStandardSpanFields(t *testing.T) {
	sink := &recordingSink{}
	ctx := WithRecorder(context.Background(), sink)
	ctx = WithMetadata(ctx, Metadata{
		TraceID: testTraceID, ParentSpanID: testSpanID, TraceFlags: "00",
		TraceState: "congo=t61rcWkgMzE", ClientID: "local-caller", Entrypoint: "mcp",
	})
	ctx, span := Start(ctx, Call{Direction: "inbound", Server: "pogo", Tool: "agent_task_list"})
	span.End("ok", "")
	c := sink.calls[0]
	if c.SpanID == "" || c.SpanID != SpanIDFor(c.ID) {
		t.Fatalf("span id not derived from event id: %+v", c)
	}
	if c.ParentSpanID != testSpanID || c.TraceFlags != "00" || c.ClientID != "local-caller" || c.Entrypoint != "mcp" {
		t.Fatalf("standard fields wrong: %+v", c)
	}
	md := MetadataFrom(ctx)
	if md.ParentID != c.ID || md.ParentSpanID != c.SpanID || md.TraceFlags != "00" || md.TraceState != "congo=t61rcWkgMzE" || md.ClientID != "local-caller" {
		t.Fatalf("returned context metadata wrong: %+v", md)
	}
	// The outbound side of this exact context emits a valid child traceparent.
	meta := TraceMeta(ctx)
	want := "00-" + testTraceID + "-" + c.SpanID + "-00"
	if meta["traceparent"] != want {
		t.Fatalf("outbound traceparent = %#v, want %q", meta, want)
	}

	// A child derives its parent span from the returned context, and flags
	// default to 01 when nothing set them.
	_, child := Start(ctx, Call{Direction: "outbound", Tool: "search"})
	child.End("ok", "")
	cc := sink.calls[2]
	if cc.ParentSpanID != c.SpanID || cc.TraceID != c.TraceID || cc.ParentID != c.ID {
		t.Fatalf("child linkage wrong: %+v", cc)
	}
	plain, plainSpan := Start(WithRecorder(context.Background(), sink), Call{Direction: "outbound", Tool: "fresh"})
	plainSpan.End("ok", "")
	pc := sink.calls[4]
	if pc.TraceFlags != "01" || pc.ParentSpanID != "" || pc.Entrypoint != "unknown" {
		t.Fatalf("fresh span defaults wrong: %+v", pc)
	}
	if md := MetadataFrom(plain); md.ParentSpanID != pc.SpanID {
		t.Fatalf("fresh context not updated: %+v", md)
	}

	// ParentSpanID derives from a local 32-hex ParentID when absent.
	local := NewID()
	derived, dSpan := Start(WithRecorder(WithMetadata(context.Background(), Metadata{TraceID: NewID(), ParentID: local}), sink), Call{Direction: "outbound", Tool: "d"})
	dSpan.End("ok", "")
	dc := sink.calls[6]
	if dc.ParentSpanID != SpanIDFor(local) {
		t.Fatalf("parent span not derived from local ParentID: %+v", dc)
	}
	if got := MetadataFrom(derived).ParentSpanID; got != dc.SpanID {
		t.Fatalf("derived context parent span = %q, want %q", got, dc.SpanID)
	}
}

func TestCallJSONTraceKeys(t *testing.T) {
	data, err := json.Marshal(Call{ID: "id", TraceID: "t", SpanID: "s", TraceFlags: "01", Entrypoint: "mcp"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, key := range []string{`"span_id"`, `"trace_flags"`, `"entrypoint"`} {
		if !strings.Contains(s, key) {
			t.Fatalf("missing %s in %s", key, s)
		}
	}
	for _, omitted := range []string{`"parent_span_id"`, `"client_id"`} {
		if strings.Contains(s, omitted) {
			t.Fatalf("%s must be omitempty in %s", omitted, s)
		}
	}
}
