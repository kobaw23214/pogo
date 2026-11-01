package web

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/information"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func TestInformationHostReadPassesAllPolicyModes(t *testing.T) {
	for _, mode := range []Mode{ModeStrict, ModeStandard, ModeAuto} {
		policy := NewPolicy()
		policy.Set(mode, 0)
		if _, auto := policy.Decide(agent.GateRequest{ToolName: tools.InformationToolName, Args: json.RawMessage(`{"action":"read","id":"inf_example"}`)}); !auto {
			t.Fatalf("read-only host information blocked in %s", mode)
		}
		if mode != ModeAuto {
			if _, auto := policy.Decide(agent.GateRequest{ToolName: "arbitrary_information_writer"}); auto {
				t.Fatal("library grant widened unknown tools")
			}
		}
	}
}

func TestInformationOrdinaryAndStudioSessionsUseSameTrustedLibrary(t *testing.T) {
	marker := "shared-information-session-142"
	var entryID string
	turns := []llm.Response{textTurn("ordinary ready"), toolTurn(tools.InformationToolName, `{"action":"read","id":"placeholder"}`), textTurn("read complete")}
	h, cap := newStudioHarness(t, turns)
	store, err := h.mgr.InformationStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	view, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	_, entry, err := store.Create(view.Revision, information.CreateInput{Kind: "shortcut", Name: "共享资料", Shortcut: &information.Shortcut{ID: "shared", Title: "共享资料", Content: marker}})
	if err != nil {
		t.Fatal(err)
	}
	entryID = entry.ID
	// Replace the scripted tool args before any model call; the real Manager
	// builds both registries and executes the Studio read on its own store.
	turns[1] = toolTurn(tools.InformationToolName, `{"action":"read","id":"`+entryID+`"}`)
	ordinary := h.createSession()
	studioAsk(t, h, ordinary, "ordinary question")
	a := createStudioAgent(t, h, "Information Bot", "Read shared reference data.", "ready")
	conversation := createStudioConversationBody(t, h, a.ID, "")
	studioAsk(t, h, conversation.ID, "read information")
	schemas := cap.toolSchemas()
	if len(schemas) != 3 || !slices.Contains(schemas[0], tools.InformationToolName) || !slices.Contains(schemas[1], tools.InformationToolName) {
		t.Fatalf("ordinary/Studio schemas: %v", schemas)
	}
	_, _, histories, _ := cap.snapshot()
	found := false
	for _, message := range histories[2] {
		for _, block := range message.Content {
			if block.Type == llm.BlockToolResult && strings.Contains(block.Text, marker) {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("Manager's actual information.read did not reach model")
	}
}
