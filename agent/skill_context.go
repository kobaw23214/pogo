package agent

import (
	"fmt"
	"github.com/yosukeno/pogo/tools"
	"math"
)

// ReplaceSkillContext is serialized with runs/compaction by the owner. Build
// and validate first, persist the exact policy, then commit all runtime views.
// Stateful native tools, history, usage and source registry are retained.
// parts optionally labels prompt for the overhead breakdown, under the same
// rule as Config.SystemPromptParts.
func (a *Agent) ReplaceSkillContext(prompt string, update tools.SkillContextUpdate, persist func() error, parts ...PromptPart) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return fmt.Errorf("cannot replace Skills during an active run")
	}
	registry, apply, err := a.registry.WithSkillContext(update)
	if err != nil {
		return err
	}
	if err := persist(); err != nil {
		return err
	}
	apply()
	a.registry, a.systemPrompt = registry, prompt
	a.systemPromptParts = labeledSystemPrompt(prompt, parts)
	a.resetMCPSelection()
	// Even equal-sized prompt changes invalidate the provider's old reading.
	a.measuredThrough = math.MaxInt
	return nil
}
