package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/taskqueue"
)

type sentinelTaskPayload struct {
	BatchID string `json:"batch_id"`
	Attempt int    `json:"attempt"`
}

func sentinelTaskHash(s sentinel.Spec) string {
	// Collection generation, enabled flag and retained work conversation may
	// change without changing the user's standing task.
	raw, _ := json.Marshal(struct {
		OwnerID, AgentID, Workspace, ScriptDir, Task string
		Command                                      []string
		Network                                      bool
		MaxTurns                                     int
		ExpiresAt                                    int64 `json:"ExpiresAt,omitempty"`
	}{s.OwnerID, s.AgentID, s.Workspace, s.ScriptDir, s.Task, s.Command, s.Network, s.MaxTurns, s.ExpiresAt})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (r *taskRunRuntime) enqueueSentinel(w sentinel.Watch, batch sentinel.Batch, retryOf string) (taskqueue.Run, bool, error) {
	if w.Spec.Expired(r.m.sentinels.timeNow()) {
		return taskqueue.Run{}, false, sentinel.ErrExpired
	}
	db, err := r.database(true)
	if err != nil {
		return taskqueue.Run{}, false, err
	}
	if retryOf == "" && batch.Attempt > 1 {
		prior, err := db.List(taskqueue.Filter{TaskID: "sentinel:" + w.Spec.ID})
		if err != nil {
			return taskqueue.Run{}, false, err
		}
		for _, row := range prior {
			var old sentinelTaskPayload
			if json.Unmarshal(row.Payload, &old) == nil && old.BatchID == batch.ID && old.Attempt == batch.Attempt-1 && (row.Status == "interrupted" || row.Status == "failed" || row.Status == "cancelled") {
				retryOf = row.ID
				break
			}
		}
	}
	payload, _ := json.Marshal(sentinelTaskPayload{batch.ID, batch.Attempt})
	run, created, err := db.Enqueue(taskqueue.Request{TaskID: "sentinel:" + w.Spec.ID, Source: "sentinel", SourceID: w.Spec.ID, Trigger: "condition", TriggerID: fmt.Sprintf("batch:%s:%d", batch.ID, batch.Attempt), DefinitionHash: sentinelTaskHash(w.Spec), OwnerID: w.Spec.OwnerID, AgentID: w.Spec.AgentID, Title: w.Spec.Title, DueAt: batch.CreatedAt, Payload: payload, RetryOf: retryOf})
	if err == nil {
		// Cancellation commits to the queue before dismissing the source
		// batch. Replaying publication repairs a crash between those stores,
		// without executing the dismissed observations or blocking new ones.
		if run.Status == "cancelled" {
			r.cancelSentinelQueued(run)
		}
		r.wake()
	}
	return run, created, err
}

func (r *taskRunRuntime) sentinelReady(run taskqueue.Run) bool {
	db, err := r.m.sentinels.database(false)
	if err != nil || db == nil {
		return true // Let bounded execution record the unavailable source.
	}
	w, err := db.Get(run.SourceID)
	if err != nil {
		return true
	}
	if r.m.sentinels.expireDeadline(db, w) {
		// Permit the bounded queue executor to write an interrupted receipt,
		// without running a model or leaving a queued admission forever.
		return true
	}
	if !w.Spec.Enabled {
		return false
	}
	if w.Spec.SessionID == "" {
		return true
	}
	sess, err := r.m.Get(w.Spec.SessionID)
	if err != nil {
		return true
	}
	sess.mu.Lock()
	busy := sess.active != nil || sess.compacting || sess.inboxStarting != ""
	sess.mu.Unlock()
	return !busy
}

func (r *taskRunRuntime) executeSentinel(ctx context.Context, run taskqueue.Run) taskqueue.Outcome {
	fail := func(message string) taskqueue.Outcome {
		return taskqueue.Outcome{Status: "interrupted", Error: message}
	}
	var payload sentinelTaskPayload
	if json.Unmarshal(run.Payload, &payload) != nil || payload.BatchID == "" {
		return fail("事件批次绑定无效，未执行")
	}
	native := r.m.sentinels
	db, err := native.database(false)
	if err != nil || db == nil {
		return fail("哨兵事件库不可用，未执行")
	}
	w, err := db.Get(run.SourceID)
	if err != nil || w.Spec.OwnerID != run.OwnerID || w.Spec.AgentID != run.AgentID || sentinelTaskHash(w.Spec) != run.DefinitionHash {
		return fail("哨兵定义或权限绑定已变化，未执行")
	}
	if native.expireDeadline(db, w) {
		return fail(sentinel.ErrExpired.Error())
	}
	if _, err = native.authorized(w.Spec); err != nil {
		return fail(err.Error())
	}
	if err = verifySentinelBundle(w.Spec.ScriptDir); err != nil {
		return fail(err.Error())
	}
	batch, err := db.GetBatch(payload.BatchID)
	if err != nil || batch.WatchID != w.Spec.ID || batch.Attempt != payload.Attempt {
		return fail("事件批次已变化，本次未执行")
	}
	if batch.Status == "completed" || batch.Status == "deferred" {
		return taskqueue.Outcome{Status: "completed", SessionID: batch.SessionID, Summary: batch.Summary, EndReason: batch.Decision}
	}
	if !w.Spec.Enabled {
		return taskqueue.Outcome{EndReason: "not_started"}
	}
	if batch.Status == "interrupted" {
		// This queue claim came from a durable queued request, not from a
		// recovered running request. Native Recover may conservatively mark
		// its pre-execution admission interrupted. No native RunID is proof
		// required here before returning it to admission.
		if batch.RunID != "" {
			return fail("前次事件处理结果待核查，不自动重跑")
		}
		if err = db.SetBatchRun(batch.ID, batch.SessionID, "", "retry_pending"); err != nil {
			return fail(err.Error())
		}
	}
	native.runSentinelBatch(ctx, db, w, &batch)
	native.mu.Lock()
	delete(native.runCancels, w.Spec.ID)
	native.mu.Unlock()
	result, err := db.GetBatch(batch.ID)
	if err != nil {
		return fail("事件处理回执不可读取，请核查")
	}
	if result.Status == "retry_pending" || result.Status == "queued" {
		return taskqueue.Outcome{EndReason: "not_started"}
	}
	if result.Status == "completed" || result.Status == "deferred" {
		return taskqueue.Outcome{Status: "completed", SessionID: result.SessionID, Summary: result.Summary, EndReason: result.Decision}
	}
	return taskqueue.Outcome{Status: "interrupted", SessionID: result.SessionID, Error: result.Error, EndReason: "interrupted"}
}

func (r *taskRunRuntime) cancelSentinelQueued(run taskqueue.Run) {
	var payload sentinelTaskPayload
	if json.Unmarshal(run.Payload, &payload) != nil {
		return
	}
	db, err := r.m.sentinels.database(false)
	if err != nil || db == nil {
		return
	}
	batch, err := db.GetBatch(payload.BatchID)
	if err == nil && batch.WatchID == run.SourceID && batch.Attempt == payload.Attempt && batch.RunID == "" {
		// No model execution began. The user's cancellation dismisses this
		// batch, while preserving its frozen observations in audit history;
		// subsequent new source events must remain able to trigger normally.
		_ = db.SettleBatch(batch.ID, "ignore", "用户取消尚未开始的事件处理；本批次未执行", 0)
	}
}

func (r *taskRunRuntime) retrySentinel(run taskqueue.Run) (taskqueue.Run, error) {
	if run.Status != "interrupted" && run.Status != "failed" && run.Status != "cancelled" {
		return run, taskqueue.ErrConflict
	}
	var payload sentinelTaskPayload
	if json.Unmarshal(run.Payload, &payload) != nil {
		return run, taskqueue.ErrConflict
	}
	db, err := r.m.sentinels.database(false)
	if err != nil || db == nil {
		if err == nil {
			err = taskqueue.ErrNotFound
		}
		return run, err
	}
	w, err := db.Get(run.SourceID)
	if err != nil || w.Spec.OwnerID != run.OwnerID || w.Spec.AgentID != run.AgentID || sentinelTaskHash(w.Spec) != run.DefinitionHash {
		return run, taskqueue.ErrConflict
	}
	if r.m.sentinels.expireDeadline(db, w) {
		return run, sentinel.ErrExpired
	}
	if _, err = r.m.sentinels.authorized(w.Spec); err != nil {
		return run, err
	}
	batch, err := db.GetBatch(payload.BatchID)
	if err != nil || batch.WatchID != run.SourceID {
		return run, taskqueue.ErrNotFound
	}
	if batches, err := db.ListBatches(run.SourceID, 100); err != nil {
		return run, err
	} else if used, _ := sentinelBudget(batches); used >= 100 && batch.Attempt == payload.Attempt {
		return run, errors.New("哨兵的每日执行准入预算已用尽，暂不能开始新的重试")
	}
	if batch.Attempt == payload.Attempt && batch.Status == "queued" && batch.RunID == "" {
		// Queue recovery stopped the generic admission before the native
		// executor began. An explicit retry still creates a new native attempt.
		if err = db.InterruptBatch(batch.ID, "显式重试已中断的排队批次"); err != nil {
			return run, err
		}
		batch.Status = "interrupted"
	}
	if batch.Attempt == payload.Attempt && batch.Status == "interrupted" {
		if err = db.RetryBatch(batch.ID); err != nil {
			return run, err
		}
		batch, err = db.GetBatch(batch.ID)
	}
	if err != nil || batch.Attempt != payload.Attempt+1 {
		return run, taskqueue.ErrConflict
	}
	if err = db.QueueBatch(batch.ID); err != nil {
		// A repeated retry may find that exact new attempt already running.
		queue, e := r.database(false)
		if e == nil && queue != nil {
			rows, _ := queue.List(taskqueue.Filter{TaskID: run.TaskID})
			for _, row := range rows {
				if row.TriggerID == fmt.Sprintf("batch:%s:%d", batch.ID, batch.Attempt) {
					return row, nil
				}
			}
		}
		return run, errors.Join(taskqueue.ErrConflict, err)
	}
	next, _, err := r.enqueueSentinel(w, batch, run.ID)
	return next, err
}
