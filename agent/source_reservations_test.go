package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func citationHistory(t *testing.T, label, answer string) []llm.Message {
	t.Helper()
	return []llm.Message{
		{Role: llm.RoleUser, Content: []llm.Block{sourceResultBlock(t, Source{Label: label, Type: SourceWebPage, CallID: "old-page", Title: "Earlier evidence", Locator: "https://example.test/old"})}},
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: answer}}},
		llm.UserText("[pogo] citation check: remove unsupported S84"),
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "Corrected answer without the unavailable citation."}}},
	}
}

func TestSourceReservationsKeepHistoricalGuessesUnavailableAfterDownload(t *testing.T) {
	history := citationHistory(t, "S83", "Old unsupported statement. [1](#cite:S84) Far guess. [1](#cite:S9999)")
	s := newSourceStore("")
	s.observe(history)
	next := s.Next("browser", nil, "new-download", "host download receipt", json.RawMessage(`{"action":"downloads"}`))
	if next.Label != "S85" || next.Type != SourceTool {
		t.Fatalf("historical guess acquired a new meaning or caused a sequence jump: %+v", next)
	}
	if _, found := s.Get("S84"); found {
		t.Fatal("reservation was registered as evidence")
	}
	if len(s.All()) != 2 || len(ValidateCitations("Old claim. [1](#cite:S84)", s)) != 1 {
		t.Fatal("a later download made the old invalid citation valid")
	}
	history = append(history, llm.Message{Role: llm.RoleUser, Content: []llm.Block{sourceResultBlock(t, *next)}})
	restored := newSourceStore("")
	restored.observe(history)
	if next := restored.Next("browser", nil, "after-reload", "receipt", json.RawMessage(`{"action":"downloads"}`)); next.Label != "S86" {
		t.Fatalf("reload lost allocation history: %+v", next)
	}
	if invalid := ValidateCitations("Old. [1](#cite:S84) Real. [1](#cite:S85)", restored); len(invalid) != 1 || invalid[0] != "S84" {
		t.Fatalf("restored real and reserved labels were confused: %v", invalid)
	}
}

func TestSourceReservationsSurviveRewindWithoutErasingRealSources(t *testing.T) {
	s := newSourceStore("")
	history := citationHistory(t, "S83", "Supported. [1](#cite:S83) Unsupported. [1](#cite:s84)")
	s.observe(history)
	// In-memory rewind may remove the old answer, but cannot recycle its label.
	s.observe(history[:1])
	if got, ok := s.Get("S83"); !ok || got.Title != "Earlier evidence" {
		t.Fatal("reserving an actual source erased it")
	}
	if next := s.Next("read", nil, "after-rewind", "new evidence", nil); next.Label != "S85" {
		t.Fatalf("rewind recycled an earlier answer's label: %+v", next)
	}
	s.observe(history)
	if next := s.Next("read", nil, "again", "new evidence", nil); next.Label != "S86" {
		t.Fatalf("repeated restoration moved allocation backwards: %+v", next)
	}
}

func TestSourceReservationsOnlyUseActualGeneratedLabelShapes(t *testing.T) {
	for _, text := range []string{
		"S1 is an example", "`[1](#cite:S1)`", "```md\n[1](#cite:S1)\n```",
		"[1](https://example.test/#cite:S1)", "[1](#cite:S1]", "[1](#cite:S01)",
		"[1](#cite:S0)", "[1](#cite:S-1)", "[1](#cite:S1suffix)",
	} {
		s := newSourceStore("")
		s.reserveCitations(text)
		if next := s.Next("read", nil, "first", "evidence", nil); next.Label != "S1" {
			t.Errorf("non-anchor %q reserved a label: %s", text, next.Label)
		}
	}
	s := newSourceStore("")
	// A Markdown link remains live even with an invalid display label. Reserve
	// its valid target so correction cannot make it point at future evidence.
	s.reserveCitations("[wrong display](#cite:s1) [1](#cite:S9999) [1](#cite:S10001)")
	if next := s.Next("read", nil, "first", "evidence", nil); next.Label != "S2" {
		t.Fatalf("case normalization or sparse reservation failed: %+v", next)
	}
}

func TestSourceReservationsPreserveRealLongLabels(t *testing.T) {
	s := newSourceStore("")
	s.observe(citationHistory(t, "S9999", "Existing. [1](#cite:S9999) Unknown. [1](#cite:S10000)"))
	next := s.Next("read", nil, "long-label", "real evidence", nil)
	if next.Label != "S10001" {
		t.Fatalf("long reservation reused or rejected: %+v", next)
	}
	if invalid := ValidateCitations("Real. [1](#cite:S10001) Same. [1](#cite:s10001) Unknown. [1](#cite:S10000)", s); len(invalid) != 1 || invalid[0] != "S10000" {
		t.Fatalf("actual long source failed validation: %v", invalid)
	}
	restored := newSourceStore("")
	restored.observe([]llm.Message{{Role: llm.RoleUser, Content: []llm.Block{sourceResultBlock(t, *next)}}})
	if got := restored.Next("read", nil, "longer", "real evidence", nil); got.Label != "S10002" {
		t.Fatalf("real long source did not survive reload: %+v", got)
	}
}

func TestSourceReservationsAndParallelAllocationAreRaceSafe(t *testing.T) {
	s := newSourceStore("")
	s.reserveCitations("[1](#cite:S2) [1](#cite:S84) [1](#cite:S9999)")
	const count = 128
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.reserveCitations(fmt.Sprintf("[1](#cite:S%d)", 2000+i))
			src := s.Next("read", nil, fmt.Sprint(i), "real evidence", nil)
			if src.Label == "S2" || src.Label == "S84" || src.Label == "S9999" {
				t.Errorf("reserved label allocated: %s", src.Label)
			}
			if got, ok := s.Get(src.Label); !ok || got.CallID != src.CallID {
				t.Errorf("parallel allocation lost source %s", src.Label)
			}
		}(i)
	}
	wg.Wait()
	labels := make(map[string]bool)
	for _, source := range s.All() {
		if labels[source.Label] {
			t.Fatalf("duplicate label: %s", source.Label)
		}
		labels[source.Label] = true
	}
	if len(labels) != count || s.seq != count+2 {
		t.Fatalf("reservation changed real source count or caused a large jump: count=%d seq=%d", len(labels), s.seq)
	}
}

func TestLiveAssistantCitationIsReservedBeforeItsToolResults(t *testing.T) {
	call := downloadReceiptCall()
	call.Message.Content = append([]llm.Block{{Type: llm.BlockText, Text: "Premature download claim. [1](#cite:S1)"}}, call.Message.Content...)
	link := &downloadReceiptLink{reply: tools.BrowserReply{OK: true, Data: json.RawMessage(`{"supported":true,"downloads":[]}`)}}
	client := &fakeClient{responses: []llm.Response{call, downloadReceiptAnswer("Recorded status only. [1](#cite:S2)")}}
	a := New(Config{Client: client, Registry: tools.NewRegistry(&tools.Browser{Link: link}), Citations: true})
	events := citeRun(t, a, "Check download status")
	ends := eventsOf(events, EventToolEnd)
	if len(ends) != 1 || ends[0].Source == nil || ends[0].Source.Label != "S2" {
		t.Fatalf("same-message premature anchor acquired the tool result: %+v", ends)
	}
	if len(eventsOf(events, EventSteer)) != 0 || len(ValidateCitations("Premature. [1](#cite:S1)", a.sources)) != 1 {
		t.Fatal("live reservation either broke valid final citation or registered the premature anchor")
	}
}

func TestResumedDownloadSkipsHistoricalGuessAndStreamsRealCitation(t *testing.T) {
	link := &downloadReceiptLink{reply: tools.BrowserReply{OK: true, Data: json.RawMessage(`{"supported":true,"downloads":[]}`)}}
	answer := "当前仅有下载状态记录。[1](#cite:S85)"
	client := &fakeClient{responses: []llm.Response{downloadReceiptCall(), downloadReceiptAnswer(answer)}, deltas: func(call int, emit func(llm.Delta)) {
		if call == 2 {
			emit(llm.Delta{Kind: llm.DeltaText, Text: answer})
		}
	}}
	a := New(Config{Client: client, Registry: tools.NewRegistry(&tools.Browser{Link: link}), Citations: true})
	a.SetMessages(citationHistory(t, "S83", "Old unsupported answer. [1](#cite:S84)"))
	events := citeRun(t, a, "再检查下载状态")
	if len(eventsOf(events, EventSteer)) != 0 || client.callCount() != 2 {
		t.Fatal("a correctly stamped streaming answer required citation repair")
	}
	var streamed strings.Builder
	for _, event := range events {
		if event.Kind == EventTextDelta {
			streamed.WriteString(event.Text)
		}
	}
	if streamed.String() != answer {
		t.Fatalf("normal inline citation was lost while streaming: %q", streamed.String())
	}
	if len(ValidateCitations("Old. [1](#cite:S84)", a.sources)) != 1 {
		t.Fatal("new source legitimized the old unsupported answer")
	}
}
