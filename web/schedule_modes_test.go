package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/schedule"
)

func TestAgentScheduleExecutionModesRunBeyondLegacyCap(t *testing.T) {
	for _, tc := range []struct {
		name, mode, approval        string
		turns, toolSteps, wantCalls int
		wantPolicy                  Mode
		wantEnd                     agent.EndReason
	}{
		{"new-defaults", "", "", 0, 60, 61, ModeAuto, agent.EndCompleted},
		{"explicit-finite", "limited", "strict", 75, 60, 61, ModeStrict, agent.EndCompleted},
		{"legacy-zero", "legacy", "standard", 0, 60, 50, ModeStandard, agent.EndTurnLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := scheduleEnv(t)
			var modelCalls atomic.Int64
			h := newHarness(t, func(n int) (llm.Response, error) {
				modelCalls.Store(int64(n))
				if n <= tc.toolSteps {
					return llm.Response{StopReason: llm.StopToolUse, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: fmt.Sprintf("step%d", n), Name: "not-selected", Input: json.RawMessage(`{}`)}}}}, nil
				}
				return textTurn("finished safely"), nil
			})
			h.mgr.SetMaxTurnsOverride(300)
			a := createStudioAgent(t, h, "Execution options", "context", "ready")
			a = patchStudioPolicy(t, h, a, map[string]any{"tools": []string{}, "skills": []string{}, "mcp_servers": []string{}})
			body := map[string]any{"name": "modes", "agent_id": a.ID, "prompt": "finish the steps", "kind": "every", "every_sec": 3600, "timezone": "UTC", "max_turns": tc.turns}
			if tc.mode != "" {
				body["turn_limit_mode"] = tc.mode
			}
			if tc.approval != "" {
				body["approval_mode"] = tc.approval
			}
			created := agentScheduleResponse(t, h.do("POST", "/api/schedule/jobs", studioBody(body)), 201)["job"].(map[string]any)
			if tc.name == "new-defaults" && (created["turn_limit_mode"] != "unlimited" || created["approval_mode"] != "auto") {
				t.Fatalf("default contract %v", created)
			}
			job, err := schedule.NewStore(dir).Find("modes")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			logs := schedule.LogPaths{Out: filepath.Join(t.TempDir(), "out.log"), Err: filepath.Join(t.TempDir(), "err.log")}
			if err := os.MkdirAll(filepath.Dir(logs.Out), 0700); err != nil {
				t.Fatal(err)
			}
			out := h.mgr.runAgentSchedule(ctx, job, logs, io.Discard)
			if out.EndReason != string(tc.wantEnd) {
				t.Fatalf("end=%+v", out)
			}
			if tc.wantEnd == agent.EndCompleted && out.Err != "" {
				t.Fatal(out.Err)
			}
			sess, err := h.mgr.Get(out.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if sess.Policy().State().Mode != string(tc.wantPolicy) {
				t.Fatalf("wrong policy %+v", sess.Policy().State())
			}
			// Full access changes review only: unavailable tools remain errors.
			results := 0
			for _, message := range sess.agent.Messages() {
				for _, block := range message.Content {
					if block.Type == llm.BlockToolResult {
						results++
						if !block.IsError {
							t.Fatal("unselected tool executed")
						}
					}
				}
			}
			if results == 0 {
				t.Fatal("missing unavailable-tool result")
			}
			if int(modelCalls.Load()) != tc.wantCalls {
				t.Fatalf("model turns=%d want%d", modelCalls.Load(), tc.wantCalls)
			}
		})
	}
}
