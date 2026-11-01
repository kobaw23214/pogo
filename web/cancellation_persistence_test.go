package web

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/tools"
)

type cancellationPersistenceTool struct{ started chan struct{} }

func (*cancellationPersistenceTool) Name() string        { return "wait_for_cancel" }
func (*cancellationPersistenceTool) Description() string { return "Wait until the run is cancelled" }
func (*cancellationPersistenceTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (*cancellationPersistenceTool) ExecutionMode() tools.ExecutionMode { return tools.Sequential }
func (tool *cancellationPersistenceTool) Execute(ctx context.Context, _ json.RawMessage) (tools.Result, error) {
	close(tool.started)
	<-ctx.Done()
	// Work completed before cancellation still counts, including delegated
	// tokens reported by an unsuccessful tool invocation.
	return tools.Result{Details: tools.SubagentDetails{InputTok: 11, OutTok: 7}}, ctx.Err()
}

// Exercise the real Session.Start -> Cancel -> finish lifecycle and read a new
// Store from disk before Close can repair anything. Then Close must not append
// the same transcript or spend again. The paired agent-level regression adds
// deterministic backpressure at the cancellation boundary.
func TestCancelledRunPersistsPairedResultsAndUsageBeforeClose(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("unused")))
	s := h.session(h.createSession())
	tool := &cancellationPersistenceTool{started: make(chan struct{})}
	response := toolTurn(tool.Name(), `{}`)
	response.Usage = llm.Usage{Input: 120, Output: 18, CacheRead: 40, Reasoning: 6}
	for _, id := range []string{"skipped_2", "skipped_3"} {
		response.Message.Content = append(response.Message.Content, llm.Block{
			Type: llm.BlockToolUse, ID: id, Name: tool.Name(), Input: json.RawMessage(`{}`),
		})
	}
	modelCalls := 0
	s.agent = agent.New(agent.Config{
		Client: &scriptedClient{model: "k3", respond: func() (llm.Response, error) {
			modelCalls++
			return response, nil
		}},
		Registry: tools.NewRegistry(tool),
	})
	if _, err := s.Start("cancel this batch", nil, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tool.started:
	case <-time.After(5 * time.Second):
		t.Fatal("tool did not start")
	}
	s.mu.Lock()
	done := s.active.done
	s.mu.Unlock()
	if !s.Cancel() {
		t.Fatal("active run rejected cancellation")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled run did not finish persistence")
	}
	if s.Active() || modelCalls != 1 {
		t.Fatalf("cancel did not settle the original run: active=%v calls=%d", s.Active(), modelCalls)
	}
	wantMessages := s.agent.Messages()
	wantUsage := llm.Usage{Input: 131, Output: 25, CacheRead: 40, Reasoning: 6}
	wantDelegated := llm.Usage{Input: 11, Output: 7}
	checkDisk := func() {
		t.Helper()
		reopened, err := session.Open(s.Path())
		if err != nil {
			t.Fatal(err)
		}
		messages := reopened.Messages("")
		if !reflect.DeepEqual(messages, wantMessages) {
			t.Fatalf("reopened transcript differs from completed run: got=%#v want=%#v", messages, wantMessages)
		}
		if len(messages) != 3 || len(messages[1].ToolCalls()) != 3 || len(messages[2].Content) != 3 {
			t.Fatalf("cancelled batch is missing call/result blocks: %#v", messages)
		}
		for i, call := range messages[1].ToolCalls() {
			result := messages[2].Content[i]
			if result.Type != llm.BlockToolResult || result.ToolUseID != call.ID || !result.IsError {
				t.Fatalf("call %s has no paired cancelled result: %+v", call.ID, result)
			}
		}
		usage, delegated := reopened.UsageTotals()
		if usage != wantUsage || delegated != wantDelegated {
			t.Fatalf("reopened usage=%+v delegated=%+v, want %+v / %+v", usage, delegated, wantUsage, wantDelegated)
		}
	}
	checkDisk()
	backlog, _, unsubscribe := s.hub.Subscribe(0)
	unsubscribe()
	for _, event := range backlog {
		if event.Type == EvError && strings.Contains(event.Error, "persist") {
			t.Fatalf("cancellation emitted a persistence error: %s", event.Error)
		}
	}
	s.Close()
	checkDisk()
}
