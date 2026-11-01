package web

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
)

// --- parsing -----------------------------------------------------------------

func TestParseFollowupChipsCleanAnswer(t *testing.T) {
	chips, repaired := parseFollowupChips(`{"follow_ups": [
		{"title": "深入一点", "prompt": "这个样本的持久化机制是什么"},
		{"title": "下一步", "prompt": "用 yara 扫一下同家族样本"}
	]}`, true)
	if repaired {
		t.Error("a clean answer was reported as repaired")
	}
	if len(chips) != 2 {
		t.Fatalf("chips = %v, want 2", chips)
	}
	if chips[0].Title != "深入一点" || chips[0].Prompt != "这个样本的持久化机制是什么" {
		t.Errorf("chip[0] = %+v", chips[0])
	}
	// Chips share the starter-card wire shape, but only ever carry title and
	// prompt: a generated chip's one action is putting its prompt in the
	// composer, so no panel or icon may leak in.
	if chips[0].Panel != "" || chips[0].At != "" || chips[0].Icon != "" {
		t.Errorf("chip carries a field generation never sets: %+v", chips[0])
	}
}

// "Only output JSON" is a hope, not a property: models wrap the object in
// prose often enough that the parse has to slice it out.
func TestParseFollowupChipsSlicesOutTheObject(t *testing.T) {
	raw := "好的，以下是建议：\n{\"follow_ups\": [{\"title\": \"t\", \"prompt\": \"p\"}]}\n希望有帮助。"
	chips, _ := parseFollowupChips(raw, true)
	if len(chips) != 1 || chips[0].Prompt != "p" {
		t.Fatalf("chips = %v, want the object parsed out of the prose", chips)
	}
}

// Silence is a designed answer — both as the model's own choice (the prompt
// names the cases for it) and as every parse failure's outcome, because the
// rule-based fallback needs no signal.
func TestParseFollowupChipsSilence(t *testing.T) {
	for _, raw := range []string{
		"",                                  // nothing came back
		"no object here at all",             // prose only
		`{"follow_ups": []}`,                // the model's own "nothing to suggest"
		`{"follow_ups": [{"title": "t"}]}`,  // a chip with no prompt has no action
		`{"follow_ups": [{"prompt": "p"}]}`, // a chip with no title has no label
		`{"follow_ups": "not a list"}`,      // wrong shape entirely
	} {
		if chips, _ := parseFollowupChips(raw, true); chips != nil {
			t.Errorf("parseFollowupChips(%q, true) = %v, want silence", raw, chips)
		}
	}
	// A token-capped stream is unfinished, not broken, so the repair fallback
	// is off for it: truncation must stay silence, not dress up as an answer.
	if chips, repaired := parseFollowupChips(`{"follow_ups": [{"title": "t", "prompt": "p"}`, false); chips != nil || repaired {
		t.Errorf("a truncated stream was parsed: %v, repaired=%v", chips, repaired)
	}
}

// The repair fallback: an answer that came back in JSON but broke it — the
// field failure of weaker endpoints — still yields its chips, because nothing
// executes from a chip and the strict parse already had its chance.
func TestParseFollowupChipsRepairsBrokenJSON(t *testing.T) {
	for _, raw := range []string{
		`{"follow_ups": [{"title": "深挖", "prompt": "持久化机制是什么"}`,                         // missing brackets
		`{'follow_ups': [{'title': 't', 'prompt': 'p'}]}`,                               // single quotes
		`{"follow_ups": [{"title": "a", "prompt": "b"} {"title": "c", "prompt": "d"}]}`, // missing comma
	} {
		chips, repaired := parseFollowupChips(raw, true)
		if !repaired || len(chips) == 0 || chips[0].Prompt == "" {
			t.Errorf("parseFollowupChips(%q, true) = %v, repaired=%v; want the repaired chips", raw, chips, repaired)
		}
	}
	// The same breakage with the gate closed stays silence.
	if chips, repaired := parseFollowupChips(`{"follow_ups": [{"title": "t", "prompt": "p"}`, false); chips != nil || repaired {
		t.Errorf("repair ran with the gate closed: %v, repaired=%v", chips, repaired)
	}
}

func TestParseFollowupChipsEnforcesTheLimits(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"follow_ups": [`)
	for i := 0; i < skills.MaxFollowupChips+2; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"title": "t` + strings.Repeat("x", i) + `", "prompt": "p` + strings.Repeat("y", i) + `"}`)
	}
	b.WriteString(`]}`)
	if got, _ := parseFollowupChips(b.String(), true); len(got) != skills.MaxFollowupChips {
		t.Errorf("kept %d chips, want the cap of %d", len(got), skills.MaxFollowupChips)
	}

	longTitle := strings.Repeat("标", skills.MaxStarterTitle+10)
	longPrompt := strings.Repeat("问", skills.MaxStarterPrompt+10)
	chips, _ := parseFollowupChips(`{"follow_ups": [{"title": "`+longTitle+`", "prompt": "`+longPrompt+`"}]}`, true)
	if len(chips) != 1 {
		t.Fatalf("chips = %v, want the over-long chip truncated rather than dropped", chips)
	}
	if got := len([]rune(chips[0].Title)); got != skills.MaxStarterTitle {
		t.Errorf("title is %d runes, want the %d-rune cap", got, skills.MaxStarterTitle)
	}
	if got := len([]rune(chips[0].Prompt)); got != skills.MaxStarterPrompt {
		t.Errorf("prompt is %d runes, want the %d-rune cap", got, skills.MaxStarterPrompt)
	}

	// Control characters are an artefact in a one-line chip, not formatting.
	chips, _ = parseFollowupChips(`{"follow_ups": [{"title": "a\nb", "prompt": "c\td"}]}`, true)
	if len(chips) != 1 || chips[0].Title != "ab" || chips[0].Prompt != "cd" {
		t.Errorf("control characters were not stripped: %+v", chips)
	}
}

// The prompt is the chip's actionable content, so two chips asking the same
// question are the same suggestion however the titles differ — and case or
// punctuation differences are not what makes two questions different.
func TestParseFollowupChipsDeduplicatesByPrompt(t *testing.T) {
	chips, _ := parseFollowupChips(`{"follow_ups": [
		{"title": "one", "prompt": "What's next?"},
		{"title": "two", "prompt": "whats next"},
		{"title": "three", "prompt": "What's next！"},
		{"title": "four", "prompt": "something else"}
	]}`, true)
	if len(chips) != 2 {
		t.Fatalf("chips = %v, want the normalised duplicates gone", chips)
	}
	if chips[0].Title != "one" || chips[1].Title != "four" {
		t.Errorf("kept %v, want the first of each duplicate", chips)
	}
}

// --- rendering ---------------------------------------------------------------

func TestRenderFollowupHistoryKeepsToolNamesNotArguments(t *testing.T) {
	msgs := []llm.Message{
		llm.UserText("查一下 config.toml"),
		{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockThinking, Text: "let me think about this"},
			{Type: llm.BlockToolUse, ID: "c1", Name: "read", Input: json.RawMessage(`{"path":"config.toml"}`)},
		}},
		{Role: llm.RoleUser, Content: []llm.Block{
			{Type: llm.BlockToolResult, ToolUseID: "c1", Text: "model = \"k3\""},
		}},
		{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockText, Text: "配置用的是 k3。"},
		}},
	}
	out := renderFollowupHistory(msgs)
	for _, want := range []string{
		"user: 查一下 config.toml", "[called tool read]", "tool result: model = \"k3\"", "assistant: 配置用的是 k3。",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered history is missing %q:\n%s", want, out)
		}
	}
	// Arguments are the bulk without the signal a suggestion needs, and thinking
	// is not part of what any provider saw.
	for _, unwanted := range []string{"config.toml\"", "let me think"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("rendered history contains %q:\n%s", unwanted, out)
		}
	}
}

func TestRenderFollowupHistoryKeepsOnlyTheTail(t *testing.T) {
	var msgs []llm.Message
	for i := 0; i < followupHistoryMessages+2; i++ {
		msgs = append(msgs, llm.UserText("message-"+string(rune('a'+i))))
	}
	out := renderFollowupHistory(msgs)
	if strings.Contains(out, "message-a") || strings.Contains(out, "message-b") {
		t.Errorf("the oldest exchanges were kept:\n%s", out)
	}
	if !strings.Contains(out, "message-h") {
		t.Errorf("the newest exchange was dropped:\n%s", out)
	}
}

// A budget overrun drops the oldest context, never the exchange the chips are
// actually about.
func TestRenderFollowupHistoryDropsTheOldestFirst(t *testing.T) {
	big := strings.Repeat("字", followupLineClip) // 3 bytes per rune: 4.5KB a line
	msgs := []llm.Message{
		llm.UserText("FIRST " + big),
		llm.UserText("MIDDLE " + big),
		llm.UserText("LAST " + big),
	}
	out := renderFollowupHistory(msgs)
	if !strings.Contains(out, "LAST") {
		t.Errorf("the newest message was dropped:\n%.100s...", out)
	}
	if strings.Contains(out, "FIRST") && strings.Contains(out, "MIDDLE") {
		t.Errorf("nothing was dropped though the lines alone exceed the budget")
	}
}

// --- gates -------------------------------------------------------------------

func TestFollowupEventGate(t *testing.T) {
	if got := followupEventGate(Event{Type: EvRunEnd}); got != "" {
		t.Errorf("a clean run_end was gated: %q", got)
	}
	if got := followupEventGate(Event{Type: EvRunEnd, Error: "budget exceeded: ..."}); got == "" {
		t.Error("a failed run was not gated")
	}
	if got := followupEventGate(Event{Type: EvRunEnd, StopReason: string(llm.StopAborted)}); got == "" {
		// An interrupted run carries no error — stopping on purpose is not one.
		t.Error("an interrupted run was not gated")
	}
}

func TestFollowupHistoryGate(t *testing.T) {
	if got := followupHistoryGate(nil); got == "" {
		t.Error("an empty conversation was not gated")
	}
	// Chips extend a dialogue; a transcript with nothing the user said is not one.
	if got := followupHistoryGate([]llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "hi"}}},
	}); got == "" {
		t.Error("a conversation with no user message was not gated")
	}
	// And they attach to an answer: a last assistant message of tool calls alone
	// is a run that never answered.
	if got := followupHistoryGate([]llm.Message{
		llm.UserText("go"),
		{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockToolUse, ID: "c1", Name: "read", Input: json.RawMessage(`{}`)},
		}},
	}); got == "" {
		t.Error("an answerless run was not gated")
	}
	if got := followupHistoryGate([]llm.Message{
		llm.UserText("go"),
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "done"}}},
	}); got != "" {
		t.Errorf("an answered prompt was gated: %q", got)
	}
}

// The wire contract with the client, pinned literally: extra fields are the
// universal envelope (seq/ts), but the payload is exactly these three.
func TestFollowupsEventShape(t *testing.T) {
	h := NewHub()
	_, ch, cancel := h.Subscribe(0)
	defer cancel()
	h.Publish(Event{Type: EvFollowups, MessageID: "m12", Chips: []skills.StarterCard{
		{Title: "深入", Prompt: "这个样本还做了什么"},
	}})
	e := waitEvent(t, ch, EvFollowups)
	blob, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Type      string `json:"type"`
		MessageID string `json:"message_id"`
		Chips     []struct {
			Title  string `json:"title"`
			Prompt string `json:"prompt"`
		} `json:"chips"`
	}
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Type != "followups" || decoded.MessageID != "m12" ||
		len(decoded.Chips) != 1 || decoded.Chips[0].Title == "" || decoded.Chips[0].Prompt == "" {
		t.Errorf("the followups frame does not match the contract: %s", blob)
	}
}

// --- hub ---------------------------------------------------------------------

// The snapshot must recover the chips on its own: a client that reloads after
// the event has passed has nothing else to get them from.
func TestFollowupsFoldIntoTheSnapshot(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "r1"})
	h.Publish(Event{Type: EvUserMessage, Text: "q"})
	h.Publish(Event{Type: EvTurnStart, Turn: 1})
	h.Publish(Event{Type: EvMessage, Role: string(llm.RoleAssistant), Content: []llm.Block{
		{Type: llm.BlockText, Text: "a"},
	}})
	h.Publish(Event{Type: EvRunEnd, StopReason: string(llm.StopEndTurn)})

	id, ok := h.LastAssistantID()
	if !ok || id != "m2" {
		t.Fatalf("LastAssistantID = %q, %v; want m2", id, ok)
	}
	h.Publish(Event{Type: EvFollowups, MessageID: id, Chips: []skills.StarterCard{
		{Title: "t", Prompt: "p"},
	}})

	snap := h.Snapshot()
	if snap.Followups == nil || snap.Followups.MessageID != "m2" || len(snap.Followups.Chips) != 1 {
		t.Fatalf("snapshot followups = %+v", snap.Followups)
	}
	// A generation that came back empty is never published; if one somehow is,
	// it must not erase what an earlier run suggested.
	h.Publish(Event{Type: EvFollowups, MessageID: "m2"})
	if h.Snapshot().Followups == nil {
		t.Error("an empty followups event cleared the chips")
	}

	// The event is replay-logged, so a client resuming with ?from= sees it
	// without taking a snapshot.
	backlog, _, cancel := h.Subscribe(0)
	defer cancel()
	seen := false
	for _, e := range backlog {
		if e.Type == EvFollowups {
			seen = true
		}
	}
	if !seen {
		t.Error("the followups event is not in the replay log")
	}

	// A rewind abandons the branch the chips reference; they go with it.
	h.Reset(nil, llm.Usage{})
	if h.Snapshot().Followups != nil {
		t.Error("chips survived a reset that abandoned their message")
	}
}

// --- end to end --------------------------------------------------------------

func followupTurn(t *testing.T) llm.Response {
	t.Helper()
	return llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
			Type: llm.BlockText,
			Text: `{"follow_ups": [{"title": "持久化机制", "prompt": "这个样本的持久化机制是什么"}, {"title": "同家族", "prompt": "用 yara 扫一下同家族样本"}]}`,
		}}},
		StopReason: llm.StopEndTurn,
		Usage:      llm.Usage{Input: 120, Output: 30},
	}
}

// The chips arrive after run_end, as their own event: the one property that
// lets generation cost a model call without ever delaying the answer.
func TestFollowupsArriveAfterRunEnd(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("这是一个木马。"), followupTurn(t)),
		func(c *Config) { c.Followups = true })
	sid := h.createSession()
	s := h.start(sid, "分析一下这个样本")
	s.wait(t, EvRunEnd)

	e := s.wait(t, EvFollowups)
	if e.MessageID == "" {
		t.Error("the followups event names no message")
	}
	if len(e.Chips) != 2 || e.Chips[0].Prompt == "" {
		t.Errorf("chips = %+v", e.Chips)
	}
	if got := h.calls(); got != 2 {
		t.Errorf("the model saw %d calls, want the run's answer plus the generation", got)
	}

	// A client that missed the event recovers the chips from the snapshot, and
	// the generation's spend reached the session's totals both live and on
	// disk — the run that would have recorded it was already over.
	snap := h.session(sid).Hub().Snapshot()
	if snap.Followups == nil || snap.Followups.MessageID != e.MessageID {
		t.Errorf("snapshot followups = %+v, want the event's chips", snap.Followups)
	}
	if snap.Usage.Input != 120 || snap.Usage.Output != 30 {
		t.Errorf("snapshot usage = %+v, want the generation's 120/30 included", snap.Usage)
	}
	usage, _ := h.session(sid).store.UsageTotals()
	if usage.Input != 120 || usage.Output != 30 {
		t.Errorf("recorded usage = %+v, want the generation's 120/30", usage)
	}
}

// The repair gate itself, pinned end to end: an answer the model finished but
// broke is repaired and its chips published; the same breakage under a token
// cap stays silence, because closing a truncated document's brackets would
// pass truncation off as an answer. This exercises the call site in
// generateFollowups, not just the pure function.
func TestFollowupsRepairGateEndToEnd(t *testing.T) {
	brokenTurn := func(stop llm.StopReason) llm.Response {
		return llm.Response{
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockText,
				Text: `{"follow_ups": [{"title": "深挖", "prompt": "持久化机制是什么"}`,
			}}},
			StopReason: stop,
		}
	}

	h := newHarness(t, scriptedTurns(textTurn("这是一个木马。"), brokenTurn(llm.StopEndTurn)),
		func(c *Config) { c.Followups = true })
	sid := h.createSession()
	s := h.start(sid, "分析一下这个样本")
	s.wait(t, EvRunEnd)
	e := s.wait(t, EvFollowups)
	if len(e.Chips) != 1 || e.Chips[0].Prompt != "持久化机制是什么" {
		t.Errorf("chips = %+v, want the repaired chip published", e.Chips)
	}

	h2 := newHarness(t, scriptedTurns(textTurn("done."), brokenTurn(llm.StopMaxTokens)),
		func(c *Config) { c.Followups = true })
	sid2 := h2.createSession()
	s2 := h2.start(sid2, "再分析一个")
	s2.wait(t, EvRunEnd)
	// Give a hypothetical goroutine its chance to (wrongly) publish.
	time.Sleep(300 * time.Millisecond)
	if snap := h2.session(sid2).Hub().Snapshot(); snap.Followups != nil {
		t.Errorf("a token-capped broken answer produced chips: %+v", snap.Followups.Chips)
	}
}

// The zero-value default — which is also what -followups=false sets — means no
// generation call at all, so the client falls back to rule-based matching.
func TestFollowupsStayOffWithoutTheSwitch(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("done."), followupTurn(t)))
	sid := h.createSession()
	s := h.start(sid, "hi")
	s.wait(t, EvRunEnd)
	// Give a hypothetical goroutine its chance: the assertion is that none ran.
	time.Sleep(300 * time.Millisecond)
	if got := h.calls(); got != 1 {
		t.Errorf("the model saw %d calls, want only the run's own", got)
	}
	if snap := h.session(sid).Hub().Snapshot(); snap.Followups != nil {
		t.Errorf("snapshot followups = %+v, want none with the switch off", snap.Followups)
	}
}

// A failed run produced nothing worth suggesting a next step from.
func TestFollowupsSkippedWhenTheRunFails(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) {
		return llm.Response{}, errors.New("provider is down")
	}, func(c *Config) { c.Followups = true })
	sid := h.createSession()
	s := h.start(sid, "hi")
	if e := s.wait(t, EvRunEnd); e.Error == "" {
		t.Fatal("precondition: the run should have failed")
	}
	time.Sleep(300 * time.Millisecond)
	if got := h.calls(); got != 1 {
		t.Errorf("the model saw %d calls, want only the failed run's own", got)
	}
}
