package web

import (
	"context"
	"errors"
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

func TestAsyncUserInputAnswerSteersActiveRunWithoutAnotherRun(t *testing.T) {
	model := &asyncUserInputModel{histories: make(chan []llm.Message, 4), secondStarted: make(chan struct{}), releaseSecond: make(chan struct{})}
	h := newHarness(t, nil, func(cfg *Config) {
		cfg.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return model }
	})
	sid := h.createSession()
	stream := h.start(sid, "登记可选问题后继续")
	requestEvent := stream.wait(t, EvUserInputRequest)
	request := *requestEvent.UserInput
	stream.wait(t, EvToolEnd)
	select {
	case <-model.secondStarted:
	case <-stream.frames:
		t.Fatal("run ended before the active-delivery setup")
	}
	body := fmt.Sprintf(`{"action":"user_input_answer","request_id":%q,"revision":1,"answers":{"approach":["A"],"checks":["单测","端到端"]}}`, request.RequestID)
	h.post("/api/sessions/"+sid+"/control", body, http.StatusOK)
	stream.wait(t, EvUserInputResolved)
	close(model.releaseSecond)
	stream.wait(t, EvRunEnd)
	if model.calls != 3 {
		t.Fatalf("active answer caused a separate run: %d model calls", model.calls)
	}
	state := h.session(sid).hub.Snapshot()
	if len(state.Inbox) != 1 || state.Inbox[0].Status != "delivered" || state.Inbox[0].RunID != request.RunID {
		t.Fatalf("answer did not steer the original run: %+v", state.Inbox)
	}
	if len(state.Live.PendingUserInputs) != 0 || state.UserInputAnswers[request.CallID].Status != "answered" {
		t.Fatalf("active answer did not settle: %+v", state)
	}
}

type asyncAnswerModel struct {
	calls     int
	histories chan []llm.Message
}

func (*asyncAnswerModel) Model() string { return "k3" }
func (m *asyncAnswerModel) Stream(ctx context.Context, _ string, history []llm.Message, _ []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	m.calls++
	m.histories <- append([]llm.Message(nil), history...)
	if m.calls > 1 {
		return llm.Response{}, fmt.Errorf("saved answer was delivered more than once")
	}
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "已收到保存的回答"}}}, StopReason: llm.StopEndTurn}, nil
}

func TestAsyncUserInputAnsweredBeforeCrashRecoversOnce(t *testing.T) {
	model := &asyncAnswerModel{histories: make(chan []llm.Message, 2)}
	h := newHarness(t, nil, func(cfg *Config) {
		cfg.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return model }
	})
	sid := h.createSession()
	session := h.session(sid)
	if _, err := session.userInput.AskAsync(context.Background(), tools.UserInputRequest{CallID: "saved-before-crash", Questions: userInputQuestionsFixture()}); err != nil {
		t.Fatal(err)
	}
	request := session.hub.Snapshot().Live.PendingUserInputs[0]
	session.userInput.deliverAnswer = func(PendingUserInput, map[string][]string) (InboxItem, error) {
		return InboxItem{}, errors.New("simulated process loss before delivery")
	}
	answers := map[string][]string{"approach": {"A"}, "checks": {"单测", "端到端"}}
	if _, err := session.userInput.Answer(request.RequestID, request.Revision, answers); err == nil {
		t.Fatal("simulated delivery loss did not surface")
	}
	records, err := loadUserInputStore(userInputStorePath(session.store.Path()))
	if err != nil || records[request.RequestID].Status != "answered" {
		t.Fatalf("answer was not saved before the simulated crash: %+v, %v", records, err)
	}
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
	stream := h.stream(sid, "")
	stream.next(t)
	stream.wait(t, EvMessage)
	stream.wait(t, EvRunEnd)
	if model.calls != 1 {
		t.Fatalf("saved answer started %d model calls, want one", model.calls)
	}
	var history []llm.Message
	select {
	case history = <-model.histories:
	default:
		t.Fatal("model did not receive the saved answer")
	}
	found := false
	for _, message := range history {
		for _, block := range message.Content {
			if block.Type == llm.BlockText && strings.Contains(block.Text, `"kind":"user_input_answer"`) && strings.Contains(block.Text, `"approach":["A"]`) {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("recovered answer did not preserve its content")
	}
	state := h.session(sid).hub.Snapshot()
	if len(state.Inbox) != 1 || state.Inbox[0].Status != "delivered" || state.UserInputAnswers[request.CallID].Status != "answered" {
		t.Fatalf("recovered answer did not settle: %+v", state)
	}
}
