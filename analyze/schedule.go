package analyze

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/tools"
)

// The agent loop does not record how it scheduled a batch, so this file rebuilds
// that decision from what a transcript does hold: each call's tool name and
// arguments, and the execution time its tool_result recorded. The rule is
// agent.segments, the per-call test agent.mayOverlap, and both are applied to
// the tools' own declarations rather than to a list kept here.

// callMode is how the loop scheduled one call, as far as a transcript can tell.
type callMode int

const (
	modeParallel callMode = iota
	modeSequential
	// modeUnknown is a call whose schedule depended on something the transcript
	// does not carry: an MCP server's or a Skill manifest's annotations, a tool
	// a host defines outside the tools package, or the mode of a retained
	// subagent. Such calls are scheduled as sequential here, which is what the
	// loop does with any call it cannot prove may overlap, and they are counted
	// so the report says how much of its answer rests on that assumption.
	modeUnknown
)

// scheduledCall is one tool_use with what the report needs to know about it.
type scheduledCall struct {
	name string
	mode callMode
	// readOnly marks a call scheduled alone although the tool's own
	// declarations say this call changes nothing.
	readOnly bool
	// durationMS is the execution time its tool_result recorded. The field is
	// omitted at zero on disk, so a sub-millisecond call and one that was never
	// timed read the same.
	durationMS int64
}

// ScheduleSummary is how a set of batches was, or would have been, dispatched.
type ScheduleSummary struct {
	// DispatchSteps counts the loop's segments: every call that runs alone is
	// one step, and so is every run of calls that overlapped.
	DispatchSteps int `json:"dispatch_steps"`
	// Overlapped counts calls that ran alongside at least one sibling.
	Overlapped int `json:"overlapped"`
	// Stalled counts overlap-capable calls that ran alone although their batch
	// held another overlap-capable call: a call that could not overlap stood
	// between them. They ran one at a time for a reason that had nothing to do
	// with them, and zero is a real answer — the rule cost this workload nothing.
	Stalled int `json:"stalled_by_sequential_sibling"`
	// EstimatedWallMS replays the recorded durations through the segments, with
	// at most agent.DefaultToolConcurrency calls in flight. An estimate: a
	// duration measured while overlapping includes whatever contention there was.
	EstimatedWallMS int64 `json:"estimated_wall_ms,omitempty"`
}

func (s *ScheduleSummary) add(o ScheduleSummary) {
	s.DispatchSteps += o.DispatchSteps
	s.Overlapped += o.Overlapped
	s.Stalled += o.Stalled
	s.EstimatedWallMS += o.EstimatedWallMS
}

// nativeTools holds a zero-value instance of every tool the tools package
// defines, keyed by name. Schedules are read from these instances, so a tool
// changing its mind cannot leave the report quietly wrong, and a tool missing
// from the set fails TestEveryNativeToolHasASchedule.
//
// The subagent instance carries an empty retained-context store because web
// sessions have one, and the store is what makes the scheduler accept the
// action and agent_id arguments at all. A terminal session has no store, and
// its schema never offers those arguments either.
var nativeTools = func() map[string]tools.Tool {
	list := []tools.Tool{
		&tools.Read{}, &tools.Ls{}, &tools.Find{}, &tools.Grep{}, &tools.ToolResultRead{},
		&tools.Write{}, &tools.Edit{}, &tools.Bash{},
		&tools.ShellTasks{}, &tools.ShellOutput{}, &tools.ShellStop{},
		&tools.Information{},
		&tools.ShortcutQuery{}, &tools.ShortcutManage{}, &tools.ShortcutDelete{}, &tools.ShortcutMerge{},
		&tools.ShortcutSearch{}, &tools.ShortcutRead{}, &tools.ShortcutCreate{}, &tools.ShortcutList{},
		&tools.ShortcutFolderCreate{}, &tools.ShortcutUpdate{}, &tools.ShortcutFolderRename{}, &tools.ShortcutMove{},
		&tools.SkillSearch{}, &tools.RuntimeSkillRead{}, &tools.SkillSave{},
		&tools.BackgroundTask{}, &tools.Watch{}, &tools.WatchTest{}, &tools.WatchDecide{},
		&tools.Browser{}, &tools.Office{}, &tools.ImageAnalyze{}, &tools.MCPDescribe{},
		&tools.RequestUserInput{}, &tools.RequestUserInputAsync{},
		&tools.Todo{}, &tools.SubagentTasks{},
		&tools.Subagent{Store: &tools.SubagentStore{}},
	}
	list = append(list, tools.NewAgentWorkflowTools(inertWorkflowBackend{})...)
	list = append(list, tools.NewWorkflowTools(inertWorkflowBackend{})...)
	byName := make(map[string]tools.Tool, len(list))
	for _, t := range list {
		byName[t.Name()] = t
	}
	return byName
}()

// inertWorkflowBackend lets the workflow constructors build their tools so that
// their names and schedules can be read. Nothing here executes a tool.
type inertWorkflowBackend struct{}

func (inertWorkflowBackend) AgentWorkflowCall(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("analyze: workflow tools are not executable here")
}

func (inertWorkflowBackend) WorkflowCall(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("analyze: workflow tools are not executable here")
}

// argReadOnly holds the argument-level read-only tests of tools the loop runs
// alone whatever the arguments: the same predicates the approval policy relies
// on to pass these calls unreviewed (web/policy.go, reviewsLocked). watch is
// not here because its reads already overlap (Watch.ExecutionModeForArgs).
var argReadOnly = map[string]func(json.RawMessage) bool{
	tools.OfficeToolName:     tools.OfficeReadOnly,
	tools.BackgroundTaskName: tools.BackgroundTaskReadOnly,
	tools.ShortcutMergeName:  tools.ShortcutMergeReadOnly,
}

// classify rebuilds agent.mayOverlap for one call from the tool's declarations
// and the recorded arguments, and says whether a call scheduled alone declared
// itself read-only.
func classify(name string, input json.RawMessage) (mode callMode, readOnly bool) {
	t, ok := nativeTools[name]
	if !ok || (name == "subagent" && subagentContinue(input)) {
		return modeUnknown, false
	}
	m := t.ExecutionMode()
	if dynamic, ok := t.(tools.ArgumentExecutionMode); ok {
		m = dynamic.ExecutionModeForArgs(input)
	}
	if m == tools.Parallel {
		return modeParallel, false
	}
	if ro, ok := t.(tools.ReadOnlyCapable); ok && ro.IsReadOnly() {
		return modeSequential, true
	}
	if test, ok := argReadOnly[name]; ok && test(input) {
		return modeSequential, true
	}
	return modeSequential, false
}

// subagentContinue reports a follow-up to a retained child. The scheduler lets
// it overlap only when that child was created read-only, which is recorded in
// host state rather than in the call, so the call is reported as unknown rather
// than guessed. It also keeps the empty store above from ever being asked to
// read a record.
func subagentContinue(input json.RawMessage) bool {
	var a struct {
		Action string `json:"action"`
	}
	return json.Unmarshal(input, &a) == nil && a.Action == "continue"
}

// overlapsNow is the loop's rule. An unknown call cannot overlap; see modeUnknown.
func overlapsNow(c scheduledCall) bool { return c.mode == modeParallel }

// overlapsIfReadOnlyParallel also lets read-only calls that ran alone overlap:
// the counterfactual that prices argument-aware scheduling for them.
func overlapsIfReadOnlyParallel(c scheduledCall) bool {
	return c.mode == modeParallel || c.readOnly
}

// schedule dispatches one batch the way agent.segments does: consecutive
// overlap-capable calls share a segment, every other call has one to itself
// and acts as a barrier, and a lone overlap-capable call runs alone.
func schedule(batch []scheduledCall, overlaps func(scheduledCall) bool) ScheduleSummary {
	var s ScheduleSummary
	capable := 0
	for _, c := range batch {
		if overlaps(c) {
			capable++
		}
	}
	for i := 0; i < len(batch); {
		j := i + 1
		if overlaps(batch[i]) {
			for j < len(batch) && overlaps(batch[j]) {
				j++
			}
		}
		segment := batch[i:j]
		s.DispatchSteps++
		switch {
		case len(segment) > 1:
			s.Overlapped += len(segment)
		case overlaps(segment[0]) && capable > 1:
			s.Stalled++
		}
		s.EstimatedWallMS += segmentWall(segment)
		i = j
	}
	return s
}

// segmentWall replays a segment's recorded durations with at most
// agent.DefaultToolConcurrency calls in flight, each next call taking the
// earliest free slot.
func segmentWall(segment []scheduledCall) int64 {
	slots := make([]int64, max(1, min(len(segment), agent.DefaultToolConcurrency)))
	var end int64
	for _, c := range segment {
		next := 0
		for i := range slots {
			if slots[i] < slots[next] {
				next = i
			}
		}
		slots[next] += c.durationMS
		end = max(end, slots[next])
	}
	return end
}

// splitters names the calls in a batch that kept overlap-capable siblings
// apart: calls that could not overlap, with an overlap-capable call somewhere
// before them and somewhere after them.
func splitters(batch []scheduledCall, overlaps func(scheduledCall) bool) []string {
	after := 0
	for _, c := range batch {
		if overlaps(c) {
			after++
		}
	}
	before := 0
	var names []string
	for _, c := range batch {
		if overlaps(c) {
			before++
			after--
			continue
		}
		if before > 0 && after > 0 {
			names = append(names, c.name)
		}
	}
	return names
}

// addBatch counts one assistant message's tool calls. results joins each call to
// its tool_result by tool_use_id, for the recorded duration.
func (b *BatchDistribution) addBatch(calls []blockRecord, results map[string]blockRecord) {
	count := len(calls)
	if count == 0 {
		return
	}
	if b.BySize == nil {
		b.BySize = make(map[int]int)
	}
	if b.Combos == nil {
		b.Combos = make(map[string]int)
	}
	names := make([]string, count)
	batch := make([]scheduledCall, count)
	for i, c := range calls {
		names[i] = c.Name
		mode, readOnly := classify(c.Name, c.Input)
		batch[i] = scheduledCall{name: c.Name, mode: mode, readOnly: readOnly}
		if r, ok := results[c.ID]; ok && c.ID != "" {
			batch[i].durationMS = max(r.DurationMS, 0)
		}
		b.ToolTimeMS += batch[i].durationMS
		if mode == modeUnknown {
			increment(&b.UnknownMode, c.Name)
		}
		if readOnly {
			increment(&b.ReadOnlySequential, c.Name)
		}
	}

	b.TotalCalls += count
	b.BySize[count]++
	b.Combos[comboKey(names)]++
	switch count {
	case 1:
		b.Singles++
	case 2:
		b.Doubles++
	default:
		b.Multiple++
	}

	b.ScheduleSummary.add(schedule(batch, overlapsNow))
	for _, name := range splitters(batch, overlapsNow) {
		increment(&b.SplitBy, name)
	}
	// Accumulated for every batch, so the counterfactual covers the same history
	// as the current numbers; finish drops it when no call would change.
	if b.IfReadOnlyParallel == nil {
		b.IfReadOnlyParallel = &ScheduleSummary{}
	}
	b.IfReadOnlyParallel.add(schedule(batch, overlapsIfReadOnlyParallel))
}

// finish derives the per-turn average once every batch has been added.
func (b *BatchDistribution) finish() {
	turns := 0
	for _, n := range b.BySize {
		turns += n
	}
	if turns > 0 {
		b.AverageCalls = float64(b.TotalCalls) / float64(turns)
	}
	if len(b.ReadOnlySequential) == 0 {
		b.IfReadOnlyParallel = nil
	}
}

// writeScheduling renders the scheduling half of the batch report.
func writeScheduling(b *strings.Builder, d BatchDistribution) {
	if d.TotalCalls == 0 {
		return
	}
	b.WriteString("  Scheduling (the agent loop's segment rule, rebuilt from tool declarations):\n")
	fmt.Fprintf(b, "    Dispatch steps: %d for %d calls (calls sharing a step ran concurrently)\n",
		d.DispatchSteps, d.TotalCalls)
	fmt.Fprintf(b, "    Overlapped with a sibling: %d call(s)\n", d.Overlapped)
	fmt.Fprintf(b, "    Serialized by a sequential sibling: %d call(s)\n", d.Stalled)
	if len(d.SplitBy) > 0 {
		fmt.Fprintf(b, "    Split by: %s\n", rankedCounts(d.SplitBy))
	}
	cf := d.IfReadOnlyParallel
	if len(d.ReadOnlySequential) > 0 {
		fmt.Fprintf(b, "    Read-only calls scheduled alone: %s\n", rankedCounts(d.ReadOnlySequential))
		if cf != nil {
			fmt.Fprintf(b, "    If those could overlap: %d steps, %d overlapped, %d serialized\n",
				cf.DispatchSteps, cf.Overlapped, cf.Stalled)
		}
	}
	if len(d.UnknownMode) > 0 {
		fmt.Fprintf(b, "    Schedule not recorded, counted as sequential: %s\n", rankedCounts(d.UnknownMode))
	}
	if d.ToolTimeMS > 0 {
		line := fmt.Sprintf("    Tool time: %s summed over calls, about %s wall under this rule",
			formatDuration(d.ToolTimeMS), formatDuration(d.EstimatedWallMS))
		if cf != nil {
			line += fmt.Sprintf(", about %s if read-only calls could overlap", formatDuration(cf.EstimatedWallMS))
		}
		b.WriteString(line + "\n")
	}
}

// rankedCounts renders "bash 3, docs_office 1": largest first, ties by name.
func rankedCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, m[k])
	}
	return strings.Join(parts, ", ")
}

func increment(m *map[string]int, key string) {
	if *m == nil {
		*m = make(map[string]int)
	}
	(*m)[key]++
}
