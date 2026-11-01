package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// stubLink records the ops it executes and answers from a queue, so the tool's
// formatting and validation can be tested without any browser.
type stubLink struct {
	ops     []json.RawMessage
	replies []BrowserReply
	err     error
}

func (*stubLink) BrowserBackend() string { return BrowserBackendNative }

func (s *stubLink) Execute(_ context.Context, op json.RawMessage) (BrowserReply, error) {
	s.ops = append(s.ops, op)
	if s.err != nil {
		return BrowserReply{}, s.err
	}
	if len(s.replies) == 0 {
		return BrowserReply{OK: true}, nil
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r, nil
}

func op(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBrowserReadableContentMetadataUsesWorkerText(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply BrowserReply
		want  bool
	}{
		{"page text", BrowserReply{OK: true, Text: "Project has 10 commits."}, true},
		{"empty snapshot", BrowserReply{OK: true, URL: "about:blank", Text: " \n "}, false},
		{"no matching elements", BrowserReply{OK: true, Text: "No matching elements or readable page text."}, false},
		{"failed observation", BrowserReply{OK: false, Text: "Page has changed; retry snapshot."}, false},
		{"only metadata", BrowserReply{OK: true, URL: "https://example.com", Title: "Project", Interactive: 8}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			link := &stubLink{replies: []BrowserReply{tc.reply}}
			result, err := (&Browser{Link: link}).Execute(context.Background(), op(t, map[string]any{"action": "snapshot"}))
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Details.(map[string]any)["has_readable_content"]; got != tc.want {
				t.Fatalf("readable content = %v, want %v", got, tc.want)
			}
			if len(link.ops) != 1 {
				t.Fatal("metadata computation repeated the operation")
			}
		})
	}
}

func TestBrowserValidatesPerAction(t *testing.T) {
	link := &stubLink{}
	b := &Browser{Link: link}

	cases := []struct {
		name    string
		args    string
		wantErr string
	}{
		{"click needs target", `{"action":"click"}`, "target is required"},
		{"type needs target", `{"action":"type","text":"x"}`, "target is required"},
		{"navigate needs url", `{"action":"navigate"}`, "url is required"},
		{"wait needs a condition", `{"action":"wait"}`, "one of text, gone_text, url"},
		{"unknown action", `{"action":"drag"}`, "unknown action"},
		{"snapshot is bare", `{"action":"snapshot"}`, ""},
		{"find needs a facet", `{"action":"find"}`, "at least one of role"},
		{"find by role alone is enough", `{"action":"find","role":"button"}`, ""},
		{"select needs target", `{"action":"select","text":"0"}`, "target is required"},
		{"select needs text", `{"action":"select","target":"e4"}`, "text is required"},
		{"press needs a key", `{"action":"press"}`, "key is required"},
		{"press takes target-less keys", `{"action":"press","key":"Escape"}`, ""},
		{"hover needs target", `{"action":"hover"}`, "target is required"},
		{"eval is retired", `{"action":"eval"}`, "retired"},
		{"eval is retired with expression", `{"action":"eval","expression":"  "}`, "retired"},
		{"scroll is bare", `{"action":"scroll"}`, ""},
		{"scroll rejects a bad direction", `{"action":"scroll","direction":"sideways"}`, "not one of down"},
		{"wait accepts a url glob", `{"action":"wait","url":"**/detail/**"}`, ""},
		{"wait accepts a ref", `{"action":"wait","target":"e12"}`, ""},
	}
	for _, c := range cases {
		_, err := b.Execute(context.Background(), json.RawMessage(c.args))
		if c.wantErr == "" {
			if err != nil {
				t.Fatalf("%s: unexpected error %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Fatalf("%s: want error containing %q, got %v", c.name, c.wantErr, err)
		}
	}
}

// The op the pane receives is rebuilt from validated fields: a field outside
// the schema must never reach an executor. The pane trusts the tool; that
// trust is only as good as this.
func TestBrowserRejectsUnvettedFields(t *testing.T) {
	link := &stubLink{}
	b := &Browser{Link: link}
	if _, err := b.Execute(context.Background(), op(t, map[string]any{"action": "type", "target": "e3", "text": "abc", "submit": true, "selector": "body"})); err == nil || len(link.ops) != 0 {
		t.Fatalf("unvetted field accepted: %v", err)
	}
}
func TestBrowserRejectsOutOfRangeWait(t *testing.T) {
	link := &stubLink{}
	b := &Browser{Link: link}
	if _, err := b.Execute(context.Background(), op(t, map[string]any{"action": "wait", "text": "done", "seconds": 9999})); err == nil || len(link.ops) != 0 {
		t.Fatalf("out-of-range wait accepted: %v", err)
	}
}

func TestBrowserWaitMinuteDefaultAndExplicitBoundsReachExecutor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seconds any
		want    float64
	}{
		{"omitted", nil, 60}, {"zero uses default", 0, 60},
		{"shorter", 10, 10}, {"longer than old cap", 45, 45},
		{"full minute", 60, 60}, {"fractional", 0.001, 0.001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, batch := range []bool{false, true} {
				link := &stubLink{replies: []BrowserReply{{OK: true}}}
				browser := &Browser{Link: link}
				wait := map[string]any{"action": "wait", "text": "ready"}
				if tc.seconds != nil {
					wait["seconds"] = tc.seconds
				}
				args := wait
				if batch {
					args = map[string]any{"action": "batch", "actions": []any{wait}}
				}
				if _, err := browser.Execute(context.Background(), op(t, args)); err != nil {
					t.Fatal(err)
				}
				var normalized browserArgs
				if err := json.Unmarshal(link.ops[0], &normalized); err != nil {
					t.Fatal(err)
				}
				if batch {
					normalized = normalized.Actions[0]
				}
				if normalized.Seconds != tc.want {
					t.Fatalf("batch=%v: want %v, got %v", batch, tc.want, normalized.Seconds)
				}
			}
		})
	}
	for _, seconds := range []any{-1, 60.001, "60", nil} {
		link := &stubLink{}
		browser := &Browser{Link: link}
		if _, err := browser.Execute(context.Background(), op(t, map[string]any{"action": "wait", "text": "ready", "seconds": seconds})); err == nil || len(link.ops) != 0 {
			t.Fatalf("invalid seconds %v reached executor: %v", seconds, err)
		}
	}
	seconds := (&Browser{}).InputSchema()["properties"].(map[string]any)["seconds"].(map[string]any)
	if seconds["maximum"] != MaxBrowserWaitSeconds {
		t.Fatalf("schema limit disagrees with runtime: %v", seconds)
	}
}

func TestBrowserFormatsReply(t *testing.T) {
	link := &stubLink{replies: []BrowserReply{{
		OK: true, URL: "http://x/panels/lib/", Title: "样本库", Interactive: 12,
		Text: "[e1] textbox \"家族\" \n[e2] button \"检索\"",
	}}}
	b := &Browser{Link: link}
	res, err := b.Execute(context.Background(), op(t, map[string]any{"action": "snapshot"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"panels/lib/", "样本库", "[e2] button"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("snapshot text missing %q: %s", want, res.Text)
		}
	}

	link.replies = []BrowserReply{{OK: false, Error: "ref e9 not found in the current page snapshot; take a new snapshot first"}}
	res, err = b.Execute(context.Background(), op(t, map[string]any{"action": "click", "target": "e9"}))
	if err != nil {
		t.Fatalf("a page-level failure is a result, not an error: %v", err)
	}
	if !strings.Contains(res.Text, "not found in the current page snapshot") {
		t.Fatalf("page failure text should reach the model: %s", res.Text)
	}

	link.err = errors.New("no browser pane is connected")
	if _, err := b.Execute(context.Background(), op(t, map[string]any{"action": "click", "target": "e1"})); err == nil {
		t.Fatal("a channel failure is an error the loop reports")
	}
}

func TestBrowserReadOnlyClassification(t *testing.T) {
	// The policy's review split rides this table; an action landing on the
	// wrong side of it is a review-policy change made by accident. eval is on
	// the write side on purpose: its argument is arbitrary code, so no caller
	// can promise it only observes.
	for action, ro := range map[string]bool{
		"snapshot": true, "wait": true, "extract": true, "scroll": true, "find": true,
		"click": false, "type": false, "navigate": false,
		"eval": false, "select": false, "press": false, "hover": false, "batch": false,
	} {
		if BrowserReadOnly(action) != ro {
			t.Fatalf("%s classified read-only=%v, want %v", action, BrowserReadOnly(action), ro)
		}
	}
	// Unknown and unparseable both review as writes — the safe side.
	if BrowserReadOnly("drag") {
		t.Fatal("unknown action must not be read-only")
	}
	if BrowserReadOnly(BrowserActionOf(json.RawMessage(`{"action":"snapshot"`))) {
		t.Fatal("unparseable args must not be read-only")
	}
}

func TestBrowserSchemaShape(t *testing.T) {
	b := &Browser{Link: &stubLink{}}
	s := b.InputSchema()
	if s["type"] != "object" {
		t.Fatalf("want object schema, got %v", s["type"])
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing: %v", s)
	}
	if _, ok := props["action"]; !ok {
		t.Fatal("action property missing")
	}
	req, ok := s["required"].([]string)
	if !ok || len(req) != 1 || req[0] != "action" {
		t.Fatalf("required should be [action], got %v", s["required"])
	}
}

// A batch is one round trip for a whole sequence, which is where the latency of
// a multi-step task lives: filling a form used to cost one model turn per field.
// The steps must still go through per-action validation, and they must arrive as
// one op — a batch that fanned out into N ops would have saved nothing.
func TestBrowserBatchSendsOneValidatedOp(t *testing.T) {
	link := &stubLink{replies: []BrowserReply{{OK: true, Interactive: 9}}}
	b := &Browser{Link: link}
	if _, err := b.Execute(context.Background(), op(t, map[string]any{
		"action": "batch",
		"actions": []map[string]any{
			{"action": "type", "target": "e3", "text": "Qakbot"},
			{"action": "click", "target": "e5"},
			{"action": "wait", "text": "检索结果", "seconds": 10},
			{"action": "snapshot"},
		},
	})); err != nil {
		t.Fatal(err)
	}
	if len(link.ops) != 1 {
		t.Fatalf("a batch must cost one round trip, got %d ops", len(link.ops))
	}
	var got struct {
		Action  string `json:"action"`
		Actions []struct {
			Action  string  `json:"action"`
			Target  string  `json:"target"`
			Text    string  `json:"text"`
			Seconds float64 `json:"seconds"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(link.ops[0], &got); err != nil {
		t.Fatal(err)
	}
	if got.Action != BrowserActionBatch || len(got.Actions) != 4 {
		t.Fatalf("batch shape lost in rebuild: %s", link.ops[0])
	}
	if got.Actions[0].Text != "Qakbot" || got.Actions[1].Target != "e5" {
		t.Fatalf("step fields lost in rebuild: %s", link.ops[0])
	}
	// Per-action normalisation applies inside a batch too, or a batched wait
	// would outlive the cap a lone one respects.
	if got.Actions[2].Seconds != 10 {
		t.Fatalf("batched wait should retain bounded seconds, got %v", got.Actions[2].Seconds)
	}
}

func TestBrowserBatchRejectsBadPlans(t *testing.T) {
	b := &Browser{Link: &stubLink{}}
	cases := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{"empty", map[string]any{"action": "batch"}, "actions is required"},
		{
			"nested",
			map[string]any{"action": "batch", "actions": []map[string]any{{"action": "batch"}}},
			"itself a batch",
		},
		{
			// The step number is the point: a model told only "invalid" cannot
			// tell which of twelve steps to fix.
			"invalid step names its index",
			map[string]any{"action": "batch", "actions": []map[string]any{
				{"action": "snapshot"},
				{"action": "click"},
			}},
			"step 2",
		},
	}
	for _, c := range cases {
		_, err := b.Execute(context.Background(), op(t, c.args))
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Fatalf("%s: want error containing %q, got %v", c.name, c.wantErr, err)
		}
	}

	over := make([]map[string]any, MaxBrowserBatchSteps+1)
	for i := range over {
		over[i] = map[string]any{"action": "snapshot"}
	}
	_, err := b.Execute(context.Background(), op(t, map[string]any{"action": "batch", "actions": over}))
	if err == nil || !strings.Contains(err.Error(), "exceeds the limit") {
		t.Fatalf("an oversized plan should be refused before it costs a round trip, got %v", err)
	}
}

// A failed batch still has to say what already happened: the page is in the
// state the successful steps left it in, and a model that cannot see that has to
// start over instead of resuming.
func TestBrowserBatchFailureCarriesTheStepsThatRan(t *testing.T) {
	link := &stubLink{replies: []BrowserReply{{
		OK:    false,
		Error: "batch stopped at step 2/3 (click)",
		Steps: []BrowserStep{
			{Action: "type", OK: true},
			{Action: "click", OK: false, Error: `e5 is covered by dialog "确认删除"`},
		},
	}}}
	b := &Browser{Link: link}
	res, err := b.Execute(context.Background(), op(t, map[string]any{
		"action": "batch",
		"actions": []map[string]any{
			{"action": "type", "target": "e3", "text": "x"},
			{"action": "click", "target": "e5"},
			{"action": "snapshot"},
		},
	}))
	if err != nil {
		t.Fatalf("a page-level batch failure is a result, not an error: %v", err)
	}
	for _, want := range []string{"stopped at step 2/3", "1. type — ok", "2. click — failed", "covered by"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("failed batch text missing %q:\n%s", want, res.Text)
		}
	}
}

// The receipt is what the skill's "did that land?" discipline reads. Losing the
// interactive count or the scroll position turns a truncated snapshot back into
// something the model can only notice, not act on.
func TestBrowserReplyCarriesPulseAndScroll(t *testing.T) {
	link := &stubLink{replies: []BrowserReply{{
		OK: true, URL: "/panels/lib/", Title: "样本库", Interactive: 42,
		Scroll: "scroll 40% (800/2000px)", Text: "[e1] button \"检索\"",
	}}}
	b := &Browser{Link: link}
	res, err := b.Execute(context.Background(), op(t, map[string]any{"action": "scroll", "direction": "down"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"42 interactive", "scroll 40%", "[e1] button"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("receipt missing %q: %s", want, res.Text)
		}
	}

	// A page that fits needs no scroll line: saying so on every action is noise.
	link.replies = []BrowserReply{{OK: true, URL: "/panels/lib/", Scroll: "whole page visible"}}
	res, err = b.Execute(context.Background(), op(t, map[string]any{"action": "snapshot"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, "whole page visible") {
		t.Fatalf("a page that fits should not spend a line saying so: %s", res.Text)
	}
}

// A trapped dialog is the explanation for whatever the page did next, so it has
// to reach the model — on success and on failure. Losing it turns "the click did
// nothing" into an unanswerable question, which is the state this replaced: a
// native modal blocks the pane's thread and the call burns the channel timeout
// with nothing to show.
func TestBrowserReportsPendingDialogs(t *testing.T) {
	link := &stubLink{replies: []BrowserReply{{
		OK: true, URL: "/panels/lib/", Interactive: 12,
		Dialogs: []string{`confirm: 确认删除该样本？ → pending human intervention`},
	}}}
	b := &Browser{Link: link}
	res, err := b.Execute(context.Background(), op(t, map[string]any{"action": "click", "target": "e5"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "dialog pending") || !strings.Contains(res.Text, "确认删除") {
		t.Fatalf("a trapped dialog must reach the model: %s", res.Text)
	}

	link.replies = []BrowserReply{{
		OK: false, Error: "click did not run: e5 is covered by dialog",
		Dialogs: []string{"alert: 会话已过期"},
	}}
	res, err = b.Execute(context.Background(), op(t, map[string]any{"action": "click", "target": "e5"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "会话已过期") {
		t.Fatalf("a dialog is often why the action failed; it must travel with the failure: %s", res.Text)
	}
}

// navigate no longer refuses an absolute URL. The tool does not resolve or
// register anything itself — the pane does, because only it knows the app's own
// origin — so the contract here is just that the url survives validation intact.
func TestBrowserNavigateAcceptsAbsoluteURLs(t *testing.T) {
	link := &stubLink{replies: []BrowserReply{{OK: true}, {OK: true}}}
	b := &Browser{Link: link}
	for _, target := range []string{"/panels/lib/", "https://malpedia.example/family/win.qakbot"} {
		if _, err := b.Execute(context.Background(), op(t, map[string]any{
			"action": "navigate", "url": target,
		})); err != nil {
			t.Fatalf("navigate %s: %v", target, err)
		}
	}
	var got struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(link.ops[1], &got); err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://malpedia.example/family/win.qakbot" {
		t.Fatalf("an absolute url must reach the pane unchanged, got %q", got.URL)
	}
}

// browser was the one built-in whose result had no ceiling. Every other tool runs
// its output through TruncateHead or TruncateTail; this one wrote the page state in
// verbatim on the strength of a comment saying the pane had already budgeted it.
// The pane had — in TypeScript, in the browser, at 120,000 characters for a full
// snapshot, which is more than twice this package's ceiling for everything else,
// set by code that is not in this process.
func TestBrowserBoundsAndSpillsAnOversizedSnapshot(t *testing.T) {
	// A snapshot's element list: many lines, each plausible, adding up to well over
	// the ceiling. Built from lines rather than one long string so the line-based
	// limit is what bites, which is the ordinary shape of the failure.
	var page strings.Builder
	for i := range 4000 {
		fmt.Fprintf(&page, "e%04d button \"Row %s\"\n", i, strings.Repeat("x", 20))
	}
	snapshot := page.String()
	if len(snapshot) < MaxBytes {
		t.Fatalf("the fixture is only %d bytes; it has to exceed %d to test anything", len(snapshot), MaxBytes)
	}

	spillDir := t.TempDir()
	b := &Browser{
		SpillDir: spillDir,
		Link: &stubLink{replies: []BrowserReply{{
			OK: true, URL: "https://example.test/table", Title: "Big table",
			Interactive: 4000, Text: snapshot,
		}}},
	}
	res, err := b.Execute(context.Background(), op(t, browserArgs{Action: BrowserActionSnapshot}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Text) > MaxBytes+2048 {
		t.Errorf("the result is %d bytes, over the %s ceiling plus its note",
			len(res.Text), formatSize(MaxBytes))
	}
	// Head, not tail: the page line is what says which page this is, and it is the
	// first thing in the reply.
	if !strings.HasPrefix(res.Text, "page: https://example.test/table") {
		t.Errorf("the page line did not survive truncation:\n%s", firstN(res.Text, 120))
	}

	d, ok := res.Details.(map[string]any)
	if !ok {
		t.Fatalf("details are %T, want map[string]any", res.Details)
	}
	path, _ := d["full_output_path"].(string)
	if path == "" {
		t.Fatal("browser truncated its reply and spilled nothing")
	}
	if d["truncated"] != true {
		t.Error("details do not record that the reply was truncated")
	}
	if !strings.Contains(res.Text, path) {
		t.Error("the note does not name the file the details point at")
	}
	full := read(t, path)
	if !strings.Contains(full, snapshot) {
		t.Error("the spill file does not hold the whole snapshot")
	}
	if len(full) <= len(res.Text) {
		t.Errorf("spill file is %d bytes and the result is %d: it is not the full reply",
			len(full), len(res.Text))
	}
}

// A reply that fits is untouched, byte for byte. The ceiling has to be invisible in
// the ordinary case or it changes every browser result in the transcript.
func TestBrowserLeavesASmallReplyAlone(t *testing.T) {
	spillDir := t.TempDir()
	b := &Browser{
		SpillDir: spillDir,
		Link: &stubLink{replies: []BrowserReply{{
			OK: true, URL: "https://example.test/", Title: "Small", Interactive: 2,
			Text: "e001 button \"Go\"\ne002 link \"Home\"",
		}}},
	}
	res, err := b.Execute(context.Background(), op(t, browserArgs{Action: BrowserActionSnapshot}))
	if err != nil {
		t.Fatal(err)
	}
	if want := "e002 link \"Home\""; !strings.HasSuffix(res.Text, want) {
		t.Errorf("a small reply was altered:\n%s", res.Text)
	}
	if d := res.Details.(map[string]any); d["truncated"] != nil || d["full_output_path"] != nil {
		t.Errorf("a reply that fitted reported truncation: %v", d)
	}
	if entries, err := os.ReadDir(spillDir); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Errorf("a reply that fitted spilled %d files", len(entries))
	}
}
