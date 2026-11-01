package tools

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// synthFrom builds a SynthTool from a valid manifest, for tests that exercise
// execution rather than loading.
func synthFrom(t *testing.T) *SynthTool {
	t.Helper()
	body := `{"tools": [{
		"name": "t", "description": "test tool",
		"inputSchema": {"type": "object", "properties": {
			"a": {"type": "string"}, "n": {"type": "number"}, "b": {"type": "boolean"}
		}},
		"x-pogo-exec": {"argv": ["echo"], "map": {
			"a": "append", "n": {"flag": "--n"}, "b": {"flag": "--force", "presence": true}
		}}
	}]}`
	src := writeManifest(t, "s", body)
	got, _, diags := LoadSynthTools([]SynthSource{src})
	if len(diags) != 0 {
		t.Fatalf("load diagnostics: %+v", diags)
	}
	st := got[0].(*SynthTool)
	st.Cwd = t.TempDir()
	return st
}

func TestSynthExecuteArgv(t *testing.T) {
	st := synthFrom(t)
	res, err := st.Execute(context.Background(), json.RawMessage(
		`{"a": "hello world", "n": 1099511627776, "b": true}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	d := res.Details.(SynthDetails)
	// One argv element per argument: "hello world" must arrive whole, and the
	// number must keep its literal rather than going through a float.
	// Properties are visited in sorted name order (a, b, n) — declaration
	// order does not survive JSON decoding, and a stable order is the property
	// worth keeping.
	want := []string{"echo", "hello world", "--force", "--n", "1099511627776"}
	if strings.Join(d.Argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %q, want %q", d.Argv, want)
	}
	if d.ExitCode != 0 || d.TimedOut {
		t.Fatalf("details = %+v", d)
	}
}

func TestSynthExecuteAbsentAndFalse(t *testing.T) {
	st := synthFrom(t)
	res, err := st.Execute(context.Background(), json.RawMessage(`{"a": "x"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	d := res.Details.(SynthDetails)
	if strings.Join(d.Argv, " ") != "echo x" {
		t.Fatalf("argv = %q, want [echo x]", d.Argv)
	}
}

func TestSynthExecuteBadArgType(t *testing.T) {
	st := synthFrom(t)
	// The object validator would catch this first in the real loop
	// (ValidateArgs runs before the gate), and Execute re-checks because a
	// tool must be safe to call directly.
	if _, err := st.Execute(context.Background(), json.RawMessage(`{"a": {"nested": true}}`)); err == nil {
		t.Fatal("object-typed argument should be refused, not stringified")
	}
}

// A positional value that begins with '-' is a flag in disguise: getopt parses
// argv by shape, and some tools (malquery's reorderArgs) actively hoist
// dash-prefixed positionals into the flag area. The manifest never declared
// that flag, so the call must fail rather than reach the tool reshaped.
func TestSynthExecuteRejectsDashPrefixedPositional(t *testing.T) {
	st := synthFrom(t)
	_, err := st.Execute(context.Background(), json.RawMessage(`{"a": "--sha256-only"}`))
	if err == nil {
		t.Fatal("a dash-prefixed positional was passed through as an argv element")
	}
	if !strings.Contains(err.Error(), "must not begin with '-'") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
	// The flag kind is unaffected: its value travels as the flag's own
	// argument, which getopt consumes literally rather than re-parsing.
	res, err := st.Execute(context.Background(), json.RawMessage(`{"n": -3}`))
	if err != nil {
		t.Fatalf("a negative number as a declared flag's value must still work: %v", err)
	}
	if d := res.Details.(SynthDetails); d.Argv[len(d.Argv)-1] != "-3" {
		t.Fatalf("flag value did not arrive literally: %q", d.Argv)
	}
}

func TestSynthExecuteFailureCarriesDetails(t *testing.T) {
	st := synthFrom(t)
	// false appended via echo: exit code 0 but prints "false"; instead force a
	// real failure through a command that does not exist.
	st.exec = synthExec{argv: []string{"sh", "-c", "echo boom >&2; exit 3"}, argMap: st.exec.argMap}
	res, err := st.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("non-zero exit must be an error the model can read")
	}
	d := res.Details.(SynthDetails)
	if d.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", d.ExitCode)
	}
	// Failure output travels on the error, bash's contract: the model reads
	// it there, and Details still carries the code for the card.
	if !strings.Contains(err.Error(), "code 3") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error %q should carry the stderr and the exit code", err)
	}
}

func TestSynthExecuteTimeout(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep on PATH")
	}
	st := synthFrom(t)
	st.exec = synthExec{argv: []string{"sleep", "5"}, timeout: 1}
	_, err := st.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout error, got %v", err)
	}
}

func TestSynthValidateArgsIntegration(t *testing.T) {
	st := synthFrom(t)
	// The shared validator reads the manifest schema: missing required fields
	// and enum misses are rejected before the gate with the standard message.
	if err := ValidateArgs(st, json.RawMessage(`{"n": 5}`)); err != nil {
		t.Fatalf("valid args rejected: %v", err)
	}
	st2 := synthFrom(t)
	st2.info.InputSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"mode": map[string]any{"type": "string", "enum": []any{"fast", "slow"}},
		},
		"required": []any{"mode"},
	}
	err := ValidateArgs(st2, json.RawMessage(`{"mode": "quik"}`))
	if err == nil || !strings.Contains(err.Error(), `"fast", "slow"`) {
		t.Fatalf("enum miss should name the alternatives, got %v", err)
	}
}
