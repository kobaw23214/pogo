package jsonfix

import (
	"encoding/json"
	"strings"
	"testing"
)

// Valid input is the common case and must cost one json.Valid, never a walk.
func TestRepairPassesValidJSONThrough(t *testing.T) {
	in := `{"follow_ups": []}`
	out, repaired := Repair(in)
	if repaired || out != in {
		t.Errorf("Repair(%q) = %q, %v; want the valid input back untouched", in, out, repaired)
	}
}

// The breakage this package exists for: the ways a model answering in JSON
// actually breaks it.
func TestRepairFixesTheUsualBreakage(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"missing closing bracket", `{"follow_ups": [{"title": "t"`},
		{"single quotes", `{'title': 't'}`},
		{"missing comma", `{"a": "1" "b": "2"}`},
	} {
		out, repaired := Repair(tc.in)
		if !repaired {
			t.Errorf("%s: Repair(%q) claimed no repair", tc.name, tc.in)
			continue
		}
		if !json.Valid([]byte(out)) {
			t.Errorf("%s: the repaired output is not valid JSON: %q", tc.name, out)
		}
	}
}

// Empty is the one input with no content to stand behind. (Bare prose is not
// in this list: the repairer quotes it into a JSON string, which callers gate
// on finding an object first — see the followups parse, where prose never
// reaches the decoder with or without repair.)
func TestRepairRefusesHopelessInput(t *testing.T) {
	out, repaired := Repair("")
	if repaired {
		t.Errorf("Repair(\"\") = %q, true; want no repair claimed", out)
	}
	if out != "" {
		t.Errorf("Repair(\"\") = %q; want the input returned unchanged", out)
	}
}

// The budget is a refusal, not a best-effort: an oversized answer fails the
// same way it would have without this package.
func TestRepairRefusesOversizedInput(t *testing.T) {
	in := `{"a": "` + strings.Repeat("x", Budget+1)
	out, repaired := Repair(in)
	if repaired {
		t.Error("an over-budget input was repaired")
	}
	if out != in {
		t.Error("an over-budget input was not returned unchanged")
	}
}
