package agent

import (
	"math"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func TestRuntimePromptRefreshKeepsPromptBudgetAndHistoryConsistent(t *testing.T) {
	var full atomic.Bool
	narrow := "Current project only."
	broad := strings.TrimSpace("Full account project access: " + strings.Repeat("account workspace ", 40))
	a := New(Config{Registry: tools.NewRegistry(), SystemPrompt: "Base instructions.", RuntimePrompt: func() string {
		if full.Load() {
			return broad
		}
		return narrow
	}})
	before := a.OverheadTokens()
	a.messages = []llm.Message{llm.UserText("Keep this history")}
	a.lastInput, a.measuredThrough = 100, 1
	full.Store(true)
	a.refreshRuntimePrompt()
	if !strings.Contains(a.modelSystemPrompt(), broad) || strings.Contains(a.modelSystemPrompt(), narrow) {
		t.Fatal("prompt did not follow the current mode")
	}
	if a.OverheadTokens() <= before || a.measuredThrough != math.MaxInt {
		t.Fatal("runtime prompt retained stale token facts")
	}
	total, parts := a.OverheadBreakdown()
	if total != a.OverheadTokens() {
		t.Fatal("overhead view disagrees")
	}
	found := false
	for _, p := range parts {
		if p.Kind == OverheadPrompt && p.Name == PromptPartRuntime && p.Tokens > 0 {
			found = true
		}
	}
	if !found || len(a.messages) != 1 || a.messages[0].Text() != "Keep this history" {
		t.Fatal("runtime hint changed history or lost overhead attribution")
	}
	full.Store(false)
	a.refreshRuntimePrompt()
	if a.OverheadTokens() != before || !strings.Contains(a.modelSystemPrompt(), narrow) {
		t.Fatal("returning to ordinary mode retained a broader runtime hint")
	}
}
