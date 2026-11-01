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

func TestToolProbeArgumentsContract(t *testing.T) {
	const valid = `{"tool":"fixture_target","limit":1,"compact":true}`
	for _, tc := range []struct {
		name, first           string
		repair, consume, pass bool
		corrections           int
	}{
		{"correct", valid, false, true, true, 0},
		{"integral decimal", `{"tool":"fixture_target","limit":1.0,"compact":true}`, false, true, true, 0},
		{"selector recovery", `{"tool_name":"fixture_target","limit":1,"compact":true}`, true, true, true, 1},
		{"selector repeats", `{"tool_name":"fixture_target","limit":1,"compact":true}`, false, true, false, 1},
		{"string boolean", `{"tool":"fixture_target","limit":1,"compact":"true"}`, false, true, false, 1},
		{"fraction", `{"tool":"fixture_target","limit":1.00000000000000001,"compact":true}`, false, true, false, 1},
		{"extra key", `{"tool":"fixture_target","limit":1,"compact":true,"extra":1}`, false, true, false, 1},
		{"ignored result", valid, false, false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				var req wireRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if len(req.Tools) != 1 || req.Tools[0].Function.Name != "pogo_probe_arguments" {
					t.Error("wrong tool declarations")
				}
				var delta map[string]any
				reason := "stop"
				last := req.Messages[len(req.Messages)-1]
				if n == 1 || strings.HasPrefix(last.Content, "Error:") {
					args := tc.first
					if n > 1 && tc.repair {
						args = valid
					}
					if n > 1 && (last.Role != "tool" || last.ToolCallID != "arg-1") {
						t.Error("correction lost matching tool result")
					}
					delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("arg-%d", n), "type": "function", "function": map[string]string{"name": "pogo_probe_arguments", "arguments": args}}}}
					reason = "tool_calls"
				} else {
					var result map[string]string
					if last.Role != "tool" || json.Unmarshal([]byte(last.Content), &result) != nil {
						t.Error("lost tool output")
					}
					text := "What would you like to investigate?"
					if tc.consume {
						text = result["nonce"]
					}
					delta = map[string]any{"content": text}
				}
				frame, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": reason}}})
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
			}))
			defer srv.Close()
			report := ProbeToolArguments(context.Background(), New(Options{BaseURL: srv.URL, MaxRetries: -1}))
			if report.Passed != tc.pass || report.Corrections != tc.corrections || requests.Load() > 4 {
				t.Fatalf("report=%+v requests=%d", report, requests.Load())
			}
			if !tc.pass && report.Failure == "" {
				t.Fatal("failed probe omitted cause")
			}
			raw, _ := json.Marshal(report)
			for _, secret := range []string{"fixture_target", "pogo_probe_", "tool_name", "What would"} {
				if strings.Contains(string(raw), secret) {
					t.Fatalf("report leaked fixture contents: %s", raw)
				}
			}
		})
	}
}
