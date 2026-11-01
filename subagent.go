package main

import (
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/tools"
)

// toolOptions decides which tools this process gets.
//
// One function for both sides of the fork, because the interesting part is that
// they are not symmetric and the asymmetry has to be visible in one place: a
// parent gets the subagent tool and unrestricted bash, a child gets neither. Two
// separate constructions would drift.
//
// skillPaths are the -skill flags this process was given; forwarded into the
// subagent tool so children inherit the session's skills (Subagent.SkillPaths).
// The terminal's paths, not skills.Roots of the loaded list: a child re-loads
// the bundles itself and needs the same paths the operator named.
//
// mcp are the tools the mcpclient pool projected for this session. The caller
// passes nil for a subagent child — the connections behind the tools are
// stateful and owned by the parent's pool, so v1 does not inherit them (see
// run(), where the pool is built only at depth 0).
func toolOptions(cwd, model string, readRoots, writeRoots []string, synth, mcp []tools.Tool, skillPaths []string) tools.Options {
	o := tools.Options{Cwd: cwd, ReadRoots: readRoots, ReadOnly: readOnlyChild(), Synth: synth, MCP: mcp, CommandProtectionManifest: os.Getenv(tools.EnvCommandProtectionManifest)}
	depth := subagentDepth()
	if depth > 0 {
		selected, err := tools.NativeSelectionFromEnvironment()
		if err != nil {
			empty := []string{}
			selected = &empty
		}
		o.Enabled = selected
		// Only a spawned child consumes this capability. A top-level CLI never
		// guesses a library from SessionDir or a stale server environment.
		o.Information = tools.InformationFromEnvironment(cwd)
	}

	// A child has no browser of its own, so it borrows its parent's pane over a
	// pipe — the same arrangement as the approval gate, and registered on the same
	// terms: only when the parent actually gave it a channel. Nil everywhere else,
	// which is how "this process cannot drive a page" stays structural rather than
	// becoming a refusal the model can argue with.
	//
	// Deliberately not gated on the read-only marker. An explore child is exactly
	// the kind of child this is for: reading pages is a read, and keeping ten
	// snapshots out of the parent's context is the whole reason to delegate the
	// reading. It is not lent onward either, and that needs no code — the subagent
	// tool below is registered only below DefaultSubagentMaxDepth, which is 1, so a
	// child never has children to lend anything to.
	if fdb := tools.OpenFDBrowser(); fdb != nil {
		o.Browser = fdb
	}

	// Memory belongs to the top-level session only, and is withheld the same way the
	// task list is: by not granting the root, so a child cannot spend a turn finding
	// out it is refused.
	//
	// A retained child resumes its own transcript, not the parent's project-wide
	// memory. Keeping those separate prevents narrow specialist conclusions from
	// becoming a second writer of shared project policy. Retaining a context must
	// not silently add capabilities that the original delegation did not have.
	//
	// A read-only child is refused too, though it could do no harm writing. Reading is
	// not the risk; being handed a set of conclusions is. An explore child is asked one
	// narrow question and answers from the code, and notes it did not write are context
	// it cannot verify and did not ask for.
	if depth == 0 {
		o.WriteRoots = writeRoots
	}

	// Only the top-level session keeps a task list. Withheld from every child
	// regardless of mode, for the reasons in tools.Options.Todo — and withheld the
	// same way the subagent tool is, by not registering it, so a child cannot spend
	// a turn discovering that it exists but refuses.
	o.Todo = depth == 0

	// Writable children share the workspace; the guard catches accidental
	// directory escapes and Git mutations. OS sandboxing remains independent.
	if depth > 0 && !o.ReadOnly {
		o.Guard = &tools.Guard{Workspace: cwd}
	}
	// Registered only while there is room to nest. Not registering it is a stronger
	// bound than refusing the call: a tool that is absent costs no schema tokens
	// and cannot be attempted, whereas one that always refuses invites retries.
	//
	// A read-only child never gets it, whatever the depth. Delegating is not itself
	// a write, but an explore child that could spawn an edit child would be a way
	// to reach write tools from a session that was promised not to have any.
	if depth < tools.DefaultSubagentMaxDepth && !o.ReadOnly {
		// MaxTurns is deliberately absent: see Subagent.MaxTurns. The parent's turn
		// limit bounds the parent's run, and the child's is a different run.
		// Both env settings are validated in main before anything runs, so the errors
		// here cannot be new information; the safe value comes back with them.
		concurrency, _ := tools.ConcurrencyFromEnv()
		o.Subagent = &tools.Subagent{
			Enabled: o.Enabled,
			Cwd:     cwd, Model: model, Depth: depth,
			// Resolved here rather than inside the tool so that package tools keeps
			// knowing nothing about the model catalogue. Empty unless the user's
			// config names one, in which case explore children run it instead.
			ExploreModel: config.SubagentModel(model),
			ExplorePool:  explorePool(),
			Concurrency:  concurrency,
			// The skills this session loaded travel to every child, and the
			// read-only declared tools are named for the explore note. Only a
			// parent reports them: a child has no subagent tool to inform, and a
			// read-only child's own registry already applied the filter.
			SkillPaths:         skillPaths,
			ReadOnlySkillTools: readOnlySynthNames(synth),
		}
		if o.Information != nil && o.Information.Err == nil && o.Information.Store != nil {
			o.Subagent.InformationRoot = o.Information.Store.Root()
			o.Subagent.InformationScope = o.Information.Store.ScopeID()
		}
	}
	return o
}

// readOnlySynthNames names the declared tools an explore child keeps; see
// tools.ReadOnlySynthNames. Local wrapper only so the comment can say what this
// caller wants it for.
func readOnlySynthNames(synth []tools.Tool) []string {
	return tools.ReadOnlySynthNames(synth)
}

// checkSubagentEnv validates delegation concurrency and rejects a retired
// worktree configuration before a task starts. The writer lease is internal.
func checkSubagentEnv(_ io.Writer) error {
	if subagentDepth() > 0 {
		if _, err := tools.NativeSelectionFromEnvironment(); err != nil {
			return err
		}
	}
	if err := tools.AdoptSubagentEditLease(); err != nil {
		return err
	}
	if _, err := tools.IsolationFromEnv(); err != nil {
		return err
	}
	_, err := tools.ConcurrencyFromEnv()
	return err
}

// subagentDepth reads how deep this process is.
//
// From the environment rather than a flag on purpose. A model can propose flags
// through the tool it is calling; it never writes the environment of a process its
// parent spawns. That makes the depth bound something the model cannot argue with.
func subagentDepth() int {
	n, err := strconv.Atoi(os.Getenv(tools.EnvSubagentDepth))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// readOnlyChild reports whether this process was spawned as an explore-mode
// subagent. Same reasoning as subagentDepth: it arrives in the environment
// because the model must not be able to argue with it.
func readOnlyChild() bool {
	return os.Getenv(tools.EnvSubagentReadOnly) == "1"
}

// transcriptDir picks where this process writes its own session file. A
// delegated run's transcript goes one level down, out of every session picker
// (see session.SubagentDirName). Retained children are resumed through their
// parent's scoped registry, never promoted to ordinary main conversations.
// The depth comes from the environment, so the model cannot argue with it.
func transcriptDir() string {
	dir := session.DefaultDir()
	if subagentDepth() > 0 {
		dir = filepath.Join(dir, session.SubagentDirName)
	}
	return dir
}

// explorePool adapts the configured subagent pool into the tool's plain shape;
// package tools stays ignorant of the catalogue.
func explorePool() []tools.ExploreTarget {
	pool := config.SubagentPool()
	out := make([]tools.ExploreTarget, 0, len(pool))
	for _, m := range pool {
		out = append(out, tools.ExploreTarget{Provider: m.Provider, Model: m.ID})
	}
	return out
}
