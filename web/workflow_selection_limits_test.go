package web

import (
	"slices"
	"testing"

	"github.com/yosukeno/pogo/tools"
)

func TestWorkflowStoreFullBuiltinSelectionRoundTrip(t *testing.T) {
	var names []string
	for _, tool := range tools.BuiltinCatalog() {
		names = append(names, tool.Name)
	}
	if len(names) <= 16 {
		t.Fatal("fixture must cover a catalog larger than the former 16-tool limit")
	}
	dir := t.TempDir()
	store := bootWorkflowStore(t, dir)
	input := wfTestInput()
	selected := append(slices.Clone(names), " "+names[0]+" ", names[len(names)-1])
	input.Nodes[0].Tools = &selected
	created, err := store.put("", input, 0)
	if err != nil {
		t.Fatalf("create with the full tool catalog: %v", err)
	}
	if created.Nodes[0].Tools == nil || !slices.Equal(*created.Nodes[0].Tools, names) {
		t.Fatalf("created tool selection = %v, want every catalog tool in order", created.Nodes[0].Tools)
	}
	input.Nodes[0].Tools = &names
	updated, err := store.put(created.ID, input, created.Revision)
	if err != nil {
		t.Fatalf("edit with the full tool catalog: %v", err)
	}
	reopened := bootWorkflowStore(t, dir)
	saved, ok := reopened.get(updated.ID)
	if !ok || saved.Nodes[0].Tools == nil || !slices.Equal(*saved.Nodes[0].Tools, names) {
		t.Fatalf("saved tool selection lost catalog entries after reopening: %+v", saved)
	}
	// Removing the count cap must not turn a node into a broader grant than
	// its Agent: the existing intersection remains the runtime boundary.
	agentTools := []string{"write", "browser"}
	narrowed := narrowWorkflowTools(&agentTools, saved.Nodes[0].Tools)
	if narrowed == nil || !slices.Equal(*narrowed, agentTools) {
		t.Fatalf("workflow expanded the Agent's tool grants: %v", narrowed)
	}
}
