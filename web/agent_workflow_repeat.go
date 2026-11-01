package web

// Repeat containers keep one durable parent node. Each item has its own
// execution receipt and (for Agent bodies) its own scoped conversation.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yosukeno/pogo/agent"
	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/session"
)

const (
	awRepeatReceiptLimit      = 2 << 20
	awRepeatItemInputLimit    = 64 << 10
	awRepeatAgentSummaryLimit = 8 << 10
	awRepeatExecutionLogLimit = 2 << 10
)

var errAWRepeatLimit = errors.New("迭代记录或聚合结果超过限制，请减少项数或精简数据")

func awRepeatArtifactID(parentID string, index int) string {
	sum := sha256.Sum256([]byte(parentID + ":" + strconv.Itoa(index)))
	return "ri_" + hex.EncodeToString(sum[:12])
}

func awRepeatArtifactRelativePath(parentID string, index int, path string) string {
	return filepath.ToSlash(filepath.Join("iteration-results", parentID, strconv.Itoa(index), filepath.FromSlash(path)))
}

// Only completed, explicitly preceding containers contribute files. Item
// workspaces never receive another in-flight sibling's writable directory.
func (m *Manager) awMaterializeRepeatArtifacts(owner *identity.Scope, run af.Run, allowed map[string]bool, cwd string) error {
	for _, receipt := range run.Steps {
		if !allowed[receipt.StepID] || receipt.Status != af.Completed || receipt.Repeat == nil {
			continue
		}
		for _, item := range receipt.Repeat.Items {
			if item.Status != af.Completed || item.Artifact == "" {
				continue
			}
			if !af.ValidID(receipt.StepID) || item.Index < 0 || item.Index >= af.MaxRepeatItems || !af.SafePath(item.Artifact) || !af.ValidHash(item.ArtifactSHA256) {
				return errors.New("前序迭代产物身份无效")
			}
			data, err := m.awReadArtifact(run.ID, awRepeatArtifactID(receipt.StepID, item.Index))
			if err != nil || awResourceHash(data) != item.ArtifactSHA256 {
				return errors.New("前序迭代产物快照不可用或校验失败")
			}
			if err = m.awWriteOwned(owner, filepath.Join(cwd, filepath.FromSlash(awRepeatArtifactRelativePath(receipt.StepID, item.Index, item.Artifact))), data); err != nil {
				return err
			}
		}
	}
	return nil
}

func awRepeatClone[T any](value T) T {
	raw, _ := json.Marshal(value)
	var copy T
	_ = json.Unmarshal(raw, &copy)
	return copy
}

func awRepeatTerminal(status string) bool {
	return status == af.Completed || status == af.Failed || status == af.Cancelled || status == af.Interrupted || status == af.Skipped
}

func awRepeatStopStatus(ctx context.Context, runtime *taskRunRuntime) string {
	if runtime.ctx != nil && runtime.ctx.Err() != nil {
		return af.Interrupted
	}
	if ctx.Err() != nil {
		return af.Cancelled
	}
	return af.Failed
}

func awRepeatPermissionStatus(ctx context.Context, runtime *taskRunRuntime) string {
	if ctx.Err() != nil {
		return awRepeatStopStatus(ctx, runtime)
	}
	return af.Interrupted
}

func (r *taskRunRuntime) awRepeatCheck(ctx context.Context, run af.Run, snap awExecutionSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.m.mu.Lock()
	valid := r.m.scheduleOwnerValid
	r.m.mu.Unlock()
	current, ok := r.m.studio.get(run.AgentID)
	if valid == nil || !valid(snap.Owner) || !ok || current.Status != "ready" || !r.m.studioNativeSelected(current, "agent_workflow_run") {
		return errors.New("当前账号、Agent或内部工作流执行权限已撤销")
	}
	if r.m.agentWorkflowFingerprint(current, snap.Owner) != run.AgentFingerprint {
		return errors.New("Agent能力配置已变化，未继续处理下一项")
	}
	return r.m.checkAgentWorkflowResources(ctx, snap.Owner, current, run)
}

// awExecuteRepeat never replays an existing item receipt. Restart recovery and
// explicit retry are managed by the outer logical Run controller.
func (r *taskRunRuntime) awExecuteRepeat(parent context.Context, run af.Run, index int, snap awExecutionSnapshot, publish func(af.StepRun) error) (af.StepRun, string, string) {
	step := run.Definition.Steps[index]
	st := awRepeatClone(run.Steps[index])
	failed := func(status, message string) (af.StepRun, string, string) {
		st.Status, st.Summary, st.FinishedAt = status, message, time.Now().UnixMilli()
		return st, status, message
	}
	if st.Repeat != nil {
		return failed(af.Interrupted, "迭代节点已有执行记录，未自动重跑")
	}
	if err := af.ValidateRepeatSpec(step); err != nil {
		return failed(af.Failed, err.Error())
	}
	if err := r.awRepeatCheck(parent, run, snap); err != nil {
		return failed(awRepeatPermissionStatus(parent, r), err.Error())
	}
	if st.StartedAt == 0 {
		st.StartedAt = time.Now().UnixMilli()
	}
	st.Status = af.Running
	st.Repeat = &af.RepeatRun{Kind: step.Kind, Items: []af.RepeatItem{}}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var mu sync.Mutex
	var lastPublished = awRepeatClone(st)
	var persistError error
	var failure, message string
	commit := func() error {
		raw, err := json.Marshal(st.Repeat)
		if err != nil || len(raw) > awRepeatReceiptLimit {
			return errAWRepeatLimit
		}
		if publish != nil {
			if err = publish(awRepeatClone(st)); err != nil {
				persistError = err
				cancel()
				return err
			}
		}
		lastPublished = awRepeatClone(st)
		return nil
	}
	stop := func(status, text string) {
		if failure == "" {
			failure, message = status, text
			cancel()
		}
	}
	update := func(item af.RepeatItem) error {
		mu.Lock()
		defer mu.Unlock()
		if persistError != nil {
			return persistError
		}
		if item.Index < 0 || item.Index >= len(st.Repeat.Items) {
			return errors.New("迭代项索引无效")
		}
		if awRepeatTerminal(st.Repeat.Items[item.Index].Status) {
			return nil // a domain-limit receipt may already have settled this item
		}
		if len(item.Input) > awRepeatItemInputLimit || len(item.CodeInputs) > af.MaxCodeDataBytes {
			return errAWRepeatLimit
		}
		item = awRepeatBoundLogs(item)
		previous := st.Repeat.Items[item.Index]
		previousState := st.Repeat.State
		st.Repeat.Items[item.Index] = awRepeatClone(item)
		if step.Kind == "loop" && item.Status == af.Completed {
			if len(item.Output) > awRepeatItemInputLimit {
				// Rejected state must not advance the durable loop cursor. The
				// actual code attempt remains available in the item history.
				item.Status, item.Output, item.Error = af.Failed, nil, "循环状态超过64KiB，请精简状态数据"
				item.FallbackUsed = false
				item.Summary = item.Error
				st.Repeat.Items[item.Index] = item
				stop(af.Failed, item.Error)
			} else {
				st.Repeat.State = append(json.RawMessage(nil), item.Output...)
			}
		}
		if step.Kind == "iteration" && len(item.Output) > 0 {
			var outputBytes int
			for _, entry := range st.Repeat.Items {
				outputBytes += len(entry.Output)
			}
			if outputBytes > af.MaxCodeDataBytes {
				// The execution is real, but this result cannot enter the data
				// contract. Keep its runtime/log receipt and reject the payload.
				item.Status, item.Output, item.Error = af.Failed, nil, errAWRepeatLimit.Error()
				item.FallbackUsed = false // the fallback was rejected, not consumed
				item.Summary = item.Error
				st.Repeat.Items[item.Index] = item
				stop(af.Failed, item.Error)
			}
		}
		if err := commit(); err != nil {
			st.Repeat.Items[item.Index] = previous
			st.Repeat.State = previousState
			if errors.Is(err, errAWRepeatLimit) {
				stop(af.Failed, err.Error())
			}
			return err
		}
		return nil
	}
	if step.Kind == "iteration" {
		items, err := af.ResolveIterationItems(run, step.ID)
		if err != nil {
			st.Repeat = nil
			return failed(af.Failed, err.Error())
		}
		st.Repeat.Total = len(items)
		for i, item := range items {
			if len(item) > awRepeatItemInputLimit {
				st.Repeat = nil
				return failed(af.Failed, "迭代单项输入超过64KiB")
			}
			st.Repeat.Items = append(st.Repeat.Items, af.RepeatItem{Index: i, Status: af.Queued, Input: append(json.RawMessage(nil), item...)})
		}
		if err = commit(); err != nil {
			st = lastPublished
			return failed(af.Interrupted, "迭代初始记录无法保存，未开始执行")
		}
		concurrency := max(1, step.Iteration.Concurrency)
		next := 0
		var workers sync.WaitGroup
		for worker := 0; worker < min(concurrency, len(items)); worker++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for {
					mu.Lock()
					if failure != "" || ctx.Err() != nil || next >= len(items) {
						mu.Unlock()
						return
					}
					i := next
					next++
					// Reserve space for in-flight runtime receipts and one whole
					// bounded aggregate before invoking another body.
					current, _ := json.Marshal(st.Repeat)
					active := 0
					for _, entry := range st.Repeat.Items {
						if entry.Status == af.Running {
							active++
						}
					}
					if len(current)+(active+1)*(32<<10)+(2*af.MaxCodeDataBytes) > awRepeatReceiptLimit {
						stop(af.Failed, errAWRepeatLimit.Error())
						mu.Unlock()
						return
					}
					mu.Unlock()
					body, err := af.ResolveRepeatBody(run, step.ID, items[i], i)
					item := af.RepeatItem{Index: i, Status: af.Running, Input: append(json.RawMessage(nil), items[i]...), StartedAt: time.Now().UnixMilli()}
					if err != nil {
						item.Status = af.Failed
						item.Error = err.Error()
						item.Summary = item.Error
						item.FinishedAt = time.Now().UnixMilli()
					} else if err = r.awRepeatCheck(ctx, run, snap); err != nil {
						item.Status, item.Error, item.Summary, item.FinishedAt = awRepeatPermissionStatus(ctx, r), err.Error(), err.Error(), time.Now().UnixMilli()
					} else {
						item = r.awExecuteRepeatItem(ctx, run, step.ID, i, body, item, snap, update)
					}
					if err = update(item); err != nil {
						mu.Lock()
						if persistError == nil {
							stop(af.Interrupted, "迭代项执行结果无法保存；未自动重跑")
						}
						mu.Unlock()
						return
					}
					if item.Status != af.Completed {
						mu.Lock()
						if item.Status == af.Cancelled || item.Status == af.Interrupted || step.Iteration.OnItemError != "continue" {
							stop(item.Status, item.Error)
						}
						mu.Unlock()
					}
				}
			}()
		}
		workers.Wait()
	} else {
		state, err := af.ResolveLoopInitial(run, step.ID)
		if err != nil {
			st.Repeat = nil
			return failed(af.Failed, err.Error())
		}
		if len(state) > awRepeatItemInputLimit {
			st.Repeat = nil
			return failed(af.Failed, "循环初始状态超过64KiB")
		}
		st.Repeat.Total, st.Repeat.State = step.Loop.MaxIterations, state
		if err = commit(); err != nil {
			st = lastPublished
			return failed(af.Interrupted, "循环初始记录无法保存，未开始执行")
		}
		for i := 0; i < step.Loop.MaxIterations; i++ {
			if ctx.Err() != nil {
				break
			}
			if len(state) > awRepeatItemInputLimit {
				stop(af.Failed, "循环状态超过64KiB，未执行下一轮")
				break
			}
			current, _ := json.Marshal(st.Repeat)
			if len(current)+4*af.MaxCodeDataBytes+(32<<10) > awRepeatReceiptLimit {
				stop(af.Failed, errAWRepeatLimit.Error())
				break
			}
			body, err := af.ResolveRepeatBody(run, step.ID, state, i)
			item := af.RepeatItem{Index: i, Status: af.Queued, Input: append(json.RawMessage(nil), state...)}
			st.Repeat.Items = append(st.Repeat.Items, item)
			if err != nil {
				item.Status, item.Error, item.Summary, item.StartedAt, item.FinishedAt = af.Failed, err.Error(), err.Error(), time.Now().UnixMilli(), time.Now().UnixMilli()
				_ = update(item)
				stop(af.Failed, err.Error())
				break
			}
			if err = r.awRepeatCheck(ctx, run, snap); err != nil {
				item.Status, item.Error, item.Summary, item.StartedAt, item.FinishedAt = awRepeatPermissionStatus(ctx, r), err.Error(), err.Error(), time.Now().UnixMilli(), time.Now().UnixMilli()
				_ = update(item)
				stop(item.Status, item.Error)
				break
			}
			item.Status, item.StartedAt = af.Running, time.Now().UnixMilli()
			item = r.awExecuteRepeatItem(ctx, run, step.ID, i, body, item, snap, update)
			if err = update(item); err != nil {
				stop(af.Interrupted, "循环执行结果无法保存；未自动重跑")
				break
			}
			if item.Status != af.Completed {
				stop(item.Status, item.Error)
				break
			}
			state = append(json.RawMessage(nil), item.Output...)
			finished, err := af.EvaluateLoopUntil(step.Loop, state, i)
			if err != nil {
				stop(af.Failed, err.Error())
				break
			}
			if finished {
				break
			}
			if i == step.Loop.MaxIterations-1 {
				st.Repeat.LimitReached = true
				stop(af.Failed, "循环已到执行上限，退出条件尚未满足")
			}
		}
	}
	if persistError != nil {
		st = lastPublished
		awRepeatSettleUnknown(st.Repeat, af.Interrupted, "该项实际结果待核查，未自动重跑")
		st.Status, st.Summary, st.FinishedAt = af.Interrupted, "迭代记录无法持久化，实际结果待核查；未自动重跑", time.Now().UnixMilli()
		return st, af.Interrupted, st.Summary
	}
	if parent.Err() != nil && failure == "" {
		failure, message = awRepeatStopStatus(parent, r), "本次迭代已停止"
	}
	for i := range st.Repeat.Items {
		if st.Repeat.Items[i].Status == af.Queued {
			st.Repeat.Items[i].Status = af.Skipped
			st.Repeat.Items[i].FinishedAt = time.Now().UnixMilli()
		}
	}
	if failure != "" {
		awRepeatSettleUnknown(st.Repeat, af.Interrupted, "该项结果未完整入库，实际结果待核查")
	}
	st.FinishedAt = time.Now().UnixMilli()
	if failure != "" {
		st.Status, st.Summary = failure, message
		return st, failure, message
	}
	var output any
	if step.Kind == "iteration" {
		values := make([]json.RawMessage, len(st.Repeat.Items))
		type itemError struct {
			Index int    `json:"index"`
			Error string `json:"error"`
		}
		errors := []itemError{}
		completed := 0
		for i, item := range st.Repeat.Items {
			values[i] = json.RawMessage("null")
			if item.Status == af.Completed {
				values[i] = item.Output
				completed++
			} else {
				errors = append(errors, itemError{i, item.Error})
			}
		}
		output = map[string]any{"items": values, "errors": errors, "total": len(values), "completed": completed, "failed": len(errors)}
	} else {
		output = map[string]any{"state": st.Repeat.State, "iterations": len(st.Repeat.Items), "limit_reached": false}
	}
	raw, err := json.Marshal(output)
	if err != nil || len(raw) > af.MaxCodeDataBytes {
		st.Status, st.Summary = af.Failed, errAWRepeatLimit.Error()
		return st, af.Failed, st.Summary
	}
	st.Status, st.Output = af.Completed, raw
	st.Summary = fmt.Sprintf("%s已完成，共处理%d项", map[string]string{"iteration": "集合迭代", "loop": "有界循环"}[step.Kind], len(st.Repeat.Items))
	return st, "", ""
}

func awRepeatSettleUnknown(repeat *af.RepeatRun, status, message string) {
	if repeat == nil {
		return
	}
	for i := range repeat.Items {
		item := &repeat.Items[i]
		if item.Status == af.Running {
			item.Status, item.Error, item.Summary, item.FinishedAt = status, message, message, time.Now().UnixMilli()
		}
		if item.Status == af.Queued {
			item.Status, item.FinishedAt = af.Skipped, time.Now().UnixMilli()
		}
	}
}

func awRepeatBoundLogs(item af.RepeatItem) af.RepeatItem {
	item = awRepeatClone(item)
	bound := func(execution *af.CodeExecution) {
		if execution == nil {
			return
		}
		budget := awRepeatExecutionLogLimit
		for _, stream := range []struct {
			text      *string
			truncated *bool
		}{{&execution.Stdout, &execution.StdoutTruncated}, {&execution.Stderr, &execution.StderrTruncated}} {
			if len(*stream.text) > budget {
				text := (*stream.text)[:budget]
				for !utf8.ValidString(text) {
					text = text[:len(text)-1]
				}
				*stream.text = text
				*stream.truncated = true
			}
			budget -= len(*stream.text)
		}
	}
	bound(item.CodeExecution)
	for i := range item.Attempts {
		bound(item.Attempts[i].Execution)
	}
	return item
}

func (r *taskRunRuntime) awExecuteRepeatItem(ctx context.Context, run af.Run, parentID string, index int, body af.Step, item af.RepeatItem, snap awExecutionSnapshot, publish func(af.RepeatItem) error) af.RepeatItem {
	fail := func(status, message string) af.RepeatItem {
		item.Status, item.Error, item.Summary, item.FinishedAt = status, message, message, time.Now().UnixMilli()
		return item
	}
	if body.Kind == "code" {
		virtual := run
		virtual.NodeTest = nil
		virtual.Definition = awRepeatClone(run.Definition)
		virtual.Definition.Steps = []af.Step{body}
		virtual.Definition.Graph = nil
		virtual.Steps = []af.StepRun{{StepID: body.ID, Status: af.Running, StartedAt: item.StartedAt}}
		inputs, err := af.ResolveCodeInputs(virtual, body.ID)
		if err != nil {
			return fail(af.Failed, err.Error())
		}
		item.CodeInputs = inputs
		if len(inputs) > af.MaxCodeDataBytes {
			return fail(af.Failed, "迭代代码输入超过256KiB")
		}
		if err = publish(item); err != nil {
			return fail(af.Interrupted, "迭代代码启动记录无法保存")
		}
		apply := func(receipt af.StepRun) error {
			item.CodeInputs, item.CodeExecution, item.Attempts, item.FallbackUsed = receipt.CodeInputs, receipt.CodeExecution, receipt.Attempts, receipt.FallbackUsed
			return publish(item)
		}
		after, status, message := r.awExecuteCodeProgress(ctx, virtual, 0, apply)
		item.CodeInputs, item.CodeExecution, item.Attempts, item.FallbackUsed = after.CodeInputs, after.CodeExecution, after.Attempts, after.FallbackUsed
		item.Output, item.Summary, item.FinishedAt = after.Output, after.Summary, after.FinishedAt
		if status != "" {
			return fail(status, message)
		}
		item.Status = af.Completed
		return item
	}
	return r.awExecuteRepeatAgent(ctx, run, parentID, index, body, item, snap, publish)
}

func (r *taskRunRuntime) awRepeatAgentSession(run af.Run, snap awExecutionSnapshot, parentID string, index int) (*Session, error) {
	dir := filepath.Join(run.Workspace, "nodes", parentID, "items", strconv.Itoa(index))
	relative, err := r.m.awRelative(snap.Owner, dir)
	if err != nil {
		return nil, err
	}
	root, err := r.m.awRoot(snap.Owner)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err = root.MkdirAll(relative, 0700); err != nil {
		return nil, err
	}
	for _, file := range run.Files {
		data, e := r.m.awReadOwned(snap.Owner, filepath.Join(run.Workspace, filepath.FromSlash(file.Path)), 4<<20)
		if e != nil || awResourceHash(data) != file.SHA256 {
			return nil, errors.New("冻结输入资料不可用")
		}
		if e = r.m.awWriteOwned(snap.Owner, filepath.Join(dir, filepath.FromSlash(file.Path)), data); e != nil {
			return nil, e
		}
	}
	ancestors := awGraphAncestors(run.Definition, parentID)
	if run.Definition.Graph == nil {
		for _, step := range run.Definition.Steps {
			if step.ID == parentID {
				break
			}
			ancestors[step.ID] = true
		}
	}
	for _, receipt := range run.Steps {
		if !ancestors[receipt.StepID] || receipt.Status != af.Completed || receipt.Artifact == "" {
			continue
		}
		data, e := r.m.awReadArtifact(run.ID, receipt.StepID)
		if e != nil || awResourceHash(data) != receipt.ArtifactSHA256 {
			return nil, errors.New("前序产物快照不可用")
		}
		if e = r.m.awWriteOwned(snap.Owner, filepath.Join(dir, filepath.FromSlash(receipt.Artifact)), data); e != nil {
			return nil, e
		}
	}
	if err = r.m.awMaterializeRepeatArtifacts(snap.Owner, run, ancestors, dir); err != nil {
		return nil, err
	}
	if _, err = r.m.agentWorkflowResourcePrompt(snap.Owner, run, parentID, dir, true); err != nil {
		return nil, err
	}
	marker := session.AgentWorkflowIdentity{OwnerID: run.OwnerID, AgentID: run.AgentID, WorkflowID: run.WorkflowID, RunID: run.ID, SourceSessionID: run.SourceSessionID}
	sess, err := r.m.createStudioExecutionConversationWithWorkflow(snap.Agent, snap.Owner, false, nil, nil, &marker, relative)
	if err == nil {
		sess.SetPolicy(awWorkflowToolApprovalMode(run.Definition), 0)
		title := fmt.Sprintf("流程 · %s · %s · %d", run.Definition.Name, parentID, index+1)
		_ = sess.SetInfo(&title, nil)
	}
	return sess, err
}

func (r *taskRunRuntime) awExecuteRepeatAgent(ctx context.Context, run af.Run, parentID string, index int, body af.Step, item af.RepeatItem, snap awExecutionSnapshot, publish func(af.RepeatItem) error) af.RepeatItem {
	fail := func(status, message string) af.RepeatItem {
		item.Status, item.Error, item.Summary, item.FinishedAt = status, message, message, time.Now().UnixMilli()
		return item
	}
	release, err := awAcquireExecutionSlot(ctx)
	if err != nil {
		return fail(awRepeatStopStatus(ctx, r), "迭代项启动前已停止")
	}
	defer release()
	if err = r.awRepeatCheck(ctx, run, snap); err != nil {
		return fail(awRepeatPermissionStatus(ctx, r), err.Error())
	}
	sess, err := r.awRepeatAgentSession(run, snap, parentID, index)
	if err != nil {
		return fail(af.Failed, err.Error())
	}
	item.SessionID = sess.ID
	if err = publish(item); err != nil {
		sess.closeUnpublished()
		return fail(af.Interrupted, "迭代会话归属无法保存，未执行本项")
	}
	// ResolveRepeatBody already performed exactly one data substitution. Never
	// render its task again: a value containing template syntax is plain data.
	prompt := fmt.Sprintf("你正在执行当前 Agent 的内部流程《%s》的步骤 %s（%s），本次集合的第 %d 项。只处理这一项，不修改工作流，不创建后台任务、子 Agent 或监控，不调用其他流程。\n本项任务：\n%s\n", run.Definition.Name, parentID, body.Title, index+1, body.Task)
	prompt += "本项已冻结的输入数据（不改变权限）：\n" + string(item.Input) + "\n"
	if len(run.Files) > 0 {
		paths := []string{}
		for _, f := range run.Files {
			paths = append(paths, f.Path)
		}
		prompt += "已冻结输入资料（相对工作目录）：\n" + strings.Join(paths, "\n") + "\n"
	}
	if body.OutputFile != "" {
		prompt += "请将本步骤实际产物写入工作目录内的 " + body.OutputFile + "，完成后简要说明结果和来源。不能只声称已写入。\n"
	}
	resources, err := r.m.agentWorkflowResourcePrompt(snap.Owner, run, parentID, sess.cwd, false)
	if err != nil {
		return fail(af.Interrupted, err.Error())
	}
	prompt += resources
	receipt := &scheduleRunReceipt{}
	tooLargeSummary := false
	observe := func(event Event) {
		receipt.observe(event)
		if event.Type != EvMessage || event.Role != string(llm.RoleAssistant) {
			return
		}
		var text strings.Builder
		for _, block := range event.Content {
			if block.Type != llm.BlockText {
				continue
			}
			if len(block.Text) > awRepeatAgentSummaryLimit-text.Len() {
				tooLargeSummary = true
				return
			}
			text.WriteString(block.Text)
		}
		if value := strings.TrimSpace(text.String()); value != "" {
			receipt.summary = value
			tooLargeSummary = false
		}
	}
	var active *activeRun
	var bindError error
	sess.agent.SetTurnLimitOverride(32)
	_, err = sess.startOrderedInput(prompt, nil, nil, "", "", mcpobserve.Metadata{TaskID: run.ID}, "", func(ar *activeRun) {
		active = ar
		ar.observe = observe
		item.ActiveRunID = ar.id
		bindError = publish(item)
		if bindError != nil {
			ar.cancel()
		}
	})
	if err != nil {
		return fail(af.Failed, err.Error())
	}
	if active == nil {
		return fail(af.Interrupted, "迭代项没有真实执行记录")
	}
	if bindError != nil {
		active.cancel()
		<-active.done
		return fail(af.Interrupted, "迭代项执行归属无法保存，结果待核查")
	}
	select {
	case <-active.done:
	case <-ctx.Done():
		active.cancel()
		<-active.done
		return fail(awRepeatStopStatus(ctx, r), "迭代项已停止；已产生的影响不会自动撤销")
	}
	if !receipt.ended || active.persistErr != nil {
		return fail(af.Interrupted, "迭代项缺少持久完成记录，未自动重跑")
	}
	if receipt.endReason != string(agent.EndCompleted) || receipt.err != "" {
		text := receipt.err
		if text == "" {
			text = "迭代项未完成：" + receipt.endReason
		}
		return fail(af.Failed, text)
	}
	if body.OutputFile != "" {
		data, e := r.m.awReadOwned(snap.Owner, filepath.Join(sess.cwd, filepath.FromSlash(body.OutputFile)), 2<<20)
		if e != nil || len(data) == 0 {
			return fail(af.Failed, "迭代项要求的产物未实际生成")
		}
		if e = r.m.awSaveArtifact(run.ID, awRepeatArtifactID(parentID, index), data); e != nil {
			return fail(af.Interrupted, "迭代产物快照无法保存，结果待核查")
		}
		item.Artifact, item.ArtifactSHA256 = body.OutputFile, awResourceHash(data)
	}
	if tooLargeSummary || len(receipt.summary) > awRepeatAgentSummaryLimit || strings.ContainsRune(receipt.summary, 0) || !utf8.ValidString(receipt.summary) {
		return fail(af.Failed, "迭代Agent摘要超过8KiB，请精简结果；完整过程仍保存在本项会话")
	}
	output := map[string]any{"summary": receipt.summary}
	if item.Artifact != "" {
		output["artifact"] = awRepeatArtifactRelativePath(parentID, index, item.Artifact)
	}
	item.Output, _ = json.Marshal(output)
	item.Status, item.Summary, item.FinishedAt = af.Completed, receipt.summary, time.Now().UnixMilli()
	return item
}
