package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/workflowcode"
)

func awCodeRuntimes() any { return workflowcode.Capabilities() }

func awHasCode(d af.Definition) bool {
	for _, step := range d.Steps {
		if step.Kind == "code" || step.Kind == "iteration" || step.Kind == "loop" {
			return true
		}
	}
	return false
}

func (s *Server) agentWorkflowTestCodeNode(ctx context.Context, owner *identity.Scope, a studioAgent, db *taskqueue.Store, q agentWorkflowRequest, out map[string]any) (map[string]any, error) {
	d, err := db.AWGet(sessionResultOwnerKey(owner), a.ID, q.WorkflowID)
	if err != nil {
		return nil, err
	}
	if d.Archived || d.Deleted || q.ExpectedRevision != d.Revision || q.ExpectedHash != af.Hash(d) {
		return nil, taskqueue.ErrConflict
	}
	var spec *af.CodeSpec
	for _, step := range d.Steps {
		if step.ID == q.StepID && step.Kind == "code" {
			spec = step.Code
		}
	}
	if spec == nil {
		return nil, errors.New("单节点试跑需要选择已保存的代码节点")
	}
	if err = af.ValidateCodeInputs(spec, q.NodeInputs); err != nil {
		return nil, err
	}
	// Sample arguments are explicit. No preceding node, model, tool or business
	// side effect is launched to manufacture them for this isolated trial.
	if len(q.Files) > 0 || len(q.Inputs) > 0 {
		return nil, errors.New("单节点试跑仅接受 node_inputs，不运行前序节点或读取资料文件")
	}
	run, err := s.prepareAgentWorkflowRun(ctx, owner, a, d, q.SourceSessionID, map[string]string{}, nil, true, "")
	if err != nil {
		return nil, err
	}
	run.NodeTest = &af.NodeTest{StepID: q.StepID, Inputs: append(json.RawMessage(nil), q.NodeInputs...)}
	accepted, _, reused, err := db.AWAdmit(run, q.RequestID)
	if err != nil || reused {
		s.mgr.awRemoveWorkspace(owner, run.Workspace)
	}
	if err != nil {
		return nil, err
	}
	out["run"], out["reused"] = awPublicRun(accepted), reused
	s.mgr.taskRuns.wake()
	return out, nil
}

// Full runs, batch bodies and node tests share one execution and audit path.
func (r *taskRunRuntime) awExecuteCode(ctx context.Context, run af.Run, index int) (af.StepRun, string, string) {
	return r.awExecuteCodeProgress(ctx, run, index, nil)
}

func (r *taskRunRuntime) awExecuteCodeProgress(ctx context.Context, run af.Run, index int, publish func(af.StepRun) error) (af.StepRun, string, string) {
	step, st := run.Definition.Steps[index], run.Steps[index]
	fail := func(status, message string) (af.StepRun, string, string) {
		st.Status, st.Summary, st.FinishedAt = status, message, time.Now().UnixMilli()
		return st, status, message
	}
	interrupted := func() string {
		if r.ctx != nil && r.ctx.Err() != nil {
			return af.Interrupted
		}
		return af.Cancelled
	}
	var inputs json.RawMessage
	var err error
	if run.NodeTest != nil {
		inputs = run.NodeTest.Inputs
	} else {
		inputs, err = af.ResolveCodeInputs(run, step.ID)
	}
	if err != nil {
		return fail(af.Failed, err.Error())
	}
	if err = af.ValidateCodePolicy(step.Code, step.Policy); err != nil {
		return fail(af.Failed, err.Error())
	}
	st.CodeInputs = append(json.RawMessage(nil), inputs...)
	notify := func() error {
		if publish == nil {
			return nil
		}
		return publish(awCloneStepReceipt(st))
	}
	retries, delay := 0, 0
	if step.Policy != nil {
		retries, delay = step.Policy.MaxRetries, step.Policy.RetryDelayMS
	}
	for attempt := 1; attempt <= retries+1; attempt++ {
		if ctx.Err() != nil {
			return fail(interrupted(), "代码执行已停止")
		}
		if status, e := r.awCodeAttemptPermission(ctx, run); e != nil {
			return fail(status, e.Error())
		}
		release, e := awAcquireExecutionSlot(ctx)
		if e != nil {
			return fail(interrupted(), "等待代码执行名额时已停止")
		}
		if status, e := r.awCodeAttemptPermission(ctx, run); e != nil {
			release()
			return fail(status, e.Error())
		}
		started := time.Now().UnixMilli()
		st.Attempts = append(st.Attempts, af.CodeAttempt{Number: attempt, StartedAt: started})
		st.Status, st.Summary = af.Running, fmt.Sprintf("正在执行%s代码，第%d次尝试", step.Code.Language, attempt)
		if e = notify(); e != nil {
			release()
			return fail(af.Interrupted, "代码尝试启动记录无法保存；未继续执行")
		}
		result, executeErr := workflowcode.Execute(ctx, workflowcode.Request{Language: step.Code.Language, Source: step.Code.Source, Inputs: inputs, TimeoutSeconds: step.Code.TimeoutSeconds})
		release()
		sum := sha256.Sum256([]byte(step.Code.Source))
		receipt := &af.CodeExecution{Language: step.Code.Language, Runtime: result.Runtime, SourceSHA256: hex.EncodeToString(sum[:]), Stdout: result.Stdout, Stderr: result.Stderr, ExitCode: result.ExitCode, TimedOut: result.TimedOut, DurationMS: result.DurationMS, ErrorCode: result.ErrorCode, StdoutTruncated: result.StdoutTruncated, StderrTruncated: result.StderrTruncated}
		if executeErr == nil {
			if e = af.ValidateCodeOutput(step.Code, result.Output); e != nil {
				executeErr = e
				receipt.ErrorCode = "output_schema_invalid"
			}
		}
		finished := time.Now().UnixMilli()
		actual := af.CodeAttempt{Number: attempt, StartedAt: started, FinishedAt: finished, Execution: receipt, Output: append(json.RawMessage(nil), result.Output...)}
		if executeErr != nil {
			actual.Error = executeErr.Error()
		}
		st.Attempts[len(st.Attempts)-1] = actual
		st.CodeExecution = receipt
		st.FinishedAt = finished
		if executeErr == nil {
			st.Output = append(json.RawMessage(nil), result.Output...)
			st.Status, st.Summary = af.Completed, fmt.Sprintf("%s 代码执行完成", step.Code.Language)
			return st, "", ""
		}
		if ctx.Err() != nil {
			return fail(interrupted(), executeErr.Error())
		}
		// Cancellation, missing isolation and persistence errors are never retried
		// or hidden behind a configured business fallback.
		recoverable := af.RecoverableCodeExecution(receipt)
		if attempt <= retries && recoverable {
			st.FinishedAt = 0
			st.Summary = fmt.Sprintf("第%d次尝试失败，等待重试：%s", attempt, actual.Error)
			if e = notify(); e != nil {
				return fail(af.Interrupted, "代码失败记录无法保存；未继续重试")
			}
			timer := time.NewTimer(time.Duration(delay) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fail(interrupted(), "代码重试已停止")
			case <-timer.C:
			}
			continue
		}
		if recoverable && step.Policy != nil && step.Policy.OnFailure == "fallback" {
			st.Output = append(json.RawMessage(nil), step.Policy.Fallback...)
			st.FallbackUsed = true
			st.Status, st.Summary = af.Completed, "代码执行失败，已使用明确配置的备用输出；失败详情已保留"
			return st, "", ""
		}
		return fail(af.Failed, executeErr.Error())
	}
	return fail(af.Interrupted, "代码尝试状态异常")
}

func awCloneStepReceipt(value af.StepRun) af.StepRun {
	raw, _ := json.Marshal(value)
	var clone af.StepRun
	_ = json.Unmarshal(raw, &clone)
	return clone
}

func (r *taskRunRuntime) awCodeAttemptPermission(ctx context.Context, requested af.Run) (string, error) {
	if ctx.Err() != nil {
		return awRepeatStopStatus(ctx, r), ctx.Err()
	}
	db, err := r.m.taskRuns.database(false)
	if err != nil || db == nil {
		return af.Interrupted, errors.New("代码执行的真实运行记录不可用")
	}
	run, err := db.AWRun(requested.OwnerID, requested.AgentID, requested.WorkflowID, requested.ID)
	if err != nil || run.VersionID != requested.VersionID {
		return af.Interrupted, errors.New("代码执行的冻结版本绑定已失效")
	}
	if run.CancelRequested || awGraphTerminal(run.Status) {
		return af.Cancelled, errors.New("本次运行已停止，未继续执行代码尝试")
	}
	var snap awExecutionSnapshot
	if json.Unmarshal(run.AgentSnapshot, &snap) != nil || snap.Agent.ID != run.AgentID || sessionResultOwnerKey(snap.Owner) != run.OwnerID {
		return af.Interrupted, errors.New("代码执行身份快照无效")
	}
	if err = r.awRepeatCheck(ctx, run, snap); err != nil {
		return af.Interrupted, err
	}
	return "", nil
}

func (r *taskRunRuntime) executeAgentWorkflowNodeTest(ctx context.Context, db *taskqueue.Store, execution taskqueue.Run, run af.Run, snap awExecutionSnapshot) taskqueue.Outcome {
	settle := func(status, message string) taskqueue.Outcome {
		current, err := db.AWRun(run.OwnerID, run.AgentID, run.WorkflowID, run.ID)
		if err == nil {
			current, err = db.AWGraphSettle(run.OwnerID, run.AgentID, run.WorkflowID, run.ID, execution.ID, execution.Revision, current.Revision, status, message)
		}
		if err != nil {
			return taskqueue.Outcome{Status: taskqueue.Interrupted, Error: "单节点试跑结果无法保存；未自动重跑"}
		}
		return awGraphOutcome(current)
	}
	current, ok := r.m.studio.get(run.AgentID)
	r.m.mu.Lock()
	valid := r.m.scheduleOwnerValid
	r.m.mu.Unlock()
	if !ok || current.Status != "ready" || valid == nil || !valid(snap.Owner) || !r.m.studioNativeSelected(current, "agent_workflow_run") || r.m.agentWorkflowFingerprint(current, snap.Owner) != run.AgentFingerprint {
		return settle(af.Interrupted, "当前账号、Agent配置或执行权限已变化")
	}
	if run.CancelRequested || ctx.Err() != nil {
		return settle(af.Cancelled, "单节点试跑已取消")
	}
	i := awGraphIndex(run, run.NodeTest.StepID)
	if i < 0 || run.Definition.Steps[i].Kind != "code" {
		return settle(af.Failed, "试跑节点绑定无效")
	}
	before, after := run.Steps[i], run.Steps[i]
	after.Status, after.StartedAt = af.Running, time.Now().UnixMilli()
	after.CodeInputs = append(json.RawMessage(nil), run.NodeTest.Inputs...)
	next, err := db.AWGraphSaveStep(run.OwnerID, run.AgentID, run.WorkflowID, run.ID, execution.ID, execution.Revision, before, after)
	if err != nil {
		return settle(af.Interrupted, "单节点试跑启动记录无法保存")
	}
	run = next
	previous := run.Steps[i]
	publish := func(value af.StepRun) error {
		next, e := db.AWGraphSaveStep(run.OwnerID, run.AgentID, run.WorkflowID, run.ID, execution.ID, execution.Revision, previous, value)
		if e == nil {
			previous = next.Steps[i]
		}
		return e
	}
	after, status, message := r.awExecuteCodeProgress(ctx, run, i, publish)
	if _, err = db.AWGraphSaveStep(run.OwnerID, run.AgentID, run.WorkflowID, run.ID, execution.ID, execution.Revision, previous, after); err != nil {
		return settle(af.Interrupted, "单节点试跑执行记录无法保存")
	}
	if status == "" {
		status = af.Completed
	}
	return settle(status, message)
}

func awJoinOutput(run af.Run, id string) (json.RawMessage, error) {
	ancestors := awGraphAncestors(run.Definition, id)
	values := map[string]json.RawMessage{}
	for _, step := range run.Steps {
		if ancestors[step.StepID] && step.Status == af.Completed && len(step.Output) > 0 {
			// Keep branch identities rather than overwriting equal field names.
			for _, definition := range run.Definition.Steps {
				if definition.ID == step.StepID && (definition.Kind == "code" || definition.Kind == "iteration" || definition.Kind == "loop") {
					values[step.StepID] = step.Output
				}
			}
		}
	}
	if len(values) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(values)
	if len(raw) > af.MaxCodeDataBytes {
		return nil, errors.New("汇聚结构化输出超过256KiB，请在分支中精简结果")
	}
	return raw, err
}
