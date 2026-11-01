package web

// Opt-in real-provider acceptance. It uses only a temporary workspace and
// session store. The provider wrapper rejects every tool except the question
// before execution, so this test cannot mutate a deployed Agent or business data.
import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

type userInputLiveClient struct {
	actual llm.Client
	calls  int
}

func (c *userInputLiveClient) Model() string { return c.actual.Model() }
func (c *userInputLiveClient) Stream(ctx context.Context, system string, history []llm.Message, schemas []llm.ToolSchema, onDelta func(llm.Delta)) (llm.Response, error) {
	c.calls++
	if c.calls > 6 {
		return llm.Response{}, errors.New("live question acceptance exhausted its model-call budget")
	}
	response, err := c.actual.Stream(ctx, system, history, schemas, onDelta)
	if err != nil {
		return response, err
	}
	for _, block := range response.Message.Content {
		if block.Type == llm.BlockToolUse && block.Name != tools.RequestUserInputName && block.Name != tools.RequestUserInputAsyncName {
			return llm.Response{}, fmt.Errorf("live acceptance blocked unexpected tool %q before execution", block.Name)
		}
	}
	return response, nil
}

func TestUserInputLiveProvider(t *testing.T) {
	if os.Getenv("POGO_USER_INPUT_LIVE") != "1" {
		t.Skip("real-provider question acceptance requires POGO_USER_INPUT_LIVE=1")
	}
	if _, err := config.Load(); err != nil {
		t.Fatalf("load configured live provider: %v", err)
	}
	model := os.Getenv("POGO_USER_INPUT_LIVE_MODEL")
	if model == "" {
		model = config.DefaultModel()
	}
	resolved, err := config.Resolve(model)
	if err != nil {
		t.Fatalf("resolve live provider: %v", err)
	}
	base := t.TempDir()
	h := &harness{t: t, token: "isolated-question-live-token"}
	h.mgr, err = NewManager(Config{
		Cwd: filepath.Join(base, "workspace"), SessionDir: filepath.Join(base, "sessions"), Model: resolved.Model,
		MaxTurns: 6, Retries: 1, GateTimeout: 5 * time.Second, RunTimeout: 5 * time.Minute, IdleTimeout: 5 * time.Minute,
		NewClient: func(cfg config.Resolved, onRetry func(llm.RetryInfo)) llm.Client {
			maxTokens := cfg.MaxTokens
			if maxTokens <= 0 || maxTokens > 4096 {
				maxTokens = 4096
			}
			actual := llm.New(llm.Options{
				Compatibility: cfg.Compatibility, BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Model: cfg.Model,
				MaxTokens: maxTokens, Headers: cfg.Headers, ExtraBody: cfg.ExtraBody,
				Timeout: 2 * time.Minute, MaxRetries: 1, OnRetry: onRetry,
			})
			return &userInputLiveClient{actual: actual}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.mgr.Close)
	srv, err := NewServer(h.mgr, ServerOptions{
		Token: h.token, PrefsStore: filepath.Join(base, "prefs.json"), SharesStore: filepath.Join(base, "shares.json"),
		StartersStore: filepath.Join(base, "starters.json"), MCPClientStore: filepath.Join(base, "mcp.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.srv = httptest.NewServer(srv.Handler())
	t.Cleanup(h.srv.Close)

	sid := h.createSession()
	stream := h.start(sid, "这次必须使用 request_user_input 问我一个问题：我选择仅本地修改，还是同时更新文档？给出这两个选项和简短说明。不要调用其他工具，不要替我选择；等我回答后用一句话复述我的选择。")
	event := waitLiveUserInputEvent(t, stream, EvUserInputRequest)
	if event.UserInput == nil || len(event.UserInput.Questions) == 0 {
		t.Fatalf("live model produced no structured question: %+v", event)
	}
	request := *event.UserInput
	stream.close()
	stream = h.stream(sid, "")
	if snapshot := stream.next(t); snapshot.Type != EvSnapshot || snapshot.Snapshot == nil || len(snapshot.Snapshot.Live.PendingUserInputs) != 1 || snapshot.Snapshot.Live.PendingUserInputs[0].RequestID != request.RequestID {
		t.Fatalf("reconnect did not restore the same pending question: %+v", snapshot)
	}
	answers := map[string][]string{}
	for _, question := range request.Questions {
		if len(question.Options) < 2 {
			t.Fatalf("live model returned too few choices: %+v", question)
		}
		answers[question.ID] = []string{question.Options[0].Label}
	}
	h.post("/api/sessions/"+sid+"/control", studioBody(map[string]any{
		"action": "user_input_answer", "request_id": request.RequestID, "revision": request.Revision, "answers": answers,
	}), 200)
	toolEnd := waitLiveUserInputEvent(t, stream, EvToolEnd)
	if toolEnd.IsError || !strings.Contains(toolEnd.Text, `"answers":`) {
		t.Fatalf("live question did not resume with an answer: %+v", toolEnd)
	}
	waitLiveUserInputEvent(t, stream, EvMessage)
	end := waitLiveUserInputEvent(t, stream, EvRunEnd)
	if end.Error != "" || end.IsError {
		t.Fatalf("live resumed run failed: %+v", end)
	}
	if got := h.session(sid).hub.Snapshot().Live.PendingUserInputs; len(got) != 0 {
		t.Fatalf("answered live question remained pending: %+v", got)
	}
	asyncSID := h.createSession()
	asyncStream := h.start(asyncSID, "这次必须使用 request_user_input_async 问我一个可选偏好：我选择仅本地修改还是同时更新文档？给出两个选项和简短说明。登记后立即继续并结束当前回合，不要等待我的回答，不要调用其他工具。")
	asyncEvent := waitLiveUserInputEvent(t, asyncStream, EvUserInputRequest)
	if asyncEvent.UserInput == nil || !asyncEvent.UserInput.Async || len(asyncEvent.UserInput.Questions) == 0 {
		t.Fatalf("live model produced no optional question: %+v", asyncEvent.UserInput)
	}
	asyncRequest := *asyncEvent.UserInput
	asyncToolEnd := waitLiveUserInputEvent(t, asyncStream, EvToolEnd)
	if asyncToolEnd.IsError || !strings.Contains(asyncToolEnd.Text, `"waiting_for_user":true`) {
		t.Fatalf("optional question was not acknowledged: %+v", asyncToolEnd)
	}
	waitLiveUserInputEvent(t, asyncStream, EvRunEnd)
	if pending := h.session(asyncSID).hub.Snapshot().Live.PendingUserInputs; len(pending) != 1 {
		t.Fatalf("optional question disappeared after the first run: %+v", pending)
	}
	asyncAnswers := map[string][]string{}
	for _, question := range asyncRequest.Questions {
		if len(question.Options) < 2 {
			t.Fatalf("live model returned too few optional choices: %+v", question)
		}
		asyncAnswers[question.ID] = []string{question.Options[0].Label}
	}
	h.post("/api/sessions/"+asyncSID+"/control", studioBody(map[string]any{
		"action": "user_input_answer", "request_id": asyncRequest.RequestID, "revision": asyncRequest.Revision, "answers": asyncAnswers,
	}), 200)
	asyncEnd := waitLiveUserInputEvent(t, asyncStream, EvRunEnd)
	if asyncEnd.Error != "" || asyncEnd.IsError {
		t.Fatalf("late asynchronous answer failed: %+v", asyncEnd)
	}
	state := h.session(asyncSID).hub.Snapshot()
	notificationFound := false
	for _, message := range state.Messages {
		for _, block := range message.Content {
			if block.Type == llm.BlockText && strings.Contains(block.Text, `"kind":"user_input_answer"`) && strings.Contains(block.Text, `"request_id":"`+asyncRequest.RequestID+`"`) {
				notificationFound = true
			}
		}
	}
	if !notificationFound {
		t.Fatal("the late answer was not recorded in the follow-up conversation")
	}
	if len(state.Live.PendingUserInputs) != 0 || len(state.Inbox) != 1 || state.Inbox[0].Status != "delivered" || state.UserInputAnswers[asyncRequest.CallID].Status != "answered" {
		t.Fatalf("asynchronous answer did not settle after a new turn: %+v", state)
	}
	t.Logf("real provider %s completed synchronous resume and asynchronous end-answer-new-turn flows", resolved.Model)
}

func waitLiveUserInputEvent(t *testing.T, stream *sseStream, want EventType) Event {
	t.Helper()
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case event, open := <-stream.frames:
			if !open {
				t.Fatalf("live stream ended before %s", want)
			}
			if event.Type == want {
				return event
			}
			if event.Type == EvRunEnd && want != EvRunEnd {
				t.Fatalf("live run ended before %s: reason=%s error=%s event=%+v", want, event.EndReason, event.Error, event)
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for live %s", want)
		}
	}
}
