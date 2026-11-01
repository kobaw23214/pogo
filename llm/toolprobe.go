package llm

import (
	"context"
	"encoding/json"
	"math/big"
	"strings"

	"github.com/google/uuid"
)

// ToolProbeReport contains metadata only, never prompts, reasoning, credentials,
// arguments, or tool output. The probe exposes exactly one local no-I/O tool.
type ToolProbeReport struct {
	Passed         bool            `json:"passed"`
	ResultConsumed bool            `json:"result_consumed"`
	Corrections    int             `json:"corrections"`
	Failure        string          `json:"failure,omitempty"`
	Steps          []ToolProbeStep `json:"steps"`
}

type ToolProbeStep struct {
	StopReason   StopReason `json:"stop_reason"`
	NativeCalls  int        `json:"native_calls"`
	TextCalls    int        `json:"text_calls"`
	HasReasoning bool       `json:"has_reasoning"`
	Issue        string     `json:"issue,omitempty"`
	Usage        Usage      `json:"usage"`
}

// ProbeToolCalls verifies the full streamed production-client round trip.
// Success requires returning a fresh nonce that only the fixture tool produced.
// It cannot invoke any MCP, shell, filesystem or business capability.
func ProbeToolCalls(ctx context.Context, client Client) ToolProbeReport {
	schema := ToolSchema{Name: "pogo_probe_nonce", Description: "Return a fresh diagnostic nonce. No arguments. No external I/O.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
	return probeTool(ctx, client, schema,
		"First call pogo_probe_nonce with no arguments, then return its nonce verbatim in your final answer. Do not invent a nonce.",
		func(args map[string]any) bool { return len(args) == 0 })
}

// ProbeToolArguments also checks exact field names and primitive argument
// types. This catches endpoints that pass the no-argument test but substitute
// tool_name for tool, stringify booleans, or ignore the integer constraint.
func ProbeToolArguments(ctx context.Context, client Client) ToolProbeReport {
	schema := ToolSchema{Name: "pogo_probe_arguments", Description: "Inspect a synthetic target with the exact field tool (not tool_name), integer limit and boolean compact; return a fresh nonce. No external I/O.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false,
			"required": []string{"tool", "limit", "compact"},
			"properties": map[string]any{
				"tool":    map[string]any{"type": "string", "enum": []string{"fixture_target"}},
				"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 1},
				"compact": map[string]any{"type": "boolean"},
			}}}
	return probeTool(ctx, client, schema,
		"Call pogo_probe_arguments to inspect fixture_target with a limit of 1 and compact enabled. Follow its declared parameter names and types. Then return its nonce verbatim; do not invent a nonce.",
		func(args map[string]any) bool {
			number, ok := args["limit"].(json.Number)
			if !ok || len(args) != 3 || args["tool"] != "fixture_target" || args["compact"] != true {
				return false
			}
			value, ok := new(big.Rat).SetString(number.String())
			return ok && value.Cmp(big.NewRat(1, 1)) == 0
		})
}

func probeTool(ctx context.Context, client Client, schema ToolSchema, prompt string, valid func(map[string]any) bool) ToolProbeReport {
	report := ToolProbeReport{Steps: []ToolProbeStep{}}
	schemas := []ToolSchema{schema}
	history := []Message{UserText(prompt)}
	nonce := ""
	recovered := false
	seenIDs := map[string]bool{}
	for range 4 {
		response, err := client.Stream(ctx, "You are testing the tool protocol. Use only the declared diagnostic tool.", history, schemas, nil)
		if err != nil {
			report.Failure = "model_request_failed"
			return report
		}
		step := ToolProbeStep{StopReason: response.StopReason, Usage: response.Usage}
		for _, b := range response.Message.Content {
			if b.Type == BlockThinking {
				step.HasReasoning = true
			}
			if b.Type == BlockToolUse {
				if b.ToolOrigin == "" {
					step.NativeCalls++
				} else {
					step.TextCalls++
				}
			}
		}
		if response.ProtocolIssue != nil {
			step.Issue = response.ProtocolIssue.Code
		}
		report.Steps = append(report.Steps, step)
		if response.StopReason != StopEndTurn && response.StopReason != StopToolUse {
			report.Failure = "incomplete_response"
			return report
		}
		history = append(history, response.Message)
		if issue := response.ProtocolIssue; issue != nil {
			if recovered {
				report.Failure = "tool_protocol"
				return report
			}
			recovered = true
			report.Corrections++
			history = append(history, UserText(issue.Error()))
			continue
		}
		calls := response.Message.ToolCalls()
		if len(calls) == 0 {
			report.ResultConsumed = nonce != "" && strings.Contains(response.Message.Text(), nonce)
			report.Passed = report.ResultConsumed
			if !report.Passed {
				report.Failure = "tool_result_not_consumed"
			}
			return report
		}
		if len(calls) != 1 || nonce != "" || calls[0].ID == "" || seenIDs[calls[0].ID] || calls[0].Name != schemas[0].Name {
			report.Failure = "unexpected_tool_call"
			return report
		}
		seenIDs[calls[0].ID] = true
		value, err := strictJSON(calls[0].Input)
		args, ok := value.(map[string]any)
		if err != nil || !ok || !valid(args) {
			report.Steps[len(report.Steps)-1].Issue = "invalid_probe_arguments"
			if recovered {
				report.Failure = "invalid_probe_arguments"
				return report
			}
			recovered = true
			report.Corrections++
			history = append(history, Message{Role: RoleUser, Content: []Block{{
				Type: BlockToolResult, ToolUseID: calls[0].ID, IsError: true,
				Text: "Error: invalid arguments; no tool executed. Use the exact declared parameter names and types, with the requested values and no extra fields.",
			}}})
			continue
		}
		nonce = "pogo_probe_" + uuid.NewString()
		result, _ := json.Marshal(map[string]string{"nonce": nonce})
		history = append(history, Message{Role: RoleUser, Content: []Block{
			{Type: BlockToolResult, ToolUseID: calls[0].ID, Text: string(result)},
		}})
	}
	report.Failure = "probe_turn_limit"
	return report
}
