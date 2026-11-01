package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/yosukeno/pogo/mcpobserve"
)

const (
	RequestUserInputName       = "request_user_input"
	MaxUserInputQuestions      = 4
	MaxUserInputOptions        = 4
	MaxUserInputQuestionLength = 1000
	MaxUserInputAnswerLength   = 2000
)

// UserInputQuestion is a decision, not an authorization to run another tool.
// IDs stay stable across transports so answers cannot be attached to the wrong
// question when two prompts have similar wording.
type UserInputQuestion struct {
	ID          string            `json:"id"`
	Header      string            `json:"header"`
	Question    string            `json:"question"`
	Options     []UserInputOption `json:"options"`
	MultiSelect bool              `json:"multi_select"`
}

type UserInputOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type UserInputRequest struct {
	CallID    string              `json:"-"`
	RunID     string              `json:"-"`
	Questions []UserInputQuestion `json:"questions"`
}

// UserInputBroker is implemented only by a host with a real person attached.
// Nil in CLI children and unattended executions means no question tool exists.
type UserInputBroker interface {
	Ask(context.Context, UserInputRequest) (map[string][]string, error)
}

type UserInputResponse struct {
	Questions []UserInputQuestion `json:"questions"`
	Answers   map[string][]string `json:"answers"`
}

type UserInputDetails struct {
	RequestID string              `json:"request_id,omitempty"`
	CallID    string              `json:"call_id,omitempty"`
	Status    string              `json:"status,omitempty"`
	Async     bool                `json:"async,omitempty"`
	Questions []UserInputQuestion `json:"questions"`
	Answers   map[string][]string `json:"answers"`
}

type RequestUserInput struct {
	Broker UserInputBroker
}

func (*RequestUserInput) Name() string { return RequestUserInputName }

func (*RequestUserInput) Description() string {
	return "Ask the user one to four structured questions and wait for their answers. Use only when a decision is genuinely theirs and changes what you do next; infer routine choices and verify facts yourself instead. " +
		"Prefer one question with 2-4 distinct options and a clear tradeoff in each description. Put a recommended option first and suffix its label with (Recommended). " +
		"Set multi_select only when choices can be combined. The interface always offers a custom answer, so do not add an Other option. " +
		"This collects preferences, never permission to execute a command or another tool. Do not use it for routine progress updates or to ask whether obvious work should proceed."
}

func (*RequestUserInput) ExecutionMode() ExecutionMode { return Sequential }

func (*RequestUserInput) InputSchema() map[string]any {
	return object([]string{"questions"}, map[string]any{
		"questions": map[string]any{
			"type": "array", "minItems": 1, "maxItems": MaxUserInputQuestions,
			"description": "One to four short, independent questions. Prefer asking only one.",
			"items": object([]string{"id", "header", "question", "options", "multi_select"}, map[string]any{
				"id":           prop("string", "Stable snake_case identifier used to map the answer, unique within this call."),
				"header":       prop("string", "Very short category label, at most 12 characters."),
				"question":     prop("string", "One complete, specific question in the user's language."),
				"multi_select": prop("boolean", "True when several options may be selected; false for mutually exclusive choices."),
				"options": map[string]any{
					"type": "array", "minItems": 2, "maxItems": MaxUserInputOptions,
					"items": object([]string{"label", "description"}, map[string]any{
						"label":       prop("string", "Concise option label, preferably 1-5 words. Do not add an Other option."),
						"description": prop("string", "One short sentence explaining the impact or tradeoff of this choice."),
					}),
				},
			}),
		},
	})
}

func (t *RequestUserInput) ValidateArgs(raw json.RawMessage) error {
	if err := validateAgainst(t.InputSchema(), raw, ""); err != nil {
		return err
	}
	var args struct {
		Questions []UserInputQuestion `json:"questions"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return err
	}
	_, err := NormalizeUserInputQuestions(args.Questions)
	return err
}

// NormalizeUserInputQuestions validates and copies model-originated questions
// before they cross into the host's trusted pending-request state.
func NormalizeUserInputQuestions(questions []UserInputQuestion) ([]UserInputQuestion, error) {
	if len(questions) < 1 || len(questions) > MaxUserInputQuestions {
		return nil, fmt.Errorf("questions must contain 1-%d items", MaxUserInputQuestions)
	}
	ids := make(map[string]bool, len(questions))
	out := make([]UserInputQuestion, len(questions))
	for i, original := range questions {
		q := original
		q.ID, q.Header, q.Question = strings.TrimSpace(q.ID), strings.TrimSpace(q.Header), strings.TrimSpace(q.Question)
		if !validUserInputID(q.ID) {
			return nil, fmt.Errorf("questions[%d].id must be snake_case, start with a letter and contain at most 64 characters", i)
		}
		if ids[q.ID] {
			return nil, fmt.Errorf("questions[%d].id %q is duplicated", i, q.ID)
		}
		ids[q.ID] = true
		if q.Header == "" || utf8.RuneCountInString(q.Header) > 12 {
			return nil, fmt.Errorf("questions[%d].header must contain 1-12 characters", i)
		}
		if q.Question == "" || utf8.RuneCountInString(q.Question) > MaxUserInputQuestionLength {
			return nil, fmt.Errorf("questions[%d].question must contain 1-%d characters", i, MaxUserInputQuestionLength)
		}
		if len(q.Options) < 2 || len(q.Options) > MaxUserInputOptions {
			return nil, fmt.Errorf("questions[%d].options must contain 2-%d items", i, MaxUserInputOptions)
		}
		q.Options = append([]UserInputOption(nil), q.Options...)
		labels := make(map[string]bool, len(q.Options))
		for j, option := range q.Options {
			option.Label, option.Description = strings.TrimSpace(option.Label), strings.TrimSpace(option.Description)
			if option.Label == "" || utf8.RuneCountInString(option.Label) > 80 {
				return nil, fmt.Errorf("questions[%d].options[%d].label must contain 1-80 characters", i, j)
			}
			if strings.EqualFold(option.Label, "other") || option.Label == "其他" || option.Label == "自定义" {
				return nil, fmt.Errorf("questions[%d].options[%d] must not provide the interface's built-in custom answer", i, j)
			}
			if labels[option.Label] {
				return nil, fmt.Errorf("questions[%d].options[%d].label %q is duplicated", i, j, option.Label)
			}
			labels[option.Label] = true
			if option.Description == "" || utf8.RuneCountInString(option.Description) > 500 {
				return nil, fmt.Errorf("questions[%d].options[%d].description must contain 1-500 characters", i, j)
			}
			q.Options[j] = option
		}
		out[i] = q
	}
	return out, nil
}

func validUserInputID(id string) bool {
	if len(id) == 0 || len(id) > 64 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for _, c := range id[1:] {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

func (t *RequestUserInput) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if t.Broker == nil {
		return Result{}, fmt.Errorf("%s is unavailable without an interactive user", RequestUserInputName)
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
	callID := mcpobserve.MetadataFrom(ctx).ToolCallID
	if callID == "" {
		return Result{}, fmt.Errorf("%s requires a host-owned tool call ID", RequestUserInputName)
	}
	answers, err := t.Broker.Ask(ctx, UserInputRequest{CallID: callID, Questions: questions})
	if err != nil {
		return Result{Text: err.Error()}, err
	}
	response := UserInputResponse{Questions: questions, Answers: answers}
	text, err := json.Marshal(response)
	if err != nil {
		return Result{}, err
	}
	return Result{Text: string(text), Details: UserInputDetails{Questions: questions, Answers: answers}}, nil
}
