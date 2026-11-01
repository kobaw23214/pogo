package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPDescribeRejectsLostSelectors(t *testing.T) {
	d := helperOf(t, NewRegistry().WithMCP([]Tool{describeSpec("notes", "lookup", "look up notes", nil, nil)}))
	for _, tc := range []struct{ raw, hint string }{
		{`{"tool_name":"lookup"}`, `use "tool"`},
		{`{"tool_name":"mcp_describe"}`, `use "tool"`},
		{`{"tool":"lookup","tool_name":"other"}`, `use "tool"`},
		{`{"Tool":"lookup"}`, "unknown parameter"},
		{`{"tool":"lookup","tool":"other"}`, "duplicate parameter"},
		{`{"tool":null}`, "cannot be null"},
		{`{"tool":" "}`, "non-empty"},
		{`{"limit":"10"}`, "incorrect parameter type"},
		{`{"tool":"lookup","filter":"other"}`, "not tool detail"},
		{`{"mode":"detail"}`, "mode must be"},
		{`{"mode":"list"}`, "mode must be"},
		{`{"mode":"Index"}`, "mode must be"},
		{`{"tool":"lookup","mode":"index"}`, "not tool detail"},
		{`{"mode":"groups","server":"notes"}`, "omit other parameters"},
		{`{} {}`, "exactly one"},
		{`null`, "JSON object"},
		{`[]`, "JSON object"},
		{`{"tool":`, "invalid value"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			for _, execute := range []bool{false, true} {
				var err error
				if execute {
					var result Result
					result, err = d.Execute(context.Background(), json.RawMessage(tc.raw))
					if result.Text != "" || result.Details != nil {
						t.Fatal("invalid arguments produced a successful index/detail")
					}
				} else {
					err = ValidateArgs(d, json.RawMessage(tc.raw))
				}
				if err == nil || !strings.Contains(err.Error(), tc.hint) {
					t.Fatalf("execute=%v err=%v, want %s", execute, err, tc.hint)
				}
			}
		})
	}
	for _, raw := range []string{"", `{}`, `{"filter":"notes","limit":1}`, `{"mode":"groups"}`, `{"mode":"index"}`, `{"filter":"notes","mode":"index"}`, `{"tool":"lookup","server":"notes"}`} {
		if err := ValidateArgs(d, json.RawMessage(raw)); err != nil {
			t.Fatalf("valid %s: %v", raw, err)
		}
		result, err := d.Execute(context.Background(), json.RawMessage(raw))
		if err != nil || result.Text == "" {
			t.Fatalf("valid %s: %v", raw, err)
		}
	}
	if d.InputSchema()["additionalProperties"] != false {
		t.Fatal("schema must prohibit ignored selectors")
	}
}
