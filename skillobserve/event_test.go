package skillobserve

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/mcpobserve"
)

type eventRecorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *eventRecorder) Record(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

type mcpCounter struct{ calls int }

func (r *mcpCounter) Record(mcpobserve.Call) { r.calls++ }

func TestSkillSpanSharesTraceWithoutCountingAsMCP(t *testing.T) {
	r := &eventRecorder{}
	mc := &mcpCounter{}
	ctx := mcpobserve.WithMetadata(context.Background(), mcpobserve.Metadata{TraceID: mcpobserve.NewID(), SessionID: "s", RunID: "r", TaskID: "t", OwnerID: "o", ToolCallID: "tc", Entrypoint: "mcp"})
	ctx = mcpobserve.WithRecorder(ctx, mc)
	ctx, outer := mcpobserve.Start(ctx, mcpobserve.Call{Direction: "inbound", Server: "pogo", Tool: "direct"})
	ctx = WithRecorder(WithSource(ctx, "external", ""), r)
	parent := mcpobserve.MetadataFrom(ctx)
	child, span := Start(ctx, Identify("pdf", "embedded", filepath.Join(t.TempDir(), "SKILL.md")), "execute", "manifest", "pdf.text", "pdf_text")
	md := mcpobserve.MetadataFrom(child)
	if md.TraceID != parent.TraceID || md.ParentID == parent.ParentID || md.Entrypoint != "mcp" {
		t.Fatalf("broken context propagation: %+v", md)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); span.End("ok", "") }()
	}
	wg.Wait()
	outer.End("ok", "")
	if len(r.events) != 2 || mc.calls != 2 {
		t.Fatalf("duplicated or mixed event sinks: skill=%d mcp=%d", len(r.events), mc.calls)
	}
	e := r.events[1]
	if e.ParentID != parent.ParentID || e.Caller != "external" || e.Entrypoint != "mcp" || e.SessionID != "s" || e.RunID != "r" || e.OwnerID != "o" || e.ToolCallID != "tc" || e.Status != "ok" {
		t.Fatalf("event: %+v", e)
	}
}

func TestSkillIdentityAndUnknownBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-workspace", "SKILL.md")
	id := Identify("reader", "project", path)
	if id != Identify("reader", "project", filepath.Join(filepath.Dir(path), ".", "SKILL.md")) || id.ID == Identify("reader", "project", path+"other").ID {
		t.Fatal("identity must be stable and path scoped")
	}
	r := &eventRecorder{}
	ctx := WithRecorder(context.Background(), r)
	_, s := Start(ctx, id, "load", "file_read", "read", "")
	s.End("error", "synthetic-private-error-secret")
	e := r.events[1]
	if e.Caller != "unknown" || e.Entrypoint != "unknown" || e.ErrorKind != "unknown_error" {
		t.Fatalf("guessed source or retained raw error: %+v", e)
	}
	data, _ := json.Marshal(r.events)
	if strings.Contains(string(data), "private-workspace") || strings.Contains(string(data), "synthetic-private") {
		t.Fatal("private data entered event")
	}
	_, s = Start(ctx, Identity{}, "load", "file_read", "read", "")
	s.End("ok", "")
	if len(r.events) != 2 {
		t.Fatal("unresolved identity became attributed usage")
	}
	// A locally executed tool can originate from an external MCP task.
	ctx = mcpobserve.WithMetadata(WithSource(ctx, "internal", ""), mcpobserve.Metadata{Entrypoint: "mcp"})
	_, s = Start(ctx, id, "execute", "manifest", "echo", "echo")
	s.End("unsupported", "")
	if e = r.events[len(r.events)-1]; e.Caller != "internal" || e.Entrypoint != "mcp" || e.Status != "interrupted" {
		t.Fatalf("wrong origin/outcome: %+v", e)
	}
}

func TestSkillIdentityCanonicalAlias(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "SKILL.md")
	alias := filepath.Join(dir, "alias.md")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if Identify("fixture", "path", file) != Identify("fixture", "path", alias) {
		t.Fatal("symlink alias split one skill identity")
	}
}
