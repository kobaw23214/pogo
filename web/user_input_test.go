package web

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/tools"
)

func userInputQuestionsFixture() []tools.UserInputQuestion {
	return []tools.UserInputQuestion{
		{ID: "approach", Header: "方案", Question: "选择哪种实现？", MultiSelect: false, Options: []tools.UserInputOption{{Label: "A", Description: "复用现有结构"}, {Label: "B", Description: "引入新的状态"}}},
		{ID: "checks", Header: "验证", Question: "需要哪些检查？", MultiSelect: true, Options: []tools.UserInputOption{{Label: "单测", Description: "验证核心规则"}, {Label: "端到端", Description: "验证真实恢复"}}},
	}
}

func waitUserInputRequest(t *testing.T, hub *Hub) PendingUserInput {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if pending := hub.Snapshot().Live.PendingUserInputs; len(pending) == 1 {
			return pending[0]
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("no question appeared in the session snapshot")
	return PendingUserInput{}
}

func TestUserInputAnswerReconnectAndIdempotency(t *testing.T) {
	hub := NewHub()
	service := NewWebUserInput(hub, time.Minute)
	defer service.Close()
	done := make(chan map[string][]string, 1)
	errDone := make(chan error, 1)
	go func() {
		answers, err := service.Ask(context.Background(), tools.UserInputRequest{CallID: "call-answer", Questions: userInputQuestionsFixture()})
		done <- answers
		errDone <- err
	}()
	request := waitUserInputRequest(t, hub)
	if request.CallID != "call-answer" || request.Revision != 1 || len(request.Questions) != 2 {
		t.Fatalf("wrong pending request: %+v", request)
	}
	snapshot := hub.Snapshot()
	snapshot.Live.PendingUserInputs[0].Questions[0].Options[0].Label = "tampered"
	if got := hub.Snapshot().Live.PendingUserInputs[0].Questions[0].Options[0].Label; got != "A" {
		t.Fatal("snapshot shares mutable question state with the pending request")
	}
	answers := map[string][]string{"approach": {"A"}, "checks": {"单测", "端到端"}}
	if _, err := service.Answer(request.RequestID, 0, answers); !errors.Is(err, ErrUserInputRevision) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	if duplicate, err := service.Answer(request.RequestID, request.Revision, answers); err != nil || duplicate {
		t.Fatalf("first answer rejected: duplicate=%v err=%v", duplicate, err)
	}
	if duplicate, err := service.Answer(request.RequestID, request.Revision, answers); err != nil || !duplicate {
		t.Fatalf("same retry was not idempotent: duplicate=%v err=%v", duplicate, err)
	}
	if _, err := service.Answer(request.RequestID, request.Revision, map[string][]string{"approach": {"B"}, "checks": {"单测", "端到端"}}); !errors.Is(err, ErrUserInputResolved) {
		t.Fatalf("second choice overwrote the first: %v", err)
	}
	select {
	case got := <-done:
		if got["approach"][0] != "A" || len(got["checks"]) != 2 {
			t.Fatalf("wrong answer mapping: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("tool did not resume after answer")
	}
	if err := <-errDone; err != nil {
		t.Fatal(err)
	}
	if pending := hub.Snapshot().Live.PendingUserInputs; len(pending) != 0 {
		t.Fatalf("answered question stayed pending: %+v", pending)
	}
}

func TestUserInputRejectsMalformedAnswers(t *testing.T) {
	hub := NewHub()
	service := NewWebUserInput(hub, time.Minute)
	defer service.Close()
	done := make(chan error, 1)
	go func() {
		_, err := service.Ask(context.Background(), tools.UserInputRequest{CallID: "call-validation", Questions: userInputQuestionsFixture()})
		done <- err
	}()
	request := waitUserInputRequest(t, hub)
	for name, answers := range map[string]map[string][]string{
		"missing":         {"approach": {"A"}},
		"unknown":         {"approach": {"A"}, "checks": {"单测"}, "other": {"B"}},
		"multiple single": {"approach": {"A", "B"}, "checks": {"单测"}},
		"blank custom":    {"approach": {" "}, "checks": {"单测"}},
		"duplicate":       {"approach": {"A"}, "checks": {"单测", "单测"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Answer(request.RequestID, request.Revision, answers); err == nil {
				t.Fatal("malformed answer accepted")
			}
			if len(hub.Snapshot().Live.PendingUserInputs) != 1 {
				t.Fatal("invalid answer resolved the pending question")
			}
		})
	}
	if _, err := service.Answer(request.RequestID, request.Revision, map[string][]string{"approach": {"A"}, "checks": {"自定义验证"}}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestUserInputSkipTimeoutAndRunCancellationNeverChoose(t *testing.T) {
	for _, test := range []struct {
		name    string
		timeout time.Duration
		resolve func(*WebUserInput, PendingUserInput, context.CancelFunc)
		status  string
	}{
		{name: "skip", timeout: time.Minute, status: "cancelled", resolve: func(service *WebUserInput, request PendingUserInput, _ context.CancelFunc) {
			if _, err := service.Cancel(request.RequestID, request.Revision); err != nil {
				t.Error(err)
			}
		}},
		{name: "timeout", timeout: 30 * time.Millisecond, status: "timeout", resolve: func(*WebUserInput, PendingUserInput, context.CancelFunc) {}},
		{name: "run cancelled", timeout: time.Minute, status: "cancelled", resolve: func(_ *WebUserInput, _ PendingUserInput, cancel context.CancelFunc) { cancel() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			hub := NewHub()
			service := NewWebUserInput(hub, test.timeout)
			defer service.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				answers, err := service.Ask(ctx, tools.UserInputRequest{CallID: "call-" + test.status, Questions: userInputQuestionsFixture()})
				if answers != nil {
					t.Errorf("unanswered request returned selections: %+v", answers)
				}
				done <- err
			}()
			request := waitUserInputRequest(t, hub)
			test.resolve(service, request, cancel)
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "do not infer") && !strings.Contains(err.Error(), "Do not infer") {
					t.Fatalf("missing no-inference guidance: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("unanswered request did not settle")
			}
			if len(hub.Snapshot().Live.PendingUserInputs) != 0 {
				t.Fatal("settled request remained answerable")
			}
			if _, err := service.Answer(request.RequestID, request.Revision, map[string][]string{"approach": {"A"}, "checks": {"单测"}}); err == nil {
				t.Fatal("settled request accepted a late answer")
			}
		})
	}
}

func TestUserInputIsNotAnExecutionApproval(t *testing.T) {
	for _, mode := range []Mode{ModeStrict, ModeStandard, ModeAuto} {
		policy := NewPolicy()
		policy.Set(mode, 0)
		request := agent.GateRequest{CallID: "question", ToolName: tools.RequestUserInputName}
		if _, auto := policy.Decide(request); !auto {
			t.Fatalf("%s added an execution approval to a preference question", mode)
		}
	}
	hub := NewHub()
	gate := NewWebGate(hub, NewPolicy(), time.Second)
	if decision := gate.Review(context.Background(), agent.GateRequest{CallID: "question", ToolName: tools.RequestUserInputName}); !decision.Allow {
		t.Fatal("execution gate blocked the dedicated question channel")
	}
	if len(hub.Snapshot().Live.PendingGates) != 0 || len(hub.Snapshot().Live.PendingUserInputs) != 0 {
		t.Fatal("execution gate fabricated a preference question")
	}
}

func TestUserInputCloseReleasesWaiter(t *testing.T) {
	hub := NewHub()
	service := NewWebUserInput(hub, time.Minute)
	done := make(chan error, 1)
	go func() {
		_, err := service.Ask(context.Background(), tools.UserInputRequest{CallID: "close-wait", Questions: userInputQuestionsFixture()})
		done <- err
	}()
	waitUserInputRequest(t, hub)
	service.Close()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "session closed") {
			t.Fatalf("wrong close outcome: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("closing the session left the tool waiting")
	}
	if len(hub.Snapshot().Live.PendingUserInputs) != 0 {
		t.Fatal("closed session retained an answerable question")
	}
}
