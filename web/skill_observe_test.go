package web

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/skillobserve"
	"github.com/yosukeno/pogo/skills"
)

// skillObserveRecorder is a skillobserve.Recorder fake. The store is another
// worker's surface; these tests pin the instrumentation points.
type skillObserveRecorder struct {
	mu     sync.Mutex
	events []skillobserve.Event
}

func (r *skillObserveRecorder) Record(e skillobserve.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

// finals collapses running/final snapshots by event ID, the state a store
// would upsert to.
func (r *skillObserveRecorder) finals() []skillobserve.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	byID := map[string]skillobserve.Event{}
	var order []string
	for _, e := range r.events {
		if _, ok := byID[e.ID]; !ok {
			order = append(order, e.ID)
		}
		byID[e.ID] = e
	}
	out := make([]skillobserve.Event, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// writeObservedSkill lays down a test skill via the shared helper and flags
// it hidden when asked.
func writeObservedSkill(t *testing.T, base, name string, hidden bool) skills.Skill {
	t.Helper()
	sk := writeTestSkill(t, base, name, "test skill", "# "+name+"\nbody")
	sk.DisableModelInvocation = hidden
	return sk
}

func eventsByStage(events []skillobserve.Event, stage string) []skillobserve.Event {
	var out []skillobserve.Event
	for _, e := range events {
		if e.Stage == stage {
			out = append(out, e)
		}
	}
	return out
}

func TestSkillObserveDiscoveryPerRun(t *testing.T) {
	base := t.TempDir()
	visible := writeObservedSkill(t, base, "viz", false)
	hidden := writeObservedSkill(t, base, "ghost", true)
	h := newHarness(t, scriptedTurns(textTurn("done")), func(c *Config) {
		c.Skills = []skills.Skill{visible, hidden}
	})
	skillsRec := &skillObserveRecorder{}
	h.mgr.SetSkillRecorder(skillsRec)
	mcpRec := &observeRecorder{}
	h.mgr.SetMCPRecorder(mcpRec)

	sid := h.createSession()
	s := h.start(sid, "hi")
	s.wait(t, EvRunEnd)

	disc := eventsByStage(skillsRec.finals(), "discovery")
	if len(disc) != 1 {
		t.Fatalf("discovery events = %d, want 1 (hidden must not count): %+v", len(disc), disc)
	}
	e := disc[0]
	want := skillIdentityOf(visible)
	if e.SkillID != want.ID || e.SkillName != "viz" || e.SkillSource != "user" {
		t.Fatalf("identity = %+v want %+v", e, want)
	}
	if e.Mechanism != "catalog" || e.Status != "ok" || e.Caller != "internal" || e.Entrypoint != "pogo" {
		t.Fatalf("event = %+v", e)
	}
	if e.SessionID != sid || e.RunID == "" || e.TraceID == "" {
		t.Fatalf("correlation = %+v", e)
	}
	// The path used to build the identity never enters the event.
	js, _ := json.Marshal(e)
	privatePath, _ := json.Marshal(base)
	if strings.Contains(string(js), string(privatePath[1:len(privatePath)-1])) || strings.Contains(string(js), "SKILL.md") {
		t.Fatalf("path leak: %s", js)
	}
	// Skill events never land in the MCP recorder.
	if got := len(mcpRec.finals()); got != 0 {
		t.Fatalf("MCP calls = %d, want 0", got)
	}
}

func TestSkillObserveMCPSubmittedRunStaysInternal(t *testing.T) {
	base := t.TempDir()
	visible := writeObservedSkill(t, base, "viz", false)
	h := newHarness(t, scriptedTurns(textTurn("done")), func(c *Config) {
		c.Skills = []skills.Skill{visible}
	})
	rec := &skillObserveRecorder{}
	h.mgr.SetSkillRecorder(rec)

	sid := h.createSession()
	sess := h.session(sid)
	stream := h.stream(sid, "")
	if first := stream.next(t); first.Type != EvSnapshot {
		t.Fatalf("first frame = %s", first.Type)
	}
	// The shape agent_submit_task produces: the inbound call's trace, the
	// mcp entrypoint — while everything the run does stays the internal caller.
	observed := mcpobserve.Metadata{TraceID: mcpobserve.NewID(), Entrypoint: "mcp", TaskID: "task-1"}
	if _, err := sess.StartObserved("hi", nil, "", observed); err != nil {
		t.Fatal(err)
	}
	stream.wait(t, EvRunEnd)

	disc := eventsByStage(rec.finals(), "discovery")
	if len(disc) != 1 {
		t.Fatalf("discovery = %+v", disc)
	}
	e := disc[0]
	if e.Caller != "internal" || e.Entrypoint != "mcp" {
		t.Fatalf("caller/entrypoint = %s/%s", e.Caller, e.Entrypoint)
	}
	if e.TraceID != observed.TraceID || e.TaskID != "task-1" || e.RunID == "" || e.SessionID != sid {
		t.Fatalf("correlation = %+v", e)
	}
}

func TestSkillObserveExplicitInvocationSharesTrace(t *testing.T) {
	base := t.TempDir()
	sk := writeObservedSkill(t, base, "viz", false)
	h := newHarness(t, scriptedTurns(textTurn("done")), func(c *Config) {
		c.Skills = []skills.Skill{sk}
	})
	rec := &skillObserveRecorder{}
	h.mgr.SetSkillRecorder(rec)

	sid := h.createSession()
	stream := h.stream(sid, "")
	if first := stream.next(t); first.Type != EvSnapshot {
		t.Fatalf("first frame = %s", first.Type)
	}
	h.post("/api/sessions/"+sid+"/messages", `{"prompt":"/skill:viz do it"}`, http.StatusAccepted)
	stream.wait(t, EvRunEnd)

	finals := rec.finals()
	loads := eventsByStage(finals, "load")
	if len(loads) != 1 {
		t.Fatalf("load events = %+v", loads)
	}
	load := loads[0]
	if load.Mechanism != "explicit" || load.Status != "ok" || load.Caller != "internal" || load.Entrypoint != "pogo" {
		t.Fatalf("load = %+v", load)
	}
	if load.SkillID != skillIdentityOf(sk).ID {
		t.Fatalf("load identity = %+v", load)
	}
	// The run ID is allocated after the expansion; it is empty, never invented.
	if load.RunID != "" {
		t.Fatalf("invented run id: %+v", load)
	}
	disc := eventsByStage(finals, "discovery")
	if len(disc) != 1 {
		t.Fatalf("discovery = %+v", disc)
	}
	if disc[0].TraceID != load.TraceID || disc[0].RunID == "" {
		t.Fatalf("trace not shared: load=%+v disc=%+v", load, disc[0])
	}
}

func TestSkillObserveExplicitUnknownSkillUnrecorded(t *testing.T) {
	base := t.TempDir()
	sk := writeObservedSkill(t, base, "viz", false)
	h := newHarness(t, scriptedTurns(textTurn("done")), func(c *Config) {
		c.Skills = []skills.Skill{sk}
	})
	rec := &skillObserveRecorder{}
	h.mgr.SetSkillRecorder(rec)
	sid := h.createSession()
	resp := h.do(http.MethodPost, "/api/sessions/"+sid+"/messages", `{"prompt":"/skill:nope"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := len(rec.finals()); got != 0 {
		t.Fatalf("events = %d, want 0 — unknown input is not attribution", got)
	}
}

func TestSkillObserveSnapshotStableAcrossReload(t *testing.T) {
	base := t.TempDir()
	v1 := writeObservedSkill(t, base, "viz", false)
	h := newHarness(t, scriptedTurns(textTurn("done")), func(c *Config) {
		c.Skills = []skills.Skill{v1}
	})
	sid := h.createSession()
	sess := h.session(sid)
	if len(sess.skillSnapshot) != 1 {
		t.Fatalf("snapshot = %+v", sess.skillSnapshot)
	}
	oldID := sess.skillSnapshot[0].identity.ID

	// A same-name skill installed elsewhere: a new identity, and the live
	// manager re-projects — but the session keeps what it was built with.
	v2 := writeObservedSkill(t, t.TempDir(), "viz", false)
	newID := skillIdentityOf(v2).ID
	if oldID == newID {
		t.Fatal("distinct paths must give distinct identities")
	}
	h.mgr.SetSkills([]skills.Skill{v2})
	if got := sess.skillSnapshot[0].identity.ID; got != oldID {
		t.Fatalf("session re-attributed: %s -> %s", oldID, got)
	}
	if got := h.mgr.skillSnapshot[0].identity.ID; got != newID {
		t.Fatalf("manager not refreshed: %s", got)
	}
}

func TestSkillObserveMCPMiddlewareMarksExternal(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("ok")))
	rec := &skillObserveRecorder{}
	h.mgr.SetSkillRecorder(rec)
	l := newMCPLayer(h.mgr, MCPOptions{}, true, nil)

	var captured context.Context
	handler := l.observeMiddleware()(func(ctx context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
		captured = ctx
		return &mcp.CallToolResult{}, nil
	})
	if _, err := handler(context.Background(), "tools/call", &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "agent_task_list"}}); err != nil {
		t.Fatal(err)
	}
	if captured == nil {
		t.Fatal("handler not reached")
	}
	caller, entrypoint := skillobserve.SourceFrom(captured)
	if caller != "external" || entrypoint != "mcp" {
		t.Fatalf("source = %s/%s", caller, entrypoint)
	}
	md := mcpobserve.MetadataFrom(captured)
	if md.Entrypoint != "mcp" || md.TraceID == "" {
		t.Fatalf("metadata = %+v", md)
	}
	// A span started on this context records through the injected skill
	// recorder, parented under the inbound span — with no session/run.
	id := skillobserve.Identify("viz", "project", filepath.Join(t.TempDir(), "SKILL.md"))
	_, span := skillobserve.Start(captured, id, "execute", "manifest", "viz.tool", "viz_tool")
	span.End("ok", "")
	finals := rec.finals()
	if len(finals) != 1 {
		t.Fatalf("events = %+v", finals)
	}
	e := finals[0]
	if e.Caller != "external" || e.Entrypoint != "mcp" || e.TraceID != md.TraceID || e.ParentID == "" {
		t.Fatalf("event = %+v", e)
	}
	if e.SessionID != "" || e.RunID != "" {
		t.Fatalf("invented correlation: %+v", e)
	}
}
