package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
)

func awIterationDefinition(concurrency int, continueAfterError bool) af.Definition {
	body := awCodeStep("body", "python", "def main(inputs):\n    print('item', inputs['index'])\n    if inputs['value'] < 0:\n        raise ValueError('synthetic negative item')\n    return {'value': inputs['value'] * 2}\n", map[string]af.CodeBinding{"value": {Type: "number", Source: "iteration.item"}, "index": {Type: "integer", Source: "iteration.index"}}, map[string]af.ValueSchema{"value": {Type: "number"}})
	onError := "stop"
	if continueAfterError {
		onError = "continue"
	}
	return af.Definition{Name: "独立数组处理", Inputs: []af.Input{{Key: "values", Type: "array", Required: true}}, Steps: []af.Step{{ID: "each", Kind: "iteration", Iteration: &af.IterationSpec{Items: af.CodeBinding{Type: "array", Source: "inputs.values"}, Body: &body, Concurrency: concurrency, MaxItems: 10, OnItemError: onError}}}}
}

func TestAgentWorkflowRepeatCodeOrderedResultsAndContinueFailures(t *testing.T) {
	awRequireCodeRuntimes(t)
	h, _ := awGraphModelHarness(t, "", false)
	a := awTestAgent(t, h, "批量代码验收")
	source := h.studioConversationAs(aliceToken, a.ID)
	d := awCreateCodeDefinition(t, h, a, awIterationDefinition(3, true), "repeat-create")
	accepted := awStartCodeTrial(t, h, a, d, source, map[string]any{"values": []int{3, -1, 7}}, "repeat-start")
	done := awTestRun(t, h, aliceToken, a.ID, d.ID, accepted.ID, af.Completed)
	item := done.Steps[0]
	if item.Repeat == nil || item.Repeat.Total != 3 || len(item.Repeat.Items) != 3 {
		t.Fatal("item receipts missing", item)
	}
	if item.Repeat.Items[0].Status != af.Completed || item.Repeat.Items[1].Status != af.Failed || item.Repeat.Items[2].Status != af.Completed {
		t.Fatal("continue failure policy lost item outcomes", item.Repeat.Items)
	}
	for i, entry := range item.Repeat.Items {
		if entry.Index != i || entry.CodeExecution == nil || len(entry.CodeInputs) == 0 || entry.StartedAt == 0 || entry.FinishedAt < entry.StartedAt {
			t.Fatal("invalid item receipt", entry)
		}
	}
	var output struct {
		Items  []json.RawMessage `json:"items"`
		Errors []struct {
			Index int    `json:"index"`
			Error string `json:"error"`
		} `json:"errors"`
		Completed int `json:"completed"`
		Failed    int `json:"failed"`
	}
	if json.Unmarshal(item.Output, &output) != nil || output.Completed != 2 || output.Failed != 1 || len(output.Errors) != 1 || output.Errors[0].Index != 1 || string(output.Items[1]) != "null" {
		t.Fatal("ordered aggregate invalid", string(item.Output))
	}
	var first, last map[string]int
	_ = json.Unmarshal(output.Items[0], &first)
	_ = json.Unmarshal(output.Items[2], &last)
	if first["value"] != 6 || last["value"] != 14 {
		t.Fatal("parallel result order changed", string(item.Output))
	}
	if done.ExecutionSessionID != "" {
		t.Fatal("code iteration created a model session")
	}
}

func TestAgentWorkflowRepeatCodeStopsAndRetainsFinishedItems(t *testing.T) {
	awRequireCodeRuntimes(t)
	h, _ := awGraphModelHarness(t, "", false)
	a := awTestAgent(t, h, "失败停止验收")
	source := h.studioConversationAs(aliceToken, a.ID)
	d := awCreateCodeDefinition(t, h, a, awIterationDefinition(1, false), "repeat-stop-create")
	run := awStartCodeTrial(t, h, a, d, source, map[string]any{"values": []int{2, -1, 9}}, "repeat-stop-start")
	failed := awGraphWaitTerminal(t, h, a, d, run.ID, af.Failed)
	items := failed.Steps[0].Repeat.Items
	if items[0].Status != af.Completed || items[1].Status != af.Failed || items[2].Status != af.Skipped || len(items[0].Output) == 0 || len(items[2].CodeInputs) != 0 {
		t.Fatal("stop policy re-ran or discarded items", items)
	}
}

func TestAgentWorkflowRepeatAggregateLimitPreservesActualAttemptAndEarlierResult(t *testing.T) {
	awRequireCodeRuntimes(t)
	h, _ := awGraphModelHarness(t, "", false)
	a := awTestAgent(t, h, "聚合上限验收")
	source := h.studioConversationAs(aliceToken, a.ID)
	body := awCodeStep("body", "python", "def main(inputs):\n    return {'text': 'x' * 140000}\n", map[string]af.CodeBinding{}, map[string]af.ValueSchema{"text": {Type: "string"}})
	d := awCreateCodeDefinition(t, h, a, af.Definition{Name: "聚合结果上限", Steps: []af.Step{{ID: "each", Kind: "iteration", Iteration: &af.IterationSpec{Items: af.CodeBinding{Type: "array", Value: json.RawMessage(`[1,2,3]`)}, Body: &body, Concurrency: 1, OnItemError: "continue"}}}}, "repeat-limit-create")
	run := awStartCodeTrial(t, h, a, d, source, map[string]string{}, "repeat-limit-start")
	failed := awGraphWaitTerminal(t, h, a, d, run.ID, af.Failed)
	items := failed.Steps[0].Repeat.Items
	if items[0].Status != af.Completed || len(items[0].Output) < 140000 || items[1].Status != af.Failed || items[2].Status != af.Skipped {
		t.Fatal("aggregate bound erased previous results or kept launching", failed)
	}
	if len(items[1].Attempts) != 1 || len(items[1].Attempts[0].Output) < 140000 || len(items[1].Output) != 0 {
		t.Fatal("domain rejection rewrote an actual code attempt", items[1])
	}
}

func TestAgentWorkflowRepeatAggregateLimitRejectsFallbackWithoutRewritingFailureAttempts(t *testing.T) {
	awRequireCodeRuntimes(t)
	h, _ := awGraphModelHarness(t, "", false)
	a := awTestAgent(t, h, "备用输出容量验收")
	source := h.studioConversationAs(aliceToken, a.ID)
	body := awCodeStep("body", "python", "def main(inputs):\n    return {'text': 123}\n", map[string]af.CodeBinding{}, map[string]af.ValueSchema{"text": {Type: "string"}})
	fallback, _ := json.Marshal(map[string]string{"text": strings.Repeat("x", 140000)})
	body.Policy = &af.CodePolicy{MaxRetries: 1, OnFailure: "fallback", Fallback: fallback}
	d := awCreateCodeDefinition(t, h, a, af.Definition{Name: "备用输出容量上限", Steps: []af.Step{{ID: "each", Kind: "iteration", Iteration: &af.IterationSpec{Items: af.CodeBinding{Type: "array", Value: json.RawMessage(`[1,2,3]`)}, Body: &body, Concurrency: 1, OnItemError: "continue"}}}}, "repeat-fallback-limit-create")
	run := awStartCodeTrial(t, h, a, d, source, map[string]string{}, "repeat-fallback-limit-start")
	failed := awGraphWaitTerminal(t, h, a, d, run.ID, af.Failed)
	items := failed.Steps[0].Repeat.Items
	if items[0].Status != af.Completed || !items[0].FallbackUsed || len(items[0].Output) < 140000 || items[1].Status != af.Failed || items[1].FallbackUsed || items[2].Status != af.Skipped {
		t.Fatal("rejected fallback was marked consumed or existing output lost", failed)
	}
	if len(items[1].Attempts) != 2 || len(items[1].Output) != 0 {
		t.Fatal("failure attempts missing", items[1])
	}
	for _, attempt := range items[1].Attempts {
		var raw map[string]int
		_ = json.Unmarshal(attempt.Output, &raw)
		if raw["text"] != 123 || attempt.Error == "" || attempt.Execution == nil || attempt.Execution.ErrorCode != "output_schema_invalid" {
			t.Fatal("actual failed execution receipt was rewritten", attempt)
		}
	}
}

func TestAgentWorkflowRepeatCancellationDoesNotStartQueuedItems(t *testing.T) {
	awRequireCodeRuntimes(t)
	h, _ := awGraphModelHarness(t, "", false)
	a := awTestAgent(t, h, "迭代取消验收")
	source := h.studioConversationAs(aliceToken, a.ID)
	body := awCodeStep("body", "javascript", "async function main(inputs) { await new Promise(r => setTimeout(r, 1000)); return {value: inputs.value}; }", map[string]af.CodeBinding{"value": {Type: "number", Source: "iteration.item"}}, map[string]af.ValueSchema{"value": {Type: "number"}})
	d := awCreateCodeDefinition(t, h, a, af.Definition{Name: "取消逐项执行", Steps: []af.Step{{ID: "each", Kind: "iteration", Iteration: &af.IterationSpec{Items: af.CodeBinding{Type: "array", Value: json.RawMessage(`[1,2,3,4,5,6]`)}, Body: &body, Concurrency: 2}}}}, "repeat-cancel-create")
	run := awStartCodeTrial(t, h, a, d, source, map[string]string{}, "repeat-cancel-start")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]string{"action": "run", "workflow_id": d.ID, "run_id": run.ID}, 200), "run")
		if current.Steps[0].Repeat != nil {
			started := 0
			for _, item := range current.Steps[0].Repeat.Items {
				if len(item.Attempts) > 0 {
					started++
				}
			}
			if started == 2 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	awTestCall(t, h, aliceToken, a.ID, "run", map[string]string{"action": "cancel", "request_id": "repeat-cancel", "workflow_id": d.ID, "run_id": run.ID}, 200)
	cancelled := awGraphWaitTerminal(t, h, a, d, run.ID, af.Cancelled)
	started := 0
	for _, item := range cancelled.Steps[0].Repeat.Items {
		if len(item.Attempts) > 0 {
			started++
		}
		if item.Status == af.Running || item.Status == af.Queued {
			t.Fatal("cancelled container retained an apparently live item", item)
		}
	}
	if started > 2 {
		t.Fatal("queued item started after cancellation", started)
	}
}

func awLoopDefinition(max int) af.Definition {
	body := awCodeStep("body", "javascript", "function main(inputs) { console.log('round', inputs.index); return {value: inputs.state.value + 1}; }", map[string]af.CodeBinding{"state": {Type: "object", Source: "loop.state"}, "index": {Type: "integer", Source: "loop.index"}}, map[string]af.ValueSchema{"value": {Type: "integer"}})
	return af.Definition{Name: "有界状态循环", Steps: []af.Step{{ID: "repeat", Kind: "loop", Loop: &af.LoopSpec{Initial: af.CodeBinding{Type: "object", Value: json.RawMessage(`{"value":0}`)}, Body: &body, Until: af.Condition{Source: "loop.state.value", Operator: "gte", Value: "3"}, MaxIterations: max}}}}
}

func TestAgentWorkflowRepeatLoopStateAndExplicitLimitFailure(t *testing.T) {
	awRequireCodeRuntimes(t)
	h, _ := awGraphModelHarness(t, "", false)
	a := awTestAgent(t, h, "循环验收")
	source := h.studioConversationAs(aliceToken, a.ID)
	for _, maximum := range []int{5, 2} {
		d := awCreateCodeDefinition(t, h, a, awLoopDefinition(maximum), fmt.Sprintf("loop-create-%d", maximum))
		run := awStartCodeTrial(t, h, a, d, source, map[string]string{}, fmt.Sprintf("loop-start-%d", maximum))
		status := af.Completed
		if maximum == 2 {
			status = af.Failed
		}
		done := awGraphWaitTerminal(t, h, a, d, run.ID, status)
		receipt := done.Steps[0].Repeat
		if receipt == nil || len(receipt.Items) != min(maximum, 3) || receipt.LimitReached != (maximum == 2) {
			t.Fatal("loop bounds invalid", done)
		}
		for i, item := range receipt.Items {
			var initial, result map[string]int
			_ = json.Unmarshal(item.Input, &initial)
			_ = json.Unmarshal(item.Output, &result)
			if initial["value"] != i || result["value"] != i+1 {
				t.Fatal("loop state changed or lost", receipt)
			}
		}
		var state map[string]int
		_ = json.Unmarshal(receipt.State, &state)
		if state["value"] != min(maximum, 3) {
			t.Fatal("final loop state wrong", receipt)
		}
		if maximum == 2 && len(done.Steps[0].Output) > 0 {
			t.Fatal("loop limit falsely reported success")
		}
	}
}

func TestAgentWorkflowRepeatLoopHistoryCanExceedAggregateLimitWithSmallCurrentState(t *testing.T) {
	awRequireCodeRuntimes(t)
	h, _ := awGraphModelHarness(t, "", false)
	a := awTestAgent(t, h, "多轮状态容量验收")
	source := h.studioConversationAs(aliceToken, a.ID)
	body := awCodeStep("body", "python", "def main(inputs):\n    return {'value': inputs['state']['value'] + 1, 'padding': 'x' * 40000}\n", map[string]af.CodeBinding{"state": {Type: "object", Source: "loop.state"}}, map[string]af.ValueSchema{"value": {Type: "integer"}, "padding": {Type: "string"}})
	d := awCreateCodeDefinition(t, h, a, af.Definition{Name: "多轮历史与当前状态分别限额", Steps: []af.Step{{ID: "repeat", Kind: "loop", Loop: &af.LoopSpec{Initial: af.CodeBinding{Type: "object", Value: json.RawMessage(`{"value":0}`)}, Body: &body, Until: af.Condition{Source: "loop.state.value", Operator: "gte", Value: "7"}, MaxIterations: 8}}}}, "loop-history-size-create")
	run := awStartCodeTrial(t, h, a, d, source, map[string]string{}, "loop-history-size-start")
	done := awTestRun(t, h, aliceToken, a.ID, d.ID, run.ID, af.Completed)
	receipt := done.Steps[0].Repeat
	if receipt == nil || len(receipt.Items) != 7 || receipt.LimitReached || len(receipt.State) >= af.MaxRepeatItemBytes {
		t.Fatal("loop did not retain seven small states", done)
	}
	total := 0
	for _, item := range receipt.Items {
		total += len(item.Output)
		if item.Status != af.Completed {
			t.Fatal("accepted earlier loop state was rejected", item)
		}
	}
	if total <= af.MaxCodeDataBytes {
		t.Fatal("fixture did not cross the iteration aggregate threshold", total)
	}
	var final struct {
		State        struct{ Value int }
		Iterations   int
		LimitReached bool `json:"limit_reached"`
	}
	if json.Unmarshal(done.Steps[0].Output, &final) != nil || final.State.Value != 7 || final.Iterations != 7 || final.LimitReached {
		t.Fatal("loop final state invalid", string(done.Steps[0].Output))
	}
}

func TestAgentWorkflowRepeatLoopRejectsOversizeTerminalStateWithoutAdvancingCursor(t *testing.T) {
	awRequireCodeRuntimes(t)
	h, _ := awGraphModelHarness(t, "", false)
	a := awTestAgent(t, h, "退出轮状态容量验收")
	source := h.studioConversationAs(aliceToken, a.ID)
	body := awCodeStep("body", "python", "def main(inputs):\n    return {'value': 1, 'padding': 'x' * 80000}\n", map[string]af.CodeBinding{}, map[string]af.ValueSchema{"value": {Type: "integer"}, "padding": {Type: "string"}})
	d := awCreateCodeDefinition(t, h, a, af.Definition{Name: "拒绝超大退出轮状态", Steps: []af.Step{{ID: "repeat", Kind: "loop", Loop: &af.LoopSpec{Initial: af.CodeBinding{Type: "object", Value: json.RawMessage(`{"value":0}`)}, Body: &body, Until: af.Condition{Source: "loop.state.value", Operator: "gte", Value: "1"}, MaxIterations: 2}}}}, "loop-terminal-size-create")
	run := awStartCodeTrial(t, h, a, d, source, map[string]string{}, "loop-terminal-size-start")
	failed := awGraphWaitTerminal(t, h, a, d, run.ID, af.Failed)
	receipt := failed.Steps[0].Repeat
	if receipt == nil || len(receipt.Items) != 1 || !strings.Contains(failed.Error, "64KiB") {
		t.Fatal("oversize state did not produce an explicit capacity failure", failed)
	}
	var state map[string]int
	_ = json.Unmarshal(receipt.State, &state)
	item := receipt.Items[0]
	if state["value"] != 0 || item.Status != af.Failed || len(item.Output) != 0 || len(item.Attempts) != 1 || len(item.Attempts[0].Output) < 80000 {
		t.Fatal("rejected state advanced cursor or erased real code result", receipt)
	}
}

type awRepeatAgentModel struct {
	mu    sync.Mutex
	began map[int]int
}
type awRepeatAgentClient struct {
	shared       *awRepeatAgentModel
	phase, index int
}

type awRepeatLongSummaryClient struct{}

func (*awRepeatLongSummaryClient) Model() string { return "k3" }
func (*awRepeatLongSummaryClient) Stream(context.Context, string, []llm.Message, []llm.ToolSchema, func(llm.Delta)) (llm.Response, error) {
	return textTurn(strings.Repeat("oversized-summary-", 600)), nil
}

func TestAgentWorkflowRepeatAgentOversizeSummaryIsExplicitFailure(t *testing.T) {
	h := newMultiuserHarness(t, nil, func(c *Config) {
		c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return &awRepeatLongSummaryClient{} }
	})
	a := awTestAgent(t, h, "摘要边界验收")
	source := h.studioConversationAs(aliceToken, a.ID)
	body := af.Step{Kind: "agent", Task: "处理当前输入 {{ iteration.item }}，返回实际结果。"}
	d := awCreateCodeDefinition(t, h, a, af.Definition{Name: "摘要容量边界", Steps: []af.Step{{ID: "each", Kind: "iteration", Iteration: &af.IterationSpec{Items: af.CodeBinding{Type: "array", Value: json.RawMessage(`["one"]`)}, Body: &body}}}}, "repeat-long-summary-create")
	run := awStartCodeTrial(t, h, a, d, source, map[string]string{}, "repeat-long-summary-start")
	failed := awGraphWaitTerminal(t, h, a, d, run.ID, af.Failed)
	item := failed.Steps[0].Repeat.Items[0]
	if item.Status != af.Failed || !strings.Contains(item.Error, "8KiB") || len(item.Output) > 0 || item.SessionID == "" {
		t.Fatal("truncated model text was falsely accepted as complete output", item)
	}
}

func (*awRepeatAgentClient) Model() string { return "k3" }
func (c *awRepeatAgentClient) Stream(ctx context.Context, _ string, history []llm.Message, schemas []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	c.phase++
	all := ""
	for _, message := range history {
		for _, block := range message.Content {
			all += block.Text + "\n"
		}
	}
	if strings.Contains(all, "的步骤 report（") {
		call := func(name string, args any) (llm.Response, error) {
			raw, _ := json.Marshal(args)
			r := toolTurn(name, string(raw))
			r.Message.Content[0].ID = fmt.Sprintf("repeat_report_%d", c.phase)
			return r, nil
		}
		switch c.phase {
		case 1:
			return call("read", map[string]string{"path": awRepeatArtifactRelativePath("each", 0, "item.md")})
		case 2:
			if !strings.Contains(all, "item-1 / REPEAT-SOURCE-934") {
				return llm.Response{}, fmt.Errorf("first real item artifact unavailable downstream")
			}
			return call("read", map[string]string{"path": awRepeatArtifactRelativePath("each", 1, "item.md")})
		case 3:
			if !strings.Contains(all, "item-2 / REPEAT-SOURCE-934") {
				return llm.Response{}, fmt.Errorf("second real item artifact unavailable downstream")
			}
			return call("write", map[string]string{"path": "report.md", "content": "Both real per-item artifacts read and consolidated."})
		default:
			return textTurn("已读取两项真实文件并生成report.md"), nil
		}
	}
	match := regexp.MustCompile(`本次集合的第 ([0-9]+) 项`).FindStringSubmatch(all)
	if len(match) != 2 {
		return llm.Response{}, fmt.Errorf("missing isolated iteration identity")
	}
	_, _ = fmt.Sscanf(match[1], "%d", &c.index)
	for _, schema := range schemas {
		if strings.HasPrefix(schema.Name, "agent_workflow_") || strings.HasPrefix(schema.Name, "workflow_") || schema.Name == "background_task" || schema.Name == "subagent" {
			return llm.Response{}, fmt.Errorf("recursive capability exposed")
		}
	}
	call := func(name string, args any) (llm.Response, error) {
		raw, _ := json.Marshal(args)
		r := toolTurn(name, string(raw))
		r.Message.Content[0].ID = fmt.Sprintf("repeat_%d_%d", c.index, c.phase)
		return r, nil
	}
	switch c.phase {
	case 1:
		c.shared.mu.Lock()
		c.shared.began[c.index]++
		c.shared.mu.Unlock()
		return call("read", map[string]string{"path": "inputs/01-source.md"})
	case 2:
		if !strings.Contains(all, "REPEAT-SOURCE-934") {
			return llm.Response{}, fmt.Errorf("actual input file was not read")
		}
		return call("write", map[string]string{"path": "item.md", "content": fmt.Sprintf("item-%d / REPEAT-SOURCE-934", c.index)})
	case 3:
		return call("bash", map[string]string{"command": fmt.Sprintf("printf 'approval-check-repeat-%d'", c.index)})
	case 4:
		if !strings.Contains(all, fmt.Sprintf("approval-check-repeat-%d", c.index)) {
			return llm.Response{}, fmt.Errorf("actual gated tool did not complete")
		}
		return textTurn(fmt.Sprintf("第%d项已实际写入item.md", c.index)), nil
	}
	return llm.Response{}, fmt.Errorf("unexpected repeat Agent round %d: %v", c.phase, ctx.Err())
}

func TestAgentWorkflowRepeatAgentItemsKeepSessionsApprovalsAndArtifactsSeparate(t *testing.T) {
	shared := &awRepeatAgentModel{began: map[int]int{}}
	h := newMultiuserHarness(t, nil, func(c *Config) {
		c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return &awRepeatAgentClient{shared: shared} }
	})
	a := awTestAgent(t, h, "逐项Agent验收", "write", "bash")
	source := h.studioConversationAs(aliceToken, a.ID)
	if err := os.WriteFile(filepath.Join(h.session(source).cwd, "source.md"), []byte("REPEAT-SOURCE-934"), 0600); err != nil {
		t.Fatal(err)
	}
	body := af.Step{Kind: "agent", Title: "处理单份资料", Task: "处理当前资料 {{ iteration.item }}，保留真实来源，不读取其他项。", OutputFile: "item.md"}
	d := awCreateCodeDefinition(t, h, a, af.Definition{Name: "资料逐项处理", Steps: []af.Step{{ID: "each", Kind: "iteration", Iteration: &af.IterationSpec{Items: af.CodeBinding{Type: "array", Value: json.RawMessage(`["alpha","beta"]`)}, Body: &body, Concurrency: 2, MaxItems: 2}}, {ID: "review", Kind: "confirm", Prompt: "确认 {{ steps.each.output.completed }} 项处理结果"}, {ID: "report", Kind: "agent", Title: "汇总实际文件", Task: "逐项读取 {{ steps.each.output.items }} 中的产物文件，再生成最终汇总。", OutputFile: "report.md"}}}, "repeat-agent-create")
	run := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "request_id": "repeat-agent-start", "workflow_id": d.ID, "source_session_id": source, "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "inputs": map[string]string{}, "files": []string{"source.md"}}, 200), "run")
	deadline := time.Now().Add(10 * time.Second)
	approved := map[string]bool{}
	var latest af.Run
	for time.Now().Before(deadline) {
		latest = awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]string{"action": "run", "workflow_id": d.ID, "run_id": run.ID}, 200), "run")
		if latest.Status == af.Failed || latest.Status == af.Interrupted {
			t.Fatal("repeat failed", latest.Error)
		}
		if latest.Steps[0].Repeat != nil {
			for _, item := range latest.Steps[0].Repeat.Items {
				if item.SessionID == "" {
					continue
				}
				worker := h.session(item.SessionID)
				for _, gate := range worker.Hub().Snapshot().Live.PendingGates {
					if gate.Tool != "bash" || !strings.Contains(string(gate.Args), "approval-check-repeat-") {
						t.Fatal("unexpected item approval", gate)
					}
					if approved[gate.GateID] {
						continue
					}
					resp := h.doAs(aliceToken, http.MethodPost, "/api/sessions/"+worker.ID+"/control", studioBody(map[string]any{"action": "gate_decide", "gate_id": gate.GateID, "allow": true}))
					raw, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					if resp.StatusCode != 200 {
						t.Fatalf("item approval: %d %s", resp.StatusCode, raw)
					}
					approved[gate.GateID] = true
				}
			}
		}
		if latest.Status == af.WaitingConfirmation {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if latest.Status != af.WaitingConfirmation || len(approved) != 2 || latest.Steps[0].Repeat == nil {
		t.Fatal("independent approvals did not finish", latest)
	}
	items := latest.Steps[0].Repeat.Items
	if items[0].SessionID == "" || items[0].SessionID == items[1].SessionID || items[0].ActiveRunID == items[1].ActiveRunID {
		t.Fatal("item sessions collided", items)
	}
	for i, item := range items {
		if item.Status != af.Completed || item.Artifact != "item.md" || item.ArtifactSHA256 == "" {
			t.Fatal("item artifact lost", item)
		}
		actual, err := h.mgr.awReadArtifact(run.ID, awRepeatArtifactID("each", i))
		if err != nil || !strings.Contains(string(actual), fmt.Sprintf("item-%d", i+1)) {
			t.Fatal("same named artifacts overwrote another item", string(actual), err)
		}
		if !h.mgr.agentWorkflowSessionOwned(item.SessionID, h.mgr.agentWorkflowMarker(item.SessionID)) {
			t.Fatal("iteration session lost logical run ownership")
		}
	}
	awTestCall(t, h, aliceToken, a.ID, "confirm", map[string]any{"workflow_id": d.ID, "run_id": run.ID, "step_id": "review", "request_id": "repeat-agent-confirm", "feedback": "独立核对通过"}, 200)
	done := awTestRun(t, h, aliceToken, a.ID, d.ID, run.ID, af.Completed)
	if done.ID != run.ID || done.Steps[1].Feedback != "独立核对通过" {
		t.Fatal("confirmation replaced logical run")
	}
	if done.Steps[2].Artifact != "report.md" || !strings.Contains(done.Steps[2].Summary, "两项真实文件") {
		t.Fatal("downstream Agent could not read actual item artifacts", done)
	}
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if len(shared.began) != 2 || shared.began[1] != 1 || shared.began[2] != 1 {
		t.Fatal("items were replayed", shared.began)
	}
}
