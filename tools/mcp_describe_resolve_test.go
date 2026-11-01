package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// These tests cover detail-mode name resolution: the canonical local wire
// name stays authoritative, a unique exact remote (server-declared) tool name
// resolves — optionally scoped by server — and ambiguity or server mismatch
// fail explicitly instead of guessing. No fuzzy matching is introduced.

func describeHelper(t *testing.T, tools ...*MCPTool) *MCPDescribe {
	t.Helper()
	root := t.TempDir()
	asTools := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		asTools = append(asTools, tool)
	}
	return helperOf(t, New(Options{Cwd: root, MCP: asTools, SpillDir: filepath.Join(root, "spill")}))
}

func TestMCPDescribeResolvesUniqueRemoteName(t *testing.T) {
	d := describeHelper(t, noteTool(), calcTool())
	res, err := d.Execute(context.Background(), json.RawMessage(`{"tool":"find_note"}`))
	if err != nil {
		t.Fatal(err)
	}
	details := res.Details.(MCPDescribeDetails)
	if details.WireName != "mcp_notepad_find_note" || details.Server != "notepad" || details.Tool != "find_note" {
		t.Fatalf("remote name resolved to the wrong entry: %#v", details)
	}
	// The answer names the concrete next invocation with the canonical wire
	// name, keeps both schemas complete, and carries the reuse guidance.
	for _, want := range []string{`"wire_name": "mcp_notepad_find_note"`, "next_step", `"mcp_notepad_find_note"`,
		"reuse_note", `"input_schema"`, `"output_schema"`} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("detail answer missing %q:\n%s", want, res.Text)
		}
	}
	// Truthful wording in both contexts: the helper never executes and never
	// authorizes; next-request exposure is conditional agent behavior.
	if !strings.Contains(res.Text, "cannot execute") || strings.Contains(res.Text, "has been authorized") {
		t.Fatalf("next-step wording is not truthful:\n%s", res.Text)
	}
}

func TestMCPDescribeRemoteNameResolvesHashedWireName(t *testing.T) {
	// A long server/tool pair whose wire name is truncated with a hash: the
	// remote name is the only practical handle, and it must still resolve.
	server := "warehouse-" + strings.Repeat("s", 40)
	remote := "lookup_" + strings.Repeat("t", 40)
	tool := describeSpec(server, remote, "Long-named synthetic service", nil, nil)
	if tool.Name() == "mcp_"+server+"_"+remote {
		t.Fatal("fixture did not trigger wire-name hashing")
	}
	d := describeHelper(t, tool)
	res, err := d.Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"tool":%q}`, remote)))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Details.(MCPDescribeDetails).WireName; got != tool.Name() {
		t.Fatalf("resolved %q, want canonical %q", got, tool.Name())
	}
}

func TestMCPDescribeWireNameStaysAuthoritativeOverRemoteCollision(t *testing.T) {
	// Server bravo literally declares a tool named like server's alpha wire
	// name. The exact wire name must still pick alpha's tool, not the
	// colliding remote name.
	alpha := describeSpec("alpha", "fetch", "Alpha service", nil, nil)
	bravo := describeSpec("bravo", "mcp_alpha_fetch", "Bravo service", nil, nil)
	d := describeHelper(t, alpha, bravo)
	res, err := d.Execute(context.Background(), json.RawMessage(`{"tool":"mcp_alpha_fetch"}`))
	if err != nil {
		t.Fatal(err)
	}
	if details := res.Details.(MCPDescribeDetails); details.Server != "alpha" || details.Tool != "fetch" {
		t.Fatalf("wire name lost to a colliding remote name: %#v", details)
	}
	// A server qualifier cannot redirect an authoritative wire name. Bravo's
	// colliding tool remains reachable by its own distinct canonical wire name.
	res, err = d.Execute(context.Background(), json.RawMessage(`{"tool":"mcp_alpha_fetch","server":"bravo"}`))
	if err == nil || !strings.Contains(err.Error(), "not \"bravo\"") {
		t.Fatalf("wire name with mismatched server must fail, got %v", err)
	}
}

func TestMCPDescribeAmbiguousRemoteNameFailsWithBoundedCandidates(t *testing.T) {
	var tools []*MCPTool
	for i := 0; i < 12; i++ {
		tools = append(tools, describeSpec(fmt.Sprintf("svc%02d", i), "lookup", "Shared-name service", nil, nil))
	}
	d := describeHelper(t, tools...)
	_, err := d.Execute(context.Background(), json.RawMessage(`{"tool":"lookup"}`))
	if err == nil {
		t.Fatal("ambiguous remote name resolved instead of failing")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ambiguous") || !strings.Contains(msg, "12 matches") {
		t.Fatalf("ambiguity not explicit: %v", err)
	}
	if !strings.Contains(msg, "mcp_svc00_lookup") || !strings.Contains(msg, "and 4 more") {
		t.Fatalf("candidate list not bounded and useful: %v", err)
	}
	if strings.Contains(msg, "mcp_svc08_lookup") {
		t.Fatalf("candidate list exceeded the bound: %v", err)
	}
	// Server scope disambiguates to exactly one canonical wire name.
	res, err := d.Execute(context.Background(), json.RawMessage(`{"tool":"lookup","server":"svc07"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Details.(MCPDescribeDetails).WireName; got != "mcp_svc07_lookup" {
		t.Fatalf("scoped resolution = %q", got)
	}
}

func TestMCPDescribeServerMismatchNeverRedirects(t *testing.T) {
	d := describeHelper(t, describeSpec("notes", "lookup", "First service", nil, nil),
		describeSpec("archive", "lookup", "Second service", nil, nil))
	// Remote name scoped to a server that does not declare it: fail, naming
	// the servers that do, rather than silently picking one.
	_, err := d.Execute(context.Background(), json.RawMessage(`{"tool":"lookup","server":"missing"}`))
	if err == nil || !strings.Contains(err.Error(), "mcp_notes_lookup") || !strings.Contains(err.Error(), "mcp_archive_lookup") {
		t.Fatalf("mismatch did not fail with real candidates: %v", err)
	}
	// Canonical wire name with a mismatched server scope: fail explicitly.
	_, err = d.Execute(context.Background(), json.RawMessage(`{"tool":"mcp_notes_lookup","server":"archive"}`))
	if err == nil || !strings.Contains(err.Error(), "never redirects") {
		t.Fatalf("wire-name/server mismatch did not fail: %v", err)
	}
}

func TestMCPDescribeUnknownNameKeepsIndexHint(t *testing.T) {
	d := describeHelper(t, noteTool())
	_, err := d.Execute(context.Background(), json.RawMessage(`{"tool":"dataset-7713"}`))
	if err == nil || !strings.Contains(err.Error(), "no MCP tool named") ||
		!strings.Contains(err.Error(), "list what is available") {
		t.Fatalf("unknown identifier error regressed: %v", err)
	}
	// Discovery selectors must not disappear into a successful different mode.
	res, err := d.Execute(context.Background(), json.RawMessage(`{"tool":"find_note","extra":true}`))
	if err == nil || !strings.Contains(err.Error(), "unknown parameter") || res.Text != "" {
		t.Fatalf("unknown argument was ignored: %v", err)
	}
}
