package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/schedule"
)

func TestAgentScheduleTurnLimitSurvivesManagerPreferenceUpdate(t *testing.T) {
	for _, tc := range []struct {
		name, mode            string
		max, steps, wantCalls int
		end                   agent.EndReason
	}{
		{"unlimited", schedule.TurnLimitUnlimited, 0, 61, 62, agent.EndCompleted},
		{"finite", schedule.TurnLimitLimited, 3, 6, 3, agent.EndTurnLimit},
		{"legacy", schedule.TurnLimitLegacy, 0, 55, 50, agent.EndTurnLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := scheduleEnv(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var phase, taskCalls, ordinaryCalls atomic.Int64
			h := newHarness(t, func(n int) (llm.Response, error) {
				if phase.Load() == 1 {
					ordinaryCalls.Add(1)
				} else {
					taskCalls.Add(1)
					if n == 1 {
						close(entered)
						<-release
					}
					if n > tc.steps {
						return textTurn("done"), nil
					}
				}
				return llm.Response{StopReason: llm.StopToolUse, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: fmt.Sprintf("step%d", n), Name: "not-selected", Input: json.RawMessage(`{}`)}}}}, nil
			})
			h.mgr.SetMaxTurnsOverride(300)
			a := createStudioAgent(t, h, "Pinned limits", "context", "ready")
			a = patchStudioPolicy(t, h, a, map[string]any{"tools": []string{}, "skills": []string{}, "mcp_servers": []string{}})
			ordinaryID := startStudioConversation(t, h, a.ID)
			ordinary := h.session(ordinaryID)
			body := map[string]any{"name": "pinned-limit", "agent_id": a.ID, "prompt": "finish", "kind": "every", "every_sec": 3600, "timezone": "UTC", "turn_limit_mode": tc.mode, "max_turns": tc.max}
			agentScheduleResponse(t, h.do("POST", "/api/schedule/jobs", studioBody(body)), 201)
			job, err := schedule.NewStore(dir).Find("pinned-limit")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			result := make(chan schedule.RunOutcome, 1)
			go func() {
				result <- h.mgr.runAgentSchedule(ctx, job, schedule.LogPaths{Out: filepath.Join(t.TempDir(), "out.log")}, io.Discard)
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("task did not reach live model boundary")
			}
			// The real Manager pushes this to every cached Agent, including the active task.
			h.mgr.SetMaxTurnsOverride(2)
			close(release)
			var out schedule.RunOutcome
			select {
			case out = <-result:
			case <-ctx.Done():
				t.Fatal("task did not finish")
			}
			if out.EndReason != string(tc.end) || int(taskCalls.Load()) != tc.wantCalls {
				t.Fatalf("task setting overridden: calls=%d out=%+v", taskCalls.Load(), out)
			}
			// A separate pre-existing ordinary conversation still follows global settings.
			phase.Store(1)
			receipt := &scheduleRunReceipt{}
			var active *activeRun
			_, err = ordinary.startOrderedInput("ordinary", nil, nil, "", "", mcpobserve.Metadata{}, "", func(run *activeRun) { active = run; run.observe = receipt.observe })
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-active.done:
			case <-ctx.Done():
				t.Fatal("ordinary run did not stop")
			}
			if ordinaryCalls.Load() != 2 || receipt.endReason != string(agent.EndTurnLimit) {
				t.Fatalf("ordinary default no longer updates: calls=%d receipt=%+v", ordinaryCalls.Load(), receipt)
			}
		})
	}
}
