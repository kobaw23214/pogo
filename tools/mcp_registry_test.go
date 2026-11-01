package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRegistryMCPReplacementPreservesSessionTools(t *testing.T) {
	makeTool := func(name string, ro bool) *MCPTool {
		tool, err := NewMCPTool("fixture", mcpSpec(name, name, &mcp.ToolAnnotations{ReadOnlyHint: ro}), 0,
			func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		return tool
	}
	old, next := makeTool("old", true), makeTool("next", false)
	root := t.TempDir()
	reg := New(Options{Cwd: root, MCP: []Tool{old}, Todo: true, SpillDir: filepath.Join(root, "first")})
	replaced := reg.WithMCP([]Tool{next})
	if _, ok := reg.Get(old.Name()); !ok {
		t.Fatal("original run registry changed")
	}
	if _, ok := replaced.Get(old.Name()); ok {
		t.Fatal("removed MCP tool survived")
	}
	for _, tool := range reg.All() {
		if _, mcpTool := tool.(*MCPTool); mcpTool {
			continue
		}
		if _, helper := tool.(*MCPDescribe); helper {
			// The inspection helper belongs to the MCP snapshot, not the
			// session: WithMCP rebuilds it from the new catalog, so the
			// instance must change while everything session-local is kept.
			after, ok := replaced.Get(tool.Name())
			if !ok {
				t.Fatal("refreshed registry lost its MCP helper")
			}
			if after == tool {
				t.Fatal("stale helper carried into the new snapshot")
			}
			continue
		}
		if after, ok := replaced.Get(tool.Name()); !ok || after != tool {
			t.Fatalf("stateful local tool was replaced: %s", tool.Name())
		}
	}
	if old.SpillDir != "" || next.SpillDir != "" {
		t.Fatal("shared pool wrapper mutated")
	}
	registered, _ := replaced.Get(next.Name())
	if registered.(*MCPTool).SpillDir != reg.spillDir || reg.spillDir == filepath.Join(root, "first") {
		t.Fatal("new tools lost the session's spill directory")
	}
	if reg.results == nil || replaced.results != reg.results {
		t.Fatal("MCP catalog refresh replaced the session's stored observation namespace")
	}
	if reader, ok := replaced.Get(ToolResultReadName); !ok || reader.(*ToolResultRead).Store != reg.results {
		t.Fatal("result reader does not retain the session store across MCP refresh")
	}
	empty := replaced.WithMCP(nil)
	readded := empty.WithMCP([]Tool{old})
	if empty.results != reg.results || readded.results != reg.results || readded.spillDir != reg.spillDir {
		t.Fatal("removing/readding the MCP catalog changed the session's result storage")
	}
	if readded.All()[readded.mcpStart].Name() != old.Name() {
		t.Fatal("empty catalog lost MCP insertion point")
	}

	ro := New(Options{Cwd: root, ReadOnly: true}).WithMCP([]Tool{old, next})
	if _, ok := ro.Get(next.Name()); ok {
		t.Fatal("refresh widened a read-only registry")
	}
	if _, ok := ro.Get(old.Name()); !ok {
		t.Fatal("read-only MCP tool missing")
	}
}
