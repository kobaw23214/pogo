package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func officeResetTestRequest(t *testing.T, sid, file string, d *officeDocumentSession) officeResetRequest {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	v := d.view()
	return officeResetRequest{ConversationID: sid, Path: "leased.docx", InstanceID: "reset_writer_instance", ExpectedSessionID: v.ID, ExpectedGeneration: v.Generation, ExpectedDirtyEpoch: v.DirtyEpoch, ExpectedPersistedEpoch: v.PersistedEpoch, ExpectedLeaseEpoch: v.LeaseEpoch, ExpectedVersion: officeHash(data), ExpectedCanvasVersion: v.CanvasVersion, ConfirmDiscardChanges: true}
}
func officeResetTestDo(t *testing.T, h *muHarness, p officeResetRequest) (int, []byte) {
	t.Helper()
	body, _ := json.Marshal(p)
	r := h.doAs(aliceToken, "POST", "/api/office/reset", string(body))
	defer r.Body.Close()
	data, _ := io.ReadAll(r.Body)
	return r.StatusCode, data
}
func officeResetTestDirty(t *testing.T, o *officeService, d *officeDocumentSession) {
	t.Helper()
	d.mu.Lock()
	d.dirtyEpoch, d.persistedEpoch, d.blocked, d.restarted = 9, 3, true, true
	d.saveState = "error"
	d.mu.Unlock()
	officePresenceExpire(t, o, d)
}

func TestOfficeResetDirtyOriginalKeepsCanonicalEvidenceAndReusesLostResponse(t *testing.T) {
	h, sid, file, old := officePresenceHarness(t)
	officeResetTestDirty(t, h.psrv.office, old)
	old.mu.Lock()
	old.recoveryCopy = &officeRecoveryCopy{ID: officeNewID(), State: "unknown", SourceSessionID: old.id, Generation: old.generation, DirtyEpoch: 9, PersistedEpoch: 3, BaseVersion: old.baseHash}
	old.mu.Unlock()
	canonical, _ := os.ReadFile(file)
	p := officeResetTestRequest(t, sid, file, old)
	status, data := officeResetTestDo(t, h, p)
	if status != 200 {
		t.Fatalf("reset %d %s", status, data)
	}
	fresh := h.psrv.office.findDocument(file)
	if fresh == nil || fresh.id == old.id || fresh.key == old.key || fresh.fileToken == old.fileToken || fresh.bridgeToken == old.bridgeToken || !old.closed || old.dirtyEpoch != 9 || old.persistedEpoch != 3 {
		t.Fatal("reset reused old authority or fabricated its save")
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(canonical, after) || fresh.baseHash != officeHash(canonical) || fresh.dirtyEpoch != 0 || fresh.persistedEpoch != 0 {
		t.Fatal("reset changed saved file bytes")
	}
	dir, _ := h.psrv.office.editorJournalRoot()
	raw, err := officeVersionRead(dir, "reset-"+old.id+".json", officeMaxJSON)
	if err != nil {
		t.Fatal(err)
	}
	records, recordErr := h.psrv.office.loadEditorRecords()
	if recordErr != nil || len(records) != 1 || records[0].ID != fresh.id {
		t.Fatal("reset of unknown recovery retained a writer-quota record")
	}
	var audit officeResetAudit
	if json.Unmarshal(raw, &audit) != nil || audit.Original.DirtyEpoch != 9 || audit.Original.PersistedEpoch != 3 || !audit.Request.ConfirmDiscardChanges || audit.SavedVersion != officeHash(canonical) {
		t.Fatal("discard confirmation/evidence not durable")
	}
	status, data = officeResetTestDo(t, h, p)
	if status != 200 || h.psrv.office.findDocument(file).id != fresh.id {
		t.Fatalf("exact lost-response retry was not idempotent %d %s", status, data)
	}
	p.InstanceID = "different_reset_instance"
	status, _ = officeResetTestDo(t, h, p)
	if status != 409 {
		t.Fatal("old confirmation cleared the replacement writer")
	}
	if _, err = h.psrv.office.commandAs(context.Background(), old, "old_reset_command", "word.read", json.RawMessage(`{}`), officeActivityUser); err == nil {
		t.Fatal("reset revived old operation authority")
	}
	if err = h.psrv.office.commitExport(old, &officeOperation{id: "stale_reset_export"}, "writer_instance_original", officeTestPackage(t, "must never commit")); err == nil {
		t.Fatal("reset revived old canonical export")
	}
	next := &officeService{cfg: h.psrv.office.cfg, manager: h.mgr, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}}
	if err = next.restoreEditorJournal(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.close)
	if next.sessions[old.id] != nil || next.findDocument(file) == nil || next.findDocument(file).id != fresh.id {
		t.Fatal("restart resurrected abandoned editor")
	}
}

func TestOfficeResetExplicitDiscardOnlineConfirmationAndOwner(t *testing.T) {
	h, sid, file, d := officePresenceHarness(t)
	p := officeResetTestRequest(t, sid, file, d)
	p.ConfirmDiscardChanges = false
	status, _ := officeResetTestDo(t, h, p)
	if status != 400 {
		t.Fatal("missing explicit discard confirmation accepted")
	}
	p.ConfirmDiscardChanges = true
	status, _ = officeResetTestDo(t, h, p)
	if status != 409 {
		t.Fatal("online editor interrupted without explicit permission")
	}
	officePresenceExpire(t, h.psrv.office, d)
	p = officeResetTestRequest(t, sid, file, d)
	d.mu.Lock()
	d.touchWriterLocked()
	d.mu.Unlock()
	status, _ = officeResetTestDo(t, h, p)
	if status != 409 || h.psrv.office.findDocument(file) != d {
		t.Fatal("offline confirmation silently interrupted reconnected editor")
	}
	body, _ := json.Marshal(p)
	r := h.doAs(bobToken, "POST", "/api/office/reset", string(body))
	r.Body.Close()
	if r.StatusCode != 404 {
		t.Fatal("cross-owner reset disclosed or interrupted document")
	}
	p.AllowInterruptOnline = true
	status, data := officeResetTestDo(t, h, p)
	if status != 200 {
		t.Fatalf("confirmed online reset %d %s", status, data)
	}
}

func TestOfficeResetRejectsChangedCASAndInFlightWork(t *testing.T) {
	mutations := map[string]func(*officeDocumentSession){
		"dirty": func(d *officeDocumentSession) { d.dirtyEpoch++ }, "persisted": func(d *officeDocumentSession) { d.persistedEpoch++ }, "generation": func(d *officeDocumentSession) { d.generation++ }, "lease": func(d *officeDocumentSession) { d.leaseEpoch++ }, "canvas": func(d *officeDocumentSession) { d.baseHash = officeHash([]byte("newcanvas")) },
		"saving": func(d *officeDocumentSession) { d.saving = true }, "exporting": func(d *officeDocumentSession) { d.exportBusy = true }, "freeze": func(d *officeDocumentSession) { d.freeze = &officeFreeze{} }, "batch": func(d *officeDocumentSession) { d.batchLease = "activebatch" }, "replacement": func(d *officeDocumentSession) { d.replacementPending = true }, "pendingMutation": func(d *officeDocumentSession) {
			d.queue = append(d.queue, &officeOperation{id: "unfinished_reset_op", name: "word.replace", done: make(chan struct{})})
		},
	}
	for name, change := range mutations {
		t.Run(name, func(t *testing.T) {
			h, sid, file, d := officePresenceHarness(t)
			officePresenceExpire(t, h.psrv.office, d)
			p := officeResetTestRequest(t, sid, file, d)
			d.mu.Lock()
			change(d)
			d.mu.Unlock()
			status, _ := officeResetTestDo(t, h, p)
			if status != 409 || d.closed || h.psrv.office.findDocument(file) != d {
				t.Fatal("changed/inflight canvas was abandoned")
			}
			d.mu.Lock()
			d.saving, d.exportBusy = false, false
			d.freeze = nil
			d.batchLease = ""
			d.replacementPending = false
			d.queue = nil
			d.mu.Unlock()
		})
	}
	t.Run("disk", func(t *testing.T) {
		h, sid, file, d := officePresenceHarness(t)
		officePresenceExpire(t, h.psrv.office, d)
		p := officeResetTestRequest(t, sid, file, d)
		if err := os.WriteFile(file, officeTestPackage(t, "external revision"), 0600); err != nil {
			t.Fatal(err)
		}
		status, data := officeResetTestDo(t, h, p)
		if status != 409 || !bytes.Contains(data, []byte("FILE_CHANGED")) || d.closed {
			t.Fatalf("stale saved bytes reset %d %s", status, data)
		}
	})
}

func TestOfficeResetAuditFailureLeavesOriginalWriterAndEpochs(t *testing.T) {
	h, sid, file, d := officePresenceHarness(t)
	officeResetTestDirty(t, h.psrv.office, d)
	p := officeResetTestRequest(t, sid, file, d)
	dir, _ := h.psrv.office.editorJournalRoot()
	if err := os.Mkdir(filepath.Join(dir, "reset-"+d.id+".json"), 0700); err != nil {
		t.Fatal(err)
	}
	status, data := officeResetTestDo(t, h, p)
	if status != 503 || d.closed || h.psrv.office.findDocument(file) != d || d.dirtyEpoch != 9 || d.persistedEpoch != 3 {
		t.Fatalf("audit failure released writer %d %s", status, data)
	}
	records, err := h.psrv.office.loadEditorRecords()
	if err != nil || len(records) != 1 || records[0].Closed {
		t.Fatal("audit failure damaged original durable writer")
	}
}

func TestOfficeResetLegacyUnknownCanBeExplicitlyAbandonedWithoutFabricatedRecovery(t *testing.T) {
	h, sid, file, d := officePresenceHarness(t)
	officeResetTestDirty(t, h.psrv.office, d)
	d.mu.Lock()
	d.legacyImported = true
	d.fileToken = ""
	d.legacyEvidenceFile = "legacy-original.json"
	d.legacyEvidenceSHA256 = officeHash([]byte("unchanged evidence"))
	d.mu.Unlock()
	p := officeResetTestRequest(t, sid, file, d)
	status, data := officeResetTestDo(t, h, p)
	if status != 200 {
		t.Fatalf("legacy explicit reset %d %s", status, data)
	}
	dir, _ := h.psrv.office.editorJournalRoot()
	raw, err := officeVersionRead(dir, "reset-"+d.id+".json", officeMaxJSON)
	if err != nil {
		t.Fatal(err)
	}
	var audit officeResetAudit
	_ = json.Unmarshal(raw, &audit)
	if !audit.Original.LegacyImported || audit.Original.LegacyEvidenceFile != "legacy-original.json" || audit.Original.DirtyEpoch != 9 || audit.Original.PersistedEpoch != 3 || audit.Original.RecoveryCopy != nil || audit.Original.FileToken != "" {
		t.Fatal("legacy reset rewrote evidence or fabricated recovered content")
	}
}

func TestOfficeResetRestartLateFinalOnlyArchivesOriginalAndRejectsItsFileAuthority(t *testing.T) {
	h, sid, file, old := officePresenceHarness(t)
	officeResetTestDirty(t, h.psrv.office, old)
	p := officeResetTestRequest(t, sid, file, old)
	status, data := officeResetTestDo(t, h, p)
	if status != 200 {
		t.Fatalf("reset %d %s", status, data)
	}
	fresh := h.psrv.office.findDocument(file)
	canonical, _ := os.ReadFile(file)
	o := h.psrv.office
	next := &officeService{cfg: o.cfg, manager: h.mgr, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}}
	if err := next.restoreEditorJournal(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.close)
	engineBytes := officeTestPackage(t, "late old canvas after explicit abandon")
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(engineBytes) }))
	defer engine.Close()
	next.cfg.serviceURL, next.cfg.serviceOrigin = engine.URL, engine.URL
	callback := officePresenceEngineCallback(t, next, old, 2, engine.URL+"/cache/files/data/"+old.key+"_resetlate/output.docx", "")
	if callback.Code != 200 || !bytes.Contains(callback.Body.Bytes(), []byte(`"error":0`)) {
		t.Fatalf("reset archival callback %d %s", callback.Code, callback.Body)
	}
	copies, err := next.recoveryCopiesFor(old.owner, old.panelPath)
	if err != nil || len(copies) != 1 || copies[0].SourceSessionID != old.id || copies[0].DirtyEpoch != 9 || copies[0].PersistedEpoch != 3 {
		t.Fatal("late old content was lost or claimed saved")
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(canonical, after) || next.findDocument(file).id != fresh.id || next.sessions[old.id] != nil {
		t.Fatal("late reset callback changed current editor/file")
	}
	h.psrv.office = next
	defer func() { h.psrv.office = o }()
	for _, target := range []string{"/api/office/sessions/" + old.id + "/config?instanceId=wrong_reset_instance", "/office/files/" + old.id + "/" + old.fileToken, "/office/bridge/" + old.id + "/" + old.bridgeToken + "/config.json"} {
		r := h.doAs(aliceToken, "GET", target, "")
		r.Body.Close()
		if r.StatusCode != 404 {
			t.Fatal("discard audit revived old non-callback authority")
		}
	}
	p.InstanceID = "retry_after_real_restart"
	status, _ = officeResetTestDo(t, h, p)
	if status != 409 {
		t.Fatal("restart replay abandoned new writer")
	}
}

func TestOfficeResetAccessAdvertisesExplicitOnlineRisk(t *testing.T) {
	h, sid, _, d := officePresenceHarness(t)
	r := h.doAs(aliceToken, "GET", "/api/office/access?conversation_id="+sid+"&path=leased.docx", "")
	raw, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(raw), `"canReset":true`) || !strings.Contains(string(raw), `"resetRequiresOnlineConfirmation":true`) {
		t.Fatal("access did not advertise reset confirmation")
	}
	d.mu.Lock()
	d.exportBusy = true
	d.leaseExpiresAt = time.Now().Add(-time.Hour)
	d.mu.Unlock()
	r = h.doAs(aliceToken, "GET", "/api/office/access?conversation_id="+sid+"&path=leased.docx", "")
	raw, _ = io.ReadAll(r.Body)
	r.Body.Close()
	d.mu.Lock()
	d.exportBusy = false
	d.mu.Unlock()
	if !strings.Contains(string(raw), `"canReset":false`) || !strings.Contains(string(raw), `"resetRequiresOnlineConfirmation":false`) {
		t.Fatal("busy reset was advertised as allowed")
	}
}

func TestOfficeResetClosedRecordWriteFailureCannotReleaseWriter(t *testing.T) {
	h, sid, file, d := officePresenceHarness(t)
	officeResetTestDirty(t, h.psrv.office, d)
	p := officeResetTestRequest(t, sid, file, d)
	dir, _ := h.psrv.office.editorJournalRoot()
	index := filepath.Join(dir, d.id+".json")
	original, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(index, 0700); err != nil {
		t.Fatal(err)
	}
	status, data := officeResetTestDo(t, h, p)
	if status != 503 || d.closed || d.callbackOnly || h.psrv.office.findDocument(file) != d || d.dirtyEpoch != 9 || d.persistedEpoch != 3 {
		t.Fatalf("closed-record write failure released writer %d %s", status, data)
	}
	if err = os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(index, original, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestOfficeResetSameOwnerMayContinueInAnotherConversation(t *testing.T) {
	h, sid, file, d := officePresenceHarness(t)
	officeResetTestDirty(t, h.psrv.office, d)
	other := h.createAs(aliceToken)
	p := officeResetTestRequest(t, sid, file, d)
	p.ConversationID = other
	status, data := officeResetTestDo(t, h, p)
	if status != 200 || h.psrv.office.findDocument(file).conversationID != other {
		t.Fatalf("same owner cross-conversation reset %d %s", status, data)
	}
}

func TestOfficeResetRefreshAfterProvisionalAuditAndWriterFailureKeepsEveryConfirmation(t *testing.T) {
	h, sid, file, old := officePresenceHarness(t)
	officeResetTestDirty(t, h.psrv.office, old)
	p := officeResetTestRequest(t, sid, file, old)
	o := h.psrv.office
	dir, _ := o.editorJournalRoot()
	index := filepath.Join(dir, old.id+".json")
	original, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(index, 0700); err != nil {
		t.Fatal(err)
	}
	status, data := officeResetTestDo(t, h, p)
	if status != 503 || old.closed || o.findDocument(file) != old {
		t.Fatalf("first index failure %d %s", status, data)
	}
	firstAudit, err := officeVersionRead(dir, "reset-"+old.id+".json", officeMaxJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(index, original, 0600); err != nil {
		t.Fatal(err)
	}
	var first officeResetAudit
	_ = json.Unmarshal(firstAudit, &first)
	historyName := "reset-history-" + old.id + "-" + officeHash([]byte(first.Fingerprint)) + ".json"
	if err = os.Mkdir(filepath.Join(dir, historyName), 0700); err != nil {
		t.Fatal(err)
	}
	localRetry := p
	localRetry.InstanceID = "fresh_confirm_history_io_failure"
	status, data = officeResetTestDo(t, h, localRetry)
	unchanged, _ := officeVersionRead(dir, "reset-"+old.id+".json", officeMaxJSON)
	if status != 503 || old.closed || o.findDocument(file) != old || !bytes.Equal(firstAudit, unchanged) {
		t.Fatalf("history failure changed active/audit %d %s", status, data)
	}
	if err = os.Remove(filepath.Join(dir, historyName)); err != nil {
		t.Fatal(err)
	}
	next := &officeService{cfg: o.cfg, manager: h.mgr, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}}
	if err = next.restoreEditorJournal(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.close)
	h.psrv.office = next
	defer func() { h.psrv.office = o }()
	loaded := next.findDocument(file)
	if loaded == nil || loaded.id != old.id || loaded.dirtyEpoch != 9 || loaded.persistedEpoch != 3 {
		t.Fatal("failed attempt did not retain original after restart")
	}
	// Further edits after the failed attempt require a fresh matching CAS;
	// they must not be blocked by the older confirmation's dirty epoch.
	loaded.mu.Lock()
	loaded.dirtyEpoch = 10
	err = next.persistEditorLocked(loaded)
	loaded.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	refreshed := officeResetTestRequest(t, sid, file, loaded)
	refreshed.InstanceID = "fresh_confirmation_after_restart"
	status, data = officeResetTestDo(t, h, refreshed)
	if status != 200 {
		t.Fatalf("fresh confirmed retry %d %s", status, data)
	}
	var previous officeResetAudit
	_ = json.Unmarshal(firstAudit, &previous)
	history := "reset-history-" + old.id + "-" + officeHash([]byte(previous.Fingerprint)) + ".json"
	retained, err := officeVersionRead(dir, history, officeMaxJSON)
	if err != nil || !bytes.Equal(retained, firstAudit) {
		t.Fatal("refresh overwrote provisional confirmation")
	}
	current, err := officeVersionRead(dir, "reset-"+old.id+".json", officeMaxJSON)
	if err != nil || bytes.Equal(current, firstAudit) {
		t.Fatal("new confirmation was not independently recorded")
	}
	fresh := next.findDocument(file)
	status, data = officeResetTestDo(t, h, refreshed)
	if status != 200 || next.findDocument(file) != fresh {
		t.Fatalf("new confirmation retry not idempotent %d %s", status, data)
	}
	status, _ = officeResetTestDo(t, h, p)
	if status != 409 || next.findDocument(file) != fresh {
		t.Fatal("old provisional confirmation cleared replacement writer")
	}
}
