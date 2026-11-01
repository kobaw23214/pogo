package web

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/llm"
)

func awRepeatAcceptanceDefinition() af.Definition {
	batch := awIterationDefinition(2, true).Steps[0]
	batch.ID, batch.Title = "each", "逐项复核"
	batch.Iteration.Items = af.CodeBinding{Type: "array", Source: "inputs.values"}
	prepare := awCodeStep("prepare", "javascript", "function main(inputs){return {round:0,total:inputs.batch.items.reduce((n,item)=>n+(item?item.value:0),0),failed:inputs.batch.failed};}", map[string]af.CodeBinding{"batch": {Type: "object", Source: "steps.each.output"}}, map[string]af.ValueSchema{"round": {Type: "integer"}, "total": {Type: "number"}, "failed": {Type: "integer"}})
	prepare.Title = "整理计算状态"
	body := awCodeStep("body", "python", "def main(inputs):\n    state = inputs['state']\n    print('第', inputs['index'] + 1, '轮计算')\n    return {'round': state['round'] + 1, 'total': state['total'] + 1, 'failed': state['failed']}\n", map[string]af.CodeBinding{"state": {Type: "object", Source: "loop.state"}, "index": {Type: "integer", Source: "loop.index"}}, map[string]af.ValueSchema{"round": {Type: "integer"}, "total": {Type: "number"}, "failed": {Type: "integer"}})
	loop := af.Step{ID: "refine", Kind: "loop", Title: "有界计算", Loop: &af.LoopSpec{Initial: af.CodeBinding{Type: "object", Source: "steps.prepare.output"}, Body: &body, Until: af.Condition{Source: "loop.state.round", Operator: "gte", Value: "3"}, MaxIterations: 5}}
	fallback := awCodeStep("backup", "python", "def main(inputs):\n    print('验证备用输出路径')\n    raise ValueError('独立测试：模拟处理失败')\n", map[string]af.CodeBinding{}, map[string]af.ValueSchema{"discount": {Type: "number"}})
	fallback.Title = "重试与备用结果"
	fallback.Policy = &af.CodePolicy{MaxRetries: 1, RetryDelayMS: 100, OnFailure: "fallback", Fallback: json.RawMessage(`{"discount":0}`)}
	finish := awCodeStep("finish", "javascript", "function main(inputs){return {total:inputs.state.total-inputs.discount,rounds:inputs.state.round,failed_items:inputs.state.failed};}", map[string]af.CodeBinding{"state": {Type: "object", Source: "steps.refine.output.state"}, "discount": {Type: "number", Source: "steps.backup.output.discount"}}, map[string]af.ValueSchema{"total": {Type: "number"}, "rounds": {Type: "integer"}, "failed_items": {Type: "integer"}})
	finish.Title = "输出复核结果"
	return af.Definition{Name: "批量复核与有界计算", Inputs: []af.Input{{Key: "values", Label: "待复核数值", Type: "array", Required: true, Default: "[20,-5,30]"}}, Steps: []af.Step{batch, prepare, loop, {ID: "review", Kind: "confirm", Title: "本人确认", Prompt: "请核对当前金额 {{ steps.refine.output.state.total }}，确认后继续。"}, fallback, finish}}
}

// Only planning decisions are scripted. Every authoring call and every body
// execution goes through the real authenticated host, queue and runner.
type awRepeatAuthorClient struct{ phase int }

func (*awRepeatAuthorClient) Model() string { return "k3" }
func (c *awRepeatAuthorClient) Stream(_ context.Context, _ string, history []llm.Message, _ []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	c.phase++
	call := func(name string, args any) (llm.Response, error) {
		raw, _ := json.Marshal(args)
		r := toolTurn(name, string(raw))
		r.Message.Content[0].ID = name + string(rune('a'+c.phase))
		return r, nil
	}
	switch c.phase {
	case 1:
		return call("agent_workflow_read", map[string]any{"action": "capabilities"})
	case 2:
		return call("agent_workflow_read", map[string]any{"action": "resources"})
	case 3:
		d := awRepeatAcceptanceDefinition()
		return call("agent_workflow_edit", map[string]any{"action": "create", "request_id": "repeat-chat-create", "name": d.Name, "inputs": d.Inputs, "steps": d.Steps})
	}
	all := ""
	for _, message := range history {
		for _, block := range message.Content {
			all += block.Text + "\n"
		}
	}
	id := regexp.MustCompile(`"id"\s*:\s*"(aw_[A-Za-z0-9_-]+)"`).FindStringSubmatch(all)
	hash := regexp.MustCompile(`"definition_hash"\s*:\s*"([a-f0-9]{64})"`).FindStringSubmatch(all)
	if len(id) != 2 || len(hash) != 2 {
		return llm.Response{}, errors.New("工作流创建未返回真实版本")
	}
	if c.phase == 4 {
		return call("agent_workflow_read", map[string]any{"action": "validate", "workflow_id": id[1], "expected_revision": 1})
	}
	if c.phase == 5 {
		return call("agent_workflow_run", map[string]any{"action": "trial", "request_id": "repeat-chat-trial", "workflow_id": id[1], "expected_revision": 1, "expected_hash": hash[1], "inputs": map[string]any{"values": []int{20, -5, 30}}})
	}
	return textTurn("已通过真实工具创建并校验草稿，发起独立试跑。流程逐项处理数据并保留失败项，随后代码循环3轮，在金额103处等待你本人确认；确认后演示代码重试与明确的备用输出，最后产生结构化结果。未启用流程。"), nil
}
