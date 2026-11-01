package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// stub stands in for `pogo -mode json`.
//
// A shell script rather than a compiled binary so the tests do not depend on the
// toolchain being able to build pogo while pogo is being built. What matters is
// that it is a real process speaking the real JSONL contract from Phase 0: the
// thing under test is the parent's handling of a child, and a fake in-process child
// would test nothing about pipes, exit codes or process groups.
//
// Behaviour is selected by STUB_MODE, and the script's working directory is the
// parent workspace, because that is what both modes use.
const stubScript = `#!/bin/sh
say() { printf '%s\n' "$1"; }
say '{"type":"session","ts":1,"session":"/tmp/child.jsonl"}'
case "$STUB_MODE" in
  writes)
    say '{"type":"turn_start","turn":1}'
    say '{"type":"tool_start","call_id":"c1","name":"write"}'
    printf 'from the subagent\n' > added.txt
    say '{"type":"token","text":"I added added.txt"}'
    say '{"type":"run_end","stop_reason":"end_turn","usage":{"input":120,"output":8}}'
    ;;
  readonly)
    say '{"type":"turn_start","turn":1}'
    say '{"type":"token","text":"the entry point is main.go"}'
    say '{"type":"run_end","stop_reason":"end_turn","usage":{"input":90,"output":6}}'
    ;;
  argsdump)
    say '{"type":"turn_start","turn":1}'
    flat=$(printf '%s' "$*" | tr '\n' ' ')
    say "{\"type\":\"token\",\"text\":\"args=$flat roots=$POGO_READ_ROOTS\"}"
    say '{"type":"run_end","stop_reason":"end_turn","usage":{"input":50,"output":5}}'
    ;;
  silent)
    say '{"type":"turn_start","turn":3}'
    say '{"type":"run_end","stop_reason":"end_turn"}'
    ;;
  runerror)
    say '{"type":"turn_start","turn":1}'
    say '{"type":"token","text":"got part way"}'
    say '{"type":"run_end","stop_reason":"error","error":"upstream refused"}'
    ;;
  crash)
    say '{"type":"turn_start","turn":1}'
    printf 'something went wrong\n' >&2
    exit 3
    ;;
  hang)
    say '{"type":"turn_start","turn":1}'
    # A grandchild, like the go test a real subagent would start. Killing only the
    # direct child would reparent this to init and leave it running.
    sleep 30 &
    printf '%s\n' "$!" > "$STUB_PIDFILE"
    sleep 30
    ;;
  tamper)
    say '{"type":"turn_start","turn":1}'
    printf 'gitdir: %s\n' "$STUB_MAIN_GITDIR" > .git
    printf 'sneaky\n' > added.txt
    say '{"type":"token","text":"done"}'
    say '{"type":"run_end","stop_reason":"end_turn"}'
    ;;
  role)
    # Reports whether the read-only child was told what it is.
    say '{"type":"turn_start","turn":1}'
    case "$*" in
      *"<your-role>"*) got=fenced ;;
      *) got=none ;;
    esac
    case "$*" in
      *"no shell"*) sh=stated ;;
      *) sh=silent ;;
    esac
    say "{\"type\":\"token\",\"text\":\"role=$got shell=$sh\"}"
    say '{"type":"run_end","stop_reason":"end_turn"}'
    ;;
  sharednote)
    # Reports which facts of the shared-isolation preamble arrived, and whether the
    # worktree one leaked in. Flags rather than the text, same as the two modes
    # around it: re-encoding a multi-line prompt as JSON from sh is not worth the
    # escaping, and the assertion wants the facts anyway, not the wording.
    say '{"type":"turn_start","turn":1}'
    case "$*" in
      *"<your-directory>"*) got=fenced ;;
      *) got=none ;;
    esac
    case "$*" in
      *"no git here"*) nogit=stated ;;
      *) nogit=silent ;;
    esac
    case "$*" in
      *"full path"*) paths=stated ;;
      *) paths=silent ;;
    esac
    case "$*" in
      *"Other subagents"*) siblings=stated ;;
      *) siblings=silent ;;
    esac
    case "$*" in
      *"<your-checkout>"*) leak=worktree ;;
      *) leak=none ;;
    esac
    say "{\"type\":\"token\",\"text\":\"note=$got nogit=$nogit paths=$paths siblings=$siblings leak=$leak\"}"
    say '{"type":"run_end","stop_reason":"end_turn"}'
    ;;
  prompt)
    # Reports whether the preamble reached it, without trying to re-encode a
    # multi-line prompt as JSON from shell.
    say '{"type":"turn_start","turn":1}'
    case "$*" in
      *"<your-checkout>"*) got=fenced ;;
      *) got=none ;;
    esac
    case "$*" in
      *node_modules*) dep=named ;;
      *) dep=absent ;;
    esac
    # Which model the parent asked for, read back off our own argv.
    mdl=none
    prev=
    for a in "$@"; do
      if [ "$prev" = "-model" ]; then mdl=$a; fi
      prev=$a
    done
    say "{\"type\":\"token\",\"text\":\"preamble=$got dependency=$dep model=$mdl\"}"
    say '{"type":"run_end","stop_reason":"end_turn"}'
    ;;
  probe)
    # Reports where it was put and what it can see from there, which is the whole
    # difference between the two modes.
    say '{"type":"turn_start","turn":1}'
    if [ -f untracked.txt ]; then sees=yes; else sees=no; fi
    say "{\"type\":\"token\",\"text\":\"cwd=$PWD sees_untracked=$sees readonly=${POGO_SUBAGENT_READONLY:-0}\"}"
    say '{"type":"run_end","stop_reason":"end_turn","usage":{"input":10,"output":2}}'
    ;;
  garbage)
    say 'not json at all'
    say '{"type":"turn_start","turn":1}'
    say '{"type":"token","text":"survived the noise"}'
    say '{"truncated json'
    say '{"type":"run_end","stop_reason":"end_turn"}'
    ;;
esac
`

// repoFixture now creates only ordinary files. Keeping its name avoids changing
// gate-channel fixtures that reuse the subprocess protocol.
func repoFixture(t *testing.T) (root, stub string) {
	t.Helper()
	root = t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stub = filepath.Join(t.TempDir(), "pogo-stub")
	if err := os.WriteFile(stub, []byte(stubScript), 0755); err != nil {
		t.Fatal(err)
	}
	return root, stub
}

func call(t *testing.T, s *Subagent, mode, task string) (Result, error) {
	t.Helper()
	args, err := json.Marshal(subagentArgs{Task: task, Mode: mode})
	if err != nil {
		t.Fatal(err)
	}
	return s.Execute(context.Background(), args)
}

func details(t *testing.T, r Result) SubagentDetails {
	t.Helper()
	d, ok := r.Details.(SubagentDetails)
	if !ok {
		t.Fatalf("Details = %T, want SubagentDetails", r.Details)
	}
	return d
}

// Every way a child can fail has to produce something the model can act on. A bare
// "exit status 1" sends it straight back to the same task.
func TestSubagentFailuresAreActionable(t *testing.T) {
	cases := []struct {
		mode string
		want []string
	}{
		{"silent", []string{"without producing an answer", "transcript", "Re-issue"}},
		{"runerror", []string{"upstream refused", "Partial answer", "got part way"}},
		{"crash", []string{"exited with code 3", "something went wrong"}},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			root, stub := repoFixture(t)
			t.Setenv("STUB_MODE", c.mode)
			s := &Subagent{Cwd: root, Exe: stub}

			_, err := call(t, s, ModeEdit, "do the thing")
			if err == nil {
				t.Fatalf("Execute() with a %s child succeeded, want an error", c.mode)
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, missing %q", err, want)
				}
			}
		})
	}
}

// TestSubagentTimeoutKillsTheChild lives in subagent_kill_unix_test.go: it asserts
// a process-group guarantee that only Unix makes, and its syscall.Kill is what kept
// `go vet` from passing for GOOS=windows.

// A child that dies mid-write leaves a partial line. Losing the whole turn over the
// last thirty bytes of a crash would be a bug of our own.
func TestSubagentSurvivesUnparseableLines(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "garbage")
	s := &Subagent{Cwd: root, Exe: stub}

	res, err := call(t, s, ModeEdit, "produce noise")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Text, "survived the noise") {
		t.Errorf("Text = %q, want the answer that arrived between the bad lines", res.Text)
	}
}

// Nesting is bounded by the depth the parent puts in the child's environment, which
// the model cannot write.
func TestSubagentRefusesToNestTooDeep(t *testing.T) {
	root, stub := repoFixture(t)
	s := &Subagent{Cwd: root, Exe: stub, Depth: DefaultSubagentMaxDepth}

	_, err := call(t, s, ModeEdit, "delegate again")
	if err == nil {
		t.Fatal("a subagent at the depth limit was allowed to spawn another")
	}
	if !strings.Contains(err.Error(), "do this task yourself") {
		t.Errorf("error = %v, want it to say what to do instead", err)
	}
	// Nothing was created on the way to refusing.
}

// The misdelegation this pins was observed live: a task whose data only exists
// as a command's output ("read-only analysis, never execute the sample") went
// to an explore child — which has no shell at all — and it spent eleven turns
// proving the files were not there before reporting back empty. The trap is
// lexical: "read-only" in the task describes intent, and the mode line reads
// like the same word. The explore paragraph now says where the line actually
// is: commands, not intent.
func TestExploreDescriptionRulesOutCommandProducedAnswers(t *testing.T) {
	desc := (&Subagent{Cwd: t.TempDir()}).Description()
	for _, want := range []string{"no shell", "commands, not intent", "read-only query CLI"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description is missing %q:\n%s", want, desc)
		}
	}
}

// The env readers decide a safety property from a string, so the case that matters
// is the one nobody types on purpose: a misspelling must land on the isolating
// default *and* be reported, because being silently ignored is what leaves an
// operator convinced the mode is on.
func TestIsolationFromEnv(t *testing.T) {
	for _, c := range []struct {
		set     string
		want    string
		wantErr bool
	}{
		{"", IsolationShared, false},
		{"worktree", "", true},
		{IsolationShared, IsolationShared, false},
		{"  shared  ", IsolationShared, false},
		{"shard", "", true},
		{"SHARED", "", true},
		{"none", "", true},
	} {
		t.Setenv(EnvIsolation, c.set)
		got, err := IsolationFromEnv()
		if got != c.want {
			t.Errorf("%s=%q: got %q, want %q", EnvIsolation, c.set, got, c.want)
		}
		if (err != nil) != c.wantErr {
			t.Errorf("%s=%q: err = %v, wantErr %v", EnvIsolation, c.set, err, c.wantErr)
		}
		// A refusal has to name both choices, or it tells the reader what is wrong
		// without telling them what to write instead.
		if err != nil {
			for _, want := range []string{IsolationShared} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s=%q: err %v does not name %q", EnvIsolation, c.set, err, want)
				}
			}
		}
	}
}

func TestConcurrencyFromEnv(t *testing.T) {
	for _, c := range []struct {
		set     string
		want    int
		wantErr bool
	}{
		{"", 0, false}, // 0 means "leave it to the default", same as the field
		{"4", 4, false},
		{" 4 ", 4, false},
		{"0", 0, true},
		{"-1", 0, true},
		{"lots", 0, true},
	} {
		t.Setenv(EnvConcurrency, c.set)
		got, err := ConcurrencyFromEnv()
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("%s=%q: got (%d, %v), want (%d, wantErr %v)",
				EnvConcurrency, c.set, got, err, c.want, c.wantErr)
		}
	}
}

// Writable delegation leaves outputs in the existing workspace without version history.
func TestSharedIsolationWorksInTheParentsDirectory(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "writes")
	s := &Subagent{Cwd: root, Exe: stub}

	res, err := call(t, s, ModeEdit, "add a file")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	d := details(t, res)
	if d.Isolation != IsolationShared {
		t.Errorf("Details.Isolation = %q, want %q", d.Isolation, IsolationShared)
	}
	if d.Mode != ModeEdit {
		t.Errorf("Details.Mode = %q, want %q", d.Mode, ModeEdit)
	}
	if d.Commit != "" || d.Ref != "" || d.Worktree != "" {
		t.Errorf("Details = %+v, want no commit, ref or worktree under shared isolation", d)
	}
	// The deliverable is the file, in the place the parent can read it.
	body, err := os.ReadFile(filepath.Join(root, "added.txt"))
	if err != nil {
		t.Fatalf("the subagent's file is not in the parent's directory: %v", err)
	}
	if strings.TrimSpace(string(body)) != "from the subagent" {
		t.Errorf("added.txt = %q", body)
	}
	// "changed no files" would be a claim nothing measured: there is no commit to
	// measure with. Saying it would send the parent looking for output that is there.
	if strings.Contains(res.Text, "changed no files") {
		t.Errorf("Text = %q, claims nothing changed with no commit to base that on", res.Text)
	}
	if !strings.Contains(res.Text, "output files remain") {
		t.Errorf("Text = %q, missing output location", res.Text)
	}
}

// Both modes must remain available in ordinary directories.
func TestSharedIsolationNeedsNoRepository(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(t.TempDir(), "pogo-stub")
	if err := os.WriteFile(stub, []byte(stubScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_MODE", "writes")
	s := &Subagent{Cwd: dir, Exe: stub}

	props, _ := s.InputSchema()["properties"].(map[string]any)
	mode, _ := props["mode"].(map[string]any)
	if enum, _ := mode["enum"].([]string); !slices.Equal(enum, []string{ModeExplore, ModeEdit}) {
		t.Errorf("mode enum = %v, want both modes: shared isolation needs no worktree", enum)
	}

	res, err := call(t, s, ModeEdit, "add a file")
	if err != nil {
		t.Fatalf("Execute in a plain directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "added.txt")); err != nil {
		t.Errorf("the subagent's file is missing: %v", err)
	}
	if !strings.Contains(res.Text, "I added added.txt") {
		t.Errorf("Text = %q, want the subagent's answer", res.Text)
	}
}

// The child must name output paths and avoid deleting files outside its task.
func TestSharedChildIsToldWhereItIs(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "sharednote")
	s := &Subagent{Cwd: root, Exe: stub}

	res, err := call(t, s, ModeEdit, "run the analysis")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// The last flag is the important one: the worktree preamble promises a commit
	// that is not coming, so it leaking in would be worse than saying nothing.
	const want = "note=fenced nogit=stated paths=stated siblings=stated leak=none"
	if !strings.Contains(res.Text, want) {
		t.Errorf("Text = %q, want %q", res.Text, want)
	}
}

func TestSubagentRejectsEmptyTask(t *testing.T) {
	root, stub := repoFixture(t)
	s := &Subagent{Cwd: root, Exe: stub}
	if _, err := call(t, s, ModeEdit, "   "); err == nil {
		t.Error("an empty task was accepted")
	}
}

// Skills are an environment property: every child of a session that loaded them
// gets the same -skill flags, so the child's registry (with its readOnlyHint
// filter) sees the same bundles the parent's did. The read roots ride the
// environment, which is the documented inheritance channel — asserted here so
// a refactor that drops os.Environ from the child breaks loudly.
func TestSkillsTravelToEveryChild(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(t.TempDir(), "pogo-stub")
	if err := os.WriteFile(stub, []byte(stubScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_MODE", "argsdump")
	t.Setenv(EnvReadRoots, "/data:/opt/skills/malware-analysis")
	s := &Subagent{
		Cwd: dir, Exe: stub,
		SkillPaths: []string{"/opt/skills/malware-analysis"},
	}

	res, err := call(t, s, ModeExplore, "query the library")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{
		"-skill /opt/skills/malware-analysis",
		"roots=/data:/opt/skills/malware-analysis",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("child did not receive %q:\n%s", want, res.Text)
		}
	}
}

// An explicit Studio selection must survive the process boundary, including
// an empty selection. Global discovery and inherited roots would widen it.
func TestExplicitSkillsRestrictChildDiscoveryAndRoots(t *testing.T) {
	for _, selected := range []bool{false, true} {
		t.Run(strconv.FormatBool(selected), func(t *testing.T) {
			stub := filepath.Join(t.TempDir(), "pogo-stub")
			if err := os.WriteFile(stub, []byte(stubScript), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("STUB_MODE", "argsdump")
			t.Setenv(EnvReadRoots, "/other-agent/private-skill")
			s := &Subagent{Cwd: t.TempDir(), Exe: stub, ExplicitSkills: true}
			if selected {
				s.SkillPaths = []string{"/opt/selected-skill"}
				s.SkillReadRoots = []string{"/opt/selected-skill", "/shared-data"}
			}
			res, err := call(t, s, ModeExplore, "inspect the selected capability")
			if err != nil {
				t.Fatalf("Execute: %v, %+v", err, res)
			}
			if !strings.Contains(res.Text, "-no-skills") {
				t.Fatalf("child may discover unselected skills: %s", res.Text)
			}
			if strings.Contains(res.Text, "/other-agent/private-skill") {
				t.Fatalf("child inherited unselected roots: %s", res.Text)
			}
			if selected {
				for _, want := range []string{"-skill /opt/selected-skill", "roots=" + strings.Join(s.SkillReadRoots, string(os.PathListSeparator))} {
					if !strings.Contains(res.Text, want) {
						t.Errorf("child did not receive %q: %s", want, res.Text)
					}
				}
			} else if strings.Contains(res.Text, "-skill ") || !strings.Contains(res.Text, "roots=") {
				t.Fatalf("empty selection must stay empty: %s", res.Text)
			}
			if got := os.Getenv(EnvReadRoots); got != "/other-agent/private-skill" {
				t.Fatalf("parent environment changed: %s", got)
			}
		})
	}
}

// The explore note names the read-only declared tools it holds, for the same
// reason it says "no shell" rather than leaving the tool absent: a silence is
// read as "look harder". Without any, the sentence is absent — a plain coding
// session must not be told about tools it does not have.
func TestExploreNoteNamesTheReadOnlyDeclaredTools(t *testing.T) {
	with := (&Subagent{ReadOnlySkillTools: []string{"mal_search", "mal_similar"}}).exploreNote()
	if !strings.Contains(with, "mal_search, mal_similar") {
		t.Errorf("exploreNote with tools is missing the names:\n%s", with)
	}
	if !strings.Contains(with, "declared read-only tools") {
		t.Errorf("exploreNote with tools is missing the framing sentence:\n%s", with)
	}
	if without := (&Subagent{}).exploreNote(); strings.Contains(without, "declared read-only tools") {
		t.Errorf("exploreNote without tools mentions declared tools:\n%s", without)
	}
}

// The parent has to know the two facts it was observed getting wrong: declared
// tools do travel to children, and naming one in the task is the right brief.
// Without skills the paragraph is absent, so a plain coding session's
// description does not talk about tools nobody declared.
func TestDescriptionBriefsTheParentOnSkillTools(t *testing.T) {
	s := &Subagent{SkillPaths: []string{"/opt/skills/malware-analysis"}}
	d := s.Description()
	for _, want := range []string{
		"inherit this session's skills",
		"declared read-only",
		"name it in the task",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("Description with skills is missing %q", want)
		}
	}
	if plain := (&Subagent{}).Description(); strings.Contains(plain, "this session's skills") {
		t.Error("Description without skills mentions skills")
	}
}

// Both modes see files as they exist when the task starts.
func TestBothModesSeeCurrentWorkspaceFiles(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "probe")
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("new work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Subagent{Cwd: root, Exe: stub}

	res, err := call(t, s, ModeExplore, "what is in this directory")
	if err != nil {
		t.Fatalf("explore: %v", err)
	}
	if !strings.Contains(res.Text, "sees_untracked=yes") {
		t.Errorf("an explore child could not see the parent's uncommitted file: %q", res.Text)
	}

	res, err = call(t, s, ModeEdit, "what is in this directory")
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !strings.Contains(res.Text, "sees_untracked=yes") {
		t.Errorf("an edit child cannot see the current workspace; "+
			"the input contract is wrong: %q", res.Text)
	}
}

// Explore runs in place and leaves nothing behind: no worktree, no commit, no ref.
// The absence is the point — a question that only needed reading should cost one
// process and nothing on disk.
func TestExploreCreatesNoWorktreeAndNoCommit(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "probe")
	s := &Subagent{Cwd: root, Exe: stub}

	res, err := call(t, s, ModeExplore, "where is the entry point")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	d := details(t, res)
	if d.Mode != ModeExplore {
		t.Errorf("Details.Mode = %q, want %q", d.Mode, ModeExplore)
	}
	if d.Worktree != "" || d.Ref != "" || d.Commit != "" {
		t.Errorf("Details = %+v, want no worktree and no commit", d)
	}
	// The child was told it is read-only, and told through the environment so the
	// model in it cannot argue with the setting.
	if !strings.Contains(res.Text, "readonly=1") {
		t.Errorf("the child was not marked read-only: %q", res.Text)
	}
	if !strings.Contains(res.Text, "cwd="+resolved(t, root)) {
		t.Errorf("the child ran somewhere other than the parent's directory %s: %q", root, res.Text)
	}
	// "changed no files" is edit-mode language; after an explore run it would be
	// noise about something that was never possible.
	if strings.Contains(res.Text, "changed no files") {
		t.Errorf("explore result talks about changing files: %q", res.Text)
	}
}

// Read-only delegation works in an ordinary directory.
func TestExploreWorksOutsideAGitRepo(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(t.TempDir(), "pogo-stub")
	if err := os.WriteFile(stub, []byte(stubScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_MODE", "probe")
	s := &Subagent{Cwd: dir, Exe: stub}

	res, err := call(t, s, ModeExplore, "what is here")
	if err != nil {
		t.Fatalf("explore outside a repo: %v", err)
	}
	if !strings.Contains(res.Text, "cwd="+resolved(t, dir)) {
		t.Errorf("ran in the wrong directory, want %s: %q", dir, res.Text)
	}
}

// resolved is what a shell's $PWD reports for a path. On macOS t.TempDir() hands
// back a path under /var, which is itself a link to /private/var, so comparing the
// two spellings fails for reasons that have nothing to do with the code.
func resolved(t *testing.T, path string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return out
}

// A missing or unknown mode is refused before anything is spawned, and the refusal
// says what the two choices mean. The alternative — defaulting — is the documented
// failure elsewhere: a subagent asked to fix something silently comes back with
// advice and no changes, which reads like success.
func TestSubagentRequiresAKnownMode(t *testing.T) {
	root, stub := repoFixture(t)
	s := &Subagent{Cwd: root, Exe: stub}

	for _, mode := range []string{"", "readonly", "Explore", "write"} {
		_, err := call(t, s, mode, "do something")
		if err == nil {
			t.Fatalf("mode %q was accepted", mode)
		}
		for _, want := range []string{ModeExplore, ModeEdit} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusing mode %q: %v, missing the %q option", mode, err, want)
			}
		}
	}
}

// An explore child has to be told what it is, because the alternative was observed:
// given no statement that it has no shell, a live child decided to confirm its
// finding by running the tests, spent its remaining turns looking for a way, and
// returned nothing. An absent tool is a silence, and a model reads silence as
// "look harder".
func TestExploreChildIsToldItHasNoShell(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "role")
	s := &Subagent{Cwd: root, Exe: stub}

	res, err := call(t, s, ModeExplore, "where is the entry point")
	if err != nil {
		t.Fatalf("explore: %v", err)
	}
	if !strings.Contains(res.Text, "role=fenced") {
		t.Errorf("explore child got no role preamble: %q", res.Text)
	}
	if !strings.Contains(res.Text, "shell=stated") {
		t.Errorf("explore child was not told it has no shell: %q", res.Text)
	}

	// An edit child has a shell, so the same note would be a lie.
	res, err = call(t, s, ModeEdit, "change something")
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if strings.Contains(res.Text, "role=fenced") {
		t.Errorf("an edit child was told it is read-only: %q", res.Text)
	}
}

// The note itself, asserted directly: the part that changes behaviour is not
// "you have no shell" but what to do instead of hunting for one.
func TestExploreNoteSaysWhatToDoInstead(t *testing.T) {
	got := (&Subagent{}).exploreNote()
	for _, want := range []string{
		"<your-role>", "</your-role>",
		"no shell",             // the capability is absent, not merely unlisted
		"no way around it",     // do not go looking
		"say exactly what you", // report the command you would have run
		"stop",                 // and end the turn
		"uncommitted",          // it does see the parent's real files
	} {
		if !strings.Contains(got, want) {
			t.Errorf("exploreNote() is missing %q:\n%s", want, got)
		}
	}
}

// A read-only child may run a different model than its parent, and only a
// read-only one. An edit child is doing the work the parent would have done, so
// downgrading it would change the outcome of the task rather than the cost of a
// lookup.
func TestOnlyExploreChildrenCanRunADifferentModel(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "prompt")
	s := &Subagent{Cwd: root, Exe: stub, Model: "parent-model", ExploreModel: "small-model"}

	res, err := call(t, s, ModeExplore, "look something up")
	if err != nil {
		t.Fatalf("explore: %v", err)
	}
	if !strings.Contains(res.Text, "model=small-model") {
		t.Errorf("explore child did not run the configured model: %q", res.Text)
	}
	if d := details(t, res); d.Model != "small-model" {
		t.Errorf("Details.Model = %q, want the model actually run", d.Model)
	}

	res, err = call(t, s, ModeEdit, "change something")
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !strings.Contains(res.Text, "model=parent-model") {
		t.Errorf("an edit child was downgraded: %q", res.Text)
	}

	// Unset means inherit, which is the default and must stay the behaviour for
	// anyone with no configuration file.
	s.ExploreModel = ""
	res, err = call(t, s, ModeExplore, "look again")
	if err != nil {
		t.Fatalf("explore without a mapping: %v", err)
	}
	if !strings.Contains(res.Text, "model=parent-model") {
		t.Errorf("explore child did not inherit: %q", res.Text)
	}
}

// A delegated run has structure — turns, tool calls, an ending — and an interface
// that only gets "· read" cannot show any of it. The child already speaks the same
// event contract its parent's consumers speak, so the parent forwards the events
// whole instead of flattening them.
func TestSubagentForwardsTheChildsEventsAsFrames(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "writes")
	s := &Subagent{Cwd: root, Exe: stub}

	var frames []json.RawMessage
	var text strings.Builder
	args, err := json.Marshal(subagentArgs{Task: "add a file", Mode: ModeEdit})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecuteStreaming(context.Background(), args, func(p Partial) {
		if len(p.Frame) > 0 {
			frames = append(frames, p.Frame)
		}
		text.WriteString(p.Text)
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Every frame is a parseable event of the same contract, not a line of prose.
	var kinds []string
	for _, f := range frames {
		var e struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(f, &e); err != nil {
			t.Fatalf("frame is not valid JSON: %s: %v", f, err)
		}
		if e.Type == "" {
			t.Errorf("frame has no type: %s", f)
		}
		kinds = append(kinds, e.Type)
	}
	for _, want := range []string{"session", "turn_start", "tool_start", "run_end"} {
		if !contains(kinds, want) {
			t.Errorf("frames = %v, want one of type %q", kinds, want)
		}
	}
	// An allowlist, so anything not on it is absent — including the two delta streams.
	// A live run with the earlier denylist spent 40% of its frames on `thinking`,
	// which no interface displayed — that was the reason to filter, and the UI
	// has since grown a place to show it. The two delta streams are now forwarded
	// as coalesced tail frames (see emitTailFrame): every frame of a kind carries
	// the accumulated tail, never a bare delta, so a consumer joining from the
	// last frame alone can still render. `message` stays out either way.
	for _, unwanted := range []string{"message"} {
		if contains(kinds, unwanted) {
			t.Errorf("%q was forwarded; the filter is an allowlist: %v", unwanted, kinds)
		}
	}
	// The token tail frames specifically: present, and growing — the last one
	// carries the whole answer when it fits the tail budget.
	var lastToken string
	sawToken := false
	for _, f := range frames {
		var e struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(f, &e); err != nil {
			t.Fatalf("frame is not valid JSON: %s: %v", f, err)
		}
		if e.Type == "token" {
			sawToken = true
			lastToken = e.Text
		}
	}
	if !sawToken {
		t.Errorf("no token tail frame in %v; the live answer stream is gone", kinds)
	}
	if sawToken && !strings.Contains(lastToken, "added.txt") {
		t.Errorf("last token tail = %q, want the accumulated answer", lastToken)
	}
	// Text still arrives too. The two consumers want different things and neither
	// should have to parse the other's: a terminal wants the line, a browser wants
	// the event.
	if !strings.Contains(text.String(), "write") {
		t.Errorf("human-readable text was dropped: %q", text.String())
	}
}

// The explore pool spreads concurrent children across providers least-in-flight
// first: a burst lands one per provider before any provider sees a second, and
// a finished child hands its slot back.
func TestExplorePoolBalancesLeastInFlight(t *testing.T) {
	s := &Subagent{ExplorePool: []ExploreTarget{
		{Provider: "a", Model: "ma"},
		{Provider: "b", Model: "mb"},
		{Provider: "c", Model: "mc"},
	}}

	want := []string{"ma", "mb", "mc", "ma", "mb", "mc"}
	for i, w := range want {
		if m, _ := s.pickExplore(); m != w {
			t.Fatalf("pick %d = %q, want %q", i, m, w)
		}
	}
	// Six forks in flight, two per provider. A child on b finishing makes b the
	// least loaded again, so the next fork goes there — not to the next in line.
	s.releaseExplore("b")
	if m, _ := s.pickExplore(); m != "mb" {
		t.Errorf("after a b child exits, pick = %q, want mb", m)
	}
}

// In-flight counts are per provider, not per pool entry: two models on one
// endpoint share the endpoint's concurrency, so the second entry is not a way
// to sneak twice the load onto it.
func TestExplorePoolCountsPerProvider(t *testing.T) {
	s := &Subagent{ExplorePool: []ExploreTarget{
		{Provider: "a", Model: "m1"},
		{Provider: "a", Model: "m2"},
		{Provider: "b", Model: "m3"},
	}}
	if m, _ := s.pickExplore(); m != "m1" {
		t.Fatalf("first pick = %q, want m1", m)
	}
	if m, _ := s.pickExplore(); m != "m3" {
		t.Errorf("second pick = %q, want m3: provider a already has one in flight", m)
	}
}

// The explicit single-model mapping still wins over the pool, and no pool at
// all means inherit the parent — the behaviour before pools existed.
func TestExplorePoolPriority(t *testing.T) {
	s := &Subagent{
		ExploreModel: "fixed",
		ExplorePool:  []ExploreTarget{{Provider: "a", Model: "ma"}},
	}
	if m, p := s.pickExplore(); m != "fixed" || p != "" {
		t.Errorf("explicit mapping: pick = (%q, %q), want (fixed, \"\")", m, p)
	}
	plain := &Subagent{}
	if m, p := plain.pickExplore(); m != "" || p != "" {
		t.Errorf("no pool: pick = (%q, %q), want inherit", m, p)
	}
	// A no-charge pick must not panic on release.
	plain.releaseExplore("")
}
