package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestToolProbeRoundTrip(t *testing.T) {
	for _, mode := range []string{"native", "xml", "json", "text"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				var req wireRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				delta := map[string]any{"reasoning_content": "private reasoning"}
				reason := "stop"
				if n == 1 {
					switch mode {
					case "native":
						delta["tool_calls"] = []any{map[string]any{"index": 0, "id": "n1", "type": "function",
							"function": map[string]string{"name": "pogo_probe_nonce", "arguments": "{}"}}}
						reason = "tool_calls"
					case "xml":
						delta["content"] = `<tool_call><invoke name="pogo_probe_nonce"></invoke></tool_call>`
					default:
						delta["content"] = `<tool_call>{"name":"pogo_probe_nonce","arguments":{}}</tool_call>`
					}
				} else {
					var result map[string]string
					last := req.Messages[len(req.Messages)-1]
					text := last.Content
					if mode == "text" {
						if len(req.Tools) != 0 || last.Role != "user" {
							t.Error("text transport used native tools/history")
						}
						envelope := strings.TrimSuffix(strings.TrimPrefix(text, "<tool_result>"), "</tool_result>")
						if err := json.Unmarshal([]byte(envelope), &result); err != nil {
							t.Error(err)
						}
						text = result["output"]
					}
					if err := json.Unmarshal([]byte(text), &result); err != nil {
						t.Error(err)
					}
					delta["content"] = result["nonce"]
					prev := req.Messages[len(req.Messages)-2]
					if prev.ReasoningContent == nil || *prev.ReasoningContent != "private reasoning" {
						t.Error("reasoning lost in next request")
					}
				}
				frame, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": reason}}})
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
			}))
			defer srv.Close()
			compat := Compatibility{ToolCalls: mode, PreserveReasoning: true}
			if mode == "text" {
				compat.ToolCalls, compat.ToolHistory = "json", "text"
			}
			client := New(Options{BaseURL: srv.URL, MaxRetries: -1, Compatibility: compat})
			report := ProbeToolCalls(context.Background(), client)
			if !report.Passed || !report.ResultConsumed || len(report.Steps) != 2 || requests.Load() != 2 {
				t.Fatalf("probe failed: %+v", report)
			}
			encoded, _ := json.Marshal(report)
			if strings.Contains(string(encoded), "private reasoning") || strings.Contains(string(encoded), "pogo_probe_") {
				t.Fatalf("probe report leaked data: %s", encoded)
			}
		})
	}
}

func TestTextBufferDoesNotRetryAnIncompleteOrOversizedResponse(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		var requests atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			text := "<tool_call>"
			if oversized {
				text += strings.Repeat("x", maxTextToolBytes)
			}
			frame, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": text}}}})
			fmt.Fprintf(w, "data: %s\n\n", frame) // EOF, deliberately no completed frame
		}))
		client := New(Options{BaseURL: srv.URL, MaxRetries: 2, Compatibility: Compatibility{ToolCalls: "xml"}})
		var visible string
		r, err := client.Stream(context.Background(), "", nil, protocolSchemas(), func(d Delta) { visible += d.Text })
		srv.Close()
		if err == nil || requests.Load() != 1 || len(r.Message.ToolCalls()) != 0 || visible != "" {
			t.Fatalf("replayed unsafe response: err=%v requests=%d visible=%q", err, requests.Load(), visible)
		}
	}
}

func TestProtocolReasoningCountsInHistoryEstimate(t *testing.T) {
	history := []Message{{Role: RoleAssistant, Content: []Block{{Type: BlockThinking, Text: strings.Repeat("x", 4000)}, {Type: BlockText, Text: "done"}}}}
	native := New(Options{})
	reasoning := New(Options{Compatibility: Compatibility{PreserveReasoning: true}})
	if EstimateTokensFor(reasoning, history)-EstimateTokensFor(native, history) != 1000 {
		t.Fatal("replayed reasoning not counted")
	}
}
