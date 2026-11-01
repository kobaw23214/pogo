package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
)

func TestMCPEvidenceReviewIsBoundedAndPreservesCallsAndFinalAnswer(t *testing.T) {
	caller := &stampingCaller{}
	registry, _ := guideCatalog(t, caller)
	client := &viewClient{}
	gates := 0
	a := New(Config{Client: client, Registry: registry, Citations: true,
		Gate: gateFunc(func(GateRequest) GateDecision { gates++; return Allow }),
	})
	for run := 0; run < 2; run++ {
		firstLabel, secondLabel := 1+run*2, 2+run*2
		draft := fmt.Sprintf("Draft finding. [1](#cite:S%d) [1](#cite:S%d)", firstLabel, secondLabel)
		final := fmt.Sprintf("Complete finding with its scope and unresolved limits. [1](#cite:S%d) [1](#cite:S%d)", firstLabel, secondLabel)
		client.responses = []llm.Response{
			toolUseResp("first", "mcp_synthetic_readonly", `{"query":"first"}`),
			toolUseResp("second", "mcp_synthetic_readonly", `{"query":"second"}`),
			answerTurn(draft), answerTurn(final),
		}
		events := drainEvents(t, a, "research")
		reviews := 0
		for i, event := range events {
			if event.Kind == EventSteer && strings.Contains(event.Text, "Evidence review") {
				reviews++
				if !resultsSettledBefore(events, i, "second") {
					t.Fatal("review preceded complete tool results")
				}
			}
		}
		if reviews != 1 || gates != (run+1)*2 || caller.count("readonly") != (run+1)*2 {
			t.Fatalf("run %d: reviews=%d gates=%d calls=%d", run, reviews, gates, caller.count("readonly"))
		}
		messages := a.Messages()
		if got := messages[len(messages)-1].Text(); got != final {
			t.Fatalf("final deliverable = %q", got)
		}
	}
}

func TestMCPEvidenceReviewRequiresMultipleReadsAndCitation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		counts map[string]int
		answer string
		want   bool
	}{
		{"no reads", nil, "[1](#cite:S1)", false},
		{"one read", map[string]int{"a": 1}, "[1](#cite:S1)", false},
		{"uncited answer", map[string]int{"a": 2}, "done", false},
		{"two scopes", map[string]int{"a": 1, "b": 1}, "[1](#cite:S1)", true},
		{"repeat already warned", map[string]int{"a": 2}, "[1](#cite:S1)", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := mcpReadRepeats{counts: tc.counts, warned: true}
			if got := tracker.reviewNotice(tc.answer) != ""; got != tc.want {
				t.Fatalf("review requested = %v, want %v", got, tc.want)
			}
		})
	}
}
