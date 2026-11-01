package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRenderMCPContentDeduplicatesEquivalentJSONText(t *testing.T) {
	text := "\t{\n  \"ordered\": [1, 2.0, true],\n  \"name\": \"pogo\"\n}\n"
	res := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
		StructuredContent: map[string]any{
			"name":    "pogo",
			"ordered": []any{int64(1), float64(2), true},
		},
	}
	details := MCPDetails{}
	if got := renderMCPContent(res, &details); got != text {
		t.Fatalf("text = %q, want the equivalent text block alone", got)
	}
}

func TestRenderMCPContentPreservesDifferentJSONText(t *testing.T) {
	res := &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: `{"answer":7}`}},
		StructuredContent: map[string]any{"answer": 8},
	}
	details := MCPDetails{}
	got := renderMCPContent(res, &details)
	for _, want := range []string{`{"answer":7}`, "[structured content]", `"answer":8`} {
		if !strings.Contains(got, want) {
			t.Errorf("text missing %q:\n%s", want, got)
		}
	}
}

func TestRenderMCPContentNumberComparisonIsExact(t *testing.T) {
	const adjacent = int64(9007199254740992)
	exact := &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: `{"id":9007199254740992}`}},
		StructuredContent: map[string]any{"id": adjacent},
	}
	if got := renderMCPContent(exact, &MCPDetails{}); strings.Contains(got, "[structured content]") {
		t.Fatalf("exact large integer was duplicated:\n%s", got)
	}

	adjacentResult := &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: `{"id":9007199254740992}`}},
		StructuredContent: map[string]any{"id": adjacent + 1},
	}
	got := renderMCPContent(adjacentResult, &MCPDetails{})
	if !strings.Contains(got, "[structured content]") || !strings.Contains(got, `"id":9007199254740993`) {
		t.Fatalf("adjacent large integers were conflated:\n%s", got)
	}
}

func TestMCPJSONDedupConservativeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, text, structured string
		equal                  bool
	}{
		{"decimal exponent", `{"v":1.20e3}`, `{"v":1200}`, true},
		{"negative zero", `{"v":-0.0}`, `{"v":0}`, true},
		{"paired surrogate", `{"v":"\ud83d\ude00"}`, `{"v":"😀"}`, true},
		{"unpaired surrogate", `{"v":"\ud800"}`, `{"v":"�"}`, false},
		{"invalid utf8", "{\"v\":\"\xff\"}", `{"v":"�"}`, false},
		{"nested duplicate", `{"a":[{"x":1,"x":2}]}`, `{"a":[{"x":2}]}`, false},
		{"escaped duplicate key", `{"x":1,"\u0078":2}`, `{"x":2}`, false},
		{"deep nesting", strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300), strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := containsIdenticalJSONValue([]string{tc.text}, []byte(tc.structured)); got != tc.equal {
				t.Fatalf("equivalence = %v, want %v", got, tc.equal)
			}
		})
	}
}

func TestRenderMCPContentArrayOrderAndMalformedText(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		structured any
		dedup      bool
	}{
		{name: "ordered array", text: `{"items":[1,2]}`, structured: map[string]any{"items": []any{int64(1), int64(2)}}, dedup: true},
		{name: "reordered array", text: `{"items":[1,2]}`, structured: map[string]any{"items": []any{int64(2), int64(1)}}},
		{name: "malformed", text: `{"items":[1,2`, structured: map[string]any{"items": []any{int64(1), int64(2)}}},
		{name: "duplicate keys", text: `{"items":[1,2],"items":[1,2]}`, structured: map[string]any{"items": []any{int64(1), int64(2)}}},
		{name: "trailing object", text: `{"items":[1,2]} {}`, structured: map[string]any{"items": []any{int64(1), int64(2)}}},
		{name: "trailing prose", text: `{"items":[1,2]} done`, structured: map[string]any{"items": []any{int64(1), int64(2)}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			res := &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: test.text}},
				StructuredContent: test.structured,
			}
			got := renderMCPContent(res, &MCPDetails{})
			if strings.Contains(got, "[structured content]") == test.dedup {
				t.Fatalf("dedup=%v, rendered text:\n%s", test.dedup, got)
			}
		})
	}
}

func TestRenderMCPContentDeduplicateAmongMultimodalBlocks(t *testing.T) {
	payload := `{"answer":42}`
	res := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "before"},
			&mcp.ImageContent{MIMEType: "image/png", Data: []byte("png")},
			&mcp.TextContent{Text: payload},
			&mcp.AudioContent{MIMEType: "audio/wav", Data: []byte("wav")},
			&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///report", MIMEType: "application/pdf"}},
			&mcp.TextContent{Text: "after"},
		},
		StructuredContent: map[string]any{"answer": 42},
	}
	details := MCPDetails{}
	got := renderMCPContent(res, &details)
	for _, want := range []string{
		"before",
		"[image/png content omitted, 3 bytes]",
		payload,
		"[audio/wav content omitted, 3 bytes]",
		`[embedded resource: uri="file:///report", mime_type="application/pdf", 0 bytes]`,
		"after",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("text missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[structured content]") {
		t.Errorf("matching block among multiple text blocks was duplicated:\n%s", got)
	}

	wantContent := []MCPContentBlock{
		{Type: "text", Text: "before"},
		{Type: "image", MIMEType: "image/png", Size: 3, Data: "png"},
		{Type: "text", Text: payload},
		{Type: "audio", MIMEType: "audio/wav", Size: 3, Data: "wav"},
		{Type: "resource", URI: "file:///report", MIMEType: "application/pdf"},
		{Type: "text", Text: "after"},
	}
	if len(details.Content) != len(wantContent) {
		t.Fatalf("details content length = %d, want %d: %#v", len(details.Content), len(wantContent), details.Content)
	}
	for i, want := range wantContent {
		if details.Content[i] != want {
			t.Errorf("details content[%d] = %#v, want %#v", i, details.Content[i], want)
		}
	}
	if details.StructuredContent == nil {
		t.Fatal("raw details.StructuredContent was not preserved")
	}
	encoded, err := json.Marshal(details.StructuredContent)
	if err != nil || string(encoded) != `{"answer":42}` {
		t.Fatalf("raw structured details = %#v, encode = %q, %v", details.StructuredContent, encoded, err)
	}
}

func TestRenderMCPContentRejectsHugeExponentWithoutExpansion(t *testing.T) {
	hostile := json.Number("1e1000000")
	res := &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: `{"number":1e1000000}`}},
		StructuredContent: map[string]any{"number": hostile},
	}
	got := renderMCPContent(res, &MCPDetails{})
	if !strings.Contains(got, "[structured content]") || !strings.Contains(got, "1e1000000") {
		t.Fatalf("uncertain hostile exponent must preserve both copies:\n%s", got)
	}
}
