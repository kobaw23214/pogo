package web

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/llm"
)

// Scripted authoring exercises actual conversation tools and host permissions;
// only language-model decisions are offline. Python/JS execute for real.
type awCodeAuthorClient struct{ phase int }

func (*awCodeAuthorClient) Model() string { return "k3" }
func awCodeAcceptanceDefinition() af.Definition {
	return af.Definition{Name: "订单计算与确认", Inputs: []af.Input{{Key: "orders", Label: "订单列表", Type: "array", Required: true, Default: `[{"amount":15},{"amount":25}]`}, {Key: "factor", Label: "计算系数", Type: "number", Default: "2", Required: true}}, Steps: []af.Step{
		awCodeStep("sum", "javascript", "function main(inputs) { console.log('正在汇总订单'); return {amount: inputs.orders.reduce((n, row) => n + row.amount, 0), count: inputs.orders.length}; }", map[string]af.CodeBinding{"orders": {Type: "array", Source: "inputs.orders"}}, map[string]af.ValueSchema{"amount": {Type: "number"}, "count": {Type: "integer"}}),
		awCodeStep("calculate", "python", "def main(inputs):\n    print('正在计算金额')\n    return {'total': inputs['amount'] * inputs['factor']}\n", map[string]af.CodeBinding{"amount": {Type: "number", Source: "steps.sum.output.amount"}, "factor": {Type: "number", Source: "inputs.factor"}}, map[string]af.ValueSchema{"total": {Type: "number"}}),
		{ID: "review", Kind: "confirm", Title: "本人确认金额", Prompt: "请确认计算金额 {{ steps.calculate.output.total }}"},
		awCodeStep("finish", "javascript", "function main(inputs) { console.log('确认后生成结果'); return {total: inputs.total, confirmed: true}; }", map[string]af.CodeBinding{"total": {Type: "number", Source: "steps.calculate.output.total"}}, map[string]af.ValueSchema{"total": {Type: "number"}, "confirmed": {Type: "boolean"}}),
	}}
}

func (c *awCodeAuthorClient) Stream(_ context.Context, _ string, history []llm.Message, _ []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
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
		d := awCodeAcceptanceDefinition()
		return call("agent_workflow_edit", map[string]any{"action": "create", "request_id": "code-chat-create", "name": d.Name, "inputs": d.Inputs, "steps": d.Steps})
	}
	all := ""
	for _, message := range history {
		for _, block := range message.Content {
			all += block.Text + "\n"
		}
	}
	idMatch := regexp.MustCompile(`"id"\s*:\s*"(aw_[A-Za-z0-9_-]+)"`).FindStringSubmatch(all)
	hashMatches := regexp.MustCompile(`"definition_hash"\s*:\s*"([a-f0-9]{64})"`).FindAllStringSubmatch(all, -1)
	var hashMatch []string
	if len(hashMatches) > 0 {
		hashMatch = hashMatches[len(hashMatches)-1]
	}
	if len(idMatch) != 2 || len(hashMatch) != 2 {
		return llm.Response{}, errors.New("creation returned no actual workflow identity/hash")
	}
	if c.phase == 4 {
		return call("agent_workflow_read", map[string]any{"action": "validate", "workflow_id": idMatch[1], "expected_revision": 1})
	}
	if c.phase == 5 {
		return call("agent_workflow_run", map[string]any{"action": "test_node", "request_id": "code-chat-node-test", "workflow_id": idMatch[1], "step_id": "calculate", "expected_revision": 1, "expected_hash": hashMatch[1], "node_inputs": map[string]any{"amount": 12, "factor": 3}})
	}
	if c.phase == 7 {
		return call("agent_workflow_read", map[string]any{"action": "get", "workflow_id": idMatch[1]})
	}
	if c.phase == 8 {
		code := awCodeAcceptanceDefinition().Steps[1].Code
		code.Source = "def main(inputs):\n    print('正在计算金额：已增加1')\n    return {'total': inputs['amount'] * inputs['factor'] + 1}\n"
		return call("agent_workflow_edit", map[string]any{"action": "patch", "request_id": "code-chat-modify", "workflow_id": idMatch[1], "expected_revision": 1, "operations": []map[string]any{{"op": "update_step", "step_id": "calculate", "patch": map[string]any{"code": code}}}})
	}
	if c.phase == 9 {
		return call("agent_workflow_read", map[string]any{"action": "validate", "workflow_id": idMatch[1], "expected_revision": 2})
	}
	if c.phase == 10 {
		return call("agent_workflow_run", map[string]any{"action": "test_node", "request_id": "code-chat-modified-test", "workflow_id": idMatch[1], "step_id": "calculate", "expected_revision": 2, "expected_hash": hashMatch[1], "node_inputs": map[string]any{"amount": 12, "factor": 3}})
	}
	if c.phase >= 11 {
		return textTurn("已局部修改 calculate 代码：乘法结果增加1，保存为草稿 r2，并发起该版本的独立单节点试跑。画布与代码配置已同步更新；未启用流程。"), nil
	}
	if strings.Contains(all, "error") && !strings.Contains(all, `"ok":true`) {
		return llm.Response{}, errors.New("authoring tool failed")
	}
	return textTurn("已通过工具创建《订单计算与确认》草稿，并发起 calculate 的独立单节点试跑；其余步骤不会执行。代码使用真实 Python／JavaScript 运行。可在画布查看实际结果、修改节点并试跑整个草稿；完整流程会在本人确认处暂停。尚未启用该流程。"), nil
}
