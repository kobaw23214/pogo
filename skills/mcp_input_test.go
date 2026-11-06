package skills

import (
	"strings"
	"testing"
)

func TestMCPInputReferencesValidateShapeWithoutClientInstructions(t *testing.T) {
	valid := StarterInputPart{Type: "mcp", Server: "remote", Name: "search/list:v2"}
	if err := ValidateInputParts([]StarterInputPart{valid}, "inline", "workspace"); err != "" {
		t.Fatal(err)
	}
	for _, change := range []StarterInputPart{
		{Type: "mcp", Server: "remote", Name: "search", Text: "CLIENT_INSTRUCTIONS"},
		{Type: "mcp", Server: "remote", Name: "search", Path: "uploads/private.txt"},
		{Type: "mcp", Server: "remote", Name: "search", AsFile: true},
		{Type: "mcp", Server: "remote", Name: "search", Title: "forged title"},
		{Type: "text", Server: "remote", Text: "not a selector"},
		{Type: "skill", Server: "remote", Name: "office"},
	} {
		if ValidateInputParts([]StarterInputPart{change}, "inline", "workspace") == "" {
			t.Fatalf("accepted forged MCP metadata: %+v", change)
		}
	}
	for _, name := range []string{"echo", "查询/文档:v2", "a+b%2F", "search!~*()", strings.Repeat("x", 512)} {
		if !ValidInputMCPReference("remote-2", name) {
			t.Errorf("valid name rejected: %q", name)
		}
	}
	for _, name := range []string{"", "has space", "line\nbreak", "has\u200bhidden", "has\ufeffhidden", "has\"quote", "<tag>", "a\\b", strings.Repeat("x", 513)} {
		if ValidInputMCPReference("remote", name) {
			t.Errorf("invalid name accepted: %q", name)
		}
	}
	if got := MCPInputText("remote", "a+b%/文档:!~*()"); got != "/mcp:remote/a%2Bb%25%2F%E6%96%87%E6%A1%A3%3A!~*()" {
		t.Fatalf("canonical selector=%q", got)
	}
}
