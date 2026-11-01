package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/sentinelexec"
	"github.com/yosukeno/pogo/session"
)

// One server owns the collectors. A chat closing never owns their lifetime.
// The runtime is source-agnostic: only the saved script understands its source.
type sentinelRuntime struct {
	m          *Manager
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	store      *sentinel.Store
	lease      *os.File
	err        error
	workers    map[string]*sentinelWorker
	runs       map[string]bool
	runCancels map[string]context.CancelFunc
	wg         sync.WaitGroup
	runner     func(context.Context, sentinelexec.Config, func(sentinel.Frame) error) error
	dispatcher *taskRunRuntime
	clock      atomic.Int64 // host test seam; zero uses wall time; never script supplied
}

func (r *sentinelRuntime) timeNow() int64 {
	if at := r.clock.Load(); at > 0 {
		return at
	}
	return nowMS()
}

// expireDeadline is independent of conversation lifetime and maintenance.
// Retiring the generation prevents an in-flight source from committing after
// the saved deadline; queued and active work remains available for review.
func (r *sentinelRuntime) expireDeadline(db *sentinel.Store, w sentinel.Watch) bool {
	at := r.timeNow()
	if !w.Spec.Expired(at) {
		return false
	}
	current, _, err := db.ExpireAt(w.Spec.ID, w.Spec.Generation, at)
	if errors.Is(err, sentinel.ErrStaleGeneration) && !current.Spec.Expired(at) {
		return false
	}
	r.mu.Lock()
	if worker := r.workers[w.Spec.ID]; worker != nil {
		worker.cancel()
	}
	if cancel := r.runCancels[w.Spec.ID]; cancel != nil {
		cancel()
	}
	r.mu.Unlock()
	return true
}

type sentinelWorker struct {
	generation int64
	mode       string
	cancel     context.CancelFunc
	done       chan struct{}
}

func newSentinelRuntime(m *Manager) *sentinelRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	return &sentinelRuntime{m: m, ctx: ctx, cancel: cancel, workers: map[string]*sentinelWorker{}, runs: map[string]bool{}, runCancels: map[string]context.CancelFunc{}, runner: (&sentinelexec.Runner{}).Run, dispatcher: m.taskRuns}
}
func (r *sentinelRuntime) database(create bool) (*sentinel.Store, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx.Err() != nil {
		return nil, errors.New("哨兵运行时已停止")
	}
	if r.store != nil {
		return r.store, nil
	}
	if r.err != nil {
		return nil, r.err
	}
	p := filepath.Join(r.m.cfg.SessionDir, "sentinels", "events.db")
	if !create {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return nil, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return nil, err
	}
	lease, err := lockSentinelRuntime(filepath.Join(filepath.Dir(p), "controller.lock"))
	if err != nil {
		r.err = err
		return nil, err
	}
	s, err := sentinel.Open(p)
	if err == nil {
		err = s.Recover()
	}
	if err != nil {
		if s != nil {
			_ = s.Close()
		}
		lease.Close()
		r.err = err
		return nil, err
	}
	r.store = s
	r.lease = lease
	return s, nil
}
func (r *sentinelRuntime) loop() {
	defer r.m.wg.Done()
	defer func() {
		r.cancel()
		r.mu.Lock()
		for _, w := range r.workers {
			w.cancel()
		}
		r.mu.Unlock()
		r.wg.Wait()
		if r.dispatcher != nil {
			r.dispatcher.stop()
			<-r.dispatcher.done
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.store != nil {
			_ = r.store.Close()
		}
		if r.lease != nil {
			_ = r.lease.Close()
		}
	}()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.m.stop:
			return
		case <-r.ctx.Done():
			return
		case <-t.C:
			r.tick()
		}
	}
}
func (r *sentinelRuntime) authorized(s sentinel.Spec) (*identity.Scope, error) {
	var owner *identity.Scope
	if len(s.Owner) > 0 && string(s.Owner) != "null" {
		owner = &identity.Scope{}
		if json.Unmarshal(s.Owner, owner) != nil || !owner.Valid() {
			return nil, errors.New("哨兵账号绑定无效")
		}
	}
	if sessionResultOwnerKey(owner) != s.OwnerID {
		return nil, errors.New("哨兵账号绑定不一致")
	}
	r.m.mu.Lock()
	valid := r.m.scheduleOwnerValid
	r.m.mu.Unlock()
	if valid == nil || !valid(owner) {
		return nil, errors.New("哨兵账号已不可用或运行时尚未就绪")
	}
	a, ok := r.m.studio.get(s.AgentID)
	if !ok || a.Status != "ready" {
		return nil, errors.New("哨兵所属 Agent 已暂停或删除")
	}
	if !sentinelToolsSelected(a.Tools) {
		return nil, errors.New("Agent 的哨兵工具权限已撤销")
	}
	if err := r.m.validateSentinelWorkspace(s.Workspace); err != nil {
		return nil, err
	}
	if _, _, err := r.m.scheduleWorkspace(owner, s.Workspace, false); err != nil {
		return nil, err
	}
	if s.Network && os.Getenv("POGO_SANDBOX_NET") != "1" {
		return nil, errors.New("部署未允许脚本网络访问")
	}
	return owner, nil
}
func (r *sentinelRuntime) tick() {
	if active, _ := r.m.releaseMaintenance(); active {
		// Maintenance is temporary control-plane quiescence, not a change to
		// the user's enabled flag or generation. Retire long-lived source
		// processes; their last acknowledged state/cursor remains durable.
		r.mu.Lock()
		for id, worker := range r.workers {
			select {
			case <-worker.done:
				delete(r.workers, id)
			default:
				worker.cancel()
			}
		}
		r.mu.Unlock()
	}
	db, err := r.database(false)
	if err != nil || db == nil {
		return
	}
	watches, err := db.ListAll()
	if err != nil {
		return
	}
	r.mu.Lock()
	for id, w := range r.workers {
		select {
		case <-w.done:
			delete(r.workers, id)
		default:
		}
	}
	r.mu.Unlock()
	for _, w := range watches {
		if r.expireDeadline(db, w) {
			continue
		}
		if active, _ := r.m.releaseMaintenance(); active {
			continue
		}
		_, authErr := r.authorized(w.Spec)
		r.mu.Lock()
		worker := r.workers[w.Spec.ID]
		if authErr != nil && r.runCancels[w.Spec.ID] != nil {
			r.runCancels[w.Spec.ID]()
		}
		if worker != nil && (!w.Spec.Enabled || authErr != nil || worker.generation != w.Spec.Generation) {
			worker.cancel()
		}
		streams := 0
		for _, item := range r.workers {
			if item.mode == "stream" {
				streams++
			}
		}
		// A persisted retry deadline is an explicit recovery plan, including
		// after a controller restart. Do not strand an enabled source after
		// five temporary failures. Fatal sealed-bundle rejection deliberately
		// has no retry deadline (failures=5,next_at=0) and stays blocked.
		retryAllowed := w.Failures < 5 || w.NextAt > 0
		canCollect := (w.Spec.Mode != "stream" || streams < 3) && worker == nil && len(r.workers) < 4 && w.Spec.Enabled && authErr == nil && w.NextAt <= nowMS() && retryAllowed
		if canCollect {
			ctx, cancel := context.WithCancel(r.ctx)
			worker = &sentinelWorker{generation: w.Spec.Generation, mode: w.Spec.Mode, cancel: cancel, done: make(chan struct{})}
			r.workers[w.Spec.ID] = worker
			r.wg.Add(1)
			go func(w sentinel.Watch, sw *sentinelWorker) {
				defer r.wg.Done()
				defer close(sw.done)
				r.collect(ctx, db, w)
			}(w, worker)
		}
		capacityWaiting := worker == nil && w.Spec.Enabled && authErr == nil && w.NextAt <= nowMS() && retryAllowed
		canRun := w.Spec.Enabled && authErr == nil && w.Pending > 0 && !r.runs[w.Spec.ID] && len(r.runs) < 2
		if canRun {
			r.runs[w.Spec.ID] = true
			r.wg.Add(1)
			go func(w sentinel.Watch) {
				defer r.wg.Done()
				defer func() { r.mu.Lock(); delete(r.runs, w.Spec.ID); r.mu.Unlock() }()
				r.dispatch(db, w)
			}(w)
		}
		r.mu.Unlock()
		if capacityWaiting {
			_ = db.SetRuntimeGeneration(w.Spec.ID, w.Spec.Generation, "waiting_capacity", "等待脚本执行槽位", w.NextAt, w.Failures)
		}
		if authErr != nil && w.Spec.Enabled {
			_ = db.SetRuntimeGeneration(w.Spec.ID, w.Spec.Generation, "blocked", authErr.Error(), w.NextAt, w.Failures)
		}
	}
}
func (r *sentinelRuntime) collect(ctx context.Context, db *sentinel.Store, w sentinel.Watch) {
	if r.expireDeadline(db, w) {
		return
	}
	if active, _ := r.m.releaseMaintenance(); active {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := w.Spec
	if s.ExpiresAt > 0 {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithDeadline(ctx, time.UnixMilli(s.ExpiresAt))
		defer deadlineCancel()
	}
	stopDeadlineCheck := context.AfterFunc(ctx, func() { r.expireDeadline(db, w) })
	defer stopDeadlineCheck()
	// A control action may have committed after the scheduler took its snapshot.
	current, err := db.Get(s.ID)
	if err != nil || r.expireDeadline(db, current) || !current.Spec.Enabled || current.Spec.Generation != s.Generation || ctx.Err() != nil {
		return
	}
	if _, err := r.authorized(s); err != nil {
		_ = db.SetRuntimeGeneration(s.ID, s.Generation, "blocked", err.Error(), 0, w.Failures)
		return
	}
	if err := verifySentinelBundle(s.ScriptDir); err != nil {
		_ = db.SetRuntimeGeneration(s.ID, s.Generation, "error", err.Error(), 0, 5)
		return
	}
	if active, _ := r.m.releaseMaintenance(); active {
		return
	}
	_ = db.SetRuntimeGeneration(s.ID, s.Generation, "watching", "", 0, w.Failures)
	started := time.Now()
	if active, _ := r.m.releaseMaintenance(); active {
		return
	}
	err = r.runner(ctx, sentinelexec.Config{Command: s.Command, ScriptDir: s.ScriptDir, Workspace: s.Workspace, Network: s.Network, Mode: s.Mode, Timeout: time.Duration(s.TimeoutSeconds) * time.Second, State: w.State, Cursor: w.Cursor}, func(frame sentinel.Frame) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.expireDeadline(db, w) {
			return sentinel.ErrExpired
		}
		if active, _ := r.m.releaseMaintenance(); active {
			// Refuse a new frame/ACK even before the next supervisor tick
			// notices the marker. This is an operator stop, not a source
			// failure or a reason to advance the committed cursor.
			cancel()
			return context.Canceled
		}
		if _, err := r.authorized(s); err != nil {
			return err
		}
		// Bound lifetime storage as well as pending work; never ACK beyond this budget.
		var disk int64
		for _, suffix := range []string{"", "-wal"} {
			if st, e := os.Stat(filepath.Join(r.m.cfg.SessionDir, "sentinels", "events.db") + suffix); e == nil {
				disk += st.Size()
			}
		}
		if disk >= 256<<20 {
			return errors.New("哨兵事件存储已达到 256 MiB 预算，未推进采集游标")
		}
		return db.Accept(s.ID, s.Generation, frame)
	})
	if r.expireDeadline(db, w) {
		return
	}
	if ctx.Err() != nil {
		return
	}
	if active, _ := r.m.releaseMaintenance(); active {
		return
	}
	if err != nil {
		failures := w.Failures + 1
		if time.Since(started) >= 60*time.Second {
			failures = 1
		}
		delay := time.Duration(1<<min(failures, 8)) * time.Second
		_ = db.SetRuntimeGeneration(s.ID, s.Generation, "error", err.Error(), time.Now().Add(delay).UnixMilli(), failures)
		return
	}
	next := time.Now().Add(time.Duration(max(s.IntervalSeconds, 1)) * time.Second).UnixMilli()
	if s.Mode == "stream" { // A clean EOF is a disconnected source, not a live watch.
		failures := w.Failures + 1
		if time.Since(started) >= 60*time.Second {
			failures = 1
		}
		_ = db.SetRuntimeGeneration(s.ID, s.Generation, "error", "监听脚本已退出，等待重新连接", time.Now().Add(2*time.Second).UnixMilli(), failures)
		return
	}
	_ = db.SetRuntimeGeneration(s.ID, s.Generation, "waiting", "", next, 0)
}
func (r *sentinelRuntime) stopWatch(id string) {
	r.mu.Lock()
	w := r.workers[id]
	r.mu.Unlock()
	if w != nil {
		w.cancel()
	}
}

// This is a conservative admission guard, not a billing meter. Historical
// attempts on a recently updated batch may be counted again after a deferral.
func sentinelBudget(batches []sentinel.Batch) (int, int64) {
	count := 0
	var next int64
	cutoff := time.Now().Add(-24 * time.Hour).UnixMilli()
	for _, batch := range batches {
		if batch.UpdatedAt >= cutoff {
			count += batch.Attempt
			until := batch.UpdatedAt + int64((24*time.Hour)/time.Millisecond)
			if next == 0 || until < next {
				next = until
			}
		}
	}
	return count, next
}

func (r *sentinelRuntime) dispatch(db *sentinel.Store, w sentinel.Watch) {
	// The source owns observations and batches; the shared dispatcher owns
	// every bounded model execution. This method never starts an Agent.
	if r.expireDeadline(db, w) {
		return
	}
	if active, _ := r.m.releaseMaintenance(); active {
		return
	}
	old, err := db.ListBatches(w.Spec.ID, 100)
	if err != nil {
		return
	}
	used, _ := sentinelBudget(old)
	var batch *sentinel.Batch
	for i := range old {
		if old[i].Status == "queued" {
			batch = &old[i]
			break
		}
		if old[i].Status == "interrupted" && old[i].SessionID != "" {
			if session, e := r.m.Get(old[i].SessionID); e == nil {
				if receipt, e := session.readWakeReceipt(fmt.Sprintf("%s-%d", old[i].ID, old[i].Attempt)); e == nil && receipt.Status == "completed" {
					_ = db.SettleBatch(old[i].ID, receipt.Decision, receipt.Summary, receipt.DeferUntil)
					return
				}
			}
		}
	}
	if batch == nil {
		if active, _ := r.m.releaseMaintenance(); active {
			return
		}
		if used >= 100 {
			return
		}
		batch, err = db.ClaimBatch(w.Spec.ID, sentinel.MaxBatchEvents)
	}
	if err != nil || batch == nil {
		return
	}
	if active, _ := r.m.releaseMaintenance(); active {
		return
	}
	if err = db.QueueBatch(batch.ID); err != nil {
		return
	}
	if r.expireDeadline(db, w) {
		return
	}
	_, _, _ = r.m.taskRuns.enqueueSentinel(w, *batch, "")
}

func (r *sentinelRuntime) runSentinelBatch(parent context.Context, db *sentinel.Store, w sentinel.Watch, batch *sentinel.Batch) {
	if r.expireDeadline(db, w) {
		return
	}
	if w.Spec.ExpiresAt > 0 {
		var cancel context.CancelFunc
		parent, cancel = context.WithDeadline(parent, time.UnixMilli(w.Spec.ExpiresAt))
		defer cancel()
	}
	stopDeadlineCheck := context.AfterFunc(parent, func() { r.expireDeadline(db, w) })
	defer stopDeadlineCheck()
	var err error
	if w.Spec.DebounceMS > 0 {
		t := time.NewTimer(time.Duration(w.Spec.DebounceMS) * time.Millisecond)
		select {
		case <-t.C:
		case <-parent.Done():
			t.Stop()
			_ = db.InterruptBatch(batch.ID, "服务已停止，任务尚未启动")
			return
		}
	}
	if active, _ := r.m.releaseMaintenance(); active {
		// Debounce is preparation, not model execution. Keep the same frozen
		// batch/attempt available for the dispatcher after maintenance.
		_ = db.SetBatchRun(batch.ID, batch.SessionID, "", "retry_pending")
		return
	}
	current, err := db.Get(w.Spec.ID)
	if err != nil || r.expireDeadline(db, current) || !current.Spec.Enabled || current.Spec.Generation != w.Spec.Generation {
		_ = db.SetBatchRun(batch.ID, batch.SessionID, "", "retry_pending")
		return
	}
	w = current
	owner, err := r.authorized(w.Spec)
	if err != nil {
		_ = db.InterruptBatch(batch.ID, err.Error())
		return
	}
	a, _ := r.m.studio.get(w.Spec.AgentID)
	sid := w.Spec.SessionID
	if sid == "" {
		base := r.m.cfg.Cwd
		if owner != nil {
			base = r.m.UserRoot(*owner)
		}
		rel, e := filepath.Rel(base, w.Spec.Workspace)
		if e != nil {
			_ = db.InterruptBatch(batch.ID, e.Error())
			return
		}
		identity := &session.SentinelIdentity{WatchID: w.Spec.ID, ParentSessionID: r.m.sentinelParentSession(w), AgentID: w.Spec.AgentID}
		sess, e := r.m.createStudioBoundConversationKind(a, owner, false, identity, rel)
		if e != nil {
			_ = db.InterruptBatch(batch.ID, e.Error())
			return
		}
		sid = sess.ID
		title := "哨兵 · " + w.Spec.Title
		_ = sess.SetInfo(&title, nil)
		if e = db.UpdateSession(w.Spec.ID, sid); e != nil {
			_ = db.InterruptBatch(batch.ID, e.Error())
			return
		}
	}
	sess, err := r.m.Get(sid)
	if err != nil {
		_ = db.InterruptBatch(batch.ID, "哨兵工作会话不可用: "+err.Error())
		return
	}
	if sess.studioID != w.Spec.AgentID || sessionResultOwnerKey(sess.owner) != w.Spec.OwnerID || sess.cwd != w.Spec.Workspace {
		_ = db.InterruptBatch(batch.ID, "哨兵工作会话绑定不一致")
		return
	}
	if err = bindTaskSession(parent, sid); err != nil {
		_ = db.InterruptBatch(batch.ID, err.Error())
		return
	}
	if err = db.SetBatchRun(batch.ID, sid, "", "admitting"); err != nil {
		_ = db.InterruptBatch(batch.ID, err.Error())
		return
	}
	batch.SessionID = sid
	wakeID := fmt.Sprintf("%s-%d", batch.ID, batch.Attempt)
	if prior, e := sess.readWakeReceipt(wakeID); e == nil && prior.Status == "completed" {
		_ = db.SettleBatch(batch.ID, prior.Decision, prior.Summary, prior.DeferUntil)
		return
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	r.mu.Lock()
	r.runCancels[w.Spec.ID] = cancel
	r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		_ = db.InterruptBatch(batch.ID, err.Error())
		return
	}
	if active, _ := r.m.releaseMaintenance(); active {
		// Binding a retained session can itself take time. Recheck immediately
		// before admitting a wake, without inventing a run or consuming an
		// attempt merely because the operator paused the service.
		_ = db.SetBatchRun(batch.ID, sid, "", "retry_pending")
		return
	}
	input := sentinelWakeInput{ID: wakeID, WatchID: w.Spec.ID, BatchID: batch.ID, Task: w.Spec.Task, Events: batch.Events, MaxTurns: w.Spec.MaxTurns, Generation: w.Spec.Generation}
	bind, err := sess.startSentinelWake(input, ctx)
	if errors.Is(err, ErrRunActive) { // No execution began; preserve this same logical attempt.
		_ = db.SetBatchRun(batch.ID, sid, "", "retry_pending")
		return
	}
	if err != nil {
		_ = db.InterruptBatch(batch.ID, err.Error())
		return
	}
	if err = db.SetBatchRun(batch.ID, sid, bind.run.id, "running"); err != nil {
		bind.run.cancel()
		<-bind.run.done
		_ = db.InterruptBatch(batch.ID, err.Error())
		return
	}
	select {
	case <-bind.run.done:
	case <-ctx.Done():
		bind.run.cancel()
		<-bind.run.done
	}
	receipt, err := sess.readWakeReceipt(wakeID)
	if err == nil && receipt.Status == "completed" {
		err = db.SettleBatch(batch.ID, receipt.Decision, receipt.Summary, receipt.DeferUntil)
	}
	if err != nil || receipt.Status != "completed" {
		reason := "执行中断，已保留事件；检查状态后可显式重试"
		if err != nil {
			reason = err.Error()
		}
		_ = db.InterruptBatch(batch.ID, reason)
	}
}
