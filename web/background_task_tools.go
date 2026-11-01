package web

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/tools"
)

type backgroundTaskBackend struct {
	m                            *Manager
	s                            *Session
	ownerKey, agentID, workspace string
}

func (m *Manager) backgroundTaskBackend(s *Session) tools.BackgroundTaskBackend {
	if s.studioID == "" {
		return nil
	}
	return &backgroundTaskBackend{m: m, s: s, ownerKey: sessionResultOwnerKey(s.owner), agentID: s.studioID, workspace: s.cwd}
}

func (b *backgroundTaskBackend) access(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.s.mu.Lock()
	bound := !b.s.closing && b.s.studioID == b.agentID && b.s.cwd == b.workspace && sessionResultOwnerKey(b.s.owner) == b.ownerKey
	b.s.mu.Unlock()
	if !bound {
		return errors.New("后台任务会话绑定已失效")
	}
	a, ok := b.m.studio.get(b.agentID)
	if !ok || a.Status != "ready" || !b.m.studioNativeSelected(a, tools.BackgroundTaskName) {
		return errors.New("Agent 或后台任务工具已不可用")
	}
	b.m.mu.Lock()
	valid := b.m.scheduleOwnerValid
	b.m.mu.Unlock()
	if valid == nil || !valid(b.s.owner) {
		return errors.New("后台任务账号不可用或运行时尚未就绪")
	}
	_, _, err := b.m.scheduleWorkspace(b.s.owner, b.workspace, false)
	return err
}

// Both the HTTP editor and the model-facing tool use this same input boundary
// after the caller has resolved a trusted owner, Agent and conversation path.
func (m *Manager) prepareBoundScheduleInput(owner *identity.Scope, target string, a studioAgent, b *scheduleJobBody) error {
	prompt, _, err := normalizeMessageInput(b.Prompt, nil, b.Parts, b.InputLayout)
	if err != nil {
		return err
	}
	if slashCommand(prompt) != "" {
		return errors.New("控制命令不可作为定时任务")
	}
	parts := scheduleInvocationParts(b.Prompt, b.Parts)
	if _, _, err = normalizeMessageInput(b.Prompt, nil, parts, b.InputLayout); err != nil {
		return err
	}
	subset, _ := m.studioSelection(a)
	if subset == nil {
		subset = m.Skills()
	}
	candidate := &Session{mgr: m, cwd: target, owner: owner, studioID: a.ID, studioWeb: true, invocationSkills: subset}
	if err = candidate.checkInputSkillAccess(parts); err != nil {
		return err
	}
	parts, err = m.freezeScheduleParts(owner, target, parts)
	if err != nil {
		return err
	}
	b.Parts = inputref.Clone(parts)
	return nil
}

func (b *backgroundTaskBackend) Task(ctx context.Context, raw json.RawMessage) (tools.BackgroundTaskReceipt, error) {
	if err := (&tools.BackgroundTask{}).ValidateArgs(raw); err != nil {
		return tools.BackgroundTaskReceipt{}, err
	}
	if err := b.access(ctx); err != nil {
		return tools.BackgroundTaskReceipt{}, err
	}
	var a tools.BackgroundTaskRequest
	_ = json.Unmarshal(raw, &a)
	a.TaskID = strings.TrimPrefix(a.TaskID, a.Source+":")
	if a.Source == "sentinel" {
		return b.sentinelTask(ctx, a)
	}
	return b.scheduleTask(ctx, a, time.Now().UTC().Truncate(time.Millisecond))
}

func (b *backgroundTaskBackend) scheduleOwned(st *schedule.Store, id string) (schedule.Job, error) {
	j, err := st.Find(id)
	if err != nil || j.AgentID != b.agentID || sessionResultOwnerKey(j.Owner) != b.ownerKey || j.WorkflowID != "" {
		return schedule.Job{}, ErrNotFound
	}
	return j, nil
}

func (b *backgroundTaskBackend) scheduleReceipt(j schedule.Job, action string, now time.Time) tools.BackgroundTaskReceipt {
	r := tools.BackgroundTaskReceipt{Source: "schedule", TaskID: "schedule:" + j.ID, SourceID: j.ID, Registered: action != "remove", Enabled: j.Enabled && action != "remove", Verification: "registration_only", ExpiresAt: j.ExpiresAt, Status: "paused"}
	if action == "remove" {
		r.Status = "removed"
	} else if j.ExpiresAt != "" {
		if end, err := time.Parse(time.RFC3339, j.ExpiresAt); err == nil && !end.After(now) {
			r.Status = "expired"
		}
	}
	if r.Enabled && r.Status != "expired" {
		r.Status = "scheduled"
		if n := nextFire(j, now); n != nil {
			end, err := time.Parse(time.RFC3339, j.ExpiresAt)
			if j.ExpiresAt == "" || err == nil && n.Before(end) {
				r.NextRun = n.UTC().Format(time.RFC3339Nano)
			}
		}
	}
	view := (&Server{mgr: b.m}).scheduleJobViews([]schedule.Job{j})[0]
	// Keep the shared view consistent with the actual exclusive deadline.
	if r.NextRun == "" {
		view.NextRun = nil
	}
	r.Result = map[string]any{"job": view, "backend": "internal", "timing": "every starts one full interval after registration; no new execution starts at or after expires_at; already running work is not rolled back"}
	// Give the model the exclusive-boundary arithmetic rather than asking it
	// to guess whether the final interval at the cutoff will execute. These
	// are planned triggers, not a guarantee of successful future runs.
	if j.Kind == schedule.KindEvery && j.EverySec > 0 && r.NextRun != "" && j.ExpiresAt != "" {
		first, firstErr := time.Parse(time.RFC3339Nano, r.NextRun)
		end, endErr := time.Parse(time.RFC3339Nano, j.ExpiresAt)
		if firstErr == nil && endErr == nil && first.Before(end) {
			interval := time.Duration(j.EverySec) * time.Second
			if interval > 0 {
				r.Result.(map[string]any)["remaining_planned_triggers_before_deadline"] = int64((end.Sub(first)-time.Nanosecond)/interval) + 1
			}
		}
	}
	return r
}

func (b *backgroundTaskBackend) scheduleTask(ctx context.Context, a tools.BackgroundTaskRequest, now time.Time) (tools.BackgroundTaskReceipt, error) {
	st := schedule.NewStore(schedule.DefaultDir())
	if a.Action == "list" {
		jobs, err := st.Load()
		if err != nil {
			return tools.BackgroundTaskReceipt{}, err
		}
		out := []tools.BackgroundTaskReceipt{}
		for _, j := range jobs {
			if j.AgentID == b.agentID && sessionResultOwnerKey(j.Owner) == b.ownerKey && j.WorkflowID == "" {
				out = append(out, b.scheduleReceipt(j, "status", now))
			}
		}
		return tools.BackgroundTaskReceipt{Source: "schedule", Verification: "registration_only", Result: out}, nil
	}
	if a.Action == "create" {
		deadline, err := tools.ResolveTaskDeadline(a.ExpiresAt, a.StopAfterSeconds, now)
		if err != nil {
			return tools.BackgroundTaskReceipt{}, err
		}
		turns := b.m.boundBackgroundMaxTurns(a.MaxTurns)
		approval := string(ModeStandard)
		if b.s.Policy().State().Mode == string(ModeStrict) {
			approval = string(ModeStrict)
		}
		limited := schedule.TurnLimitLimited
		body := scheduleJobBody{AgentID: b.agentID, Title: a.Title, Prompt: a.Task, Kind: a.Kind, EverySec: a.EverySeconds, At: a.At, Cron: a.Cron, Timezone: a.Timezone, Workdir: b.workspace, MaxTurns: turns, TurnLimitMode: &limited, ApprovalMode: &approval}
		if deadline != 0 {
			body.ExpiresAt = time.UnixMilli(deadline).UTC().Format(time.RFC3339Nano)
		}
		_, target, err := b.m.scheduleWorkspace(b.s.owner, b.workspace, false)
		if err != nil {
			return tools.BackgroundTaskReceipt{}, err
		}
		body.Workdir = target
		if err = body.normalizeAgentRunOptions(nil); err != nil {
			return tools.BackgroundTaskReceipt{}, err
		}
		agent, ok := b.m.studio.get(b.agentID)
		if !ok {
			return tools.BackgroundTaskReceipt{}, ErrNotFound
		}
		if err = b.m.prepareBoundScheduleInput(b.s.owner, target, agent, &body); err != nil {
			return tools.BackgroundTaskReceipt{}, err
		}
		job, next, err := body.build(now)
		if err != nil {
			return tools.BackgroundTaskReceipt{}, err
		}
		if deadline != 0 && (next == nil || !next.Before(time.UnixMilli(deadline))) {
			return tools.BackgroundTaskReceipt{}, errors.New("截止时间之前没有可执行的计划时刻；截止时刻本身不启动新执行")
		}
		job.Owner = b.s.owner
		job.Enabled = a.Enabled != nil && *a.Enabled
		if err = b.access(ctx); err != nil {
			return tools.BackgroundTaskReceipt{}, err
		}
		if err = schedule.AddJob(st, schedule.Internal(), job); err != nil {
			return tools.BackgroundTaskReceipt{}, err
		}
		job, err = st.Find(job.ID)
		if err != nil {
			return tools.BackgroundTaskReceipt{}, err
		}
		return b.scheduleReceipt(job, a.Action, now), nil
	}
	job, err := b.scheduleOwned(st, a.TaskID)
	if err != nil {
		return tools.BackgroundTaskReceipt{}, err
	}
	switch a.Action {
	case "status":
		r := b.scheduleReceipt(job, a.Action, now)
		runs, err := st.Runs(job.ID, 50)
		if err != nil {
			return tools.BackgroundTaskReceipt{}, err
		}
		r.Result.(map[string]any)["runs"] = scheduleRunViews(runs, b.agentID)
		return r, nil
	case "pause":
		err = schedule.SetEnabled(st, job.ID, false)
	case "resume":
		if job.DefHash != "" && schedule.DefinitionHash(job) != job.DefHash {
			return tools.BackgroundTaskReceipt{}, errors.New("任务定义已变化，不能恢复未经确认的内容")
		}
		if job.ExpiresAt != "" {
			end, e := time.Parse(time.RFC3339, job.ExpiresAt)
			if e != nil || !end.After(now) {
				return tools.BackgroundTaskReceipt{}, errors.New("后台任务已到截止时间；恢复不会延长原截止时间")
			}
		}
		if _, _, err = b.m.scheduleWorkspace(job.Owner, job.Workdir, false); err != nil {
			return tools.BackgroundTaskReceipt{}, err
		}
		err = schedule.SetEnabled(st, job.ID, true)
	case "remove":
		if b.m.SchedRunning()[job.ID] {
			return tools.BackgroundTaskReceipt{}, schedule.ErrRunning
		}
		if b.m.taskRuns != nil {
			db, e := b.m.taskRuns.database(false)
			if e != nil {
				return tools.BackgroundTaskReceipt{}, e
			}
			if db != nil {
				rows, e := db.List(taskqueue.Filter{OwnerID: b.ownerKey, AgentID: b.agentID, TaskID: "schedule:" + job.ID, ActiveOnly: true, Limit: 1})
				if e != nil {
					return tools.BackgroundTaskReceipt{}, e
				}
				if len(rows) != 0 {
					return tools.BackgroundTaskReceipt{}, schedule.ErrRunning
				}
			}
		}
		_, err = schedule.RemoveJob(st, schedule.Internal(), job.ID)
		if err == nil {
			return b.scheduleReceipt(job, a.Action, now), nil
		}
	default:
		return tools.BackgroundTaskReceipt{}, errors.New("unsupported background task action")
	}
	if err != nil {
		return tools.BackgroundTaskReceipt{}, err
	}
	job, err = st.Find(job.ID)
	if err != nil {
		return tools.BackgroundTaskReceipt{}, err
	}
	return b.scheduleReceipt(job, a.Action, now), nil
}

// New registrations may narrow the host's positive limit, never raise it.
// Unlimited/unspecified host settings still receive a finite background cap.
// Existing saved definitions are not rewritten when the host setting changes.
func (m *Manager) boundBackgroundMaxTurns(requested int) int {
	turns := 32
	if host := m.effectiveMaxTurns(); host > 0 {
		turns = min(turns, host)
	}
	if requested > 0 {
		turns = min(turns, requested)
	}
	return turns
}

func (b *backgroundTaskBackend) sentinelTask(ctx context.Context, a tools.BackgroundTaskRequest) (tools.BackgroundTaskReceipt, error) {
	backend := b.m.sentinelBackend(b.s)
	if backend == nil {
		return tools.BackgroundTaskReceipt{}, errors.New("哨兵运行时不可用")
	}
	if a.Action == "prepare" || a.Action == "check" {
		return b.sentinelScriptTask(ctx, a, backend)
	}
	request := tools.WatchRequest{Action: a.Action, WatchID: a.TaskID, Title: a.Title, Task: a.Task, Mode: a.Mode, ScriptPath: a.ScriptPath, Args: a.Args, IntervalSeconds: a.IntervalSeconds, TimeoutSeconds: a.TimeoutSeconds, DebounceMS: a.DebounceMS, MaxTurns: a.MaxTurns, Network: a.Network, Enabled: a.Enabled, ExpiresAt: a.ExpiresAt, StopAfterSeconds: a.StopAfterSeconds}
	raw, err := json.Marshal(request)
	if err != nil {
		return tools.BackgroundTaskReceipt{}, err
	}
	if err = (&tools.Watch{}).ValidateArgs(raw); err != nil {
		return tools.BackgroundTaskReceipt{}, err
	}
	var retryWatch *sentinel.Watch
	if a.Action == "retry" {
		// Capture the existing owned registration before queueing. Retrying a
		// batch neither registers a new watch nor enables a paused collector.
		statusRaw, _ := json.Marshal(tools.WatchRequest{Action: "status", WatchID: a.TaskID})
		status, err := backend.Watch(ctx, statusRaw)
		if err != nil {
			return tools.BackgroundTaskReceipt{}, err
		}
		value, ok := status.(map[string]any)
		if !ok {
			return tools.BackgroundTaskReceipt{}, errors.New("哨兵状态回执不完整")
		}
		watch, ok := value["watch"].(sentinel.Watch)
		if !ok || watch.Spec.ID != a.TaskID {
			return tools.BackgroundTaskReceipt{}, errors.New("哨兵状态归属不一致")
		}
		retryWatch = &watch
	}
	v, err := backend.Watch(ctx, raw)
	if err != nil {
		return tools.BackgroundTaskReceipt{}, err
	}
	if retryWatch != nil {
		r := backgroundSentinelReceipt(*retryWatch, "retry")
		r.Status, r.Verification, r.Result = "retry_queued", "queued", v
		return r, nil
	}
	r := tools.BackgroundTaskReceipt{Source: "sentinel", Verification: "registration_only", Result: v}
	if watches, ok := v.([]sentinel.Watch); ok {
		items := make([]tools.BackgroundTaskReceipt, 0, len(watches))
		for _, item := range watches {
			items = append(items, backgroundSentinelReceipt(item, "status"))
		}
		r.Result = items
		return r, nil
	}
	var w sentinel.Watch
	switch value := v.(type) {
	case sentinel.Watch:
		w = value
	case map[string]any:
		if saved, ok := value["watch"].(sentinel.Watch); ok {
			w = saved
		}
	}
	if w.Spec.ID != "" {
		r = backgroundSentinelReceipt(w, a.Action)
		if status, ok := v.(map[string]any); ok {
			status["watch"] = r.Result
			r.Result = status
		}
	} else if a.Action == "remove" {
		r.TaskID, r.SourceID, r.Status = "sentinel:"+a.TaskID, a.TaskID, "removed"
	}
	return r, nil
}

// Preparing and checking are foreground, bounded setup operations. Neither
// constitutes registration or a background handoff, even for an existing task.
func (b *backgroundTaskBackend) sentinelScriptTask(ctx context.Context, a tools.BackgroundTaskRequest, backend tools.WatchBackend) (tools.BackgroundTaskReceipt, error) {
	r := tools.BackgroundTaskReceipt{Source: "sentinel", Verification: "not_run", Status: "prepared"}
	if a.Action == "prepare" {
		req := tools.WatchRequest{Action: "prepare", ScriptPath: a.ScriptPath, Mode: a.Mode}
		raw, err := json.Marshal(req)
		if err != nil {
			return tools.BackgroundTaskReceipt{}, err
		}
		if err = (&tools.Watch{}).ValidateArgs(raw); err != nil {
			return tools.BackgroundTaskReceipt{}, err
		}
		result, err := backend.Watch(ctx, raw)
		if err != nil {
			return tools.BackgroundTaskReceipt{}, err
		}
		r.Result = result
		return r, nil
	}

	// Use exactly the legacy check's contract, including the prohibition on
	// overriding a saved task's script, network grant, state or cursor.
	req := tools.WatchTestRequest{WatchID: a.TaskID, ScriptPath: a.ScriptPath, Mode: a.Mode, Args: a.Args, TimeoutSeconds: a.TimeoutSeconds, Network: a.Network}
	raw, err := json.Marshal(req)
	if err != nil {
		return tools.BackgroundTaskReceipt{}, err
	}
	if err = (&tools.WatchTest{}).ValidateArgs(raw); err != nil {
		return tools.BackgroundTaskReceipt{}, err
	}
	scriptBackend, ok := backend.(*sentinelToolBackend)
	if !ok {
		return tools.BackgroundTaskReceipt{}, errors.New("哨兵脚本检查不可用")
	}
	result, err := scriptBackend.checkScriptFor(ctx, raw, tools.BackgroundTaskName)
	if a.TaskID != "" && (err == nil || errors.Is(err, errSentinelTestNoFrame)) {
		// This identifies the owned target, not a registration made by check.
		r.TaskID, r.SourceID = "sentinel:"+a.TaskID, a.TaskID
	}
	if errors.Is(err, errSentinelTestNoFrame) {
		r.Status, r.Verification = "unverified", "unverified"
		r.Result = map[string]any{
			"test_only": true, "script_execution_attempted": true, "protocol_frame_verified": false,
			"registered": false, "enabled": false, "cursor_committed": false, "events_enqueued": false, "agent_woken": false,
			"business_conditions_verified": false, "stream_ack_cycle_verified": false,
			"verification_scope": "no protocol frame received within the bounded stream check; source and business readiness remain unverified",
			"reason":             err.Error(),
		}
		return r, nil
	}
	if err != nil {
		return tools.BackgroundTaskReceipt{}, err
	}
	checked, ok := result.(map[string]any)
	if !ok {
		return tools.BackgroundTaskReceipt{}, errors.New("哨兵脚本检查回执无效")
	}
	// Copy the shared runner's result rather than changing the legacy receipt.
	view := make(map[string]any, len(checked)+7)
	for key, value := range checked {
		view[key] = value
	}
	frame, ok := checked["frame"].(*sentinel.Frame)
	if !ok || frame == nil {
		return tools.BackgroundTaskReceipt{}, errors.New("哨兵脚本检查未返回协议帧")
	}
	view["script_executed"], view["protocol_frame_verified"] = true, true
	view["event_count"] = len(frame.Events)
	view["registered"], view["enabled"], view["events_enqueued"] = false, false, false
	r.Status, r.Verification, r.Result = "checked", "protocol_frame", view
	return r, nil
}

func backgroundSentinelReceipt(w sentinel.Watch, action string) tools.BackgroundTaskReceipt {
	w.Spec.Owner, w.Spec.OwnerID = nil, ""
	r := tools.BackgroundTaskReceipt{Source: "sentinel", TaskID: "sentinel:" + w.Spec.ID, SourceID: w.Spec.ID, Registered: action != "remove", Enabled: w.Spec.Enabled && action != "remove", Status: w.Status, Verification: "registration_only", Result: w}
	if w.NextAt > 0 {
		r.NextRun = time.UnixMilli(w.NextAt).UTC().Format(time.RFC3339Nano)
	}
	if w.Spec.ExpiresAt > 0 {
		r.ExpiresAt = time.UnixMilli(w.Spec.ExpiresAt).UTC().Format(time.RFC3339Nano)
	}
	return r
}
