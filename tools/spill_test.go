package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// A tool that runs something spills the output that did not fit and names the file
// in the result. That note is the only thing telling the model its view was cut
// short, so it is the last place in the tool allowed to be wrong.

// The success path is asserted by reading the file back. The note promises the
// full output, so the file has to hold all of it rather than the truncated view.
func TestBashSpillsTheCompleteOutputAndNamesWhereItWent(t *testing.T) {
	b := &Bash{Cwd: t.TempDir(), SpillDir: t.TempDir()}
	res, err := b.Execute(context.Background(), args(t, bashArgs{Command: "seq 1 2500"}))
	if err != nil {
		t.Fatal(err)
	}
	d, ok := res.Details.(BashDetails)
	if !ok {
		t.Fatalf("details are %T, want BashDetails", res.Details)
	}
	if !d.Truncated {
		t.Fatalf("2500 lines should not have fit in %d", MaxLines)
	}
	if d.FullOutputPath == "" {
		t.Fatal("no spill path reported for a truncated result")
	}

	// The two channels have to agree: the model reads the note, the interface reads
	// the details, and a path in one but not the other is a silent disagreement.
	if !strings.Contains(res.Text, d.FullOutputPath) {
		t.Error("the note does not name the file the details point at")
	}
	// A pointer with no instruction is read as infrastructure noise, so the note
	// has to say what to do with the path, not just that there is one.
	if !strings.Contains(res.Text, "offset") || !strings.Contains(res.Text, "grep") {
		t.Errorf("the note does not say how to retrieve the rest:\n%s", noteOf(res.Text))
	}

	full := read(t, d.FullOutputPath)
	if got := strings.Count(full, "\n"); got != 2500 {
		t.Errorf("the spill file holds %d lines, want 2500", got)
	}
	if !strings.HasPrefix(full, "1\n") {
		t.Error("the spill file does not start at the beginning of the output")
	}
	// The distinction worth pinning: the file is the whole output, not a second copy
	// of the tail the model already has.
	if len(full) <= len(res.Text) {
		t.Errorf("spill file is %d bytes and the truncated result is %d: it is not the full output",
			len(full), len(res.Text))
	}
}

// The point of the whole mechanism: the path handed to the model is one the model
// can open. read refuses anything outside the working directory and the readable
// roots, so a spill directory that is not a root produces a pointer to a refusal —
// worse than plain truncation, because the model believes the content is
// recoverable and reports a confident answer built on the half it can see.
//
// cwd and the spill directory are deliberately unrelated here. That is the shape
// of a real deployment: a container whose workspace is /home/agent and whose
// POGO_READ_ROOTS lists the data mounts, where /tmp — bash's spill directory
// before this — is reachable by nothing at all. The guarantee has to come from New
// putting the two together, not from an operator remembering to.
func TestSpilledOutputIsReachableByTheReadTool(t *testing.T) {
	ctx := context.Background()
	cwd, spillDir := t.TempDir(), t.TempDir()
	reg := New(Options{Cwd: cwd, SpillDir: spillDir})

	bash, ok := reg.Get("bash")
	if !ok {
		t.Fatal("no bash tool in the registry")
	}
	res, err := bash.Execute(ctx, args(t, bashArgs{Command: "seq 1 2500"}))
	if err != nil {
		t.Fatal(err)
	}
	path := res.Details.(BashDetails).FullOutputPath
	if path == "" {
		t.Fatal("no spill path reported for a truncated result")
	}
	if within(canonical(path), canonical(cwd)) {
		t.Fatal("the spill file landed inside cwd, so this proves nothing about the roots")
	}

	rd, ok := reg.Get("read")
	if !ok {
		t.Fatal("no read tool in the registry")
	}
	got, err := rd.Execute(ctx, args(t, readArgs{Path: path}))
	if err != nil {
		t.Fatalf("read refused the path its own registry spilled to: %v", err)
	}
	if !strings.Contains(got.Text, "\n1\n") && !strings.HasPrefix(got.Text, "1\n") {
		t.Errorf("read did not return the start of the spilled output:\n%s", firstN(got.Text, 120))
	}
	// The other half of the promise: reading the spill file back must not spill
	// again. read is truncated and never spilled, so the cascade has a bottom.
	if strings.Contains(got.Text, "Full output:") {
		t.Errorf("reading a spill file produced another spill:\n%s", noteOf(got.Text))
	}
	if !strings.Contains(got.Text, "Use offset=") {
		t.Errorf("read dropped its own continuation hint:\n%s", noteOf(got.Text))
	}
}

// The production path, which no other test here exercises: main.go and the web
// manager never set Options.SpillDir, so the directory comes from DefaultSpillDir
// and therefore from the environment. Worth its own test because it is the only
// arrangement that actually ships, and because the deployment that motivated this
// work is precisely the one where the old behaviour was broken.
//
// The shape is a container's: a workspace directory, a read-root whitelist that
// lists the data mounts and nothing else, and a relocated spill directory outside
// both. Under the old code the spill file went to /tmp — in no root, reachable by
// nothing, with the note cheerfully naming it.
func TestSpillDirFromTheEnvironmentIsStillReadable(t *testing.T) {
	ctx := context.Background()
	cwd, dataMount, spillDir := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv(SpillDirEnv, spillDir)

	if got := DefaultSpillDir(); got != spillDir {
		t.Fatalf("DefaultSpillDir() = %q, want the configured %q", got, spillDir)
	}
	// SpillDir deliberately left empty: this is what the production callers pass.
	reg := New(Options{Cwd: cwd, ReadRoots: []string{dataMount}})

	bash, _ := reg.Get("bash")
	res, err := bash.Execute(ctx, args(t, bashArgs{Command: "seq 1 2500"}))
	if err != nil {
		t.Fatal(err)
	}
	path := res.Details.(BashDetails).FullOutputPath
	if path == "" {
		t.Fatal("no spill path reported for a truncated result")
	}
	if !within(canonical(path), canonical(spillDir)) {
		t.Fatalf("spilled to %q, want it under the configured %q", path, spillDir)
	}

	rd, _ := reg.Get("read")
	if _, err := rd.Execute(ctx, args(t, readArgs{Path: path})); err != nil {
		t.Fatalf("read refused a path resolved from %s: %v", SpillDirEnv, err)
	}
	// And the whitelist it was handed still works, so adding the spill root did not
	// come at the cost of the roots the deployment asked for.
	if _, err := rd.Execute(ctx, args(t, readArgs{Path: write(t, dataMount, "sample.txt", "x")})); err != nil {
		t.Fatalf("the configured read root stopped working: %v", err)
	}
}

// Two identical outputs must spill to one path. The note carrying that path is part
// of the tool result, and the result is hashed to notice an agent going in circles
// (agent.hashToolResults) — a fresh temp name per call makes two identical turns
// hash differently, which is precisely the bug that check's comment warns about for
// the tool_use id, arriving through a different door.
func TestSpillIsContentAddressedSoIdenticalOutputIsOnePath(t *testing.T) {
	dir := t.TempDir()

	first, ok := Spill(dir, "bash", "same payload")
	if !ok {
		t.Fatal("first spill failed")
	}
	second, ok := Spill(dir, "bash", "same payload")
	if !ok {
		t.Fatal("second spill failed")
	}
	if first != second {
		t.Errorf("identical output spilled to two paths:\n%s\n%s", first, second)
	}
	other, ok := Spill(dir, "bash", "different payload")
	if !ok {
		t.Fatal("third spill failed")
	}
	if other == first {
		t.Error("different output spilled to the same path")
	}
	if got := read(t, first); got != "same payload" {
		t.Errorf("spilled %q, want %q", got, "same payload")
	}

	// Nothing partial left behind: the write is create-temp-then-rename, and a
	// leftover .partial-* is a file nothing points at that looks like a log.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("spill directory holds %v, want exactly two files", names)
	}
}

// The same property one level up, because that is where it has to hold: the note a
// model reads must be byte-identical for two identical runs, or the prompt prefix
// changes every turn and the stagnation check can never fire.
func TestTheSpillNoteIsStableAcrossIdenticalRuns(t *testing.T) {
	dir := t.TempDir()
	one := KeepTail(dir, "bash", strings.Repeat("line\n", MaxLines+500))
	two := KeepTail(dir, "bash", strings.Repeat("line\n", MaxLines+500))
	if !one.Truncated || one.Path == "" {
		t.Fatal("the input should have been truncated and spilled")
	}
	if one.Text() != two.Text() {
		t.Errorf("two identical runs produced different text:\n%s\n%s",
			noteOf(one.Text()), noteOf(two.Text()))
	}
}

// The same property end to end on the real tool, because this is the one the
// stagnation detector consumes: agent.hashToolResults hashes the result text, so
// two identical commands have to produce identical text or the check can never
// fire. It could not, before this: the spill path was a fresh os.CreateTemp name
// every call, sitting in the note, inside the hashed text.
//
// TestHashToolResults in the agent package pins the other half — that the hash is
// over the text and not the tool_use id — and the two together are the guarantee.
func TestIdenticalBashRunsProduceIdenticalText(t *testing.T) {
	ctx := context.Background()
	dir, spillDir := t.TempDir(), t.TempDir()
	b := &Bash{Cwd: dir, SpillDir: spillDir}

	first, err := b.Execute(ctx, args(t, bashArgs{Command: "seq 1 2500"}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.Execute(ctx, args(t, bashArgs{Command: "seq 1 2500"}))
	if err != nil {
		t.Fatal(err)
	}
	if first.Details.(BashDetails).FullOutputPath == "" {
		t.Fatal("the output should have been truncated and spilled")
	}
	if first.Text != second.Text {
		t.Errorf("two identical runs produced different text, so stagnation can never fire:\n%s\n%s",
			noteOf(first.Text), noteOf(second.Text))
	}
}

// When the output cannot be saved, no path may be reported. Until this was fixed
// every error here was discarded and the path handed over anyway, so a full disk
// produced a note promising the complete output at a file holding nothing — or
// holding part of it, which is worse, because the model can read that one and get a
// confident answer out of a fragment it was told was whole.
func TestBashReportsNoPathWhenTheOutputCannotBeSaved(t *testing.T) {
	dir := t.TempDir()
	// A regular file as the spill directory, so MkdirAll cannot make it and no file
	// can be created inside it.
	b := &Bash{Cwd: dir, SpillDir: write(t, dir, "not-a-directory", "")}
	res, err := b.Execute(context.Background(), args(t, bashArgs{Command: "seq 1 2500"}))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Details.(BashDetails)
	if !d.Truncated {
		t.Fatal("2500 lines should not have fit")
	}
	if d.FullOutputPath != "" {
		t.Errorf("reported %q although nothing was written there", d.FullOutputPath)
	}
	if strings.Contains(res.Text, "Full output") {
		t.Errorf("the note still promises full output:\n%s", noteOf(res.Text))
	}
	if !strings.Contains(res.Text, "could not be saved") {
		t.Errorf("the note does not say the rest is unavailable:\n%s", noteOf(res.Text))
	}
	// The truncation itself must still be announced. Losing the spill is a degraded
	// result; losing the fact that output was cut would be a wrong one, because the
	// model would read a partial tail as the whole answer.
	if !strings.Contains(res.Text, "of 2500") {
		t.Errorf("the note no longer says how much was cut:\n%s", noteOf(res.Text))
	}
}

// Spill reports a path only together with a complete file, so callers can treat a
// non-empty path as a promise. Tested directly because the tools only ever ask the
// question one way round.
func TestSpillReportsNothingWhenItCannotWrite(t *testing.T) {
	dir := t.TempDir()
	if path, ok := Spill(dir, "bash", "payload"); !ok || path == "" {
		t.Fatalf("spill to a usable directory failed: path=%q ok=%v", path, ok)
	} else if got := read(t, path); got != "payload" {
		t.Errorf("spilled %q, want %q", got, "payload")
	}

	path, ok := Spill(write(t, dir, "not-a-directory", ""), "bash", "payload")
	if ok {
		t.Error("spill claimed success with no writable directory")
	}
	if path != "" {
		t.Errorf("spill returned %q alongside a failure", path)
	}
}

// A declared tool's output is the case the generalisation exists for. The command
// has already run — a CAPA scan, a database query — so the part that was cut cannot
// be asked for again without paying for the whole run a second time. Before this,
// truncated meant gone for every manifest tool.
//
// The assertion that matters is the last one: the file holds the *original* output,
// not the truncated view. Spilling after truncating would produce a file that looks
// complete, reads plausibly, and is missing its beginning — which is the failure
// mode this ordering exists to make impossible.
func TestSynthSpillsTheOutputItCouldNotShow(t *testing.T) {
	st := synthFrom(t)
	st.SpillDir = t.TempDir()
	// echo is the manifest's argv; one long argument is the cheapest way to make it
	// print far more than a result can hold. The argument has to stay under the
	// kernel's 128KB per-argument ceiling (MAX_ARG_STRLEN) — past it exec fails
	// with E2BIG on Linux and the tool never runs at all.
	line := strings.Repeat("x", 200)
	var b strings.Builder
	for range 400 {
		b.WriteString(line)
		b.WriteString("\n")
	}
	payload := strings.TrimSuffix(b.String(), "\n")

	res, err := st.Execute(context.Background(), json.RawMessage(
		fmt.Sprintf(`{"a": %q}`, payload)))
	if err != nil {
		// Spilling needs more than MaxBytes (50KB) in one argument, and that
		// does not fit every exec ceiling: Windows caps the whole command line
		// at 32KB, so there the tool never runs at all. E2BIG is the platform
		// declining the fixture, not the property failing, so skip on it.
		if strings.Contains(err.Error(), "too long") {
			t.Skipf("the platform's argv ceiling is below what spilling needs: %v", err)
		}
		t.Fatalf("execute: %v", err)
	}
	d := res.Details.(SynthDetails)
	if !d.Truncated {
		t.Fatalf("80KB of output should not have fit in %s", formatSize(MaxBytes))
	}
	if d.FullOutputPath == "" {
		t.Fatal("a declared tool truncated its output and spilled nothing")
	}
	if !strings.Contains(res.Text, d.FullOutputPath) {
		t.Error("the note does not name the file the details point at")
	}
	// Room for the note on top of the ceiling, and nothing like the 80KB the
	// command actually printed.
	if len(res.Text) > MaxBytes+2048 {
		t.Errorf("the result is %d bytes, over the %s ceiling plus its note",
			len(res.Text), formatSize(MaxBytes))
	}

	full := read(t, d.FullOutputPath)
	if len(full) < len(payload) {
		t.Errorf("the spill file is %d bytes and the output was at least %d: it is the truncated view, not the output",
			len(full), len(payload))
	}
	if !strings.HasPrefix(full, line) {
		t.Error("the spill file does not start at the beginning of the output")
	}
}

// noteOf returns the trailing bracketed note, so a failure message shows the part
// under test instead of two thousand lines of seq output.
func noteOf(text string) string {
	if i := strings.LastIndex(text, "["); i >= 0 {
		return text[i:]
	}
	return text
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
