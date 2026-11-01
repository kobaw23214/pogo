package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Agent layer is the project convention pointed one level down: a
// directory per Agent definition, labelled as its own scope so a model
// weighing a note knows how near the record is.
func TestLoadAgentLabelsItsScope(t *testing.T) {
	workspace := t.TempDir()
	write(t, filepath.Join(workspace, ".pogo", DirName, "decisions.md"), "x", time.Time{})

	s, diags := LoadAgent(workspace)
	if len(diags) != 0 {
		t.Fatalf("diags = %v", diags)
	}
	if s.Empty() {
		t.Fatal("an existing note was not listed")
	}
	section := s.PromptSection()
	if !strings.Contains(section, `scope="agent"`) {
		t.Errorf("the agent layer is not labelled:\n%s", section)
	}
	if !strings.Contains(section, "decisions.md") {
		t.Errorf("the note is missing from the listing:\n%s", section)
	}

	// An empty workspace is a valid store with nothing in it, never an error.
	s, diags = LoadAgent("")
	if len(diags) != 0 || !s.Empty() || len(s.Roots()) != 0 {
		t.Errorf("empty workspace: diags = %v, roots = %v", diags, s.Roots())
	}
}

// LoadAgent creates the directory rather than skipping it, for the same reason
// Load does: a missing directory cannot be granted to the tools, so skipping
// would leave a fresh Agent with no way to take its first note.
func TestLoadAgentCreatesTheDirectory(t *testing.T) {
	workspace := t.TempDir()
	s, _ := LoadAgent(workspace)
	if len(s.Roots()) != 1 || s.Roots()[0] != ProjectDir(workspace) {
		t.Fatalf("roots = %v", s.Roots())
	}
	info, err := os.Stat(ProjectDir(workspace))
	if err != nil || !info.IsDir() {
		t.Fatalf("the directory was not created: %v", err)
	}
}

// The Agent layer lists ahead of the owner's: the nearer scope is the more
// relevant record, and precedence order is the reading order.
func TestCombineListsTheNearerScopeFirst(t *testing.T) {
	userDir, workspace := t.TempDir(), t.TempDir()
	userAt(t, userDir)
	write(t, filepath.Join(userDir, "shared.md"), "x", time.Time{})
	write(t, filepath.Join(workspace, ".pogo", DirName, "own.md"), "x", time.Time{})

	owner, _ := Load(Options{User: true})
	agent, _ := LoadAgent(workspace)
	combined := agent.Combine(owner)

	if len(combined.Roots()) != 2 {
		t.Fatalf("combined roots = %v", combined.Roots())
	}
	section := combined.PromptSection()
	ai, ui := strings.Index(section, `scope="agent"`), strings.Index(section, `scope="user"`)
	if ai < 0 || ui < 0 || ai > ui {
		t.Errorf("the agent layer is not listed first:\n%s", section)
	}

	// Combining leaves the receiver untouched, and a nil other side is fine.
	if len(agent.Roots()) != 1 || len(owner.Roots()) != 1 {
		t.Error("Combine mutated one of its inputs")
	}
	if got := agent.Combine(nil); len(got.Roots()) != 1 {
		t.Errorf("Combine(nil) dropped the receiver's roots: %v", got.Roots())
	}
}
