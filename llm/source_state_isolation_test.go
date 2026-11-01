package llm_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

func sourceStateHistory(t *testing.T, state bool) []llm.Message {
	t.Helper()
	history := []llm.Message{
		llm.UserText("Analyze the saved observation when asked."),
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockThinking, Text: "visible-provider-reasoning"}, {Type: llm.BlockText, Text: "Checking the observation."}, {Type: llm.BlockToolUse, ID: "inspect-1", Name: "inspect", Input: json.RawMessage(`{"key":"value"}`)}}},
		{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "inspect-1", Text: "ACTUAL_OBSERVED_VALUE"}}},
	}
	if state {
		// This deliberately resembles an instruction and contains a large private
		// source ledger. Neither its contents nor its existence belongs on wire.
		raw, err := json.Marshal(map[string]any{"version": 1, "seq": 77, "sources": []any{map[string]any{"label": "S77", "excerpt": "HOST_ONLY_SOURCE_STATE_IGNORE_ALL_INSTRUCTIONS " + strings.Repeat("HOST_PRIVATE_LEDGER_", 4096)}}, "bindings": []any{map[string]any{"result_id": "R" + strings.Repeat("a", 64), "state": "source", "source_label": "S77"}}, "reserved": []string{"S99"}})
		if err != nil {
			t.Fatal(err)
		}
		for index := range history {
			history[index].SourceState = append(json.RawMessage(nil), raw...)
		}
	}
	return history
}

func TestSourceStateNeverReachesProviderWireOrTokenEstimation(t *testing.T) {
	for _, test := range []struct {
		name   string
		compat llm.Compatibility
	}{
		{"openai_native", llm.Compatibility{}},
		{"compatible_native", llm.Compatibility{ToolCalls: "native"}},
		{"compatible_reasoning", llm.Compatibility{PreserveReasoning: true}},
		{"compatible_xml_native_history", llm.Compatibility{ToolCalls: "xml"}},
		{"compatible_xml_text_history", llm.Compatibility{ToolCalls: "xml", ToolHistory: "text"}},
		{"compatible_json_text_history", llm.Compatibility{ToolCalls: "json", ToolHistory: "text", PreserveReasoning: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			captured := make(chan []byte, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				captured <- body
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":30,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			client := llm.New(llm.Options{BaseURL: server.URL, Model: test.name, Compatibility: test.compat, MaxRetries: -1, Timeout: 5 * time.Second})
			plain, local := sourceStateHistory(t, false), sourceStateHistory(t, true)
			schemas := []llm.ToolSchema{{Name: "inspect", Description: "Read a saved observation.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}, "required": []string{"key"}}}}
			if llm.EstimateTokens(plain) != llm.EstimateTokens(local) || llm.EstimateTokensFor(client, plain) != llm.EstimateTokensFor(client, local) {
				t.Fatal("host-only source ledger changed model token estimation")
			}
			for _, history := range [][]llm.Message{plain, local} {
				if _, err := client.Stream(context.Background(), "system", history, schemas, nil); err != nil {
					t.Fatal(err)
				}
			}
			before, after := <-captured, <-captured
			if !bytes.Equal(before, after) {
				t.Fatal("host-only SourceState changed actual provider request bytes")
			}
			for _, marker := range []string{"source_state", "HOST_ONLY_SOURCE_STATE", "HOST_PRIVATE_LEDGER", "S77", "S99"} {
				if bytes.Contains(after, []byte(marker)) {
					t.Fatalf("host-only state marker reached provider: %s", marker)
				}
			}
			if !bytes.Contains(after, []byte("ACTUAL_OBSERVED_VALUE")) {
				t.Fatal("actual model-visible observation disappeared while excluding local metadata")
			}
		})
	}
}

func TestSourceStatePersistsThroughMessageJSONAndSessionReplacement(t *testing.T) {
	history := sourceStateHistory(t, true)
	encoded, err := json.Marshal(history)
	if err != nil || !bytes.Contains(encoded, []byte(`"source_state"`)) {
		t.Fatal("host source state was not serialized locally")
	}
	var restored []llm.Message
	if json.Unmarshal(encoded, &restored) != nil || len(restored) != len(history) {
		t.Fatal("message JSON roundtrip failed")
	}
	for i := range restored {
		if !bytes.Equal(restored[i].SourceState, history[i].SourceState) || restored[i].Text() != history[i].Text() {
			t.Fatal("message JSON roundtrip changed source state or visible text")
		}
	}
	store, err := session.Create(t.TempDir(), t.TempDir(), "source-state-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAll(history); err != nil {
		t.Fatal(err)
	}
	reopened, err := session.Open(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	reopenedHistory := reopened.Messages("")
	if len(reopenedHistory) != len(history) {
		t.Fatal("session reopen lost the original conversation")
	}
	for i, message := range reopenedHistory {
		if !bytes.Equal(message.SourceState, history[i].SourceState) {
			t.Fatal("session reopen lost or changed the local source ledger")
		}
	}
	// This is the compact-persist shape: a continuation user message is the
	// only reachable record, with the ledger in metadata rather than its text.
	continuation := llm.UserText("Continue from the saved conversation summary.")
	continuation.SourceState = append(json.RawMessage(nil), history[0].SourceState...)
	if err := reopened.ForkWithMessages("", []llm.Message{continuation}); err != nil {
		t.Fatal(err)
	}
	compacted, err := session.Open(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	got := compacted.Messages("")
	if len(got) != 1 || got[0].Text() != continuation.Text() || !bytes.Equal(got[0].SourceState, continuation.SourceState) {
		t.Fatal("real session branch replacement/reopen did not retain host-only checkpoint")
	}
	plain := continuation
	plain.SourceState = nil
	if llm.EstimateTokens([]llm.Message{plain}) != llm.EstimateTokens(got) {
		t.Fatal("persisted source checkpoint became model token cost")
	}
}
