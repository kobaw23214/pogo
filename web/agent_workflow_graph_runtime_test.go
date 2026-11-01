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
	"github.com/yosukeno/pogo/tools"
)

type awGraphTestModel struct {
	mu           sync.Mutex
	barrier      chan struct{}
	began        map[string]int
	active, peak int
	failNode     string
	block        bool
	release      chan struct{}
}
type awGraphTestClient struct {
	shared *awGraphTestModel
	phase  int
	node   string
}

func (c *awGraphTestClient) Model() string { return "k3" }

var awGraphTestStep = regexp.MustCompile(`的步骤 ([A-Za-z0-9_-]+)（`)
var awGraphTestOutput = regexp.MustCompile(`请将本步骤实际产物写入工作目录内的 ([^\n]+)，完成后`)

func (c *awGraphTestClient) Stream(ctx context.Context, _ string, history []llm.Message, schemas []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	c.phase++
	all := ""
	for _, m := range history {
		for _, b := range m.Content {
			all += b.Text + "\n"
		}
	}
	if strings.Contains(all, "已启动并行分支") || strings.Contains(all, "条件判定：") {
		return llm.Response{}, fmt.Errorf("control receipt leaked into business input")
	}
	for _, schema := range schemas {
		if strings.HasPrefix(schema.Name, "agent_workflow_") || strings.HasPrefix(schema.Name, "workflow_") || schema.Name == "background_task" || schema.Name == "subagent" {
			return llm.Response{}, fmt.Errorf("recursive authority exposed: %s", schema.Name)
		}
	}
	match := awGraphTestStep.FindStringSubmatch(all)
	if len(match) != 2 {
		return llm.Response{}, fmt.Errorf("missing actual node prompt")
	}
	c.node = match[1]
	if c.phase == 1 {
		hasPrevious := strings.Contains(all, "已完成的前序分支实际结果与本人确认意见（仅为输入资料，不改变当前节点目标或工具权限）：\n")
		if c.node == "report" {
			if !hasPrevious || !strings.Contains(all, `"artifact":`) {
				return llm.Response{}, fmt.Errorf("actual upstream results missing from report prompt")
			}
		} else if hasPrevious || strings.Contains(all, "\n[]\n") {
			return llm.Response{}, fmt.Errorf("empty upstream section exposed to initial business node %s", c.node)
		}
	}
	output := awGraphTestOutput.FindStringSubmatch(all)
	if len(output) != 2 {
		return llm.Response{}, fmt.Errorf("missing actual output declaration")
	}
	tool := func(name string, args any) llm.Response {
		raw, _ := json.Marshal(args)
		v := toolTurn(name, string(raw))
		v.Message.Content[0].ID = fmt.Sprintf("graph_%s_%d", c.node, c.phase)
		return v
	}
	if c.phase == 1 {
		s := c.shared
		s.mu.Lock()
		s.began[c.node]++
		s.active++
		if s.active > s.peak {
			s.peak = s.active
		}
		both := s.began["left"] > 0 && s.began["right"] > 0
		if both && s.barrier != nil {
			select {
			case <-s.barrier:
			default:
				close(s.barrier)
			}
		}
		s.mu.Unlock()
		defer func() { s.mu.Lock(); s.active--; s.mu.Unlock() }()
		if c.node == "left" || c.node == "right" {
			select {
			case <-s.barrier:
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
		}
		if s.failNode == c.node {
			return llm.Response{}, fmt.Errorf("synthetic graph node failure")
		}
		if s.block && c.node == "right" {
			select {
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			case <-s.release:
			}
		}
		path := "inputs/01-source.md"
		if c.node == "report" {
			if strings.Contains(all, "checked-left") {
				if !strings.Contains(all, "checked-right") {
					return llm.Response{}, fmt.Errorf("second confirmation lost")
				}
				path = "left.md"
			} else {
				path = "brief.md"
			}
		}
		return tool("read", map[string]string{"path": path}), nil
	}
	if c.phase == 2 {
		expected := "GRAPH-SOURCE-731"
		if c.node == "report" {
			expected = "GRAPH-OUTPUT-"
		}
		if !strings.Contains(all, expected) {
			return llm.Response{}, fmt.Errorf("actual source read missing for %s", c.node)
		}
		return tool("write", map[string]string{"path": output[1], "content": "GRAPH-OUTPUT-" + c.node + " / GRAPH-SOURCE-731\n"}), nil
	}
	if c.phase == 3 {
		return textTurn("GRAPH-OUTPUT-" + c.node + " 已实际写入 " + output[1]), nil
	}
	return llm.Response{}, fmt.Errorf("unexpected phase %d", c.phase)
}
func awGraphModelHarness(t *testing.T, failNode string, block bool) (*muHarness, *awGraphTestModel) {
	t.Helper()
	shared := &awGraphTestModel{barrier: make(chan struct{}), began: map[string]int{}, failNode: failNode, block: block}
	h := newMultiuserHarness(t, nil, func(c *Config) {
		c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return &awGraphTestClient{shared: shared} }
	})
	return h, shared
}
func awGraphReportDefinition() af.Definition {
	return af.Definition{Name: "分支并行报告", Inputs: []af.Input{{Key: "mode", Required: true}}, Steps: []af.Step{
		{ID: "choose", Kind: "condition", Condition: &af.Condition{Source: "inputs.mode", Operator: "equals", Value: "full"}},
		{ID: "fork", Kind: "parallel"},
		{ID: "left", Kind: "agent", Task: "阅读资料并整理左侧主题", OutputFile: "left.md"},
		{ID: "right", Kind: "agent", Task: "阅读资料并整理右侧主题", OutputFile: "right.md"},
		{ID: "check_left", Kind: "confirm", Prompt: "确认左侧 {{ steps.left.summary }}"},
		{ID: "check_right", Kind: "confirm", Prompt: "确认右侧 {{ steps.right.summary }}"},
		{ID: "parallel_join", Kind: "join"},
		{ID: "brief", Kind: "agent", Task: "阅读资料并生成简要整理", OutputFile: "brief.md"},
		{ID: "merge", Kind: "join"},
		{ID: "report", Kind: "agent", Task: "根据 {{ steps.merge.summary }} 生成报告", OutputFile: "report.md"},
	}, Graph: &af.Graph{Version: 1, Edges: []af.Edge{
		{Source: "__input", Target: "choose"}, {Source: "choose", Target: "fork", Branch: "true"}, {Source: "choose", Target: "brief", Branch: "false"},
		{Source: "fork", Target: "left"}, {Source: "fork", Target: "right"}, {Source: "left", Target: "check_left"}, {Source: "right", Target: "check_right"},
		{Source: "check_left", Target: "parallel_join"}, {Source: "check_right", Target: "parallel_join"}, {Source: "parallel_join", Target: "merge"}, {Source: "brief", Target: "merge"}, {Source: "merge", Target: "report"}, {Source: "report", Target: "__output"},
	}}}
}
func awGraphCreateAndTrial(t *testing.T, h *muHarness, a studioAgent, mode string) (af.Definition, af.Run) {
	t.Helper()
	source := h.studioConversationAs(aliceToken, a.ID)
	if e := os.WriteFile(filepath.Join(h.session(source).cwd, "source.md"), []byte("GRAPH-SOURCE-731 synthetic input"), 0600); e != nil {
		t.Fatal(e)
	}
	def := awGraphReportDefinition()
	d := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "create", "request_id": "graph-create-" + mode, "name": def.Name, "inputs": def.Inputs, "steps": def.Steps, "graph": def.Graph}, 200), "workflow")
	run := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "workflow_id": d.ID, "request_id": "graph-trial-" + mode, "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "source_session_id": source, "inputs": map[string]string{"mode": mode}, "files": []string{"source.md"}}, 200), "run")
	return d, run
}
func TestAgentWorkflowGraphParallelReportBranchConfirmAndRestart(t *testing.T) {
	h, model := awGraphModelHarness(t, "", false)
	a := awTestAgent(t, h, "并行报告助手", "write")
	d, accepted := awGraphCreateAndTrial(t, h, a, "full")
	waiting := awTestRun(t, h, aliceToken, a.ID, d.ID, accepted.ID, af.WaitingConfirmation)
	left, right := waiting.Steps[2], waiting.Steps[3]
	if left.Status != af.Completed || right.Status != af.Completed || left.SessionID == right.SessionID || left.SessionID == "" || right.SessionID == "" || waiting.Steps[7].Status != af.Skipped || waiting.Steps[0].Branch != "true" {
		t.Fatal("parallel branch did not isolate nodes or skip false branch", waiting)
	}
	if left.StartedAt > right.FinishedAt || right.StartedAt > left.FinishedAt {
		t.Fatal("nodes ran sequentially")
	}
	model.mu.Lock()
	peak, brief := model.peak, model.began["brief"]
	model.mu.Unlock()
	if peak < 2 || brief != 0 {
		t.Fatal("real model calls did not overlap or skipped branch executed", peak, brief)
	}
	for _, st := range []af.StepRun{left, right} {
		worker := h.session(st.SessionID)
		if !strings.HasSuffix(worker.cwd, filepath.Join("nodes", st.StepID)) {
			t.Fatal("shared node workspace", worker.cwd)
		}
		if !h.mgr.agentWorkflowSessionOwned(worker.ID, h.mgr.agentWorkflowMarker(worker.ID)) || h.mgr.agentWorkflowToolBackend(worker) != nil {
			t.Fatal("node execution lost scoped identity")
		}
		resp := h.doAs(bobToken, http.MethodGet, "/api/sessions/"+worker.ID, "")
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatal("another user read execution session", resp.StatusCode)
		}
	}
	// Persisted human waits survive a real host restart without rerunning models.
	awTestSegments(t, h, a.ID, d.ID, accepted.ID, 1)
	awTestRestart(t, h)
	waiting = awTestRun(t, h, aliceToken, a.ID, d.ID, accepted.ID, af.WaitingConfirmation)
	if waiting.Steps[2].SessionID != left.SessionID || waiting.Steps[3].SessionID != right.SessionID {
		t.Fatal("restart replaced completed node sessions")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, pair := range [][2]string{{"check_left", "checked-left"}, {"check_right", "checked-right"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := h.doAs(aliceToken, http.MethodPost, "/api/apps/"+a.ID+"/agent-workflows/confirm", studioBody(map[string]string{"workflow_id": d.ID, "run_id": accepted.ID, "step_id": pair[0], "request_id": pair[0], "feedback": pair[1]}))
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 200 {
				errs <- fmt.Errorf("confirm %s: %d %s", pair[0], resp.StatusCode, body)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	done := awTestRun(t, h, aliceToken, a.ID, d.ID, accepted.ID, af.Completed)
	if done.ID != accepted.ID || done.Steps[4].Feedback != "checked-left" || done.Steps[5].Feedback != "checked-right" || done.Steps[9].Artifact != "report.md" {
		t.Fatal("confirmation receipt lost", done)
	}
	if !strings.Contains(done.Steps[8].Summary, "left.md") || !strings.Contains(done.Steps[8].Summary, "right.md") {
		t.Fatal("join lost actual branch outputs", done.Steps[8])
	}
	result := awTestCall(t, h, aliceToken, a.ID, "read", map[string]string{"action": "artifact", "workflow_id": d.ID, "run_id": done.ID, "step_id": "report"}, 200)
	if !strings.Contains(awTestValue[string](t, result, "text"), "GRAPH-OUTPUT-report") {
		t.Fatal("actual report missing")
	}
	model.mu.Lock()
	leftCalls, rightCalls := model.began["left"], model.began["right"]
	model.mu.Unlock()
	if leftCalls != 1 || rightCalls != 1 {
		t.Fatal("completed work replayed after restart", leftCalls, rightCalls)
	}
	// False condition skips the complete parallel subgraph and executes only brief.
	d2, r2 := awGraphCreateAndTrial(t, h, a, "brief")
	done2 := awTestRun(t, h, aliceToken, a.ID, d2.ID, r2.ID, af.Completed)
	if done2.Steps[0].Branch != "false" || done2.Steps[1].Status != af.Skipped || done2.Steps[4].Status != af.Skipped || done2.Steps[7].Status != af.Completed || done2.Steps[9].Status != af.Completed {
		t.Fatal("false route did not skip and merge correctly", done2)
	}
}
func awGraphWaitTerminal(t *testing.T, h *muHarness, a studioAgent, d af.Definition, id, status string) af.Run {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		run := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]string{"action": "run", "workflow_id": d.ID, "run_id": id}, 200), "run")
		if run.Status == status {
			return run
		}
		if awGraphTerminal(run.Status) {
			t.Fatalf("terminal %s, wanted %s: %s", run.Status, status, run.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("graph terminal timeout")
	return af.Run{}
}
func TestAgentWorkflowGraphFailureCancelsRunningSiblings(t *testing.T) {
	h, model := awGraphModelHarness(t, "left", true)
	a := awTestAgent(t, h, "并行失败验收", "write")
	d, run := awGraphCreateAndTrial(t, h, a, "full")
	failed := awGraphWaitTerminal(t, h, a, d, run.ID, af.Failed)
	model.mu.Lock()
	active, peak, report := model.active, model.peak, model.began["report"]
	model.mu.Unlock()
	if active != 0 || peak < 2 || report != 0 || failed.Steps[2].Status != af.Failed || failed.Steps[3].Status != af.Cancelled {
		t.Fatal("failed parallel run left a model alive or advanced", active, peak, report, failed)
	}
}
func TestAgentWorkflowGraphCancellationAndPermissionRevocation(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint(revoke), func(t *testing.T) {
			h, model := awGraphModelHarness(t, "", true)
			a := awTestAgent(t, h, "并行取消验收", "write")
			d, run := awGraphCreateAndTrial(t, h, a, "full")
			select {
			case <-model.barrier:
			case <-time.After(5 * time.Second):
				t.Fatal("parallel nodes not started")
			}
			want := af.Cancelled
			if revoke {
				body := studioAgentBody(a.Name, a.Instructions, "ready")
				body["revision"], body["tools"], body["skills"], body["mcp_servers"] = a.Revision, []string{tools.AgentWorkflowReadName}, []string{}, []string{}
				response := h.doAs(h.token, http.MethodPatch, "/api/manage/agents/"+a.ID, studioBody(body))
				defer response.Body.Close()
				if response.StatusCode != 200 {
					t.Fatal("revoke", response.StatusCode)
				}
				want = af.Interrupted
			} else {
				awTestCall(t, h, aliceToken, a.ID, "run", map[string]string{"action": "cancel", "workflow_id": d.ID, "run_id": run.ID, "request_id": "cancel-graph"}, 200)
			}
			done := awGraphWaitTerminal(t, h, a, d, run.ID, want)
			model.mu.Lock()
			active, report := model.active, model.began["report"]
			model.mu.Unlock()
			if active != 0 || report != 0 {
				t.Fatal("cancel/revocation left model running", active, report)
			}
			if done.Steps[9].Status != af.Skipped {
				t.Fatal("downstream node not stopped", done)
			}
		})
	}
}

func TestAgentWorkflowGraphConditionInputFailureHasNodeReceipt(t *testing.T) {
	h, model := awGraphModelHarness(t, "", false)
	a := awTestAgent(t, h, "条件失败验收", "write")
	def := awGraphReportDefinition()
	def.Steps[0].Condition.Operator = "gt"
	def.Steps[0].Condition.Value = "1"
	d := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "create", "request_id": "invalid-number-create", "name": def.Name, "inputs": def.Inputs, "steps": def.Steps, "graph": def.Graph}, 200), "workflow")
	run := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "workflow_id": d.ID, "request_id": "invalid-number-trial", "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "inputs": map[string]string{"mode": "not-a-number"}}, 200), "run")
	failed := awGraphWaitTerminal(t, h, a, d, run.ID, af.Failed)
	if failed.Steps[0].Status != af.Failed || failed.Steps[0].Summary == "" || failed.Steps[0].StartedAt == 0 || failed.Steps[0].FinishedAt < failed.Steps[0].StartedAt {
		t.Fatal("failed condition was represented as skipped", failed.Steps[0])
	}
	for _, st := range failed.Steps[1:] {
		if st.Status != af.Skipped {
			t.Fatal("descendant executed after condition failure", st)
		}
	}
	model.mu.Lock()
	began := len(model.began)
	model.mu.Unlock()
	if began != 0 {
		t.Fatal("invalid condition invoked a model", began)
	}
}

func TestAgentWorkflowGraphConfirmationWhileSiblingIsRunning(t *testing.T) {
	h, model := awGraphModelHarness(t, "", true)
	model.release = make(chan struct{})
	a := awTestAgent(t, h, "边执行边确认助手", "write")
	d, accepted := awGraphCreateAndTrial(t, h, a, "full")
	deadline := time.Now().Add(6 * time.Second)
	var waiting af.Run
	for time.Now().Before(deadline) {
		waiting = awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]string{"action": "run", "workflow_id": d.ID, "run_id": accepted.ID}, 200), "run")
		if waiting.Status == af.Running && waiting.Steps[4].Status == af.WaitingConfirmation {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if waiting.Status != af.Running || waiting.Steps[4].Status != af.WaitingConfirmation || waiting.Steps[3].Status != af.Running {
		t.Fatal("human wait blocked until all siblings completed", waiting)
	}
	body := map[string]string{"workflow_id": d.ID, "run_id": accepted.ID, "step_id": "check_left", "request_id": "during-worker", "feedback": "checked-left"}
	confirmed := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "confirm", body, 200), "run")
	if confirmed.Status != af.Running || confirmed.Steps[4].Feedback != "checked-left" {
		t.Fatal("confirmation interrupted sibling or lost feedback", confirmed)
	}
	if !awTestValue[bool](t, awTestCall(t, h, aliceToken, a.ID, "confirm", body, 200), "reused") {
		t.Fatal("repeat confirmation not deduplicated")
	}
	close(model.release)
	waiting = awTestRun(t, h, aliceToken, a.ID, d.ID, accepted.ID, af.WaitingConfirmation)
	if waiting.Steps[4].Status != af.Completed || waiting.Steps[4].Feedback != "checked-left" || waiting.Steps[5].Status != af.WaitingConfirmation {
		t.Fatal("worker result overwrote concurrent human receipt", waiting)
	}
	awTestCall(t, h, aliceToken, a.ID, "confirm", map[string]string{"workflow_id": d.ID, "run_id": accepted.ID, "step_id": "check_right", "request_id": "after-worker", "feedback": "checked-right"}, 200)
	done := awTestRun(t, h, aliceToken, a.ID, d.ID, accepted.ID, af.Completed)
	if done.Steps[9].Artifact != "report.md" {
		t.Fatal("join did not continue after concurrent confirmation", done)
	}
}

func TestAgentWorkflowGraphPreviousContextOnlyBusinessResults(t *testing.T) {
	run := af.Run{Definition: awGraphReportDefinition(), Steps: []af.StepRun{
		{StepID: "choose", Status: af.Completed, Summary: "条件判定：true"},
		{StepID: "fork", Status: af.Completed, Summary: "已启动并行分支"},
		{StepID: "left", Status: af.Completed, Summary: "已核对左侧资料", Artifact: "left.md"},
		{StepID: "right", Status: af.Completed, Summary: "已核对右侧资料", Artifact: "right.md"},
		{StepID: "check_left", Status: af.Completed, Summary: "本人已确认左侧", Feedback: "checked-left"},
		{StepID: "check_right", Status: af.Completed, Summary: "本人已确认右侧", Feedback: "checked-right"},
		{StepID: "parallel_join", Status: af.Completed, Summary: "duplicate branch aggregation"},
		{StepID: "brief", Status: af.Skipped, Summary: "未执行的资料"},
		{StepID: "merge", Status: af.Completed, Summary: "explicit join summary"},
		{StepID: "report", Status: af.Completed, Summary: "当前节点的结果"},
	}}
	before, _ := json.Marshal(run)
	for _, target := range []string{"parallel_join", "merge", "report"} {
		value := awGraphPreviousContext(run, target)
		var items []map[string]string
		if err := json.Unmarshal([]byte(value), &items); err != nil {
			t.Fatal(err)
		}
		if len(items) != 4 {
			t.Fatalf("%s context contains control, duplicate, skipped or current-node receipts: %s", target, value)
		}
		for i, expected := range run.Steps[2:6] {
			if items[i]["step_id"] != expected.StepID || items[i]["summary"] != expected.Summary || items[i]["artifact"] != expected.Artifact || items[i]["user_feedback"] != expected.Feedback {
				t.Fatalf("%s lost actual branch results or confirmation: %s", target, value)
			}
		}
	}
	if value := awGraphPreviousContext(run, "left"); value != "[]" {
		t.Fatal("control-only ancestry must be an empty JSON array", value)
	}
	text, err := af.Render("{{ steps.choose.summary }} / {{ steps.merge.summary }}", nil, run.Steps)
	if err != nil || text != "条件判定：true / explicit join summary" {
		t.Fatal("explicit control and join references no longer read raw receipts", text, err)
	}
	after, _ := json.Marshal(run)
	if string(before) != string(after) {
		t.Fatal("context projection mutated persisted receipts")
	}
}

func TestAgentWorkflowGraphEscapedJoinContextRemainsBounded(t *testing.T) {
	t.Run("encoded feedback persists through nested joins", func(t *testing.T) {
		h, _ := awGraphModelHarness(t, "", false)
		a := awTestAgent(t, h, "转义汇聚验收", "write")
		d, accepted := awGraphCreateAndTrial(t, h, a, "full")
		awTestRun(t, h, aliceToken, a.ID, d.ID, accepted.ID, af.WaitingConfirmation)
		left := "checked-left " + strings.Repeat("<>&\x01", 3900)
		right := "checked-right " + strings.Repeat("<>&\x01", 3900)
		encoded, _ := json.Marshal([]string{left, right})
		if len(encoded) <= 128<<10 {
			t.Fatal("fixture does not exercise encoded expansion")
		}
		for _, pair := range [][2]string{{"check_left", left}, {"check_right", right}} {
			awTestCall(t, h, aliceToken, a.ID, "confirm", map[string]string{"workflow_id": d.ID, "run_id": accepted.ID, "step_id": pair[0], "request_id": pair[0], "feedback": pair[1]}, 200)
		}
		done := awTestRun(t, h, aliceToken, a.ID, d.ID, accepted.ID, af.Completed)
		for _, index := range []int{6, 8} {
			value := done.Steps[index].Summary
			if len(value) > awGraphContextMaxBytes || !json.Valid([]byte(value)) {
				t.Fatalf("join %d generated unbounded or invalid JSON (%d bytes)", index, len(value))
			}
		}
		if !strings.Contains(done.Steps[6].Summary, "确认意见已截短") || !strings.Contains(done.Steps[6].Summary, "left.md") || !strings.Contains(done.Steps[6].Summary, "right.md") {
			t.Fatal("join omitted truncation evidence or actual artifact paths")
		}
		if done.Steps[4].Feedback != left || done.Steps[5].Feedback != right || done.Steps[9].Artifact != "report.md" {
			t.Fatal("bounded context changed original receipts or failed to continue")
		}
	})
	t.Run("many escaped summaries and extreme paths", func(t *testing.T) {
		run := af.Run{Definition: af.Definition{Graph: &af.Graph{Version: 1}}}
		for i := 0; i < 28; i++ {
			id := fmt.Sprintf("upstream_%d", i)
			run.Definition.Steps = append(run.Definition.Steps, af.Step{ID: id, Kind: "agent"})
			run.Definition.Graph.Edges = append(run.Definition.Graph.Edges, af.Edge{Source: id, Target: "join"})
			run.Steps = append(run.Steps, af.StepRun{StepID: id, Status: af.Completed, Summary: strings.Repeat("<>&\x01", 500), Artifact: fmt.Sprintf("notes-%d.md", i)})
		}
		value := awGraphPreviousContext(run, "join")
		var items []map[string]string
		if len(value) > awGraphContextMaxBytes || json.Unmarshal([]byte(value), &items) != nil || len(items) != 28 {
			t.Fatal("summary fan-in did not preserve bounded JSON node records")
		}
		for i, item := range items {
			if item["step_id"] != run.Steps[i].StepID || item["artifact"] != run.Steps[i].Artifact {
				t.Fatal("ordinary artifact path or node identity was lost")
			}
		}
		if !strings.Contains(value, "摘要已截短") {
			t.Fatal("omitted summary was not marked")
		}
		// Paths cannot be sliced into another filename, even when their legal
		// control characters expand sixfold. Keep identities and mark omissions.
		for i := range run.Steps {
			run.Steps[i].Summary = ""
			run.Steps[i].Artifact = strings.Repeat("\x01", 900) + fmt.Sprintf("-%d.md", i)
		}
		value = awGraphPreviousContext(run, "join")
		items = nil
		if len(value) > awGraphContextMaxBytes || json.Unmarshal([]byte(value), &items) != nil || len(items) != 28 {
			t.Fatal("escaped path metadata exceeded hard bound")
		}
		for i, item := range items {
			if item["step_id"] != run.Steps[i].StepID {
				t.Fatal("node identity lost")
			}
			if item["artifact"] != "" && item["artifact"] != run.Steps[i].Artifact {
				t.Fatal("artifact filename was truncated")
			}
			if item["artifact"] == "" && item["note"] == "" {
				t.Fatal("artifact omission was silent")
			}
		}
	})
}
