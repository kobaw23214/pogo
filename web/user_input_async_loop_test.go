package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

type asyncUserInputModel struct {
	calls         int
	histories     chan []llm.Message
	secondStarted chan struct{}
	releaseSecond chan struct{}
}

func (*asyncUserInputModel) Model() string { return "k3" }

func (m *asyncUserInputModel) Stream(ctx context.Context, _ string, history []llm.Message, schemas []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	if m.calls == 0 {
		found := false
		for _, schema := range schemas {
			found = found || schema.Name == tools.RequestUserInputAsyncName
		}
		if !found {
			return llm.Response{}, fmt.Errorf("asynchronous question tool not declared")
		}
	}
	m.calls++
	m.histories <- append([]llm.Message(nil), history...)
	if m.calls == 2 && m.secondStarted != nil {
		close(m.secondStarted)
		<-m.releaseSecond
	}
	if m.calls > 3 {
		return llm.Response{}, fmt.Errorf("answer caused an extra model turn")
	}
	if m.calls == 1 {
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
			Type: llm.BlockToolUse, ID: "async-choice", Name: tools.RequestUserInputAsyncName,
			Input: []byte(`{"questions":[{"id":"approach","header":"方案","question":"选择哪种实现？","multi_select":false,"options":[{"label":"A","description":"复用现有结构"},{"label":"B","description":"引入新的状态"}]},{"id":"checks","header":"验证","question":"需要哪些检查？","multi_select":true,"options":[{"label":"单测","description":"验证核心规则"},{"label":"端到端","description":"验证真实恢复"}]}]}`),
		}}}, StopReason: llm.StopToolUse}, nil
	}
	text := "先完成其他工作"
	if m.calls == 3 {
		text = "已收到异步补答"
	}
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: text}}}, StopReason: llm.StopEndTurn}, nil
}

func TestAsyncUserInputEndsRunThenAnswerStartsOneNewTurnAfterRestart(t *testing.T) {
	model := &asyncUserInputModel{histories: make(chan []llm.Message, 4)}
	h := newHarness(t, nil, func(cfg *Config) {
		cfg.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return model }
	})
	sid := h.createSession()
	stream := h.start(sid, "先登记一个可选问题")
	requestEvent := stream.wait(t, EvUserInputRequest)
	if requestEvent.UserInput == nil || !requestEvent.UserInput.Async || requestEvent.UserInput.CallID != "async-choice" {
		t.Fatalf("wrong asynchronous request: %+v", requestEvent.UserInput)
	}
	toolEnd := stream.wait(t, EvToolEnd)
	if toolEnd.IsError || !strings.Contains(toolEnd.Text, `"waiting_for_user":true`) {
		t.Fatalf("question was not accepted asynchronously: %+v", toolEnd)
	}
	stream.wait(t, EvRunEnd)
	if model.calls != 2 || len(h.session(sid).hub.Snapshot().Live.PendingUserInputs) != 1 {
		t.Fatalf("agent did not finish before the answer: calls=%d pending=%d", model.calls, len(h.session(sid).hub.Snapshot().Live.PendingUserInputs))
	}
	request := *requestEvent.UserInput

	// Rebuild the complete server against the same on-disk session, not merely
	// a second Hub, to prove the pending question survives process replacement.
	oldManager, oldHTTP := h.mgr, h.srv
	cfg := oldManager.cfg
	oldManager.Close()
	oldHTTP.Close()
	mgr, err := NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.mgr = mgr
	t.Cleanup(mgr.Close)
	srv, err := NewServer(mgr, ServerOptions{Token: h.token, PrefsStore: filepath.Join(t.TempDir(), "prefs.json"), SharesStore: filepath.Join(t.TempDir(), "shares.json"), StartersStore: filepath.Join(t.TempDir(), "starters.json"), MCPClientStore: filepath.Join(t.TempDir(), "mcp.json")})
	if err != nil {
		t.Fatal(err)
	}
	h.srv = httptest.NewServer(srv.Handler())
	t.Cleanup(h.srv.Close)
	stream = h.stream(sid, "")
	snapshot := stream.next(t)
	if snapshot.Type != EvSnapshot || snapshot.Snapshot == nil || len(snapshot.Snapshot.Live.PendingUserInputs) != 1 || snapshot.Snapshot.Live.PendingUserInputs[0].RequestID != request.RequestID {
		t.Fatalf("restart did not restore the same question: %+v", snapshot)
	}
	body := fmt.Sprintf(`{"action":"user_input_answer","request_id":%q,"revision":1,"answers":{"approach":["A"],"checks":["单测","端到端"]}}`, request.RequestID)
	h.post("/api/sessions/"+sid+"/control", body, http.StatusOK)
	stream.wait(t, EvUserInputResolved)
	stream.wait(t, EvRunEnd)
	if model.calls != 3 {
		t.Fatalf("late answer started %d total model calls, want 3", model.calls)
	}
	notificationFound := false
	for len(model.histories) > 0 {
		history := <-model.histories
		for _, message := range history {
			for _, block := range message.Content {
				if block.Type == llm.BlockText && strings.Contains(block.Text, `"kind":"user_input_answer"`) && strings.Contains(block.Text, `"request_id":"`+request.RequestID+`"`) && strings.Contains(block.Text, `"approach":["A"]`) {
					notificationFound = true
				}
			}
		}
	}
	if !notificationFound {
		t.Fatal("model did not receive the original question and its late answer")
	}
	snapshotState := h.session(sid).hub.Snapshot()
	asyncCalls := 0
	for _, message := range snapshotState.Messages {
		for _, block := range message.Content {
			if block.Type == llm.BlockToolUse && block.Name == tools.RequestUserInputAsyncName {
				asyncCalls++
			}
		}
	}
	if asyncCalls != 1 {
		t.Fatalf("old asynchronous tool executed %d times", asyncCalls)
	}
	if len(snapshotState.Live.PendingUserInputs) != 0 || len(snapshotState.Inbox) != 1 || snapshotState.Inbox[0].Status != "delivered" {
		t.Fatalf("answer delivery did not settle: %+v", snapshotState)
	}
	details := snapshotState.UserInputAnswers[request.CallID]
	if details.Status != "answered" || len(details.Answers["checks"]) != 2 {
		t.Fatalf("answer was not preserved in the result history: %+v", details)
	}
}
