package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/skillobserve"
	"github.com/yosukeno/pogo/skills"
)

// A portable real declared-tool process. It is reachable only from this
// package's test binary with an explicit fixture argument, never production.
// The Linux sandbox deliberately removes inherited environment variables.
func TestSkillMonitorChildProcess(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "--" || os.Args[len(os.Args)-2] != "pogo-skill-monitor-helper" {
		return
	}
	fmt.Print(strings.Repeat("synthetic-private-skill-result", 200))
	if os.Args[len(os.Args)-1] == "fail" {
		os.Exit(2)
	}
	os.Exit(0)
}

func monitoringFixtureSkill(t *testing.T) skills.Skill {
	t.Helper()
	sk := writeTestSkill(t, t.TempDir(), "observe-fixture", "synthetic fixture only", "# synthetic-private-skill-body\nUse the fixture tools.")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	defs := []any{}
	for _, name := range []string{"echo", "fail"} {
		defs = append(defs, map[string]any{
			"name": "fixture." + name, "description": "synthetic fixture " + name,
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
			"annotations": map[string]any{"readOnlyHint": true, "idempotentHint": true},
			"x-pogo-exec": map[string]any{"argv": []string{executable, "-test.run=^TestSkillMonitorChildProcess$", "--", "pogo-skill-monitor-helper", name}, "map": map[string]any{}},
		})
	}
	data, _ := json.Marshal(map[string]any{"tools": defs})
	if err := os.WriteFile(filepath.Join(sk.Dir, "tools.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return sk
}

func TestSkillMonitorRealInternalExternalTrace(t *testing.T) {
	for _, key := range []string{"POGO_MCP", "POGO_MCP_TOKEN", "POGO_MCP_POLICY"} {
		t.Setenv(key, "")
	}
	sk := monitoringFixtureSkill(t)
	args, _ := json.Marshal(map[string]any{"path": sk.Path})
	h := newHarness(t, scriptedTurns(toolTurn("read", string(args)), toolTurn("fixture_echo", `{}`), textTurn("done")), func(c *Config) { c.Skills = []skills.Skill{sk} })
	s, err := NewServer(h.mgr, ServerOptions{Token: h.token, MCP: &MCPOptions{Token: h.token, Policy: "auto"}, MCPLog: &MCPLogOptions{DBPath: filepath.Join(t.TempDir(), "observe.db"), PayloadOff: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.mgr.Close(); s.Close() })
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	cs := connectStreamable(t, ts.URL, h.token)
	if r := callTool(t, cs, "fixture.echo", map[string]any{}); r.IsError {
		t.Fatalf("external tool: %s", resultText(r))
	}
	if r := callTool(t, cs, "fixture.fail", map[string]any{}); !r.IsError {
		t.Fatal("failing process represented as success")
	}
	submission := structured(t, callTool(t, cs, "agent_submit_task", map[string]any{"prompt": "exercise the synthetic fixture"}))
	sid := submission["session_id"].(string)
	runID := submission["run_id"].(string)
	pollStatus(t, cs, sid)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.skillCalls.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.mcpCalls.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := s.skillCalls.Query(ctx, skillobserve.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var external, internal, loads, discoveries int
	for _, e := range p.Events {
		if e.SkillName != sk.Name || e.SkillID == "" {
			t.Fatalf("incorrect ownership: %+v", e)
		}
		switch e.Stage {
		case "discovery":
			discoveries++
		case "load":
			loads++
			if e.Status != "ok" || e.Mechanism != "file_read" {
				t.Fatalf("instruction read: %+v", e)
			}
		case "execute":
			if e.Caller == "external" {
				external++
				if e.WireTool != e.Tool {
					t.Fatalf("external MCP wire name changed: %+v", e)
				}
				if e.RunID != "" || e.SessionID != "" || e.Entrypoint != "mcp" {
					t.Fatalf("fabricated external run: %+v", e)
				}
				parent, err := s.mcpCalls.Detail(ctx, e.ParentID)
				if err != nil || parent.Direction != "inbound" || parent.TraceID != e.TraceID {
					t.Fatalf("direct invocation lost MCP parent: %+v %v", e, err)
				}
			} else {
				internal++
				if e.Caller != "internal" || e.Entrypoint != "mcp" || e.SessionID != sid || e.RunID != runID || e.TaskID != sid || e.ToolCallID == "" || e.Status != "ok" {
					t.Fatalf("nested task attribution: %+v", e)
				}
				parent, err := s.mcpCalls.Detail(ctx, e.ParentID)
				if err != nil || parent.Tool != "agent_submit_task" || parent.TraceID != e.TraceID {
					t.Fatalf("task lost submission parent: %+v %v", e, err)
				}
			}
		}
	}
	if external != 2 || internal != 1 || loads != 1 || discoveries != 1 {
		t.Fatalf("events duplicated or absent: external=%d internal=%d load=%d discovery=%d %+v", external, internal, loads, discoveries, p.Events)
	}
	data, _ := json.Marshal(p)
	for _, private := range []string{"synthetic-private-skill", sk.Path, sk.Dir} {
		if strings.Contains(string(data), private) {
			t.Fatalf("private content or path in events: %q", private)
		}
	}
	// The external submit remains one MCP call; discovery/read/local execute
	// stay only in the skill event table.
	mcpPage, err := s.mcpCalls.Query(ctx, mcpobserve.Filter{Tool: "fixture.echo", Limit: 100})
	if err != nil || len(mcpPage.Calls) != 1 {
		t.Fatalf("skill operations contaminated MCP calls: %+v %v", mcpPage, err)
	}
}

func TestSkillMonitorExplicitInstructionRead(t *testing.T) {
	sk := writeTestSkill(t, t.TempDir(), "manual-fixture", "fixture", "private instruction text")
	sk.DisableModelInvocation = true
	h := newHarness(t, scriptedTurns(textTurn("done")), func(c *Config) { c.Skills = []skills.Skill{sk} })
	s, err := NewServer(h.mgr, ServerOptions{Token: h.token})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sid := h.createSession()
	resp := h.do(http.MethodPost, "/api/sessions/"+sid+"/messages", `{"prompt":"/skill:manual-fixture extra"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("submit=%d", resp.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.skillCalls.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := s.skillCalls.Query(ctx, skillobserve.Filter{SkillName: sk.Name, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Events) != 1 || p.Events[0].Stage != "load" || p.Events[0].Mechanism != "explicit" || p.Events[0].Caller != "internal" || p.Events[0].Status != "ok" || p.Events[0].SessionID != sid {
		t.Fatalf("explicit load or hidden discovery: %+v", p.Events)
	}
}

func TestSkillMonitorExplicitReadFailure(t *testing.T) {
	sk := writeTestSkill(t, t.TempDir(), "unreadable-fixture", "fixture", "private instructions")
	h := newHarness(t, scriptedTurns(textTurn("done")), func(c *Config) { c.Skills = []skills.Skill{sk} })
	rec := &skillObserveRecorder{}
	h.mgr.SetSkillRecorder(rec)
	sid := h.createSession()
	if err := os.Remove(sk.Path); err != nil {
		t.Fatal(err)
	}
	resp := h.do(http.MethodPost, "/api/sessions/"+sid+"/messages", `{"prompt":"/skill:unreadable-fixture"}`)
	resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatalf("unreadable instructions accepted: %d", resp.StatusCode)
	}
	events := rec.finals()
	if len(events) != 1 {
		t.Fatalf("expected one failed read without a run: %+v", events)
	}
	e := events[0]
	if e.Stage != "load" || e.Status != "error" || e.ErrorKind != "read_error" || e.SessionID != sid || e.RunID != "" {
		t.Fatalf("failed read correlation: %+v", e)
	}
	data, _ := json.Marshal(e)
	if strings.Contains(string(data), sk.Path) || strings.Contains(string(data), "private instructions") {
		t.Fatal("read failure leaked instructions or path")
	}
}
