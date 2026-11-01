package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type informationViewTestReply struct {
	Session officeSessionView           `json:"session"`
	Config  map[string]json.RawMessage  `json:"config"`
	Source  informationOfficeViewSource `json:"source"`
}

func informationViewFixture(t *testing.T) (*muHarness, *officeService, string, []byte) {
	t.Helper()
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	o, _, _ := officeTestService(t)
	h.psrv.office = o
	data := officeTestPackage(t, "immutable source selection")
	var uploaded informationResult
	json.Unmarshal(infoUpload(t, h, aliceToken, "/api/information/files", "library.docx", string(data), map[string]string{"revision": "0"}, 201), &uploaded)
	if uploaded.Entry == nil {
		t.Fatal("missing uploaded file")
	}
	return h, o, uploaded.Entry.ID, data
}

func TestInformationOfficeViewUsesRealReadonlyConfigAndImmutableSource(t *testing.T) {
	h, o, id, original := informationViewFixture(t)
	request := map[string]any{"version": 1, "instanceId": "viewer_instance_001", "operationId": "viewer_operation_001"}
	target := "/api/information/entries/" + id + "/office-view"
	var opened informationViewTestReply
	json.Unmarshal(infoJSON(t, h, aliceToken, "POST", target, request, 200), &opened)
	v := opened.Session
	if !v.ReadOnly || v.Dirty || v.CanvasVersion != officeHash(original) || v.CanvasVersion != opened.Source.SHA256 || opened.Source.EntryID != id || opened.Source.Version != 1 || len(opened.Source.SnapshotSHA256) != 64 || opened.Source.Path == "" {
		t.Fatalf("unbound readonly source: %+v %+v", v, opened.Source)
	}
	var document struct {
		Permissions map[string]bool `json:"permissions"`
	}
	var editor struct {
		Mode     string `json:"mode"`
		Callback string `json:"callbackUrl"`
	}
	json.Unmarshal(opened.Config["document"], &document)
	json.Unmarshal(opened.Config["editorConfig"], &editor)
	if editor.Mode != "view" || editor.Callback != "" || document.Permissions["edit"] || document.Permissions["review"] || document.Permissions["download"] || !document.Permissions["copy"] {
		t.Fatal("not a genuine nonediting Office config")
	}
	o.mu.Lock()
	d := o.sessions[v.ID]
	active := len(o.active)
	o.mu.Unlock()
	if d == nil || d.parent != nil || d.cwd != "" || d.absolute != "" || d.journal != nil || active != 1 {
		t.Fatal("viewer acquired writer resources")
	}
	infoJSON(t, h, aliceToken, "PATCH", "/api/information/entries/"+id, map[string]any{"revision": 1, "name": "renamed.docx"}, 200)
	if d.informationSource.Title != "library.docx" || d.informationSource.Version != 1 {
		t.Fatal("historical snapshot followed current library metadata")
	}
	manifest := officeTestBridge(t, o, d, "GET", "config.json", nil, "")
	if manifest.Code != 200 || !bytes.Contains(manifest.Body.Bytes(), []byte(`"isViewer":true`)) {
		t.Fatal("viewer plugin unavailable", manifest.Body.String())
	}
	beforeFiles, _ := filepath.Glob(filepath.Join(o.batchDir, "editors", "*"))
	instance := "viewer_instance_001"
	caps := officeTestCapabilities(d.editor)
	caps["selectionRead"] = true
	if ready := officeTestBridge(t, o, d, "POST", "ready", map[string]any{"instanceId": instance, "capabilities": caps}, ""); ready.Code != 200 || !d.view().Ready {
		t.Fatal("viewer handshake failed", ready.Body.String())
	}
	binding := d.view()
	args, _ := json.Marshal(map[string]any{"documentId": binding.DocumentID, "instanceId": instance, "canvasVersion": binding.CanvasVersion, "dirtyEpoch": binding.DirtyEpoch})
	before := d.view()
	if !before.ReadOnly || len(before.SupportedOperations) != 2 || before.SupportedOperations[1] != "selection.read" {
		t.Fatal("viewer advertises writer operations", before.SupportedOperations)
	}
	done := officeSelectionQueue(t, o, d, "viewer_selection_001", args)
	officeTestDispatch(t, o, d, instance, "viewer_selection_001")
	r := officeTestBridge(t, o, d, "POST", "results", map[string]any{"instanceId": instance, "operationId": "viewer_selection_001", "ok": true, "result": map[string]any{"kind": "word", "text": "immutable source selection"}}, "")
	if r.Code != 200 || !bytes.Contains(<-done, []byte("immutable source selection")) {
		t.Fatal("actual selection bridge failed", r.Body.String())
	}
	for _, test := range []struct {
		action string
		body   any
	}{
		{"save", map[string]any{}}, {"recover", map[string]any{}}, {"reload", map[string]any{}},
		{"commands", map[string]any{"operationId": "viewer_mutation_001", "operation": "word.replace", "args": map[string]any{"search": "immutable", "replacement": "changed", "expectedText": "immutable"}}},
	} {
		infoJSON(t, h, aliceToken, "POST", "/api/office/sessions/"+v.ID+"/"+test.action, test.body, 403)
	}
	for _, resource := range []string{"export", "batch-freeze", "reconcile"} {
		if result := officeTestBridge(t, o, d, "POST", resource, map[string]any{}, ""); result.Code != 403 {
			t.Fatal("writer bridge admitted", resource, result.Code)
		}
	}
	if result := officeTestBridge(t, o, d, "POST", "state", map[string]any{"instanceId": instance, "dirty": true}, ""); result.Code != 403 {
		t.Fatal("viewer accepted dirty event", result.Body.String())
	}
	if _, err := o.save(context.Background(), d, "viewer_save_001"); err == nil {
		t.Fatal("internal save admitted")
	}
	callbackURL := fmt.Sprintf("/office/callbacks/%s/%s", v.ID, d.fileToken)
	infoJSON(t, h, aliceToken, "POST", callbackURL, map[string]any{}, 403)
	infoJSON(t, h, bobToken, "GET", "/api/office/sessions/"+v.ID+"/status", nil, 404)
	infoJSON(t, h, bobToken, "POST", target, request, 404)
	var repeat informationViewTestReply
	json.Unmarshal(infoJSON(t, h, aliceToken, "POST", target, request, 200), &repeat)
	if repeat.Session.ID != v.ID {
		t.Fatal("lost config retry created duplicate")
	}
	infoJSON(t, h, aliceToken, "GET", "/api/information/office-views", nil, 200)
	infoJSON(t, h, aliceToken, "POST", "/api/office/sessions/"+v.ID+"/close", nil, 200)
	infoJSON(t, h, aliceToken, "POST", target, request, 410)
	var listed struct {
		Active int `json:"active"`
	}
	json.Unmarshal(infoJSON(t, h, aliceToken, "GET", "/api/information/office-views", nil, 200), &listed)
	if listed.Active != 0 {
		t.Fatal("cancelled viewer leaked")
	}
	afterFiles, _ := filepath.Glob(filepath.Join(o.batchDir, "editors", "*"))
	if len(afterFiles) != len(beforeFiles) {
		t.Fatal("viewer persisted writer journal")
	}
	got := infoJSON(t, h, aliceToken, "GET", "/api/information/entries/"+id+"/content?version=1", nil, 200)
	if !bytes.Equal(got, original) {
		t.Fatal("immutable library asset changed")
	}
	if now := d.view(); now.Dirty || now.Revision != 0 || now.DirtyEpoch != 0 || now.PersistedEpoch != 0 {
		t.Fatal("viewer modified canonical state", now)
	}
}

func TestInformationOfficeViewConcurrentIdempotenceScopeLimitsAndExpiry(t *testing.T) {
	h, o, id, _ := informationViewFixture(t)
	target := "/api/information/entries/" + id + "/office-view"
	request := `{"version":1,"instanceId":"viewer_instance_race","operationId":"viewer_operation_race"}`
	var wg sync.WaitGroup
	ids := make(chan string, 6)
	failures := make(chan string, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := h.doAs(aliceToken, "POST", target, request)
			defer r.Body.Close()
			b, _ := io.ReadAll(r.Body)
			var reply informationViewTestReply
			if r.StatusCode != 200 || json.Unmarshal(b, &reply) != nil {
				failures <- fmt.Sprintf("%d %s", r.StatusCode, b)
				return
			}
			ids <- reply.Session.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(failures)
	for failure := range failures {
		t.Fatal(failure)
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("concurrent retry duplicated viewer")
		}
	}
	infoJSON(t, h, aliceToken, "POST", target, map[string]any{"version": 1, "instanceId": "viewer_instance_other", "operationId": "viewer_operation_race"}, 409)
	infoJSON(t, h, aliceToken, "POST", target, map[string]any{"version": 1, "instanceId": "viewer_instance_002", "operationId": "viewer_operation_002"}, 200)
	infoJSON(t, h, aliceToken, "POST", target, map[string]any{"version": 1, "instanceId": "viewer_instance_003", "operationId": "viewer_operation_003"}, 429)
	var strangers struct {
		Active int `json:"active"`
	}
	json.Unmarshal(infoJSON(t, h, bobToken, "GET", "/api/information/office-views", nil, 200), &strangers)
	if strangers.Active != 0 {
		t.Fatal("viewer enumeration leaked another owner")
	}
	o.mu.Lock()
	d := o.sessions[first]
	o.mu.Unlock()
	file := d.informationSnapshot.file
	d.mu.Lock()
	d.lastSeenAt = time.Now().Add(-informationOfficeViewIdle - time.Minute)
	d.viewCreatedAt = time.Now().Add(-informationOfficeViewMaximum - time.Minute)
	d.mu.Unlock()
	o.expireInformationView(d)
	if !d.view().Closed {
		t.Fatal("expired viewer remained alive")
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) || d.informationSnapshot != nil {
		t.Fatal("expired viewer retained a private snapshot", err)
	}
	infoJSON(t, h, aliceToken, "POST", target, json.RawMessage(request), 410)
}

func TestInformationOfficeViewRejectsForgedOwnerAndInvalidFormat(t *testing.T) {
	h, _, id, _ := informationViewFixture(t)
	target := "/api/information/entries/" + id + "/office-view"
	for _, body := range []string{`{"version":1,"instanceId":"viewer_instance_001","operationId":"viewer_operation_001","owner":"bob"}`, `{"version":0,"instanceId":"viewer_instance_001","operationId":"viewer_operation_001"}`, `{"version":1,"instanceId":"x","operationId":"viewer_operation_001"}`} {
		r := h.doAs(aliceToken, "POST", target, body)
		r.Body.Close()
		if r.StatusCode != 400 {
			t.Fatal("forged or unversioned viewer admitted", r.StatusCode)
		}
	}
	var invalid informationResult
	json.Unmarshal(infoUpload(t, h, aliceToken, "/api/information/files", "broken.xlsx", "bad archive", map[string]string{"revision": "1"}, 201), &invalid)
	infoJSON(t, h, aliceToken, "POST", "/api/information/entries/"+invalid.Entry.ID+"/office-view", map[string]any{"version": 1, "instanceId": "viewer_instance_001", "operationId": "viewer_operation_bad"}, 400)
	// Viewing never creates a conversation or workspace export.
	entries, _ := os.ReadDir(h.psrv.mgr.cfg.SessionDir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "information-view-") {
			t.Fatal("viewer became a conversation")
		}
	}
}

func TestInformationOfficeViewExcelUsesSameNativeSelectionBridgeWithoutWriterLease(t *testing.T) {
	h, o, _, _ := informationViewFixture(t)
	members := informationOfficeWorkbook(`<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>原始数值</t></is></c><c r="B1"><v>42</v></c></row></sheetData></worksheet>`)
	members["[Content_Types].xml"] = `<Types/>`
	original := informationOfficeZIP(t, members)
	var uploaded informationResult
	json.Unmarshal(infoUpload(t, h, aliceToken, "/api/information/files", "library.xlsx", string(original), map[string]string{"revision": "1"}, 201), &uploaded)
	var opened informationViewTestReply
	json.Unmarshal(infoJSON(t, h, aliceToken, "POST", "/api/information/entries/"+uploaded.Entry.ID+"/office-view", map[string]any{"version": 1, "instanceId": "viewer_excel_instance", "operationId": "viewer_excel_operation"}, 200), &opened)
	o.mu.Lock()
	d := o.sessions[opened.Session.ID]
	o.mu.Unlock()
	if d.editor != "cell" || d.parent != nil || !d.readOnly {
		t.Fatal("Excel viewer acquired editable document")
	}
	caps := officeTestCapabilities("cell")
	caps["selectionRead"] = true
	if ready := officeTestBridge(t, o, d, "POST", "ready", map[string]any{"instanceId": "viewer_excel_instance", "capabilities": caps}, ""); ready.Code != 200 {
		t.Fatal(ready.Body.String())
	}
	v := d.view()
	args, _ := json.Marshal(map[string]any{"documentId": v.DocumentID, "instanceId": "viewer_excel_instance", "canvasVersion": v.CanvasVersion, "dirtyEpoch": v.DirtyEpoch})
	done := officeSelectionQueue(t, o, d, "viewer_excel_selection", args)
	officeTestDispatch(t, o, d, "viewer_excel_instance", "viewer_excel_selection")
	result := officeTestBridge(t, o, d, "POST", "results", map[string]any{"instanceId": "viewer_excel_instance", "operationId": "viewer_excel_selection", "ok": true, "result": map[string]any{"kind": "cell", "sheet": "明细", "range": "A1:B1", "text": "原始数值\t42", "values": [][]any{{"原始数值", 42}}}}, "")
	if result.Code != 200 || !bytes.Contains(<-done, []byte(`A1:B1`)) {
		t.Fatal("Excel selection bridge failed", result.Body.String())
	}
	if _, err := o.command(context.Background(), d, "viewer_excel_apply", "sheet.apply", json.RawMessage(`{}`)); err == nil {
		t.Fatal("Excel viewer accepted mutation")
	}
	if now := d.view(); now.Dirty || now.DirtyEpoch != 0 || now.CanvasVersion != officeHash(original) {
		t.Fatal("Excel selection changed immutable snapshot")
	}
	if err := o.closeDocument(d); err != nil {
		t.Fatal(err)
	}
}
