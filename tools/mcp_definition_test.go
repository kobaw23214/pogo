package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func definitionCaller() MCPCaller {
	return func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	}
}

func definitionFixture() *mcp.Tool {
	destructive, openWorld := true, false
	return &mcp.Tool{
		Meta:        mcp.Meta{"vendor": map[string]any{"kind": "example"}},
		Name:        "search",
		Title:       "Search",
		Description: "full server description",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
			"required": []any{"query"},
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"answer": map[string]any{"type": "string"},
			},
		},
		Annotations: &mcp.ToolAnnotations{
			Title:           "Search annotation",
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: &destructive,
			OpenWorldHint:   &openWorld,
		},
		Icons: []mcp.Icon{{Source: "https://example.test/icon.svg", MIMEType: "image/svg+xml"}},
	}
}

func mustDefinitionJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal definition: %v", err)
	}
	return data
}

func TestMCPDefinitionFingerprintCoversWholeDefinition(t *testing.T) {
	base := definitionFixture()
	tool, err := NewMCPTool("srv", base, time.Second, definitionCaller())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(mustDefinitionJSON(t, base))
	want := hex.EncodeToString(sum[:])
	if tool.DefinitionFingerprint() != want {
		t.Fatalf("fingerprint = %s, want SHA-256 of canonical encoding/json output %s", tool.DefinitionFingerprint(), want)
	}

	// encoding/json sorts object keys, so insertion order is not definition identity.
	orderedFixture := definitionFixture()
	orderedFixture.InputSchema = map[string]any{
		"required":   []any{"query"},
		"properties": map[string]any{"query": map[string]any{"type": "string"}},
		"type":       "object",
	}
	orderedFixture.OutputSchema = map[string]any{
		"properties": map[string]any{"answer": map[string]any{"type": "string"}},
		"type":       "object",
	}
	ordered, err := NewMCPTool("srv", orderedFixture, time.Second, definitionCaller())
	if err != nil {
		t.Fatal(err)
	}
	if tool.DefinitionFingerprint() != ordered.DefinitionFingerprint() {
		t.Fatal("equivalent definitions with different map iteration/insertion order differ")
	}

	changes := map[string]func(*mcp.Tool){
		"name":             func(s *mcp.Tool) { s.Name = "changed" },
		"description":      func(s *mcp.Tool) { s.Description = "changed" },
		"input schema":     func(s *mcp.Tool) { s.InputSchema.(map[string]any)["additionalProperties"] = false },
		"output schema":    func(s *mcp.Tool) { s.OutputSchema.(map[string]any)["additionalProperties"] = false },
		"annotation title": func(s *mcp.Tool) { s.Annotations.Title = "changed" },
		"read only":        func(s *mcp.Tool) { s.Annotations.ReadOnlyHint = false },
		"idempotent":       func(s *mcp.Tool) { s.Annotations.IdempotentHint = false },
		"destructive":      func(s *mcp.Tool) { s.Annotations.DestructiveHint = nil },
		"open world":       func(s *mcp.Tool) { s.Annotations.OpenWorldHint = nil },
		"title":            func(s *mcp.Tool) { s.Title = "changed" },
		"icons":            func(s *mcp.Tool) { s.Icons = nil },
		"metadata":         func(s *mcp.Tool) { s.Meta["changed"] = true },
	}
	for field, change := range changes {
		changed := definitionFixture()
		change(changed)
		changedTool, err := NewMCPTool("srv", changed, time.Second, definitionCaller())
		if err != nil {
			t.Fatalf("%s: %v", field, err)
		}
		if tool.DefinitionFingerprint() == changedTool.DefinitionFingerprint() {
			t.Errorf("fingerprint did not change when %s changed", field)
		}
	}
}

func TestMCPDefinitionSnapshotAndAccessorDetachment(t *testing.T) {
	spec := definitionFixture()
	tool, err := NewMCPTool("srv", spec, time.Second, definitionCaller())
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := tool.DefinitionFingerprint()
	wantAnnotations := *tool.Annotations()
	wantInput := tool.InputSchema()
	wantOutput := tool.OutputSchema()

	// Every one of these mutations is against memory still referenced by spec.
	spec.Name = "mutated"
	spec.Title = "mutated"
	spec.Description = "mutated"
	spec.Icons = nil
	spec.Meta["changed"] = true
	input := spec.InputSchema.(map[string]any)
	input["type"] = "mutated"
	input["properties"].(map[string]any)["query"] = "mutated"
	output := spec.OutputSchema.(map[string]any)
	output["type"] = "mutated"
	output["properties"].(map[string]any)["answer"] = "mutated"
	spec.Annotations.Title = "mutated"
	spec.Annotations.ReadOnlyHint = false
	spec.Annotations.IdempotentHint = false
	*spec.Annotations.DestructiveHint = false
	*spec.Annotations.OpenWorldHint = true

	if tool.DefinitionFingerprint() != fingerprint {
		t.Fatal("mutating the incoming spec changed the frozen fingerprint")
	}
	if got := tool.InputSchema(); !reflect.DeepEqual(got, wantInput) {
		t.Fatalf("input snapshot changed: got %#v, want %#v", got, wantInput)
	}
	if got := tool.OutputSchema(); !reflect.DeepEqual(got, wantOutput) {
		t.Fatalf("output snapshot changed: got %#v, want %#v", got, wantOutput)
	}
	if got := tool.Annotations(); !reflect.DeepEqual(*got, wantAnnotations) {
		t.Fatalf("annotations snapshot changed: got %#v, want %#v", *got, wantAnnotations)
	}

	// Copies returned to one caller cannot poison the next caller's copy.
	inputCopy := tool.InputSchema()
	inputCopy["type"] = "caller mutation"
	inputCopy["properties"].(map[string]any)["query"] = "caller mutation"
	if got := tool.InputSchema(); !reflect.DeepEqual(got, wantInput) {
		t.Fatalf("mutating returned input changed wrapper: got %#v, want %#v", got, wantInput)
	}
	outputCopy := tool.OutputSchema()
	outputCopy["type"] = "caller mutation"
	outputCopy["properties"].(map[string]any)["answer"] = "caller mutation"
	if got := tool.OutputSchema(); !reflect.DeepEqual(got, wantOutput) {
		t.Fatalf("mutating returned output changed wrapper: got %#v, want %#v", got, wantOutput)
	}
	annotationCopy := tool.Annotations()
	*annotationCopy.DestructiveHint = false
	*annotationCopy.OpenWorldHint = true
	annotationCopy.Title = "caller mutation"
	annotationCopy.ReadOnlyHint = false
	annotationCopy.IdempotentHint = false
	if got := tool.Annotations(); !reflect.DeepEqual(*got, wantAnnotations) {
		t.Fatalf("mutating returned annotations changed wrapper: got %#v, want %#v", *got, wantAnnotations)
	}
	if tool.DefinitionFingerprint() != fingerprint {
		t.Fatal("accessor mutation changed the fingerprint")
	}
}

func TestMCPOutputSchemaAbsent(t *testing.T) {
	spec := definitionFixture()
	spec.OutputSchema = nil
	tool, err := NewMCPTool("srv", spec, time.Second, definitionCaller())
	if err != nil {
		t.Fatal(err)
	}
	if got := tool.OutputSchema(); got != nil {
		t.Fatalf("absent OutputSchema = %#v, want nil", got)
	}
}

func TestMCPDefinitionFingerprintUsesUntruncatedDescription(t *testing.T) {
	spec := definitionFixture()
	spec.Description = strings.Repeat("描", MaxSynthDescriptionLength+1) + "A"
	other := definitionFixture()
	other.Description = strings.Repeat("描", MaxSynthDescriptionLength+1) + "B"

	a, err := NewMCPTool("srv", spec, time.Second, definitionCaller())
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewMCPTool("srv", other, time.Second, definitionCaller())
	if err != nil {
		t.Fatal(err)
	}
	if a.Description() != b.Description() {
		t.Fatalf("model descriptions unexpectedly differ: %q vs %q", a.Description(), b.Description())
	}
	if a.DefinitionFingerprint() == b.DefinitionFingerprint() {
		t.Fatal("description tail lost by model-facing truncation must remain in fingerprint")
	}
}

func TestMCPWithSpillDirDoesNotMutatePoolWrapper(t *testing.T) {
	tool, err := NewMCPTool("srv", definitionFixture(), time.Second, definitionCaller())
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := tool.DefinitionFingerprint()
	session := tool.WithSpillDir(`X:\session\spill`)
	if tool.SpillDir != "" {
		t.Fatalf("pool SpillDir = %q, want unchanged empty value", tool.SpillDir)
	}
	if session.SpillDir != `X:\session\spill` {
		t.Fatalf("session SpillDir = %q", session.SpillDir)
	}
	if session.DefinitionFingerprint() != fingerprint {
		t.Fatal("session-local spill copy changed definition identity")
	}
}

func TestMCPDefinitionValidationAndScheduling(t *testing.T) {
	nonSerializable := definitionFixture()
	nonSerializable.InputSchema.(map[string]any)["bad"] = make(chan int)
	if _, err := NewMCPTool("srv", nonSerializable, time.Second, definitionCaller()); err == nil {
		t.Fatal("non-JSON-serializable definition must fail")
	}

	for name, invalid := range map[string]any{
		"non-object":   "not an object",
		"explicit nil": json.RawMessage("null"),
	} {
		invalidOutput := definitionFixture()
		invalidOutput.OutputSchema = invalid
		if _, err := NewMCPTool("srv", invalidOutput, time.Second, definitionCaller()); err == nil ||
			!strings.Contains(err.Error(), "outputSchema") {
			t.Fatalf("%s: error = %v, want an outputSchema diagnostic", name, err)
		}
	}

	tests := []struct {
		name       string
		annotation mcp.ToolAnnotations
		readOnly   bool
		mode       ExecutionMode
	}{
		{name: "parallel", annotation: mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}, readOnly: true, mode: Parallel},
		{name: "not idempotent", annotation: mcp.ToolAnnotations{ReadOnlyHint: true}, readOnly: true, mode: Sequential},
		{name: "not read only", annotation: mcp.ToolAnnotations{IdempotentHint: true}, mode: Sequential},
		{name: "neither hint", annotation: mcp.ToolAnnotations{}, mode: Sequential},
	}
	for _, test := range tests {
		spec := definitionFixture()
		spec.Annotations = &test.annotation
		tool, err := NewMCPTool("srv", spec, time.Second, definitionCaller())
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if tool.IsReadOnly() != test.readOnly || tool.ExecutionMode() != test.mode {
			t.Errorf("%s: readOnly=%v mode=%v, want %v/%v", test.name,
				tool.IsReadOnly(), tool.ExecutionMode(), test.readOnly, test.mode)
		}
	}
}
