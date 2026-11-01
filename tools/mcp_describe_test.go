package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPDescribeRequiresExactWireName(t *testing.T) {
	first := describeSpec("notes", "lookup", "First service", nil, nil)
	second := describeSpec("archive", "lookup", "Second service", nil, nil)
	d := helperOf(t, New(Options{Cwd: t.TempDir(), MCP: []Tool{first, second}}))
	if _, err := d.Execute(context.Background(), json.RawMessage(`{"tool":"lookup"}`)); err == nil {
		t.Fatal("an unqualified server tool name must not choose the first matching service")
	}
	for _, tool := range []*MCPTool{first, second} {
		args, _ := json.Marshal(map[string]string{"tool": tool.Name()})
		result, err := d.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		if result.Details.(MCPDescribeDetails).Server != tool.ServerName() {
			t.Fatalf("wrong server for %s", tool.Name())
		}
	}
}

func TestMCPDescribeLargeIndexSpillsAndPaginationStaysAvailable(t *testing.T) {
	root := t.TempDir()
	var subjects []Tool
	for i := 0; i < 200; i++ {
		subjects = append(subjects, describeSpec("notes", fmt.Sprintf("lookup_%03d", i),
			strings.Repeat("metadata ", 180), nil, nil))
	}
	d := helperOf(t, New(Options{Cwd: root, MCP: subjects, SpillDir: filepath.Join(root, "spill")}))
	result, err := d.Execute(context.Background(), json.RawMessage(`{"limit":200}`))
	if err != nil {
		t.Fatal(err)
	}
	details := result.Details.(MCPDescribeDetails)
	if !details.Truncated || details.FullOutputPath == "" || details.Total != 200 {
		t.Fatalf("large index was not bounded with a complete spill: %#v", details)
	}
	full, err := os.ReadFile(details.FullOutputPath)
	if err != nil || !strings.Contains(string(full), "mcp_notes_lookup_199") {
		t.Fatalf("index spill lost the final entry: %v", err)
	}
	page, err := d.Execute(context.Background(), json.RawMessage(`{"filter":"lookup_19","offset":2,"limit":1}`))
	if err != nil || !strings.Contains(page.Text, "mcp_notes_lookup_192") || strings.Contains(page.Text, "mcp_notes_lookup_193") {
		t.Fatalf("filtered page is incorrect: %v, %s", err, page.Text)
	}
	if !strings.Contains(page.Text, "untrusted metadata") {
		t.Fatal("index omitted its external-data notice")
	}
}

// The fixtures are two neutral, unrelated synthetic services: a note pad and
// a calculator. No real server, product, or business domain is referenced.

func describeSpec(server, name, desc string, outputSchema map[string]any, an *mcp.ToolAnnotations) *MCPTool {
	spec := &mcp.Tool{
		Name:        name,
		Description: desc,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"query": map[string]any{"type": "string"},
		}},
		OutputSchema: outputSchema,
		Annotations:  an,
	}
	if outputSchema == nil {
		// A typed-nil map in an any field reads as "declared but null" to the
		// wrapper; leave the field genuinely absent instead.
		spec.OutputSchema = nil
	}
	tool, err := NewMCPTool(server, spec, 0,
		func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	if err != nil {
		panic(err)
	}
	return tool
}

func noteTool() *MCPTool {
	return describeSpec("notepad", "find_note", "Finds a note by title in the pad.",
		map[string]any{"type": "object", "properties": map[string]any{
			"note": map[string]any{"type": "string"},
		}}, &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true})
}

func calcTool() *MCPTool {
	return describeSpec("calc", "compute", "Evaluates an arithmetic expression.", nil, nil)
}

func helperOf(t *testing.T, r *Registry) *MCPDescribe {
	t.Helper()
	tool, ok := r.Get(MCPDescribeName)
	if !ok {
		t.Fatal("mcp_describe not installed")
	}
	d, ok := tool.(*MCPDescribe)
	if !ok {
		t.Fatalf("%s is %T, not the auto helper", MCPDescribeName, tool)
	}
	return d
}

func TestMCPDescribeIndexAndDetail(t *testing.T) {
	root := t.TempDir()
	reg := New(Options{Cwd: root, MCP: []Tool{noteTool(), calcTool()}, SpillDir: filepath.Join(root, "spill")})
	d := helperOf(t, reg)

	// Index: both unrelated services listed with provenance and schema hints.
	res, err := d.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mcp_notepad_find_note", "mcp_calc_compute", "notepad", "calc",
		"Finds a note by title", "input+output schemas"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("index missing %q:\n%s", want, res.Text)
		}
	}
	calcLine := res.Text[strings.Index(res.Text, "mcp_calc_compute"):]
	if strings.Contains(strings.SplitN(calcLine, "\n", 2)[0], "input+output") {
		t.Fatalf("output schema hint on a tool without one:\n%s", res.Text)
	}
	details, ok := res.Details.(MCPDescribeDetails)
	if !ok || details.Total != 2 || details.Shown != 2 {
		t.Fatalf("details = %#v", res.Details)
	}

	// Detail: full contract with output schema, fingerprint and provenance.
	res, err = d.Execute(context.Background(), json.RawMessage(`{"tool":"mcp_notepad_find_note"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"server": "notepad"`, `"tool": "find_note"`,
		`"input_schema"`, `"output_schema"`, `"note"`, `"definition_fingerprint"`,
		`"readOnlyHint": true`, "untrusted"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("detail missing %q:\n%s", want, res.Text)
		}
	}
	details, ok = res.Details.(MCPDescribeDetails)
	if !ok || details.Server != "notepad" || details.Tool != "find_note" || details.Fingerprint == "" {
		t.Fatalf("details = %#v", res.Details)
	}

	// Detail on the schema-less tool says null, never omits the key.
	res, err = d.Execute(context.Background(), json.RawMessage(`{"tool":"mcp_calc_compute"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, `"output_schema": null`) {
		t.Fatalf("output schema silently omitted:\n%s", res.Text)
	}

	// Unknown name is a normal error naming the recovery path.
	if _, err = d.Execute(context.Background(), json.RawMessage(`{"tool":"mcp_nope"}`)); err == nil ||
		!strings.Contains(err.Error(), "no arguments") {
		t.Fatalf("unknown tool error = %v", err)
	}

	// Filter narrows the index to one service.
	res, err = d.Execute(context.Background(), json.RawMessage(`{"filter":"arithmetic"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, "notepad") || !strings.Contains(res.Text, "mcp_calc_compute") {
		t.Fatalf("filter did not narrow:\n%s", res.Text)
	}
}

func TestMCPDescribeKeepsFullDescriptionAndSnapshotImmutable(t *testing.T) {
	long := strings.Repeat("Detailed contract paragraph. ", 100) // ~2900 runes > MaxSynthDescriptionLength
	spec := &mcp.Tool{
		Name:        "big",
		Description: long,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "original"},
		}},
	}
	tool, err := NewMCPTool("docs", spec, 0,
		func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(tool.Description(), truncatedMark) {
		t.Fatal("model-facing description should be truncated")
	}
	d := newMCPDescribe([]*MCPTool{tool}, "")

	// Mutating the original declaration maps must not rewrite the snapshot.
	spec.InputSchema.(map[string]any)["properties"].(map[string]any)["query"].(map[string]any)["description"] = "rewritten"
	spec.Description = "rewritten entirely"

	res, err := d.Execute(context.Background(), json.RawMessage(`{"tool":"mcp_docs_big"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, long[:200]) {
		t.Fatal("detail lost the full untruncated description")
	}
	if strings.Contains(res.Text, "rewritten") {
		t.Fatal("snapshot mutated through the original declaration")
	}
}

func TestMCPDescribeRefreshLifecycle(t *testing.T) {
	root := t.TempDir()
	first := noteTool()
	reg := New(Options{Cwd: root, MCP: []Tool{first}, SpillDir: filepath.Join(root, "spill")})

	// Add on refresh: the new helper sees both; the old helper still
	// describes the old snapshot.
	second := calcTool()
	grown := reg.WithMCP([]Tool{first, second})
	res, err := helperOf(t, grown).Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "mcp_calc_compute") {
		t.Fatalf("added tool missing from refreshed helper:\n%s", res.Text)
	}
	oldRes, err := helperOf(t, reg).Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(oldRes.Text, "mcp_calc_compute") {
		t.Fatal("old run's helper adopted the new snapshot")
	}

	// Change on refresh: same name, new description -> the new helper shows
	// the new contract, the old one keeps the old one.
	changed := describeSpec("notepad", "find_note", "Finds a note by full-text search.", nil,
		&mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true})
	swapped := reg.WithMCP([]Tool{changed})
	res, err = helperOf(t, swapped).Execute(context.Background(), json.RawMessage(`{"tool":"mcp_notepad_find_note"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "full-text search") || strings.Contains(res.Text, "by title in the pad") {
		t.Fatalf("changed definition not reflected:\n%s", res.Text)
	}
	oldRes, err = helperOf(t, reg).Execute(context.Background(), json.RawMessage(`{"tool":"mcp_notepad_find_note"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(oldRes.Text, "by title in the pad") {
		t.Fatal("old run's helper lost its snapshot")
	}

	// Remove all: the helper disappears with the catalog.
	empty := grown.WithMCP(nil)
	if _, ok := empty.Get(MCPDescribeName); ok {
		t.Fatal("helper survived an empty MCP catalog")
	}
}

func TestMCPDescribeAbsentWithoutMCP(t *testing.T) {
	reg := New(Options{Cwd: t.TempDir()})
	if _, ok := reg.Get(MCPDescribeName); ok {
		t.Fatal("helper installed without any MCP tools")
	}
}

// nameStub stands in for a session tool that happens to use the helper's
// name; the registry must keep it and skip the auto helper.
type nameStub struct{ name string }

func (s nameStub) Name() string                 { return s.name }
func (s nameStub) Description() string          { return "session-provided stub" }
func (s nameStub) InputSchema() map[string]any  { return object(nil, map[string]any{}) }
func (s nameStub) ExecutionMode() ExecutionMode { return Sequential }
func (s nameStub) Execute(context.Context, json.RawMessage) (Result, error) {
	return Result{Text: "stub"}, nil
}

func TestMCPDescribeNameConflictYields(t *testing.T) {
	root := t.TempDir()
	reg := New(Options{Cwd: root, MCP: []Tool{noteTool()}, Synth: []Tool{nameStub{MCPDescribeName}}})
	tool, ok := reg.Get(MCPDescribeName)
	if !ok {
		t.Fatal("session tool displaced")
	}
	if _, isHelper := tool.(*MCPDescribe); isHelper {
		t.Fatal("auto helper shadowed the session's own tool")
	}
	// Same precedence across a refresh.
	next := reg.WithMCP([]Tool{calcTool()})
	tool, _ = next.Get(MCPDescribeName)
	if _, isHelper := tool.(*MCPDescribe); isHelper {
		t.Fatal("refresh installed the helper over the session's tool")
	}
	if _, ok := next.Get(calcTool().Name()); !ok {
		t.Fatal("refresh lost the MCP tool while resolving the conflict")
	}
}

func TestMCPDescribeReadOnlySession(t *testing.T) {
	root := t.TempDir()
	reg := New(Options{Cwd: root, ReadOnly: true, MCP: []Tool{noteTool(), calcTool()}})
	d := helperOf(t, reg)
	res, err := d.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "mcp_notepad_find_note") || strings.Contains(res.Text, "mcp_calc_compute") {
		t.Fatalf("helper does not match the read-only filtered catalog:\n%s", res.Text)
	}
}

func TestMCPDescribeOversizedMetadataSpills(t *testing.T) {
	root := t.TempDir()
	spill := filepath.Join(root, "spill")
	huge := map[string]any{"type": "object", "properties": map[string]any{
		"blob": map[string]any{"type": "string", "description": strings.Repeat("x", MaxBytes*2)},
	}}
	tool := describeSpec("notepad", "find_note", "Finds a note.", huge,
		&mcp.ToolAnnotations{ReadOnlyHint: true})
	reg := New(Options{Cwd: root, MCP: []Tool{tool}, SpillDir: spill})
	res, err := helperOf(t, reg).Execute(context.Background(), json.RawMessage(`{"tool":"mcp_notepad_find_note"}`))
	if err != nil {
		t.Fatal(err)
	}
	details, ok := res.Details.(MCPDescribeDetails)
	if !ok || !details.Truncated || details.FullOutputPath == "" {
		t.Fatalf("details = %#v, want truncation with a spill path", res.Details)
	}
	data, err := os.ReadFile(details.FullOutputPath)
	if err != nil {
		t.Fatal(err)
	}
	// The spilled file holds the complete metadata: the oversized output
	// schema was not dropped, only paged out.
	if !strings.Contains(string(data), strings.Repeat("x", MaxBytes)) {
		t.Fatal("spill file lost the oversized output schema")
	}
}

func TestMCPDescribeIsReadOnlyAndParallel(t *testing.T) {
	d := newMCPDescribe([]*MCPTool{noteTool()}, "")
	if !d.IsReadOnly() || d.ExecutionMode() != Parallel {
		t.Fatal("helper must be read-only and parallel")
	}
}
