package web

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

// The context meter's itemized overhead (agent.OverheadBreakdown) reaches the
// browser through snapshots, model_changed and — when the tool view changed
// it — turn_start. These tests hold the wire to that contract with a real
// Studio Agent bound to the embedded agent-workflows Skill, whose tool group
// is the overhead change the breakdown exists to explain.

func overheadPart(parts []agent.OverheadPart, kind, name string) (agent.OverheadPart, bool) {
	for _, p := range parts {
		if p.Kind == kind && p.Name == name {
			return p, true
		}
	}
	return agent.OverheadPart{}, false
}

// Refreshes update the hub without an event, so whether a turn_start carries
// the figure is decided against what the stream last delivered.
func TestHubCarriesOverheadOnlyWhenTheStreamLacksIt(t *testing.T) {
	h := NewHub()
	base := []agent.OverheadPart{{Kind: agent.OverheadPrompt, Name: agent.PromptPartBase, Tokens: 100}}
	grown := append(append([]agent.OverheadPart(nil), base...), agent.OverheadPart{Kind: agent.OverheadTool, Name: "read", Tokens: 20})
	h.SetOverhead(100, base)
	if !h.OverheadUnstreamed(100, base) {
		t.Fatal("a figure no event carried counted as delivered")
	}
	total := int64(100)
	h.Publish(Event{Type: EvTurnStart, Turn: 1, OverheadTokens: &total, OverheadBreakdown: base})
	if h.OverheadUnstreamed(100, base) {
		t.Fatal("a delivered figure was sent again")
	}
	h.SetOverhead(120, grown)
	if !h.OverheadUnstreamed(120, grown) {
		t.Fatal("a silent refresh was never delivered")
	}
	if s := h.Snapshot(); s.OverheadTokens != 120 || len(s.OverheadBreakdown) != 2 {
		t.Fatalf("snapshot overhead %d %+v", s.OverheadTokens, s.OverheadBreakdown)
	}
}

func TestStudioContextBreakdownFollowsTheToolView(t *testing.T) {
	loaded := bindingTestSkills(t)
	sk, ok := skills.Find(loaded, "agent-workflows")
	if !ok {
		t.Fatal("embedded agent-workflows Skill unavailable")
	}
	h, capture := newStudioHarness(t, []llm.Response{
		toolTurn("read", fmt.Sprintf(`{"path":%q}`, sk.Path)),
		textTurn(strings.Repeat("progress narrative ", 3000)),
		textTurn("short summary of the work"),
		textTurn("after compaction"),
	})
	h.mgr.SetSkills(loaded)
	a := postStudioAgent(t, h, studioAgentCapsBody("flows", "ready", map[string]any{
		"tools":       []string{tools.AgentWorkflowReadName, tools.AgentWorkflowEditName, tools.AgentWorkflowEnableName, tools.AgentWorkflowRunName},
		"skills":      []string{bindingSkillID(t, loaded, "agent-workflows")},
		"mcp_servers": []string{},
	}), http.StatusCreated)
	sid := startStudioConversation(t, h, a.ID)

	stream := h.stream(sid, "")
	first := stream.next(t)
	if first.Type != EvSnapshot || first.Snapshot == nil {
		t.Fatalf("first frame = %s, want snapshot", first.Type)
	}
	snap := first.Snapshot
	for _, key := range []string{agent.PromptPartBase, agent.PromptPartSkills, agent.PromptPartAgent, agent.PromptPartSkillTools} {
		if _, ok := overheadPart(snap.OverheadBreakdown, agent.OverheadPrompt, key); !ok {
			t.Fatalf("snapshot breakdown lacks prompt section %s: %+v", key, snap.OverheadBreakdown)
		}
	}
	if _, ok := overheadPart(snap.OverheadBreakdown, agent.OverheadSkillTools, "agent-workflows"); ok {
		t.Fatal("the withheld group was counted before the Skill loaded")
	}

	h.post("/api/sessions/"+sid+"/messages", `{"prompt":"build me a flow"}`, http.StatusAccepted)
	turn1, turn2 := stream.wait(t, EvTurnStart), stream.wait(t, EvTurnStart)
	stream.wait(t, EvRunEnd)
	if turn1.OverheadTokens == nil || turn2.OverheadTokens == nil {
		t.Fatal("turn_start did not carry an overhead the stream had not delivered")
	}
	group, ok := overheadPart(turn2.OverheadBreakdown, agent.OverheadSkillTools, "agent-workflows")
	if !ok || group.Count != 4 || *turn2.OverheadTokens <= *turn1.OverheadTokens {
		t.Fatalf("loading the Skill was not itemized: %d -> %d %+v", *turn1.OverheadTokens, *turn2.OverheadTokens, turn2.OverheadBreakdown)
	}
	systems, _, _, _ := capture.snapshot()
	names := capture.toolSchemas()
	if systems[0] != systems[1] {
		t.Fatal("loading the Skill changed the system prompt")
	}
	if n := len(names[1]); strings.Join(names[1][:len(names[0])], ",") != strings.Join(names[0], ",") ||
		strings.Join(names[1][n-4:], ",") != strings.Join(tools.SkillBindingTools("agent-workflows"), ",") {
		t.Fatalf("the group was not appended after the earlier declarations: %v -> %v", names[0], names[1])
	}

	// Compaction restores the Skill, so the group and its cost survive it.
	h.post("/api/sessions/"+sid+"/control", `{"action":"compact"}`, http.StatusOK)
	stream.wait(t, EvCompacted)
	stream.close()
	continuation := h.session(sid).agent.Messages()[0]
	if len(continuation.Content) != 2 || continuation.Content[1].InputSkill != "agent-workflows" {
		t.Fatalf("continuation lacks the restored Skill: %+v", continuation.Content)
	}
	if _, ok := overheadPart(h.session(sid).Hub().Snapshot().OverheadBreakdown, agent.OverheadSkillTools, "agent-workflows"); !ok {
		t.Fatal("compaction dropped the group from the breakdown")
	}
	// A reopened session derives the same view from the persisted branch.
	h.mgr.evictAll()
	reopened := h.session(sid)
	total, parts := reopened.agent.OverheadBreakdown()
	if _, ok := overheadPart(parts, agent.OverheadSkillTools, "agent-workflows"); !ok || total != reopened.Hub().Snapshot().OverheadTokens {
		t.Fatalf("reopened view differs: agent %d hub %d %+v", total, reopened.Hub().Snapshot().OverheadTokens, parts)
	}

	again := h.stream(sid, "")
	again.next(t)
	h.post("/api/sessions/"+sid+"/messages", `{"prompt":"continue"}`, http.StatusAccepted)
	again.wait(t, EvTurnStart)
	again.wait(t, EvRunEnd)
	again.close()
	names = capture.toolSchemas()
	_, _, histories, _ := capture.snapshot()
	last := histories[len(histories)-1]
	if !strings.Contains(strings.Join(names[len(names)-1], ","), tools.AgentWorkflowEditName) ||
		len(last) == 0 || !strings.Contains(studioMessageText(last[0]), `<skill name="agent-workflows"`) {
		t.Fatal("the request after compaction lacks the restored Skill or its tools")
	}
}
