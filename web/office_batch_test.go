package web

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
)

func officeBatchFixture(t *testing.T) (*officeService, *officeDocumentSession, *officeLink, string) {
	t.Helper()
	o, d, path := officeTestService(t)
	o.batchDir = officeCanonicalTestDir(t)
	l := o.link(d.conversationID, d.cwd, d.owner, d.hub).(*officeLink)
	return o, d, l, path
}
func officePrepared(t *testing.T, o *officeService, l *officeLink, path string) map[string]any {
	t.Helper()
	reply, err := o.prepareBatch(context.Background(), l, path, "prepare-operation-1", "batch", "")
	if err != nil {
		t.Fatal(err)
	}
	if reply["state"] != "prepared" {
		t.Fatalf("not prepared: %v", reply)
	}
	return reply
}
func officeWritePackage(t *testing.T, path, label string) []byte {
	t.Helper()
	data := officeTestPackage(t, label)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return data
}
func officeWaitFreeze(t *testing.T, d *officeDocumentSession) *officeFreezeView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v := d.view()
		if v.Batch != nil {
			return v.Batch
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("freeze never requested")
	return nil
}
func officeAckFreeze(t *testing.T, o *officeService, d *officeDocumentSession, instance string, f *officeFreezeView) int {
	t.Helper()
	v := d.view()
	return officeTestBridge(t, o, d, "POST", "batch-freeze", map[string]any{"instanceId": instance, "batchId": f.ID, "canvasVersion": v.CanvasVersion, "dirtyEpoch": v.DirtyEpoch, "persistedEpoch": v.PersistedEpoch, "frozen": true}, "").Code
}
func TestOfficeBatchActualStageCommitAndIdempotency(t *testing.T) {
	o, d, l, path := officeBatchFixture(t)
	before, _ := os.ReadFile(path)
	p := officePrepared(t, o, l, path)
	stage := p["staged_path"].(string)
	if !filepath.IsAbs(stage) || filepath.Dir(filepath.Dir(stage)) != d.cwd {
		t.Fatal("stage escaped cwd")
	}
	changed := officeWritePackage(t, stage, "batch result")
	original, _ := os.ReadFile(path)
	if !bytes.Equal(original, before) {
		t.Fatal("prepare changed original")
	}
	result, err := o.commitBatch(context.Background(), l, p["batch_id"].(string), "commit-operation-1")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, changed) || result["state"] != "committed" {
		t.Fatal("did not commit exact staged bytes")
	}
	fresh := result["session"].(officeSessionView)
	if !d.view().Closed || d.view().ReplacedBy != fresh.ID || fresh.ID == d.id || fresh.CanvasVersion != officeHash(changed) || fresh.ReplacementPending || d.view().DiskVersion != fresh.CanvasVersion {
		t.Fatal("did not replace document identity")
	}
	st, _ := os.Stat(path)
	again, err := o.commitBatch(context.Background(), l, p["batch_id"].(string), "commit-operation-1")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(st, after) || again["session"].(officeSessionView).ID != fresh.ID {
		t.Fatal("retry repeated rename or created another editor")
	}
	if _, err = os.Stat(stage); !os.IsNotExist(err) {
		t.Fatal("owned staging file not removed")
	}
	_, err = o.commitBatch(context.Background(), l, p["batch_id"].(string), "other-commit-operation")
	officeExpectError(t, err, "OPERATION_ID_CONFLICT")
	_, err = o.abortBatch(l, p["batch_id"].(string), "abort-after-commit")
	officeExpectError(t, err, "BATCH_ALREADY_COMMITTED")
}
func TestOfficeBatchRejectsChangedSourceAndPreservesStage(t *testing.T) {
	o, d, l, path := officeBatchFixture(t)
	p := officePrepared(t, o, l, path)
	officeWritePackage(t, p["staged_path"].(string), "batch")
	external := officeWritePackage(t, path, "external newer")
	_, err := o.commitBatch(context.Background(), l, p["batch_id"].(string), "conflict-commit")
	officeExpectError(t, err, "FILE_CHANGED")
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, external) || d.view().Closed || !d.view().ExternalChange || d.view().Batch != nil {
		t.Fatal("conflict discarded external state or old canvas")
	}
	result, err := o.abortBatch(l, p["batch_id"].(string), "abort-conflict")
	if err != nil || result["state"] != "aborted" {
		t.Fatal(err)
	}
}
func TestOfficeBatchRejectsSourceReplacementWithSameBytes(t *testing.T) {
	o, _, l, path := officeBatchFixture(t)
	p := officePrepared(t, o, l, path)
	data, _ := os.ReadFile(path)
	tmp := path + ".replacement"
	os.WriteFile(tmp, data, 0600)
	os.Rename(tmp, path)
	_, err := o.commitBatch(context.Background(), l, p["batch_id"].(string), "inode-commit")
	officeExpectError(t, err, "FILE_CHANGED")
}
func TestOfficeBatchRejectsInvalidPackageAndStageSymlink(t *testing.T) {
	for _, mode := range []string{"invalid", "symlink", "directory"} {
		t.Run(mode, func(t *testing.T) {
			o, _, l, path := officeBatchFixture(t)
			p := officePrepared(t, o, l, path)
			stage := p["staged_path"].(string)
			switch mode {
			case "invalid":
				os.WriteFile(stage, []byte("not a zip"), 0600)
			case "symlink":
				os.Remove(stage)
				os.Symlink(path, stage)
			case "directory":
				os.Rename(filepath.Dir(stage), filepath.Dir(stage)+"-moved")
				os.Mkdir(filepath.Dir(stage), 0700)
				officeWritePackage(t, stage, "foreign")
			}
			before, _ := os.ReadFile(path)
			if _, err := o.commitBatch(context.Background(), l, p["batch_id"].(string), "invalid-commit"); err == nil {
				t.Fatal("unsafe stage accepted")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("invalid stage overwrote source")
			}
			_, err := o.abortBatch(l, p["batch_id"].(string), "abort-invalid")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "directory" {
				if _, err = os.Stat(stage); err != nil {
					t.Fatal("abort deleted replacement directory")
				}
			}
		})
	}
}
func TestOfficeBatchFreezeRequiredAndLateEpochRejected(t *testing.T) {
	o, d, l, path := officeBatchFixture(t)
	instance := officeTestReady(t, o, d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := o.prepareBatch(ctx, l, path, "freeze-prepare", "batch", ""); result <- err }()
	f := officeWaitFreeze(t, d)
	select {
	case <-result:
		t.Fatal("prepare completed without editor ACK")
	default:
	}
	if code := officeAckFreeze(t, o, d, "foreign-instance", f); code != 409 {
		t.Fatal("foreign instance ACK accepted")
	}
	if code := officeAckFreeze(t, o, d, instance, f); code != 200 {
		t.Fatal("valid ACK rejected")
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.dirtyEpoch++
	d.mu.Unlock()
	_, err := o.commitBatch(context.Background(), l, f.ID, "late-input-commit")
	officeExpectError(t, err, "UNSAVED_DOCUMENT")
	if !d.view().Dirty || d.view().Closed {
		t.Fatal("late user input lost")
	}
	_, err = o.abortBatch(l, f.ID, "late-input-abort")
	if err != nil {
		t.Fatal(err)
	}
}
func TestOfficeBatchPrepareCancellationReleasesOnlyItsFreeze(t *testing.T) {
	o, d, l, path := officeBatchFixture(t)
	instance := officeTestReady(t, o, d)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := o.prepareBatch(ctx, l, path, "cancel-prepare", "batch", ""); done <- err }()
	f := officeWaitFreeze(t, d)
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled prepare succeeded")
	}
	if d.view().Batch != nil || d.view().Closed {
		t.Fatal("cancel left editor frozen or closed")
	}
	if officeAckFreeze(t, o, d, instance, f) != 409 {
		t.Fatal("late cancelled ACK accepted")
	}
}
func TestOfficeBatchFreezeRejectsDirtyAndHonorsExplicitRefusal(t *testing.T) {
	o, d, l, path := officeBatchFixture(t)
	instance := officeTestReady(t, o, d)
	d.mu.Lock()
	d.dirtyEpoch++
	d.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := o.prepareBatch(context.Background(), l, path, "dirty-prepare", "batch", "")
		done <- err
	}()
	f := officeWaitFreeze(t, d)
	if officeAckFreeze(t, o, d, instance, f) != 409 {
		t.Fatal("unsaved canvas ACK accepted")
	}
	reply := officeTestBridge(t, o, d, "POST", "batch-freeze", map[string]any{"instanceId": instance, "batchId": f.ID, "frozen": false, "error": "save failed"}, "")
	if reply.Code != 200 {
		t.Fatal(reply.Body.String())
	}
	officeExpectError(t, <-done, "FREEZE_REJECTED")
	if !d.view().Dirty || d.view().Batch != nil {
		t.Fatal("failure discarded dirty canvas or kept lock")
	}
}
func TestOfficeBatchDoesNotAllowOtherSessionOrOwner(t *testing.T) {
	o, d, l, path := officeBatchFixture(t)
	p := officePrepared(t, o, l, path)
	for _, other := range []*officeLink{o.link("another-conversation", d.cwd, nil, d.hub).(*officeLink), o.link(d.conversationID, d.cwd, &identity.Scope{}, d.hub).(*officeLink)} {
		_, err := o.batchFor(other, p["batch_id"].(string))
		officeExpectError(t, err, "BATCH_NOT_FOUND")
	}
	_, err := o.prepareBatch(context.Background(), l, path, "second-prepare", "batch", "")
	officeExpectError(t, err, "DOCUMENT_BATCH_ACTIVE")
	_, err = o.open(d.conversationID, d.cwd, d.owner, d.hub, path, "sample.docx", nil)
	officeExpectError(t, err, "DOCUMENT_BATCH_ACTIVE")
}
func TestOfficeBatchExternalChangeHashAndDirtyCanvasPreserved(t *testing.T) {
	o, d, _, path := officeBatchFixture(t)
	old, _ := os.Stat(path)
	d.mu.Lock()
	d.dirtyEpoch = 3
	d.mu.Unlock()
	data := officeWritePackage(t, path, "modified")
	os.Chtimes(path, old.ModTime(), old.ModTime())
	o.observeDisk(d)
	v := d.view()
	if !v.ExternalChange || !v.Dirty || v.DiskVersion != officeHash(data) || v.CanvasVersion == v.DiskVersion || v.ReloadAvailable {
		t.Fatal("external change obscured dirty canvas")
	}
	rev := v.SyncRevision
	o.observeDisk(d)
	if d.view().SyncRevision != rev {
		t.Fatal("unchanged observation churned sync revision")
	}
}
func TestOfficeBatchRestartReceiptAndInterruptedPrepare(t *testing.T) {
	o, d, l, path := officeBatchFixture(t)
	p := officePrepared(t, o, l, path)
	id := p["batch_id"].(string)
	fresh := &officeService{cfg: o.cfg, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}, batchDir: o.batchDir}
	defer fresh.close()
	link := fresh.link(d.conversationID, d.cwd, nil, NewHub()).(*officeLink)
	b, err := fresh.batchFor(link, id)
	if err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	err = fresh.reconcileBatch(b)
	state := b.record.State
	b.mu.Unlock()
	if err != nil || state != "interrupted" {
		t.Fatal("prepared restart silently resumed", err, state)
	}
	_, err = fresh.commitBatch(context.Background(), link, id, "restart-commit")
	officeExpectError(t, err, "BATCH_NOT_PREPARED")
	_, err = fresh.abortBatch(link, id, "restart-abort")
	if err != nil {
		t.Fatal(err)
	}
}
func TestOfficeBatchCommittedReceiptSurvivesRestart(t *testing.T) {
	o, d, l, path := officeBatchFixture(t)
	p := officePrepared(t, o, l, path)
	officeWritePackage(t, p["staged_path"].(string), "committed")
	_, err := o.commitBatch(context.Background(), l, p["batch_id"].(string), "persist-commit")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	fresh := &officeService{cfg: o.cfg, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}, batchDir: o.batchDir}
	defer fresh.close()
	link := fresh.link(d.conversationID, d.cwd, nil, NewHub()).(*officeLink)
	raw, err := link.executeBatch(context.Background(), officeToolRequest{Action: "batch_status", BatchID: p["batch_id"].(string)})
	if err != nil || !bytes.Contains(raw, []byte(`"state":"committed"`)) {
		t.Fatal("missing durable receipt", err)
	}
	reply, err := fresh.commitBatch(context.Background(), link, p["batch_id"].(string), "persist-commit")
	if err != nil || reply["session"] == nil {
		t.Fatal("committed canvas not recoverable", err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("restart replay rewrote committed bytes")
	}
}
func TestOfficeBatchCrashWindowReconciliation(t *testing.T) {
	for _, which := range []string{"before-rename", "after-rename", "foreign-same-bytes"} {
		t.Run(which, func(t *testing.T) {
			o, _, l, path := officeBatchFixture(t)
			p := officePrepared(t, o, l, path)
			b, _ := o.batchFor(l, p["batch_id"].(string))
			b.mu.Lock()
			data := officeTestPackage(t, "result")
			tmp := path + ".new"
			os.WriteFile(tmp, data, 0600)
			st, _ := os.Stat(tmp)
			b.record.CommitOperation = "crash-commit"
			b.record.State = "committing"
			b.record.ProposedVersion = officeHash(data)
			b.record.ProposedIdentity = officeFileIdentity(st)
			b.record.BootEpoch = "old-boot"
			if err := o.saveBatch(b); err != nil {
				t.Fatal(err)
			}
			if which == "after-rename" {
				os.Rename(tmp, path)
			} else if which == "foreign-same-bytes" {
				os.Remove(tmp)
				os.WriteFile(path, data, 0600)
			}
			err := o.reconcileBatch(b)
			state := b.record.State
			b.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"before-rename": "interrupted", "after-rename": "committed", "foreign-same-bytes": "outcome_unknown"}[which]
			if state != want {
				t.Fatalf("state %s != %s", state, want)
			}
		})
	}
}
func TestOfficeBatchOldExportCannotOverwriteNewVersion(t *testing.T) {
	o, d, l, path := officeBatchFixture(t)
	p := officePrepared(t, o, l, path)
	newBytes := officeWritePackage(t, p["staged_path"].(string), "new")
	_, err := o.commitBatch(context.Background(), l, p["batch_id"].(string), "guard-commit")
	if err != nil {
		t.Fatal(err)
	}
	op := &officeOperation{id: "late-old-export", deadline: time.Now().Add(time.Hour)}
	err = o.commitExport(d, op, "old-instance", officeTestPackage(t, "stale"))
	officeExpectError(t, err, "STALE_EXPORT")
	current, _ := os.ReadFile(path)
	if !bytes.Equal(current, newBytes) {
		t.Fatal("late export overwrote new version")
	}
}
func TestOfficeBatchReloadDirtyNeedsExactHumanConfirmation(t *testing.T) {
	o, d, l, path := officeBatchFixture(t)
	instance := officeTestReady(t, o, d)
	d.mu.Lock()
	d.dirtyEpoch = 2
	d.mu.Unlock()
	external := officeWritePackage(t, path, "external")
	_, err := o.reloadDocument(context.Background(), l, d, "tool-reload-operation", officeHash(external))
	officeExpectError(t, err, "UNSAVED_DOCUMENT")
	consent := &officeDiscardConfirmation{InstanceID: instance, CanvasVersion: d.view().CanvasVersion, DirtyEpoch: 2}
	done := make(chan error, 1)
	go func() {
		_, err := o.reloadDocumentConfirmed(context.Background(), l, d, "human-reload-operation", officeHash(external), consent)
		done <- err
	}()
	f := officeWaitFreeze(t, d)
	if !f.DiscardChanges || f.ConfirmedDirtyEpoch != 2 {
		t.Fatal("missing explicit confirmation scope")
	}
	if officeAckFreeze(t, o, d, instance, f) != 200 {
		t.Fatal("confirmed discard ACK refused")
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	v := d.view()
	if !v.Closed || !v.Dirty || v.ReplacedBy == "" {
		t.Fatal("discard masqueraded as save or did not replace")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, external) {
		t.Fatal("reload overwrote external version")
	}
}
func TestOfficeBatchReloadDiscardRejectsNewerInput(t *testing.T) {
	o, d, l, path := officeBatchFixture(t)
	instance := officeTestReady(t, o, d)
	d.mu.Lock()
	d.dirtyEpoch = 2
	d.mu.Unlock()
	external := officeWritePackage(t, path, "external")
	consent := &officeDiscardConfirmation{InstanceID: instance, CanvasVersion: d.view().CanvasVersion, DirtyEpoch: 2}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := o.reloadDocumentConfirmed(ctx, l, d, "human-discard-race", officeHash(external), consent)
		done <- err
	}()
	f := officeWaitFreeze(t, d)
	d.mu.Lock()
	d.dirtyEpoch++
	d.mu.Unlock()
	if officeAckFreeze(t, o, d, instance, f) != 409 {
		t.Fatal("newer human input discarded")
	}
	cancel()
	<-done
	if d.view().Closed || !d.view().Dirty {
		t.Fatal("failed discard lost canvas")
	}
}
func TestOfficeBatchReplacementRequiresReadback(t *testing.T) {
	o, old, l, path := officeBatchFixture(t)
	oldInstance := officeTestReady(t, o, old)
	prepared := make(chan map[string]any, 1)
	prepareErr := make(chan error, 1)
	go func() {
		reply, err := o.prepareBatch(context.Background(), l, path, "readback-prepare", "batch", "")
		prepared <- reply
		prepareErr <- err
	}()
	f := officeWaitFreeze(t, old)
	if officeAckFreeze(t, o, old, oldInstance, f) != 200 {
		t.Fatal("freeze refused")
	}
	p := <-prepared
	if err := <-prepareErr; err != nil {
		t.Fatal(err)
	}
	reply, err := o.commitBatch(context.Background(), l, p["batch_id"].(string), "readback-commit")
	if err != nil {
		t.Fatal(err)
	}
	v := reply["session"].(officeSessionView)
	d := o.sessions[v.ID]
	instance := officeTestReady(t, o, d)
	_, err = o.save(context.Background(), d, "premature-save")
	officeExpectError(t, err, "RELOAD_UNVERIFIED")
	_, err = o.command(context.Background(), d, "premature-write", "word.replace", json.RawMessage(`{"search":"original","replacement":"x","expectedText":"original","expectedCount":1}`))
	officeExpectError(t, err, "RELOAD_UNVERIFIED")
	bad := officeTestBridge(t, o, d, "POST", "reload-ready", map[string]any{"instanceId": instance, "canvasVersion": v.CanvasVersion, "readOperationId": "missing-read"}, "")
	if bad.Code != 409 {
		t.Fatal("reload accepted without content read")
	}
	done := make(chan error, 1)
	go func() {
		_, err := o.command(context.Background(), d, "real-read-operation", "word.read", json.RawMessage(`{}`))
		done <- err
	}()
	op := officeTestQueued(t, d, "real-read-operation")
	officeTestDispatch(t, o, d, instance, op.id)
	rr := officeTestBridge(t, o, d, "POST", "results", map[string]any{"instanceId": instance, "operationId": op.id, "ok": true, "result": map[string]any{"kind": "word", "text": "original"}}, "")
	if rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	good := officeTestBridge(t, o, d, "POST", "reload-ready", map[string]any{"instanceId": instance, "canvasVersion": v.CanvasVersion, "readOperationId": op.id}, "")
	if good.Code != 200 || d.view().ReplacementPending || !d.view().ReloadConfirmed {
		t.Fatal("verified reload failed", good.Body.String())
	}
}

func TestOfficeBatchDiskObservationCacheAndForce(t *testing.T) {
	o, d, _, path := officeBatchFixture(t)
	o.observeDisk(d)
	d.mu.Lock()
	initial := d.diskReadCount
	d.mu.Unlock()
	for i := 0; i < 20; i++ {
		o.observeDisk(d)
	}
	d.mu.Lock()
	got := d.diskReadCount
	d.mu.Unlock()
	if got != initial {
		t.Fatal("unchanged poll re-read complete package")
	}
	st, _ := os.Stat(path)
	data, _ := os.ReadFile(path)
	data[len(data)-1] ^= 1
	os.WriteFile(path, data, 0600)
	os.Chtimes(path, st.ModTime(), st.ModTime())
	o.observeDiskVersion(d, true)
	if !d.view().ExternalChange || d.view().DiskVersion != officeHash(data) {
		t.Fatal("force failed same-metadata change")
	}
	d.mu.Lock()
	d.diskObservedAt = time.Now().Add(-31 * time.Second)
	before := d.diskReadCount
	d.mu.Unlock()
	o.observeDisk(d)
	d.mu.Lock()
	after := d.diskReadCount
	d.mu.Unlock()
	if after != before+1 {
		t.Fatal("periodic hash fallback missing")
	}
}
func TestOfficeBatchPreparedLeaseExpiryReleasesCanvas(t *testing.T) {
	o, d, l, path := officeBatchFixture(t)
	p := officePrepared(t, o, l, path)
	b, _ := o.batchFor(l, p["batch_id"].(string))
	b.mu.Lock()
	b.record.ExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if !o.expireBatchLocked(b) {
		t.Fatal("lease did not expire")
	}
	b.mu.Unlock()
	if d.view().Batch != nil || d.batchLease != "" || d.view().Closed {
		t.Fatal("expiry kept or destroyed canvas")
	}
	_, err := o.commitBatch(context.Background(), l, p["batch_id"].(string), "expired-commit")
	officeExpectError(t, err, "BATCH_NOT_PREPARED")
	if _, err = os.Stat(p["staged_path"].(string)); !os.IsNotExist(err) {
		t.Fatal("expired owned stage remained")
	}
}
func TestOfficeBatchAbortPendingFreezeUnblocksPrepare(t *testing.T) {
	o, d, l, path := officeBatchFixture(t)
	officeTestReady(t, o, d)
	done := make(chan error, 1)
	go func() {
		_, err := o.prepareBatch(context.Background(), l, path, "cancel-pending-freeze", "batch", "")
		done <- err
	}()
	f := officeWaitFreeze(t, d)
	reply, err := o.abortBatch(l, f.ID, "cancel-pending-abort")
	if err != nil || reply["state"] != "aborted" {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		officeExpectError(t, err, "BATCH_ABORTED")
	case <-time.After(time.Second):
		t.Fatal("abort waited for freeze timeout")
	}
	if d.view().Batch != nil {
		t.Fatal("abort did not release UI")
	}
}
func TestOfficeBatchParentSymlinkCannotEscapeWorkspace(t *testing.T) {
	o, d, l, _ := officeBatchFixture(t)
	inside := filepath.Join(d.cwd, "reports")
	os.Mkdir(inside, 0700)
	path := filepath.Join(inside, "报表.docx")
	officeWritePackage(t, path, "original")
	p := officePrepared(t, o, l, path)
	outside := t.TempDir()
	victim := filepath.Join(outside, "报表.docx")
	before := officeWritePackage(t, victim, "must remain")
	os.Rename(inside, inside+"-old")
	os.Symlink(outside, inside)
	_, err := o.commitBatch(context.Background(), l, p["batch_id"].(string), "outside-parent-commit")
	if err == nil {
		t.Fatal("escaped through replaced parent")
	}
	got, _ := os.ReadFile(victim)
	if !bytes.Equal(got, before) {
		t.Fatal("modified outside workspace")
	}
}
func TestOfficeBatchReplacementConstructionFailureKeepsOldCanvas(t *testing.T) {
	o, d, l, path := officeBatchFixture(t)
	p := officePrepared(t, o, l, path)
	b, _ := o.batchFor(l, p["batch_id"].(string))
	b.mu.Lock()
	b.record.DiskVersion = b.record.BaseVersion
	os.Remove(path)
	_, err := o.replaceBatchDocument(l, b)
	b.mu.Unlock()
	if err == nil || d.view().Closed || d.view().ReplacedBy != "" {
		t.Fatal("failed new canvas retired old")
	}
}

func TestOfficeBatchCompletionUsesOnlyActualOldCanvasLedger(t *testing.T) {
	o, d, l, path := officeBatchFixture(t)
	// Real prepare establishes the owner/cwd/old-session mapping. The completion
	// accessor must not infer any mutation or persistence from prepared state.
	p := officePrepared(t, o, l, path)
	id := p["batch_id"].(string)
	d.mu.Lock()
	d.dirtyEpoch = 7
	d.persistedEpoch = 3
	d.blocked = true
	d.mu.Unlock()
	for _, action := range []string{"batch_prepare", "batch_commit", "batch_abort", "batch_status"} {
		receipt := l.OfficeBatchCompletion(action, id, "prepare-operation-1")
		if receipt == nil || receipt.DocumentSessionID != d.id || receipt.PersistedEpoch != 3 || !receipt.Blocked || receipt.MutationEpoch != 0 || receipt.SaveAttempted {
			t.Fatalf("%s invented completion: %#v", action, receipt)
		}
	}
	b, _ := o.batchFor(l, id)
	b.mu.Lock()
	b.record.State = "failed"
	b.record.ErrorCode = "FREEZE_REJECTED"
	b.mu.Unlock()
	if r := l.OfficeBatchCompletion("batch_prepare", "ignored-id", "prepare-operation-1"); r == nil || r.PersistedEpoch != 3 || r.MutationEpoch != 0 {
		t.Fatal("failed batch concealed or invented actual ledger state")
	}
}
func TestOfficeBatchCompletionSurvivesRetirementButNeverReadsReceiptFiles(t *testing.T) {
	o, d, l, path := officeBatchFixture(t)
	p := officePrepared(t, o, l, path)
	id := p["batch_id"].(string)
	d.mu.Lock()
	d.dirtyEpoch = 2
	d.persistedEpoch = 2
	d.mu.Unlock()
	if _, err := o.commitBatch(context.Background(), l, id, "completion-commit"); err != nil {
		t.Fatal(err)
	}
	if r := l.OfficeBatchCompletion("batch_commit", id, "completion-commit"); r == nil || r.DocumentSessionID != d.id || r.PersistedEpoch != 2 || r.MutationEpoch != 0 || !d.view().Closed {
		t.Fatal("retired old canvas lost saved epoch")
	}
	// A valid persisted receipt remains on disk. Clearing only the memory index
	// must make this bookkeeping accessor return nil, without loading that file.
	o.batchMu.Lock()
	o.batches = nil
	o.batchMu.Unlock()
	if r := l.OfficeBatchCompletion("batch_commit", id, "completion-commit"); r != nil {
		t.Fatal("completion loaded a persisted receipt")
	}
}
func TestOfficeBatchCompletionRejectsForeignOrRevokedScope(t *testing.T) {
	o, d, l, path := officeBatchFixture(t)
	p := officePrepared(t, o, l, path)
	id := p["batch_id"].(string)
	wrongOwner := *l
	wrongOwner.owner = &identity.Scope{}
	wrongCwd := *l
	wrongCwd.cwd = t.TempDir()
	wrongRole := *l
	wrongRole.role = &officeRole{}
	wrongSession := *l
	wrongSession.conversationID = "other-session"
	for _, other := range []*officeLink{&wrongOwner, &wrongCwd, &wrongRole, &wrongSession} {
		if r := other.OfficeBatchCompletion("batch_commit", id, "completion-commit"); r != nil {
			t.Fatal("foreign scope received old canvas receipt")
		}
	}
	if r := l.OfficeBatchCompletion("batch_prepare", id, "another-operation"); r != nil {
		t.Fatal("another prepare operation received receipt")
	}
	l.role.revoked.Store(true)
	if r := l.OfficeBatchCompletion("batch_commit", id, "completion-commit"); r != nil {
		t.Fatal("revoked role received receipt")
	}
	_ = d
}
