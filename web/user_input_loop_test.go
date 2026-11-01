package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

type userInputModel struct {
	calls    int
	received chan []llm.Message
}

func (*userInputModel) Model() string { return "k3" }

func (m *userInputModel) Stream(ctx context.Context, _ string, history []llm.Message, schemas []llm.ToolSchema, onDelta func(llm.Delta)) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	if m.calls == 0 {
		found := false
		for _, schema := range schemas {
			found = found || schema.Name == tools.RequestUserInputName
		}
		if !found {
			return llm.Response{}, fmt.Errorf("question tool not declared to model")
		}
	}
	m.calls++
	if m.calls > 1 {
		m.received <- append([]llm.Message(nil), history...)
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "已收到选择"}}}, StopReason: llm.StopEndTurn}, nil
	}
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
		Type: llm.BlockToolUse, ID: "model-choice", Name: tools.RequestUserInputName,
		Input: []byte(`{"questions":[{"id":"approach","header":"方案","question":"选择哪种实现？","multi_select":false,"options":[{"label":"A","description":"复用现有结构"},{"label":"B","description":"引入新的状态"}]},{"id":"checks","header":"验证","question":"需要哪些检查？","multi_select":true,"options":[{"label":"单测","description":"验证核心规则"},{"label":"端到端","description":"验证真实恢复"}]}]}`),
	}}}, StopReason: llm.StopToolUse}, nil
}

func TestUserInputFullLoopAnswersResumeOriginalCallAndReachModel(t *testing.T) {
	model := &userInputModel{received: make(chan []llm.Message, 1)}
	h := newHarness(t, nil, func(cfg *Config) {
		cfg.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return model }
	})
	sid := h.createSession()
	stream := h.start(sid, "先让我选择方案")
	requestEvent := stream.wait(t, EvUserInputRequest)
	if requestEvent.UserInput == nil || requestEvent.UserInput.CallID != "model-choice" || model.calls != 1 {
		t.Fatalf("wrong question event or model advanced too early: %+v calls=%d", requestEvent.UserInput, model.calls)
	}
	request := *requestEvent.UserInput
	body := fmt.Sprintf(`{"action":"user_input_answer","request_id":%q,"revision":1,"answers":{"approach":["A"],"checks":["单测","端到端"]}}`, request.RequestID)
	h.post("/api/sessions/"+sid+"/control", body, http.StatusOK)
	toolEnd := stream.wait(t, EvToolEnd)
	if toolEnd.IsError || !strings.Contains(toolEnd.Text, `"approach":["A"]`) || !strings.Contains(toolEnd.Text, `"checks":["单测","端到端"]`) {
		t.Fatalf("wrong tool result: %+v", toolEnd)
	}
	final := stream.wait(t, EvMessage)
	if len(final.Content) == 0 || final.Content[0].Text != "已收到选择" {
		t.Fatalf("missing continued answer: %+v", final)
	}
	stream.wait(t, EvRunEnd)
	var received []llm.Message
	select {
	case received = <-model.received:
	default:
		t.Fatal("model was not called again with the answer")
	}
	found := false
	for _, message := range received {
		for _, block := range message.Content {
			if block.Type == llm.BlockToolResult && block.ToolUseID == "model-choice" {
				found = true
				if block.IsError || !strings.Contains(block.Text, `"approach":["A"]`) || !strings.Contains(block.Text, `"checks":["单测","端到端"]`) {
					t.Fatalf("model received wrong answer: %+v", block)
				}
			}
		}
	}
	if !found {
		t.Fatal("model did not receive the original call's tool result")
	}
	if pending := h.session(sid).hub.Snapshot().Live.PendingUserInputs; len(pending) != 0 {
		t.Fatalf("settled question survived run end: %+v", pending)
	}
}
