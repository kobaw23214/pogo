package web

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

func sessionAccessCall(t *testing.T, tool tools.Tool, arguments map[string]any) (tools.Result, error) {
	t.Helper()
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	return tool.Execute(context.Background(), raw)
}

func sessionAccessTool(t *testing.T, registry *tools.Registry, name string) tools.Tool {
	t.Helper()
	tool, ok := registry.Get(name)
	if !ok {
		t.Fatalf("missing file tool %s", name)
	}
	return tool
}

func fileAccessTestSession(cwd, root string) *Session {
	s := &Session{cwd: cwd, root: root, policy: NewPolicy(), hub: NewHub()}
	s.gate = NewWebGate(s.hub, s.policy, 0)
	return s
}

func TestSessionFullAccessFollowsLivePolicyWithoutRebuildingTools(t *testing.T) {
	base := t.TempDir()
	account, foreign := filepath.Join(base, "alice"), filepath.Join(base, "bob")
	work, sibling := filepath.Join(account, "agents", "current"), filepath.Join(account, "agents", "reference")
	for _, dir := range []string{work, sibling, foreign} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	fixture := filepath.Join(sibling, "reference.txt")
	if err := os.WriteFile(fixture, []byte("SIBLING-REFERENCE"), 0600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(foreign, "other.txt")
	if err := os.WriteFile(secret, []byte("OTHER-ACCOUNT"), 0600); err != nil {
		t.Fatal(err)
	}
	s := fileAccessTestSession(work, account)
	registry := tools.New(tools.Options{Cwd: work, FileAccess: s.fileAccess(), SpillDir: t.TempDir()})
	read, list, write := sessionAccessTool(t, registry, "read"), sessionAccessTool(t, registry, "ls"), sessionAccessTool(t, registry, "write")
	for _, mode := range []Mode{ModeStandard, ModeStrict} {
		s.SetPolicy(mode, 0)
		if _, err := sessionAccessCall(t, read, map[string]any{"path": fixture}); err == nil {
			t.Fatalf("%s reached the sibling project", mode)
		}
	}
	s.SetPolicy(ModeAuto, 0)
	result, err := sessionAccessCall(t, read, map[string]any{"path": fixture})
	if err != nil || !strings.Contains(result.Text, "SIBLING-REFERENCE") {
		t.Fatalf("existing read did not pick up full access: %q %v", result.Text, err)
	}
	result, err = sessionAccessCall(t, list, map[string]any{"path": filepath.Dir(work)})
	if err != nil || !strings.Contains(result.Text, "reference/") {
		t.Fatalf("existing ls cannot list the Agent parent: %q %v", result.Text, err)
	}
	output := filepath.Join(sibling, "updated.txt")
	if _, err := sessionAccessCall(t, write, map[string]any{"path": output, "content": "UPDATED-REFERENCE"}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(output); err != nil || string(raw) != "UPDATED-REFERENCE" {
		t.Fatalf("full access did not write the sibling project: %q %v", raw, err)
	}
	if _, err := sessionAccessCall(t, read, map[string]any{"path": secret}); err == nil {
		t.Fatal("full access crossed the account root")
	}
	if _, err := sessionAccessCall(t, write, map[string]any{"path": filepath.Join(foreign, "new.txt"), "content": "forbidden"}); err == nil {
		t.Fatal("full access wrote another account")
	}
	s.SetPolicy(ModeStandard, 0)
	if _, err := sessionAccessCall(t, read, map[string]any{"path": fixture}); err == nil {
		t.Fatal("existing read retained access after leaving auto mode")
	}
	if _, err := sessionAccessCall(t, write, map[string]any{"path": output, "content": "SHOULD-NOT-WRITE"}); err == nil {
		t.Fatal("existing write retained access after leaving auto mode")
	}
	if raw, _ := os.ReadFile(output); string(raw) != "UPDATED-REFERENCE" {
		t.Fatal("denied write changed the sibling file")
	}
}

func TestSessionFullAccessLimitedAutoExpiresAndDoesNotWidenOtherSessions(t *testing.T) {
	root := t.TempDir()
	work, sibling := filepath.Join(root, "current"), filepath.Join(root, "reference")
	for _, path := range []string{work, sibling} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	one := fileAccessTestSession(work, root)
	two := fileAccessTestSession(work, root)
	oneRegistry := tools.New(tools.Options{Cwd: work, FileAccess: one.fileAccess(), SpillDir: t.TempDir()})
	twoRegistry := tools.New(tools.Options{Cwd: work, FileAccess: two.fileAccess(), SpillDir: t.TempDir()})
	oneList, twoList := sessionAccessTool(t, oneRegistry, "ls"), sessionAccessTool(t, twoRegistry, "ls")
	one.SetPolicy(ModeAuto, 1)
	one.spendAutoTurn()
	if _, err := sessionAccessCall(t, oneList, map[string]any{"path": sibling}); err != nil {
		t.Fatalf("the permitted auto turn was narrowed early: %v", err)
	}
	if _, err := sessionAccessCall(t, twoList, map[string]any{"path": sibling}); err == nil {
		t.Fatal("one session's full access widened another session")
	}
	one.spendAutoTurn()
	if one.policy.State().Mode != string(ModeStandard) {
		t.Fatal("limited auto did not revert")
	}
	if _, err := sessionAccessCall(t, oneList, map[string]any{"path": sibling}); err == nil {
		t.Fatal("a captured tool retained the expired auto grant")
	}
}

func lastSessionFileResult(t *testing.T, s *Session) llm.Block {
	t.Helper()
	s.wait()
	messages := s.agent.Messages()
	for i := len(messages) - 1; i >= 0; i-- {
		for j := len(messages[i].Content) - 1; j >= 0; j-- {
			if result := messages[i].Content[j]; result.Type == llm.BlockToolResult {
				return result
			}
		}
	}
	t.Fatal("run produced no file tool result")
	return llm.Block{}
}

func TestStudioFullAccessChangesAnExistingConversationFileBoundary(t *testing.T) {
	root := t.TempDir()
	sibling := filepath.Join(root, "reference")
	if err := os.Mkdir(sibling, 0700); err != nil {
		t.Fatal(err)
	}
	fixture, output := filepath.Join(sibling, "source.txt"), filepath.Join(sibling, "copy.txt")
	if err := os.WriteFile(fixture, []byte("STUDIO-SIBLING-SOURCE"), 0600); err != nil {
		t.Fatal(err)
	}
	readArgs, _ := json.Marshal(map[string]any{"path": fixture})
	writeArgs, _ := json.Marshal(map[string]any{"path": output, "content": "STUDIO-SIBLING-COPY"})
	h, capture := newStudioHarness(t, []llm.Response{
		toolTurn("read", string(readArgs)), textTurn("denied"),
		toolTurn("read", string(readArgs)), textTurn("read"),
		toolTurn("write", string(writeArgs)), textTurn("written"),
		toolTurn("read", string(readArgs)), textTurn("denied again"),
	}, func(c *Config) { c.Cwd = root })
	a := createStudioAgent(t, h, "Full access", "Use the selected workspace permissions.", "ready")
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	loop := s.agent
	studioAsk(t, h, sid, "read reference before full access")
	if !lastSessionFileResult(t, s).IsError {
		t.Fatal("standard conversation read a sibling project")
	}
	s.SetPolicy(ModeAuto, 0)
	studioAsk(t, h, sid, "read reference with full access")
	if result := lastSessionFileResult(t, s); result.IsError || !strings.Contains(result.Text, "STUDIO-SIBLING-SOURCE") {
		t.Fatalf("full-access registered read failed: %+v", result)
	}
	studioAsk(t, h, sid, "write reference with full access")
	if result := lastSessionFileResult(t, s); result.IsError {
		t.Fatalf("full-access registered write failed: %+v", result)
	}
	if raw, err := os.ReadFile(output); err != nil || string(raw) != "STUDIO-SIBLING-COPY" {
		t.Fatalf("sibling write did not complete: %q %v", raw, err)
	}
	s.SetPolicy(ModeStandard, 0)
	studioAsk(t, h, sid, "read reference after full access")
	if !lastSessionFileResult(t, s).IsError || s.agent != loop {
		t.Fatal("switching policy replaced the agent or retained sibling access")
	}
	systems, _, _, _ := capture.snapshot()
	if len(systems) != 8 {
		t.Fatalf("model calls=%d, want 8", len(systems))
	}
	for i, full := range []bool{false, false, true, true, true, true, false, false} {
		if strings.Contains(systems[i], "full access is enabled") != full {
			t.Fatalf("model call %d used stale file access guidance: %s", i, systems[i])
		}
	}
}

func TestStudioFullAccessSurvivesNativeAndSkillRefresh(t *testing.T) {
	root := t.TempDir()
	sibling := filepath.Join(root, "reference")
	if err := os.Mkdir(sibling, 0700); err != nil {
		t.Fatal(err)
	}
	fixture, output := filepath.Join(sibling, "source.txt"), filepath.Join(sibling, "copy.txt")
	if err := os.WriteFile(fixture, []byte("REFRESH-SIBLING-SOURCE"), 0600); err != nil {
		t.Fatal(err)
	}
	sk := writeTestSkill(t, t.TempDir(), "access-refresh", "Access refresh fixture", "Keep the current access policy.")
	readArgs, _ := json.Marshal(map[string]any{"path": fixture})
	writeArgs, _ := json.Marshal(map[string]any{"path": output, "content": "REFRESH-SIBLING-COPY"})
	h, _ := newStudioHarness(t, []llm.Response{
		toolTurn("read", string(readArgs)), textTurn("read"),
		toolTurn("write", string(writeArgs)), textTurn("written"),
		toolTurn("read", string(readArgs)), textTurn("denied"),
	}, func(c *Config) { c.Cwd = root; c.Skills = []skills.Skill{sk} })
	a := postStudioAgent(t, h, studioAgentCapsBody("Full refresh", "ready", map[string]any{
		"skills": []string{}, "tools": []string{"read"}, "mcp_servers": []string{},
	}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	loop := s.agent
	s.SetPolicy(ModeAuto, 0)
	studioAsk(t, h, sid, "read before native and Skill refresh")
	if result := lastSessionFileResult(t, s); result.IsError {
		t.Fatalf("initial full access failed: %+v", result)
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{skillIDOf(sk)}, "tools": []string{"read", "write"}})
	studioAsk(t, h, sid, "write after native and Skill refresh")
	if result := lastSessionFileResult(t, s); result.IsError {
		t.Fatalf("refreshed native tool lost full access: %+v", result)
	}
	if raw, err := os.ReadFile(output); err != nil || string(raw) != "REFRESH-SIBLING-COPY" {
		t.Fatalf("refreshed write did not complete: %q %v", raw, err)
	}
	s.SetPolicy(ModeStandard, 0)
	studioAsk(t, h, sid, "read after access is narrowed")
	if !lastSessionFileResult(t, s).IsError || s.agent != loop {
		t.Fatal("refreshed tool ignored policy revocation or replaced the conversation")
	}
}

func TestSessionFileAccessPromptTracksPolicy(t *testing.T) {
	s := fileAccessTestSession("/account/agents/current", "/account")
	if prompt := s.fileAccessPrompt(); !strings.Contains(prompt, "limited to the working directory") || strings.Contains(prompt, "full access is enabled") {
		t.Fatalf("standard prompt=%q", prompt)
	}
	s.SetPolicy(ModeAuto, 1)
	if prompt := s.fileAccessPrompt(); !strings.Contains(prompt, "full access is enabled") || !strings.Contains(prompt, `"/account"`) || !strings.Contains(prompt, `"/account/agents/current"`) {
		t.Fatalf("auto prompt=%q", prompt)
	}
	s.spendAutoTurn()
	s.spendAutoTurn()
	if strings.Contains(s.fileAccessPrompt(), "full access is enabled") {
		t.Fatal("prompt retained an expired full-access grant")
	}
}
