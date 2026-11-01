package tools

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
)

// The library normally lives outside the workspace. Keep the same protection
// when a single-user deployment chooses a SessionDir underneath its Cwd. This
// is a file-tool guard, not a claim of an OS boundary for arbitrary shell code.
type informationProtectedFileTool struct {
	Tool
	cwd, root string
}

func protectInformationFileTool(tool Tool, cwd, root string) Tool {
	if root == "" || (tool.Name() != "write" && tool.Name() != "edit") {
		return tool
	}
	return &informationProtectedFileTool{Tool: tool, cwd: cwd, root: root}
}

func (t *informationProtectedFileTool) Schema() map[string]any {
	if provider, ok := t.Tool.(SchemaProvider); ok {
		return provider.Schema()
	}
	return t.Tool.InputSchema()
}
func (t *informationProtectedFileTool) ValidateArgs(raw json.RawMessage) error {
	return ValidateArgs(t.Tool, raw)
}
func (t *informationProtectedFileTool) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	target := args.Path
	if !filepath.IsAbs(target) {
		target = filepath.Join(t.cwd, target)
	}
	// Lexical and real checks cover both direct paths and a workspace alias.
	// The underlying file tools retain their ordinary workspace/readonly rules.
	if within(filepath.Clean(target), filepath.Clean(t.root)) || within(canonical(target), canonical(t.root)) {
		return Result{}, errors.New("the information library is read-only; edit a workspace copy instead")
	}
	return t.Tool.Execute(ctx, raw)
}
