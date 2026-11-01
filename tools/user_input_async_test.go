package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yosukeno/pogo/mcpobserve"
)

type asyncUserInputBrokerFixture struct {
	request UserInputRequest
}

func (f *asyncUserInputBrokerFixture) AskAsync(_ context.Context, request UserInputRequest) (UserInputTicket, error) {
	f.request = request
	return UserInputTicket{RequestID: "q-async", CallID: request.CallID, RunID: request.RunID, Revision: 1, Status: "pending", Questions: request.Questions, WaitingForUser: true}, nil
}

func TestRequestUserInputAsyncReturnsAcknowledgementWithoutAnswers(t *testing.T) {
	broker := &asyncUserInputBrokerFixture{}
	tool := &RequestUserInputAsync{Broker: broker}
	ctx := mcpobserve.WithMetadata(context.Background(), mcpobserve.Metadata{ToolCallID: "async-call", RunID: "run-one"})
	result, err := tool.Execute(ctx, json.RawMessage(validUserInputArguments()))
	if err != nil {
		t.Fatal(err)
	}
	if broker.request.CallID != "async-call" || broker.request.RunID != "run-one" {
		t.Fatalf("wrong host identity: %+v", broker.request)
	}
	var ticket UserInputTicket
	if err := json.Unmarshal([]byte(result.Text), &ticket); err != nil {
		t.Fatal(err)
	}
	if ticket.Status != "pending" || !ticket.WaitingForUser || ticket.RequestID != "q-async" || ticket.Revision != 1 {
		t.Fatalf("invalid acknowledgement: %s", result.Text)
	}
	details, ok := result.Details.(UserInputDetails)
	if !ok || !details.Async || details.Status != "pending" || details.CallID != "async-call" || len(details.Answers) != 0 {
		t.Fatalf("acknowledgement fabricates an answer: %+v", result.Details)
	}
}

func TestRequestUserInputAsyncIsHostOnlyAndReserved(t *testing.T) {
	empty := []string{}
	broker := &asyncUserInputBrokerFixture{}
	registry := New(Options{Cwd: t.TempDir(), Enabled: &empty, ReadOnly: true, AsyncUserInput: broker})
	tool, ok := registry.Get(RequestUserInputAsyncName)
	if !ok || tool.ExecutionMode() != Sequential {
		t.Fatal("asynchronous host tool was not registered")
	}
	if _, ok := New(Options{Cwd: t.TempDir()}).Get(RequestUserInputAsyncName); ok {
		t.Fatal("child or unattended registry received asynchronous questions")
	}
	impostor := &RequestUserInputAsync{}
	for name, registry := range map[string]*Registry{
		"declared":  New(Options{Cwd: t.TempDir(), Synth: []Tool{impostor}}),
		"projected": New(Options{Cwd: t.TempDir(), MCP: []Tool{impostor}}),
		"refreshed": New(Options{Cwd: t.TempDir()}).WithMCP([]Tool{impostor}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := registry.Get(RequestUserInputAsyncName); ok {
				t.Fatal("external tool impersonated the asynchronous host channel")
			}
		})
	}
	next, apply, err := registry.WithNativeSelection(&empty, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply()
	if got, ok := next.Get(RequestUserInputAsyncName); !ok || got != tool {
		t.Fatal("native selection replaced the asynchronous host channel")
	}
}
