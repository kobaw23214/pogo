package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/session"
)

// runAgentSchedule enters the same bound session runtime as the Agent chat.
// No ordinary CLI fallback or copied MCP credentials. Approval mode comes from
// the sealed task definition; old definitions retain standard review.
func (m *Manager) runAgentSchedule(ctx context.Context, job schedule.Job, logs schedule.LogPaths, mirror io.Writer) schedule.RunOutcome {
	fail := func(err error) schedule.RunOutcome { return schedule.RunOutcome{Err: err.Error(), ExitCode: 1} }
	m.mu.Lock()
	validOwner := m.scheduleOwnerValid
	m.mu.Unlock()
	if job.AgentID == "" || (job.Owner != nil && !job.Owner.Valid()) || (m.Isolation() && job.Owner == nil) {
		return fail(errors.New("任务账号或 Agent 绑定无效"))
	}
	if validOwner != nil {
		if !validOwner(job.Owner) {
			return fail(errors.New("任务账号已删除或不可用"))
		}
	} else {
		return fail(errors.New("任务运行时及账号校验尚未就绪"))
	}
	a, ok := m.studio.get(job.AgentID)
	if !ok || a.Status != "ready" {
		return fail(errors.New("Agent 已删除或暂停，本次未启动"))
	}
	if job.Model != "" || job.BudgetUSD != 0 {
		return fail(errors.New("Agent 任务不可绕过当前 Agent 模型或使用未支持的独立金额预算"))
	}
	base, target, err := m.scheduleWorkspace(job.Owner, job.Workdir, true)
	if err != nil {
		return fail(err)
	}
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return fail(err)
	}
	if err := schedule.ValidateAgentRunOptions(job); err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(ctx, schedule.DefaultJobTimeout)
	defer cancel()
	workspace := rel
	if filepath.ToSlash(rel) == "agents/"+a.ID {
		workspace = ""
	} // let the existing Studio path create its default workspace
	identity := session.BackgroundTaskIdentity{Source: "schedule", TaskID: "schedule:" + job.ID, SourceID: job.ID, AgentID: a.ID, OwnerID: sessionResultOwnerKey(job.Owner)}
	if binding, ok := ctx.Value(taskRunContextKey{}).(taskRunBinding); ok && binding.runtime != nil && binding.runtime.m == m {
		identity.RunID = binding.id
	}
	sess, err := m.createScheduleConversation(a, job.Owner, identity, workspace)
	if err != nil {
		return fail(err)
	}
	if _, _, err = m.scheduleWorkspace(job.Owner, sess.cwd, false); err != nil {
		return fail(err)
	}
	out := schedule.RunOutcome{SessionID: sess.ID, Model: sess.Model().Model, AgentRevision: a.Revision}
	failSession := func(err error) schedule.RunOutcome { out.Err = err.Error(); out.ExitCode = 1; return out }
	title := strings.TrimSpace(job.Title)
	if title == "" {
		title = "定时任务 · " + job.ID
	}
	_ = sess.SetInfo(&title, nil)
	if err := bindTaskSession(ctx, sess.ID); err != nil {
		return failSession(err)
	}
	m.schedMu.Lock()
	if m.schedSessions == nil {
		m.schedSessions = map[string]string{}
	}
	m.schedSessions[job.ID] = sess.ID
	m.schedMu.Unlock()
	defer func() { m.schedMu.Lock(); delete(m.schedSessions, job.ID); m.schedMu.Unlock() }()
	parts, err := m.materializeScheduleParts(job.Owner, sess.cwd, job.Parts)
	if err != nil {
		return failSession(fmt.Errorf("任务附件无法读取: %w", err))
	}
	prompt, files, err := normalizeMessageInput(job.Prompt, nil, parts, job.InputLayout)
	if err != nil {
		return failSession(err)
	}
	if slashCommand(prompt) != "" {
		return failSession(errors.New("控制命令不可作为定时任务"))
	}
	if validOwner != nil && !validOwner(job.Owner) {
		return failSession(errors.New("任务账号已不可用"))
	}
	if job.TurnLimitMode == schedule.TurnLimitUnlimited {
		sess.agent.SetUnlimitedTurns()
	} else {
		turns := job.MaxTurns
		if turns == 0 {
			turns = m.effectiveMaxTurns()
			if turns <= 0 || turns > schedule.MaxJobTurns {
				turns = schedule.MaxJobTurns
			}
		}
		sess.agent.SetTurnLimitOverride(turns)
	}
	mode := ModeStandard
	if job.ApprovalMode != "" {
		mode, _ = ParseMode(job.ApprovalMode)
	}
	sess.SetPolicy(mode, 0)

	receipt := &scheduleRunReceipt{}
	var original *activeRun
	runID, err := sess.startOrderedInput(prompt, files, parts, job.InputLayout, "", mcpobserve.Metadata{TaskID: job.ID}, "", func(run *activeRun) { original = run; run.observe = receipt.observe })
	if err != nil {
		return failSession(err)
	}
	if original == nil {
		return failSession(errors.New("任务没有获得执行记录"))
	}
	select {
	case <-original.done:
	case <-ctx.Done():
		// Cancel the original execution only. A later user run in this same result
		// conversation must never be cancelled by the schedule's deadline.
		original.cancel()
		<-original.done
		return failSession(fmt.Errorf("任务执行已取消或超时: %w", ctx.Err()))
	}
	// Normally the receipt observes this run's own event stream directly, before
	// any queued/user run can start. Preparation failures end before consume.
	if !receipt.ended {
		sess.hub.mu.Lock()
		events := append([]Event(nil), sess.hub.log...)
		sess.hub.mu.Unlock()
		observeScheduleWindow(receipt, events, runID)
	}
	out.EndReason, out.Err, out.Summary = receipt.endReason, receipt.err, receipt.summary
	out.Model = original.model
	if !receipt.ended {
		out.Err = "执行已结束但缺少完成记录"
	}
	if original.persistErr != nil {
		out.Err = "任务结果保存失败: " + original.persistErr.Error()
	}
	if out.EndReason != string(agent.EndCompleted) && out.Err == "" {
		out.Err = "任务未完成: " + out.EndReason
	}
	if out.Err != "" {
		out.ExitCode = 1
	}
	if err := writeAgentScheduleLog(logs, out, mirror); err != nil {
		return failSession(fmt.Errorf("任务日志保存失败: %w", err))
	}
	return out
}
func writeAgentScheduleLog(logs schedule.LogPaths, out schedule.RunOutcome, mirror io.Writer) error {
	if err := os.MkdirAll(filepath.Dir(logs.Out), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(logs.Out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	w := io.Writer(f)
	if mirror != nil {
		w = io.MultiWriter(f, mirror)
	}
	return json.NewEncoder(w).Encode(out)
}

// Written by the original run goroutine; read only after that run's done closes.
type scheduleRunReceipt struct {
	ended                   bool
	endReason, err, summary string
}

func (r *scheduleRunReceipt) observe(e Event) {
	if r.ended {
		return
	}
	if e.Type == EvMessage && e.Role == string(llm.RoleAssistant) {
		var b strings.Builder
		for _, block := range e.Content {
			if block.Type == llm.BlockText {
				b.WriteString(block.Text)
			}
		}
		if text := strings.TrimSpace(b.String()); text != "" {
			chars := []rune(text)
			if len(chars) > 2000 {
				chars = chars[:2000]
			}
			r.summary = string(chars)
		}
	}
	if e.Type == EvRunEnd {
		r.ended = true
		r.endReason = e.EndReason
		r.err = e.Error
	}
}
func observeScheduleWindow(receipt *scheduleRunReceipt, events []Event, runID string) {
	current := runID
	for _, e := range events {
		if e.Type == EvRunStart {
			current = e.RunID
		}
		if current != runID {
			continue
		}
		receipt.observe(e)
		if receipt.ended {
			return
		}
	}
}
