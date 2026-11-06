package agent

import (
	"math"
	"strings"

	"github.com/yosukeno/pogo/llm"
)

// Refresh on the run goroutine, outside active tool batches. One snapshot feeds
// the actual model prompt and its cached budget/overhead facts together.
func (a *Agent) refreshRuntimePrompt() {
	if a.runtimePrompt == nil {
		return
	}
	next := strings.TrimSpace(a.runtimePrompt())
	if next == a.runtimePromptSnapshot {
		return
	}
	a.runtimePromptSnapshot = next
	parts := a.overheadPartsFor(a.schemas)
	tokens := int64(len(a.modelSystemPrompt()) / llm.BytesPerToken)
	a.overheadMu.Lock()
	a.systemPromptTokens = tokens
	a.fixedCostTokens = tokens + a.toolSchemaTokens
	a.overheadParts = parts
	a.overheadMu.Unlock()
	a.measuredThrough = math.MaxInt
}
