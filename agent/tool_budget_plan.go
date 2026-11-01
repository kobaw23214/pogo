package agent

// ToolOutputBudgetConfig limits the text a completed tool batch contributes to
// the next model request. These are estimates (ceil(text bytes / 4), including
// citation text), not provider-tokenizer counts or image token charges.
//
// Non-positive limits select defaults. Disabled preserves the original costs.
// A context window is optional: unknown must not be treated as zero free space.
type ToolOutputBudgetConfig struct {
	ContextWindow   int
	MaxOutputTokens int64
	PerResultTokens int64
	BatchTokens     int64
	Disabled        bool
}

type toolResultBudgetPlan struct {
	PerResult []int64
	// TargetTokens is the desired total after per-result, batch and headroom
	// limits. Small observations do not consume the rest of an unused budget.
	TargetTokens int64
	// RequiredTokens is the sum of min(original cost, 128) identity allowances.
	// When it exceeds TargetTokens, allocations may exceed the target (and the
	// available headroom): tool-call/result pairing, failure identity and recovery
	// handles must survive. The caller still has to perform context recovery.
	RequiredTokens int64
	// Headroom is the non-negative space after the prompt, output allowance and
	// safety reserve; -1 means that the context window is unknown.
	Headroom int64
}

const (
	toolBudgetDefaultPerResult int64 = 8_192
	toolBudgetDefaultBatch     int64 = 24_576
	toolBudgetDefaultMaxOutput int64 = 16_384
	toolBudgetIdentity         int64 = 128
	toolBudgetErrorPriority    int64 = 512
	toolBudgetMaxInt64         int64 = 1<<63 - 1
)

// planToolResultBudget is a deterministic allocation over tool-call order, not
// parallel completion order. It never drops a result. It first reserves a small
// identity allowance for each non-empty result, gives errors priority up to 512
// estimated tokens when space allows, then fairly fills the remaining results.
//
// The identity allowance is the sole exception to explicit per-result and batch
// limits. It is a planning allowance, not proof that a serialized identity and
// its citation fit in exactly 128 tokens. The rendering layer must retain those
// fields even when its measured minimum is larger and report that overflow.
func planToolResultBudget(cfg ToolOutputBudgetConfig, promptTokens int64, costs []int64, isError []bool) toolResultBudgetPlan {
	plan := toolResultBudgetPlan{
		PerResult: make([]int64, len(costs)),
		Headroom:  -1,
	}
	window := int64(cfg.ContextWindow)
	if window > 0 {
		reserve := window / 20
		if reserve < 1_024 {
			reserve = 1_024
		}
		maxOutput := cfg.MaxOutputTokens
		if maxOutput <= 0 {
			// Match llm.New's default instead of treating an unspecified output
			// cap as zero. Small windows still expose the identity overflow.
			maxOutput = toolBudgetDefaultMaxOutput
		}
		plan.Headroom = toolBudgetRemaining(window, promptTokens, maxOutput, reserve)
	}

	perResult := cfg.PerResultTokens
	if perResult <= 0 {
		perResult = toolBudgetDefaultPerResult
		if window > 0 && window/32 < perResult {
			perResult = window / 32
		}
	}
	batch := cfg.BatchTokens
	if batch <= 0 {
		batch = toolBudgetDefaultBatch
		if window > 0 && window/10 < batch {
			batch = window / 10
		}
	}

	caps := make([]int64, len(costs))
	priorityCaps := make([]int64, len(costs))
	var total, cappedTotal int64
	for i, rawCost := range costs {
		cost := rawCost
		if cost < 0 {
			cost = 0
		}
		total = toolBudgetAdd(total, cost)
		minimum := cost
		if minimum > toolBudgetIdentity {
			minimum = toolBudgetIdentity
		}
		plan.RequiredTokens = toolBudgetAdd(plan.RequiredTokens, minimum)
		if cfg.Disabled {
			plan.PerResult[i] = cost
			continue
		}

		cap := cost
		if cap > perResult {
			cap = perResult
		}
		if cap < minimum {
			cap = minimum
		}
		caps[i] = cap
		cappedTotal = toolBudgetAdd(cappedTotal, cap)
		plan.PerResult[i] = minimum
		if i < len(isError) && isError[i] {
			priorityCaps[i] = cap
			if priorityCaps[i] > toolBudgetErrorPriority {
				priorityCaps[i] = toolBudgetErrorPriority
			}
		}
	}
	if cfg.Disabled {
		plan.TargetTokens = total
		return plan
	}

	plan.TargetTokens = batch
	if plan.Headroom >= 0 && plan.TargetTokens > plan.Headroom {
		plan.TargetTokens = plan.Headroom
	}
	if plan.TargetTokens > cappedTotal {
		plan.TargetTokens = cappedTotal
	}
	remaining := plan.TargetTokens - plan.RequiredTokens
	if remaining <= 0 {
		return plan
	}
	remaining = toolBudgetWaterfill(plan.PerResult, priorityCaps, remaining)
	toolBudgetWaterfill(plan.PerResult, caps, remaining)
	return plan
}

// toolBudgetRemaining uses clamped subtraction so negative estimates cannot
// create capacity and large values cannot wrap into a positive allowance.
func toolBudgetRemaining(capacity int64, charges ...int64) int64 {
	for _, charge := range charges {
		if charge <= 0 {
			continue
		}
		if charge >= capacity {
			return 0
		}
		capacity -= charge
	}
	return capacity
}

func toolBudgetAdd(a, b int64) int64 {
	if b > toolBudgetMaxInt64-a {
		return toolBudgetMaxInt64
	}
	return a + b
}

// toolBudgetWaterfill raises the smallest allocations toward their caps. Existing
// lower bounds (including error priority) stay intact, so normal results catch up
// before already larger errors receive more. Binary search avoids iteration per
// token even for a caller-supplied MaxInt64 limit. Remainders follow call order.
func toolBudgetWaterfill(allocations, caps []int64, budget int64) int64 {
	if budget <= 0 {
		return budget
	}
	var high int64
	for i, cap := range caps {
		if cap > allocations[i] && cap > high {
			high = cap
		}
	}
	needed := func(level int64) (int64, bool) {
		var sum int64
		for i, cap := range caps {
			raised := level
			if raised > cap {
				raised = cap
			}
			if raised <= allocations[i] {
				continue
			}
			increment := raised - allocations[i]
			if increment > budget-sum {
				return 0, false
			}
			sum += increment
		}
		return sum, true
	}
	var low int64
	for low < high {
		difference := high - low
		middle := low + difference/2 + difference%2
		if _, fits := needed(middle); fits {
			low = middle
		} else {
			high = middle - 1
		}
	}
	for i, cap := range caps {
		raised := low
		if raised > cap {
			raised = cap
		}
		if raised > allocations[i] {
			budget -= raised - allocations[i]
			allocations[i] = raised
		}
	}
	// If all caps were reached, the surplus belongs to the next priority stage.
	// Otherwise the next integer water level costs more than the remainder;
	// distribute at most one token to each result at the current level.
	for i, cap := range caps {
		if budget == 0 {
			break
		}
		if allocations[i] == low && allocations[i] < cap {
			allocations[i]++
			budget--
		}
	}
	return budget
}
