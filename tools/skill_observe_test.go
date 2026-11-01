package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/skillobserve"
)

// skillRec is a skillobserve.Recorder fake collecting every snapshot for
// assertion. The store is another worker's surface; these tests pin the
// instrumentation only.
type skillRec struct {
	mu     sync.Mutex
	events []skillobserve.Event
}

func (r *skillRec) Record(e skillobserve.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *skillRec) snapshot() []skillobserve.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]skillobserve.Event(nil), r.events...)
}

// final returns the last snapshot of each event ID, the state a store would
// upsert to.
func (r *skillRec) final() []skillobserve.Event {
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

// skillObsCtx arms a context the way the web runner arms a run: metadata,
// skill recorder and the internal source.
func skillObsCtx(rec skillobserve.Recorder, caller, entrypoint string, md mcpobserve.Metadata) context.Context {
	ctx := mcpobserve.WithMetadata(context.Background(), md)
	ctx = skillobserve.WithSource(ctx, caller, entrypoint)
	if rec != nil {
		ctx = skillobserve.WithRecorder(ctx, rec)
	}
	return ctx
}

func testIdentity(t *testing.T, name string) skillobserve.Identity {
	t.Helper()
	p := filepath.Join(t.TempDir(), name, "SKILL.md")
	id := skillobserve.Identify(name, "project", p)
	if id.ID == "" {
		t.Fatal("empty identity")
	}
	return id
}

// helperArgv runs this test binary as the child process, the portable way to
// get a real process with a controlled exit mode. The sandbox intentionally
// clears inherited environment variables, so opt in through explicit argv.
func helperArgv(mode string) []string {
	return []string{os.Args[0], "-test.run=^TestSkillObserveHelperProcess$", "--", "pogo-skill-observe-helper", mode}
}

// TestSkillObserveHelperProcess is not a test; it is the child the synth
// execution tests spawn. The guard keeps it inert in a normal run.
func TestSkillObserveHelperProcess(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "--" || os.Args[len(os.Args)-2] != "pogo-skill-observe-helper" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "fail":
		fmt.Fprintln(os.Stderr, "helper failed")
		os.Exit(3)
	case "sleep":
		time.Sleep(30 * time.Second)
	default:
		fmt.Println("helper ok")
	}
	os.Exit(0)
}

// newObservedSynth builds a SynthTool running the helper process, bound to
// identity (nil identity allowed) with a short timeout.
func newObservedSynth(identity skillobserve.Identity, mode string, timeout int) *SynthTool {
	return &SynthTool{
		info:     SynthInfo{Name: "demo.tool", WireName: "demo_tool", Description: "d", InputSchema: map[string]any{"type": "object"}, Renderer: "text", Skill: identity.Name},
		exec:     synthExec{argv: helperArgv(mode), argMap: map[string]argMapping{}, timeout: timeout},
		identity: identity,
	}
}

// runObservedSynth uses the ordinary execution path, including Linux's
// environment-scrubbing sandbox. No helper-specific env exception is needed.
func runObservedSynth(t *testing.T, st *SynthTool, ctx context.Context, args string) (Result, error) {
	t.Helper()
	return st.Execute(ctx, json.RawMessage(args))
}

func TestSkillObserveSynthExecuteOK(t *testing.T) {
	rec := &skillRec{}
	id := testIdentity(t, "demo")
	st := newObservedSynth(id, "ok", 10)
	md := mcpobserve.Metadata{TraceID: mcpobserve.NewID(), SessionID: "sess", RunID: "run", OwnerID: "own", ToolCallID: "tc1", Entrypoint: "pogo"}
	res, err := runObservedSynth(t, st, skillObsCtx(rec, "internal", "", md), `{}`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Text, "helper ok") {
		t.Fatalf("output = %q", res.Text)
	}
	finals := rec.final()
	if len(finals) != 1 {
		t.Fatalf("events = %d, want 1: %+v", len(finals), finals)
	}
	e := finals[0]
	if e.Stage != "execute" || e.Mechanism != "manifest" || e.Status != "ok" || e.ErrorKind != "" {
		t.Fatalf("event = %+v", e)
	}
	if e.SkillID != id.ID || e.SkillName != "demo" || e.SkillSource != "project" {
		t.Fatalf("identity = %+v", e)
	}
	if e.Tool != "demo.tool" || e.WireTool != "demo_tool" {
		t.Fatalf("tool = %q/%q", e.Tool, e.WireTool)
	}
	if e.Caller != "internal" || e.Entrypoint != "pogo" {
		t.Fatalf("source = %s/%s", e.Caller, e.Entrypoint)
	}
	if e.SessionID != "sess" || e.RunID != "run" || e.OwnerID != "own" || e.ToolCallID != "tc1" {
		t.Fatalf("correlation = %+v", e)
	}
	if e.TraceID != md.TraceID {
		t.Fatalf("trace = %q, want %q", e.TraceID, md.TraceID)
	}
	if e.FinishedAt < e.StartedAt || e.DurationMs < 0 {
		t.Fatalf("times = %+v", e)
	}
	// Two snapshots of one event: running then final, same ID.
	if got := len(rec.snapshot()); got != 2 {
		t.Fatalf("snapshots = %d, want 2", got)
	}
}

func TestSkillObserveSynthExecuteFailureAndTimeoutAndCancel(t *testing.T) {
	id := testIdentity(t, "demo")
	md := mcpobserve.Metadata{TraceID: mcpobserve.NewID(), RunID: "run"}

	t.Run("exit code", func(t *testing.T) {
		rec := &skillRec{}
		_, err := runObservedSynth(t, newObservedSynth(id, "fail", 10), skillObsCtx(rec, "internal", "", md), `{}`)
		if err == nil {
			t.Fatal("want error")
		}
		e := rec.final()[0]
		if e.Status != "error" || e.ErrorKind != "execution_error" {
			t.Fatalf("event = %+v", e)
		}
		// Raw error text (here the exit status) never enters the event.
		js, _ := json.Marshal(e)
		if strings.Contains(string(js), "exit") || strings.Contains(string(js), "helper failed") {
			t.Fatalf("leak: %s", js)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		rec := &skillRec{}
		_, err := runObservedSynth(t, newObservedSynth(id, "sleep", 1), skillObsCtx(rec, "internal", "", md), `{}`)
		if err == nil {
			t.Fatal("want error")
		}
		e := rec.final()[0]
		if e.Status != "timeout" || e.ErrorKind != "timeout" {
			t.Fatalf("event = %+v", e)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		rec := &skillRec{}
		ctx, cancel := context.WithCancel(skillObsCtx(rec, "internal", "", md))
		go func() {
			time.Sleep(200 * time.Millisecond)
			cancel()
		}()
		_, err := runObservedSynth(t, newObservedSynth(id, "sleep", 30), ctx, `{}`)
		if err == nil {
			t.Fatal("want error")
		}
		e := rec.final()[0]
		if e.Status != "cancelled" || e.ErrorKind != "cancelled" {
			t.Fatalf("event = %+v", e)
		}
	})
}

func TestSkillObserveSynthValidationError(t *testing.T) {
	rec := &skillRec{}
	id := testIdentity(t, "demo")
	st := newObservedSynth(id, "ok", 10)
	_, err := runObservedSynth(t, st, skillObsCtx(rec, "internal", "", mcpobserve.Metadata{TraceID: "t"}), `[1,2]`)
	if err == nil {
		t.Fatal("want error")
	}
	e := rec.final()[0]
	if e.Status != "error" || e.ErrorKind != "validation_error" {
		t.Fatalf("event = %+v", e)
	}
}

func TestSkillObserveSynthNoIdentityNoRecorderUnchanged(t *testing.T) {
	// No identity bound: Start yields a recorder-less span, nothing is
	// emitted, and the tool result is byte-for-byte the old behaviour.
	rec := &skillRec{}
	st := newObservedSynth(skillobserve.Identity{}, "ok", 10)
	res, err := runObservedSynth(t, st, skillObsCtx(rec, "internal", "", mcpobserve.Metadata{TraceID: "t"}), `{}`)
	if err != nil || !strings.Contains(res.Text, "helper ok") {
		t.Fatalf("res = %v err = %v", res.Text, err)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("events = %d, want 0", got)
	}

	// Identity bound but no recorder on the context: still nothing, still fine.
	st2 := newObservedSynth(testIdentity(t, "demo"), "ok", 10)
	res, err = runObservedSynth(t, st2, context.Background(), `{}`)
	if err != nil || !strings.Contains(res.Text, "helper ok") {
		t.Fatalf("res = %v err = %v", res.Text, err)
	}
}

func TestSkillObserveSynthExternalSource(t *testing.T) {
	// The MCP direct-invocation shape: external caller, mcp entrypoint,
	// trace inherited from the inbound span, no session/run invented.
	rec := &skillRec{}
	id := testIdentity(t, "demo")
	st := newObservedSynth(id, "ok", 10)
	md := mcpobserve.Metadata{TraceID: mcpobserve.NewID(), ParentID: mcpobserve.NewID(), Entrypoint: "mcp"}
	_, err := runObservedSynth(t, st, skillObsCtx(rec, "external", "mcp", md), `{}`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	e := rec.final()[0]
	if e.Caller != "external" || e.Entrypoint != "mcp" {
		t.Fatalf("source = %s/%s", e.Caller, e.Entrypoint)
	}
	if e.WireTool != st.info.Name {
		t.Fatalf("MCP exposes the declared dotted name: %+v", e)
	}
	if e.TraceID != md.TraceID || e.ParentID != md.ParentID {
		t.Fatalf("trace = %q parent = %q", e.TraceID, e.ParentID)
	}
	if e.SessionID != "" || e.RunID != "" {
		t.Fatalf("invented correlation: %+v", e)
	}
}

func TestSkillObserveManifestBindsIdentity(t *testing.T) {
	dir := t.TempDir()
	id := testIdentity(t, "demo")
	manifest := `{"tools":[{"name":"demo.tool","description":"d","inputSchema":{"type":"object"},"x-pogo-exec":{"argv":["/bin/true"],"timeout_seconds":5},"x-pogo-renderer":"text","x-pogo-registered":true}]}`
	if err := os.WriteFile(filepath.Join(dir, ManifestFilename), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, catalog, diags := LoadSynthTools([]SynthSource{{Skill: "demo", Dir: dir, Identity: id}})
	if len(diags) != 0 {
		t.Fatalf("diags = %+v", diags)
	}
	if len(loaded) != 1 || len(catalog) != 1 {
		t.Fatalf("loaded = %d catalog = %d", len(loaded), len(catalog))
	}
	st, ok := loaded[0].(*SynthTool)
	if !ok {
		t.Fatalf("type = %T", loaded[0])
	}
	if got := st.SkillIdentity(); got != id {
		t.Fatalf("identity = %+v, want %+v", got, id)
	}
	if catalog[0].Skill != "demo" {
		t.Fatalf("catalog skill = %q", catalog[0].Skill)
	}
}

// --- read-side load events -------------------------------------------------

// skillTree builds dir/SKILL.md and returns a Read whose Cwd is root and
// whose read roots cover the skill directory, plus the injected target.
func skillTree(t *testing.T, content string) (root, skillPath string, target SkillReadTarget) {
	t.Helper()
	root = t.TempDir()
	dir := filepath.Join(root, "skills", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath = filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	id := skillobserve.Identify("demo", "project", skillPath)
	return root, skillPath, SkillReadTarget{Identity: id, Path: skillPath}
}

func newObservedRead(root string, targets []SkillReadTarget) *Read {
	return &Read{Cwd: root, Roots: CanonicalRoots([]string{filepath.Join(root, "skills")}), skillReads: skillReadIndex(targets)}
}

func readExec(t *testing.T, r *Read, ctx context.Context, args string) (Result, error) {
	t.Helper()
	return r.Execute(ctx, json.RawMessage(args))
}

func TestSkillObserveReadRegisteredSkill(t *testing.T) {
	root, skillPath, target := skillTree(t, "# demo\nbody")
	rec := &skillRec{}
	r := newObservedRead(root, []SkillReadTarget{target})
	md := mcpobserve.Metadata{TraceID: mcpobserve.NewID(), SessionID: "s", RunID: "r", ToolCallID: "tc"}
	// A relative path must hit the same registration.
	rel, err := filepath.Rel(root, skillPath)
	if err != nil {
		t.Fatal(err)
	}
	res, err := readExec(t, r, skillObsCtx(rec, "internal", "", md), fmt.Sprintf(`{"path":%q}`, rel))
	if err != nil || !strings.Contains(res.Text, "body") {
		t.Fatalf("res = %v err = %v", res.Text, err)
	}
	finals := rec.final()
	if len(finals) != 1 {
		t.Fatalf("events = %d: %+v", len(finals), finals)
	}
	e := finals[0]
	if e.Stage != "load" || e.Mechanism != "file_read" || e.Status != "ok" {
		t.Fatalf("event = %+v", e)
	}
	if e.SkillID != target.Identity.ID || e.RunID != "r" || e.ToolCallID != "tc" {
		t.Fatalf("event = %+v", e)
	}
	// The canonical path and the argument never enter the event.
	js, _ := json.Marshal(e)
	privatePath, _ := json.Marshal(root)
	if strings.Contains(string(js), string(privatePath[1:len(privatePath)-1])) || strings.Contains(string(js), "SKILL.md") {
		t.Fatalf("path leak: %s", js)
	}
}

func TestSkillObserveWindowsReadCaseAlias(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path spelling")
	}
	root, skillPath, target := skillTree(t, "# case alias")
	rec := &skillRec{}
	r := newObservedRead(root, []SkillReadTarget{target})
	args, _ := json.Marshal(map[string]string{"path": strings.ToLower(skillPath)})
	if _, err := r.Execute(skillObsCtx(rec, "internal", "pogo", mcpobserve.Metadata{}), args); err != nil {
		t.Fatal(err)
	}
	finals := rec.final()
	if len(finals) != 1 || finals[0].SkillID != target.Identity.ID || finals[0].Status != "ok" {
		t.Fatalf("case alias lost registered skill: %+v", finals)
	}
}

func TestSkillObserveReadOrdinaryFileUnrecorded(t *testing.T) {
	root, _, target := skillTree(t, "# demo")
	plain := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(plain, []byte("plain"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &skillRec{}
	r := newObservedRead(root, []SkillReadTarget{target})
	res, err := readExec(t, r, skillObsCtx(rec, "internal", "", mcpobserve.Metadata{TraceID: "t"}), `{"path":"notes.txt"}`)
	if err != nil || res.Text != "plain" {
		t.Fatalf("res = %v err = %v", res.Text, err)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("events = %d, want 0", got)
	}
}

func TestSkillObserveReadMissingSkillFileIsReadError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "skills", "ghost")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "SKILL.md")
	id := skillobserve.Identify("ghost", "project", missing)
	rec := &skillRec{}
	r := newObservedRead(root, []SkillReadTarget{{Identity: id, Path: missing}})
	rel, _ := filepath.Rel(root, missing)
	_, err := readExec(t, r, skillObsCtx(rec, "internal", "", mcpobserve.Metadata{TraceID: "t"}), fmt.Sprintf(`{"path":%q}`, rel))
	if err == nil {
		t.Fatal("want error")
	}
	finals := rec.final()
	if len(finals) != 1 {
		t.Fatalf("events = %d", len(finals))
	}
	if e := finals[0]; e.Status != "error" || e.ErrorKind != "read_error" || e.SkillID != id.ID {
		t.Fatalf("event = %+v", e)
	}
}

func TestSkillObserveReadManyMixedBatch(t *testing.T) {
	root, skillPath, target := skillTree(t, "# demo")
	plain := filepath.Join(root, "skills", "demo", "extra.txt")
	if err := os.WriteFile(plain, []byte("extra"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A second registered skill whose file is gone: the batch must classify
	// each file on its own outcome, not on the call's overall result.
	ghostPath := filepath.Join(root, "skills", "demo", "ghost.md")
	ghost := SkillReadTarget{Identity: skillobserve.Identify("ghost", "project", ghostPath), Path: ghostPath}
	rec := &skillRec{}
	r := newObservedRead(root, []SkillReadTarget{target, ghost})
	args := fmt.Sprintf(`{"paths":[%q,%q,%q]}`,
		filepath.Join("skills", "demo", "SKILL.md"),
		filepath.Join("skills", "demo", "extra.txt"),
		filepath.Join("skills", "demo", "ghost.md"))
	res, err := readExec(t, r, skillObsCtx(rec, "internal", "", mcpobserve.Metadata{TraceID: "t"}), args)
	if err != nil {
		t.Fatalf("partial batch must not fail the call: %v", err)
	}
	if !strings.Contains(res.Text, "demo") || !strings.Contains(res.Text, "[error:") {
		t.Fatalf("res = %q", res.Text)
	}
	finals := rec.final()
	if len(finals) != 2 {
		t.Fatalf("events = %d, want 2: %+v", len(finals), finals)
	}
	bySkill := map[string]skillobserve.Event{}
	for _, e := range finals {
		bySkill[e.SkillName] = e
	}
	if e := bySkill["demo"]; e.Status != "ok" || e.Mechanism != "file_read" {
		t.Fatalf("demo = %+v", e)
	}
	if e := bySkill["ghost"]; e.Status != "error" || e.ErrorKind != "read_error" {
		t.Fatalf("ghost = %+v", e)
	}
	if _, unowned := bySkill["extra.txt"]; unowned {
		t.Fatal("unregistered file attributed")
	}
	_ = skillPath
}

func TestSkillObserveReadUnchangedWithoutMapping(t *testing.T) {
	// No injected map: the read tool is exactly what it was, even with a
	// recorder on the context.
	root, skillPath, _ := skillTree(t, "# demo")
	rec := &skillRec{}
	r := &Read{Cwd: root, Roots: CanonicalRoots([]string{filepath.Join(root, "skills")})}
	res, err := readExec(t, r, skillObsCtx(rec, "internal", "", mcpobserve.Metadata{TraceID: "t"}),
		fmt.Sprintf(`{"path":%q}`, filepath.Join("skills", "demo", "SKILL.md")))
	if err != nil || !strings.Contains(res.Text, "demo") {
		t.Fatalf("res = %v err = %v", res.Text, err)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("events = %d, want 0", got)
	}
	_ = skillPath
}

// The helper stays inert in an ordinary test sweep and operates correctly
// without any inherited environment, matching the Linux sandbox contract.
func TestSkillObserveHelperProcessGuard(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestSkillObserveHelperProcess$")
	cmd.Env = []string{}
	out, err := cmd.CombinedOutput()
	if err != nil || strings.Contains(string(out), "helper ok") {
		t.Fatalf("unguarded helper out = %q err = %v", out, err)
	}
	argv := helperArgv("ok")
	cmd = exec.Command(argv[0], argv[1:]...)
	cmd.Env = []string{}
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "helper ok") {
		t.Fatalf("out = %q err = %v", out, err)
	}
}
