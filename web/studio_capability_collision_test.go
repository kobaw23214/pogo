package web

import (
	"context"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
)

// Inspect the already committed registry directly. Session.Start would refresh
// MCP first and could conceal a missing idle reprojection after a Skill edit.
func studioCollisionRuntimeSchema(t *testing.T, s *Session, model *catalogModel) llm.ToolSchema {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	events := s.agent.Run(ctx, "inspect the current capability registry")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range events {
		}
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("direct registry inspection did not stop after cancellation")
		}
	}()
	var call catalogModelCall
	select {
	case call = <-model.requests:
	case <-ctx.Done():
		t.Fatal("model never received the committed registry", ctx.Err())
	}
	call.reply <- textTurn("done")
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("direct registry inspection did not settle", ctx.Err())
	}
	return modelSchema(t, call, "mcp_alpha_echo")
}

func TestStudioCapabilityCollisionSkillOverridesAndRestoresMCPWhileIdle(t *testing.T) {
	h, model, a, s := newStudioMCPPolicyHarness(t, true)
	sk := writeManifestSkill(t, t.TempDir(), "collision", "COLLISION-SKILL", "mcp.alpha.echo")
	h.mgr.SetSkills([]skills.Skill{sk})
	if schema := studioCollisionRuntimeSchema(t, s, model); schema.Description != "original" {
		t.Fatalf("initial runtime did not contain the external tool: %+v", schema)
	}
	assertMatchesNewChat := func(wantDescription string) {
		t.Helper()
		oldSchema := studioCollisionRuntimeSchema(t, s, model)
		newSession := h.session(startStudioConversation(t, h, a.ID))
		newSchema := studioCollisionRuntimeSchema(t, newSession, model)
		if !strings.Contains(oldSchema.Description, wantDescription) || oldSchema.Description != newSchema.Description {
			t.Fatalf("idle old/new registry mismatch: old=%q new=%q, want %q", oldSchema.Description, newSchema.Description, wantDescription)
		}
	}

	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{skillIDOf(sk)}})
	if got := sessionSkillNamesHTTP(t, h, s.ID); !slices.Equal(got, []string{"collision"}) {
		t.Fatalf("same-name MCP blocked the Skill selection: %v", got)
	}
	s.policy.mu.Lock()
	_, externalPresent := s.policy.mcpTools["mcp_alpha_echo"]
	trusted := s.policy.mcpTrusted["mcp_alpha_echo"]
	s.policy.mu.Unlock()
	if externalPresent || trusted {
		t.Fatal("MCP trust leaked onto the Skill that took over its wire name")
	}
	assertMatchesNewChat("collision declared tool")

	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{}})
	assertMatchesNewChat("original")
	s.policy.mu.Lock()
	_, externalPresent = s.policy.mcpTools["mcp_alpha_echo"]
	trusted = s.policy.mcpTrusted["mcp_alpha_echo"]
	s.policy.mu.Unlock()
	if !externalPresent || !trusted {
		t.Fatal("removing the Skill did not immediately restore the selected external tool and its server trust")
	}

	// Selecting the Skill and revoking MCP in one save must settle both
	// dimensions; a second manual synchronization must not be required.
	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{skillIDOf(sk)}, "mcp_servers": []string{}})
	if got := sessionSkillNamesHTTP(t, h, s.ID); !slices.Equal(got, []string{"collision"}) {
		t.Fatalf("combined Skill/MCP edit stayed partly unapplied: %v", got)
	}
	assertMatchesNewChat("collision declared tool")
	if got := studioMCPGroupsHTTP(t, h, s.ID); len(got) != 0 {
		t.Fatalf("combined edit retained revoked external servers: %v", got)
	}
}

func TestStudioCapabilityCollisionFailedSkillCommitPreservesExternalRuntime(t *testing.T) {
	h, model, a, s := newStudioMCPPolicyHarness(t, true)
	sk := writeManifestSkill(t, t.TempDir(), "collision", "COLLISION-SKILL", "mcp.alpha.echo")
	h.mgr.SetSkills([]skills.Skill{sk})
	s.Policy().AllowTool("mcp_alpha_echo")
	input := a.studioInput
	selected := []string{skillIDOf(sk)}
	input.Skills = &selected
	if _, err := h.mgr.studio.put(a.ID, input, a.Revision); err != nil {
		t.Fatal(err)
	}
	before, _ := h.mgr.studio.binding(s.ID)
	path, backup := h.mgr.studio.path, h.mgr.studio.path+".collision-backup"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore := func() {
		if !restored {
			_ = os.Remove(path)
			_ = os.Rename(backup, path)
			restored = true
		}
	}
	defer restore()
	if _, err := s.SyncStudioSkillPolicy(); err == nil {
		t.Fatal("failed Skill persistence was accepted")
	}
	after, _ := h.mgr.studio.binding(s.ID)
	s.policy.mu.Lock()
	_, externalPresent := s.policy.mcpTools["mcp_alpha_echo"]
	remembered, trusted := s.policy.allowTool["mcp_alpha_echo"], s.policy.mcpTrusted["mcp_alpha_echo"]
	s.policy.mu.Unlock()
	if !reflect.DeepEqual(before, after) || len(s.invocationSkills) != 0 || !externalPresent || !remembered || !trusted {
		t.Fatal("failed Skill commit changed its binding, external registry or approval policy")
	}
	if schema := studioCollisionRuntimeSchema(t, s, model); schema.Description != "original" {
		t.Fatalf("failed Skill commit changed the executable MCP schema: %+v", schema)
	}
	restore()
	if result, err := s.SyncStudioSkillPolicy(); err != nil || !result.Changed {
		t.Fatalf("retry did not apply the Skill and external projection together: %+v %v", result, err)
	}
	if schema := studioCollisionRuntimeSchema(t, s, model); !strings.Contains(schema.Description, "collision declared tool") {
		t.Fatalf("retry did not select the colliding Skill implementation: %+v", schema)
	}
}
