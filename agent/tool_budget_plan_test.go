package agent

import (
	"math/rand"
	"reflect"
	"testing"
)

func TestToolBudgetPlanDefaultsAndAllocation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cfg      ToolOutputBudgetConfig
		prompt   int64
		costs    []int64
		failures []bool
		want     []int64
		target   int64
		required int64
		headroom int64
	}{
		{name: "unknown window remains usable", costs: []int64{10000, 10000, 10000, 10000}, want: []int64{6144, 6144, 6144, 6144}, target: 24576, required: 512, headroom: -1},
		{name: "small result releases capacity", cfg: ToolOutputBudgetConfig{PerResultTokens: 2000, BatchTokens: 1000}, costs: []int64{10, 1000, 1000}, want: []int64{10, 495, 495}, target: 1000, required: 266, headroom: -1},
		{name: "error priority then normal results catch up", cfg: ToolOutputBudgetConfig{PerResultTokens: 2000, BatchTokens: 900}, costs: []int64{1000, 1000, 1000}, failures: []bool{false, true}, want: []int64{194, 512, 194}, target: 900, required: 384, headroom: -1},
		{name: "insufficient batch retains both identities", cfg: ToolOutputBudgetConfig{BatchTokens: 200}, costs: []int64{1000, 1000}, failures: []bool{true, false}, want: []int64{128, 128}, target: 200, required: 256, headroom: -1},
		{name: "explicit small per-result cap has identity exception", cfg: ToolOutputBudgetConfig{PerResultTokens: 64, BatchTokens: 10000}, costs: []int64{200, 10}, want: []int64{128, 10}, target: 138, required: 138, headroom: -1},
		{name: "live headroom bounds batch", cfg: ToolOutputBudgetConfig{ContextWindow: 32000, MaxOutputTokens: 1000}, prompt: 27000, costs: []int64{10000, 10000, 10000}, want: []int64{800, 800, 800}, target: 2400, required: 384, headroom: 2400},
		{name: "tiny window does not drop results", cfg: ToolOutputBudgetConfig{ContextWindow: 1}, costs: []int64{200, 3}, want: []int64{128, 3}, target: 0, required: 131, headroom: 0},
		{name: "unspecified output cap reserves client default", cfg: ToolOutputBudgetConfig{ContextWindow: 32000}, prompt: 14000, costs: []int64{1000}, want: []int64{128}, target: 16, required: 128, headroom: 16},
		{name: "negative estimates cannot create debt", cfg: ToolOutputBudgetConfig{ContextWindow: -1}, prompt: -500, costs: []int64{-5, 0, 10}, want: []int64{0, 0, 10}, target: 10, required: 10, headroom: -1},
		{name: "disabled does not shrink", cfg: ToolOutputBudgetConfig{ContextWindow: 1, Disabled: true}, costs: []int64{10000, 7}, want: []int64{10000, 7}, target: 10007, required: 135, headroom: 0},
		{name: "integer remainder follows call order", cfg: ToolOutputBudgetConfig{BatchTokens: 1001}, costs: []int64{1000, 1000, 1000}, want: []int64{334, 334, 333}, target: 1001, required: 384, headroom: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := planToolResultBudget(tc.cfg, tc.prompt, tc.costs, tc.failures)
			if !reflect.DeepEqual(got.PerResult, tc.want) || got.TargetTokens != tc.target || got.RequiredTokens != tc.required || got.Headroom != tc.headroom {
				t.Fatalf("plan = %+v; want allocations %v, target %d, minimum %d, headroom %d", got, tc.want, tc.target, tc.required, tc.headroom)
			}
			if again := planToolResultBudget(tc.cfg, tc.prompt, tc.costs, tc.failures); !reflect.DeepEqual(got, again) {
				t.Fatalf("same batch produced different allocations: %+v / %+v", got, again)
			}
		})
	}
}

func TestToolBudgetPlanLargeIntegersDoNotWrap(t *testing.T) {
	const largest = int64(1<<63 - 1)
	plan := planToolResultBudget(ToolOutputBudgetConfig{PerResultTokens: largest, BatchTokens: largest}, 0, []int64{largest, largest}, nil)
	if !reflect.DeepEqual(plan.PerResult, []int64{largest/2 + 1, largest / 2}) || plan.TargetTokens != largest {
		t.Fatalf("large allocation wrapped or lost fairness: %+v", plan)
	}
	plan = planToolResultBudget(ToolOutputBudgetConfig{ContextWindow: 64000, MaxOutputTokens: largest}, largest, []int64{largest}, nil)
	if plan.Headroom != 0 || plan.TargetTokens != 0 || !reflect.DeepEqual(plan.PerResult, []int64{128}) {
		t.Fatalf("large prompt/output charges created false capacity: %+v", plan)
	}
}

func TestToolBudgetPlanBatchInvariants(t *testing.T) {
	random := rand.New(rand.NewSource(42))
	for iteration := 0; iteration < 200; iteration++ {
		costs := make([]int64, random.Intn(20)+1)
		failures := make([]bool, len(costs))
		for i := range costs {
			costs[i], failures[i] = int64(random.Intn(10000)), random.Intn(4) == 0
		}
		cfg := ToolOutputBudgetConfig{PerResultTokens: int64(random.Intn(2000) + 1), BatchTokens: int64(random.Intn(6000) + 1)}
		plan := planToolResultBudget(cfg, 0, costs, failures)
		var total int64
		for i, allocation := range plan.PerResult {
			minimum := min(costs[i], int64(128))
			maximum := min(costs[i], max(cfg.PerResultTokens, minimum))
			if allocation < minimum || allocation > maximum {
				t.Fatalf("iteration %d: result %d allocation %d outside identity/cap [%d,%d]", iteration, i, allocation, minimum, maximum)
			}
			total += allocation
		}
		if total > max(plan.TargetTokens, plan.RequiredTokens) {
			t.Fatalf("iteration %d: total %d exceeds target/minimum %+v", iteration, total, plan)
		}
		if !reflect.DeepEqual(plan, planToolResultBudget(cfg, 0, costs, failures)) {
			t.Fatalf("iteration %d: allocation is not repeatable", iteration)
		}
	}
}

// Registered Skill instructions are allocated whole, ahead of the batch and
// outside the per-result and batch limits; only known headroom cuts them, never
// below the identity allowance. The other results are planned as before within
// what is left, and the target includes the whole allocation.
func TestToolBudgetPlanKeepsSkillInstructionsWhole(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cfg      ToolOutputBudgetConfig
		prompt   int64
		costs    []int64
		whole    []bool
		want     []int64
		target   int64
		headroom int64
	}{
		{name: "outside per-result and batch limits", cfg: ToolOutputBudgetConfig{PerResultTokens: 512, BatchTokens: 1024},
			costs: []int64{8400, 3000}, whole: []bool{true, false}, want: []int64{8400, 512}, target: 8912, headroom: -1},
		{name: "known headroom still bounds it", cfg: ToolOutputBudgetConfig{ContextWindow: 32000, MaxOutputTokens: 1000}, prompt: 27000,
			costs: []int64{8400, 1000}, whole: []bool{true, false}, want: []int64{2400, 128}, target: 2400, headroom: 2400},
		{name: "a full context keeps the identity allowance", cfg: ToolOutputBudgetConfig{ContextWindow: 32000, MaxOutputTokens: 1000}, prompt: 29400,
			costs: []int64{8400}, whole: []bool{true}, want: []int64{128}, target: 128, headroom: 0},
		{name: "several share headroom in call order", cfg: ToolOutputBudgetConfig{ContextWindow: 32000, MaxOutputTokens: 1000}, prompt: 20000,
			costs: []int64{5000, 5000}, whole: []bool{true, true}, want: []int64{5000, 4400}, target: 9400, headroom: 9400},
		{name: "default 1M window", cfg: ToolOutputBudgetConfig{ContextWindow: 1048576, MaxOutputTokens: 65536}, prompt: 20000,
			costs: []int64{8400, 10000}, whole: []bool{true, false}, want: []int64{8400, 8192}, target: 16592, headroom: 910612},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := planToolResultBudgetWhole(tc.cfg, tc.prompt, tc.costs, nil, tc.whole)
			if !reflect.DeepEqual(got.PerResult, tc.want) || got.TargetTokens != tc.target || got.Headroom != tc.headroom {
				t.Fatalf("plan = %+v; want allocations %v, target %d, headroom %d", got, tc.want, tc.target, tc.headroom)
			}
		})
	}

	// Without whole results the plan is exactly the ordinary one.
	random := rand.New(rand.NewSource(7))
	for iteration := 0; iteration < 200; iteration++ {
		costs := make([]int64, random.Intn(12)+1)
		failures := make([]bool, len(costs))
		for i := range costs {
			costs[i], failures[i] = int64(random.Intn(20000)), random.Intn(4) == 0
		}
		cfg := ToolOutputBudgetConfig{ContextWindow: random.Intn(300000), MaxOutputTokens: int64(random.Intn(20000))}
		prompt := int64(random.Intn(200000))
		if a, b := planToolResultBudget(cfg, prompt, costs, failures), planToolResultBudgetWhole(cfg, prompt, costs, failures, make([]bool, len(costs))); !reflect.DeepEqual(a, b) {
			t.Fatalf("iteration %d: no whole results changed the plan: %+v / %+v", iteration, a, b)
		}
	}
}
