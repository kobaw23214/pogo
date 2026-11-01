package web

// Opt-in real-model acceptance. The only nonlocal request is to an explicitly
// supplied configured model endpoint; all accounts, workflows and Runs use
// temporary stores. Normal tests skip this case and need no credentials.
import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
)

func TestWorkflowAuthoringRealModelLocal(t *testing.T) {
	t.Skip("retired Agent-to-platform authoring acceptance; platform workflows are managed through HTTP/UI and Agent conversations use agent_workflow_* tools")
	model, endpoint, key := os.Getenv("POGO_WORKFLOW_LIVE_MODEL"), os.Getenv("POGO_WORKFLOW_LIVE_URL"), os.Getenv("POGO_WORKFLOW_LIVE_KEY")
	if model == "" || endpoint == "" || key == "" {
		t.Skip("opt-in configured real-model authoring acceptance")
	}
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return textTurn("unused"), nil }, func(c *Config) {
		c.MaxTurns = 24
		c.RunTimeout = 12 * time.Minute
		c.NewClient = func(_ config.Resolved, retry func(llm.RetryInfo)) llm.Client {
			return llm.New(llm.Options{Model: model, BaseURL: endpoint, APIKey: key, MaxTokens: 4096, Timeout: 2 * time.Minute, MaxRetries: 1, OnRetry: retry})
		}
	})
	sess := authoringConversation(t, h, h.token)
	sess.SetPolicy(ModeAuto, 0)
	prompt := `这是一次使用独立临时数据的自然语言搭建验收。请实际调用工作流专用工具完成以下需求，不要只给建议：
创建名称为“独立采购审批验收”的新工作流，仅含一个人工节点review，不要引用任何Agent执行节点。输入参数order_id，节点提示引用{{ inputs.order_id }}，固定bob审批，禁止发起人自批。先发现可用资源并确认bob存在；用workflow_edit创建草稿；再用一次局部update_node把提示补充为“请核对订单资料并审核 {{ inputs.order_id }}”，保留原审批策略。然后用order_id=LOCAL-REAL-MODEL-001做结构化校验和真实草稿试跑（会停在人工审批，不能等待它完成）。你已获本次隔离验收发布授权，请精确发布刚刚校验过的草稿，再用order_id=LOCAL-REAL-MODEL-002启动一次已发布运行。最后用workflow_read查询记录并说明它们正在等待bob审批，不要审批、取消、修改权限或调用shell/文件工具。创建、局部修改、试跑和运行各自使用固定request_id，网络重试复用原键。每次修改后用新revision/hash；校验与发布均调用工具。`
	if _, err := sess.Start(prompt, nil, ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(11 * time.Minute)
	for sess.Active() && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if sess.Active() {
		sess.Cancel()
		t.Fatal("real-model workflow authoring timed out")
	}
	snapshot := sess.hub.Snapshot()
	used := map[string]int{}
	for _, r := range snapshot.Results {
		if strings.HasPrefix(r.Name, "workflow_") {
			used[r.Name]++
		}
		if r.IsError {
			t.Logf("tool feedback %s: %s", r.Name, r.Text)
		}
	}
	for _, name := range []string{"workflow_discover", "workflow_read", "workflow_validate", "workflow_edit", "workflow_test", "workflow_publish", "workflow_run"} {
		if used[name] == 0 {
			t.Fatalf("real model did not complete required tool %s; used=%v", name, used)
		}
	}
	workflows := h.mgr.workflows.list(true)
	if len(workflows) != 1 {
		t.Fatalf("unexpected workflow count %d", len(workflows))
	}
	wf := workflows[0]
	if wf.Revision < 2 || len(wf.Nodes) != 1 || wf.Nodes[0].Kind != "human" || wf.Nodes[0].ApprovalPolicy == nil || !wf.Nodes[0].ApprovalPolicy.PreventSelfApproval {
		t.Fatalf("real model lost graph/approval requirements")
	}
	_, _, hash, _ := h.mgr.workflows.canonicalDraft(wf)
	p, ok := h.mgr.workflows.pointer(wf.ID)
	if !ok || p.VersionID != hash {
		t.Fatal("real model did not precisely publish reviewed draft")
	}
	runs := h.mgr.wfruns.list(wf.ID)
	if len(runs) != 2 {
		t.Fatalf("expected exactly test + published Run, got %d", len(runs))
	}
	trial := 0
	for _, r := range runs {
		if r.TestRun {
			trial++
		}
		if r.Inputs["order_id"] == "" {
			t.Fatal("real model omitted inputs")
		}
	}
	if trial != 1 {
		t.Fatal("real model did not use the independent trial path")
	}
	t.Logf("real-model authoring accepted: model=%s workflow_revision=%d runs=%d tool_calls=%v", model, wf.Revision, len(runs), used)
}
