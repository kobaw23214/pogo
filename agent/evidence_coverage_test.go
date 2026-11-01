package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yosukeno/pogo/internal/evidencebounds"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

type coverageFixture struct {
	a      *Agent
	id     string
	reader *tools.ToolResultRead
}

func newCoverageFixture(t *testing.T, body string) coverageFixture {
	t.Helper()
	empty := []string{}
	r := tools.New(tools.Options{Cwd: t.TempDir(), SpillDir: filepath.Join(t.TempDir(), "spill"), ResultScope: "coverage-test", Enabled: &empty})
	a := New(Config{Registry: r, Citations: true})
	view := r.BudgetResultView(context.Background(), tools.ModelResultBudgetRequest{Tool: "read", Text: body, Tokens: 1})
	if !sourceResultID(view.ResultID) {
		t.Fatal("fixture did not save its immutable observation")
	}
	origin := a.sources.Next("read", tools.ReadDetails{Path: "observed.log", FirstLine: 1, ShownLines: 100}, "original-observation", body, nil)
	a.sources.bindResult(view.ResultID, origin)
	tool, _ := r.Get(tools.ToolResultReadName)
	reader, ok := tool.(*tools.ToolResultRead)
	if !ok {
		t.Fatal("fixture lost protected native reader")
	}
	return coverageFixture{a, view.ResultID, reader}
}
func (f coverageFixture) page(t *testing.T, id string, args map[string]any, budget int64) (llm.Block, llm.Block) {
	t.Helper()
	args["result_id"] = f.id
	raw, _ := json.Marshal(args)
	res, err := f.reader.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	source := f.a.sources.Next(tools.ToolResultReadName, res.Details, id, res.Text, raw)
	if source == nil {
		t.Fatal("native read has no source")
	}
	text := res.Text
	if budget >= 0 {
		v := f.a.registry.BudgetResultView(context.Background(), tools.ModelResultBudgetRequest{Tool: tools.ToolResultReadName, Text: text, Details: marshalDetails(res.Details), Args: raw, Tokens: budget})
		text = v.Text
	}
	call := llm.Block{Type: llm.BlockToolUse, ID: id, Name: tools.ToolResultReadName, Input: raw}
	result := llm.Block{Type: llm.BlockToolResult, ToolUseID: id, Text: fmt.Sprintf("[cite:%s | tool_result | saved local view]\n%s", source.Label, text), Details: marshalDetails(res.Details), Source: marshalSource(source)}
	return call, result
}
func coveragePrompt(call, result llm.Block) []llm.Message {
	return []llm.Message{{Role: llm.RoleAssistant, Content: []llm.Block{call}}, {Role: llm.RoleUser, Content: []llm.Block{result}}}
}
func coverageLines(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "index=%03d literal=needle message=%s\n", i, strings.Repeat("payload ", 12))
	}
	return b.String()
}

func TestEvidenceCoverageActualPagesMergeWithoutCountingHistoryTwice(t *testing.T) {
	f := newCoverageFixture(t, coverageLines(21))
	l := newEvidenceLedger(f.a)
	c, b := f.page(t, "first", map[string]any{"view": "search", "pattern": "needle", "context_lines": 0, "limit": 20}, -1)
	l.Register([]llm.Block{c})
	prompt := coveragePrompt(c, b)
	l.ObservePrompt(prompt)
	l.ObservePrompt(prompt)
	s := l.Snapshot()
	if s.TrackedSelections != 1 || len(s.Partial) != 1 || s.Partial[0].Total != 21 || s.Partial[0].Delivered != 20 ||
		s.Partial[0].Undelivered != 1 || !s.Partial[0].ScanComplete || s.Partial[0].Complete {
		t.Fatalf("scan or duplicate history became delivery: %+v", s)
	}
	var next map[string]any
	if json.Unmarshal(s.Partial[0].NextRequest, &next) != nil || next["offset"] != float64(20) {
		t.Fatalf("continuation not first unread ordinal: %s", s.Partial[0].NextRequest)
	}
	if text, ok := l.CoverageNotice(); !ok || !strings.Contains(text, "\"delivered_matches\":20") {
		t.Fatalf("missing bounded coverage checkpoint: %s", text)
	}
	if _, ok := l.CoverageNotice(); ok {
		t.Fatal("checkpoint repeated without new human steering")
	}
	c2, b2 := f.page(t, "last", map[string]any{"view": "search", "pattern": "needle", "context_lines": 1, "offset": 20, "limit": 1}, -1)
	l.Register([]llm.Block{c2})
	l.ObservePrompt(append(prompt, coveragePrompt(c2, b2)...))
	if s := l.Snapshot(); s.IncompleteSelections != 0 || s.TrackedSelections != 1 {
		t.Fatalf("limit/context changed selection identity or page did not merge: %+v", s)
	}
	fresh := newEvidenceLedger(f.a)
	fresh.ObservePrompt(prompt)
	if len(fresh.Fragments()) != 0 || fresh.Snapshot().TrackedSelections != 0 {
		t.Fatal("old task history registered evidence in a fresh scope")
	}
}

func TestEvidenceCoverageActualBudgetPageAndZeroDelivery(t *testing.T) {
	f := newCoverageFixture(t, coverageLines(21))
	for _, budget := range []int64{512, 0} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			l := newEvidenceLedger(f.a)
			c, b := f.page(t, fmt.Sprint("budget-", budget), map[string]any{"view": "search", "pattern": "needle", "context_lines": 0, "limit": 20}, budget)
			l.Register([]llm.Block{c})
			l.ObservePrompt(coveragePrompt(c, b))
			page, ok := evidenceJSON(StripCiteHeader(b.Text))
			if !ok {
				t.Fatal("expected native budget JSON")
			}
			shown, _ := evidenceInt(page["shown_items"])
			s := l.Snapshot()
			if budget == 0 {
				if len(l.Fragments()) != 0 {
					t.Fatal("budget locator became body")
				}
				if _, ok := l.CoverageNotice(); ok {
					t.Fatal("zero-delivery receipt forced a coverage round")
				}
				return
			}
			if shown <= 0 || shown >= 20 || len(s.Partial) != 1 || s.Partial[0].Delivered != shown {
				t.Fatalf("used full host page instead of actual model budget page: shown=%d status=%+v", shown, s)
			}
		})
	}
}

func TestEvidenceCoverageStatsDoNotForceRowsAndProjectionsStaySeparate(t *testing.T) {
	rows := []map[string]any{}
	for i := 0; i < 3; i++ {
		rows = append(rows, map[string]any{"message": fmt.Sprint("message_", i), "other": fmt.Sprint("other_", i), "omitted_binary": true})
	}
	raw, _ := json.Marshal(map[string]any{"rows": rows})
	f := newCoverageFixture(t, string(raw))
	l := newEvidenceLedger(f.a)
	c, b := f.page(t, "stats", map[string]any{"view": "query", "pointer": "/rows", "aggregate": map[string]any{"op": "count"}}, -1)
	l.Register([]llm.Block{c})
	l.ObservePrompt(coveragePrompt(c, b))
	if len(l.Fragments()) != 0 {
		t.Fatal("aggregate metadata became quoted body")
	}
	if _, ok := l.CoverageNotice(); ok {
		t.Fatal("count-only task forced row pagination")
	}
	for i, field := range []string{"/message", "/other"} {
		c, b := f.page(t, fmt.Sprint("projection-", i), map[string]any{"view": "query", "pointer": "/rows", "select": []string{field}, "limit": 1}, -1)
		l.Register([]llm.Block{c})
		l.ObservePrompt(coveragePrompt(c, b))
	}
	s := l.Snapshot()
	if len(s.Partial) != 2 || s.Partial[0].Delivered != 1 || s.Partial[1].Delivered != 1 {
		t.Fatalf("different string projections merged: %+v", s)
	}
	fs := l.Fragments()
	if len(fs) != 2 || fs[0].Pointer != "/rows/0/message" || fs[1].Pointer != "/rows/0/other" || !fs[0].CompleteValue || fs[0].HasRange {
		t.Fatalf("projection fields lost their actual value domain: %+v", fs)
	}
	c, b = f.page(t, "business-field", map[string]any{"view": "json", "pointer": "/rows/0"}, -1)
	l.Register([]llm.Block{c})
	l.ObservePrompt(coveragePrompt(c, b))
	found := false
	for _, x := range l.Fragments() {
		found = found || x.Text == "message_0"
	}
	if !found {
		t.Fatal("user omitted_binary business field acted as a host control marker")
	}
}

func TestEvidenceCoverageTextUsesActualUTF8RangesAndIgnoresUnregisteredCalls(t *testing.T) {
	body := "甲乙丙丁\noriginal quotation\n"
	f := newCoverageFixture(t, body)
	c, b := f.page(t, "text", map[string]any{"view": "text", "offset": 0, "limit": 6}, -1)
	l := newEvidenceLedger(f.a)
	l.ObservePrompt(coveragePrompt(c, b))
	if len(l.Fragments()) != 0 {
		t.Fatal("unregistered old call was observed")
	}
	l.Register([]llm.Block{c})
	l.ObservePrompt(coveragePrompt(c, b))
	fs := l.Fragments()
	if len(fs) != 1 || fs[0].Text != "甲乙" || fs[0].Domain != "stored_text" || fs[0].Start != 0 || fs[0].End != 6 || !fs[0].HasRange {
		t.Fatalf("raw native text header or byte domain was misread: %+v", fs)
	}
	if _, ok := l.CoverageNotice(); ok {
		t.Fatal("an ordinary chosen text range forced full-file reading")
	}
}

func TestEvidenceCoverageCheckpointPreservesCompleteAnswerContractAndRecordsFallback(t *testing.T) {
	f := newCoverageFixture(t, coverageLines(21))
	l := newEvidenceLedger(f.a)
	l.RejectCheckpointAnswer("not_sent")
	if status := l.Snapshot(); status.CheckpointAnswerRestored || status.CheckpointFailure != "" {
		t.Fatal("an unissued checkpoint acquired a fallback status")
	}
	c, b := f.page(t, "partial-checkpoint", map[string]any{"view": "search", "pattern": "needle", "context_lines": 0, "limit": 20}, -1)
	l.Register([]llm.Block{c})
	l.ObservePrompt(coveragePrompt(c, b))
	notice, ok := l.CoverageNotice()
	if !ok {
		t.Fatal("missing partial-delivery checkpoint")
	}
	for _, contract := range []string{
		"Do not infer or add properties of undelivered matches from counts, scan completion, or a decision not to rely on them",
		"actual delivered body evidence from this or another read; otherwise leave it undetermined",
		"preserve its supported facts, exact quotations and scope without adding explanations about unseen content",
		"Read only a necessary saved continuation",
		"Return the complete answer requested by the user",
		"raw or fenced JSON",
		"all keys, value types and schema intact",
		"Do not reply with only an acknowledgement",
		"previous answer is unchanged",
		"reference back to an earlier answer",
	} {
		if !strings.Contains(notice, contract) {
			t.Fatalf("checkpoint dropped the requested complete-answer contract: %q", contract)
		}
	}
	before := l.Snapshot()
	l.RejectCheckpointAnswer("checkpoint_answer_not_complete_json")
	after := l.Snapshot()
	if !after.CheckpointSent || !after.CheckpointAnswerRestored || after.CheckpointFailure != "checkpoint_answer_not_complete_json" {
		t.Fatalf("fallback was not observable: %+v", after)
	}
	if after.IncompleteSelections != 1 || len(after.Partial) != 1 || after.Partial[0].Complete ||
		after.Partial[0].Delivered != before.Partial[0].Delivered || after.Partial[0].Total != before.Partial[0].Total {
		t.Fatalf("restoring answer structure certified unread rows: before=%+v after=%+v", before, after)
	}
	l.RejectCheckpointAnswer("later_reason")
	if l.Snapshot().CheckpointFailure != after.CheckpointFailure {
		t.Fatal("fallback overwrote the first host failure diagnostic")
	}
	if _, ok := l.CoverageNotice(); ok {
		t.Fatal("a rejected follow-up reissued the coverage checkpoint")
	}

	bounded := newEvidenceLedger(f.a)
	bounded.checkpointSent = true
	bounded.RejectCheckpointAnswer(strings.Repeat("界", 100))
	reason := bounded.Snapshot().CheckpointFailure
	if len(reason) > 128 || !utf8.ValidString(reason) {
		t.Fatal("host fallback reason was not bounded valid UTF-8")
	}
}

func TestEvidenceCoverageSelectorGapRetainsBodyFromAnIndependentRead(t *testing.T) {
	f := newCoverageFixture(t, coverageLines(21))
	l := newEvidenceLedger(f.a)
	first, firstBody := f.page(t, "selected-page", map[string]any{"view": "search", "pattern": "needle", "context_lines": 0, "limit": 20}, -1)
	other, otherBody := f.page(t, "independent-read", map[string]any{"view": "search", "pattern": "index=020", "context_lines": 0, "limit": 1}, -1)
	l.Register([]llm.Block{first, other})
	l.ObservePrompt(append(coveragePrompt(first, firstBody), coveragePrompt(other, otherBody)...))
	status := l.Snapshot()
	if status.TrackedSelections != 2 || len(status.Partial) != 1 || status.Partial[0].Pattern != "needle" ||
		status.Partial[0].Delivered != 20 || status.Partial[0].Undelivered != 1 {
		t.Fatalf("a different selector falsely completed the first selector's delivery: %+v", status)
	}
	var independentlyDelivered bool
	for _, fragment := range l.Fragments() {
		if fragment.CallID == other.ID && strings.Contains(fragment.Text, "index=020") && strings.Contains(fragment.Text, "needle") {
			independentlyDelivered = fragment.HasRange && fragment.Domain == "stored_text"
		}
	}
	if !independentlyDelivered || status.FragmentCount != 21 {
		t.Fatal("selector delivery gap erased the same body's independent actual delivery")
	}
	notice, ok := l.CoverageNotice()
	if !ok || !strings.Contains(notice, `"undelivered_matches":1`) ||
		!strings.Contains(notice, "Each undelivered_matches count applies only to that selector") ||
		!strings.Contains(notice, "another observed read may independently contain the same body") {
		t.Fatalf("checkpoint promoted a selector gap into a global absence claim: %s", notice)
	}
}

func crossSearchBody(targetErrors bool) string {
	var b strings.Builder
	for i := 0; i < 11; i++ {
		fmt.Fprintf(&b, "ERROR trace=background index=%d\n", i)
	}
	level := "INFO"
	if targetErrors {
		level = "ERROR"
	}
	fmt.Fprintf(&b, "%s trace=focus phase=scheduled\nINFO trace=focus phase=recovered\n%s trace=focus phase=rejected\n%s trace=focus phase=propagated\n", level, level, level)
	for i := 11; i < 21; i++ {
		fmt.Fprintf(&b, "ERROR trace=background index=%d\n", i)
	}
	fmt.Fprintf(&b, "%s trace=focus phase=final\n", level)
	return b.String()
}

func TestEvidenceCoverageNativeCrossSearchTailCounterexampleAndLaterPage(t *testing.T) {
	body := crossSearchBody(true)
	f := newCoverageFixture(t, body)
	l := newEvidenceLedger(f.a)
	for _, spec := range []struct {
		id, pattern string
		limit       int
	}{{"selected", "ERROR", 20}, {"target", "trace=focus", 20}} {
		c, b := f.page(t, spec.id, map[string]any{"view": "search", "pattern": spec.pattern, "limit": spec.limit, "context_lines": 0}, -1)
		l.Register([]llm.Block{c})
		l.ObservePrompt(coveragePrompt(c, b))
	}
	s := l.Snapshot()
	if len(s.Partial) != 1 || s.Partial[0].Total != 25 || s.Partial[0].Delivered != 20 || s.Partial[0].Undelivered != 5 || s.Partial[0].KnownViaOtherBody != 1 {
		t.Fatalf("independent body erased native paging or missed the known tail: %+v", s)
	}
	if len(s.CrossSearchRelations) != 1 || s.CrossSearchRelations[0].Kind != "counterexample" {
		t.Fatalf("complete target selector falsely excluded its ERROR tail: %+v", s)
	}
	w := s.CrossSearchRelations[0].Witnesses[0]
	wantStart := strings.LastIndex(body, "ERROR trace=focus")
	if w.Left.Start != wantStart || w.Left.Ordinal != nil || w.Left.OrdinalLower == nil || *w.Left.OrdinalLower != 20 || w.Left.OrdinalUpper == nil || *w.Left.OrdinalUpper != 25 || len(s.BodyReferences) == 0 {
		t.Fatalf("tail witness lost literal position, gap bounds or actual source references: %+v", s)
	}
	notice, ok := l.CoverageNotice()
	if !ok || !strings.Contains(notice, `"kind":"counterexample"`) || !strings.Contains(notice, `"body_references"`) || !strings.Contains(notice, `"selector_delivery"`) {
		t.Fatalf("bounded model notice omitted the actual relation: %s", notice)
	}
	c, b := f.page(t, "continuation", map[string]any{"view": "search", "pattern": "ERROR", "offset": 20, "limit": 5, "context_lines": 0}, -1)
	l.Register([]llm.Block{c})
	l.ObservePrompt(coveragePrompt(c, b))
	s = l.Snapshot()
	if s.IncompleteSelections != 0 || len(s.CrossSearchRelations) != 0 {
		t.Fatalf("same-selector later page remained a cross-body remainder: %+v", s)
	}
	var found bool
	for _, c := range l.literalRelations().Coverage {
		if c.Pattern == "ERROR" {
			found = c.NativeDelivered == 25 && c.OtherBodyCount == 0
		}
	}
	if !found {
		t.Fatal("native page union did not supersede only the selector gap")
	}
}

func TestEvidenceCoverageNativeCrossSearchDisjointNeedsCompleteSameLineBody(t *testing.T) {
	f := newCoverageFixture(t, crossSearchBody(false))
	l := newEvidenceLedger(f.a)
	for _, spec := range []struct{ id, pattern string }{{"errors", "ERROR"}, {"target", "trace=focus"}} {
		c, b := f.page(t, spec.id, map[string]any{"view": "search", "pattern": spec.pattern, "limit": 20, "context_lines": 1}, -1)
		l.Register([]llm.Block{c})
		l.ObservePrompt(coveragePrompt(c, b))
	}
	s := l.Snapshot()
	if len(s.CrossSearchRelations) != 1 || s.CrossSearchRelations[0].Kind != "verified_disjoint" {
		t.Fatalf("different lines inside one context became cooccurrence: %+v", s)
	}
	l.incomplete = true
	for _, relation := range l.Snapshot().CrossSearchRelations {
		if relation.Kind == "verified_disjoint" {
			t.Fatal("caller cap allowed a full exclusion proof")
		}
	}
}

func TestEvidenceCoverageNativeTruncatedContextIsNotACompleteRelationLine(t *testing.T) {
	f := newCoverageFixture(t, strings.Repeat("x", 20000)+" ERROR trace=focus "+strings.Repeat("x", 20000))
	l := newEvidenceLedger(f.a)
	for _, pattern := range []string{"ERROR", "trace=focus"} {
		c, b := f.page(t, pattern, map[string]any{"view": "search", "pattern": pattern, "limit": 1, "context_lines": 0}, -1)
		l.Register([]llm.Block{c})
		l.ObservePrompt(coveragePrompt(c, b))
	}
	for _, c := range l.literalRelations().Coverage {
		if c.KnownCompleteBody != 0 || c.CompleteBody {
			t.Fatalf("truncated native context became complete body: %+v", c)
		}
	}
	for _, r := range l.Snapshot().CrossSearchRelations {
		if r.Kind != "unknown" {
			t.Fatalf("partial line established cooccurrence/exclusion: %+v", r)
		}
	}
}

func TestEvidenceCoverageLargeReferenceReceiptIsBoundedWithoutRewritingSnapshot(t *testing.T) {
	f := newCoverageFixture(t, coverageLines(21))
	l := newEvidenceLedger(f.a)
	c, b := f.page(t, "bounded-receipt", map[string]any{"view": "search", "pattern": "needle", "limit": 20, "context_lines": 0}, -1)
	l.Register([]llm.Block{c})
	l.ObservePrompt(coveragePrompt(c, b))
	report := l.literalRelations()
	ids := make([]string, 4096)
	for i := range ids {
		ids[i] = fmt.Sprintf("large-reference-%06d-%s", i, strings.Repeat("x", 32))
	}
	for i := range report.Coverage {
		report.Coverage[i].OtherBodyCount = 1
		report.Coverage[i].OtherBody = []evidencebounds.Witness{{FragmentIDs: ids}}
	}
	before := l.Snapshot()
	notice, ok := l.CoverageNotice()
	const prefix = "[pogo] saved-result coverage checkpoint: the current task's actual model-request receipts contain only part of these selected saved results: "
	end := strings.Index(notice, ". A completed scan/count")
	if !ok || end < len(prefix) {
		t.Fatal("checkpoint missing its bounded JSON receipt")
	}
	raw := notice[len(prefix):end]
	if len(raw) > 6144 || !json.Valid([]byte(raw)) || !strings.Contains(raw, `"receipt_details_omitted":true`) || !strings.Contains(raw, `"delivered_matches":20`) || !strings.Contains(raw, f.id) {
		t.Fatalf("oversized witness arrays escaped the receipt budget: bytes=%d", len(raw))
	}
	after := l.Snapshot()
	if len(before.Partial) != 1 || len(after.Partial) != 1 || len(after.Partial[0].OtherBody[0].FragmentIDs) != len(ids) || after.Partial[0].Delivered != 20 || after.Partial[0].Undelivered != 1 {
		t.Fatal("bounded notice rewrote the full snapshot or native delivery counts")
	}
}
