package skillobserve

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/yosukeno/pogo/mcpobserve"
)

func TestSkillSpanStandardTraceFields(t *testing.T) {
	r := &eventRecorder{}
	id := Identify("pdf", "embedded", filepath.Join(t.TempDir(), "SKILL.md"))
	ctx := mcpobserve.WithMetadata(context.Background(), mcpobserve.Metadata{
		TraceID: "0123456789abcdef0123456789abcdef", ParentSpanID: "0123456789abcdef",
		TraceFlags: "00", TraceState: "congo=t61rcWkgMzE", ClientID: "local-caller",
		Entrypoint: "cli",
	})
	ctx = WithRecorder(ctx, r)
	child, span := Start(ctx, id, "execute", "manifest", "pdf.text", "pdf_text")
	span.End("ok", "")

	e := r.events[0]
	if e.SpanID == "" || e.SpanID != mcpobserve.SpanIDFor(e.ID) {
		t.Fatalf("span id not derived from event id: %+v", e)
	}
	if e.ParentSpanID != "0123456789abcdef" || e.TraceFlags != "00" || e.ClientID != "local-caller" {
		t.Fatalf("standard fields wrong: %+v", e)
	}
	if e.Entrypoint != "cli" {
		t.Fatalf("cli entrypoint not retained: %+v", e)
	}
	md := mcpobserve.MetadataFrom(child)
	if md.ParentID != e.ID || md.ParentSpanID != e.SpanID || md.TraceFlags != "00" || md.TraceState != "congo=t61rcWkgMzE" {
		t.Fatalf("returned context metadata wrong: %+v", md)
	}

	// Defaults: no incoming flags means 01, a local 32-hex parent derives the
	// parent span, and an unknown identity still emits nothing.
	r2 := &eventRecorder{}
	local := mcpobserve.NewID()
	ctx2 := WithRecorder(mcpobserve.WithMetadata(context.Background(), mcpobserve.Metadata{
		TraceID: mcpobserve.NewID(), ParentID: local,
	}), r2)
	_, span2 := Start(ctx2, id, "load", "file_read", "read", "")
	span2.End("ok", "")
	e2 := r2.events[0]
	if e2.TraceFlags != "01" || e2.ParentSpanID != mcpobserve.SpanIDFor(local) || e2.ClientID != "" {
		t.Fatalf("defaults wrong: %+v", e2)
	}
	_, silent := Start(ctx2, Identity{}, "load", "file_read", "read", "")
	silent.End("ok", "")
	if len(r2.events) != 2 {
		t.Fatal("unknown identity emitted")
	}
}
