package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func officeRecoveryUnknown(t *testing.T, o *officeService, d *officeDocumentSession, instance, id, name string, args json.RawMessage) *officeOperation {
	t.Helper()
	done := make(chan json.RawMessage, 1)
	go func() { result, _ := o.command(context.Background(), d, id, name, args); done <- result }()
	op := officeTestQueued(t, d, id)
	officeTestDispatch(t, o, d, instance, id)
	d.mu.Lock()
	op.deadline = time.Now().Add(-time.Second)
	o.expireLocked(d, op)
	d.mu.Unlock()
	if result := <-done; !bytes.Contains(result, []byte("OUTCOME_UNKNOWN")) {
		t.Fatalf("expected an immutable unknown mutation receipt: %s", result)
	}
	return op
}

func officeRecoveryBody(d *officeDocumentSession, instance string, op *officeOperation, ok bool, code string) map[string]any {
	v := d.view().Recovery
	outcome := map[string]any{"operationId": op.id, "operation": op.name, "ok": ok, "result": map[string]any{"kind": "history", "changed": true}}
	if !ok {
		outcome["error"] = map[string]any{"code": code, "message": "Native execution ended."}
	}
	return map[string]any{"instanceId": instance, "recoveryRevision": v.Revision, "barrierId": "recovery_barrier_001", "settled": true, "operations": []any{outcome}}
}

func TestOfficeReadTimeoutsAndCancellationNeverBlockOrDirty(t *testing.T) {
	for _, name := range []string{"capabilities", "word.read", "sheet.list", "sheet.read"} {
		t.Run(name, func(t *testing.T) {
			o, d, _ := officeTestService(t)
			args := json.RawMessage(`{}`)
			if strings.HasPrefix(name, "sheet.") {
				d.editor = "cell"
			}
			if name == "sheet.read" {
				args = json.RawMessage(`{"sheet":"明细","range":"A1"}`)
			}
			instance := officeTestReady(t, o, d)
			if name == "sheet.list" {
				cap := officeTestCapabilities("cell")
				cap["sheetList"] = true
				if r := officeTestBridge(t, o, d, "POST", "ready", map[string]any{"instanceId": instance, "capabilities": cap}, ""); r.Code != 200 {
					t.Fatal(r.Body.String())
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { _, err := o.command(ctx, d, "cancel_native_read_001", name, args); done <- err }()
			op := officeTestQueued(t, d, "cancel_native_read_001")
			officeTestDispatch(t, o, d, instance, op.id)
			cancel()
			if !errors.Is(<-done, context.Canceled) {
				t.Fatal("read cancellation did not return")
			}
			if v := d.view(); v.Blocked || v.Dirty || v.Recovery != nil {
				t.Fatal("read cancellation locked or dirtied canvas")
			}
			late := officeTestBridge(t, o, d, "POST", "results", map[string]any{"instanceId": instance, "operationId": op.id, "ok": true, "result": map[string]any{"text": "late"}}, "")
			if late.Code != 200 || d.view().Blocked || !bytes.Contains(op.result, []byte("READ_CANCELLED")) {
				t.Fatal("late read changed immutable cancelled result")
			}
			doneRead := make(chan error, 1)
			go func() { _, err := o.command(context.Background(), d, "read_again_001", name, args); doneRead <- err }()
			fresh := officeTestQueued(t, d, "read_again_001")
			officeTestDispatch(t, o, d, instance, fresh.id)
			if r := officeTestBridge(t, o, d, "POST", "results", map[string]any{"instanceId": instance, "operationId": fresh.id, "ok": true, "result": map[string]any{"text": "fresh"}}, ""); r.Code != 200 {
				t.Fatal(r.Body.String())
			}
			if <-doneRead != nil || d.view().Dirty {
				t.Fatal("fresh read after cancellation did not work")
			}
		})
	}
}

func TestOfficeUnknownMutationKeepsReadAvailableAndRequiresVerifiedBarrier(t *testing.T) {
	o, d, _ := officeTestService(t)
	instance := officeTestReady(t, o, d)
	op := officeRecoveryUnknown(t, o, d, instance, "unknown_undo_001", "history.undo", json.RawMessage(`{}`))
	if v := d.view(); !v.Blocked || !v.Dirty || v.Recovery == nil || len(v.Recovery.OperationIDs) != 1 {
		t.Fatal("missing conservative unknown state")
	}
	readDone := officeQueueCommand(t, o, d, "read_during_recovery_001", "word.read")
	officeTestDispatch(t, o, d, instance, "read_during_recovery_001")
	if r := officeTestBridge(t, o, d, "POST", "results", map[string]any{"instanceId": instance, "operationId": "read_during_recovery_001", "ok": true, "result": map[string]any{"kind": "word", "text": "manual retained"}}, ""); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if <-readDone != nil || !d.view().Blocked {
		t.Fatal("ordinary read improperly cleared unknown native execution")
	}
	valid := officeRecoveryBody(d, instance, op, true, "")
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { v["settled"] = false },
		func(v map[string]any) { v["instanceId"] = "another_instance_001" },
		func(v map[string]any) { v["recoveryRevision"] = uint64(99) },
		func(v map[string]any) { v["operations"] = []any{} },
		func(v map[string]any) {
			v["operations"] = []any{map[string]any{"operationId": "other_write_001", "operation": "history.undo", "ok": true, "result": map[string]any{}}}
		},
	} {
		bad := map[string]any{}
		for k, v := range valid {
			bad[k] = v
		}
		mutate(bad)
		if r := officeTestBridge(t, o, d, "POST", "reconcile", bad, ""); r.Code == 200 || !d.view().Blocked {
			t.Fatal("unverified barrier unlocked document")
		}
	}
	// A human change while recovery is pending must remain an independent epoch.
	if r := officeTestBridge(t, o, d, "POST", "state", map[string]any{"instanceId": instance, "dirty": true}, ""); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	before := d.view().DirtyEpoch
	if r := officeTestBridge(t, o, d, "POST", "reconcile", valid, ""); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if v := d.view(); v.Blocked || !v.Dirty || v.DirtyEpoch != before || !op.reconciled || op.epoch != op.unknownDirtyEpoch {
		t.Fatal("settled recovery lost manual input or counted mutation twice")
	}
	result, err := o.command(context.Background(), d, op.id, op.name, op.args)
	if err != nil || !bytes.Contains(result, []byte("OUTCOME_UNKNOWN")) || !bytes.Contains(result, []byte(`"reconciled":true`)) || len(d.queue) != 2 {
		t.Fatal("retry of original ID replayed or rewrote unknown outcome")
	}
	if err := o.closeDocument(d); err == nil {
		t.Fatal("recovery discarded possibly changed unsaved canvas")
	}
}

func TestOfficeUncertainSettledMutationRequiresFreshNativeRead(t *testing.T) {
	o, d, _ := officeTestService(t)
	instance := officeTestReady(t, o, d)
	op := officeRecoveryUnknown(t, o, d, instance, "uncertain_undo_001", "history.undo", json.RawMessage(`{}`))
	if r := officeTestBridge(t, o, d, "POST", "reconcile", officeRecoveryBody(d, instance, op, false, "EDITOR_OUTCOME_UNKNOWN"), ""); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	v := d.view()
	if !v.Blocked || !v.Recovery.NeedsRead || v.Recovery.ReadTarget.Operation != "word.read" || len(v.Recovery.OperationIDs) != 0 {
		t.Fatal("native barrier alone made uncertain content writable")
	}
	read := officeQueueCommand(t, o, d, "fresh_recovery_read_001", "word.read")
	officeTestDispatch(t, o, d, instance, "fresh_recovery_read_001")
	if r := officeTestBridge(t, o, d, "POST", "results", map[string]any{"instanceId": instance, "operationId": "fresh_recovery_read_001", "ok": true, "result": map[string]any{"kind": "word", "text": "actual native content"}}, ""); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if <-read != nil || d.view().Blocked || !d.view().Dirty || !bytes.Contains(op.result, []byte("OUTCOME_UNKNOWN")) || op.epoch != 0 {
		t.Fatal("fresh read did not recover without inventing earlier success")
	}
}

func TestOfficeWriteWaitsForNativeRecoveryWithoutReplay(t *testing.T) {
	o, d, _ := officeTestService(t)
	instance := officeTestReady(t, o, d)
	op := officeRecoveryUnknown(t, o, d, instance, "pending_unknown_001", "history.undo", json.RawMessage(`{}`))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := o.command(ctx, d, "write_after_recovery_001", "history.redo", json.RawMessage(`{}`))
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	d.mu.Lock()
	premature := d.operations["write_after_recovery_001"] != nil
	d.mu.Unlock()
	if premature {
		t.Fatal("write was dispatched before unknown native execution settled")
	}
	if r := officeTestBridge(t, o, d, "POST", "reconcile", officeRecoveryBody(d, instance, op, true, ""), ""); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	fresh := officeTestQueued(t, d, "write_after_recovery_001")
	officeTestDispatch(t, o, d, instance, fresh.id)
	officeQueueReceipt(t, o, d, instance, fresh.id, true, "")
	if <-done != nil || len(d.queue) != 2 {
		t.Fatal("followup write failed or original write replayed")
	}
}

func TestOfficeNextFlightGuardRetainsRecoveryControlWithoutDroppingCommands(t *testing.T) {
	o, d, _ := officeTestService(t)
	instance := officeTestReady(t, o, d)
	op := officeRecoveryUnknown(t, o, d, instance, "guard_unknown_001", "history.undo", json.RawMessage(`{}`))
	done := officeQueueCommand(t, o, d, "guard_read_001", "word.read")
	response := officeTestBridge(t, o, d, "GET", "next", nil, instance+"&inFlightId="+op.id)
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"command":null`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"finished":true`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"recovery"`)) {
		t.Fatal("control poll lost recovery or handed busy view another command")
	}
	officeTestDispatch(t, o, d, instance, "guard_read_001")
	officeTestBridge(t, o, d, "POST", "results", map[string]any{"instanceId": instance, "operationId": "guard_read_001", "ok": true, "result": map[string]any{}}, "")
	if <-done != nil {
		t.Fatal("read command was dropped")
	}
}

func TestOfficeUnknownExportRecoveryRequiresMatchingSettledExport(t *testing.T) {
	o, d, _ := officeTestService(t)
	instance := officeTestReady(t, o, d)
	d.mu.Lock()
	d.dirtyEpoch = 1
	d.mu.Unlock()
	done := officeTestStartSave(t, o, d, instance, "unknown_export_001")
	d.mu.Lock()
	op := d.exportIntent
	op.deadline = time.Now().Add(-time.Second)
	o.expireLocked(d, op)
	d.mu.Unlock()
	if err := <-done; err == nil {
		t.Fatal("unknown save claimed success")
	}
	body := map[string]any{"instanceId": instance, "recoveryRevision": d.view().Recovery.Revision, "barrierId": "export_barrier_001", "settled": true, "operations": []any{}, "exports": []any{map[string]any{"operationId": op.id, "settled": true}}}
	d.mu.Lock()
	d.exportBusy = true
	d.mu.Unlock()
	if r := officeTestBridge(t, o, d, "POST", "reconcile", body, ""); r.Code == http.StatusOK {
		t.Fatal("busy export was declared settled")
	}
	d.mu.Lock()
	d.exportBusy = false
	d.mu.Unlock()
	if r := officeTestBridge(t, o, d, "POST", "reconcile", body, ""); r.Code != http.StatusOK {
		t.Fatal(r.Body.String())
	}
	if v := d.view(); v.Blocked || !v.Dirty || v.Revision != 0 || v.PersistedEpoch != 0 {
		t.Fatal("export recovery falsely marked canvas saved")
	}
	_, err := o.save(context.Background(), d, op.id)
	if err == nil {
		t.Fatal("old save ID was replayed or falsely recovered as successful")
	}
}

func TestOfficeRecoveryRequiresNewVerifiedReadNotCachedOrMalformedSnapshot(t *testing.T) {
	o, d, _ := officeTestService(t)
	instance := officeTestReady(t, o, d)
	op := officeRecoveryUnknown(t, o, d, instance, "unknown_readproof_001", "history.undo", json.RawMessage(`{}`))
	old := officeQueueCommand(t, o, d, "old_readproof_001", "word.read")
	officeTestDispatch(t, o, d, instance, "old_readproof_001")
	// This read started before the settled-native barrier. Its old snapshot
	// cannot prove what was observed after the unknown write ended.
	if r := officeTestBridge(t, o, d, "POST", "reconcile", officeRecoveryBody(d, instance, op, false, "EDITOR_OUTCOME_UNKNOWN"), ""); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	officeTestBridge(t, o, d, "POST", "results", map[string]any{"instanceId": instance, "operationId": "old_readproof_001", "ok": true, "result": map[string]any{"kind": "word", "text": "older"}}, "")
	if <-old != nil || !d.view().Blocked {
		t.Fatal("a pre-barrier read cleared recovery")
	}
	bad := officeQueueCommand(t, o, d, "bad_readproof_001", "word.read")
	officeTestDispatch(t, o, d, instance, "bad_readproof_001")
	officeTestBridge(t, o, d, "POST", "results", map[string]any{"instanceId": instance, "operationId": "bad_readproof_001", "ok": true, "result": map[string]any{}}, "")
	if <-bad != nil || !d.view().Blocked {
		t.Fatal("a malformed snapshot cleared recovery")
	}
	fresh := officeQueueCommand(t, o, d, "verified_readproof_001", "word.read")
	officeTestDispatch(t, o, d, instance, "verified_readproof_001")
	officeTestBridge(t, o, d, "POST", "results", map[string]any{"instanceId": instance, "operationId": "verified_readproof_001", "ok": true, "result": map[string]any{"kind": "word", "text": "current with manual notes"}}, "")
	if <-fresh != nil || d.view().Blocked {
		t.Fatal("new native read failed to restore readiness")
	}
}

func TestOfficeExcelRecoveryDiscoversExactSheetBeforeSmallNativeRead(t *testing.T) {
	o, d, _ := officeTestService(t)
	d.editor = "cell"
	instance := officeTestReady(t, o, d)
	cap := officeTestCapabilities("cell")
	cap["sheetList"] = true
	officeTestBridge(t, o, d, "POST", "ready", map[string]any{"instanceId": instance, "capabilities": cap}, "")
	op := officeRecoveryUnknown(t, o, d, instance, "unknown_sheet_history_001", "history.undo", json.RawMessage(`{}`))
	if r := officeTestBridge(t, o, d, "POST", "reconcile", officeRecoveryBody(d, instance, op, false, "EDITOR_OUTCOME_UNKNOWN"), ""); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if target := d.view().Recovery.ReadTarget; target.Operation != "sheet.list" {
		t.Fatal("recovery guessed a worksheet name")
	}
	listed := officeQueueCommand(t, o, d, "recovery_discover_001", "sheet.list")
	officeTestDispatch(t, o, d, instance, "recovery_discover_001")
	officeTestBridge(t, o, d, "POST", "results", map[string]any{"instanceId": instance, "operationId": "recovery_discover_001", "ok": true, "result": map[string]any{"kind": "cell", "sheets": []string{"人工重命名"}}}, "")
	if <-listed != nil || !d.view().Blocked {
		t.Fatal("names alone claimed the canvas had been read")
	}
	target := d.view().Recovery.ReadTarget
	if target.Operation != "sheet.read" || !bytes.Contains(target.Args, []byte("人工重命名")) {
		t.Fatal("recovery did not use actual native worksheet name")
	}
	read := make(chan error, 1)
	go func() {
		_, err := o.command(context.Background(), d, "recovery_cellproof_001", target.Operation, target.Args)
		read <- err
	}()
	officeTestQueued(t, d, "recovery_cellproof_001")
	officeTestDispatch(t, o, d, instance, "recovery_cellproof_001")
	officeTestBridge(t, o, d, "POST", "results", map[string]any{"instanceId": instance, "operationId": "recovery_cellproof_001", "ok": true, "result": map[string]any{"kind": "cell", "sheet": "人工重命名", "range": "A1", "inputs": [][]string{{"manual formula"}}}}, "")
	if <-read != nil || d.view().Blocked || !d.view().Dirty {
		t.Fatal("actual small read did not recover without claiming save")
	}
}

func TestOfficeSaveWaitsForRecoveryAndPreservesLaterManualEpoch(t *testing.T) {
	o, d, file := officeTestService(t)
	instance := officeTestReady(t, o, d)
	op := officeRecoveryUnknown(t, o, d, instance, "unknown_before_save_001", "history.undo", json.RawMessage(`{}`))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := o.save(ctx, d, "fresh_save_recovery_001"); done <- err }()
	time.Sleep(20 * time.Millisecond)
	d.mu.Lock()
	premature := d.exportIntent != nil
	d.mu.Unlock()
	if premature {
		t.Fatal("save began before native execution settled")
	}
	officeTestBridge(t, o, d, "POST", "state", map[string]any{"instanceId": instance, "dirty": true}, "")
	if r := officeTestBridge(t, o, d, "POST", "reconcile", officeRecoveryBody(d, instance, op, true, ""), ""); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	save := officeTestQueued(t, d, "fresh_save_recovery_001")
	officeTestDispatch(t, o, d, instance, save.id)
	data := officeTestPackage(t, "agent change and manual input before export")
	// Human input after this export remains dirty even though the earlier
	// shared canvas snapshot is successfully saved.
	officeTestBridge(t, o, d, "POST", "state", map[string]any{"instanceId": instance, "dirty": true}, "")
	if err := o.commitExport(d, save, instance, data); err != nil {
		t.Fatal(err)
	}
	if <-done != nil {
		t.Fatal("save waiting for recovery failed")
	}
	disk, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(disk, data) || !d.view().Dirty || d.view().PersistedEpoch >= d.view().DirtyEpoch {
		t.Fatal("recovered save lost document or claimed later human input persisted")
	}
}

func TestOfficeRecoveryWaitDoesNotHoldDocumentOrServiceLock(t *testing.T) {
	o, d, _ := officeTestService(t)
	instance := officeTestReady(t, o, d)
	op := officeRecoveryUnknown(t, o, d, instance, "unknown_before_batch_001", "history.undo", json.RawMessage(`{}`))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- o.waitRecovery(ctx, d) }()
	// Listing and barrier processing both need document/service mutexes.
	if len(o.list(d.conversationID, nil)) != 1 {
		t.Fatal("recovery waiting blocked ordinary listing")
	}
	if r := officeTestBridge(t, o, d, "POST", "reconcile", officeRecoveryBody(d, instance, op, true, ""), ""); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if <-done != nil {
		t.Fatal("bounded batch recovery wait did not resume")
	}
}

func TestOfficeNextKnownFlightRecoversLostReceiptAcknowledgementWithoutReplaying(t *testing.T) {
	o, d, _ := officeTestService(t)
	instance := officeTestReady(t, o, d)
	done := officeQueueCommand(t, o, d, "known_write_ack_001", "history.undo")
	officeTestDispatch(t, o, d, instance, "known_write_ack_001")
	// The server accepted the native result even if its HTTP acknowledgement
	// never reaches the view. next exposes that authoritative completion.
	officeQueueReceipt(t, o, d, instance, "known_write_ack_001", true, "")
	if <-done != nil {
		t.Fatal("known native write did not finish")
	}
	for _, expectation := range []struct {
		id    string
		known bool
	}{
		{"known_write_ack_001", true},
		{"unknown_write_ack_001", false},
	} {
		if !expectation.known {
			officeRecoveryUnknown(t, o, d, instance, expectation.id, "history.redo", json.RawMessage(`{}`))
		}
		response := officeTestBridge(t, o, d, "GET", "next", nil, instance+"&inFlightId="+expectation.id)
		var body struct {
			Command any `json:"command"`
			Flight  struct {
				ID        string `json:"operationId"`
				Known     bool   `json:"known"`
				Finished  bool   `json:"finished"`
				Succeeded bool   `json:"succeeded"`
			} `json:"flight"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Command != nil || !body.Flight.Finished || body.Flight.ID != expectation.id || body.Flight.Known != expectation.known || body.Flight.Succeeded != expectation.known {
			t.Fatal("lost acknowledgement confused a known result with unknown execution")
		}
	}
	if len(d.queue) != 2 || d.view().DirtyEpoch != 2 {
		t.Fatal("completion inspection replayed the write or counted it twice")
	}
}

func TestOfficeKnownRejectedFlightDoesNotAdvertiseSuccess(t *testing.T) {
	o, d, _ := officeTestService(t)
	instance := officeTestReady(t, o, d)
	done := officeQueueCommand(t, o, d, "known_rejected_ack_001", "history.undo")
	officeTestDispatch(t, o, d, instance, "known_rejected_ack_001")
	officeQueueReceipt(t, o, d, instance, "known_rejected_ack_001", false, "NO_HISTORY")
	if <-done != nil {
		t.Fatal("definite rejection did not finish")
	}
	response := officeTestBridge(t, o, d, "GET", "next", nil, instance+"&inFlightId=known_rejected_ack_001")
	var reply struct {
		Flight struct {
			Known     bool `json:"known"`
			Succeeded bool `json:"succeeded"`
		} `json:"flight"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &reply) != nil || !reply.Flight.Known || reply.Flight.Succeeded || d.view().Dirty || d.view().Blocked {
		t.Fatal("a definite failure was mistaken for unknown execution or successful editing")
	}
}
