package web

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

type registrationOfficeLink struct{ calls int }

func (l *registrationOfficeLink) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	l.calls++
	return json.RawMessage(`{"sessions":[]}`), nil
}

func TestOfficeAgentLoopBindsTrustedConversation(t *testing.T) {
	sk := writeTestSkill(t, t.TempDir(), "docs", "Office", "Read before changing a document.")
	h := newHarness(t, scriptedTurns(toolTurn(tools.OfficeToolName, `{"action":"list"}`), textTurn("done")), func(c *Config) { c.Skills = []skills.Skill{sk} })
	link := &registrationOfficeLink{}
	boundID, boundCwd := "", ""
	h.mgr.officeForSession = func(id, cwd string, owner *identity.Scope, hub *Hub) tools.OfficeLink {
		if owner != nil || hub == nil {
			t.Fatal("classic conversation identity mismatch")
		}
		boundID, boundCwd = id, cwd
		return link
	}
	sid := h.createSession()
	stream := h.start(sid, "List the current Office document")
	end := stream.wait(t, EvToolEnd)
	stream.wait(t, EvRunEnd)
	if end.Name != tools.OfficeToolName || end.IsError || link.calls != 1 || boundID != sid || boundCwd != h.mgr.Cwd() {
		t.Fatalf("Office binding/agent result: %+v calls=%d id=%q cwd=%q", end, link.calls, boundID, boundCwd)
	}
}

func TestOfficeRegistrationFollowsExplicitDocsSelection(t *testing.T) {
	sk := writeTestSkill(t, t.TempDir(), "docs", "Office", "Office skill")
	h, cap := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) { c.Skills = []skills.Skill{sk} })
	h.mgr.officeForSession = func(string, string, *identity.Scope, *Hub) tools.OfficeLink { return &registrationOfficeLink{} }
	skillID := ""
	for id, sk := range h.mgr.skillCatalogIDs() {
		if sk.Name == "docs" {
			skillID = id
		}
	}
	if skillID == "" {
		t.Fatal("missing docs skill identity")
	}
	for _, test := range []struct {
		name      string
		selection *[]string
		want      bool
	}{
		{"legacy", nil, false},
		{"empty", new([]string), false},
		{"docs", &[]string{skillID}, true},
	} {
		body := studioAgentBody(test.name, "", "ready")
		if test.selection != nil {
			body["skills"] = *test.selection
		}
		var def studioAgent
		runtimeSkillResponse(t, h.do("POST", "/api/manage/agents", studioBody(body)), 201, &def)
		conversation := createStudioConversationBody(t, h, def.ID, "")
		studioAsk(t, h, conversation.ID, "hello")
		schemas := cap.toolSchemas()
		present := slices.Contains(schemas[len(schemas)-1], tools.OfficeToolName)
		if present != test.want {
			t.Fatalf("%s Office registration=%v, want %v", test.name, present, test.want)
		}
	}
}
