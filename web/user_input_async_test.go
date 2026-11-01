package web

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/tools"
)

func TestAsyncUserInputOutlivesRunAndRestoresPendingAndAnswered(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	hub := NewHub()
	service := NewWebUserInputWithStore(hub, time.Millisecond, transcript, func(PendingUserInput, map[string][]string) (InboxItem, error) {
		return InboxItem{Status: "steering"}, nil
	})
	ticket, err := service.AskAsync(context.Background(), tools.UserInputRequest{CallID: "async-one", RunID: "run-one", Questions: userInputQuestionsFixture()})
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Status != "pending" || !ticket.WaitingForUser || ticket.CallID != "async-one" {
		t.Fatalf("wrong asynchronous acknowledgement: %+v", ticket)
	}
	request := waitUserInputRequest(t, hub)
	if !request.Async || request.Deadline != 0 || request.RunID != "run-one" {
		t.Fatalf("asynchronous request inherited synchronous timeout: %+v", request)
	}
	hub.Publish(Event{Type: EvRunEnd})
	if len(hub.Snapshot().Live.PendingUserInputs) != 1 {
		t.Fatal("run end discarded an optional question")
	}
	service.Close()
	if got := hub.Snapshot().Live.PendingUserInputs; len(got) != 1 {
		t.Fatal("session close discarded an unsent asynchronous question")
	}
	info, err := os.Stat(userInputStorePath(transcript))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("question store permissions = %v, %v", info.Mode(), err)
	}

	hub = NewHub()
	service = NewWebUserInputWithStore(hub, time.Millisecond, transcript, func(PendingUserInput, map[string][]string) (InboxItem, error) {
		return InboxItem{Status: "steering"}, nil
	})
	restored := waitUserInputRequest(t, hub)
	if restored.RequestID != request.RequestID || restored.CallID != request.CallID {
		t.Fatalf("restored a different question: %+v", restored)
	}
	answers := map[string][]string{"approach": {"A"}, "checks": {"单测", "端到端"}}
	if _, err := service.Answer(request.RequestID, request.Revision, answers); err != nil {
		t.Fatal(err)
	}
	if len(hub.Snapshot().Live.PendingUserInputs) != 0 {
		t.Fatal("accepted answer remained a pending choice")
	}
	if got := hub.Snapshot().UserInputAnswers[request.CallID]; got.Status != "answered" || len(got.Answers["checks"]) != 2 {
		t.Fatalf("answer was not restored into history: %+v", got)
	}
	service.Close()

	deliveries := 0
	hub = NewHub()
	service = NewWebUserInputWithStore(hub, time.Millisecond, transcript, func(PendingUserInput, map[string][]string) (InboxItem, error) {
		deliveries++
		return InboxItem{Status: "delivered"}, nil
	})
	service.recoverAnswers()
	if deliveries != 1 {
		t.Fatalf("saved answer was not recovered exactly once: %d", deliveries)
	}
	records, err := loadUserInputStore(userInputStorePath(transcript))
	if err != nil || records[request.RequestID].Status != "delivered" || records[request.RequestID].DeliveredAt <= 0 {
		t.Fatalf("delivery was not persisted: %+v, %v", records, err)
	}
	if duplicate, err := service.Answer(request.RequestID, request.Revision, answers); err != nil || !duplicate {
		t.Fatalf("delivered retry was not idempotent: %v, %v", duplicate, err)
	}
	if deliveries != 1 {
		t.Fatal("delivered answer triggered another delivery")
	}
	if _, err := service.Answer(request.RequestID, request.Revision, map[string][]string{"approach": {"B"}, "checks": {"单测", "端到端"}}); !errors.Is(err, ErrUserInputResolved) {
		t.Fatalf("delivered answer was overwritten: %v", err)
	}
	service.Close()
}

func TestAsyncUserInputDeliveryFailureRetriesWithoutChangingAnswer(t *testing.T) {
	hub := NewHub()
	service := NewWebUserInput(hub, time.Minute)
	defer service.Close()
	if _, err := service.AskAsync(context.Background(), tools.UserInputRequest{CallID: "retry", Questions: userInputQuestionsFixture()}); err != nil {
		t.Fatal(err)
	}
	request := waitUserInputRequest(t, hub)
	answers := map[string][]string{"approach": {"A"}, "checks": {"单测", "端到端"}}
	if _, err := service.Answer(request.RequestID, request.Revision, answers); !errors.Is(err, ErrUserInputUnavailable) {
		t.Fatalf("missing delivery error: %v", err)
	}
	if len(hub.Snapshot().Live.PendingUserInputs) != 1 {
		t.Fatal("failed delivery discarded the pending question")
	}
	service.deliverAnswer = func(PendingUserInput, map[string][]string) (InboxItem, error) {
		return InboxItem{Status: "delivered"}, nil
	}
	if duplicate, err := service.Answer(request.RequestID, request.Revision, answers); err != nil || !duplicate {
		t.Fatalf("retry did not complete delivery: %v, %v", duplicate, err)
	}
	if len(hub.Snapshot().Live.PendingUserInputs) != 0 {
		t.Fatal("successful retry left the question pending")
	}
}

func TestAsyncUserInputCancelDoesNotWakeAgent(t *testing.T) {
	hub := NewHub()
	service := NewWebUserInput(hub, time.Minute)
	defer service.Close()
	if _, err := service.AskAsync(context.Background(), tools.UserInputRequest{CallID: "cancel", Questions: userInputQuestionsFixture()}); err != nil {
		t.Fatal(err)
	}
	request := waitUserInputRequest(t, hub)
	if _, err := service.Cancel(request.RequestID, request.Revision); err != nil {
		t.Fatal(err)
	}
	if duplicate, err := service.Cancel(request.RequestID, request.Revision); err != nil || !duplicate {
		t.Fatalf("cancel retry was not idempotent: %v, %v", duplicate, err)
	}
	if _, err := service.Answer(request.RequestID, request.Revision, map[string][]string{"approach": {"A"}, "checks": {"单测"}}); !errors.Is(err, ErrUserInputResolved) {
		t.Fatalf("cancelled question accepted an answer: %v", err)
	}
	if len(hub.Snapshot().Live.PendingUserInputs) != 0 {
		t.Fatal("cancelled question remained pending")
	}
}

func TestAsyncUserInputCorruptStoreFailsClosed(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	path := userInputStorePath(transcript)
	if err := os.WriteFile(path, []byte(`{"version":99,"records":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewWebUserInputWithStore(NewHub(), time.Minute, transcript, nil)
	if _, err := service.AskAsync(context.Background(), tools.UserInputRequest{CallID: "corrupt", Questions: userInputQuestionsFixture()}); err == nil {
		t.Fatal("corrupt store allowed a new question")
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), `"version":99`) {
		t.Fatal("corrupt source was overwritten")
	}
}
