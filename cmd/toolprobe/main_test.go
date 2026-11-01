package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/yosukeno/pogo/llm"
)

type suiteClient struct {
	requests int
	fail     bool
}

func (c *suiteClient) Model() string { return "suite-fixture" }
func (c *suiteClient) Stream(_ context.Context, _ string, history []llm.Message, schemas []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	c.requests++
	last := history[len(history)-1]
	if len(history) == 1 && !c.fail {
		args := `{}`
		if schemas[0].Name == "pogo_probe_arguments" {
			args = `{"tool":"fixture_target","limit":1,"compact":true}`
		}
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, Name: schemas[0].Name, ID: fmt.Sprintf("call-%d", c.requests), Input: json.RawMessage(args)}}}, StopReason: llm.StopToolUse}, nil
	}
	text := "ignored tool"
	if last.Content[0].Type == llm.BlockToolResult {
		var result map[string]string
		if err := json.Unmarshal([]byte(last.Content[0].Text), &result); err != nil {
			return llm.Response{}, err
		}
		text = result["nonce"]
	}
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: text}}}, StopReason: llm.StopEndTurn}, nil
}

func TestProbeSuiteCountsAndCancellation(t *testing.T) {
	client := &suiteClient{}
	compat := llm.Compatibility{ToolCalls: "xml", AllowTextPreamble: true}
	report := runSuite(context.Background(), client, "contract", 2, compat)
	if !report.Passed || report.Requested != 4 || report.Completed != 4 || report.FirstPass != 4 || report.Recovered != 0 || report.Model != client.Model() || report.Compatibility != compat || client.requests != 8 {
		t.Fatalf("wrong suite summary: %+v requests=%d", report, client.requests)
	}
	failure := runSuite(context.Background(), &suiteClient{fail: true}, "basic", 2, llm.Compatibility{})
	if failure.Passed || failure.FirstPass != 0 || failure.Completed != 2 {
		t.Fatalf("failed suite claimed success: %+v", failure)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client = &suiteClient{}
	canceled := runSuite(ctx, client, "contract", 2, compat)
	if canceled.Passed || canceled.Completed != 0 || canceled.Requested != 4 || client.requests != 0 {
		t.Fatalf("canceled suite: %+v", canceled)
	}
}
