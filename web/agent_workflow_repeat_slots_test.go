package web

import (
	"encoding/json"
	"sort"
	"testing"

	af "github.com/yosukeno/pogo/agentworkflow"
)

// Script timestamps measure the actual isolated programs, not their database
// receipts: queued slot wait and persistence latency must not count as overlap.
func TestAgentWorkflowRepeatGraphSharesTenExecutionSlotsAcrossContainers(t *testing.T) {
	awRequireCodeRuntimes(t)
	h, model := awGraphModelHarness(t, "", false)
	a := awTestAgent(t, h, "独立跨容器并发验收")
	source := h.studioConversationAs(aliceToken, a.ID)
	itemBody := func() *af.Step {
		step := awCodeStep("body", "python", "import time\n\ndef main(inputs):\n    start = time.monotonic_ns()\n    time.sleep(0.15)\n    end = time.monotonic_ns()\n    return {'value': inputs['value'], 'start': start, 'end': end}\n", map[string]af.CodeBinding{"value": {Type: "integer", Source: "iteration.item"}}, map[string]af.ValueSchema{"value": {Type: "integer"}, "start": {Type: "integer"}, "end": {Type: "integer"}})
		return &step
	}
	container := func(id, values string) af.Step {
		return af.Step{ID: id, Kind: "iteration", Iteration: &af.IterationSpec{Items: af.CodeBinding{Type: "array", Value: json.RawMessage(values)}, Body: itemBody(), Concurrency: 8, MaxItems: 8}}
	}
	combine := awCodeStep("combine", "python", "def main(inputs):\n    rows = inputs['left'] + inputs['right']\n    return {'count': len(rows), 'total': sum(row['value'] for row in rows)}\n", map[string]af.CodeBinding{"left": {Type: "array", Source: "steps.join.output.left.items"}, "right": {Type: "array", Source: "steps.join.output.right.items"}}, map[string]af.ValueSchema{"count": {Type: "integer"}, "total": {Type: "integer"}})
	d := awCreateCodeDefinition(t, h, a, af.Definition{Name: "两路批处理共享执行上限", Steps: []af.Step{{ID: "fork", Kind: "parallel"}, container("left", `[0,1,2,3,4,5,6,7]`), container("right", `[8,9,10,11,12,13,14,15]`), {ID: "join", Kind: "join"}, combine}, Graph: &af.Graph{Version: 1, Edges: []af.Edge{{Source: "__input", Target: "fork"}, {Source: "fork", Target: "left"}, {Source: "fork", Target: "right"}, {Source: "left", Target: "join"}, {Source: "right", Target: "join"}, {Source: "join", Target: "combine"}, {Source: "combine", Target: "__output"}}}}, "repeat-shared-slots-create")
	accepted := awStartCodeTrial(t, h, a, d, source, map[string]string{}, "repeat-shared-slots-start")
	done := awTestRun(t, h, aliceToken, a.ID, d.ID, accepted.ID, af.Completed)
	var combined struct{ Count, Total int }
	if json.Unmarshal(done.Steps[4].Output, &combined) != nil || combined.Count != 16 || combined.Total != 120 {
		t.Fatal("container outputs did not merge under their own namespaces", string(done.Steps[4].Output))
	}
	type event struct {
		time  int64
		delta int
	}
	events := make([]event, 0, 32)
	seen := map[int]bool{}
	for containerIndex, stepIndex := range []int{1, 2} {
		repeat := done.Steps[stepIndex].Repeat
		if repeat == nil || repeat.Total != 8 || len(repeat.Items) != 8 {
			t.Fatal("missing independent item receipts", repeat)
		}
		for index, item := range repeat.Items {
			var output struct {
				Value      int
				Start, End int64
			}
			if item.Status != af.Completed || json.Unmarshal(item.Output, &output) != nil || output.Value != containerIndex*8+index || output.End <= output.Start || seen[output.Value] {
				t.Fatal("invalid real script interval or changed item order", item)
			}
			seen[output.Value] = true
			events = append(events, event{output.Start, 1}, event{output.End, -1})
		}
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].time == events[j].time {
			return events[i].delta < events[j].delta
		}
		return events[i].time < events[j].time
	})
	active, peak := 0, 0
	for _, event := range events {
		active += event.delta
		if active < 0 {
			t.Fatal("invalid monotonic execution interval ordering")
		}
		peak = max(peak, active)
	}
	if len(seen) != 16 || active != 0 || peak <= 1 || peak > awGraphConcurrency {
		t.Fatalf("actual script concurrency peak=%d, want >1 and <=%d across both containers; items=%d active=%d", peak, awGraphConcurrency, len(seen), active)
	}
	model.mu.Lock()
	calls := len(model.began)
	model.mu.Unlock()
	if calls != 0 {
		t.Fatal("code batch unexpectedly invoked a language model")
	}
	t.Logf("16 isolated Python item intervals: actual peak=%d, shared limit=%d; namespaced aggregate count=%d total=%d", peak, awGraphConcurrency, combined.Count, combined.Total)
}
