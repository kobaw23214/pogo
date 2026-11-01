package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func evidenceReviewRead(id, callID string, args map[string]any) llm.Response {
	args["result_id"] = id
	call := toolCalls(tools.ToolResultReadName)
	call.Message.Content[0].ID = callID
	call.Message.Content[0].Input, _ = json.Marshal(args)
	return call
}

func quoteReviewAnswer(quote string, count int) string {
	raw, _ := json.Marshal(map[string]any{"count": count, "evidence": []map[string]string{{"quote": quote}}, "sources": []string{"[1](#cite:S2)"}})
	return string(raw)
}

func TestEvidenceQuoteLoopRepairsOnlyVerbatimField(t *testing.T) {
	body := "record=1 note=\"审核需要逐项签字\"\n"
	registry, history, id := nativeCitationCheckpoint(t, body)
	wrong, exact := quoteReviewAnswer("审核需要签字", 1), quoteReviewAnswer("审核需要逐项签字", 1)
	c := &citationClient{fakeClient: fakeClient{responses: []llm.Response{
		evidenceReviewRead(id, "quote-read", map[string]any{"view": "search", "pattern": "record=1"}),
		citationAnswer(wrong), citationAnswer(exact),
	}}}
	a := New(Config{Client: c, Registry: registry, Citations: true, SystemPrompt: CitationSection()})
	a.SetMessages(history)
	events := citeRun(t, a, "Return the observation with its verbatim evidence.")
	end := eventsOf(events, EventAgentEnd)[0]
	if c.callCount() != 3 || len(c.schemas[2]) != 0 || len(eventsOf(events, EventToolEnd)) != 1 ||
		end.QuoteRepair == nil || !end.QuoteRepair.Attempted || !end.QuoteRepair.Accepted ||
		end.QuoteCheck == nil || !end.QuoteCheck.Passed || a.messages[len(a.messages)-1].Text() != exact {
		t.Fatalf("verbatim field was not repaired with one text-only call: end=%+v calls=%d", end, c.callCount())
	}
}

func TestEvidenceQuoteLoopRejectsChangedFactsToolsAndFailedRepair(t *testing.T) {
	for _, mode := range []string{"fact_changed", "tool_call", "still_wrong", "transport"} {
		t.Run(mode, func(t *testing.T) {
			registry, history, id := nativeCitationCheckpoint(t, "record=1 note=\"审核需要逐项签字\"\n")
			wrong := quoteReviewAnswer("审核需要签字", 1)
			replacement := citationAnswer(quoteReviewAnswer("审核需要逐项签字", 1))
			switch mode {
			case "fact_changed":
				replacement = citationAnswer(quoteReviewAnswer("审核需要逐项签字", 2))
			case "tool_call":
				replacement = evidenceReviewRead(id, "must-not-run", map[string]any{"view": "text"})
			case "still_wrong":
				replacement = citationAnswer(wrong)
			}
			c := &citationClient{fakeClient: fakeClient{responses: []llm.Response{
				evidenceReviewRead(id, "quote-read", map[string]any{"view": "search", "pattern": "record=1"}),
				citationAnswer(wrong), replacement,
			}}}
			if mode == "transport" {
				c.failAt = 3
			}
			a := New(Config{Client: c, Registry: registry, Citations: true})
			a.SetMessages(history)
			events := citeRun(t, a, "Return evidence.")
			end := eventsOf(events, EventAgentEnd)[0]
			if c.callCount() != 3 || len(eventsOf(events, EventToolEnd)) != 1 || end.EndReason != EndCompleted ||
				end.QuoteRepair == nil || end.QuoteRepair.Accepted || end.QuoteRepair.Reason == "" ||
				a.messages[len(a.messages)-1].Text() != wrong {
				t.Fatalf("failed optional repair corrupted data or executed a tool: %+v", end)
			}
		})
	}
}

func TestEvidenceQuoteLoopDoesNotExceedTurnBudget(t *testing.T) {
	registry, history, id := nativeCitationCheckpoint(t, "record=1 note=\"审核需要逐项签字\"\n")
	c := &citationClient{fakeClient: fakeClient{responses: []llm.Response{
		evidenceReviewRead(id, "quote-read", map[string]any{"view": "search", "pattern": "record=1"}),
		citationAnswer(quoteReviewAnswer("审核需要签字", 1)),
	}}}
	a := New(Config{Client: c, Registry: registry, Citations: true, MaxTurns: 2})
	a.SetMessages(history)
	events := citeRun(t, a, "Return evidence.")
	end := eventsOf(events, EventAgentEnd)[0]
	if c.callCount() != 2 || end.QuoteRepair != nil || end.QuoteCheck == nil || end.QuoteCheck.Passed || !end.QuoteCheck.Verifiable {
		t.Fatalf("budget hid failed quote verification or allowed an extra request: %+v", end)
	}
}

func TestEvidenceCoverageLoopCanReadNextPageOrLimitConclusion(t *testing.T) {
	for _, finishPage := range []bool{false, true} {
		t.Run(fmt.Sprint(finishPage), func(t *testing.T) {
			var body strings.Builder
			for i := 0; i < 21; i++ {
				fmt.Fprintf(&body, "row=%d observed=true\n", i)
			}
			registry, history, id := nativeCitationCheckpoint(t, body.String())
			responses := []llm.Response{
				evidenceReviewRead(id, "page-one", map[string]any{"view": "search", "pattern": "row=", "limit": 20, "context_lines": 0}),
				citationAnswer(`{"coverage":"Every matching row was observed.","sources":["[1](#cite:S2)"]}`),
			}
			if finishPage {
				responses = append(responses,
					evidenceReviewRead(id, "page-two", map[string]any{"view": "search", "pattern": "row=", "offset": 20, "limit": 20, "context_lines": 0}),
					citationAnswer(`{"coverage":"All 21 matching rows were observed.","sources":["[1](#cite:S2)","[1](#cite:S3)"]}`))
			} else {
				responses = append(responses, citationAnswer(`{"coverage":"Only the first 20 matching rows were observed; the remaining row is unverified.","sources":["[1](#cite:S2)"]}`))
			}
			c := &citationClient{fakeClient: fakeClient{responses: responses}}
			a := New(Config{Client: c, Registry: registry, Citations: true})
			a.SetMessages(history)
			events := citeRun(t, a, "Summarize the observation.")
			if c.callCount() != len(responses) || len(eventsOf(events, EventSteer)) != 1 || len(c.schemas[2]) == 0 {
				t.Fatalf("coverage review looped or prevented a necessary continuation: calls=%d events=%+v", c.callCount(), events)
			}
			expectedReads := 1
			if finishPage {
				expectedReads++
			}
			if len(eventsOf(events, EventToolEnd)) != expectedReads {
				t.Fatal("checkpoint forced unnecessary pagination or repeated the original observation")
			}
		})
	}
}

func TestEvidenceCoverageLoopExhaustedBudgetKeepsCompletedAnswer(t *testing.T) {
	registry, history, id := nativeCitationCheckpoint(t, strings.Repeat("row observed=true\n", 21))
	answer := citationAnswer(`{"coverage":"Only the displayed first 20 matches were observed.","sources":["[1](#cite:S2)"]}`)
	answer.Usage = llm.Usage{Input: 11}
	c := &citationClient{fakeClient: fakeClient{responses: []llm.Response{
		evidenceReviewRead(id, "page-one", map[string]any{"view": "search", "pattern": "row", "limit": 20}), answer,
	}}}
	a := New(Config{Client: c, Registry: registry, Citations: true, TokenBudget: 10})
	a.SetMessages(history)
	events := citeRun(t, a, "Report the observed subset.")
	end := eventsOf(events, EventAgentEnd)[0]
	if c.callCount() != 2 || end.EndReason != EndCompleted || len(eventsOf(events, EventSteer)) != 0 ||
		a.messages[len(a.messages)-1].Text() != answer.Message.Text() {
		t.Fatalf("optional coverage check discarded a completed bounded answer: %+v", end)
	}
}

func TestEvidenceCoverageCheckpointRetainsFullJSONAfterAcknowledgement(t *testing.T) {
	for _, replacement := range []string{
		"The coverage was checked; the previous strict JSON remains unchanged.",
		`{"coverage":"only observed rows","sources":["[1](#cite:S2)"]}`,
		`{"coverage":"only observed rows","count":"20","sources":["[1](#cite:S2)"]}`,
	} {
		t.Run(replacement[:min(25, len(replacement))], func(t *testing.T) {
			registry, history, id := nativeCitationCheckpoint(t, strings.Repeat("row observed=true\n", 21))
			original := `{"coverage":"Only the first 20 matches were observed.","count":20,"sources":["[1](#cite:S2)"]}`
			c := &citationClient{fakeClient: fakeClient{responses: []llm.Response{
				evidenceReviewRead(id, "page-one", map[string]any{"view": "search", "pattern": "row", "limit": 20}),
				citationAnswer(original), citationAnswer(replacement),
			}}}
			c.deltas = func(n int, emit func(llm.Delta)) {
				if n == 3 && emit != nil {
					emit(llm.Delta{Kind: llm.DeltaText, Text: "unaccepted checkpoint prose"})
				}
			}
			a := New(Config{Client: c, Registry: registry, Citations: true})
			a.SetMessages(history)
			events := citeRun(t, a, "Return the complete requested JSON.")
			end := eventsOf(events, EventAgentEnd)[0]
			if c.callCount() != 3 || a.messages[len(a.messages)-1].Text() != original || end.EvidenceCoverage == nil ||
				!end.EvidenceCoverage.CheckpointAnswerRestored || end.EvidenceCoverage.IncompleteSelections != 1 {
				t.Fatalf("checkpoint replaced the complete answer or certified partial coverage: %+v", end)
			}
			for _, event := range eventsOf(events, EventTextDelta) {
				if strings.Contains(event.Text, "unaccepted checkpoint prose") {
					t.Fatal("rejected checkpoint acknowledgement leaked to the UI")
				}
			}
		})
	}
}

func TestEvidenceCheckpointFormatGuardProtectsNestedTypes(t *testing.T) {
	original := `{"evidence":[{"quote":"literal","count":1}],"coverage":"first page","sources":["[1](#cite:S2)"]}`
	for _, changed := range []string{
		`{"evidence":[{"quote":"literal","count":"1"}],"coverage":"first page","sources":["[1](#cite:S2)"]}`,
		`{"evidence":[],"coverage":"first page","sources":["[1](#cite:S2)"]}`,
		`{"evidence":[{"quote":"literal","count":1}],"coverage":null,"sources":["[1](#cite:S2)"]}`,
		`{"evidence":[{"quote":"literal","count":1}],"coverage":"first page","sources":[]}`,
	} {
		if evidenceCheckpointFormatMatches(original, changed) {
			t.Fatalf("changed schema escaped the optional-checkpoint guard: %s", changed)
		}
	}
	valid := `{"evidence":[{"quote":"exact new evidence","count":2}],"coverage":"both pages","sources":["[1](#cite:S2)","[1](#cite:S3)"]}`
	if !evidenceCheckpointFormatMatches(original, valid) {
		t.Fatal("schema-preserving full answer after necessary new evidence was rejected")
	}
}

func TestEvidenceCoverageCheckpointTruncationKeepsJSONAndFailure(t *testing.T) {
	registry, history, id := nativeCitationCheckpoint(t, strings.Repeat("row observed=true\n", 21))
	original := `{"coverage":"Only the first 20 matches were observed.","sources":["[1](#cite:S2)"]}`
	truncated := citationAnswer(`{"coverage":"Only the first`)
	truncated.StopReason = llm.StopMaxTokens
	truncated.Usage = llm.Usage{Input: 31, Output: 7}
	c := &citationClient{fakeClient: fakeClient{responses: []llm.Response{
		evidenceReviewRead(id, "page-one", map[string]any{"view": "search", "pattern": "row", "limit": 20}),
		citationAnswer(original), truncated,
	}}}
	a := New(Config{Client: c, Registry: registry, Citations: true})
	a.SetMessages(history)
	events := citeRun(t, a, "Return complete JSON.")
	end := eventsOf(events, EventAgentEnd)[0]
	if c.callCount() != 3 || a.messages[len(a.messages)-1].Text() != original || end.EndReason != EndMaxTokens ||
		end.StopReason != llm.StopMaxTokens || end.Usage.Input != 31 || end.Usage.Output != 7 ||
		end.EvidenceCoverage == nil || !end.EvidenceCoverage.CheckpointAnswerRestored ||
		end.EvidenceCoverage.CheckpointFailure != "checkpoint_output_incomplete" {
		t.Fatalf("truncated checkpoint corrupted the answer, hid spend or became a false success: %+v", end)
	}
}
