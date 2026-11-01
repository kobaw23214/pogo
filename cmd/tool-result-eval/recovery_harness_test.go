package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/config"
)

func TestRecoveryCLIPhasesRemainSeparateFromExistingScenarios(t *testing.T) {
	for _, phase := range []string{"", "all", "control", "compact", "persist", "compact-persist", "reconnect"} {
		if !validScenarioVariant(options{Scenario: "recovery", RecoveryPhase: phase}) {
			t.Fatalf("valid recovery phase rejected: %s", phase)
		}
		if phase != "" && validScenarioVariant(options{Scenario: "records", RecoveryPhase: phase}) {
			t.Fatal("records accepted recovery-only option")
		}
	}
	if validScenarioVariant(options{Scenario: "recovery", RecoveryPhase: "switch"}) || validScenarioVariant(options{Scenario: "recovery", LogTraceOnly: true}) || validScenarioVariant(options{Scenario: "logs", RecoverySwitchModel: "another"}) || validRecoveryPhase("made-up") {
		t.Fatal("invalid scenario/phase combination accepted")
	}
	if !validScenarioVariant(options{Scenario: "recovery", RecoveryPhase: "switch", RecoverySwitchModel: "another"}) {
		t.Fatal("explicit different-model phase rejected")
	}
	if len(recoveryPhases(options{})) != 5 || len(recoveryPhases(options{RecoverySwitchModel: "another"})) != 6 || strings.Join(recoveryPhases(options{RecoveryPhase: "compact-persist"}), ",") != "compact-persist" {
		t.Fatal("phase selection silently combined lifecycle boundaries")
	}
}

// The scripted endpoint is deliberately allowed to know the test oracle. The
// acceptance result must still depend on real SDK data, a genuine search page
// in the next request, session persistence and actual source stamps.
func TestRecoveryHarnessLifecycleBoundariesAndLateEvidence(t *testing.T) {
	f := makeRecoveryFixture()
	var mu sync.Mutex
	observedModels := map[string]int{}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		var input struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(body, &input); err != nil {
			t.Errorf("decode wire request: %v", err)
			w.WriteHeader(500)
			return
		}
		mu.Lock()
		observedModels[input.Model]++
		mu.Unlock()
		last := input.Messages[len(input.Messages)-1]
		var receipt struct {
			View string `json:"view"`
		}
		_ = json.Unmarshal([]byte(agent.StripCiteHeader(last.Content)), &receipt)
		response := "收到，按本轮资料协调场地、物资和人员。"
		finish := "stop"
		var delta any
		if len(input.Messages) > 0 && strings.HasPrefix(input.Messages[0].Content, "You are compacting the transcript") {
			id := resultIDPattern.FindString(last.Content)
			if id == "" {
				t.Error("real compaction request did not contain the saved result reference")
			}
			response = "日志观察已保存，尚未分析。后续从保存结果 " + id + " 读取；原来源 [1](#cite:S1)。活动筹备资料已整理。"
		} else if strings.HasPrefix(last.Content, f.EarlyTask) {
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "original-logs", "type": "function", "function": map[string]any{"name": "mcp_eval_get_logs", "arguments": "{}"}}}}
			finish = "tool_calls"
		} else if strings.HasPrefix(last.Content, f.LateTask) {
			id := resultIDPattern.Find(body)
			args, _ := json.Marshal(map[string]any{"result_id": string(id), "view": "search", "pattern": logTargetTrace, "context_lines": 0, "limit": 20})
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "late-search", "type": "function", "function": map[string]any{"name": "tool_result", "arguments": string(args)}}}}
			finish = "tool_calls"
		} else if last.Role == "tool" && receipt.View == "search" {
			label := "S1"
			if strings.HasPrefix(last.Content, "[cite:") {
				label = strings.SplitN(strings.TrimPrefix(last.Content, "[cite:"), " | ", 2)[0]
			}
			answer := f.Logs.Expected
			answer.Sources = []string{"[1](#cite:" + label + ")"}
			raw, _ := json.Marshal(answer)
			response = "```json\n" + string(raw) + "\n```"
		} else if last.Role == "tool" {
			response = "已保存日志，保留恢复信息，稍后分析。 [1](#cite:S1)"
		}
		if delta == nil {
			delta = map[string]any{"content": response}
		}
		frame, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
	}))
	defer model.Close()
	resolved := config.Resolved{Model: "scripted-primary", Provider: "fixture-a", BaseURL: model.URL, ContextWindow: 100000, MaxTokens: 2000}
	switched := config.Resolved{Model: "scripted-alternate", Provider: "fixture-b", BaseURL: model.URL, ContextWindow: 128000, MaxTokens: 3000}
	for _, phase := range []string{"control", "compact", "persist", "compact-persist", "reconnect", "switch"} {
		t.Run(phase, func(t *testing.T) {
			opt := options{Scenario: "recovery", RecoveryPhase: phase, recovery: &f, RecoverySwitchModel: switched.Model, switchResolved: &switched}
			log := &safeLog{encoder: json.NewEncoder(io.Discard)}
			report := runRecoveryPhase(context.Background(), opt, resolved, phase, "auto", 1, log)
			if report.MCPCalls != 1 || report.FileToolCalls != 0 || !report.EarlyEvidence.Passed || !report.Late.NumericCorrect || !report.Late.CoverageCorrect || report.Late.LogEvidence == nil || !report.Late.LogEvidence.Passed || !report.Late.Recovery.MatchesSource {
				t.Fatalf("lifecycle test did not reach real late read/facts/evidence: failure=%s/%s late=%s/%s mcp=%d file=%d facts=%t final=%s", report.FailureStage, report.ErrorClass, report.Late.EndReason, report.Late.ErrorClass, report.MCPCalls, report.FileToolCalls, report.Late.NumericCorrect, bounded(report.Late.FinalAnswer, 600))
			}
			if phase == "compact-persist" {
				// A correct answer cannot hide a missing parent stamp. Do not lock
				// the current production defect in as expected permanent behavior.
				if report.Passed != (report.Late.Passed && report.Source.Passed) {
					t.Fatalf("total verdict ignored actual source/evidence outcome: %+v", report.Source)
				}
			} else if !report.Passed || !report.Source.Passed {
				t.Fatalf("working lifecycle control failed: stage=%s error=%s source=%+v", report.FailureStage, report.ErrorClass, report.Source)
			}
			if phase == "compact" || phase == "compact-persist" {
				if !report.Boundary.CompactCalled || !report.Boundary.CompactCommitted || report.Boundary.CompactAfterTokens >= report.Boundary.CompactBeforeTokens || report.Boundary.Summary == nil || report.Boundary.Summary.RootCauseLeaked {
					t.Fatal("real compaction/commit was not measured truthfully")
				}
			}
			if phase == "persist" || phase == "compact-persist" {
				if !report.Boundary.RegistryRebuilt || !report.Boundary.AgentRebuilt || !report.Boundary.PersistedRoundTrip || report.Boundary.SessionFileSHA256 == "" {
					t.Fatal("persist phase did not actually reopen its session and rebuild agent/registry")
				}
			}
			if phase == "reconnect" && (!report.Boundary.ClientRebuilt || report.Boundary.ModelChanged) {
				t.Fatal("same-model reconnect was mislabeled as cross-model")
			}
			lateRequest := report.Late.Requests[0]
			if phase == "switch" {
				if !report.Boundary.ModelChanged || lateRequest.Model != switched.Model || lateRequest.Provider != switched.Provider || lateRequest.ContextWindow != switched.ContextWindow || lateRequest.MaxOutputTokens != switched.MaxTokens || lateRequest.ToolOutputBudget == nil || lateRequest.ToolOutputBudget.ContextWindow != switched.ContextWindow {
					t.Fatalf("actual switched request/config not recorded: %+v", lateRequest)
				}
			} else if lateRequest.Model != resolved.Model || lateRequest.Provider != resolved.Provider {
				t.Fatal("same-model phase changed model metadata")
			}
			if report.EarlyTaskSHA256 != digest([]byte(f.EarlyTask)) || report.LateTaskSHA256 != digest([]byte(f.LateTask)) || report.Usage.Input == nil || *report.Usage.Input != int64((len(report.Stages)+2)*100) {
				t.Fatalf("task mutation or missing completed-request accounting: stages=%d usage=%+v", len(report.Stages), report.Usage)
			}
			warmInput, warmOutput := int64(500), int64(50)
			if phase == "compact" || phase == "compact-persist" {
				warmInput += 100
				warmOutput += 10
			}
			if report.Boundary.UsageBefore.Input != 500 || report.Boundary.UsageBefore.Output != 50 || report.Boundary.UsageAfter.Input != warmInput || report.Boundary.UsageAfter.Output != warmOutput || report.AgentUsage.Input != *report.Usage.Input || report.Usage.Output == nil || report.AgentUsage.Output != *report.Usage.Output {
				t.Fatalf("session/reconnect discarded or double-counted actual cumulative spend: before=%+v after=%+v final=%+v measured=%+v", report.Boundary.UsageBefore, report.Boundary.UsageAfter, report.AgentUsage, report.Usage)
			}
		})
	}
	mu.Lock()
	defer mu.Unlock()
	if observedModels[switched.Model] == 0 || observedModels[resolved.Model] == 0 {
		t.Fatal("scripted wire endpoint did not receive both configured models")
	}
}
