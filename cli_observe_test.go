package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/skillobserve"
	"github.com/yosukeno/pogo/skills"
)

// writeSkill materializes one loadable skill under dir.
func writeSkill(t *testing.T, dir, name string) skills.Skill {
	t.Helper()
	skDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(skDir, "SKILL.md")
	body := "---\nname: " + name + "\ndescription: test skill\n---\n\n# " + name + "\n\ndo the thing.\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return skills.Skill{Name: name, Path: path, Dir: skDir, Source: "path"}
}

// memoryObserver builds the observer the CLI would get with
// POGO_MCP_LOG_DB=none: everything works, nothing persists.
func memoryObserver(t *testing.T, list []skills.Skill) *cliObserver {
	t.Helper()
	t.Setenv("POGO_CLI_CALLS", "")
	t.Setenv("POGO_MCP_LOG_DB", "none")
	t.Setenv("POGO_MCP_CALLS", "")
	t.Setenv("POGO_SKILL_CALLS", "")
	obs := openCLIObservability(list)
	if obs == nil {
		t.Fatal("observer disabled unexpectedly")
	}
	t.Cleanup(obs.Close)
	return obs
}

func skillEvents(t *testing.T, obs *cliObserver) []skillobserve.Event {
	t.Helper()
	page, err := obs.rt.Skills.Query(context.Background(), skillobserve.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	return page.Events
}

// POGO_CLI_CALLS=0 disables CLI instrumentation wholesale.
func TestCLIObserveDisabledByEnv(t *testing.T) {
	t.Setenv("POGO_CLI_CALLS", "0")
	if obs := openCLIObservability(nil); obs != nil {
		obs.Close()
		t.Fatal("POGO_CLI_CALLS=0 must disable CLI observation")
	}
	t.Setenv("POGO_CLI_CALLS", "")
	t.Setenv("POGO_MCP_CALLS", "0")
	t.Setenv("POGO_SKILL_CALLS", "0")
	if obs := openCLIObservability(nil); obs != nil {
		obs.Close()
		t.Fatal("both halves disabled must not open a runtime")
	}
}

// Each run gets its own local run ID; discovery is recorded per run from the
// actually loaded skill list, with the cli entrypoint and the real session.
func TestCLIRunContextAndDiscovery(t *testing.T) {
	sk := writeSkill(t, t.TempDir(), "demo")
	obs := memoryObserver(t, []skills.Skill{sk})
	obs.sessionID = "session-real-1"

	ctx1, run1 := obs.runContext(context.Background(), "")
	obs.recordDiscovery(ctx1)
	ctx2, run2 := obs.runContext(context.Background(), "")
	obs.recordDiscovery(ctx2)

	if run1 == "" || run1 == run2 {
		t.Fatalf("run ids must be real and distinct: %q vs %q", run1, run2)
	}
	md := mcpobserve.MetadataFrom(ctx1)
	if md.SessionID != "session-real-1" || md.Entrypoint != "cli" || md.RunID != run1 {
		t.Errorf("run metadata = %+v", md)
	}

	events := skillEvents(t, obs)
	if len(events) != 2 {
		t.Fatalf("discovery events = %d, want 2 (one per run)", len(events))
	}
	wantID := skillobserve.Identify(sk.Name, sk.Source, canonicalCLIPath(sk.Path))
	for _, e := range events {
		if e.SkillID != wantID.ID || e.Stage != "discovery" || e.Mechanism != "catalog" {
			t.Errorf("unexpected discovery event: %+v", e)
		}
		if e.Caller != "internal" || e.Entrypoint != "cli" {
			t.Errorf("source = %q/%q, want internal/cli", e.Caller, e.Entrypoint)
		}
		if e.SessionID != "session-real-1" {
			t.Errorf("session = %q, want the real session id", e.SessionID)
		}
	}
	if events[0].TraceID == events[1].TraceID {
		t.Error("separate runs must not share a trace without an explicit load")
	}
	runs := map[string]bool{}
	for _, e := range events {
		runs[e.RunID] = true
	}
	if !runs[run1] || !runs[run2] {
		t.Errorf("discovery run ids %v, want %q and %q", runs, run1, run2)
	}
}

// Hidden (disable-model-invocation) skills are never catalog discoveries,
// and a skill without a SKILL.md path records nothing.
func TestCLIDiscoverySkipsHiddenAndPathless(t *testing.T) {
	vis := writeSkill(t, t.TempDir(), "visible")
	hidden := writeSkill(t, t.TempDir(), "hidden")
	hidden.DisableModelInvocation = true
	pathless := skills.Skill{Name: "bare", Source: "path"}
	obs := memoryObserver(t, []skills.Skill{vis, hidden, pathless})

	ctx, _ := obs.runContext(context.Background(), "")
	obs.recordDiscovery(ctx)
	events := skillEvents(t, obs)
	if len(events) != 1 || events[0].SkillName != "visible" {
		t.Fatalf("discovery events = %+v, want exactly the visible skill", events)
	}
}

// An explicit /skill load is measured as its own span, and the run that
// consumes the expansion continues the same trace with a fresh run id.
func TestCLIExplicitSkillLoadSharesTrace(t *testing.T) {
	sk := writeSkill(t, t.TempDir(), "demo")
	obs := memoryObserver(t, []skills.Skill{sk})
	obs.sessionID = "session-real-1"

	expanded, trace, err := obs.loadSkillExplicit(sk, "")
	if err != nil {
		t.Fatal(err)
	}
	if expanded == "" || trace == "" {
		t.Fatalf("expanded/trace must be non-empty: %q / %q", expanded, trace)
	}
	ctx, runID := obs.runContext(context.Background(), trace)
	obs.recordDiscovery(ctx)

	events := skillEvents(t, obs)
	var load, discovery int
	for _, e := range events {
		if e.TraceID != trace {
			t.Errorf("event %s/%s has trace %q, want shared %q",
				e.Stage, e.Mechanism, e.TraceID, trace)
		}
		switch e.Stage {
		case "load":
			load++
			if e.Mechanism != "explicit" || e.Status != "ok" {
				t.Errorf("load event = %+v", e)
			}
			if e.RunID != "" {
				t.Errorf("explicit load must not claim a run id, got %q", e.RunID)
			}
		case "discovery":
			discovery++
			if e.RunID != runID {
				t.Errorf("discovery run = %q, want %q", e.RunID, runID)
			}
		}
	}
	if load != 1 || discovery != 1 {
		t.Errorf("load=%d discovery=%d, want 1/1", load, discovery)
	}
}

// A failed explicit load is a non-success observation, and it yields no
// trace for a following run to continue.
func TestCLIExplicitSkillLoadFailure(t *testing.T) {
	sk := writeSkill(t, t.TempDir(), "demo")
	obs := memoryObserver(t, []skills.Skill{sk})
	if err := os.Remove(sk.Path); err != nil {
		t.Fatal(err)
	}
	expanded, trace, err := obs.loadSkillExplicit(sk, "")
	if err == nil {
		t.Fatal("missing SKILL.md must fail")
	}
	if expanded != "" {
		t.Errorf("failed load returned text %q", expanded)
	}
	events := skillEvents(t, obs)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 load failure", len(events))
	}
	e := events[0]
	if e.Stage != "load" || e.Status != "error" || e.ErrorKind != "read_error" {
		t.Errorf("failed load event = %+v, want load/error/read_error", e)
	}
	// trace is still returned (the span exists) but callers only forward it
	// on success; assert the failure left a traceable span either way.
	if e.TraceID == "" {
		t.Error("failed load span lost its trace")
	}
	_ = trace
}

// Read targets bind exactly the loaded skills' canonical SKILL.md paths —
// nothing else can be attributed to a skill by the read tool.
func TestCLIReadTargetsFromLoadedSkills(t *testing.T) {
	sk := writeSkill(t, t.TempDir(), "demo")
	pathless := skills.Skill{Name: "bare", Source: "path"}
	obs := memoryObserver(t, []skills.Skill{sk, pathless})
	targets := obs.readTargets()
	if len(targets) != 1 {
		t.Fatalf("read targets = %d, want 1", len(targets))
	}
	if targets[0].Path != canonicalCLIPath(sk.Path) {
		t.Errorf("target path = %q, want canonical %q", targets[0].Path, canonicalCLIPath(sk.Path))
	}
	want := skillobserve.Identify(sk.Name, sk.Source, canonicalCLIPath(sk.Path))
	if targets[0].Identity != want {
		t.Errorf("target identity = %+v, want %+v", targets[0].Identity, want)
	}
}

// synthSources binds each manifest source to its loaded skill's identity, so
// a manifest tool's execution is attributed to the skill that declared it.
func TestSynthSourcesBindIdentity(t *testing.T) {
	sk := writeSkill(t, t.TempDir(), "demo")
	srcs := synthSources([]skills.Skill{sk})
	if len(srcs) != 1 {
		t.Fatalf("synth sources = %d, want 1", len(srcs))
	}
	want := skillobserve.Identify(sk.Name, sk.Source, canonicalCLIPath(sk.Path))
	if srcs[0].Identity != want {
		t.Errorf("synth identity = %+v, want %+v", srcs[0].Identity, want)
	}
}
