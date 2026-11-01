package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func toolNamed(t *testing.T, name string) Tool {
	t.Helper()
	tool, ok := Default(t.TempDir()).Get(name)
	if !ok {
		t.Fatalf("no tool named %q", name)
	}
	return tool
}

func validate(t *testing.T, name, args string) error {
	t.Helper()
	return ValidateArgs(toolNamed(t, name), json.RawMessage(args))
}

// The message has to name the field the tool actually wants, because that is the
// only thing the model can act on. Before this, an absent field produced no error
// at all: it arrived as a zero value and the tool failed later for a reason that
// looked unrelated.
func TestMissingRequiredFieldIsNamed(t *testing.T) {
	cases := []struct{ tool, args, want string }{
		{"read", `{}`, `missing required field "path"`},
		{"bash", `{}`, `missing required field "command"`},
		{"write", `{"path":"a.go"}`, `missing required field "content"`},
		{"grep", `{"path":"."}`, `missing required field "pattern"`},
		{"find", `{}`, `missing required field "pattern"`},
	}
	for _, c := range cases {
		err := validate(t, c.tool, c.args)
		if err == nil {
			t.Errorf("%s%s: expected an error", c.tool, c.args)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %q, want it to contain %q", c.tool, err, c.want)
		}
		// The field list lets the model correct itself from the error alone rather
		// than guessing or asking to see the schema again.
		if !strings.Contains(err.Error(), "Expected:") {
			t.Errorf("%s: message does not list the accepted fields: %q", c.tool, err)
		}
	}
}

// A model that sends file_path needs to be told which name this tool uses. Saying
// only "path is missing" leaves it to guess that its own key was the problem.
func TestAMisspelledFieldIsPointedOut(t *testing.T) {
	for _, sent := range []string{"file_path", "filePath", "Path", "filepath"} {
		err := validate(t, "read", `{"`+sent+`":"a.go"}`)
		if err == nil {
			t.Fatalf("%s: expected an error", sent)
		}
		if !strings.Contains(err.Error(), sent) {
			t.Errorf("%s: message does not mention what was received: %q", sent, err)
		}
	}

	// A near-match is called out as the likely culprit.
	if err := validate(t, "read", `{"file_path":"a.go"}`); err == nil ||
		!strings.Contains(err.Error(), `received "file_path"`) {
		t.Errorf("a near-match should be named as the culprit: %v", err)
	}

	// An unrelated key is reported neutrally rather than blamed. It is still worth
	// naming — "cmd" for "command" is a real mistake no cheap similarity test
	// catches — but the message must not claim it was meant to be the missing one.
	err := validate(t, "read", `{"reason":"because"}`)
	if err == nil {
		t.Fatal("expected an error about the missing path")
	}
	if !strings.Contains(err.Error(), "unrecognised field(s) received: reason") {
		t.Errorf("unrecognised fields should be listed: %q", err)
	}
	if strings.Contains(err.Error(), `received "reason"`) {
		t.Errorf("an unrelated field was blamed as the intended one: %q", err)
	}

	// The abbreviation case, which is why the neutral branch exists.
	if err := validate(t, "bash", `{"cmd":"ls"}`); err == nil ||
		!strings.Contains(err.Error(), "cmd") {
		t.Errorf("bash with cmd should mention what arrived: %v", err)
	}
}

func TestWrongTypeSaysWhatWasWantedAndWhatArrived(t *testing.T) {
	err := validate(t, "read", `{"path":123}`)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{`"path"`, "must be a string", "got number"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message missing %q: %q", want, err)
		}
	}
	// No Go internals. This is the exact failure mode being replaced.
	for _, leak := range []string{"json:", "Go struct", "readArgs"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("message leaks implementation detail %q: %q", leak, err)
		}
	}

	if err := validate(t, "read", `{"path":"a.go","limit":"20"}`); err == nil {
		t.Error("a numeric string for a number field should be reported")
	} else if !strings.Contains(err.Error(), "must be a number, got string") {
		t.Errorf("got %q", err)
	}
}

// Extra keys are how models routinely behave and are harmless. Rejecting them
// would spend a turn to gain nothing.
func TestUnknownFieldsAreTolerated(t *testing.T) {
	if err := validate(t, "read", `{"path":"a.go","thoughts":"looks fine","depth":3}`); err != nil {
		t.Errorf("unknown fields should be tolerated: %v", err)
	}
}

// A tool with no required fields is legitimately called with nothing at all.
func TestNoArgumentsIsFineWhenNothingIsRequired(t *testing.T) {
	for _, args := range []string{"", "{}", "   "} {
		if err := validate(t, "ls", args); err != nil {
			t.Errorf("ls with %q: %v", args, err)
		}
	}
	if err := validate(t, "read", ""); err == nil {
		t.Error("read still needs a path when called with nothing")
	}
}

func TestNonObjectArgumentsAreRejectedPlainly(t *testing.T) {
	err := validate(t, "read", `["a.go"]`)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "must be a JSON object") || !strings.Contains(err.Error(), "got array") {
		t.Errorf("got %q", err)
	}
}

// edit's nested items schema must be checked too, and the failing element
// identified by index — "one of the edits is wrong" is not actionable.
func TestNestedArrayElementsAreValidatedByIndex(t *testing.T) {
	// A plain object schema with an items array, standing in for any future tool
	// that nests without edit's opt-out.
	schema := object([]string{"path", "edits"}, map[string]any{
		"path": prop("string", "path"),
		"edits": map[string]any{
			"type": "array",
			"items": object([]string{"oldText", "newText"}, map[string]any{
				"oldText": prop("string", "old"),
				"newText": prop("string", "new"),
			}),
		},
	})
	args := json.RawMessage(`{"path":"a.go","edits":[{"oldText":"a","newText":"b"},{"oldText":"c"}]}`)
	err := validateAgainst(schema, args, "")
	if err == nil {
		t.Fatal("expected an error about the second edit")
	}
	if !strings.Contains(err.Error(), "edits[1].newText") {
		t.Errorf("the failing element is not identified: %q", err)
	}
}

// edit opts out because its schema does not describe the flat form. The opt-out
// must keep that leniency working while still catching real mistakes.
func TestEditKeepsItsFlatFormButStillValidates(t *testing.T) {
	edit := toolNamed(t, "edit")

	flat := json.RawMessage(`{"path":"a.go","oldText":"x","newText":"y"}`)
	if err := ValidateArgs(edit, flat); err != nil {
		t.Errorf("the flat single-edit form must stay accepted: %v", err)
	}
	nested := json.RawMessage(`{"path":"a.go","edits":[{"oldText":"x","newText":"y"}]}`)
	if err := ValidateArgs(edit, nested); err != nil {
		t.Errorf("the documented form must be accepted: %v", err)
	}

	for _, c := range []struct{ args, want string }{
		{`{"edits":[{"oldText":"x","newText":"y"}]}`, `missing required field "path"`},
		{`{"path":"a.go"}`, `missing required field "edits"`},
		{`{"path":"a.go","oldText":"x"}`, `missing required field "edits"`},
		{`{"path":"a.go","edits":[{"oldText":"","newText":"y"}]}`, `edits[0].oldText must not be empty`},
	} {
		err := ValidateArgs(edit, json.RawMessage(c.args))
		if err == nil {
			t.Errorf("%s: expected an error", c.args)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %q, want %q", c.args, err, c.want)
		}
	}
}

// Every built-in must either validate against its own schema or say why not. A
// tool whose valid arguments its own schema rejects is a trap for the model.
func TestEveryToolAcceptsArgumentsMatchingItsSchema(t *testing.T) {
	valid := map[string]string{
		"read":  `{"path":"a.go"}`,
		"ls":    `{"path":"."}`,
		"find":  `{"pattern":"*.go"}`,
		"grep":  `{"pattern":"x","include":"*.go"}`,
		"write": `{"path":"a.go","content":"x"}`,
		"edit":  `{"path":"a.go","edits":[{"oldText":"a","newText":"b"}]}`,
		"bash":  `{"command":"ls","timeout":10}`,
		// Validation checks the ID's argument shape without reading storage.
		ToolResultReadName: `{"result_id":"R` + strings.Repeat("a", 64) + `"}`,
	}
	for _, tool := range Default(t.TempDir()).All() {
		args, ok := valid[tool.Name()]
		if !ok {
			t.Errorf("%s has no sample arguments here; add one when adding a tool", tool.Name())
			continue
		}
		if err := ValidateArgs(tool, json.RawMessage(args)); err != nil {
			t.Errorf("%s rejects arguments that match its schema: %v", tool.Name(), err)
		}
	}
}

func TestJSONKindNamesTypesTheSchemaWay(t *testing.T) {
	cases := map[string]string{
		`"x"`: "string", `12`: "number", `-1.5`: "number", `true`: "boolean",
		`false`: "boolean", `null`: "null", `{}`: "object", `[]`: "array", ``: "null",
	}
	for raw, want := range cases {
		if got := jsonKind(json.RawMessage(raw)); got != want {
			t.Errorf("jsonKind(%q) = %q, want %q", raw, got, want)
		}
	}
}

// A closed set of values is the case where the error can just say what the
// alternatives are. The subagent tool is the first user: a model that sends
// "readonly" instead of "explore" should learn both names from one message.
func TestValidateArgsChecksEnums(t *testing.T) {
	sub := &Subagent{}

	err := ValidateArgs(sub, json.RawMessage(`{"task":"x","mode":"readonly"}`))
	if err == nil {
		t.Fatal("ValidateArgs accepted a mode outside the enum")
	}
	for _, want := range []string{`"explore"`, `"edit"`, `"readonly"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %s", err, want)
		}
	}

	for _, mode := range []string{ModeExplore, ModeEdit} {
		args := json.RawMessage(`{"task":"x","mode":"` + mode + `"}`)
		if err := ValidateArgs(sub, args); err != nil {
			t.Errorf("ValidateArgs rejected the valid mode %q: %v", mode, err)
		}
	}

	// An absent enum field is the required check's business, not this one's, and the
	// message should be about what is missing rather than about a value it never got.
	err = ValidateArgs(sub, json.RawMessage(`{"task":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "missing required field") {
		t.Errorf("error for an absent mode = %v, want a missing-field message", err)
	}

	// A field with no enum in its schema is unaffected: this must not become a
	// check that every string has to opt out of.
	if err := ValidateArgs(&Read{Cwd: "."}, json.RawMessage(`{"path":"anything.txt"}`)); err != nil {
		t.Errorf("ValidateArgs rejected a plain string field: %v", err)
	}
}

// read's paths[] is an array of scalars, and it is the shipped shape the
// element recursion used to kill: every call was refused before the gate with
// "arguments must be a JSON object, got string" — a message about the whole
// arguments — and the model burned a turn re-guessing well-formed JSON. This
// is the regression test for that exact report, run through ValidateArgs
// (the pre-gate path), not Execute.
func TestReadPathsPassValidation(t *testing.T) {
	err := ValidateArgs(toolNamed(t, "read"), json.RawMessage(
		`{"paths":["/opt/skills/malware-analysis/SKILL.md","/opt/skills/web-drawing/SKILL.md"]}`))
	if err != nil {
		t.Fatalf("multi-file read must validate: %v", err)
	}
}

// Scalar elements are type- and enum-checked against items, and the failing
// element is named by index — the same standard the object recursion holds.
func TestScalarArrayElementsAreChecked(t *testing.T) {
	schema := object([]string{"paths"}, map[string]any{
		"paths": map[string]any{
			"type":  "array",
			"items": map[string]any{"type": "string"},
		},
	})
	err := validateAgainst(schema, json.RawMessage(`{"paths":["a.go",5,"b.go"]}`), "")
	if err == nil || !strings.Contains(err.Error(), `paths[1]`) || !strings.Contains(err.Error(), "must be a string, got number") {
		t.Errorf("the failing element is not identified: %v", err)
	}

	enumSchema := object([]string{"mode"}, map[string]any{
		"mode": map[string]any{
			"type":  "array",
			"items": map[string]any{"type": "string", "enum": []string{"fast", "slow"}},
		},
	})
	err = validateAgainst(enumSchema, json.RawMessage(`{"mode":["fast","quik"]}`), "")
	if err == nil || !strings.Contains(err.Error(), `mode[1]`) || !strings.Contains(err.Error(), `"fast", "slow"`) {
		t.Errorf("enum miss should name the element and the alternatives: %v", err)
	}

	// Well-formed scalars of every declared kind pass.
	if err := validateAgainst(schema, json.RawMessage(`{"paths":["a.go","b.go"]}`), ""); err != nil {
		t.Errorf("valid string elements rejected: %v", err)
	}
}

// The report behind numeric type matching, run through the whole MCP path: a
// server declares pagination parameters as integers, the model sends
// {"current":1,"size":1}, and the call died at validation — "field current
// must be a integer, got number" — because jsonKind names every JSON number
// "number" and the type check compared those names for equality. The tool
// never ran; here it must.
func TestMCPIntegerPaginationFieldsValidate(t *testing.T) {
	schema := object([]string{"current", "size"}, map[string]any{
		"current": map[string]any{"type": "integer", "description": "page number, 1-based"},
		"size":    map[string]any{"type": "integer", "description": "page size"},
	})
	tool, err := NewMCPTool("docs", &mcp.Tool{
		Name: "search", Description: "search the docs", InputSchema: schema,
	}, 0, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
		return nil, nil // never reached: validation runs before any call
	})
	if err != nil {
		t.Fatalf("NewMCPTool: %v", err)
	}
	if err := ValidateArgs(tool, json.RawMessage(`{"current":1,"size":1}`)); err != nil {
		t.Errorf("the exact reported call must validate: %v", err)
	}
}

// JSON has one number kind, so whole values arrive in every notation a model
// might use: fraction digits that cancel (1.0), exponents (1e3), both
// (1.20e1), and magnitudes float64 cannot hold exactly.
func TestIntegerFieldsAcceptWholeNumbers(t *testing.T) {
	schema := object(nil, map[string]any{
		"current": map[string]any{"type": "integer"},
		"size":    map[string]any{"type": "integer"},
	})
	for _, raw := range []string{
		`{"current":0,"size":1}`,
		`{"current":-2,"size":1}`,
		`{"current":1.0,"size":1}`,
		`{"current":1e3,"size":1}`,
		`{"current":1.20e1,"size":1}`,
		`{"current":9007199254740993,"size":1}`,
	} {
		if err := validateAgainst(schema, json.RawMessage(raw), ""); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

// Fractions stay refused whatever notation carries them — including the one
// float64 would round to 1 — and a numeric string or a bool is not an integer
// just because its value looks like one.
func TestIntegerFieldsRejectFractionsAndNonNumbers(t *testing.T) {
	schema := object(nil, map[string]any{
		"current": map[string]any{"type": "integer"},
	})
	for _, c := range []struct{ raw, want string }{
		{`{"current":1.1}`, "got number"},
		{`{"current":1e-3}`, "got number"},
		{`{"current":1.0000000000000001}`, "got number"}, // float64 calls this 1
		{`{"current":"1"}`, "got string"},
		{`{"current":true}`, "got boolean"},
	} {
		err := validateAgainst(schema, json.RawMessage(c.raw), "")
		if err == nil {
			t.Errorf("%s: expected rejection", c.raw)
			continue
		}
		if !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), `"current"`) {
			t.Errorf("%s: got %q, want it to name the field and %q", c.raw, err, c.want)
		}
	}
}

// The literal-level decision, including shapes the schema-level tests cannot
// reach: negative zero, zero at any exponent, all-zero exponents, and
// exponents too large for int64 — each answered in one bounded pass, the same
// pass as for 1, with no big-integer allocation behind them.
func TestIsIntegerLiteral(t *testing.T) {
	accept := []string{
		"0", "-0", "1", "-2", "1.0", "1e3", "1.20e1", "9007199254740993",
		// The exponent lands each of these on a whole number: 15, 1.
		"1.5e1", "1000e-3", "10.00e-1",
		// Zero exponents, including a mountain of zeros that is still 0.
		"1e0", "1e+0", "1e-0", "1e0000000000000000000000000000000",
		// Zero is whole at any exponent.
		"-0e-5", "0.000e-7",
		// Leading zeros in the exponent: it is 3, not huge.
		"1000e-000000000000000000003",
		// Exponents past int64's range: only sign and scale matter, and each
		// answer costs the same one pass as for 1.
		"1e1000000000",
		"1e999999999999999999999",
		"0e-999999999999999999999",
	}
	for _, raw := range accept {
		if !isIntegerLiteral(json.RawMessage(raw)) {
			t.Errorf("isIntegerLiteral(%q) = false, want true", raw)
		}
	}
	reject := []string{
		"01", "-01", "1.", ".5", "1e", "1e-", "+1",
		"1.1", "1e-3", "1.0000000000000001",
		"1e-1000000000",
		"1e-999999999999999999999",
		"0.5e-999999999999999999999",
	}
	for _, raw := range reject {
		if isIntegerLiteral(json.RawMessage(raw)) {
			t.Errorf("isIntegerLiteral(%q) = true, want false", raw)
		}
	}
}

// Scalar array items share the property rule, so an integer items type checks
// each literal the same way, naming the failing element by index.
func TestIntegerArrayElementsAreChecked(t *testing.T) {
	schema := object(nil, map[string]any{
		"pages": map[string]any{
			"type":  "array",
			"items": map[string]any{"type": "integer"},
		},
	})
	if err := validateAgainst(schema, json.RawMessage(`{"pages":[0,-2,1.0,1e3,9007199254740993]}`), ""); err != nil {
		t.Errorf("whole-number elements must pass: %v", err)
	}
	err := validateAgainst(schema, json.RawMessage(`{"pages":[1,1.5,2]}`), "")
	if err == nil || !strings.Contains(err.Error(), `pages[1]`) || !strings.Contains(err.Error(), "got number") {
		t.Errorf("the fraction element should be named with its kind: %v", err)
	}
	err = validateAgainst(schema, json.RawMessage(`{"pages":[1,"2"]}`), "")
	if err == nil || !strings.Contains(err.Error(), `pages[1]`) || !strings.Contains(err.Error(), "got string") {
		t.Errorf("a numeric string element should be named: %v", err)
	}
}

// Object array items recurse through validateAgainst, so an integer property
// two levels down gets the same treatment a top-level one does.
func TestNestedObjectArrayIntegerFields(t *testing.T) {
	schema := object([]string{"pages"}, map[string]any{
		"pages": map[string]any{
			"type": "array",
			"items": object([]string{"page"}, map[string]any{
				"page": map[string]any{"type": "integer"},
			}),
		},
	})
	if err := validateAgainst(schema, json.RawMessage(`{"pages":[{"page":1},{"page":2.0}]}`), ""); err != nil {
		t.Errorf("whole page numbers must pass, 2.0 included: %v", err)
	}
	err := validateAgainst(schema, json.RawMessage(`{"pages":[{"page":1},{"page":1.5}]}`), "")
	if err == nil || !strings.Contains(err.Error(), `pages[1].page`) {
		t.Errorf("the failing nested field should be named: %v", err)
	}
}

// number keeps accepting every legal JSON number — fractional ones included,
// so the integer strictness is the integer subset and nothing broader — and
// its mismatch message is unchanged.
func TestNumberFieldsStillAcceptEveryNumber(t *testing.T) {
	schema := object(nil, map[string]any{
		"limit": map[string]any{"type": "number"},
	})
	for _, raw := range []string{
		`{"limit":1}`, `{"limit":-0.5}`, `{"limit":1e-3}`, `{"limit":9007199254740993}`,
	} {
		if err := validateAgainst(schema, json.RawMessage(raw), ""); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	err := validateAgainst(schema, json.RawMessage(`{"limit":"20"}`), "")
	if err == nil || !strings.Contains(err.Error(), "must be a number, got string") {
		t.Errorf(`{"limit":"20"}: got %v, want the number-for-string message`, err)
	}
}
