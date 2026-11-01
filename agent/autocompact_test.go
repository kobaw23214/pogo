package agent

import (
	"testing"

	"github.com/yosukeno/pogo/llm"
)

// The default is manual, exactly as before the flag existed: every empty and
// explicit-off spelling resolves to a zero Threshold.
func TestParseCompactSpecOffIsTheDefault(t *testing.T) {
	for _, spec := range []string{"", "off"} {
		for _, window := range []int{0, 200_000, 1_000_000} {
			cfg, err := ParseCompactSpec(spec, window)
			if err != nil || cfg.Threshold != 0 {
				t.Errorf("ParseCompactSpec(%q, %d) = %+v, %v; want the zero config", spec, window, cfg, err)
			}
		}
	}
}

// "auto" keeps a reserve of max(16384, a twentieth of the window): the floor
// bites on a 200K window (16384 > 10000), the twentieth on a 1M one (50000).
// On both, compaction arms only after the clearing trigger (four fifths) has
// long been passed — the design's ordering, reversible means first.
func TestParseCompactSpecAutoReserve(t *testing.T) {
	small, err := ParseCompactSpec("auto", 200_000)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(200_000 - CompactReserveFloor); small.Threshold != want {
		t.Errorf("200K window: threshold %d, want %d (the floor reserve)", small.Threshold, want)
	}
	if edit, _ := ParseContextEdit("auto", 200_000); small.Threshold <= edit.Trigger {
		t.Errorf("compaction (%d) must arm above the clearing trigger (%d), or the lossy "+
			"path would run while the reversible one still had work to do", small.Threshold, edit.Trigger)
	}

	wide, err := ParseCompactSpec("auto", 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(1_000_000 - 50_000); wide.Threshold != want {
		t.Errorf("1M window: threshold %d, want %d (a twentieth reserve)", wide.Threshold, want)
	}
}

// The same no-window rule ParseContextEdit applies: "auto" resolves to manual
// rather than a made-up threshold, because a percentage of nothing is nothing.
func TestParseCompactSpecAutoWithoutWindowIsManual(t *testing.T) {
	for _, spec := range []string{"auto", "auto:80%"} {
		cfg, err := ParseCompactSpec(spec, 0)
		if err != nil || cfg.Threshold != 0 {
			t.Errorf("ParseCompactSpec(%q, 0) = %+v, %v; want the zero config", spec, cfg, err)
		}
	}
}

func TestParseCompactSpecExplicitForms(t *testing.T) {
	pct, err := ParseCompactSpec("auto:80%", 200_000)
	if err != nil || pct.Threshold != 160_000 {
		t.Errorf("80%% of 200K = %+v, %v; want 160000", pct, err)
	}
	n, err := ParseCompactSpec("auto:120000", 200_000)
	if err != nil || n.Threshold != 120_000 {
		t.Errorf("absolute form = %+v, %v; want 120000 (the window must not rescale it)", n, err)
	}
	for _, spec := range []string{"on", "auto:", "auto:0", "auto:200%", "auto:0%", "auto:-5", "auto:abc", "80%"} {
		if _, err := ParseCompactSpec(spec, 200_000); err == nil {
			t.Errorf("ParseCompactSpec(%q) accepted a spec it should refuse", spec)
		}
	}
}

// ShouldCompact's floor: a conversation with less than two messages has nothing
// a summary could be smaller than, and Compact itself would refuse it.
func TestShouldCompactDeclinesShortConversations(t *testing.T) {
	a := New(Config{Client: &summariser{}, Registry: emptyRegistry(), Compact: CompactConfig{Threshold: 1}})
	a.SetMessages([]llm.Message{llm.UserText("hi")})
	if a.ShouldCompact() {
		t.Error("one message should never arm compaction")
	}
}

// The threshold is crossed on promptTokens — the same part-measured,
// part-estimated figure the clearing trigger reads — and honoured from both
// sides of the line.
func TestShouldCompactReadsTheThreshold(t *testing.T) {
	msgs := history(4, 2_000)
	for _, tc := range []struct {
		threshold int64
		want      bool
	}{
		{1, true},        // anything arms on a real history
		{1 << 62, false}, // an unreachable threshold never arms
	} {
		a := New(Config{Client: &summariser{}, Registry: emptyRegistry(), Compact: CompactConfig{Threshold: tc.threshold}})
		a.SetMessages(msgs)
		if got := a.ShouldCompact(); got != tc.want {
			t.Errorf("threshold %d: ShouldCompact = %v, want %v", tc.threshold, got, tc.want)
		}
	}
}

// Off is not just a zero threshold on the config — the agent built without the
// feature must never arm, whatever the history.
func TestShouldCompactOffNeverArms(t *testing.T) {
	a := New(Config{Client: &summariser{}, Registry: emptyRegistry()})
	a.SetMessages(history(4, 2_000))
	if a.ShouldCompact() {
		t.Error("a zero config must leave compaction manual")
	}
}

// promptTokens' guard was "clearing is off, so nobody is asking". Compaction
// is a second asker: with clearing off and compaction armed, the estimate has
// to exist, or ShouldCompact would read zero forever and the feature would be
// a flag that does nothing.
func TestPromptTokensAnswersWhenOnlyCompactionAsks(t *testing.T) {
	msgs := history(4, 2_000)
	silent := New(Config{Client: &summariser{}, Registry: emptyRegistry(), ToolOutputBudget: ToolOutputBudgetConfig{Disabled: true}})
	silent.SetMessages(msgs)
	if got := silent.promptTokens(); got != 0 {
		t.Errorf("nobody asking: promptTokens = %d, want 0 (the walk is not free)", got)
	}
	asking := New(Config{Client: &summariser{}, Registry: emptyRegistry(), Compact: CompactConfig{Threshold: 1}, ToolOutputBudget: ToolOutputBudgetConfig{Disabled: true}})
	asking.SetMessages(msgs)
	if got := asking.promptTokens(); got <= 0 {
		t.Errorf("compaction asking with clearing off: promptTokens = %d, want > 0", got)
	}
}
