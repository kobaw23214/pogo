package web

// Tests for the MCP server side's forwarded external tools: the pool's
// connected servers re-exposed on this machine's own /mcp under
// "<server>.<tool>" names, opt-in via the include_tools whitelist — plus the
// serve_catalog picker inventory on GET /api/mcp. The fixtures are the
// existing suites' own: serveFixture/putMCP from mcpserve_test.go and the
// in-memory upstream server from mcprefresh_test.go, because the wire
// behavior (whitelist filter, SDK dispatch, pool session) is the thing under
// test, same as there.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/skills"
)

// forwardFixture pairs a connected upstream (one "echo" tool: writes back its
// description plus the verbatim arguments) with the standard /api/mcp
// harness, then PUTs a config wiring the two under the given server section.
func forwardFixture(t *testing.T, serverSection string, tweaks ...func(*Config)) (*mcpClientAPI, *webCatalogFixture) {
	t.Helper()
	f := newWebCatalogFixture(t, false, true)
	a := serveFixture(t, tweaks...)
	a.putMCP(t, `{"servers":{"filesystem":{"url":"`+f.url+`"}},"server":`+serverSection+`}`)
	a.waitMCPConnected(t, "filesystem")
	return a, f
}

// TestMCPForwardOptInEmptyWhitelist: with no whitelist the historical surface
// is unchanged — a connected server's tools are not listed and calling the
// would-be forward name fails exactly like any unknown tool. The exposure is
// a grant, not an upgrade side effect.
func TestMCPForwardOptInEmptyWhitelist(t *testing.T) {
	a, _ := forwardFixture(t, `{"enabled":true}`)
	cs := connectStreamable(t, a.srv.URL, "")

	names := listMCPNames(t, cs)
	if names["filesystem.echo"] {
		t.Fatalf("empty whitelist must not forward: %v", names)
	}
	if !names["agent_submit_task"] {
		t.Fatalf("builtin surface must be untouched: %v", names)
	}
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "filesystem.echo", Arguments: map[string]any{"text": "hi"},
	}); err == nil {
		t.Fatal("calling an unregistered forward name must fail like an unknown tool")
	}
}

// TestMCPForwardWhitelistHit: "filesystem.*" registers the server's tools
// under their dotted names, a call forwards and returns the upstream content,
// and clearing the whitelist unregisters again — both directions hot, no
// restart.
func TestMCPForwardWhitelistHit(t *testing.T) {
	a, f := forwardFixture(t, `{"enabled":true,"policy":"auto","include_tools":["agent_*","filesystem.*"]}`)
	cs := connectStreamable(t, a.srv.URL, "")

	// The first tools/list after the pool connected is also ensureForwardSync's
	// proof: the PUT raced the connect, so only the list-time sync can have
	// registered these.
	names := listMCPNames(t, cs)
	if !names["filesystem.echo"] || !names["agent_submit_task"] {
		t.Fatalf("whitelisted list = %v, want filesystem.echo + agent_*", names)
	}
	if names["agent_task_list"] != true || len(names) != 10 {
		t.Fatalf("filtered list = %v, want the 9 agent_* tools plus filesystem.echo", names)
	}

	res := callTool(t, cs, "filesystem.echo", map[string]any{"text": "hi"})
	if text := resultText(res); !strings.Contains(text, "original:") || !strings.Contains(text, `"hi"`) {
		t.Fatalf("forwarded result = %q, want the upstream's echo of the arguments", text)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", f.calls.Load())
	}

	// Clearing the whitelist removes the forwarded names without a restart —
	// the opt-in must close behind itself.
	a.putMCP(t, `{"servers":{"filesystem":{"url":"`+f.url+`"}},"server":{"enabled":true}}`)
	if names := listMCPNames(t, cs); names["filesystem.echo"] || !names["agent_submit_task"] {
		t.Fatalf("cleared-whitelist list = %v, want builtins only", names)
	}
}

// TestMCPForwardPolicyGate: a forwarded call clears the same two bars a synth
// passthrough clears — schema validation, then the sessionless policy matrix.
// Under the default standard policy a mutating tool is refused, and the
// refusal names agent_submit_task; it is a result, not a protocol error, and
// it never reaches the upstream.
func TestMCPForwardPolicyGate(t *testing.T) {
	a, f := forwardFixture(t, `{"enabled":true,"include_tools":["filesystem.*"]}`)
	cs := connectStreamable(t, a.srv.URL, "")

	names := listMCPNames(t, cs)
	if !names["filesystem.echo"] {
		t.Fatalf("whitelist registers regardless of policy; policy gates the call: %v", names)
	}

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "filesystem.echo", Arguments: map[string]any{"text": "hi"},
	})
	if err != nil {
		t.Fatalf("a policy refusal is a tool result, not a protocol error: %v", err)
	}
	if text := resultText(res); !res.IsError ||
		!strings.Contains(text, "standard") || !strings.Contains(text, "agent_submit_task") {
		t.Fatalf("refusal = %v %q, want the standard-policy pointer at agent_submit_task", res.IsError, text)
	}
	if f.calls.Load() != 0 {
		t.Fatalf("refused call reached the upstream %d times", f.calls.Load())
	}

	// Bad arguments fail the first bar — the same "invalid arguments" shape
	// the synth passthrough returns.
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "filesystem.echo", Arguments: map[string]any{},
	})
	if err != nil || !res.IsError || !strings.Contains(resultText(res), "invalid arguments") {
		t.Fatalf("missing-arg call = %v %q, want an invalid-arguments result", err, resultText(res))
	}
}

// TestForwardToolName pins the exposed-name mapping: the dotted
// "<server>.<tool>" form, with characters outside the MCP name alphabet
// folded to "_" in the tool half (server names are already confined by
// mcpclient's validation).
func TestForwardToolName(t *testing.T) {
	for serverTool, want := range map[string]string{
		"filesystem\x00read_file":   "filesystem.read_file",
		"filesystem\x00read file":   "filesystem.read_file",
		"filesystem\x00a/b\\c:d":    "filesystem.a_b_c_d",
		"filesystem\x00ok.name-1_2": "filesystem.ok.name-1_2",
		"filesystem\x00日本語":         "filesystem.___",
	} {
		parts := strings.SplitN(serverTool, "\x00", 2)
		if got := forwardToolName(parts[0], parts[1]); got != want {
			t.Errorf("forwardToolName(%q, %q) = %q, want %q", parts[0], parts[1], got, want)
		}
	}
}

// TestMCPForwardNameCollisionWithSynth: a server named "mal" forwarding its
// "echo" tool would land on "mal.echo" — a name the synth passthrough already
// owns. The forwarded tool skips (the SDK's AddTool would replace silently),
// so the manifest tool still answers.
func TestMCPForwardNameCollisionWithSynth(t *testing.T) {
	skillDir := t.TempDir()
	toolsJSON := `{"tools":[{"name":"mal.echo","title":"Echo","description":"test passthrough",` +
		`"inputSchema":{"type":"object","properties":{},"additionalProperties":false},` +
		`"annotations":{"readOnlyHint":true},"x-pogo-exec":{"argv":["echo","hi"]}}]}`
	if err := os.WriteFile(filepath.Join(skillDir, "tools.json"), []byte(toolsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	sk := skills.Skill{Name: "t", Description: "test", Path: filepath.Join(skillDir, "SKILL.md"), Dir: skillDir}

	f := newWebCatalogFixture(t, false, true)
	a := serveFixture(t, func(c *Config) { c.Skills = []skills.Skill{sk} })
	a.putMCP(t, `{"servers":{"mal":{"url":"`+f.url+`"}},"server":{"enabled":true,"policy":"auto","include_tools":["mal.*"]}}`)
	a.waitMCPConnected(t, "mal")
	cs := connectStreamable(t, a.srv.URL, "")

	names := listMCPNames(t, cs)
	if len(names) != 1 || !names["mal.echo"] {
		t.Fatalf("list = %v, want exactly mal.echo", names)
	}
	// The synth tool takes no arguments; the fixture's echo requires "text".
	// A call with none answers the manifest's argv output — any other answer
	// means the forwarded tool displaced it.
	res := callTool(t, cs, "mal.echo", map[string]any{})
	if text := resultText(res); !strings.Contains(text, "hi") || strings.Contains(text, "original:") {
		t.Fatalf("mal.echo answered %q, want the synth tool's argv output", text)
	}
}

// TestMCPServeCatalog: GET /api/mcp's serve_catalog is the picker's whole
// inventory — the static builtin list (pinned against a live tools/list so
// the mirror cannot drift from server()), the skill manifest tools, and the
// pooled servers with each tool's exposed and remote names.
func TestMCPServeCatalog(t *testing.T) {
	skillDir := t.TempDir()
	toolsJSON := `{"tools":[{"name":"mal.echo","title":"Echo","description":"test passthrough",` +
		`"inputSchema":{"type":"object","properties":{},"additionalProperties":false},` +
		`"annotations":{"readOnlyHint":true},"x-pogo-exec":{"argv":["echo","hi"]}}]}`
	if err := os.WriteFile(filepath.Join(skillDir, "tools.json"), []byte(toolsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	sk := skills.Skill{Name: "t", Description: "test", Path: filepath.Join(skillDir, "SKILL.md"), Dir: skillDir}

	f := newWebCatalogFixture(t, false, true)
	a := serveFixture(t, func(c *Config) { c.Skills = []skills.Skill{sk} })
	a.putMCP(t, `{"servers":{"filesystem":{"url":"`+f.url+`"}},"server":{"enabled":true}}`)
	a.waitMCPConnected(t, "filesystem")

	v := a.getMCP(t)
	cat := v.ServeCatalog
	if cat == nil {
		t.Fatal("GET /api/mcp carries no serve_catalog")
	}

	// The builtin mirror is only honest if it equals the live surface: list
	// tools for real and compare both directions.
	cs := connectStreamable(t, a.srv.URL, "")
	live := listMCPNames(t, cs)
	if len(cat.Builtin) != 9 {
		t.Fatalf("builtin = %d rows, want the 9 agent_* tools", len(cat.Builtin))
	}
	if cat.Builtin[0].Name != "agent_submit_task" || cat.Builtin[0].Title == "" {
		t.Fatalf("builtin[0] = %#v", cat.Builtin[0])
	}
	for _, b := range cat.Builtin {
		if !live[b.Name] {
			t.Errorf("builtin %s absent from the live tools/list", b.Name)
		}
		if !strings.HasPrefix(b.Name, "agent_") {
			t.Errorf("builtin %s is not an agent_* name", b.Name)
		}
	}
	for name := range live {
		if strings.HasPrefix(name, "agent_") {
			found := false
			for _, b := range cat.Builtin {
				found = found || b.Name == name
			}
			if !found {
				t.Errorf("live tool %s missing from the builtin mirror", name)
			}
		}
	}

	if len(cat.Skills) != 1 || cat.Skills[0].Name != "mal.echo" || !cat.Skills[0].Registered || cat.Skills[0].Title != "Echo" {
		t.Fatalf("skills = %#v, want the one registered mal.echo", cat.Skills)
	}

	if len(cat.Servers) != 1 {
		t.Fatalf("servers = %#v, want the one pooled server", cat.Servers)
	}
	row := cat.Servers[0]
	if row.Name != "filesystem" || row.State != "connected" {
		t.Fatalf("server row = %#v", row)
	}
	if len(row.Tools) != 1 {
		t.Fatalf("server tools = %#v, want the one echo tool", row.Tools)
	}
	tool := row.Tools[0]
	if tool.Name != "filesystem.echo" || tool.Remote != "echo" {
		t.Fatalf("tool row = %#v, want the exposed/remote pair", tool)
	}
	if tool.Title != "original" || tool.Description != "original" || tool.ReadOnly {
		t.Fatalf("tool row metadata = %#v", tool)
	}
}
