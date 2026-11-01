package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// catalogSpec builds a neutral synthetic tool spec with catalog hints.
func catalogSpec(name, group, role string) *mcp.Tool {
	meta := map[string]any{}
	if group != "" || role != "" {
		hint := map[string]any{}
		if group != "" {
			hint["group"] = group
		}
		if role != "" {
			hint["role"] = role
		}
		meta[MCPCatalogMetaKey] = hint
	}
	return &mcp.Tool{
		Name:        name,
		Description: "synthetic " + name + " operation for layered discovery tests",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Meta:        meta,
	}
}

func catalogTool(t *testing.T, server string, spec *mcp.Tool) *MCPTool {
	t.Helper()
	tool, err := NewMCPTool(server, spec, 0,
		func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			return textResult("ok"), nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func TestMCPCatalogHintParsing(t *testing.T) {
	tool := catalogTool(t, "alpha", catalogSpec("nav", "navigators", MCPCatalogRoleDirectory))
	if tool.CatalogGroup() != "navigators" || tool.CatalogRole() != MCPCatalogRoleDirectory {
		t.Fatalf("hints not captured: %q %q", tool.CatalogGroup(), tool.CatalogRole())
	}

	cases := map[string]map[string]any{
		"no meta":        nil,
		"wrong key":      {"other": map[string]any{"group": "x"}},
		"hint not map":   {MCPCatalogMetaKey: "navigators"},
		"unknown role":   {MCPCatalogMetaKey: map[string]any{"group": "g", "role": "admin"}},
		"non-string grp": {MCPCatalogMetaKey: map[string]any{"group": 7, "role": "operation"}},
	}
	for name, meta := range cases {
		spec := mcpSpec("plain", "desc", nil)
		spec.Meta = meta
		tool := catalogTool(t, "beta", spec)
		switch name {
		case "unknown role":
			if tool.CatalogRole() != "" || tool.CatalogGroup() != "g" {
				t.Fatalf("%s: role/group = %q/%q", name, tool.CatalogRole(), tool.CatalogGroup())
			}
		case "non-string grp":
			if tool.CatalogGroup() != "" || tool.CatalogRole() != MCPCatalogRoleOperation {
				t.Fatalf("%s: role/group = %q/%q", name, tool.CatalogRole(), tool.CatalogGroup())
			}
		default:
			if tool.CatalogGroup() != "" || tool.CatalogRole() != "" {
				t.Fatalf("%s: malformed hint leaked: %q/%q", name, tool.CatalogGroup(), tool.CatalogRole())
			}
		}
	}

	// The frozen clone rules: mutating the caller's spec afterwards changes nothing.
	spec := catalogSpec("mut", "before", "operation")
	tool = catalogTool(t, "gamma", spec)
	spec.Meta[MCPCatalogMetaKey] = map[string]any{"group": "after", "role": "directory"}
	if tool.CatalogGroup() != "before" || tool.CatalogRole() != "operation" {
		t.Fatal("late mutation of _meta rewrote the frozen wrapper")
	}
}

// catalogFixture is a small two-server catalog with groups, roles and one
// long full description only the full text matches.
func catalogFixture(t *testing.T) *MCPDescribe {
	t.Helper()
	longTail := "zzqmarker" // appears only in the untruncated full description
	specs := []struct {
		name, group, role, server, desc string
	}{
		{"browse", "navigators", "directory", "alpha", "browse the alpha catalog"},
		{"fetch", "fetchers", "operation", "alpha", "fetch one alpha record"},
		{"plain", "", "", "alpha", "ungrouped alpha tool"},
		{"scan", "navigators", "operation", "beta", "scan beta records. " + strings.Repeat("padding ", MaxSynthDescriptionLength) + longTail},
	}
	var ts []*MCPTool
	for _, s := range specs {
		spec := catalogSpec(s.name, s.group, s.role)
		spec.Description = s.desc
		ts = append(ts, catalogTool(t, s.server, spec))
	}
	d := newMCPDescribe(ts, t.TempDir())
	if d == nil {
		t.Fatal("no helper")
	}
	return d
}

func TestMCPDescribeExactFiltersAndGroupsMode(t *testing.T) {
	d := catalogFixture(t)
	run := func(args string) Result {
		t.Helper()
		res, err := d.Execute(context.Background(), json.RawMessage(args))
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		return res
	}

	res := run(`{"server":"alpha"}`)
	if strings.Contains(res.Text, "mcp_beta_scan") || !strings.Contains(res.Text, "mcp_alpha_fetch") {
		t.Fatalf("server filter wrong:\n%s", res.Text)
	}
	res = run(`{"group":"navigators"}`)
	if !strings.Contains(res.Text, "mcp_alpha_browse") || !strings.Contains(res.Text, "mcp_beta_scan") ||
		strings.Contains(res.Text, "mcp_alpha_fetch") {
		t.Fatalf("group filter wrong:\n%s", res.Text)
	}
	res = run(`{"role":"directory"}`)
	if !strings.Contains(res.Text, "mcp_alpha_browse") || strings.Contains(res.Text, "mcp_beta_scan") {
		t.Fatalf("role filter wrong:\n%s", res.Text)
	}
	// Text search covers the FULL frozen description, not just the summary.
	res = run(`{"filter":"zzqmarker"}`)
	if !strings.Contains(res.Text, "mcp_beta_scan") {
		t.Fatalf("full-description search missed:\n%s", res.Text)
	}

	res = run(`{"mode":"groups"}`)
	for _, want := range []string{"server alpha", "server beta", "group navigators", "group fetchers", "declared no group"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("groups mode missing %q:\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "mcp_alpha_fetch  (") {
		t.Fatalf("groups mode leaked tool rows:\n%s", res.Text)
	}
}

func TestMCPDescribeDetailCarriesCatalogHints(t *testing.T) {
	d := catalogFixture(t)
	res, err := d.Execute(context.Background(), json.RawMessage(`{"tool":"mcp_alpha_browse"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, `"group": "navigators"`) || !strings.Contains(res.Text, `"role": "directory"`) {
		t.Fatalf("detail missing hints:\n%s", res.Text)
	}
	det, ok := res.Details.(MCPDescribeDetails)
	if !ok || det.WireName != "mcp_alpha_browse" || det.Fingerprint == "" {
		t.Fatalf("detail provenance missing: %#v", res.Details)
	}
	// A tool without hints keeps them absent rather than inventing values.
	res, err = d.Execute(context.Background(), json.RawMessage(`{"tool":"mcp_alpha_plain"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, `"group"`) || strings.Contains(res.Text, `"role"`) {
		t.Fatalf("hint-less tool grew hints:\n%s", res.Text)
	}
}
