package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/yosukeno/pogo/mcpobserve"
)

const RequestUserInputAsyncName = "request_user_input_async"

// AsyncUserInputBroker registers a question without waiting for an answer.
// The host, not the tool result, owns subsequent answer delivery.
type AsyncUserInputBroker interface {
	AskAsync(context.Context, UserInputRequest) (UserInputTicket, error)
}

type UserInputTicket struct {
	RequestID      string              `json:"request_id"`
	CallID         string              `json:"call_id"`
	RunID          string              `json:"run_id,omitempty"`
	Revision       int                 `json:"revision"`
	Status         string              `json:"status"`
	Questions      []UserInputQuestion `json:"questions"`
	WaitingForUser bool                `json:"waiting_for_user"`
}

type RequestUserInputAsync struct {
	Broker AsyncUserInputBroker
}

func (*RequestUserInputAsync) Name() string { return RequestUserInputAsyncName }

func (*RequestUserInputAsync) Description() string {
	return "Register one to four optional questions for the user and continue without waiting for answers. Use only for preferences or supplementary information that is not required for the next action. " +
		"Use request_user_input instead when the answer decides what may happen next. Never use this for execution approval. " +
		"Each question needs a stable snake_case id, a short header and 2-4 distinct options with tradeoffs. Set multi_select when choices can be combined; the interface provides custom answers. " +
		"Continue using clearly stated assumptions, never claim the user chose an option, and do not repeat the question just because it remains pending. A later answer will arrive as a new host-issued notification tied to this call."
}

func (*RequestUserInputAsync) ExecutionMode() ExecutionMode { return Sequential }

func (t *RequestUserInputAsync) InputSchema() map[string]any {
	return (&RequestUserInput{}).InputSchema()
}

func (t *RequestUserInputAsync) ValidateArgs(raw json.RawMessage) error {
	return (&RequestUserInput{}).ValidateArgs(raw)
}

func (t *RequestUserInputAsync) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if t.Broker == nil {
		return Result{}, fmt.Errorf("%s is unavailable without an interactive user", RequestUserInputAsyncName)
	}
	if err := t.ValidateArgs(raw); err != nil {
		return Result{}, err
	}
	var args struct {
		Questions []UserInputQuestion `json:"questions"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	questions, err := NormalizeUserInputQuestions(args.Questions)
	if err != nil {
		return Result{}, err
	}
	metadata := mcpobserve.MetadataFrom(ctx)
	if metadata.ToolCallID == "" {
		return Result{}, fmt.Errorf("%s requires a host-owned tool call ID", RequestUserInputAsyncName)
	}
	ticket, err := t.Broker.AskAsync(ctx, UserInputRequest{CallID: metadata.ToolCallID, RunID: metadata.RunID, Questions: questions})
	if err != nil {
		return Result{Text: err.Error()}, err
	}
	if ticket.RequestID == "" || ticket.CallID != metadata.ToolCallID || ticket.Status != "pending" || !ticket.WaitingForUser {
		return Result{}, fmt.Errorf("%s received an invalid host acknowledgement", RequestUserInputAsyncName)
	}
	ticket.Questions, ticket.RunID = questions, metadata.RunID
	text, err := json.Marshal(ticket)
	if err != nil {
		return Result{}, err
	}
	return Result{Text: string(text), Details: UserInputDetails{RequestID: ticket.RequestID, CallID: ticket.CallID, Status: "pending", Async: true, Questions: questions}}, nil
}

func hostUserInputName(name string) bool {
	return name == RequestUserInputName || name == RequestUserInputAsyncName
}
