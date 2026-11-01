package main

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/tools"
)

// toolOptions is the fork point: one function decides what a top-level run gets and
// what a child gets, and the difference between them is the containment. Tested
// here rather than through a spawned process because the inputs are three
// environment variables and the output is a struct — going through a real child
// would test the pipes again and this not at all.
func TestToolOptionsPerSituation(t *testing.T) {
	cases := []struct {
		name     string
		depth    string
		readOnly string
		// want
		readOnlyOpt bool
		guard       bool
		subagent    bool
		todo        bool
	}{{
		// The case that must not change: a person at a terminal has unrestricted
		// bash and can delegate.
		name: "top level", subagent: true, todo: true,
	}, {
		// An edit child: bash is guarded so it cannot reach the main checkout
		// through git, and it cannot delegate further.
		name: "edit child", depth: "1", guard: true,
	}, {
		// An explore child: no bash to guard, and no subagent tool, so the
		// read-only promise cannot be escaped by delegating.
		name: "explore child", depth: "1", readOnly: "1", readOnlyOpt: true,
	}, {
		// Depth is absent but read-only is set. Should still be read-only: the two
		// markers are independent, and treating a missing depth as "top level" must
		// not silently hand write tools to a child.
		//
		// It does get the task list, and that is correct rather than an oversight:
		// the list is withheld by depth, not by write access, because what makes it
		// pointless in a child is having no reader — no compaction boundary to
		// survive and a parent card already showing the progress. A read-only
		// top-level session has both.
		name: "read-only without depth", readOnly: "1", readOnlyOpt: true, todo: true,
	}, {
		// Garbage depth reads as zero rather than failing, because a broken
		// environment must not make pogo unusable at a terminal.
		name: "unparseable depth", depth: "not a number", subagent: true, todo: true,
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(tools.EnvSubagentDepth, c.depth)
			t.Setenv(tools.EnvSubagentReadOnly, c.readOnly)
			o := toolOptions("/tmp/wd", "some-model", nil, []string{"/tmp/mem"}, nil, nil, nil)

			// Memory follows the task list exactly: top level only, withheld by not
			// granting the root rather than by refusing the call. The reason is the todo
			// one — a child lives for one run and never crosses the session boundary
			// memory exists for, so its notes have no reader, and what would be left is
			// a second writer for "what do I know about this project".
			if hasMemory := len(o.WriteRoots) > 0; hasMemory != c.todo {
				t.Errorf("WriteRoots granted = %v, want %v (same rule as the task list)",
					hasMemory, c.todo)
			}

			if o.ReadOnly != c.readOnlyOpt {
				t.Errorf("ReadOnly = %v, want %v", o.ReadOnly, c.readOnlyOpt)
			}
			if (o.Guard != nil) != c.guard {
				t.Errorf("Guard set = %v, want %v", o.Guard != nil, c.guard)
			}
			if (o.Subagent != nil) != c.subagent {
				t.Errorf("Subagent set = %v, want %v", o.Subagent != nil, c.subagent)
			}
			if o.Todo != c.todo {
				t.Errorf("Todo = %v, want %v", o.Todo, c.todo)
			}
			// Whatever the situation, a registry built from these options never
			// contains a tool that can change something unless write tools were
			// meant to be there.
			r := tools.New(o)
			for _, name := range []string{"write", "edit", "bash", "subagent"} {
				if _, ok := r.Get(name); ok && c.readOnlyOpt {
					t.Errorf("read-only options produced a registry containing %q", name)
				}
			}
			// Withheld structurally, the same way the subagent tool is. A child
			// that could see a "todo" tool could spend a turn on it, and the
			// registry is the only place able to make that impossible.
			if _, ok := r.Get("todo"); ok != c.todo {
				t.Errorf("registry has todo = %v, want %v", ok, c.todo)
			}
		})
	}
}

// The child is told what it is through the environment, not through a flag. A model
// can propose flags via the tool call it is making; it never writes the environment
// of a process its parent spawns. If these ever became flags, both the depth bound
// and the read-only promise would be things the model could argue with.
func TestChildMarkersComeFromTheEnvironment(t *testing.T) {
	t.Setenv(tools.EnvSubagentReadOnly, "1")
	if !readOnlyChild() {
		t.Error("readOnlyChild() = false with the marker set")
	}
	// Only an exact "1" counts. Anything else is a misconfiguration, and the safe
	// reading of a misconfiguration here is "not a read-only child", because the
	// parent that spawns one always sets it correctly — a stray value means this is
	// not a child at all.
	for _, v := range []string{"", "0", "true", "yes", "2"} {
		t.Setenv(tools.EnvSubagentReadOnly, v)
		if readOnlyChild() {
			t.Errorf("readOnlyChild() = true for %q", v)
		}
	}
	t.Setenv(tools.EnvSubagentDepth, "3")
	if got := subagentDepth(); got != 3 {
		t.Errorf("subagentDepth() = %d, want 3", got)
	}
	t.Setenv(tools.EnvSubagentDepth, "-1")
	if got := subagentDepth(); got != 0 {
		t.Errorf("subagentDepth() = %d for a negative value, want 0", got)
	}
}

// A child's turn limit is its own, not the parent's. A turn limit bounds one run,
// and a subagent is a different run: how many steps a delegated task needs has
// nothing to do with how much conversation the parent has left. Observed live —
// a parent at -max-turns 4 gave its child four turns for a whole task, and the
// child spent them reading files and returned nothing.
//
// Model is inherited and turns are not, which looks inconsistent until you ask what
// each one measures. A different model changes the answer; a different turn budget
// only changes how many steps are allowed to produce it.
func TestChildDoesNotInheritTheParentsTurnLimit(t *testing.T) {
	t.Setenv(tools.EnvSubagentDepth, "")
	t.Setenv(tools.EnvSubagentReadOnly, "")
	o := toolOptions("/tmp/wd", "parent-model", nil, nil, nil, nil, nil)
	if o.Subagent == nil {
		t.Fatal("no subagent tool registered at the top level")
	}
	if o.Subagent.MaxTurns != 0 {
		t.Errorf("Subagent.MaxTurns = %d, want 0 so the child uses its own default",
			o.Subagent.MaxTurns)
	}
	// The model still is inherited, or an expensive parent would silently delegate
	// to whatever the default happens to be.
	if o.Subagent.Model != "parent-model" {
		t.Errorf("Subagent.Model = %q, want the parent's", o.Subagent.Model)
	}
}

// Legacy shared configuration remains accepted; worktree must not silently
// turn into an in-place edit after an upgrade.
func TestCheckSubagentEnvRejectsRetiredWorktree(t *testing.T) {
	for _, value := range []string{"", "shared"} {
		t.Setenv(tools.EnvIsolation, value)
		var notices strings.Builder
		if err := checkSubagentEnv(&notices); err != nil || notices.Len() != 0 {
			t.Errorf("isolation %q: err=%v notices=%q", value, err, notices.String())
		}
	}
	for _, c := range []struct{ env, value string }{
		{tools.EnvIsolation, "worktree"},
		{tools.EnvIsolation, "shard"},
		{tools.EnvConcurrency, "lots"},
		{tools.EnvConcurrency, "0"},
	} {
		t.Setenv(tools.EnvIsolation, "")
		t.Setenv(tools.EnvConcurrency, "")
		t.Setenv(c.env, c.value)
		err := checkSubagentEnv(io.Discard)
		if err == nil || !strings.Contains(err.Error(), c.env) {
			t.Errorf("%s=%q: got %v, want a named configuration error", c.env, c.value, err)
		}
	}
}

// A delegated run's transcript belongs one level down, where no session picker
// looks; a top-level run's belongs at the top, where they all do. Tested here
// because the choice is two environment variables and a path join — a spawned
// child would test the pipes again and this not at all.
func TestTranscriptDirFollowsDepth(t *testing.T) {
	base := t.TempDir()
	t.Setenv("POGO_SESSION_DIR", base)

	t.Setenv(tools.EnvSubagentDepth, "")
	if got := transcriptDir(); got != base {
		t.Errorf("top level: transcriptDir() = %q, want %q", got, base)
	}
	t.Setenv(tools.EnvSubagentDepth, "1")
	want := filepath.Join(base, session.SubagentDirName)
	if got := transcriptDir(); got != want {
		t.Errorf("child: transcriptDir() = %q, want %q", got, want)
	}
	// A garbage depth reads as top level, matching subagentDepth.
	t.Setenv(tools.EnvSubagentDepth, "deep")
	if got := transcriptDir(); got != base {
		t.Errorf("garbage depth: transcriptDir() = %q, want %q", got, base)
	}
}

// The child's half of the browser channel. Registration has to be conditional on
// the parent actually having attached one: a browser tool that is present but
// always fails would cost schema tokens and invite retries, which is the same
// argument the subagent and todo tools are withheld by absence rather than by
// refusal.
func TestChildGetsTheBrowserToolOnlyWhenTheParentLentAPane(t *testing.T) {
	t.Setenv(tools.EnvSubagentDepth, "1")

	t.Setenv(tools.EnvBrowserFD, "")
	if o := toolOptions("/tmp/wd", "m", nil, nil, nil, nil, nil); o.Browser != nil {
		t.Fatal("without a channel the child must not register the browser tool")
	}

	t.Setenv(tools.EnvBrowserFD, browserFDForTest(t))
	o := toolOptions("/tmp/wd", "m", nil, nil, nil, nil, nil)
	if o.Browser == nil {
		t.Fatal("with a channel the child must register the browser tool")
	}
	// Nothing to lend onward, and that is the depth cap doing it rather than a
	// browser decision: DefaultSubagentMaxDepth is 1, so a child gets no subagent
	// tool and therefore never has children. Pinned here because the day that cap
	// rises, lending has to be decided deliberately instead of inherited by
	// accident.
	if o.Subagent != nil {
		t.Fatal("a depth-1 child should have no subagent tool to lend a pane to")
	}

	// Garbage reads as absent rather than panicking on a bad descriptor.
	t.Setenv(tools.EnvBrowserFD, "not a number")
	if o := toolOptions("/tmp/wd", "m", nil, nil, nil, nil, nil); o.Browser != nil {
		t.Fatal("an unparseable descriptor must read as no channel at all")
	}
}

// A read-only explore child is exactly what browsing delegation is for — reading
// pages is a read — so the channel must survive the read-only filter.
func TestExploreChildKeepsTheBrowserChannel(t *testing.T) {
	t.Setenv(tools.EnvSubagentDepth, "1")
	t.Setenv(tools.EnvSubagentReadOnly, "1")
	t.Setenv(tools.EnvBrowserFD, browserFDForTest(t))

	o := toolOptions("/tmp/wd", "m", nil, nil, nil, nil, nil)
	if !o.ReadOnly {
		t.Fatal("the read-only marker should still apply")
	}
	if o.Browser == nil {
		t.Fatal("an explore child must still be able to read pages")
	}
}

// browserFDForTest returns a descriptor number this process genuinely owns.
//
// Naming an arbitrary number instead is a trap worth recording: OpenFDBrowser
// wraps base and base+1 in os.File, which takes ownership, so the wrappers'
// finalizers close descriptors the test binary was using. The symptom is not a
// failure here — these assertions pass — but an unrelated later test hanging on a
// pipe whose end was closed underneath it.
//
// The pipe is deliberately never closed: the wrappers own those descriptors now,
// and closing them here as well would be the same double-close bug pointed the
// other way. Two leaked descriptors for the life of a test binary is the cheap
// side of that trade.
func browserFDForTest(t *testing.T) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if int(w.Fd()) != int(r.Fd())+1 {
		r.Close()
		w.Close()
		t.Skipf("pipe descriptors %d and %d are not consecutive", r.Fd(), w.Fd())
	}
	return strconv.Itoa(int(r.Fd()))
}
