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

type quoteFixture struct {
	a        *Agent
	registry *tools.Registry
	ledger   *evidenceLedger
	next     int
}

func newQuoteFixture(t *testing.T) *quoteFixture {
	t.Helper()
	registry, _ := budgetRegistryForTest(t)
	a := New(Config{Client: &fakeClient{}, Registry: registry, Citations: true})
	return &quoteFixture{a: a, registry: registry, ledger: newEvidenceLedger(a)}
}

func (f *quoteFixture) save(t *testing.T, text string, structured, native any) string {
	t.Helper()
	payload := &tools.ResultPayload{Text: text}
	if structured != nil {
		payload.Structured, _ = json.Marshal(structured)
	}
	if native != nil {
		payload.Native, _ = json.Marshal(native)
	}
	prepared := f.registry.PrepareResult(context.Background(), "browser", tools.Result{Text: text, Payload: payload}, nil)
	if !sourceResultID(prepared.ResultID) {
		t.Fatal("test observation was not saved by the real registry")
	}
	f.next++
	source := f.a.sources.Next("browser", map[string]any{"url": "https://example.com/source", "title": "Observed source"}, fmt.Sprintf("observation-%d", f.next), text, nil)
	f.a.sources.bindResult(prepared.ResultID, source)
	return prepared.ResultID
}

func (f *quoteFixture) read(t *testing.T, args map[string]any, budget int64) []llm.Message {
	t.Helper()
	f.next++
	raw, _ := json.Marshal(args)
	call := llm.Block{Type: llm.BlockToolUse, Name: tools.ToolResultReadName, ID: fmt.Sprintf("native-read-%d", f.next), Input: raw}
	reader, _ := f.registry.Get(call.Name)
	result, err := reader.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	detail, ok := result.Details.(tools.ToolResultReadDetails)
	if !ok {
		t.Fatal("native reader did not return its host receipt")
	}
	source := f.a.sources.Next(call.Name, detail, call.ID, result.Text, raw)
	if source == nil {
		t.Fatal("test body has no trusted original source")
	}
	body := result.Text
	if budget > 0 {
		view := f.registry.BudgetResultView(context.Background(), tools.ModelResultBudgetRequest{Tool: call.Name, Text: body, Details: marshalDetails(detail), Args: raw, Tokens: budget})
		body = view.Text
	}
	block := llm.Block{Type: llm.BlockToolResult, ToolUseID: call.ID, Text: fmt.Sprintf("[cite:%s | tool_result | stored view]\n%s", source.Label, body), Source: marshalSource(source), Details: marshalDetails(detail)}
	prompt := []llm.Message{{Role: llm.RoleAssistant, Content: []llm.Block{call}}, {Role: llm.RoleUser, Content: []llm.Block{block}}}
	f.ledger.Register([]llm.Block{call})
	// This is the exact native/budgeted prompt a successful scripted request
	// would have received, not the UI page or a verifier's read from storage.
	f.ledger.ObservePrompt(prompt)
	return prompt
}

func quoteAnswer(quote string) string {
	raw, _ := json.Marshal(map[string]any{"value": 10, "description": "A description may paraphrase.", "evidence": []any{map[string]any{"id": "first", "quote": quote}}, "sources": []string{"[1](#cite:S2)"}})
	return string(raw)
}

func TestEvidenceQuotesLiteralUnicodeAndNarrowOptIn(t *testing.T) {
	f := newQuoteFixture(t)
	line := "Policy: 必须使用原始证据，不能改写。"
	id := f.save(t, line+"\n", nil, nil)
	f.read(t, map[string]any{"result_id": id, "view": "search", "pattern": "Policy:", "context_lines": 0}, 0)
	for _, wrap := range []func(string) string{func(s string) string { return s }, func(s string) string { return "```json\n" + s + "\n```" }, func(s string) string { return "~~~json\n" + s + "\n~~~" }} {
		if check := CheckEvidenceQuotes(wrap(quoteAnswer(line)), f.ledger); !check.OptedIn || !check.Passed || !check.Verifiable || len(check.Paths) != 0 {
			t.Fatalf("actual Unicode excerpt was not accepted: %+v", check)
		}
	}
	for _, changed := range []string{"Policy: 必须原始证据，不能改写。", "Policy: 必须使用原始证据,不能改写。", "Policy:  必须使用原始证据，不能改写。", ""} {
		check := CheckEvidenceQuotes(quoteAnswer(changed), f.ledger)
		if !check.OptedIn || check.Passed || !check.Verifiable || len(check.Paths) != 1 || check.Paths[0] != "/evidence/0/quote" || EvidenceQuoteRepairNotice(check) == "" {
			t.Fatalf("nonliteral excerpt was normalized or silently approved: %+v", check)
		}
		if strings.Contains(EvidenceQuoteRepairNotice(check), changed) && changed != "" {
			t.Fatal("untrusted quote content was inserted into a user-role correction instruction")
		}
	}
	for _, ordinary := range []string{`{"quote":"a description"}`, `{"evidence":[{"quote":"description"}]}`, `{"sources":null,"evidence":[{"quote":"description"}]}`, `{"sources":[],"evidence":[{"description":"description"}]}`, `{"sources":[],"evidence":[{"quote":null}]}`, `{"sources":[],"evidence":[]}`} {
		if check := CheckEvidenceQuotes(ordinary, f.ledger); check.OptedIn || EvidenceQuoteRepairNotice(check) != "" {
			t.Fatalf("ordinary JSON opted into quote verification: %s / %+v", ordinary, check)
		}
	}
}

func TestEvidenceQuotesOnlySameSourceDomainAndContiguousBytesMayJoin(t *testing.T) {
	for _, test := range []struct {
		name     string
		gap      bool
		separate bool
	}{
		{name: "adjacent_UTF8"}, {name: "one_character_gap", gap: true}, {name: "different_result", separate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newQuoteFixture(t)
			prefix, suffix := "前段中文", "后段原文"
			text := prefix + suffix
			if test.gap {
				text = prefix + "缺" + suffix
			}
			id := f.save(t, text, nil, nil)
			f.read(t, map[string]any{"result_id": id, "view": "text", "offset": 0, "limit": len(prefix)}, 0)
			offset := len(prefix)
			if test.gap {
				offset += len("缺")
			}
			if test.separate {
				id = f.save(t, prefix+suffix+" different", nil, nil)
			}
			f.read(t, map[string]any{"result_id": id, "view": "text", "offset": offset, "limit": len(suffix)}, 0)
			check := CheckEvidenceQuotes(quoteAnswer(prefix+suffix), f.ledger)
			if !check.Verifiable || check.Passed != (!test.gap && !test.separate) {
				t.Fatalf("actual source/range boundary was crossed: %+v", check)
			}
		})
	}
	for _, test := range []struct{ name, view, pointer string }{{"different_JSON_pointer", "json", "/right"}, {"different_domain", "native", "/structuredContent/body"}} {
		t.Run(test.name, func(t *testing.T) {
			f := newQuoteFixture(t)
			left := map[string]any{"left": "headxxxx", "right": "xxxxtail", "structuredContent": map[string]any{"body": "headxxxx"}}
			native := map[string]any{"structuredContent": map[string]any{"body": "xxxxtail"}}
			id := f.save(t, "observation", left, native)
			firstPointer := "/left"
			if test.name == "different_domain" {
				firstPointer = "/structuredContent/body"
			}
			f.read(t, map[string]any{"result_id": id, "view": "json", "pointer": firstPointer, "offset": 0, "limit": 4}, 0)
			f.read(t, map[string]any{"result_id": id, "view": test.view, "pointer": test.pointer, "offset": 4, "limit": 4}, 0)
			if check := CheckEvidenceQuotes(quoteAnswer("headtail"), f.ledger); check.Passed || !check.Verifiable {
				t.Fatalf("unrelated representations were stitched together: %+v", check)
			}
		})
	}
}

func TestEvidenceQuotesMetadataHiddenPagesAndUnregisteredHistoryAreNotBodies(t *testing.T) {
	for _, test := range []struct {
		name   string
		args   map[string]any
		budget int64
	}{
		{"summary", map[string]any{"view": "summary"}, 0},
		{"native_directory", map[string]any{"view": "native"}, 0},
		{"native_protocol_metadata", map[string]any{"view": "native", "pointer": "/content/0/type"}, 0},
		{"aggregate_only", map[string]any{"view": "query", "pointer": "/rows", "aggregate": map[string]any{"op": "count"}}, 0},
		{"budget_omitted", map[string]any{"view": "search", "pattern": "original"}, 128},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newQuoteFixture(t)
			text := "original " + strings.Repeat("long body ", 600)
			id := f.save(t, text, map[string]any{"rows": []any{map[string]any{"description": "original"}}}, map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}})
			test.args["result_id"] = id
			prompt := f.read(t, test.args, test.budget)
			if len(f.ledger.Fragments()) != 0 {
				t.Fatal("a protocol/summary/omission receipt became body text")
			}
			check := CheckEvidenceQuotes(quoteAnswer("original"), f.ledger)
			if check.Passed || check.Verifiable || EvidenceQuoteRepairNotice(check) != "" {
				t.Fatalf("body-free metadata established a quotation: %+v", check)
			}
			otherRun := newEvidenceLedger(f.a)
			otherRun.ObservePrompt(prompt)
			if check := CheckEvidenceQuotes(quoteAnswer("original"), otherRun); check.Passed || check.Verifiable {
				t.Fatal("an old read was promoted to a newly registered native observation")
			}
		})
	}
	if check := CheckEvidenceQuotes(quoteAnswer("original"), nil); check.Passed || check.Verifiable || check.Reason != "no_delivered_native_body" {
		t.Fatalf("missing verifier passed or pretended the quote was disproved: %+v", check)
	}
}

func TestEvidenceQuoteRewriteChangesOnlyQuotesAndMustRevalidate(t *testing.T) {
	f := newQuoteFixture(t)
	line := "Exact source says: use the original material."
	id := f.save(t, line, nil, nil)
	f.read(t, map[string]any{"result_id": id, "view": "text"}, 0)
	original := quoteAnswer("Exact source says: the original material.")
	replacement := quoteAnswer(line)
	if failure := EvidenceQuoteRewriteFailure(original, replacement, f.ledger); failure != "" {
		t.Fatalf("literal correction of the one quote was rejected: %s", failure)
	}
	for _, test := range []struct{ name, candidate string }{
		{"still_wrong", original},
		{"changed_number", strings.Replace(replacement, `"value":10`, `"value":11`, 1)},
		{"changed_description", strings.Replace(replacement, "A description may paraphrase.", "A changed description.", 1)},
		{"changed_source", strings.Replace(replacement, "#cite:S2", "#cite:S1", 1)},
		{"changed_type", strings.Replace(replacement, `"value":10`, `"value":"10"`, 1)},
		{"changed_fence", "```json\n" + replacement + "\n```"},
		{"dropped_quote", strings.Replace(replacement, `"quote":`+fmt.Sprintf("%q", line), `"description":"removed quote"`, 1)},
		{"trailing_text", replacement + "\nCorrected."},
	} {
		t.Run(test.name, func(t *testing.T) {
			if failure := EvidenceQuoteRewriteFailure(original, test.candidate, f.ledger); failure == "" {
				t.Fatal("a nonliteral or data-changing quote repair was accepted")
			}
		})
	}
	var answer map[string]any
	json.Unmarshal([]byte(replacement), &answer)
	answer["evidence"].([]any)[0].(map[string]any)["quote"] = line
	raw, _ := json.Marshal(answer)
	if failure := EvidenceQuoteRewriteFailure(original, string(raw), f.ledger); failure != "" {
		t.Fatal("key order or JSON encoding changed the preserved business values")
	}
}

func TestEvidenceQuotesProjectedStringsRemainIndependentCompleteValues(t *testing.T) {
	f := newQuoteFixture(t)
	id := f.save(t, "stored observation", map[string]any{"rows": []any{map[string]any{"text": "alpha beta"}, map[string]any{"text": "gamma delta"}}}, nil)
	f.read(t, map[string]any{"result_id": id, "view": "query", "pointer": "/rows", "select": []string{"/text"}}, 0)
	if check := CheckEvidenceQuotes(quoteAnswer("alpha beta"), f.ledger); !check.Passed || !check.Verifiable {
		t.Fatalf("actual projected source string was rejected: %+v", check)
	}
	if check := CheckEvidenceQuotes(quoteAnswer("beta gamma"), f.ledger); check.Passed || !check.Verifiable {
		t.Fatalf("independent source values were concatenated into an invented quotation: %+v", check)
	}
}

func TestEvidenceQuotesLedgerCapKeepsUnseenQuotesUnknown(t *testing.T) {
	f := newQuoteFixture(t)
	text := "visible-first " + strings.Repeat("context ", 70000) + "tail-only-quote"
	id := f.save(t, text, nil, nil)
	for offset := 0; offset < len(text); offset += 32768 {
		f.read(t, map[string]any{"result_id": id, "view": "text", "offset": offset, "limit": 32768}, 0)
	}
	if !f.ledger.Incomplete() {
		t.Fatal("test did not reach the actual delivered-body accounting cap")
	}
	if check := CheckEvidenceQuotes(quoteAnswer("tail-only-quote"), f.ledger); check.Passed || check.Verifiable || check.Reason != "delivery_ledger_incomplete" || EvidenceQuoteRepairNotice(check) != "" {
		t.Fatalf("a capped verifier falsely disproved or repaired an unaccounted quotation: %+v", check)
	}
	if check := CheckEvidenceQuotes(quoteAnswer("visible-first"), f.ledger); !check.Passed || !check.Verifiable {
		t.Fatalf("accounting incompleteness invalidated a known delivered quotation: %+v", check)
	}
}

func TestEvidenceQuoteContractBoundsAndAmbiguousJSONFailClosed(t *testing.T) {
	for _, answer := range []string{
		`{"sources":[],"evidence":[{"quote":"one","quote":"two"}]}`,
		`{"sources":[],"evidence":[{"quote":"one","data":{"x":1,"x":2}}]}`,
	} {
		check := CheckEvidenceQuotes(answer, nil)
		if !check.OptedIn || check.Passed || check.Verifiable || check.Reason != "invalid_quote_contract" {
			t.Fatalf("duplicate JSON keys were silently discarded: %+v", check)
		}
	}
	items := make([]any, evidenceQuoteMaxItems+1)
	for index := range items {
		items[index] = map[string]any{"quote": "a"}
	}
	raw, _ := json.Marshal(map[string]any{"sources": []string{}, "evidence": items})
	if check := CheckEvidenceQuotes(string(raw), nil); check.Passed || check.Reason != "quote_contract_exceeds_limit" {
		t.Fatalf("unbounded quote work was accepted: %+v", check)
	}
}
