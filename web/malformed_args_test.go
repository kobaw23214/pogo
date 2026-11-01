package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

type malformedArgsClient struct {
	model   string
	respond func([]llm.Message) (llm.Response, error)
}

func (c *malformedArgsClient) Model() string { return c.model }
func (c *malformedArgsClient) Stream(_ context.Context, _ string, messages []llm.Message, _ []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	if _, err := json.Marshal(messages); err != nil {
		return llm.Response{}, fmt.Errorf("model retry history cannot be serialized: %w", err)
	}
	return c.respond(messages)
}

// A rejected model call must be visible, retryable and persistable together:
// merely escaping the tool_start event still leaves the assistant message and
// reconnect snapshot unable to marshal, which was the reported regression.
func TestMalformedShortcutArgumentsRecoverAcrossSSEPersistenceAndReload(t *testing.T) {
	for _, grant := range []string{"shortcut_manage", "shortcut_create"} {
		t.Run(grant, func(t *testing.T) { exerciseMalformedShortcutRecovery(t, grant) })
	}
}

func exerciseMalformedShortcutRecovery(t *testing.T, grant string) {
	t.Helper()
	const bad = `{"action":"create","request_id":"json-retry","root_id":"root","nodes":[{"local_id":"root","title":"Literal shell text","parts":[{"kind":"text","text":"echo \$HOME"}]}]}`
	good := strings.Replace(bad, `\$`, `\\$`, 1)
	if json.Valid([]byte(bad)) || !json.Valid([]byte(good)) {
		t.Fatal("fixture must distinguish invalid JSON from a legitimate literal backslash")
	}
	var h *harness
	var s *Session
	var turns atomic.Int32
	h = newHarness(t, idleRespond, func(c *Config) {
		c.NewClient = func(cfg config.Resolved, _ func(llm.RetryInfo)) llm.Client {
			return &malformedArgsClient{model: cfg.Model, respond: func(history []llm.Message) (llm.Response, error) {
				n := turns.Add(1)
				switch n {
				case 1:
					out := toolTurn("shortcut_manage", bad)
					out.Message.Content[0].ID = "malformed-call"
					return out, nil
				case 2:
					view, err := h.mgr.userShortcuts(s.owner, s.studioID)
					if err != nil || len(view.Shortcuts) != 0 {
						return llm.Response{}, fmt.Errorf("invalid call mutated shortcut storage: count=%d error=%v", len(view.Shortcuts), err)
					}
					var rejected bool
					for _, message := range history {
						for _, block := range message.Content {
							if block.Type == llm.BlockToolResult && block.ToolUseID == "malformed-call" {
								rejected = block.IsError && strings.Contains(block.Text, "invalid JSON") && strings.Contains(block.Text, "Re-issue")
							}
						}
					}
					if !rejected {
						return llm.Response{}, fmt.Errorf("model did not receive actionable invalid-JSON feedback")
					}
					if _, err := json.Marshal(s.Hub().Snapshot()); err != nil {
						return llm.Response{}, fmt.Errorf("snapshot during retry cannot serialize: %w", err)
					}
					out := toolTurn("shortcut_manage", good)
					out.Message.Content[0].ID = "corrected-call"
					return out, nil
				default:
					return textTurn("saved and still usable"), nil
				}
			}}
		}
	})
	a := postStudioAgent(t, h, studioAgentCapsBody("Malformed shortcut recovery", "ready", map[string]any{"tools": []string{grant}}), http.StatusCreated)
	sid := startStudioConversation(t, h, a.ID)
	s = h.session(sid)
	h.setPolicy(sid, ModeAuto, 0)
	stream := h.start(sid, "Save the literal command in a shortcut")
	var sawBadMessage, sawRejected, sawSaved bool
	for {
		event := stream.next(t)
		if event.Type == EvError || event.Error != "" {
			t.Fatalf("bad tool input leaked a global stream error: type=%s error=%s", event.Type, event.Error)
		}
		if event.Type == EvMessage {
			for _, block := range event.Content {
				if block.Type == llm.BlockToolUse && block.ID == "malformed-call" {
					sawBadMessage = block.InputInvalid && block.InputError != "" && string(block.Input) == bad
				}
			}
		}
		if event.Type == EvToolEnd && event.CallID == "malformed-call" {
			sawRejected = event.IsError && strings.Contains(event.Text, "invalid JSON")
		}
		if event.Type == EvToolEnd && event.CallID == "corrected-call" {
			sawSaved = !event.IsError
		}
		if event.Type == EvRunEnd {
			break
		}
	}
	stream.close()
	s.wait()
	if !sawBadMessage || !sawRejected || !sawSaved || turns.Load() != 3 {
		t.Fatalf("incomplete recovery: original=%v rejected=%v saved=%v turns=%d", sawBadMessage, sawRejected, sawSaved, turns.Load())
	}
	view, err := h.mgr.userShortcuts(s.owner, s.studioID)
	if err != nil || len(view.Shortcuts) != 1 || view.Shortcuts[0].Content != `echo \$HOME` {
		t.Fatalf("retry did not preserve exact content as one shortcut: shortcuts=%+v error=%v", view.Shortcuts, err)
	}
	data, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatal("malformed argument corrupted a transcript record")
		}
	}
	stored, err := session.Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	var restoredOriginal bool
	for _, message := range stored.Messages("") {
		for _, block := range message.Content {
			if block.Type == llm.BlockToolUse && block.ID == "malformed-call" {
				restoredOriginal = block.InputInvalid && block.InputError != "" && string(block.Input) == bad
			}
		}
	}
	if !restoredOriginal {
		t.Fatal("persisted history lost original malformed input or failure marker")
	}
	h.mgr.evictAll()
	s = h.session(sid)
	reconnected := h.stream(sid, "")
	first := reconnected.next(t)
	reconnected.close()
	if first.Type != EvSnapshot || first.Snapshot == nil || len(first.Snapshot.Messages) == 0 {
		t.Fatal("reloaded conversation could not deliver a usable reconnect snapshot")
	}
	next := h.start(sid, "Continue in the existing conversation")
	next.wait(t, EvRunEnd)
	next.close()
	s.wait()
	if turns.Load() != 4 {
		t.Fatalf("reloaded conversation could not execute another model turn: %d", turns.Load())
	}
}
