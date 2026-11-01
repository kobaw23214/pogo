package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeCaller records the invocation and answers with a scripted result.
func fakeCaller(t *testing.T, fn func(ctx context.Context, toolName string, args json.RawMessage) (*mcp.CallToolResult, error)) MCPCaller {
	t.Helper()
	return fn
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func mcpSpec(name, desc string, an *mcp.ToolAnnotations) *mcp.Tool {
	return &mcp.Tool{
		Name:        name,
		Description: desc,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Annotations: an,
	}
}

func TestWireNameFor(t *testing.T) {
	if got := WireNameFor("srv", "echo"); got != "mcp_srv_echo" {
		t.Fatalf("WireNameFor = %q", got)
	}
	// Characters outside the wire alphabet ('.', '/', unicode) map to '_'.
	if got := WireNameFor("srv", "mal.search/deep·x"); got != "mcp_srv_mal_search_deep_x" {
		t.Fatalf("sanitize = %q", got)
	}
	// Long names truncate deterministically with a hash of the full name.
	long := strings.Repeat("t", 80)
	a, b := WireNameFor("srv", long), WireNameFor("srv", long)
	if a != b {
		t.Fatalf("not deterministic: %q vs %q", a, b)
	}
	if len(a) != MaxWireNameLength {
		t.Fatalf("truncated length = %d, want %d", len(a), MaxWireNameLength)
	}
	if !wireNameRE.MatchString(a) {
		t.Fatalf("truncated name %q fails the wire regex", a)
	}
	// Two tools sharing the 55-character prefix still get distinct names.
	other := WireNameFor("srv", long[:79]+"u")
	if a == other {
		t.Fatal("hash collision between names sharing the kept prefix")
	}
	if a[:55] != other[:55] {
		t.Fatal("the kept prefix should be shared; the hash tail is the discriminator")
	}
}

func TestNewMCPToolValidation(t *testing.T) {
	caller := fakeCaller(t, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
		return textResult("ok"), nil
	})

	// Nil schema becomes a complete empty object schema — a bare
	// {"type":"object"} with a null properties/required is the shape
	// moonshot/kimi reject (see object()).
	mt, err := NewMCPTool("srv", &mcp.Tool{Name: "noargs", Description: "d"}, time.Second, caller)
	if err != nil {
		t.Fatal(err)
	}
	schema := mt.InputSchema()
	if schema["type"] != "object" {
		t.Fatalf("schema type = %v", schema["type"])
	}
	if props, ok := schema["properties"].(map[string]any); !ok || props == nil {
		t.Fatalf("properties = %#v, want a non-nil object", schema["properties"])
	}
	if req, ok := schema["required"].([]string); !ok || req == nil {
		t.Fatalf("required = %#v, want a non-nil array", schema["required"])
	}

	// A schema that is not a JSON object is refused — the pool skips it.
	if _, err := NewMCPTool("srv", &mcp.Tool{Name: "x", Description: "d", InputSchema: "nope"}, time.Second, caller); err == nil {
		t.Fatal("non-object inputSchema must be an error")
	}

	// Invisible characters in the name or description are tool-poisoning
	// camouflage; refused, not stripped.
	if _, err := NewMCPTool("srv", mcpSpec("ev​il", "d", nil), time.Second, caller); err == nil {
		t.Fatal("zero-width space in the name must be an error")
	}
	if _, err := NewMCPTool("srv", mcpSpec("ok", "safe text\u202e with bidi override", nil), time.Second, caller); err == nil {
		t.Fatal("bidi override in the description must be an error")
	}
	if _, err := NewMCPTool("srv", mcpSpec("ok2", "zero\u200bwidth in the middle", nil), time.Second, caller); err == nil {
		t.Fatal("zero-width space in the description must be an error")
	}

	// Visible layout whitespace is typography in a remote server's prose, not
	// concealment: multi-line descriptions register, verbatim. The name's
	// check stays strict — a newline there is suspicious in itself.
	multi, err := NewMCPTool("srv", mcpSpec("prose", "第一行\n\t第二行（缩进）\r\n第三行", nil), time.Second, caller)
	if err != nil {
		t.Fatalf("multi-line description must register: %v", err)
	}
	if got := multi.Description(); got != "第一行\n\t第二行（缩进）\r\n第三行" {
		t.Fatalf("description altered: %q", got)
	}
	if _, err := NewMCPTool("srv", mcpSpec("na\nme", "d", nil), time.Second, caller); err == nil {
		t.Fatal("a newline in the name must still be an error")
	}
}

func TestNewMCPToolDescriptionTruncation(t *testing.T) {
	caller := fakeCaller(t, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
		return textResult("ok"), nil
	})
	long := strings.Repeat("长", MaxSynthDescriptionLength+50)
	mt, err := NewMCPTool("srv", mcpSpec("t", long, nil), time.Second, caller)
	if err != nil {
		t.Fatal(err)
	}
	desc := mt.Description()
	if !strings.HasSuffix(desc, "…[truncated]") {
		t.Fatalf("truncated description must carry the visible mark, tail = %q", desc[len(desc)-30:])
	}
	if n := len([]rune(desc)); n > MaxSynthDescriptionLength {
		t.Fatalf("description is %d runes, over the %d limit", n, MaxSynthDescriptionLength)
	}
}

func TestMCPToolModes(t *testing.T) {
	caller := fakeCaller(t, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
		return textResult("ok"), nil
	})
	ro, err := NewMCPTool("srv", mcpSpec("ro", "d", &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}), time.Second, caller)
	if err != nil {
		t.Fatal(err)
	}
	if ro.ExecutionMode() != Parallel || !ro.IsReadOnly() {
		t.Error("read-only idempotent should be Parallel and IsReadOnly")
	}
	rw, err := NewMCPTool("srv", mcpSpec("rw", "d", &mcp.ToolAnnotations{ReadOnlyHint: true}), time.Second, caller)
	if err != nil {
		t.Fatal(err)
	}
	if rw.ExecutionMode() != Sequential {
		t.Error("read-only but not idempotent must stay Sequential")
	}
	plain, err := NewMCPTool("srv", mcpSpec("p", "d", nil), time.Second, caller)
	if err != nil {
		t.Fatal(err)
	}
	if plain.ExecutionMode() != Sequential || plain.IsReadOnly() {
		t.Error("no annotations means Sequential and not read-only")
	}
}

func TestMCPToolExecuteContentMapping(t *testing.T) {
	res := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "first"},
			&mcp.ImageContent{MIMEType: "image/png", Data: make([]byte, 42)},
			&mcp.TextContent{Text: "second"},
		},
		StructuredContent: map[string]any{"answer": 7},
	}
	mt, err := NewMCPTool("srv", mcpSpec("t", "d", nil), time.Second,
		fakeCaller(t, func(_ context.Context, tool string, args json.RawMessage) (*mcp.CallToolResult, error) {
			if tool != "t" {
				t.Errorf("caller got tool %q, want the server-side name", tool)
			}
			return res, nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := mt.Execute(context.Background(), json.RawMessage(`{"q":1}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first", "second", "[image/png content omitted, 42 bytes]", "[structured content]", `"answer":7`} {
		if !strings.Contains(out.Text, want) {
			t.Errorf("result text missing %q:\n%s", want, out.Text)
		}
	}
	if strings.Index(out.Text, "first") > strings.Index(out.Text, "second") {
		t.Error("text blocks must join in order")
	}
	d, ok := out.Details.(MCPDetails)
	if !ok {
		t.Fatalf("Details = %T", out.Details)
	}
	if d.Server != "srv" || d.Tool != "t" || d.StructuredContent == nil {
		t.Fatalf("Details = %#v", d)
	}
}

func TestMCPToolExecuteIsError(t *testing.T) {
	mt, err := NewMCPTool("srv", mcpSpec("t", "d", nil), time.Second,
		fakeCaller(t, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "the server says no"}},
			}, nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := mt.Execute(context.Background(), json.RawMessage(`{}`))
	// error-is-result: the error marks is_error, the text still feeds the model.
	if err == nil || !strings.Contains(err.Error(), "the server says no") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.Text, "the server says no") {
		t.Fatalf("text = %q", out.Text)
	}
	if out.Details.(MCPDetails).Tool != "t" {
		t.Fatalf("details missing on the error path: %#v", out.Details)
	}
}

func TestMCPToolExecuteTimeoutAndAbort(t *testing.T) {
	block := fakeCaller(t, func(ctx context.Context, _ string, _ json.RawMessage) (*mcp.CallToolResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	mt, err := NewMCPTool("srv", mcpSpec("t", "d", nil), 50*time.Millisecond, block)
	if err != nil {
		t.Fatal(err)
	}
	out, err := mt.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if !out.Details.(MCPDetails).TimedOut {
		t.Fatalf("TimedOut not set: %#v", out.Details)
	}

	// Caller-side cancellation reads as aborted, not timed out.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = mt.Execute(ctx, json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("err = %v, want aborted", err)
	}

	// A transport-level error travels as-is.
	broken := errors.New("connection closed")
	mt2, err := NewMCPTool("srv", mcpSpec("t", "d", nil), time.Second,
		fakeCaller(t, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			return nil, broken
		}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mt2.Execute(context.Background(), json.RawMessage(`{}`)); !errors.Is(err, broken) {
		t.Fatalf("err = %v, want the transport error verbatim", err)
	}
}

func TestMCPToolExecuteSpillsLargeOutput(t *testing.T) {
	big := strings.Repeat("line of output\n", 5000) // past the 2000-line cap
	mt, err := NewMCPTool("srv", mcpSpec("t", "d", nil), time.Second,
		fakeCaller(t, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			return textResult(big), nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	mt.SpillDir = t.TempDir()
	out, err := mt.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Text) >= len(big) {
		t.Fatal("output was not truncated")
	}
	d := out.Details.(MCPDetails)
	if !d.Truncated || d.FullOutputPath == "" {
		t.Fatalf("Details = %#v, want truncated + path", d)
	}
	spilled, err := os.ReadFile(d.FullOutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(spilled) != big {
		t.Fatal("the spill file must hold the complete output")
	}
}

func TestMCPToolEmptyResult(t *testing.T) {
	mt, err := NewMCPTool("srv", mcpSpec("t", "d", nil), time.Second,
		fakeCaller(t, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := mt.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil || out.Text != "(no output)" {
		t.Fatalf("empty result = %q, %v", out.Text, err)
	}
}

func TestMCPToolDetailsMetadata(t *testing.T) {
	boolp := func(v bool) *bool { return &v }
	caller := fakeCaller(t, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
		return textResult("ok"), nil
	})

	spec := mcpSpec("t", "搜索站点内容\n支持多条件组合", &mcp.ToolAnnotations{
		ReadOnlyHint:    true,
		IdempotentHint:  true,
		OpenWorldHint:   boolp(true),
		DestructiveHint: boolp(false),
	})
	spec.Title = "站点搜索"
	mt, err := NewMCPTool("srv", spec, time.Second, caller)
	if err != nil {
		t.Fatal(err)
	}
	out, err := mt.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	d := out.Details.(MCPDetails)
	if d.Title != "站点搜索" || d.Description != "搜索站点内容\n支持多条件组合" {
		t.Fatalf("title/description = %q %q", d.Title, d.Description)
	}
	an := d.Annotations
	if an == nil || !an.ReadOnlyHint || !an.IdempotentHint || !an.OpenWorldHint || an.DestructiveHint {
		t.Fatalf("annotations = %#v", an)
	}
	if d.InputSchema["type"] != "object" {
		t.Fatalf("input_schema = %#v", d.InputSchema)
	}

	// Display-name precedence: the annotations' title is the fallback when the
	// tool declares none of its own.
	fallback := mcpSpec("t2", "d", &mcp.ToolAnnotations{Title: "注解名"})
	mt2, err := NewMCPTool("srv", fallback, time.Second, caller)
	if err != nil {
		t.Fatal(err)
	}
	out2, _ := mt2.Execute(context.Background(), json.RawMessage(`{}`))
	if got := out2.Details.(MCPDetails).Title; got != "注解名" {
		t.Fatalf("fallback title = %q", got)
	}

	// An un-annotated tool keeps the details lean: no title, no annotations.
	mt3, err := NewMCPTool("srv", mcpSpec("t3", "d", nil), time.Second, caller)
	if err != nil {
		t.Fatal(err)
	}
	out3, _ := mt3.Execute(context.Background(), json.RawMessage(`{}`))
	d3 := out3.Details.(MCPDetails)
	if d3.Title != "" || d3.Annotations != nil {
		t.Fatalf("un-annotated tool: title = %q, annotations = %#v", d3.Title, d3.Annotations)
	}
}

func TestMCPToolExecuteContentBlocks(t *testing.T) {
	size := int64(4096)
	res := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "first"},
			// The SDK carries image/audio payloads base64-encoded already.
			&mcp.ImageContent{MIMEType: "image/png", Data: []byte("aGk=")},
			&mcp.ImageContent{MIMEType: "image/png", Data: make([]byte, maxMCPInlineData+1)},
			&mcp.AudioContent{MIMEType: "audio/mpeg", Data: []byte("AAAA")},
			&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{
				URI: "file:///tmp/report.csv", MIMEType: "text/csv", Text: "a,b\n1,2\n",
			}},
			&mcp.ResourceLink{URI: "https://example.com/doc", Name: "doc", MIMEType: "text/html", Size: &size},
		},
	}
	mt, err := NewMCPTool("srv", mcpSpec("t", "d", nil), time.Second,
		fakeCaller(t, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			return res, nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := mt.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	d := out.Details.(MCPDetails)
	if len(d.Content) != 6 {
		t.Fatalf("content blocks = %d, want 6: %#v", len(d.Content), d.Content)
	}
	if d.Content[0].Type != "text" || d.Content[0].Text != "first" {
		t.Fatalf("text block = %#v", d.Content[0])
	}
	small := d.Content[1]
	if small.Type != "image" || small.Data != "aGk=" || small.Size != 4 || small.Omitted {
		t.Fatalf("small image = %#v", small)
	}
	big := d.Content[2]
	if big.Type != "image" || !big.Omitted || big.Data != "" || big.Size != maxMCPInlineData+1 {
		t.Fatalf("big image must be omitted metadata only = %#v", big)
	}
	if d.Content[3].Type != "audio" || d.Content[3].Data != "AAAA" {
		t.Fatalf("audio block = %#v", d.Content[3])
	}
	resrc := d.Content[4]
	if resrc.Type != "resource" || resrc.URI != "file:///tmp/report.csv" || resrc.Size != len("a,b\n1,2\n") {
		t.Fatalf("resource block = %#v", resrc)
	}
	link := d.Content[5]
	if link.Type != "resource_link" || link.URI != "https://example.com/doc" || link.Name != "doc" || link.Size != 4096 {
		t.Fatalf("resource_link block = %#v", link)
	}
	// The model-facing text is untouched by the structured copy: placeholders,
	// not payloads.
	if !strings.Contains(out.Text, "first") || strings.Contains(out.Text, "aGk=") {
		t.Fatalf("model text changed:\n%s", out.Text)
	}
}
