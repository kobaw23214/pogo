package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
)

func officePresenceHarness(t *testing.T) (*muHarness, string, string, *officeDocumentSession) {
	t.Helper()
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return llm.Response{}, nil }, func(cfg *Config) {
		var err error
		cfg.Cwd, err = filepath.EvalSymlinks(cfg.Cwd)
		if err != nil {
			t.Fatal(err)
		}
		cfg.SessionDir, err = filepath.EvalSymlinks(cfg.SessionDir)
		if err != nil {
			t.Fatal(err)
		}
	})
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte(strings.Repeat("p", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	h.psrv.opts.Office = &OfficeOptions{ServiceURL: officeTestHealthyEngine(t), FetchURL: h.srv.URL, PublicURL: h.srv.URL, JWTSecretFile: secret}
	if err := h.psrv.initOffice(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.psrv.closeOffice)
	sid := h.createAs(aliceToken)
	s := h.session(sid)
	file := filepath.Join(s.cwd, "leased.docx")
	if err := os.WriteFile(file, officeTestPackage(t, "canonical"), 0600); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"path": "leased.docx", "conversation_id": sid, "instanceId": "writer_instance_original"})
	response := h.doAs(aliceToken, "POST", "/api/office/sessions", string(body))
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("open failed: %d %s", response.StatusCode, data)
	}
	d := h.psrv.office.findDocument(file)
	if d == nil {
		t.Fatal("editor claim absent")
	}
	return h, sid, file, d
}

func TestOfficePresenceHeartbeatRequiresExactWriterAndDoesNotRenewOnGet(t *testing.T) {
	h, sid, _, d := officePresenceHarness(t)
	d.mu.Lock()
	d.lastSeenAt = time.Now().Add(-2 * officeWriterLease)
	d.leaseExpiresAt = d.lastSeenAt.Add(officeWriterLease)
	before := d.leaseExpiresAt
	epoch := d.leaseEpoch
	d.mu.Unlock()
	for _, target := range []string{"/api/office/access?conversation_id=" + sid + "&path=leased.docx", "/api/office/sessions/" + d.id + "/status"} {
		response := h.doAs(aliceToken, "GET", target, "")
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("status %d", response.StatusCode)
		}
	}
	d.mu.Lock()
	if !before.Equal(d.leaseExpiresAt) {
		t.Fatal("read-only status renewed writer lease")
	}
	d.mu.Unlock()
	for _, test := range []struct {
		token, instance string
		epoch           uint64
		status          int
	}{{bobToken, "writer_instance_original", epoch, 404}, {aliceToken, "wrong_writer_instance", epoch, 409}, {aliceToken, "writer_instance_original", epoch + 1, 409}, {aliceToken, "writer_instance_original", epoch, 200}} {
		body, _ := json.Marshal(map[string]any{"instanceId": test.instance, "leaseEpoch": test.epoch})
		response := h.doAs(test.token, "POST", "/api/office/sessions/"+d.id+"/heartbeat", string(body))
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != test.status {
			t.Fatalf("heartbeat %d wanted %d: %s", response.StatusCode, test.status, data)
		}
	}
	if d.view().ConnectionState != "online" {
		t.Fatal("owned heartbeat did not renew")
	}
}

func TestOfficePresenceInitialClaimIsDurable(t *testing.T) {
	h, _, _, d := officePresenceHarness(t)
	records, err := h.psrv.office.loadEditorRecords()
	if err != nil || len(records) != 1 || records[0].ID != d.id {
		t.Fatalf("durable claim: %v count=%d", err, len(records))
	}
}

func officePresenceExpire(t *testing.T, o *officeService, d *officeDocumentSession) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastSeenAt = time.Now().Add(-3 * officeWriterLease)
	d.leaseExpiresAt = time.Now().Add(-officeWriterLease)
	if err := o.persistEditorLocked(d); err != nil {
		t.Fatal(err)
	}
}

func officePresenceTakeoverBody(t *testing.T, sid, file string, d *officeDocumentSession, instance string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	v := d.view()
	p := officeTakeoverRequest{ConversationID: sid, Path: "leased.docx", InstanceID: instance, ExpectedSessionID: v.ID, ExpectedGeneration: v.Generation, ExpectedDirtyEpoch: v.DirtyEpoch, ExpectedVersion: officeHash(data), ExpectedLeaseEpoch: v.LeaseEpoch}
	if v.RecoveryCopy != nil {
		p.RecoveryID = v.RecoveryCopy.ID
	}
	body, _ := json.Marshal(p)
	return string(body)
}

func TestOfficePresenceOneCleanTakeoverWinsAndOldWriterIsFenced(t *testing.T) {
	h, sid, file, d := officePresenceHarness(t)
	canonicalBefore, _ := os.ReadFile(file)
	officePresenceExpire(t, h.psrv.office, d)
	first := officePresenceTakeoverBody(t, sid, file, d, "takeover_instance_one")
	second := officePresenceTakeoverBody(t, sid, file, d, "takeover_instance_two")
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for _, body := range []string{first, second} {
		wg.Add(1)
		go func(body string) {
			defer wg.Done()
			response := h.doAs(aliceToken, "POST", "/api/office/takeover", body)
			response.Body.Close()
			results <- response.StatusCode
		}(body)
	}
	wg.Wait()
	close(results)
	success, refusal := 0, 0
	for status := range results {
		if status == 200 {
			success++
		} else if status == 409 {
			refusal++
		} else {
			t.Fatalf("takeover status %d", status)
		}
	}
	if success != 1 || refusal != 1 {
		t.Fatalf("wins=%d refusals=%d", success, refusal)
	}
	fresh := h.psrv.office.findDocument(file)
	if fresh == nil || fresh.id == d.id || fresh.key == d.key || !d.view().Closed {
		t.Fatal("takeover retained old writer identity")
	}
	response := officeTestBridge(t, h.psrv.office, d, "POST", "state", map[string]any{"instanceId": "writer_instance_original", "dirty": true}, "")
	if response.Code != 409 {
		t.Fatal("old writer was allowed to change document state")
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(after, canonicalBefore) {
		t.Fatal("taking over modified canonical bytes")
	}
}

func TestOfficePresenceOnlineAndDirtyWritersAreNotSilentlyTakenOver(t *testing.T) {
	h, sid, file, d := officePresenceHarness(t)
	for _, dirty := range []bool{false, true} {
		if dirty {
			officePresenceExpire(t, h.psrv.office, d)
			d.mu.Lock()
			d.dirtyEpoch, d.persistedEpoch = 9, 3
			d.mu.Unlock()
		}
		response := h.doAs(aliceToken, "POST", "/api/office/takeover", officePresenceTakeoverBody(t, sid, file, d, "refused_takeover_instance"))
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 409 {
			t.Fatalf("takeover was not refused: %d %s", response.StatusCode, data)
		}
	}
	if d.view().Closed || d.view().DirtyEpoch != 9 || d.view().PersistedEpoch != 3 {
		t.Fatal("refusal discarded old unsaved state")
	}
}

func TestOfficePresenceAccessFindsSameOwnerAcrossConversationsAndDoesNotLeakToOthers(t *testing.T) {
	h, sid, _, d := officePresenceHarness(t)
	other := h.createAs(aliceToken)
	for _, test := range []struct {
		token, sid string
		status     int
	}{{aliceToken, other, 200}, {bobToken, sid, 404}} {
		response := h.doAs(test.token, "GET", "/api/office/access?conversation_id="+test.sid+"&path=leased.docx", "")
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != test.status {
			t.Fatalf("access %d wanted %d: %s", response.StatusCode, test.status, data)
		}
		if response.StatusCode == 200 {
			var reply struct {
				Writer *officeSessionView `json:"writer"`
			}
			if json.Unmarshal(data, &reply) != nil || reply.Writer == nil || reply.Writer.ID != d.id {
				t.Fatal("same-owner writer not found across conversations")
			}
			for _, secret := range []string{d.fileToken, d.bridgeToken, d.key, d.absolute} {
				if bytes.Contains(data, []byte(secret)) {
					t.Fatal("access leaked private editor capability")
				}
			}
		}
	}
}

func TestOfficePresenceJournalPreservesUnsavedAndUnknownStateAcrossRestart(t *testing.T) {
	o, d, file := officeTestService(t)
	officeTestReady(t, o, d)
	d.mu.Lock()
	d.dirtyEpoch, d.persistedEpoch, d.blocked = 9, 3, true
	if err := o.persistEditorLocked(d); err != nil {
		t.Fatal(err)
	}
	d.mu.Unlock()
	o.close()
	next := &officeService{cfg: o.cfg, batchDir: o.batchDir, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}}
	if err := next.restoreEditorJournal(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.close)
	restored := next.findDocument(file)
	if restored == nil {
		t.Fatal("restart dropped abandoned writer")
	}
	v := restored.view()
	if !v.Dirty || v.DirtyEpoch != 9 || v.PersistedEpoch != 3 || !v.Blocked || v.Ready || v.ConnectionState != "disconnected" {
		t.Fatalf("restart guessed saved/live state: %+v", v)
	}
	if err := (&Server{office: next}).bridgeIdentity(restored, "writer_instance_original"); err == nil {
		t.Fatal("old instance retained write access after server restart")
	}
	if officeCanTakeoverLocked(restored) {
		t.Fatal("orphan dirty writer was silently reclaimable")
	}
}

func officePresenceEngineCallback(t *testing.T, o *officeService, d *officeDocumentSession, status int, address, userdata string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"key": d.key, "status": status, "url": address}
	if userdata != "" {
		body["userdata"] = userdata
	}
	token, err := o.signJWT(body)
	if err != nil {
		t.Fatal(err)
	}
	body["token"] = token
	data, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "http://pogo.invalid/office/callbacks/"+d.id+"/"+d.fileToken, bytes.NewReader(data))
	r.SetPathValue("oid", d.id)
	r.SetPathValue("cap", d.fileToken)
	w := httptest.NewRecorder()
	(&Server{office: o}).handleOfficeCallback(w, r)
	return w
}

func TestOfficePresenceAutomaticCallbacksKeepDurableCopiesWithoutClaimingSaved(t *testing.T) {
	for _, status := range []int{2, 6} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			o, d, file := officeTestService(t)
			officeTestReady(t, o, d)
			changed := officeTestPackage(t, "unsaved recovered bytes")
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(changed) }))
			defer engine.Close()
			o.cfg.serviceURL = engine.URL
			o.cfg.serviceOrigin = engine.URL
			d.mu.Lock()
			d.dirtyEpoch, d.persistedEpoch = 9, 3
			d.mu.Unlock()
			before, _ := os.ReadFile(file)
			address := engine.URL + "/cache/files/data/" + d.key + "_1/output.docx"
			response := officePresenceEngineCallback(t, o, d, status, address, "")
			if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"error":0`)) {
				t.Fatalf("callback not accepted: %d %s", response.Code, response.Body)
			}
			d.mu.Lock()
			copy := *d.recoveryCopy
			dirty, persisted := d.dirtyEpoch, d.persistedEpoch
			sealed := d.recoverySealed
			d.mu.Unlock()
			if copy.State != "ready" || copy.Hash != officeHash(changed) || dirty != 9 || persisted != 3 || sealed != (status == 2) {
				t.Fatalf("false save/incorrect recovery: %+v dirty=%d persisted=%d sealed=%v", copy, dirty, persisted, sealed)
			}
			_, data, err := o.readArchivedRecoveryCopy(nil, d.panelPath, copy.ID)
			if err != nil || !bytes.Equal(data, changed) {
				t.Fatal("recovery copy not durable/readable")
			}
			after, _ := os.ReadFile(file)
			if !bytes.Equal(before, after) {
				t.Fatal("automatic callback overwrote canonical file")
			}
			response = officePresenceEngineCallback(t, o, d, status, address, "")
			if !bytes.Contains(response.Body.Bytes(), []byte(`"error":0`)) {
				t.Fatal("duplicate callback not acknowledged")
			}
			if d.view().RecoveryCopy.ID != copy.ID {
				t.Fatal("duplicate callback created a second copy")
			}
			o.close()
			next := &officeService{cfg: o.cfg, batchDir: o.batchDir, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}}
			if err = next.restoreEditorJournal(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(next.close)
			restored := next.findDocument(file)
			if restored == nil || restored.view().RecoveryCopy == nil || restored.view().PersistedEpoch != 3 {
				t.Fatal("restart lost recovery or guessed canonical saved")
			}
		})
	}
}

func TestOfficePresenceCallbackDoesNotAckBeforeExportAndJournalAreDurable(t *testing.T) {
	o, d, file := officeTestService(t)
	before, _ := os.ReadFile(file)
	for _, address := range []string{"https://unapproved.invalid/output.docx", o.cfg.serviceURL + "/cache/files/data/another-document/output.docx"} {
		response := officePresenceEngineCallback(t, o, d, 2, address, "")
		if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"error":1`)) {
			t.Fatal("unpreserved callback was acknowledged as success")
		}
	}
	if d.recoveryCopy != nil && d.recoveryCopy.State == "ready" {
		t.Fatal("invalid engine URL produced a recovery copy")
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("failed recovery changed canonical file")
	}
}

func TestOfficePresenceDirtyTakeoverNeedsExactDurableRecoveryAndPreservesIt(t *testing.T) {
	h, sid, file, d := officePresenceHarness(t)
	canonicalBefore, _ := os.ReadFile(file)
	officePresenceExpire(t, h.psrv.office, d)
	d.mu.Lock()
	d.dirtyEpoch, d.persistedEpoch, d.recoverySealed = 9, 3, true
	d.recoveryCopy = &officeRecoveryCopy{ID: officeNewID(), State: "pending", Title: "恢复副本-leased.docx", FileType: "docx", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), SourceSessionID: d.id, Generation: d.generation, DirtyEpoch: 9, PersistedEpoch: 3, BaseVersion: d.baseHash}
	changed := officeTestPackage(t, "uncommitted changes")
	if err := h.psrv.office.storeRecoveryCopyLocked(d, changed); err != nil {
		t.Fatal(err)
	}
	copyID := d.recoveryCopy.ID
	d.mu.Unlock()
	response := h.doAs(aliceToken, "POST", "/api/office/takeover", officePresenceTakeoverBody(t, sid, file, d, "recovered_takeover_instance"))
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("recovery handoff %d %s", response.StatusCode, data)
	}
	response = h.doAs(aliceToken, "GET", officeRecoveryDownloadURL(copyID, sid, "leased.docx"), "")
	data, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !bytes.Equal(data, changed) {
		t.Fatal("handoff lost isolated recovery copy")
	}
	before, _ := os.ReadFile(file)
	if !bytes.Equal(before, canonicalBefore) {
		t.Fatal("handoff silently committed recovery copy")
	}
	response = h.doAs(bobToken, "GET", officeRecoveryDownloadURL(copyID, sid, "leased.docx"), "")
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal("recovery copy leaked to other account")
	}
}

func TestOfficePresenceRecoveryManifestAndBlobAreImmutable(t *testing.T) {
	o, d, _ := officeTestService(t)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.recoverySealed = true
	d.recoveryCopy = &officeRecoveryCopy{ID: officeNewID(), State: "pending", SourceSessionID: d.id, Generation: d.generation, DirtyEpoch: d.dirtyEpoch, PersistedEpoch: d.persistedEpoch, BaseVersion: d.baseHash, FileType: "docx", Title: "recovered.docx"}
	if err := o.storeRecoveryCopyLocked(d, officeTestPackage(t, "first")); err != nil {
		t.Fatal(err)
	}
	if err := o.storeRecoveryCopyLocked(d, officeTestPackage(t, "different")); err == nil {
		t.Fatal("same recovery ID overwrote different bytes")
	}
}

func TestOfficePresenceTakeoverLostResponseIsIdempotentForExactInstanceAndCAS(t *testing.T) {
	h, sid, file, d := officePresenceHarness(t)
	officePresenceExpire(t, h.psrv.office, d)
	body := officePresenceTakeoverBody(t, sid, file, d, "idempotent_takeover_instance")
	r := httptest.NewRequest("POST", h.srv.URL+"/api/office/takeover", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+aliceToken)
	h.psrv.Handler().ServeHTTP(&officeLostConfigResponse{header: make(http.Header)}, r)
	fresh := h.psrv.office.findDocument(file)
	if fresh == nil || fresh.id == d.id {
		t.Fatal("first handoff did not commit")
	}
	response := h.doAs(aliceToken, "POST", "/api/office/takeover", body)
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || h.psrv.office.findDocument(file).id != fresh.id || !bytes.Contains(data, []byte(fresh.id)) {
		t.Fatalf("exact retry replaced writer: %d %s", response.StatusCode, data)
	}
	var p officeTakeoverRequest
	_ = json.Unmarshal([]byte(body), &p)
	p.InstanceID = "a_different_takeover_instance"
	changed, _ := json.Marshal(p)
	response = h.doAs(aliceToken, "POST", "/api/office/takeover", string(changed))
	response.Body.Close()
	if response.StatusCode != 409 {
		t.Fatal("different instance recovered another opener's handoff")
	}
}

func TestOfficePresenceRecoveryCommandIsSignedAndUnknownIsNeverBlindlyReplayed(t *testing.T) {
	h, _, _, d := officePresenceHarness(t)
	officePresenceExpire(t, h.psrv.office, d)
	d.mu.Lock()
	d.dirtyEpoch, d.persistedEpoch = 9, 3
	d.mu.Unlock()
	var calls atomic.Int32
	var forgottenReads atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/command" || r.URL.Query().Get("shardkey") != d.key {
			t.Error("recovery did not use the official keyed command route")
		}
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		var command string
		_ = json.Unmarshal(body["c"], &command)
		if command == "forcesave" {
			calls.Add(1)
		} else if command == "getForgotten" {
			forgottenReads.Add(1)
		} else {
			t.Error("unexpected recovery command")
		}
		var token string
		_ = json.Unmarshal(body["token"], &token)
		claims, err := h.psrv.office.verifyJWT(token)
		if err != nil || officeCanonicalRaw(claims["key"]) != officeCanonicalRaw(body["key"]) || officeCanonicalRaw(claims["userdata"]) != officeCanonicalRaw(body["userdata"]) {
			t.Error("force recovery was not signed for this key and ID")
		}
		writeJSON(w, 200, map[string]any{"error": 1})
	}))
	defer engine.Close()
	h.psrv.office.cfg.serviceURL, h.psrv.office.cfg.serviceOrigin = engine.URL, engine.URL
	v := d.view()
	body, _ := json.Marshal(officeRecoverRequest{ExpectedGeneration: v.Generation, ExpectedDirtyEpoch: 9, ExpectedLeaseEpoch: v.LeaseEpoch, ExpectedVersion: v.CanvasVersion})
	for i := 0; i < 2; i++ {
		response := h.doAs(aliceToken, "POST", "/api/office/sessions/"+d.id+"/recover", string(body))
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 || !bytes.Contains(data, []byte(`"state":"unknown"`)) {
			t.Fatalf("recovery unknown response: %d %s", response.StatusCode, data)
		}
	}
	if calls.Load() != 1 || forgottenReads.Load() != 1 || !d.view().RecoverySealed || d.view().PersistedEpoch != 3 {
		t.Fatal("unknown recovery was replayed or marked saved")
	}
	if _, err := h.psrv.office.saveAs(context.Background(), d, "refuse_sealed_save", officeActivityUser); err == nil {
		t.Fatal("sealed original writer was allowed to enqueue canonical save")
	}
}

func TestOfficePresenceLegacyImportRetainsExactEvidenceAndUnsavedEpochs(t *testing.T) {
	o, d, file := officeTestService(t)
	d.mu.Lock()
	d.dirtyEpoch, d.persistedEpoch = 9, 3
	record := officeEditorRecordLocked(d)
	d.mu.Unlock()
	dir, err := o.editorJournalRoot()
	if err != nil {
		t.Fatal(err)
	}
	evidence := []byte(`{"unsaved_content_recovered":false,"opaque_operation_cache_exported":false,"dirtyEpoch":9,"persistedEpoch":3}`)
	record.LegacyImported = true
	record.LegacyEvidenceFile = "legacy-" + d.id + ".evidence"
	record.LegacyEvidenceSHA256 = officeHash(evidence)
	record.FileToken = ""
	if err = officeVersionAtomicWrite(dir, record.LegacyEvidenceFile, evidence); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(record)
	if err = officeVersionAtomicWrite(dir, d.id+".json", data); err != nil {
		t.Fatal(err)
	}
	next := &officeService{cfg: o.cfg, batchDir: o.batchDir, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}}
	if err = next.restoreEditorJournal(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.close)
	loaded := next.findDocument(file)
	if loaded == nil || loaded.view().DirtyEpoch != 9 || loaded.view().PersistedEpoch != 3 || !loaded.legacyImported || loaded.fileToken != "" || officeCanTakeoverLocked(loaded) {
		t.Fatal("legacy import fabricated saved content or editor identity")
	}
	// Corrupt/missing original evidence can never silently unlock this record.
	if err = officeVersionAtomicWrite(dir, record.LegacyEvidenceFile, []byte("changed")); err != nil {
		t.Fatal(err)
	}
	if _, err = next.loadEditorRecords(); err == nil {
		t.Fatal("modified legacy evidence was accepted")
	}
}

func TestOfficePresenceMissingAbandonedFileDoesNotDisableOtherEditors(t *testing.T) {
	o, d, file := officeTestService(t)
	d.mu.Lock()
	d.dirtyEpoch = 2
	if err := o.persistEditorLocked(d); err != nil {
		t.Fatal(err)
	}
	d.mu.Unlock()
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	next := &officeService{cfg: o.cfg, batchDir: o.batchDir, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}}
	if err := next.restoreEditorJournal(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.close)
	if next.findDocument(file) == nil || !next.findDocument(file).view().Blocked {
		t.Fatal("missing file's unsaved record was discarded")
	}
	other := filepath.Join(d.cwd, "other.docx")
	if err := os.WriteFile(other, officeTestPackage(t, "other"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := next.open(d.conversationID, d.cwd, nil, NewHub(), other, "other.docx", nil); err != nil {
		t.Fatalf("unrelated editor blocked: %v", err)
	}
}

func TestOfficePresenceSavedCopyLeavesOrphanUntouchedAndRetriesWithoutOverwriting(t *testing.T) {
	h, sid, file, d := officePresenceHarness(t)
	before, _ := os.ReadFile(file)
	d.mu.Lock()
	d.dirtyEpoch, d.persistedEpoch, d.recoverySealed = 9, 3, true
	d.mu.Unlock()
	body, _ := json.Marshal(map[string]any{"conversationId": sid, "path": "leased.docx", "expectedVersion": officeHash(before), "operationId": "saved_copy_operation_001"})
	var copyPath string
	for i := 0; i < 2; i++ {
		response := h.doAs(aliceToken, "POST", "/api/office/saved-copy", string(body))
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("copy response %d %s", response.StatusCode, data)
		}
		var reply struct {
			Path            string `json:"path"`
			IncludesUnsaved bool   `json:"includesUnsavedChanges"`
			Reused          bool   `json:"reused"`
		}
		_ = json.Unmarshal(data, &reply)
		if reply.Path == "" || reply.IncludesUnsaved || reply.Reused != (i == 1) {
			t.Fatal("copy result falsely included unsaved content or recreated retry")
		}
		if i == 0 {
			copyPath = reply.Path
		} else if copyPath != reply.Path {
			t.Fatal("stable copy request changed target")
		}
	}
	copyData, err := os.ReadFile(filepath.Join(filepath.Dir(file), filepath.Base(copyPath)))
	if err != nil || !bytes.Equal(copyData, before) {
		t.Fatal("saved copy was not exact canonical snapshot")
	}
	if d.view().Closed || d.view().DirtyEpoch != 9 || d.view().PersistedEpoch != 3 || !d.view().RecoverySealed {
		t.Fatal("creating saved copy retired or cleared orphan")
	}
	if err = os.WriteFile(filepath.Join(filepath.Dir(file), filepath.Base(copyPath)), officeTestPackage(t, "edited copy"), 0600); err != nil {
		t.Fatal(err)
	}
	response := h.doAs(aliceToken, "POST", "/api/office/saved-copy", string(body))
	response.Body.Close()
	if response.StatusCode != 409 {
		t.Fatal("retry overwrote edited saved copy")
	}
	response = h.doAs(bobToken, "POST", "/api/office/saved-copy", string(body))
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal("copy bypassed account scope")
	}
	if err = os.WriteFile(file, officeTestPackage(t, "new canonical"), 0600); err != nil {
		t.Fatal(err)
	}
	response = h.doAs(aliceToken, "POST", "/api/office/saved-copy", string(body))
	response.Body.Close()
	if response.StatusCode != 409 {
		t.Fatal("old-version copy request ignored source change")
	}
}

func TestOfficePresenceManagerThenServerCloseRetainsUnsavedJournal(t *testing.T) {
	h, _, file, d := officePresenceHarness(t)
	d.mu.Lock()
	d.dirtyEpoch, d.persistedEpoch = 9, 3
	if err := h.psrv.office.persistEditorLocked(d); err != nil {
		t.Fatal(err)
	}
	d.mu.Unlock()
	o := h.psrv.office
	// This is the actual production shutdown order, including Session.Close's
	// Office-role revocation before Server.Close reaches closeOffice.
	h.mgr.Close()
	h.psrv.Close()
	records, err := o.loadEditorRecords()
	if err != nil || len(records) != 1 || records[0].Closed || records[0].DirtyEpoch != 9 || records[0].PersistedEpoch != 3 {
		t.Fatalf("shutdown discarded dirty writer: err=%v records=%+v", err, records)
	}
	next := &officeService{cfg: o.cfg, manager: h.mgr, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}}
	if err = next.restoreEditorJournal(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.close)
	v := next.findDocument(file).view()
	if !v.Dirty || v.DirtyEpoch != 9 || v.PersistedEpoch != 3 || v.ConnectionState != "disconnected" {
		t.Fatal("real shutdown restored guessed saved state")
	}
}

func TestOfficePresenceUnexpectedConversationRevokeCannotReleaseDirtyFile(t *testing.T) {
	o, d, file := officeTestService(t)
	officeTestReady(t, o, d)
	d.mu.Lock()
	d.dirtyEpoch, d.persistedEpoch = 9, 3
	d.mu.Unlock()
	o.revokeConversation(d.conversationID)
	if o.findDocument(file) != d || d.view().Closed || d.view().ConnectionState != "disconnected" || d.view().PersistedEpoch != 3 {
		t.Fatal("unexpected revoke silently released unsaved writer")
	}
	if _, err := o.open("a-different-conversation", d.cwd, nil, NewHub(), file, d.panelPath, nil); err == nil {
		t.Fatal("another opener bypassed retained orphan")
	}
	records, err := o.loadEditorRecords()
	if err != nil || len(records) != 1 || records[0].Closed || records[0].DirtyEpoch != 9 {
		t.Fatal("unexpected revoke erased durable unsaved state")
	}
}

func TestOfficePresenceCanonicalSaveJournalsSettledStateBeforeNextHeartbeat(t *testing.T) {
	o, d, file := officeTestService(t)
	instance := officeTestReady(t, o, d)
	changed := officeTestPackage(t, "canonical save settled")
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(changed) }))
	defer engine.Close()
	o.cfg.serviceURL, o.cfg.serviceOrigin = engine.URL, engine.URL
	d.mu.Lock()
	d.dirtyEpoch = 2
	d.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := o.save(context.Background(), d, "save_settled_journal_001"); done <- err }()
	officeTestQueued(t, d, "save_settled_journal_001")
	officeTestDispatch(t, o, d, instance, "save_settled_journal_001")
	if err := o.exportCurrent(context.Background(), d, instance, "save_settled_journal_001", "docx", engine.URL+"/cache/files/data/"+d.key+"_42/output.docx"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	records, err := o.loadEditorRecords()
	if err != nil || len(records) != 1 || records[0].Pending || records[0].DirtyEpoch != 2 || records[0].PersistedEpoch != 2 {
		t.Fatalf("completed save persisted false unknown work: %v %+v", err, records)
	}
	next := &officeService{cfg: o.cfg, batchDir: o.batchDir, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}}
	if err = next.restoreEditorJournal(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.close)
	restored := next.findDocument(file)
	restored.mu.Lock()
	canTakeover := officeCanTakeoverLocked(restored)
	restored.mu.Unlock()
	if !canTakeover || restored.view().Blocked || restored.view().Dirty {
		t.Fatal("settled save became unknown without another heartbeat")
	}
}

func TestOfficePresenceLateFinalCallbackDoesNotResurrectClosedWriterJournal(t *testing.T) {
	o, d, file := officeTestService(t)
	canonical, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = o.closeDocument(d); err != nil {
		t.Fatal(err)
	}
	records, err := o.loadEditorRecords()
	if err != nil || len(records) != 0 {
		t.Fatal("clean close did not compact the writer journal")
	}
	recovered := officeTestPackage(t, "late final snapshot")
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(recovered) }))
	defer engine.Close()
	o.cfg.serviceURL, o.cfg.serviceOrigin = engine.URL, engine.URL
	address := engine.URL + "/cache/files/data/" + d.key + "_final/output.docx"
	for i := 0; i < 2; i++ {
		response := officePresenceEngineCallback(t, o, d, 2, address, "")
		if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"error":0`)) {
			t.Fatalf("late callback %d %s", response.Code, response.Body)
		}
		records, err = o.loadEditorRecords()
		if err != nil || len(records) != 0 {
			t.Fatal("late callback resurrected a closed writer record")
		}
	}
	copy := d.view().RecoveryCopy
	if copy == nil || copy.State != "ready" {
		t.Fatal("late final snapshot was not retained")
	}
	_, bytesOnDisk, err := o.readArchivedRecoveryCopy(nil, d.panelPath, copy.ID)
	if err != nil || !bytes.Equal(bytesOnDisk, recovered) {
		t.Fatal("compaction removed the immutable recovery copy")
	}
	next := &officeService{cfg: o.cfg, batchDir: o.batchDir, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}}
	if err = next.restoreEditorJournal(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.close)
	if next.findDocument(file) != nil {
		t.Fatal("restart restored a retired writer from its recovery archive")
	}
	_, bytesOnDisk, err = next.readArchivedRecoveryCopy(nil, d.panelPath, copy.ID)
	if err != nil || !bytes.Equal(bytesOnDisk, recovered) {
		t.Fatal("restart lost the independent recovery archive")
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(after, canonical) {
		t.Fatal("late final callback overwrote canonical file")
	}
}

func officePresencePendingRecovery(t *testing.T, o *officeService, d *officeDocumentSession, state string) string {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dirtyEpoch, d.persistedEpoch, d.recoverySealed = 9, 3, true
	d.recoveryCopy = &officeRecoveryCopy{ID: officeNewID(), State: state, Title: "恢复副本-" + d.title, FileType: d.fileType, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), SourceSessionID: d.id, Generation: d.generation, DirtyEpoch: 9, PersistedEpoch: 3, BaseVersion: d.baseHash}
	if err := o.persistEditorLocked(d); err != nil {
		t.Fatal(err)
	}
	return d.recoveryCopy.ID
}

func TestOfficePresenceSealedRecoveryAcceptsExactFinalCallbackWithoutUserdata(t *testing.T) {
	for _, state := range []string{"pending", "unknown"} {
		t.Run(state, func(t *testing.T) {
			o, d, file := officeTestService(t)
			officeTestReady(t, o, d)
			before, _ := os.ReadFile(file)
			id := officePresencePendingRecovery(t, o, d, state)
			recovered := officeTestPackage(t, "final snapshot after gateway revoke")
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(recovered) }))
			defer engine.Close()
			o.cfg.serviceURL, o.cfg.serviceOrigin = engine.URL, engine.URL
			response := officePresenceEngineCallback(t, o, d, 2, engine.URL+"/cache/files/data/"+d.key+"_final/output.docx", "")
			if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"error":0`)) {
				t.Fatalf("sealed final callback failed %d %s", response.Code, response.Body)
			}
			v := d.view()
			if v.RecoveryCopy == nil || v.RecoveryCopy.ID != id || v.RecoveryCopy.State != "ready" || v.RecoveryCopy.Source != "final" || v.RecoveryCopy.Coverage != "engine-snapshot" || v.DirtyEpoch != 9 || v.PersistedEpoch != 3 || v.Ready {
				t.Fatal("final callback changed identity/epochs or claimed canonical saved")
			}
			_, data, err := o.readArchivedRecoveryCopy(nil, d.panelPath, id)
			if err != nil || !bytes.Equal(data, recovered) {
				t.Fatal("final callback bytes not durable")
			}
			after, _ := os.ReadFile(file)
			if !bytes.Equal(before, after) {
				t.Fatal("final callback overwrote canonical")
			}
		})
	}
}

func TestOfficePresenceFinalCallbackCannotCompleteStaleSealedRecovery(t *testing.T) {
	o, d, _ := officeTestService(t)
	officePresencePendingRecovery(t, o, d, "pending")
	d.mu.Lock()
	d.recoveryCopy.DirtyEpoch = 8
	d.mu.Unlock()
	var reads atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reads.Add(1); w.Write(officeTestPackage(t, "stale")) }))
	defer engine.Close()
	o.cfg.serviceURL, o.cfg.serviceOrigin = engine.URL, engine.URL
	response := officePresenceEngineCallback(t, o, d, 2, engine.URL+"/cache/files/data/"+d.key+"_final/output.docx", "")
	if !bytes.Contains(response.Body.Bytes(), []byte(`"error":1`)) || reads.Load() != 0 || d.view().RecoveryCopy.State == "ready" || d.view().DirtyEpoch != 9 {
		t.Fatal("stale final callback adopted a newer recovery target")
	}
}

func TestOfficePresenceForceUnavailableReadsForgottenSnapshotWithoutCanonicalCommit(t *testing.T) {
	h, sid, file, d := officePresenceHarness(t)
	officePresenceExpire(t, h.psrv.office, d)
	before, _ := os.ReadFile(file)
	d.mu.Lock()
	d.dirtyEpoch, d.persistedEpoch = 9, 3
	d.mu.Unlock()
	recovered := officeTestPackage(t, "forgotten uncommitted marker")
	var force, reads atomic.Int32
	var engine *httptest.Server
	engine = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/command" {
			if r.URL.Query().Get("shardkey") != d.key {
				t.Error("forgotten query not bound to original key")
			}
			var p map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&p)
			var command, token string
			_ = json.Unmarshal(p["c"], &command)
			_ = json.Unmarshal(p["token"], &token)
			claims, err := h.psrv.office.verifyJWT(token)
			if err != nil || officeCanonicalRaw(claims["c"]) != officeCanonicalRaw(p["c"]) || officeCanonicalRaw(claims["key"]) != officeCanonicalRaw(p["key"]) {
				t.Error("recovery command signature not bound")
			}
			if command == "forcesave" {
				force.Add(1)
				writeJSON(w, 200, map[string]any{"error": 1, "key": d.key})
				return
			}
			if command == "getForgotten" {
				reads.Add(1)
				writeJSON(w, 200, map[string]any{"error": 0, "key": d.key, "url": engine.URL + "/cache/files/forgotten/" + d.key + "/output.docx/output.docx"})
				return
			}
			t.Error("unexpected engine command")
		} else if officeForgottenDocumentPath(r.URL.Path, d.key, "docx") {
			w.Write(recovered)
			return
		}
		http.NotFound(w, r)
	}))
	defer engine.Close()
	h.psrv.office.cfg.serviceURL, h.psrv.office.cfg.serviceOrigin = engine.URL, engine.URL
	v := d.view()
	body, _ := json.Marshal(officeRecoverRequest{ExpectedGeneration: v.Generation, ExpectedDirtyEpoch: 9, ExpectedLeaseEpoch: v.LeaseEpoch, ExpectedVersion: v.CanvasVersion})
	response := h.doAs(aliceToken, "POST", "/api/office/sessions/"+d.id+"/recover", string(body))
	payload, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("recover %d %s", response.StatusCode, payload)
	}
	v = d.view()
	if v.RecoveryCopy == nil || v.RecoveryCopy.State != "ready" || v.RecoveryCopy.Source != "forgotten" || v.RecoveryCopy.Coverage != "engine-snapshot" || force.Load() != 1 || reads.Load() != 1 || v.DirtyEpoch != 9 || v.PersistedEpoch != 3 {
		t.Fatalf("unexpected fallback outcome: %+v", v.RecoveryCopy)
	}
	response = h.doAs(aliceToken, "GET", officeRecoveryDownloadURL(v.RecoveryCopy.ID, sid, "leased.docx"), "")
	payload, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !bytes.Equal(payload, recovered) {
		t.Fatal("forgotten recovery copy not readable")
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("forgotten recovery committed original file")
	}
}

func TestOfficePresenceExplicitForgottenReadNeverForcesOrReplacesRecoveryID(t *testing.T) {
	h, _, _, d := officePresenceHarness(t)
	id := officePresencePendingRecovery(t, h.psrv.office, d, "unknown")
	recovered := officeTestPackage(t, "read-only forgotten recovery")
	var count atomic.Int32
	var engine *httptest.Server
	engine = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/command" {
			var p map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&p)
			var command string
			_ = json.Unmarshal(p["c"], &command)
			if command != "getForgotten" {
				t.Error("explicit read replayed a force mutation")
			}
			count.Add(1)
			writeJSON(w, 200, map[string]any{"error": 0, "key": d.key, "url": engine.URL + "/cache/files/forgotten/" + d.key + "/output.docx/output.docx"})
			return
		}
		w.Write(recovered)
	}))
	defer engine.Close()
	h.psrv.office.cfg.serviceURL, h.psrv.office.cfg.serviceOrigin = engine.URL, engine.URL
	v := d.view()
	body, _ := json.Marshal(officeRecoverRequest{ExpectedGeneration: v.Generation, ExpectedDirtyEpoch: 9, ExpectedLeaseEpoch: v.LeaseEpoch, ExpectedVersion: v.CanvasVersion, ReadForgotten: true})
	for i := 0; i < 2; i++ {
		response := h.doAs(aliceToken, "POST", "/api/office/sessions/"+d.id+"/recover", string(body))
		payload, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("read recovery %d %s", response.StatusCode, payload)
		}
	}
	if count.Load() != 1 || d.view().RecoveryCopy.ID != id || d.view().RecoveryCopy.State != "ready" {
		t.Fatal("read-only recovery replayed/replaced its original ID")
	}
}

func TestOfficePresenceForgottenReadUsesBoundedCooldownAndDoesNotForceNewClaim(t *testing.T) {
	h, _, _, d := officePresenceHarness(t)
	officePresencePendingRecovery(t, h.psrv.office, d, "unknown")
	var count atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&p)
		var command string
		_ = json.Unmarshal(p["c"], &command)
		if command != "getForgotten" {
			t.Error("forgotten retry issued mutation")
		}
		count.Add(1)
		writeJSON(w, 200, map[string]any{"error": 1, "key": d.key})
	}))
	defer engine.Close()
	h.psrv.office.cfg.serviceURL, h.psrv.office.cfg.serviceOrigin = engine.URL, engine.URL
	for i := 0; i < 4; i++ {
		_ = h.psrv.office.readForgottenRecovery(context.Background(), d, d.view().RecoveryCopy.ID)
	}
	if count.Load() != 1 {
		t.Fatal("read-only retries bypassed cooldown")
	}
	for i := 0; i < 4; i++ {
		d.mu.Lock()
		d.recoveryCopy.ForgottenLastAt = time.Now().Add(-2 * officeForgottenReadCooldown).UTC().Format(time.RFC3339Nano)
		d.mu.Unlock()
		_ = h.psrv.office.readForgottenRecovery(context.Background(), d, d.view().RecoveryCopy.ID)
	}
	if count.Load() != officeForgottenMaxReads || d.view().RecoveryCopy.ForgottenAttempts != officeForgottenMaxReads {
		t.Fatal("read-only retries exceeded durable attempt budget")
	}
}

func TestOfficeForgottenDocumentPathRequiresExactNamespaceKeyAndFormat(t *testing.T) {
	key := "pogo-test-original-key"
	valid := "/cache/files/forgotten/" + key + "/output.docx/output.docx"
	if !officeForgottenDocumentPath(valid, key, "docx") {
		t.Fatal("pinned forgotten shape rejected")
	}
	for _, path := range []string{"/cache/files/data/" + key + "/output.docx", strings.Replace(valid, key, key+"-other", 1), strings.Replace(valid, "forgotten", "forgotten-other", 1), strings.ReplaceAll(valid, "docx", "xlsx"), valid + "/extra", "/cache/files/forgotten/../" + key + "/output.docx/output.docx"} {
		if officeForgottenDocumentPath(path, key, "docx") {
			t.Fatalf("unsafe forgotten path accepted: %s", path)
		}
	}
}

func TestOfficePresenceConcurrentForceAndFinalCallbacksNeverMixReceiptAndBytes(t *testing.T) {
	for _, finalFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(finalFirst), func(t *testing.T) {
			o, d, file := officeTestService(t)
			before, _ := os.ReadFile(file)
			id := officePresencePendingRecovery(t, o, d, "pending")
			forceBytes, finalBytes := officeTestPackage(t, "earlier force checkpoint"), officeTestPackage(t, "latest final snapshot")
			forceEntered, finalEntered := make(chan struct{}), make(chan struct{})
			forceGate, finalGate := make(chan struct{}), make(chan struct{})
			var forceOnce, finalOnce sync.Once
			releaseForce := func() { forceOnce.Do(func() { close(forceGate) }) }
			releaseFinal := func() { finalOnce.Do(func() { close(finalGate) }) }
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "_force/") {
					close(forceEntered)
					<-forceGate
					w.Write(forceBytes)
				} else {
					close(finalEntered)
					<-finalGate
					w.Write(finalBytes)
				}
			}))
			defer engine.Close()
			defer releaseForce()
			defer releaseFinal()
			o.cfg.serviceURL, o.cfg.serviceOrigin = engine.URL, engine.URL
			forceResult, finalResult := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
			go func() {
				forceResult <- officePresenceEngineCallback(t, o, d, 6, engine.URL+"/cache/files/data/"+d.key+"_force/output.docx", id)
			}()
			select {
			case <-forceEntered:
			case <-time.After(3 * time.Second):
				t.Fatal("force callback did not reach its download")
			}
			go func() {
				finalResult <- officePresenceEngineCallback(t, o, d, 2, engine.URL+"/cache/files/data/"+d.key+"_final/output.docx", "")
			}()
			select {
			case <-finalEntered:
			case <-time.After(3 * time.Second):
				t.Fatal("final callback did not claim the same pending recovery")
			}
			var forceResponse, finalResponse *httptest.ResponseRecorder
			if finalFirst {
				releaseFinal()
				finalResponse = <-finalResult
				releaseForce()
				forceResponse = <-forceResult
			} else {
				releaseForce()
				forceResponse = <-forceResult
				if d.view().RecoveryCopy.State == "ready" {
					t.Fatal("earlier bytes were published under later final receipt")
				}
				releaseFinal()
				finalResponse = <-finalResult
			}
			if !bytes.Contains(forceResponse.Body.Bytes(), []byte(`"error":1`)) || !bytes.Contains(finalResponse.Body.Bytes(), []byte(`"error":0`)) {
				t.Fatalf("callback ACKs force=%s final=%s", forceResponse.Body, finalResponse.Body)
			}
			copy := d.view().RecoveryCopy
			if copy.ID != id || copy.Source != "final" || copy.Hash != officeHash(finalBytes) || copy.State != "ready" {
				t.Fatal("receipt metadata and durable bytes came from different callbacks")
			}
			_, stored, err := o.readArchivedRecoveryCopy(nil, d.panelPath, id)
			if err != nil || !bytes.Equal(stored, finalBytes) {
				t.Fatal("final bytes were acknowledged without durable matching snapshot")
			}
			after, _ := os.ReadFile(file)
			if !bytes.Equal(after, before) || d.view().DirtyEpoch != 9 || d.view().PersistedEpoch != 3 {
				t.Fatal("concurrent callbacks changed canonical file or dirty epochs")
			}
		})
	}
}

func TestOfficePresenceReadForgottenFlagCannotCreateOrForceUnsealedRecovery(t *testing.T) {
	h, _, _, d := officePresenceHarness(t)
	var commands atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		commands.Add(1)
		writeJSON(w, 200, map[string]any{"error": 0, "key": d.key})
	}))
	defer engine.Close()
	h.psrv.office.cfg.serviceURL, h.psrv.office.cfg.serviceOrigin = engine.URL, engine.URL
	v := d.view()
	body, _ := json.Marshal(officeRecoverRequest{ExpectedGeneration: v.Generation, ExpectedDirtyEpoch: v.DirtyEpoch, ExpectedLeaseEpoch: v.LeaseEpoch, ExpectedVersion: v.CanvasVersion, ReadForgotten: true})
	response := h.doAs(aliceToken, "POST", "/api/office/sessions/"+d.id+"/recover", string(body))
	response.Body.Close()
	if response.StatusCode != 409 || commands.Load() != 0 || d.view().RecoveryCopy != nil || d.view().RecoverySealed {
		t.Fatal("read-only flag created a claim or sent a force-save")
	}
}

func TestOfficePresenceForgottenReadRejectsOtherKeysAndOtherStoragePaths(t *testing.T) {
	for _, kind := range []string{"different-response-key", "different-url-key", "live-export-path"} {
		t.Run(kind, func(t *testing.T) {
			h, _, _, d := officePresenceHarness(t)
			officePresencePendingRecovery(t, h.psrv.office, d, "unknown")
			var downloads atomic.Int32
			var engine *httptest.Server
			engine = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/command" {
					downloads.Add(1)
					w.Write(officeTestPackage(t, "must not fetch"))
					return
				}
				key := d.key
				path := "/cache/files/forgotten/" + d.key + "/output.docx/output.docx"
				if kind == "different-response-key" {
					key += "-other"
				} else if kind == "different-url-key" {
					path = strings.Replace(path, d.key, d.key+"-other", 1)
				} else {
					path = "/cache/files/data/" + d.key + "_live/output.docx"
				}
				writeJSON(w, 200, map[string]any{"error": 0, "key": key, "url": engine.URL + path})
			}))
			defer engine.Close()
			h.psrv.office.cfg.serviceURL, h.psrv.office.cfg.serviceOrigin = engine.URL, engine.URL
			_ = h.psrv.office.readForgottenRecovery(context.Background(), d, d.view().RecoveryCopy.ID)
			if downloads.Load() != 0 || d.view().RecoveryCopy.State == "ready" || d.view().DirtyEpoch != 9 || d.view().PersistedEpoch != 3 {
				t.Fatal("forgotten read crossed identity/storage namespace")
			}
		})
	}
}

func TestOfficePresenceLateForceAfterForgottenReadyKeepsPrimaryAndArchivesEveryDistinctArtifact(t *testing.T) {
	o, d, file := officeTestService(t)
	officePresencePendingRecovery(t, o, d, "unknown")
	primaryBytes := officeTestPackage(t, "earlier forgotten snapshot")
	d.mu.Lock()
	d.recoveryCopy.Source, d.recoveryCopy.Coverage, d.recoveryCopy.EngineReceiptHash = "forgotten", "engine-snapshot", officeHash([]byte("earlier forgotten receipt"))
	if err := o.storeRecoveryCopyLocked(d, primaryBytes); err != nil {
		d.mu.Unlock()
		t.Fatal(err)
	}
	primary := *d.recoveryCopy
	primaryJSON, _ := json.Marshal(primary)
	canTakeover := officeCanTakeoverLocked(d)
	d.mu.Unlock()
	if !canTakeover {
		t.Fatal("fixture primary ready copy was not eligible for explicit handoff")
	}
	dir, _ := o.editorJournalRoot()
	journalBefore, err := officeVersionRead(dir, d.id+".json", officeMaxJSON)
	if err != nil {
		t.Fatal(err)
	}
	canonicalBefore, _ := os.ReadFile(file)
	firstBytes, secondBytes := officeTestPackage(t, "later force snapshot one"), officeTestPackage(t, "later force snapshot two")
	var bytesMu sync.Mutex
	served := firstBytes
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bytesMu.Lock()
		data := append([]byte(nil), served...)
		bytesMu.Unlock()
		w.Write(data)
	}))
	defer engine.Close()
	o.cfg.serviceURL, o.cfg.serviceOrigin = engine.URL, engine.URL
	address := engine.URL + "/cache/files/data/" + d.key + "_late_one/output.docx"
	for i := 0; i < 2; i++ {
		response := officePresenceEngineCallback(t, o, d, 6, address, primary.ID)
		if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"error":0`)) {
			t.Fatalf("late force callback %d %s", response.Code, response.Body)
		}
	}
	copies, err := o.recoveryCopiesFor(nil, d.panelPath)
	if err != nil || len(copies) != 2 {
		t.Fatalf("duplicate late force was not idempotent: %v count=%d", err, len(copies))
	}
	var late officeRecoveryCopy
	for _, copy := range copies {
		if copy.ID != primary.ID {
			late = copy
		}
	}
	if late.ID == "" || late.OriginalRecoveryID != primary.ID || late.Source != "forcesave" || late.Hash != officeHash(firstBytes) {
		t.Fatal("late force bytes were not independently associated with their original issued recovery")
	}
	bytesMu.Lock()
	served = secondBytes
	bytesMu.Unlock()
	response := officePresenceEngineCallback(t, o, d, 6, address, primary.ID)
	if !bytes.Contains(response.Body.Bytes(), []byte(`"error":1`)) {
		t.Fatal("same signed receipt with changed bytes was acknowledged")
	}
	_, stored, err := o.readArchivedRecoveryCopy(nil, d.panelPath, late.ID)
	if err != nil || !bytes.Equal(stored, firstBytes) {
		t.Fatal("conflicting repeated receipt replaced immutable late artifact")
	}
	response = officePresenceEngineCallback(t, o, d, 6, engine.URL+"/cache/files/data/"+d.key+"_late_two/output.docx", primary.ID)
	if !bytes.Contains(response.Body.Bytes(), []byte(`"error":0`)) {
		t.Fatal("second distinct late artifact was not preserved")
	}
	copies, err = o.recoveryCopiesFor(nil, d.panelPath)
	if err != nil || len(copies) != 3 {
		t.Fatal("distinct late receipt overwrote another archive")
	}
	d.mu.Lock()
	afterPrimaryJSON, _ := json.Marshal(d.recoveryCopy)
	canTakeover = officeCanTakeoverLocked(d)
	d.mu.Unlock()
	journalAfter, _ := officeVersionRead(dir, d.id+".json", officeMaxJSON)
	canonicalAfter, _ := os.ReadFile(file)
	if !bytes.Equal(primaryJSON, afterPrimaryJSON) || !bytes.Equal(journalBefore, journalAfter) || !bytes.Equal(canonicalBefore, canonicalAfter) || !canTakeover || d.view().DirtyEpoch != 9 || d.view().PersistedEpoch != 3 {
		t.Fatal("late archival changed primary handoff binding, writer journal, canonical or dirty epochs")
	}
	next := &officeService{cfg: o.cfg, batchDir: o.batchDir, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}}
	if err = next.restoreEditorJournal(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.close)
	if restored := next.findDocument(file); restored == nil || restored.view().RecoveryCopy.ID != primary.ID || restored.view().RecoveryCopy.Hash != primary.Hash {
		t.Fatal("restart promoted supplemental late artifact into primary takeover binding")
	}
	copies, err = next.recoveryCopiesFor(nil, d.panelPath)
	if err != nil || len(copies) != 3 {
		t.Fatal("restart lost separately preserved late artifacts")
	}
}

func TestOfficePresenceLateForceAfterHandoffBelongsOnlyToClosedOriginalWriter(t *testing.T) {
	h, sid, file, old := officePresenceHarness(t)
	officePresencePendingRecovery(t, h.psrv.office, old, "unknown")
	old.mu.Lock()
	old.recoveryCopy.Source, old.recoveryCopy.Coverage, old.recoveryCopy.EngineReceiptHash = "forgotten", "engine-snapshot", officeHash([]byte("fallback receipt"))
	if err := h.psrv.office.storeRecoveryCopyLocked(old, officeTestPackage(t, "original fallback")); err != nil {
		old.mu.Unlock()
		t.Fatal(err)
	}
	primaryID := old.recoveryCopy.ID
	old.mu.Unlock()
	response := h.doAs(aliceToken, "POST", "/api/office/takeover", officePresenceTakeoverBody(t, sid, file, old, "late_force_handoff_instance"))
	payload, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("handoff %d %s", response.StatusCode, payload)
	}
	fresh := h.psrv.office.findDocument(file)
	if fresh == nil || fresh.id == old.id || !old.view().Closed {
		t.Fatal("fixture handoff did not replace writer")
	}
	freshBefore, _ := json.Marshal(fresh.view())
	canonicalBefore, _ := os.ReadFile(file)
	lateBytes := officeTestPackage(t, "original writer late uncommitted snapshot")
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(lateBytes) }))
	defer engine.Close()
	h.psrv.office.cfg.serviceURL, h.psrv.office.cfg.serviceOrigin = engine.URL, engine.URL
	callback := officePresenceEngineCallback(t, h.psrv.office, old, 6, engine.URL+"/cache/files/data/"+old.key+"_late/output.docx", primaryID)
	if callback.Code != 200 || !bytes.Contains(callback.Body.Bytes(), []byte(`"error":0`)) {
		t.Fatalf("closed writer late archive %d %s", callback.Code, callback.Body)
	}
	freshAfter, _ := json.Marshal(fresh.view())
	canonicalAfter, _ := os.ReadFile(file)
	if !bytes.Equal(freshBefore, freshAfter) || !bytes.Equal(canonicalBefore, canonicalAfter) || h.psrv.office.findDocument(file) != fresh || old.view().DirtyEpoch != 9 || old.view().PersistedEpoch != 3 {
		t.Fatal("closed writer callback changed the new writer or canonical file")
	}
	records, err := h.psrv.office.loadEditorRecords()
	if err != nil || len(records) != 1 || records[0].ID != fresh.id {
		t.Fatal("supplemental archive resurrected a closed writer journal")
	}
	copies, err := h.psrv.office.recoveryCopiesFor(old.owner, old.panelPath)
	if err != nil || len(copies) != 2 {
		t.Fatal("closed writer late snapshot not independently retained")
	}
	var late officeRecoveryCopy
	for _, copy := range copies {
		if copy.ID != primaryID {
			late = copy
		}
	}
	response = h.doAs(aliceToken, "GET", officeRecoveryDownloadURL(late.ID, sid, "leased.docx"), "")
	payload, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !bytes.Equal(payload, lateBytes) {
		t.Fatal("closed writer supplemental artifact not authorized/readable")
	}
	next := &officeService{cfg: h.psrv.office.cfg, manager: h.mgr, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}}
	if err = next.restoreEditorJournal(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.close)
	if next.findDocument(file) == nil || next.findDocument(file).id != fresh.id {
		t.Fatal("restart resurrected closed old writer")
	}
	_, stored, err := next.readArchivedRecoveryCopy(old.owner, old.panelPath, late.ID)
	if err != nil || !bytes.Equal(stored, lateBytes) {
		t.Fatal("restart lost supplemental artifact of closed writer")
	}
}

func TestOfficePresenceLateForceRejectsUnissuedRecoveryAndChangedSourceGeneration(t *testing.T) {
	o, d, _ := officeTestService(t)
	officePresencePendingRecovery(t, o, d, "unknown")
	d.mu.Lock()
	d.recoveryCopy.EngineReceiptHash = officeHash([]byte("existing fallback"))
	if err := o.storeRecoveryCopyLocked(d, officeTestPackage(t, "fallback")); err != nil {
		d.mu.Unlock()
		t.Fatal(err)
	}
	id := d.recoveryCopy.ID
	d.mu.Unlock()
	var downloads atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		w.Write(officeTestPackage(t, "must not fetch"))
	}))
	defer engine.Close()
	o.cfg.serviceURL, o.cfg.serviceOrigin = engine.URL, engine.URL
	address := engine.URL + "/cache/files/data/" + d.key + "_late/output.docx"
	response := officePresenceEngineCallback(t, o, d, 6, address, officeNewID())
	if !bytes.Contains(response.Body.Bytes(), []byte(`"error":1`)) {
		t.Fatal("unissued recovery ID was accepted")
	}
	d.mu.Lock()
	d.generation++
	d.mu.Unlock()
	response = officePresenceEngineCallback(t, o, d, 6, address, id)
	if !bytes.Contains(response.Body.Bytes(), []byte(`"error":1`)) || downloads.Load() != 0 {
		t.Fatal("changed source generation accepted late recovery bytes")
	}
}

func TestOfficePresenceHandoffRestartThenFirstLateForceUsesOnlySealedCallbackIdentity(t *testing.T) {
	h, sid, file, old := officePresenceHarness(t)
	officePresencePendingRecovery(t, h.psrv.office, old, "unknown")
	old.mu.Lock()
	old.recoveryCopy.Source, old.recoveryCopy.Coverage, old.recoveryCopy.EngineReceiptHash = "forgotten", "engine-snapshot", officeHash([]byte("primary fallback"))
	if err := h.psrv.office.storeRecoveryCopyLocked(old, officeTestPackage(t, "primary fallback bytes")); err != nil {
		old.mu.Unlock()
		t.Fatal(err)
	}
	primaryID := old.recoveryCopy.ID
	old.mu.Unlock()
	response := h.doAs(aliceToken, "POST", "/api/office/takeover", officePresenceTakeoverBody(t, sid, file, old, "callback_only_handoff_instance"))
	payload, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("handoff %d %s", response.StatusCode, payload)
	}
	o := h.psrv.office
	fresh := o.findDocument(file)
	canonicalBefore, _ := os.ReadFile(file)
	dir, _ := o.editorJournalRoot()
	if _, err := officeVersionRead(dir, "callback-"+old.id+".json", officeMaxJSON); err != nil {
		t.Fatal("handoff did not persist narrowly sealed callback identity")
	}
	next := &officeService{cfg: o.cfg, manager: h.mgr, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}}
	if err := next.restoreEditorJournal(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.close)
	if next.sessions[old.id] != nil || next.findDocument(file).id != fresh.id {
		t.Fatal("restart promoted callback-only identity into an editor")
	}
	lateBytes := officeTestPackage(t, "first late force arrives only after core restart")
	var downloads atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); w.Write(lateBytes) }))
	defer engine.Close()
	next.cfg.serviceURL, next.cfg.serviceOrigin = engine.URL, engine.URL
	address := engine.URL + "/cache/files/data/" + old.key + "_late_after_restart/output.docx"
	callback := officePresenceEngineCallback(t, next, old, 6, address, primaryID)
	if callback.Code != 200 || !bytes.Contains(callback.Body.Bytes(), []byte(`"error":0`)) {
		t.Fatalf("first callback after restart %d %s", callback.Code, callback.Body)
	}
	if next.sessions[old.id] != nil || next.findDocument(file).id != fresh.id {
		t.Fatal("archiving callback resurrected a writer")
	}
	copies, err := next.recoveryCopiesFor(old.owner, old.panelPath)
	if err != nil || len(copies) != 2 {
		t.Fatal("post-restart late output was not archived")
	}
	var late officeRecoveryCopy
	for _, copy := range copies {
		if copy.ID != primaryID {
			late = copy
		}
	}
	_, stored, err := next.readArchivedRecoveryCopy(old.owner, old.panelPath, late.ID)
	if err != nil || !bytes.Equal(stored, lateBytes) {
		t.Fatal("post-restart late output bytes missing")
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(after, canonicalBefore) || late.DirtyEpoch != 9 || late.PersistedEpoch != 3 {
		t.Fatal("post-restart callback changed original canonical or dirty identity")
	}
	h.psrv.office = next
	defer func() { h.psrv.office = o }()
	for _, target := range []string{"/api/office/sessions/" + old.id + "/config?instanceId=unowned_callback_instance", "/api/office/sessions/" + old.id + "/status", "/office/files/" + old.id + "/" + old.fileToken, "/office/bridge/" + old.id + "/" + old.bridgeToken + "/config.json", "/office/bridge/" + old.id + "/" + old.bridgeToken + "/next?instanceId=writer_instance_original"} {
		response = h.doAs(aliceToken, "GET", target, "")
		response.Body.Close()
		if response.StatusCode != 404 {
			t.Fatalf("sealed callback identity authorized non-callback path: %s status=%d", target, response.StatusCode)
		}
	}
	forged := &officeDocumentSession{id: old.id, key: old.key, fileToken: "wrong_original_file_capability"}
	callback = officePresenceEngineCallback(t, next, forged, 6, address, primaryID)
	if callback.Code != 404 || downloads.Load() != 1 {
		t.Fatal("unknown callback capability loaded sealed authority")
	}
	records, err := next.loadEditorRecords()
	if err != nil || len(records) != 1 || records[0].ID != fresh.id {
		t.Fatal("callback-only receipt consumed active writer journal quota")
	}
}

func TestOfficePresenceCallbackIdentityWriteFailureKeepsClosedWriterFallback(t *testing.T) {
	h, sid, file, old := officePresenceHarness(t)
	officePresencePendingRecovery(t, h.psrv.office, old, "unknown")
	old.mu.Lock()
	old.recoveryCopy.Source, old.recoveryCopy.EngineReceiptHash = "forgotten", officeHash([]byte("known fallback"))
	if err := h.psrv.office.storeRecoveryCopyLocked(old, officeTestPackage(t, "preserved primary")); err != nil {
		old.mu.Unlock()
		t.Fatal(err)
	}
	primaryID := old.recoveryCopy.ID
	old.mu.Unlock()
	dir, _ := h.psrv.office.editorJournalRoot()
	if err := os.Mkdir(filepath.Join(dir, "callback-"+old.id+".json"), 0700); err != nil {
		t.Fatal(err)
	}
	response := h.doAs(aliceToken, "POST", "/api/office/takeover", officePresenceTakeoverBody(t, sid, file, old, "callback_write_failure_handoff"))
	payload, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("safe fallback handoff %d %s", response.StatusCode, payload)
	}
	data, err := officeVersionRead(dir, old.id+".json", officeMaxJSON)
	if err != nil {
		t.Fatal("identity write failure deleted the original closed writer index")
	}
	var kept officeEditorRecord
	if json.Unmarshal(data, &kept) != nil || !kept.Closed || kept.DirtyEpoch != 9 || kept.PersistedEpoch != 3 || kept.FileToken != old.fileToken {
		t.Fatal("identity write failure lost original capability/dirty evidence")
	}
	next := &officeService{cfg: h.psrv.office.cfg, manager: h.mgr, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}}
	if err = next.restoreEditorJournal(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.close)
	lateBytes := officeTestPackage(t, "late after callback identity storage failure")
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(lateBytes) }))
	defer engine.Close()
	next.cfg.serviceURL, next.cfg.serviceOrigin = engine.URL, engine.URL
	callback := officePresenceEngineCallback(t, next, old, 6, engine.URL+"/cache/files/data/"+old.key+"_late_fallback/output.docx", primaryID)
	if callback.Code != 200 || !bytes.Contains(callback.Body.Bytes(), []byte(`"error":0`)) {
		t.Fatalf("closed index did not retain callback capability: %d %s", callback.Code, callback.Body)
	}
	if next.sessions[old.id] != nil || next.findDocument(file) == nil || next.findDocument(file).id == old.id {
		t.Fatal("closed index fallback resurrected old writer")
	}
}

func TestOfficePresenceOrdinaryCleanCloseDoesNotAccumulateCallbackIdentities(t *testing.T) {
	o, d, file := officeTestService(t)
	for i := 0; i <= officeEditorJournalLimit; i++ {
		if i > 0 {
			var err error
			d, err = o.open("conversation-1", d.cwd, nil, NewHub(), file, "sample.docx", nil)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := o.closeDocument(d); err != nil {
			t.Fatal(err)
		}
	}
	records, err := o.loadEditorRecords()
	if err != nil || len(records) != 0 {
		t.Fatal("ordinary clean closes consumed writer journal capacity")
	}
	dir, _ := o.editorJournalRoot()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "callback-") {
			t.Fatal("ordinary clean close created an unneeded durable callback identity")
		}
	}
}
