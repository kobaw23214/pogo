package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func deliveryResultBlocks(history []llm.Message) []llm.Block {
	var results []llm.Block
	for _, message := range history {
		for _, block := range message.Content {
			if block.Type == llm.BlockToolResult {
				results = append(results, block)
			}
		}
	}
	return results
}

func deliveryRecoverAllText(t *testing.T, registry *tools.Registry, id string) string {
	t.Helper()
	reader, ok := registry.Get(tools.ToolResultReadName)
	if !ok {
		t.Fatal("result reader is missing")
	}
	var all strings.Builder
	for offset, pages := 0, 0; pages < 1000; pages++ {
		args, _ := json.Marshal(map[string]any{"result_id": id, "view": "text", "offset": offset, "limit": 32768})
		page, err := reader.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("recover stored text at offset %d: %v", offset, err)
		}
		_, body, found := strings.Cut(page.Text, "\n---\n")
		details, ok := page.Details.(tools.ToolResultReadDetails)
		if !found || !ok || details.Offset != offset {
			t.Fatal("raw recovery lost its authoritative text range")
		}
		all.WriteString(body)
		if details.NextOffset == nil {
			if !details.Complete || details.Total != all.Len() {
				t.Fatal("terminal recovery page did not cover saved text")
			}
			return all.String()
		}
		if *details.NextOffset != offset+len(body) || *details.NextOffset <= offset {
			t.Fatal("raw recovery skipped or repeated saved bytes")
		}
		offset = *details.NextOffset
	}
	t.Fatal("saved-text recovery did not terminate")
	return ""
}

type deliveryModelClient struct {
	*fakeClient
	model string
}

func (c *deliveryModelClient) Model() string { return c.model }

// This checks the actual request sent after an executed action, while comparing
// the UI event and persisted source metadata against the ordinary prepared view.
func TestInitialResultDeliveryThroughAgentKeepsHistoryAndRecovery(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"json", "log"} {
		t.Run(kind, func(t *testing.T) {
			full := "LOG_HEAD_SHOULD_NOT_BE_SAMPLED\n" + strings.Repeat("saved log body\n", 3000) + "MIDDLE_RAW_EVIDENCE\n" + strings.Repeat("saved log tail\n", 3000)
			var structured json.RawMessage
			if kind == "json" {
				structured, _ = json.Marshal(map[string]any{
					"rows": []any{
						map[string]any{"name": "FIRST_ROW_SHOULD_NOT_BE_SAMPLED", "body": strings.Repeat("large saved row ", 5000)},
						map[string]any{"name": "MIDDLE_RAW_EVIDENCE", "amount": 237},
					},
					"has_more": true,
				})
				full = string(structured)
			}
			detail := tools.MCPDetails{Server: "delivery-test", Tool: kind}
			output := tools.Result{Text: full, Details: detail, Payload: &tools.ResultPayload{Text: full, Structured: structured}}
			action := &fakeTool{name: "delivery_" + kind, run: func(context.Context, json.RawMessage) (tools.Result, error) { return output, nil }}
			registry, options := budgetRegistryForTest(t, action)
			// Content-addressed preparation gives an independent comparison for
			// history/UI; it does not execute the original side-effecting tool.
			prepared := registry.PrepareResult(ctx, action.name, output, nil)
			client := &deliveryModelClient{fakeClient: &fakeClient{responses: []llm.Response{toolCalls(action.name)}}, model: "glm-delivery-fixture"}
			cfg := ToolOutputBudgetConfig{ContextWindow: 128000, MaxOutputTokens: 1000, PerResultTokens: 8192, BatchTokens: 24576}
			a := New(Config{Client: client, Registry: registry, Citations: true, ToolOutputBudget: cfg})
			events := drain(a.Run(ctx, "Keep this observation for later; do not analyze it yet."))
			if end := events[len(events)-1]; end.Err != nil {
				t.Fatalf("first delivery failed: %+v", end)
			}
			stored, sent := deliveryResultBlocks(a.Messages()), deliveryResultBlocks(client.lastSeen())
			if len(stored) != 1 || len(sent) != 1 || stored[0].ResultID != prepared.ResultID || sent[0].ResultID != prepared.ResultID {
				t.Fatal("first model request lost the original action/result identity")
			}
			originalPrefix, originalBody := toolCitationPrefix(stored[0])
			modelPrefix, modelBody := toolCitationPrefix(sent[0])
			if originalPrefix == "" || modelPrefix != originalPrefix || originalBody != prepared.Text || !reflect.DeepEqual(stored[0].Source, sent[0].Source) || !reflect.DeepEqual(stored[0].Details, sent[0].Details) {
				t.Fatal("model delivery changed the original source/header, UI details or history body")
			}
			if !strings.Contains(modelBody, `"delivery_mode":"reference"`) || !strings.Contains(modelBody, "tool_result") || modelBody == originalBody {
				t.Fatal("a result that already fits the token cap bypassed reference-first delivery")
			}
			for _, forbidden := range []string{"MIDDLE_RAW_EVIDENCE", "FIRST_ROW_SHOULD_NOT_BE_SAMPLED", "LOG_HEAD_SHOULD_NOT_BE_SAMPLED", "items_sample", "observation_shape_and_samples"} {
				if strings.Contains(modelBody, forbidden) {
					t.Fatalf("first model request exposed a sample or unread body: %q", forbidden)
				}
			}
			toolEnds := eventsOf(events, EventToolEnd)
			if len(toolEnds) != 1 || toolEnds[0].ToolOutput != prepared.Text || toolEnds[0].Source == nil || len(action.callArgs()) != 1 {
				t.Fatal("model-only delivery changed the UI event or replayed the original action")
			}
			if got := deliveryRecoverAllText(t, registry, sent[0].ResultID); got != full {
				t.Fatal("reference-first did not retain every original byte")
			}
			history := a.Messages()
			before, _ := json.Marshal(history)
			// SetMessages reconstructs the same first-delivery policy using a
			// fresh registry against the original durable host-selected scope.
			resumed := New(Config{Client: &fakeClient{}, Registry: tools.New(options), Citations: true, ToolOutputBudget: cfg})
			resumed.SetMessages(history)
			resumed.ensureToolHistoryBudgets(ctx)
			reopened := deliveryResultBlocks(resumed.cachedToolPrompt(resumed.Messages()))
			if len(reopened) != 1 || reopened[0].Text != sent[0].Text || reopened[0].ResultID != sent[0].ResultID || !reflect.DeepEqual(reopened[0].Source, sent[0].Source) {
				t.Fatal("reopening a saved conversation changed its delivery strategy or citation identity")
			}
			// A smaller second model tightens the existing reference without
			// resurrecting arbitrary samples from the original JSON/log body.
			second := &deliveryModelClient{fakeClient: &fakeClient{}, model: "kimi-delivery-fixture"}
			resumed.SetClient(second)
			resumed.SetToolOutputBudget(ToolOutputBudgetConfig{PerResultTokens: 128, BatchTokens: 128})
			resumed.ensureToolHistoryBudgets(ctx)
			tight := deliveryResultBlocks(resumed.cachedToolPrompt(resumed.Messages()))[0]
			tightPrefix, tightBody := toolCitationPrefix(tight)
			if tightPrefix != originalPrefix || tight.ResultID != prepared.ResultID || estimatedToolText(tight.Text) > 128 || !strings.Contains(tightBody, `"delivery_mode":"reference"`) || strings.Contains(tightBody, "items_sample") || strings.Contains(tightBody, "MIDDLE_RAW_EVIDENCE") {
				t.Fatalf("model switch regressed reference-first or lost its citation: %s", tight.Text)
			}
			after, _ := json.Marshal(resumed.Messages())
			if string(before) != string(after) || len(action.callArgs()) != 1 {
				t.Fatal("resume/model switch changed persisted history or replayed an action")
			}
		})
	}
}

func TestInitialResultDeliveryReaderPageStaysAuthoritative(t *testing.T) {
	ctx := context.Background()
	_, options := budgetRegistryForTest(t)
	options.ResultDelivery = tools.ResultDeliveryPolicy{Mode: tools.ResultDeliveryReference}
	registry := tools.New(options)
	full := strings.Repeat("not requested\n", 5000) + "REQUESTED_EXACT_BODY\n" + strings.Repeat("later\n", 5000)
	prepared := registry.PrepareResult(ctx, "external_mcp", tools.Result{Text: full}, nil)
	args, _ := json.Marshal(map[string]any{
		"result_id": prepared.ResultID, "view": "text", "offset": strings.Index(full, "REQUESTED_EXACT_BODY"), "limit": len("REQUESTED_EXACT_BODY\n"),
	})
	reader, _ := registry.Get(tools.ToolResultReadName)
	expected, err := reader.Execute(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{responses: []llm.Response{{StopReason: llm.StopToolUse, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: "exact-read", Name: tools.ToolResultReadName, Input: args}}}}}}
	a := New(Config{Client: client, Registry: registry, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: 8192, BatchTokens: 24576}})
	events := drain(a.Run(ctx, "Read the requested saved bytes."))
	if end := events[len(events)-1]; end.Err != nil {
		t.Fatalf("saved-result read failed: %+v", end)
	}
	sent := deliveryResultBlocks(client.lastSeen())
	if len(sent) != 1 || sent[0].Text != expected.Text || sent[0].ResultID != "" || !strings.Contains(sent[0].Text, "REQUESTED_EXACT_BODY") || strings.Contains(sent[0].Text, `"delivery_mode":"reference"`) {
		t.Fatal("global reference preference replaced a requested reader page or created a result chain")
	}
}

func TestInitialResultDeliveryHostInlinePreferenceStillRespectsModelBudget(t *testing.T) {
	ctx := context.Background()
	full := "INLINE_PREFERRED_BODY\n" + strings.Repeat("large full inline log\n", 4000)
	action := &fakeTool{name: "inline_delivery_log", run: func(context.Context, json.RawMessage) (tools.Result, error) { return tools.Result{Text: full}, nil }}
	_, options := budgetRegistryForTest(t, action)
	options.ResultDelivery = tools.ResultDeliveryPolicy{Mode: tools.ResultDeliveryInline}
	registry := tools.New(options)
	client := &fakeClient{responses: []llm.Response{toolCalls(action.name)}}
	a := New(Config{Client: client, Registry: registry, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: 32768, BatchTokens: 32768}})
	events := drain(a.Run(ctx, "Keep the host-selected inline observation."))
	if end := events[len(events)-1]; end.Err != nil {
		t.Fatal(end.Err)
	}
	sent, stored := deliveryResultBlocks(client.lastSeen()), deliveryResultBlocks(a.Messages())
	if len(sent) != 1 || len(stored) != 1 || !strings.Contains(sent[0].Text, full) || sent[0].ResultID != stored[0].ResultID {
		t.Fatal("trusted inline preference was ignored despite available model space")
	}
	before, _ := json.Marshal(a.Messages())
	a.SetToolOutputBudget(ToolOutputBudgetConfig{PerResultTokens: 256, BatchTokens: 256})
	a.ensureToolHistoryBudgets(ctx)
	tight := deliveryResultBlocks(a.cachedToolPrompt(a.Messages()))[0]
	if estimatedToolText(tight.Text) > 256 || tight.ResultID != stored[0].ResultID || strings.Contains(tight.Text, full) {
		t.Fatalf("host inline preference bypassed the context budget: %s", tight.Text)
	}
	after, _ := json.Marshal(a.Messages())
	if string(before) != string(after) || len(action.callArgs()) != 1 || deliveryRecoverAllText(t, registry, tight.ResultID) != full {
		t.Fatal("budgeting an inline preference lost original history, data or execution identity")
	}
}
