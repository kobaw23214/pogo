package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func officeQueueCommand(t *testing.T, service *officeService, document *officeDocumentSession, id, name string) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := service.command(context.Background(), document, id, name, json.RawMessage(`{}`))
		done <- err
	}()
	officeTestQueued(t, document, id)
	return done
}

func officeQueueReceipt(t *testing.T, service *officeService, document *officeDocumentSession, instance, id string, ok bool, code string) {
	t.Helper()
	body := map[string]any{"instanceId": instance, "operationId": id, "ok": ok, "result": map[string]any{"kind": "history", "changed": true}}
	if !ok {
		body["error"] = map[string]any{"code": code, "message": "The precondition did not hold."}
	}
	response := officeTestBridge(t, service, document, "POST", "results", body, "")
	if response.Code != 200 {
		t.Fatalf("receipt failed: %s", response.Body.String())
	}
}

func TestOfficeQueueSaveBarrierUsesDispatchEpochAndOneExport(t *testing.T) {
	service, document, file := officeTestService(t)
	instance := officeTestReady(t, service, document)
	first := officeQueueCommand(t, service, document, "barrier_undo_001", "history.undo")
	second := officeQueueCommand(t, service, document, "barrier_redo_001", "history.redo")
	saved := make(chan error, 1)
	go func() { _, err := service.save(context.Background(), document, "barrier_save_001"); saved <- err }()
	op := officeTestQueued(t, document, "barrier_save_001")
	alias := make(chan error, 1)
	go func() { _, err := service.save(context.Background(), document, "barrier_alias_001"); alias <- err }()
	joined := officeTestQueued(t, document, "barrier_alias_001")
	if joined != op {
		t.Fatal("a duplicate save allocated a second export")
	}
	view := document.view()
	if view.QueueDepth != 3 || view.SavePhase != "queued" || view.SaveState != "saving" {
		t.Fatal("queued save is not visible as waiting behind both operations")
	}
	_, err := service.command(context.Background(), document, "barrier_later_mutation", "history.undo", json.RawMessage(`{}`))
	officeExpectError(t, err, "SAVE_IN_PROGRESS")
	officeTestDispatch(t, service, document, instance, "barrier_undo_001")
	officeQueueReceipt(t, service, document, instance, "barrier_undo_001", true, "")
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	officeTestDispatch(t, service, document, instance, "barrier_redo_001")
	officeQueueReceipt(t, service, document, instance, "barrier_redo_001", true, "")
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	// A native human edit before export joins the same actual canvas snapshot.
	response := officeTestBridge(t, service, document, "POST", "state", map[string]any{"instanceId": instance, "dirty": true}, "")
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	officeTestDispatch(t, service, document, instance, "barrier_save_001")
	document.mu.Lock()
	epoch, count := op.epoch, len(document.queue)
	document.mu.Unlock()
	if epoch != 3 || count != 3 {
		t.Fatal("save captured admission epoch or duplicated queue work")
	}
	data := officeTestPackage(t, "two agent operations and human notes")
	if err := service.commitExport(document, op, instance, data); err != nil {
		t.Fatal(err)
	}
	if <-saved != nil || <-alias != nil {
		t.Fatal("barrier save or shared receipt failed")
	}
	current, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(current, data) {
		t.Fatal("the shared canvas export was not persisted")
	}
	view = document.view()
	if view.Dirty || view.PersistedEpoch != 3 || view.Revision != 1 || view.QueueDepth != 0 {
		t.Fatal("the save did not confirm both queued operations and manual content")
	}
}

func TestOfficeQueueSharedSaveCancellationDoesNotCancelOthers(t *testing.T) {
	for _, duplicateID := range []bool{false, true} {
		t.Run(fmt.Sprint(duplicateID), func(t *testing.T) {
			service, document, _ := officeTestService(t)
			instance := officeTestReady(t, service, document)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			primary := make(chan error, 1)
			go func() { _, err := service.save(ctx, document, "shared_save_001"); primary <- err }()
			op := officeTestQueued(t, document, "shared_save_001")
			aliasID := "shared_alias_001"
			if duplicateID {
				aliasID = "shared_save_001"
			}
			other := make(chan error, 1)
			go func() { _, err := service.save(context.Background(), document, aliasID); other <- err }()
			if duplicateID {
				// The map identity is unchanged, so synchronize on the shared bit.
				deadline := time.Now().Add(2 * time.Second)
				for {
					document.mu.Lock()
					shared := op.saveShared
					document.mu.Unlock()
					if shared {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("second same-ID waiter did not join")
					}
					time.Sleep(time.Millisecond)
				}
			} else {
				officeTestQueued(t, document, aliasID)
			}
			officeTestDispatch(t, service, document, instance, "shared_save_001")
			cancel()
			if !errors.Is(<-primary, context.Canceled) {
				t.Fatal("cancelled waiter did not leave")
			}
			document.mu.Lock()
			finished, blocked, detached := op.finished, document.blocked, op.requestContext == nil
			document.mu.Unlock()
			if finished || blocked || !detached {
				t.Fatal("one waiter poisoned the common dispatched save")
			}
			if err := service.commitExport(document, op, instance, officeTestPackage(t, "shared result")); err != nil {
				t.Fatal(err)
			}
			if err := <-other; err != nil {
				t.Fatal("the remaining save waiter did not succeed")
			}
			view, err := service.save(context.Background(), document, "shared_save_001")
			if err != nil || view.Revision != 1 || document.view().Revision != 1 {
				t.Fatal("retry replayed the common export")
			}
		})
	}
}

func TestOfficeQueueRejectedOrUnknownPredecessorStopsUndispatchedSave(t *testing.T) {
	for _, code := range []string{"PRECONDITION_FAILED", "OUTCOME_UNKNOWN"} {
		t.Run(code, func(t *testing.T) {
			service, document, file := officeTestService(t)
			instance := officeTestReady(t, service, document)
			original, _ := os.ReadFile(file)
			command := officeQueueCommand(t, service, document, "failed_before_save", "history.undo")
			saved := make(chan error, 1)
			go func() { _, err := service.save(context.Background(), document, "save_after_failure"); saved <- err }()
			barrier := officeTestQueued(t, document, "save_after_failure")
			officeTestDispatch(t, service, document, instance, "failed_before_save")
			officeQueueReceipt(t, service, document, instance, "failed_before_save", false, code)
			if err := <-command; err != nil {
				t.Fatal(err)
			}
			if <-saved == nil {
				t.Fatal("a failed predecessor was reported saved")
			}
			document.mu.Lock()
			finished, dispatched, blocked := barrier.finished, barrier.dispatched, document.blocked
			result := append([]byte(nil), barrier.result...)
			document.mu.Unlock()
			if !finished || dispatched || blocked != (code == "OUTCOME_UNKNOWN") || !bytes.Contains(result, []byte("SAVE_PREDECESSOR_FAILED")) {
				t.Fatal("save failure was delayed, dispatched, or changed unknown-outcome protection")
			}
			current, _ := os.ReadFile(file)
			if !bytes.Equal(current, original) || document.view().Revision != 0 {
				t.Fatal("rejected operation wrote the workspace document")
			}
			if code == "PRECONDITION_FAILED" {
				// Known rejection is not a permanent block. A deliberate new save
				// sees only its own pending predecessors, not old rejected history.
				retry := officeTestStartSave(t, service, document, instance, "save_after_review")
				document.mu.Lock()
				next := document.exportIntent
				document.mu.Unlock()
				if err := service.commitExport(document, next, instance, original); err != nil {
					t.Fatal(err)
				}
				if <-retry != nil {
					t.Fatal("old failed history blocked a reviewed save")
				}
			}
		})
	}
}

func TestOfficeQueueNewHumanContentDoesNotJoinOlderExport(t *testing.T) {
	service, document, _ := officeTestService(t)
	instance := officeTestReady(t, service, document)
	document.mu.Lock()
	document.dirtyEpoch = 1
	document.mu.Unlock()
	saved := officeTestStartSave(t, service, document, instance, "older_canvas_save")
	document.mu.Lock()
	op := document.exportIntent
	document.mu.Unlock()
	response := officeTestBridge(t, service, document, "POST", "state", map[string]any{"instanceId": instance, "dirty": true}, "")
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	_, err := service.save(context.Background(), document, "newer_canvas_save")
	officeExpectError(t, err, "SAVE_IN_PROGRESS")
	if err := service.commitExport(document, op, instance, officeTestPackage(t, "older exported canvas")); err != nil {
		t.Fatal(err)
	}
	if <-saved != nil {
		t.Fatal("older export failed")
	}
	view, err := service.save(context.Background(), document, "older_canvas_save")
	if err != nil || !view.Dirty || view.PersistedEpoch != 1 || view.DirtyEpoch != 2 || view.Revision != 1 {
		t.Fatal("an old save receipt marked newer native input as persisted")
	}
}

func TestOfficeQueueFinalSaveAliasesCannotBypassLedgerBudget(t *testing.T) {
	service, document, _ := officeTestService(t)
	instance := officeTestReady(t, service, document)
	document.mu.Lock()
	for i := 0; i < officeMaxOperations; i++ {
		id := fmt.Sprintf("bounded_seed_%03d", i)
		document.operations[id] = &officeOperation{id: id, name: "word.read", finished: true}
	}
	document.mu.Unlock()
	saved := officeTestStartSave(t, service, document, instance, "bounded_final_save")
	_, err := service.save(context.Background(), document, "bounded_extra_alias")
	officeExpectError(t, err, "OPERATION_LIMIT")
	document.mu.Lock()
	op, count := document.exportIntent, len(document.operations)
	document.mu.Unlock()
	if count != officeMaxOperations+1 {
		t.Fatal("alias exceeded the final save ledger slot")
	}
	if err := service.commitExport(document, op, instance, officeTestPackage(t, "bounded export")); err != nil {
		t.Fatal(err)
	}
	if <-saved != nil {
		t.Fatal("reserved final save failed")
	}
}

func TestOfficeQueueDispatchStartsFixedExportBudgetAndFencesOldTimer(t *testing.T) {
	service, document, _ := officeTestService(t)
	instance := officeTestReady(t, service, document)
	command := officeQueueCommand(t, service, document, "budget_predecessor", "history.undo")
	saved := make(chan error, 1)
	go func() { _, err := service.save(context.Background(), document, "budget_queued_save"); saved <- err }()
	op := officeTestQueued(t, document, "budget_queued_save")
	document.mu.Lock()
	queuedDeadline := op.deadline
	document.mu.Unlock()
	officeTestDispatch(t, service, document, instance, "budget_predecessor")
	officeQueueReceipt(t, service, document, instance, "budget_predecessor", true, "")
	if err := <-command; err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	officeTestDispatch(t, service, document, instance, "budget_queued_save")
	document.mu.Lock()
	exportDeadline := op.deadline
	document.mu.Unlock()
	if !exportDeadline.Before(queuedDeadline) || exportDeadline.Before(before.Add(service.cfg.saveTimeout)) || exportDeadline.After(time.Now().Add(service.cfg.saveTimeout)) {
		t.Fatal("export inherited leftover queue waiting time instead of its fixed budget")
	}
	service.expireAt(document, op, queuedDeadline)
	document.mu.Lock()
	finished, blocked := op.finished, document.blocked
	document.mu.Unlock()
	if finished || blocked {
		t.Fatal("a stale queue timer cancelled a dispatched save")
	}
	if err := service.commitExport(document, op, instance, officeTestPackage(t, "bounded export")); err != nil {
		t.Fatal(err)
	}
	if <-saved != nil {
		t.Fatal("fixed-budget export did not succeed")
	}
}

func TestOfficeQueueSlowCommitAllowsAnotherDocumentToOpen(t *testing.T) {
	service, document, _ := officeTestService(t)
	instance := officeTestReady(t, service, document)
	data := officeTestPackage(t, "independent document")
	otherPath := document.absolute + ".docx"
	if err := os.WriteFile(otherPath, data, 0640); err != nil {
		t.Fatal(err)
	}
	journal := &officeBlockingJournal{entered: make(chan struct{}), release: make(chan struct{})}
	document.journal = journal
	saved := officeTestStartSave(t, service, document, instance, "independent_save")
	document.mu.Lock()
	op := document.exportIntent
	document.mu.Unlock()
	committed := make(chan error, 1)
	go func() { committed <- service.commitExport(document, op, instance, data) }()
	<-journal.entered
	opened := make(chan error, 1)
	go func() {
		_, err := service.open(document.conversationID, document.cwd, nil, NewHub(), otherPath, "another.docx", nil)
		opened <- err
	}()
	select {
	case err := <-opened:
		close(journal.release)
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(journal.release)
		t.Fatal("one document's journal I/O blocked registration of another document")
	}
	if <-committed != nil || <-saved != nil {
		t.Fatal("independent document commit failed")
	}
}
