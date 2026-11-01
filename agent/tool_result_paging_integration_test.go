package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

type pagingAcceptanceClient struct {
	respond func([]llm.Message) (llm.Response, error)
}

func (*pagingAcceptanceClient) Model() string { return "paging-contract" }
func (c *pagingAcceptanceClient) Stream(_ context.Context, _ string, history []llm.Message, _ []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	return c.respond(history)
}

// The simulated client consumes only the actual model request. It follows the
// delivered continuation, rather than the larger original UI page or a sample.
func TestToolResultAdaptivePagingThroughAgentPreservesFirstFive(t *testing.T) {
	rows := make([]map[string]any, 100)
	for i := range rows {
		rows[i] = map[string]any{"id": 1000 + i, "amount_cents": 25000 + i, "note": strings.Repeat("not selected ", 40)}
	}
	payload, err := json.Marshal(map[string]any{"rows": rows})
	if err != nil {
		t.Fatal(err)
	}
	action := &fakeTool{name: "paging_observation", run: func(context.Context, json.RawMessage) (tools.Result, error) {
		return tools.Result{Text: string(payload), Payload: &tools.ResultPayload{Text: string(payload), Structured: payload}}, nil
	}}
	registry, _ := budgetRegistryForTest(t, action)
	client := &pagingAcceptanceClient{}
	id, next, requests := "", 0, 0
	var seen []int
	modelPages := make(map[string]string)
	call := func(name string, args any) llm.Response {
		raw, _ := json.Marshal(args)
		return llm.Response{StopReason: llm.StopToolUse, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
			Type: llm.BlockToolUse, ID: fmt.Sprintf("paging-call-%d", requests), Name: name, Input: raw,
		}}}}
	}
	client.respond = func(history []llm.Message) (llm.Response, error) {
		requests++
		if requests == 1 {
			return call(action.name, map[string]any{}), nil
		}
		var latest llm.Block
		for _, message := range history {
			for _, block := range message.Content {
				if block.Type == llm.BlockToolResult {
					latest = block
				}
			}
		}
		if latest.IsError {
			return llm.Response{}, fmt.Errorf("a valid continuation was blocked: %s", latest.Text)
		}
		if id == "" {
			id = latest.ResultID
			if !sourceResultID(id) {
				return llm.Response{}, fmt.Errorf("original observation did not retain its recovery ID")
			}
		} else {
			var page struct {
				ResultID string `json:"result_id"`
				Offset   int    `json:"offset"`
				Shown    int    `json:"shown_items"`
				Next     *int   `json:"next_offset"`
				Items    []struct {
					Index  int `json:"original_index"`
					Fields map[string]struct {
						Present bool `json:"present"`
						Value   int  `json:"value"`
					} `json:"fields"`
				} `json:"items"`
			}
			if err := json.Unmarshal([]byte(StripCiteHeader(latest.Text)), &page); err != nil {
				return llm.Response{}, fmt.Errorf("actual model page is not query JSON: %w", err)
			}
			if page.ResultID != id || page.Offset != next || page.Shown != len(page.Items) || page.Shown < 1 || estimatedToolText(latest.Text) > 384 {
				return llm.Response{}, fmt.Errorf("actual model page lost identity, data, continuation or budget: %.1000s", latest.Text)
			}
			modelPages[latest.ToolUseID] = latest.Text
			for _, item := range page.Items {
				position := len(seen)
				if item.Index != position || !item.Fields["/id"].Present || !item.Fields["/amount_cents"].Present || item.Fields["/id"].Value != 1000+position || item.Fields["/amount_cents"].Value != 25000+position {
					return llm.Response{}, fmt.Errorf("model received a skipped, repeated or invented row at position %d", position)
				}
				seen = append(seen, item.Fields["/id"].Value)
			}
			if page.Next == nil || *page.Next != page.Offset+page.Shown {
				return llm.Response{}, fmt.Errorf("next_offset did not reflect actual delivered rows")
			}
			next = *page.Next
		}
		if len(seen) >= 5 {
			return llm.Response{StopReason: llm.StopEndTurn, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "done"}}}}, nil
		}
		return call(tools.ToolResultReadName, map[string]any{"result_id": id, "view": "query", "pointer": "/rows", "offset": next, "limit": 5 - len(seen), "select": []string{"/id", "/amount_cents"}}), nil
	}
	a := New(Config{Client: client, Registry: registry, Citations: true, MaxTurns: 12, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: 384, BatchTokens: 2048}})
	events := drain(a.Run(context.Background(), "Read the first five stored rows in original order."))
	if end := events[len(events)-1]; end.Err != nil || len(seen) != 5 || len(action.callArgs()) != 1 {
		t.Fatalf("paging did not recover the first five with one original action: seen=%v calls=%d end=%+v", seen, len(action.callArgs()), end)
	}
	adapted := false
	for _, message := range a.Messages() {
		for _, block := range message.Content {
			model, read := modelPages[block.ToolUseID]
			if !read {
				continue
			}
			if block.ResultID != "" {
				t.Fatal("reading a saved page generated a result-of-result identity")
			}
			adapted = adapted || model != block.Text
			for _, event := range events {
				if event.Kind == EventToolEnd && event.ToolCallID == block.ToolUseID && event.ToolOutput != StripCiteHeader(block.Text) {
					t.Fatal("model-only paging changed the original UI/history observation")
				}
			}
		}
	}
	if !adapted {
		t.Fatal("fixture did not exercise a genuinely reduced reader page")
	}
}
