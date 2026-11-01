package web

// One persistent admission ledger for bounded background executions. Source
// clocks and collectors propose work; only this dispatcher starts it. Source
// stores remain authoritative for definitions, observed cursors and DAG nodes.
import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/workflow"
)

const taskRunConcurrency = 2

type taskExecution struct {
	run    taskqueue.Run
	cancel context.CancelFunc
}

type taskRunRuntime struct {
	m                                 *Manager
	ctx                               context.Context
	stop                              context.CancelFunc
	mu                                sync.Mutex
	admitMu                           sync.Mutex
	store                             *taskqueue.Store
	lease                             *os.File
	err                               error
	active                            map[string]taskExecution
	wakeup                            chan struct{}
	done                              chan struct{}
	wg                                sync.WaitGroup
	workflowAuthorize                 func(taskqueue.WorkflowRunMeta) bool
	workflowApprovalResolver          func(workflow.Definition, taskqueue.WorkflowRunMeta, map[string][]string, map[string]taskqueue.WorkflowApprovalAssignment) (map[string]taskqueue.WorkflowApprovalAssignment, error)
	workflowApprovalDecisionAuthorize func(workflow.RunRecord, workflow.HumanDecision) error
}

func newTaskRunRuntime(m *Manager) *taskRunRuntime {
	ctx, stop := context.WithCancel(context.Background())
	return &taskRunRuntime{m: m, ctx: ctx, stop: stop, active: map[string]taskExecution{}, wakeup: make(chan struct{}, 1), done: make(chan struct{})}
}

func (r *taskRunRuntime) database(create bool) (*taskqueue.Store, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx.Err() != nil {
		return nil, errors.New("后台任务运行时已停止")
	}
	if r.store != nil || r.err != nil {
		return r.store, r.err
	}
	dir := filepath.Join(r.m.cfg.SessionDir, "task-runs")
	path := filepath.Join(dir, "runs.db")
	if !create {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lease, err := lockSentinelRuntime(filepath.Join(dir, "controller.lock"))
	if err != nil {
		r.err = err
		return nil, err
	}
	db, err := taskqueue.Open(path)
	if err == nil {
		err = db.Recover()
		if err == nil {
			err = db.AWRecover()
		}
	}
	if err != nil {
		if db != nil {
			_ = db.Close()
		}
		_ = lease.Close()
		r.err = err
		return nil, err
	}
	r.store, r.lease = db, lease
	return db, nil
}

func (r *taskRunRuntime) wake() {
	select {
	case r.wakeup <- struct{}{}:
	default:
	}
}

func (r *taskRunRuntime) loop() {
	defer r.m.wg.Done()
	defer close(r.done)
	defer func() {
		r.stop()
		r.mu.Lock()
		for _, item := range r.active {
			item.cancel()
		}
		r.mu.Unlock()
		r.wg.Wait()
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.store != nil {
			_ = r.store.Close()
		}
		if r.lease != nil {
			_ = r.lease.Close()
		}
	}()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-r.m.stop:
			return
		case <-tick.C:
		case <-r.wakeup:
		}
		r.pump()
	}
}

func (r *taskRunRuntime) pump() {
	if r.ctx.Err() != nil {
		return
	}
	if active, _ := r.m.releaseMaintenance(); active {
		return
	}
	db, err := r.database(false)
	if err != nil || db == nil {
		return
	}
	rows, err := db.List(taskqueue.Filter{ActiveOnly: true})
	if err != nil {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CreatedAt < rows[j].CreatedAt })
	for _, row := range rows {
		if row.Status != "queued" || row.DueAt > nowMS() || r.ctx.Err() != nil {
			continue
		}
		if active, _ := r.m.releaseMaintenance(); active {
			return
		}
		// The server binds owner checks after Manager construction. Persistent
		// requests wait for that binding instead of failing during startup.
		if row.AgentID != "" || row.Source == "workflow" {
			r.m.mu.Lock()
			ready := r.m.scheduleOwnerValid != nil
			r.m.mu.Unlock()
			if !ready {
				continue
			}
		}
		if row.Source == "sentinel" && !r.sentinelReady(row) {
			continue
		}
		r.mu.Lock()
		if r.ctx.Err() != nil {
			r.mu.Unlock()
			break
		}
		if active, _ := r.m.releaseMaintenance(); active {
			r.mu.Unlock()
			return
		}
		if len(r.active) >= taskRunConcurrency {
			r.mu.Unlock()
			break
		}
		claimed, e := db.Claim(row.ID, taskRunConcurrency)
		if e != nil {
			r.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(r.ctx)
		r.active[claimed.ID] = taskExecution{run: claimed, cancel: cancel}
		r.wg.Add(1)
		r.mu.Unlock()
		go r.execute(ctx, db, claimed)
	}
}

type taskRunContextKey struct{}
type taskRunBinding struct {
	runtime  *taskRunRuntime
	id       string
	revision int64
}

func bindTaskSession(ctx context.Context, sessionID string) error {
	b, ok := ctx.Value(taskRunContextKey{}).(taskRunBinding)
	if !ok {
		return nil
	}
	db, err := b.runtime.database(false)
	if err != nil {
		return err
	}
	if db == nil {
		return errors.New("后台执行凭证不可用")
	}
	_, err = db.AttachSession(b.id, b.revision, sessionID)
	return err
}

func (r *taskRunRuntime) execute(ctx context.Context, db *taskqueue.Store, run taskqueue.Run) {
	defer func() {
		r.mu.Lock()
		item := r.active[run.ID]
		delete(r.active, run.ID)
		r.mu.Unlock()
		if item.cancel != nil {
			item.cancel()
		}
		r.wg.Done()
		r.wake()
	}()
	ctx = context.WithValue(ctx, taskRunContextKey{}, taskRunBinding{r, run.ID, run.Revision})
	var outcome taskqueue.Outcome
	maintenance, _ := r.m.releaseMaintenance()
	switch {
	case ctx.Err() != nil:
		outcome = taskqueue.Outcome{Status: "interrupted", Error: "执行启动前已取消"}
	case maintenance:
		// The marker may have appeared after Claim but before this worker
		// started. Keep that request queued rather than manufacturing an
		// interrupted/successful execution or calling its source executor.
		outcome = taskqueue.Outcome{EndReason: "not_started"}
	case run.Source == "agent_workflow":
		outcome = r.executeAgentWorkflow(ctx, db, run)
	case run.Source == "workflow":
		outcome = r.executeWorkflow(ctx, run)
	case run.Source == "schedule":
		outcome = r.executeSchedule(ctx, run)
	case run.Source == "sentinel":
		outcome = r.executeSentinel(ctx, run)
	default:
		outcome = taskqueue.Outcome{Status: "failed", Error: "不支持的后台任务来源"}
	}
	if outcome.EndReason == "not_started" {
		outcome.Status = "interrupted"
		if ctx.Err() == nil && r.ctx.Err() == nil {
			if _, err := db.Release(run.ID, run.Revision, time.Now().Add(time.Second).UnixMilli()); err == nil {
				return
			}
			if current, err := db.Get(run.ID); err == nil && current.CancelRequested {
				outcome = taskqueue.Outcome{Status: "cancelled", Summary: "用户取消了尚未开始的执行"}
			} else {
				outcome.Error = "执行尚未开始，但延期写入失败，请核查"
			}
		}
	}
	if r.ctx.Err() != nil && outcome.Status != "completed" {
		outcome.Status = "interrupted"
		outcome.Error = "服务停止；执行结果待核查，未自动重跑"
	} else if ctx.Err() != nil && outcome.Status != "completed" {
		outcome.Status = "cancelled"
		if outcome.Error == "" {
			outcome.Error = "本次执行已停止；此前可能已产生的外部影响未被撤销"
		}
	}
	if _, err := db.Finish(run.ID, run.Revision, outcome); err != nil {
		// A failed completion write must remain visible, never converted to a
		// success or followed by an automatic second model invocation.
		r.mu.Lock()
		r.err = errors.New("后台执行结果写入失败，请核查运行记录")
		r.mu.Unlock()
	}
}

type scheduleTaskPayload struct {
	Directory string `json:"directory"`
}

func (r *taskRunRuntime) enqueueSchedule(job schedule.Job, trigger string, fire time.Time) (taskqueue.Run, bool, error) {
	r.admitMu.Lock()
	defer r.admitMu.Unlock()
	r.m.mu.Lock()
	closed := r.m.closed
	r.m.mu.Unlock()
	if closed || r.ctx.Err() != nil {
		return taskqueue.Run{}, false, errors.New("后台任务运行时已停止")
	}
	db, err := r.database(true)
	if err != nil {
		return taskqueue.Run{}, false, err
	}
	taskID := "schedule:" + job.ID
	prior, err := db.List(taskqueue.Filter{TaskID: taskID})
	if err != nil {
		return taskqueue.Run{}, false, err
	}
	key := "manual:" + rand.Text()
	if trigger == schedule.TriggerScheduled {
		key = fmt.Sprintf("scheduled:%s:%d", schedule.DefinitionHash(job), fire.UnixNano())
	}
	workflowGuard := newWorkflowScheduleGuard(r, db)
	for _, row := range prior {
		if row.TriggerID == key {
			if trigger == schedule.TriggerScheduled {
				err = r.advanceScheduleAdmission(job, row)
			}
			return row, false, err
		}
		if blocked, handled, err := workflowGuard.blocked(row); err != nil {
			return taskqueue.Run{}, false, err
		} else if handled {
			if blocked {
				return taskqueue.Run{}, false, schedule.ErrRunning
			}
			continue
		}
		if row.WorkflowID != "" && row.SessionID != "" && r.m.wfruns != nil {
			if native, ok := r.m.wfruns.snapshot(row.SessionID); ok && native.Status == "working" {
				// A workflow suspended on a durable human node released its
				// execution slot, but its logical task is still unfinished.
				// Do not create duplicate approval requests on each timer tick.
				return taskqueue.Run{}, false, schedule.ErrRunning
			}
		}
		if row.Status == "queued" || row.Status == "running" {
			return taskqueue.Run{}, false, schedule.ErrRunning
		}
		// An ambiguous execution must be reviewed through explicit retry,
		// not bypassed by another clock tick or a run-now button.
		if row.Status == "interrupted" {
			retried := false
			for _, later := range prior {
				retried = retried || later.RetryOf == row.ID
			}
			if !retried {
				return taskqueue.Run{}, false, schedule.ErrRunning
			}
		}
	}
	title := job.Title
	if title == "" {
		title = job.ID
	}
	payload, _ := json.Marshal(scheduleTaskPayload{schedule.DefaultDir()})
	row, created, err := db.Enqueue(taskqueue.Request{TaskID: taskID, Source: "schedule", SourceID: job.ID, Trigger: trigger, TriggerID: key, DefinitionHash: schedule.DefinitionHash(job), OwnerID: sessionResultOwnerKey(job.Owner), AgentID: job.AgentID, WorkflowID: job.WorkflowID, Title: title, DueAt: fire.UnixMilli(), Payload: payload})
	if err == nil && trigger == schedule.TriggerScheduled {
		err = r.advanceScheduleAdmission(job, row)
	}
	if err == nil {
		r.wake()
	}
	return row, created, err
}

func (r *taskRunRuntime) advanceScheduleAdmission(job schedule.Job, run taskqueue.Run) error {
	// Repair this watermark on an idempotent replay too: a crash may occur
	// after the queue commits but before jobs.json is updated.
	return schedule.NewStore(schedule.DefaultDir()).Update(job.ID, func(current *schedule.Job) {
		if schedule.DefinitionHash(*current) == run.DefinitionHash {
			at := time.UnixMilli(run.CreatedAt)
			if at.After(current.LastQueuedAt) {
				current.LastQueuedAt = at
			}
		}
	})
}

func (r *taskRunRuntime) executeSchedule(ctx context.Context, run taskqueue.Run) taskqueue.Outcome {
	fail := func(message string) taskqueue.Outcome { return taskqueue.Outcome{Status: "failed", Error: message} }
	var payload scheduleTaskPayload
	if json.Unmarshal(run.Payload, &payload) != nil || filepath.Clean(payload.Directory) != filepath.Clean(schedule.DefaultDir()) {
		return fail("定时任务存储绑定已改变，本次未执行")
	}
	st := schedule.NewStore(payload.Directory)
	job, err := st.Find(run.SourceID)
	if err != nil || sessionResultOwnerKey(job.Owner) != run.OwnerID || job.AgentID != run.AgentID || job.WorkflowID != run.WorkflowID || schedule.DefinitionHash(job) != run.DefinitionHash {
		return taskqueue.Outcome{Status: "cancelled", Error: "任务定义已修改、删除或权限绑定变化，本次未执行"}
	}
	if !job.Enabled && (run.Trigger == schedule.TriggerScheduled || job.AgentID == "") {
		return taskqueue.Outcome{Status: "cancelled", Summary: "任务已暂停，本次未开始"}
	}
	if !r.m.SchedStart(job.ID) {
		return fail("同一任务已有执行，本次未重复启动")
	}
	defer r.m.SchedDone(job.ID)
	trigger := run.Trigger
	if trigger == "retry" {
		trigger = schedule.TriggerManual
	}
	if active, _ := r.m.releaseMaintenance(); active {
		// No native RunOnce bookkeeping has begun yet. After entering that
		// runner, preparation is part of the already-started wrapper and must
		// be drained by the release precheck, never relabelled as unstarted.
		return taskqueue.Outcome{EndReason: "not_started"}
	}
	var outcome schedule.RunOutcome
	result, err := st.RunOnceSealed(ctx, job.ID, trigger, run.DefinitionHash, nil, func(ctx context.Context, current schedule.Job, logs schedule.LogPaths, mirror io.Writer) schedule.RunOutcome {
		outcome = r.m.scheduleJobRunner(ctx, current, logs, mirror)
		return outcome
	})
	if err != nil {
		return fail(err.Error())
	}
	status := "completed"
	if result.Status != schedule.StatusOK {
		status = "failed"
	}
	return taskqueue.Outcome{Status: status, SessionID: result.SessionID, Summary: outcome.Summary, Error: outcome.Err, EndReason: result.EndReason}
}

func (r *taskRunRuntime) cancel(id string, revision int64) (taskqueue.Run, error) {
	db, err := r.database(false)
	if err != nil {
		return taskqueue.Run{}, err
	}
	if db == nil {
		return taskqueue.Run{}, taskqueue.ErrNotFound
	}
	run, err := db.Cancel(id, revision)
	if err != nil {
		return run, err
	}
	r.mu.Lock()
	active := r.active[id]
	r.mu.Unlock()
	if active.cancel != nil && run.CancelRequested {
		active.cancel()
	}
	if run.Source == "sentinel" && run.Status == "cancelled" {
		r.cancelSentinelQueued(run)
	}
	r.wake()
	return run, nil
}

func (r *taskRunRuntime) retry(id string, revision int64) (taskqueue.Run, error) {
	db, err := r.database(false)
	if err != nil {
		return taskqueue.Run{}, err
	}
	if db == nil {
		return taskqueue.Run{}, taskqueue.ErrNotFound
	}
	run, err := db.Get(id)
	if err != nil || run.Revision != revision {
		if err == nil {
			err = taskqueue.ErrConflict
		}
		return run, err
	}
	if run.Source == "sentinel" {
		return r.retrySentinel(run)
	}
	next, err := db.Retry(id, revision)
	if err == nil {
		r.wake()
	}
	return next, err
}
