package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func citationRepairEvents(events []Event) []*CitationRepairStatus {
	var reviews []*CitationRepairStatus
	for _, event := range events {
		if event.CitationRepair != nil {
			reviews = append(reviews, event.CitationRepair)
		}
	}
	return reviews
}

func TestStructuredCitationReadGetsOneSchemaPreservingRewrite(t *testing.T) {
	for _, fence := range []string{"", "```json\n", "~~~json\n"} {
		t.Run(fence, func(t *testing.T) {
			wrap := func(body string) string {
				if fence == "" {
					return body
				}
				return fence + body + "\n" + fence[:3]
			}
			original := wrap(`{"commits":10,"quote":"The project has 10 commits.","sources":[]}`)
			corrected := wrap(`{"sources":["[1](#cite:S1)"],"quote":"The project has 10 commits.","commits":10}`)
			c := &citationClient{fakeClient: fakeClient{responses: []llm.Response{toolCalls("read"), citationAnswer(original), citationAnswer(corrected)}}}
			a := New(Config{Client: c, Registry: tools.NewRegistry(citationReadTool()), Citations: true})
			events := citeRun(t, a, "Return the factual result as JSON with sources.")
			reviews := citationRepairEvents(events)
			if c.callCount() != 3 || len(c.schemas[2]) != 0 || len(reviews) != 1 || !reviews[0].Attempted || !reviews[0].Accepted || reviews[0].Reason != "" {
				t.Fatalf("structured citation repair was not one bounded text-only request: %+v", reviews)
			}
			steers := eventsOf(events, EventSteer)
			if len(steers) != 1 || !strings.Contains(steers[0].Text, "actual usable source headers: S1") || !strings.Contains(steers[0].Text, "same JSON object") {
				t.Fatalf("repair lost actual labels or the JSON contract: %+v", steers)
			}
			if got := a.messages[len(a.messages)-1].Text(); got != corrected {
				t.Fatalf("complete structured answer lost: %q", got)
			}
		})
	}
}

func TestStructuredCitationRepairRejectsMissingSourcesAndDataDrift(t *testing.T) {
	original := `{"commits":10,"quote":"The project has 10 commits.","sources":[]}`
	for _, test := range []struct{ name, answer, reason string }{
		{"still_empty", original, "structured_sources_missing_or_invalid"},
		{"sources_null", `{"commits":10,"quote":"The project has 10 commits.","sources":null}`, "structured_format_changed"},
		{"null_source_item", `{"commits":10,"quote":"The project has 10 commits.","sources":[null]}`, "structured_format_changed"},
		{"count_changed", `{"commits":11,"quote":"The project has 10 commits.","sources":["[1](#cite:S1)"]}`, "structured_answer_changed"},
		{"type_changed", `{"commits":"10","quote":"The project has 10 commits.","sources":["[1](#cite:S1)"]}`, "structured_answer_changed"},
		{"quote_changed", `{"commits":10,"quote":"The project has 11 commits.","sources":["[1](#cite:S1)"]}`, "structured_answer_changed"},
		{"key_added", `{"commits":10,"quote":"The project has 10 commits.","sources":["[1](#cite:S1)"],"extra":true}`, "structured_answer_changed"},
		{"key_removed", `{"commits":10,"sources":["[1](#cite:S1)"]}`, "structured_answer_changed"},
		{"prose_instead", `The project has 10 commits. [1](#cite:S1)`, "structured_format_changed"},
		{"trailing_prose", `{"commits":10,"quote":"The project has 10 commits.","sources":["[1](#cite:S1)"]} Done.`, "structured_format_changed"},
		{"unknown_label", `{"commits":10,"quote":"The project has 10 commits.","sources":["[1](#cite:S999)"]}`, "structured_sources_missing_or_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := &citationClient{fakeClient: fakeClient{responses: []llm.Response{toolCalls("read"), citationAnswer(original), citationAnswer(test.answer)}}}
			a := New(Config{Client: c, Registry: tools.NewRegistry(citationReadTool()), Citations: true})
			events := citeRun(t, a, "Return the factual result as JSON with sources.")
			reviews := citationRepairEvents(events)
			if c.callCount() != 3 || len(reviews) != 1 || reviews[0].Accepted || reviews[0].Reason != test.reason || a.messages[len(a.messages)-1].Text() != original {
				t.Fatalf("invalid structured rewrite: calls=%d review=%+v final=%q", c.callCount(), reviews[0], a.messages[len(a.messages)-1].Text())
			}
		})
	}
}

func TestStructuredCitationOnlyDeclaredSourceStringsAreCitations(t *testing.T) {
	store := citedStore(t, "S1")
	valid := "```json\n" + `{"quote":"Example [1](#cite:S999) and [p2] are source data.","sources":["[1](#cite:S1)"]}` + "\n```"
	if invalid := ValidateCitations(valid, store); len(invalid) != 0 || len(FindPageMarkers(valid)) != 0 {
		t.Fatalf("quoted JSON source data became live anchors: %v", invalid)
	}
	invalid := "```json\n" + `{"value":10,"sources":["[1](#cite:S999)"]}` + "\n```"
	if got := ValidateCitations(invalid, store); len(got) != 1 || got[0] != "S999" {
		t.Fatalf("standalone fenced JSON citation escaped validation: %v", got)
	}
	for _, answer := range []string{valid, `{"commits":10}`, `[{"commits":10}]`, `{"quote":"example [1](#cite:S999)","sources":null}`, `{"sources":[null]}`, `{"sources":[{"example":"[1](#cite:S999)"}]}`, `{"quote":"literal [1](#cite:S999) without a sources field"}`, "```go\nvar sources = []string{}\n```"} {
		c := &fakeClient{responses: []llm.Response{toolCalls("read"), citationAnswer(answer)}}
		a := New(Config{Client: c, Registry: tools.NewRegistry(citationReadTool()), Citations: true})
		if events := citeRun(t, a, "Read the material."); c.callCount() != 2 || len(eventsOf(events, EventSteer)) != 0 {
			t.Fatalf("valid answer or unrelated schema caused a repair: %q", answer)
		}
	}
}

// This uses the real result store/native reader plus the same host-only JSON
// checkpoint round trip used after compaction. The model learns S2 only from
// the newly read view, never from a fabricated label in the test prompt.
func nativeCitationCheckpoint(t *testing.T, text ...string) (*tools.Registry, []llm.Message, string) {
	t.Helper()
	registry, options := budgetRegistryForTest(t)
	body := "observed row value=10\nsecond row value=20\n"
	if len(text) > 0 {
		body = text[0]
	}
	structured, _ := json.Marshal(map[string]any{"rows": []map[string]int{{"value": 10}, {"value": 20}}, "body": body})
	prepared := registry.PrepareResult(context.Background(), "browser", tools.Result{Text: body,
		Payload: &tools.ResultPayload{Text: body, Structured: structured}}, nil)
	if !sourceResultID(prepared.ResultID) {
		t.Fatal("real observation was not retained")
	}
	first := New(Config{Client: &fakeClient{}, Registry: registry, Citations: true})
	original := first.sources.Next("browser", map[string]any{"url": "https://example.com/report", "title": "Observed report"}, "original-observation", body, nil)
	first.sources.bindResult(prepared.ResultID, original)
	checkpoint, err := first.compactSourceCheckpoint("An observation was saved for a later question.")
	if err != nil {
		t.Fatal(err)
	}
	message := llm.UserText("Continue the previous conversation.")
	message.SourceState = checkpoint
	raw, _ := json.Marshal([]llm.Message{message})
	var restored []llm.Message
	if json.Unmarshal(raw, &restored) != nil {
		t.Fatal("checkpoint JSON round trip failed")
	}
	return tools.New(options), restored, prepared.ResultID
}

func TestStructuredCitationNativeDerivedReadsAfterCheckpointAreMaterial(t *testing.T) {
	for _, view := range []string{"search", "query"} {
		t.Run(view, func(t *testing.T) {
			registry, history, id := nativeCitationCheckpoint(t)
			args := map[string]any{"result_id": id, "view": view}
			if view == "search" {
				args["pattern"] = "observed row"
			} else {
				args["pointer"], args["aggregate"] = "/rows", map[string]string{"op": "count"}
			}
			call := toolCalls(tools.ToolResultReadName)
			call.Message.Content[0].Input, _ = json.Marshal(args)
			original, corrected := `{"value":10,"sources":[]}`, `{"value":10,"sources":["[1](#cite:S2)"]}`
			c := &citationClient{fakeClient: fakeClient{responses: []llm.Response{call, citationAnswer(original), citationAnswer(corrected)}}}
			a := New(Config{Client: c, Registry: registry, Citations: true})
			a.SetMessages(history)
			events := citeRun(t, a, "Inspect the saved observation and return JSON with sources.")
			reviews := citationRepairEvents(events)
			if c.callCount() != 3 || len(reviews) != 1 || !reviews[0].Accepted || a.messages[len(a.messages)-1].Text() != corrected {
				t.Fatalf("native derived %s read did not opt into source review: %+v", view, reviews)
			}
			ends := eventsOf(events, EventToolEnd)
			if len(ends) != 1 || ends[0].Source.Label != "S2" || ends[0].Source.SavedResult.Origin.Label != "S1" {
				t.Fatal("citation correction re-executed an observation or lost the parent")
			}
			visible := false
			for _, message := range c.seen[1] {
				for _, block := range message.Content {
					visible = visible || block.Type == llm.BlockToolResult && strings.Contains(block.Text, "[cite:S2 | tool_result |")
				}
			}
			if !visible {
				t.Fatal("the repair relied on a label absent from actual tool history")
			}
		})
	}
}

func TestStructuredCitationNativeDirectoriesAndOmissionsDoNotForceReview(t *testing.T) {
	for _, args := range []map[string]any{{"view": "summary"}, {"view": "native"}, {"view": "search", "pattern": "absent"}} {
		registry, history, id := nativeCitationCheckpoint(t)
		args["result_id"] = id
		call := toolCalls(tools.ToolResultReadName)
		call.Message.Content[0].Input, _ = json.Marshal(args)
		c := &fakeClient{responses: []llm.Response{call, citationAnswer(`{"status":"metadata only","sources":[]}`)}}
		a := New(Config{Client: c, Registry: registry, Citations: true})
		a.SetMessages(history)
		if events := citeRun(t, a, "Inspect status."); c.callCount() != 2 || len(citationRepairEvents(events)) != 0 || len(eventsOf(events, EventSteer)) != 0 {
			t.Fatalf("directory or empty-search metadata was treated as body evidence: %+v", args)
		}
	}
}

func TestStructuredCitationBudgetReceiptIsNotDeliveredMaterial(t *testing.T) {
	for _, view := range []string{"search", "text", "json"} {
		t.Run(view, func(t *testing.T) {
			registry, history, id := nativeCitationCheckpoint(t, "observed row value=10 "+strings.Repeat("long body ", 700)+"\n")
			call := toolCalls(tools.ToolResultReadName)
			args := map[string]any{"result_id": id, "view": view}
			if view == "search" {
				args["pattern"] = "observed row"
			} else if view == "json" {
				args["pointer"] = "/body"
			}
			call.Message.Content[0].Input, _ = json.Marshal(args)
			c := &fakeClient{responses: []llm.Response{call, citationAnswer(`{"status":"page omitted","sources":[]}`)}}
			a := New(Config{Client: c, Registry: registry, Citations: true, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: 128, BatchTokens: 128}})
			a.SetMessages(history)
			events := citeRun(t, a, "Inspect the saved page.")
			omitted := false
			for _, message := range c.seen[1] {
				for _, block := range message.Content {
					omitted = omitted || block.Type == llm.BlockToolResult && strings.Contains(block.Text, `"model_budget_omitted":true`)
				}
			}
			if !omitted || c.callCount() != 2 || len(eventsOf(events, EventSteer)) != 0 {
				t.Fatalf("host rows omitted from actual prompt forced a rewrite: omitted=%t calls=%d", omitted, c.callCount())
			}
		})
	}
}

func TestStructuredCitationDoesNotBorrowPriorRunMaterial(t *testing.T) {
	c := &fakeClient{responses: []llm.Response{toolCalls("read"), citationAnswer(`{"commits":10,"sources":["[1](#cite:S1)"]}`), citationAnswer(`{"status":"new topic","sources":[]}`)}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(citationReadTool()), Citations: true})
	citeRun(t, a, "Return JSON with sources.")
	if events := citeRun(t, a, "Now discuss another topic."); c.callCount() != 3 || len(eventsOf(events, EventSteer)) != 0 {
		t.Fatal("prior-run data forced citation repair in an unrelated JSON answer")
	}
}

func TestStructuredCitationCannotSilentlyRewriteProvenanceOrExceedBudgets(t *testing.T) {
	original := `{"value":10,"explanation":"No usable source labels were provided.","sources":[]}`
	changed := `{"value":10,"explanation":"A usable source label was provided.","sources":["[1](#cite:S1)"]}`
	c := &citationClient{fakeClient: fakeClient{responses: []llm.Response{toolCalls("read"), citationAnswer(original), citationAnswer(changed)}}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(citationReadTool()), Citations: true})
	events := citeRun(t, a, "Return JSON with sources.")
	reviews := citationRepairEvents(events)
	if len(reviews) != 1 || reviews[0].Accepted || reviews[0].Reason != "structured_answer_changed" || a.messages[len(a.messages)-1].Text() != original {
		t.Fatalf("an arbitrary non-sources field was rewritten without recording the unsupported repair: %+v", reviews)
	}
	for _, mode := range []string{"turn", "tokens", "citations_off"} {
		t.Run(mode, func(t *testing.T) {
			answer := citationAnswer(original)
			answer.Usage.Output = 10
			c := &fakeClient{responses: []llm.Response{toolCalls("read"), answer}}
			config := Config{Client: c, Registry: tools.NewRegistry(citationReadTool()), Citations: mode != "citations_off"}
			if mode == "turn" {
				config.MaxTurns = 2
			} else if mode == "tokens" {
				config.TokenBudget = 10
			}
			a := New(config)
			if events := citeRun(t, a, "Return JSON with sources."); c.callCount() != 2 || len(citationRepairEvents(events)) != 0 {
				t.Fatal("structured correction exceeded request budget or citation opt-in")
			}
		})
	}
}
