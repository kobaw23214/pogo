package web

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
)

func TestHubSettledEmptyMessageHasArrayContent(t *testing.T) {
	for _, name := range []string{"empty", "interrupted"} {
		t.Run(name, func(t *testing.T) {
			h := NewHub()
			_, events, cancel := h.Subscribe(0)
			defer cancel()
			e := Event{Type: EvMessage, Role: string(llm.RoleAssistant)}
			if name == "interrupted" {
				var ok bool
				e, ok = fromAgentEvent(agent.Event{Kind: agent.EventMessage})
				if !ok {
					t.Fatal("interrupted settled message was not translated")
				}
			}
			h.Publish(e)
			settled := <-events
			if settled.Role != string(llm.RoleAssistant) || settled.Content == nil || len(settled.Content) != 0 {
				t.Fatalf("unstable settled message: %+v", settled)
			}
			// SSE's union retains omitempty: absent content is valid for an
			// empty message, and consumers normalize it to the same empty list.
			data, err := json.Marshal(settled)
			if err != nil {
				t.Fatal(err)
			}
			var eventJSON map[string]json.RawMessage
			if err := json.Unmarshal(data, &eventJSON); err != nil {
				t.Fatal(err)
			}
			if content, present := eventJSON["content"]; present && string(content) != "[]" {
				t.Fatalf("empty SSE content = %s", content)
			}
			assertHubMessageContentJSON(t, h.Snapshot(), 0, "[]")
		})
	}
}

func TestHubSettledMessagePreservesTextAndToolBlocks(t *testing.T) {
	h := NewHub()
	content := []llm.Block{
		{Type: llm.BlockText, Text: "checking"},
		{Type: llm.BlockToolUse, ID: "c1", Name: "read", Input: json.RawMessage(`{"path":"notes.txt"}`)},
	}
	_, events, cancel := h.Subscribe(0)
	defer cancel()
	h.Publish(Event{Type: EvMessage, Role: string(llm.RoleAssistant), Content: content})
	settled := <-events
	if !reflect.DeepEqual(settled.Content, content) || !reflect.DeepEqual(h.Snapshot().Messages[0].Content, content) {
		t.Fatal("message normalization changed text or tool blocks")
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	assertHubMessageContentJSON(t, h.Snapshot(), 0, string(encoded))
}

func TestHubSnapshotNormalizesPreviouslyAccumulatedNilContent(t *testing.T) {
	h := NewHub()
	h.msgs = []Message{{ID: "m1", Content: nil}, {ID: "u2", Role: string(llm.RoleUser), Content: []llm.Block{{Type: llm.BlockText, Text: "next"}}}}
	snapshot := h.Snapshot()
	assertHubMessageContentJSON(t, snapshot, 0, "[]")
	if snapshot.Messages[0].Role != string(llm.RoleAssistant) || snapshot.Messages[1].Role != string(llm.RoleUser) || snapshot.Messages[1].Content[0].Text != "next" {
		t.Fatalf("snapshot changed message identity or contents: %+v", snapshot.Messages)
	}
	// Read-time compatibility must not mutate the Hub's historical slices.
	if h.msgs[0].Content != nil || h.msgs[0].Role != "" {
		t.Fatal("snapshot normalization mutated the Hub")
	}
}

func assertHubMessageContentJSON(t *testing.T, snapshot Snapshot, index int, want string) {
	t.Helper()
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Messages) <= index || string(decoded.Messages[index].Content) != want {
		t.Fatalf("snapshot message %d content = %s, want %s", index, data, want)
	}
}
