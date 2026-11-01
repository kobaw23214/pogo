package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/mcpobserve"
)

type userInputBrokerFixture struct {
	calls   chan UserInputRequest
	answers chan map[string][]string
}

func (f *userInputBrokerFixture) Ask(ctx context.Context, request UserInputRequest) (map[string][]string, error) {
	f.calls <- request
	select {
	case answers := <-f.answers:
		return answers, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func validUserInputArguments() string {
	return `{"questions":[{"id":"implementation","header":"方案","question":"选择哪种实现？","multi_select":false,"options":[{"label":"方案 A","description":"复用现有结构"},{"label":"方案 B","description":"引入新的状态"}]}]}`
}

func TestRequestUserInputValidation(t *testing.T) {
	tool := &RequestUserInput{}
	if err := ValidateArgs(tool, json.RawMessage(validUserInputArguments())); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"missing questions":      `{}`,
		"empty questions":        `{"questions":[]}`,
		"too many questions":     `{"questions":[1,2,3,4,5]}`,
		"invalid id":             `{"questions":[{"id":"not valid","header":"方案","question":"选择？","multi_select":false,"options":[{"label":"A","description":"a"},{"label":"B","description":"b"}]}]}`,
		"long header":            `{"questions":[{"id":"choice","header":"这是一个超过十二个字的问题标题","question":"选择？","multi_select":false,"options":[{"label":"A","description":"a"},{"label":"B","description":"b"}]}]}`,
		"duplicate options":      `{"questions":[{"id":"choice","header":"方案","question":"选择？","multi_select":false,"options":[{"label":"A","description":"a"},{"label":"A","description":"b"}]}]}`,
		"built in other":         `{"questions":[{"id":"choice","header":"方案","question":"选择？","multi_select":false,"options":[{"label":"Other","description":"a"},{"label":"B","description":"b"}]}]}`,
		"missing selection mode": `{"questions":[{"id":"choice","header":"方案","question":"选择？","options":[{"label":"A","description":"a"},{"label":"B","description":"b"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateArgs(tool, json.RawMessage(raw)); err == nil {
				t.Fatal("invalid input was accepted")
			}
		})
	}
}

func TestRequestUserInputWaitsForTrustedAnswer(t *testing.T) {
	broker := &userInputBrokerFixture{calls: make(chan UserInputRequest, 1), answers: make(chan map[string][]string, 1)}
	tool := &RequestUserInput{Broker: broker}
	ctx := mcpobserve.WithMetadata(context.Background(), mcpobserve.Metadata{ToolCallID: "call-choice"})
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := tool.Execute(ctx, json.RawMessage(validUserInputArguments()))
		done <- outcome{result, err}
	}()
	request := <-broker.calls
	if request.CallID != "call-choice" || len(request.Questions) != 1 || request.Questions[0].ID != "implementation" {
		t.Fatalf("wrong trusted request: %+v", request)
	}
	select {
	case result := <-done:
		t.Fatalf("tool returned before the user answered: %+v", result)
	case <-time.After(25 * time.Millisecond):
	}
	broker.answers <- map[string][]string{"implementation": {"方案 A"}}
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	var response UserInputResponse
	if err := json.Unmarshal([]byte(result.result.Text), &response); err != nil {
		t.Fatal(err)
	}
	if response.Answers["implementation"][0] != "方案 A" || len(response.Questions) != 1 {
		t.Fatalf("answer was not returned to the original call: %s", result.result.Text)
	}
	if _, ok := result.result.Details.(UserInputDetails); !ok {
		t.Fatalf("missing structured answer details: %T", result.result.Details)
	}
}

func TestRequestUserInputCannotUseModelSuppliedAnswersOrMissingCallID(t *testing.T) {
	broker := &userInputBrokerFixture{calls: make(chan UserInputRequest, 1), answers: make(chan map[string][]string, 1)}
	tool := &RequestUserInput{Broker: broker}
	raw := strings.TrimSuffix(validUserInputArguments(), "}") + `,"answers":{"implementation":["pretend answered"]}}`
	if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), "call ID") {
		t.Fatalf("missing trusted call identity accepted: %v", err)
	}
	select {
	case <-broker.calls:
		t.Fatal("question was asked without a trusted call identity")
	default:
	}
	ctx, cancel := context.WithCancel(mcpobserve.WithMetadata(context.Background(), mcpobserve.Metadata{ToolCallID: "real-call"}))
	done := make(chan error, 1)
	go func() {
		_, err := tool.Execute(ctx, json.RawMessage(raw))
		done <- err
	}()
	<-broker.calls
	cancel()
	if err := <-done; err == nil {
		t.Fatal("model-supplied answers resumed a cancelled question")
	}
}

func TestRequestUserInputIsHostOnlyAndIndependentOfSelection(t *testing.T) {
	empty := []string{}
	broker := &userInputBrokerFixture{}
	registry := New(Options{Cwd: t.TempDir(), Enabled: &empty, ReadOnly: true, UserInput: broker})
	tool, ok := registry.Get(RequestUserInputName)
	if !ok || tool.ExecutionMode() != Sequential {
		t.Fatal("interactive host question tool was not registered as a sequential call")
	}
	if _, ok := New(Options{Cwd: t.TempDir()}).Get(RequestUserInputName); ok {
		t.Fatal("session without a user channel received the question tool")
	}
	next, apply, err := registry.WithNativeSelection(&empty, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply()
	if got, ok := next.Get(RequestUserInputName); !ok || got != tool {
		t.Fatal("tool selection replaced or revoked the host question channel")
	}
}

func TestRequestUserInputNameCannotBeImpersonated(t *testing.T) {
	impostor := &RequestUserInput{}
	for name, registry := range map[string]*Registry{
		"declared":  New(Options{Cwd: t.TempDir(), Synth: []Tool{impostor}}),
		"projected": New(Options{Cwd: t.TempDir(), MCP: []Tool{impostor}}),
		"refreshed": New(Options{Cwd: t.TempDir()}).WithMCP([]Tool{impostor}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := registry.Get(RequestUserInputName); ok {
				t.Fatal("external tool impersonated the host's question channel")
			}
		})
	}
	updated, apply, err := New(Options{Cwd: t.TempDir()}).WithSkillContext(SkillContextUpdate{Tools: []Tool{impostor}})
	if err != nil {
		t.Fatal(err)
	}
	apply()
	if _, ok := updated.Get(RequestUserInputName); ok {
		t.Fatal("skill context projection impersonated the host's question channel")
	}
}
