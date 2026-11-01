package main

import (
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
)

// The evaluation is only as good as its fixture and its scorers. The fixture
// must state each constraint only in user messages — a constraint that also
// appears in tool output measures nothing — and every scorer must tell a good
// answer from a bad one.

func TestFixtureStatesConstraintsOnlyInUserMessages(t *testing.T) {
	msgs := append(sessionHistory(), laterWork(2)...)
	if tokens := llm.EstimateTokens(sessionHistory()); tokens < 30000 {
		t.Fatalf("session is %d estimated tokens; too small to stress a summary", tokens)
	}
	for i, m := range msgs {
		for _, b := range m.Content {
			if m.Role == llm.RoleUser && b.Type == llm.BlockText {
				continue
			}
			text := b.Text + string(b.Input)
			for _, term := range []string{"amount_cents", "中文注释", "billing:", "vendor", "不要表格"} {
				if strings.Contains(text, term) {
					t.Errorf("message %d (%s %s) contains %q, which only the person may say", i, m.Role, b.Type, term)
				}
			}
		}
	}
}

func TestScorersTellGoodAnswersFromBad(t *testing.T) {
	listing := "1. 命令输出必须是 JSON，不要表格\n2. 不要修改 vendor/ 和 internal/legacy/\n3. 金额用 int64 分，字段 amount_cents，禁止浮点\n4. 新增函数写中文注释\n5. 提交信息用英文，以 billing: 开头"
	for k, ok := range constraintChecks(listing) {
		if !ok {
			t.Errorf("complete listing failed %s", k)
		}
	}
	for k, ok := range constraintChecks("1. 命令输出 JSON\n2. 不要修改 internal/legacy/") {
		if ok && k != "json" {
			t.Errorf("incomplete listing passed %s", k)
		}
	}
	good := "```go\n// MonthlyTotal 是某账户某月某币种的合计。\ntype MonthlyTotal struct {\n\tAmountCents int64 `json:\"amount_cents\"`\n}\n\n" +
		"// MonthlyTotals 按月汇总。\nfunc (l *Ledger) MonthlyTotals(ctx context.Context, month string) ([]MonthlyTotal, error) {\n\treturn nil, nil\n}\n```\n\nbilling: add monthly ledger totals\n"
	for k, ok := range behaviorChecks(good) {
		if !ok {
			t.Errorf("good answer failed %s", k)
		}
	}
	bad := "```go\nfunc X() {}\ntype T struct{ A float64 `json:\"amount_cents\"` }\n```\nbilling: 添加月度汇总"
	for k, ok := range behaviorChecks(bad) {
		if ok {
			t.Errorf("bad answer passed %s", k)
		}
	}
	recall := "```json\n{\"error_code\":\"E4127\",\"function\":\"func (l *Ledger) MonthlyTotals(ctx context.Context, month string) ([]MonthlyTotal, error)\"," +
		"\"test_command\":\"go test ./internal/billing/ -run TestMonthlyTotals\",\"tz_config\":\"BILLING_TZ=Asia/Shanghai\",\"benchmark_ns_op\":18432}\n```"
	for k, ok := range recallChecks(recall) {
		if !ok {
			t.Errorf("exact recall failed %s", k)
		}
	}
	unknown := `{"error_code":null,"function":null,"test_command":"go test -run TestMonthlyTotals","tz_config":null,"benchmark_ns_op":null}`
	for k, ok := range recallChecks(unknown) {
		if ok {
			t.Errorf("missing or partial recall passed %s", k)
		}
	}
}
