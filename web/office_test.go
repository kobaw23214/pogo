package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
)

// These tests verify host authorization and commit fences. Their fake engine
// does not establish live ONLYOFFICE editing or native history support.
func officeTestPackage(t *testing.T, text string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for name, body := range map[string]string{"[Content_Types].xml": "<Types/>", "word/document.xml": "<document>" + text + "</document>"} {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func officeTestHealthyEngine(t *testing.T) string {
	t.Helper()
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pogo-office/healthz" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, 200, map[string]any{"ready": true, "adapter": "pogo-office-history", "engineVersion": "9.4.0 (build:129)", "verifiedAssets": 9, "extensionSha256": strings.Repeat("a", 64)})
	}))
	t.Cleanup(engine.Close)
	return engine.URL
}

func officeTestService(t *testing.T) (*officeService, *officeDocumentSession, string) {
	t.Helper()
	cwd := officeCanonicalTestDir(t)
	file := filepath.Join(cwd, "sample.docx")
	if err := os.WriteFile(file, officeTestPackage(t, "original"), 0640); err != nil {
		t.Fatal(err)
	}
	service := &officeService{cfg: &officeConfig{browserURL: "http://office.invalid", serviceURL: officeTestHealthyEngine(t), browserOrigin: "http://office.invalid", serviceOrigin: "http://office.invalid", publicURL: "http://pogo.invalid", fetchURL: "http://pogo.invalid", secret: []byte(strings.Repeat("x", 64)), client: &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, commandTimeout: time.Hour, saveTimeout: time.Hour}, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}}
	service.batchDir = officeCanonicalTestDir(t) // Private persistent receipts/versions, outside the writable document workspace.
	document, err := service.open("conversation-1", cwd, nil, NewHub(), file, "sample.docx", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.close)
	return service, document, file
}

// macOS aliases /var to /private/var. These fixtures model the canonical host
// directories required by production's private-storage identity guard.
func officeCanonicalTestDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func officeTestCapabilities(editor string) map[string]any {
	return map[string]any{"editor": editor, "read": true, "wordReplace": editor == "word", "sheetApply": editor == "cell", "nativeUndo": true, "nativeRedo": true, "historyWithoutRead": true, "readPreservesHistory": true, "readPreservesSelection": true, "mutationPreconditionsPreserveHistory": true, "engineExtension": map[string]any{"id": "pogo-office-history", "version": 1, "required": true, "detected": true, "engineVersion": "9.4.0", "engineBuild": "129"}}
}

func officeTestBridge(t *testing.T, service *officeService, document *officeDocumentSession, method, resource string, body any, instance string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	path := "/office/bridge/" + document.id + "/" + document.bridgeToken + "/" + resource
	if instance != "" {
		path += "?instanceId=" + instance
	}
	origin := service.cfg.publicURL
	document.mu.Lock()
	if document.browserOrigin != "" {
		origin = document.browserOrigin
	}
	document.mu.Unlock()
	request := httptest.NewRequest(method, origin+path, reader)
	request.SetPathValue("oid", document.id)
	request.SetPathValue("cap", document.bridgeToken)
	request.SetPathValue("resource", resource)
	response := httptest.NewRecorder()
	(&Server{office: service}).handleOfficeBridge(response, request)
	return response
}

func officeTestReady(t *testing.T, service *officeService, document *officeDocumentSession) string {
	t.Helper()
	instance := officeNewID()
	response := officeTestBridge(t, service, document, "POST", "ready", map[string]any{"instanceId": instance, "capabilities": officeTestCapabilities(document.editor)}, "")
	if response.Code != 200 || !document.view().Ready {
		t.Fatal("reviewed test handshake was not accepted")
	}
	return instance
}

func officeTestQueued(t *testing.T, document *officeDocumentSession, id string) *officeOperation {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		document.mu.Lock()
		op := document.operations[id]
		document.mu.Unlock()
		if op != nil {
			return op
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("operation was not queued")
	return nil
}

func officeTestDispatch(t *testing.T, service *officeService, document *officeDocumentSession, instance, id string) {
	t.Helper()
	response := officeTestBridge(t, service, document, "GET", "next", nil, instance)
	var result struct {
		Command *struct {
			OperationID string `json:"operationId"`
		} `json:"command"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Command == nil || result.Command.OperationID != id {
		t.Fatal("one queued operation was not dispatched to its bound editor")
	}
}

func officeExpectError(t *testing.T, err error, code string) {
	t.Helper()
	var actual *officeError
	if !errors.As(err, &actual) || actual.code != code {
		t.Fatalf("expected error code %s", code)
	}
}

func TestOfficeConfigurationExplicitAndCredentialSafe(t *testing.T) {
	if value, err := parseOfficeConfig(nil); err != nil || value != nil {
		t.Fatal("nil Office config must remain disabled")
	}
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte(strings.Repeat("test-secret", 8)), 0600); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"https://user:password@office.example", "https://office.example/?token=bad", "https://office.example/#x", "file:///tmp/office", "http://office.example/arbitrary"} {
		_, err := parseOfficeConfig(&OfficeOptions{BrowserURL: address, PublicURL: "http://pogo.example", JWTSecretFile: secret})
		if err == nil {
			t.Fatal("unsafe or ambiguous Office base URL accepted")
		}
	}
	cfg, err := parseOfficeConfig(&OfficeOptions{BrowserURL: "https://office.example", PublicURL: "https://pogo.example", JWTSecretFile: secret})
	if err != nil || cfg.serviceURL != "https://office.example" || cfg.fetchURL != "https://pogo.example" {
		t.Fatal("explicit remote configuration did not resolve its approved defaults")
	}
}

func TestOfficeAgentLinkScopeAndRevealExcludeCapabilities(t *testing.T) {
	service, document, _ := officeTestService(t)
	link := service.link(document.conversationID, document.cwd, nil, document.hub)
	_, events, unsubscribe := document.hub.Subscribe(0)
	defer unsubscribe()
	raw, err := link.Execute(context.Background(), json.RawMessage(`{"action":"open","path":"sample.docx"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{document.bridgeToken, document.fileToken, "bridgeToken", "bridgeURL", `"config"`} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("agent open leaked an editor capability")
		}
	}
	select {
	case event := <-events:
		if event.Type != "office_open" || event.OfficeDocumentSessionID != document.id || event.OfficePath != "sample.docx" {
			t.Fatal("agent open did not publish its public reveal identity")
		}
	case <-time.After(time.Second):
		t.Fatal("missing Office reveal event")
	}
	request, _ := json.Marshal(map[string]any{"action": "command", "document_session_id": document.id, "operation_id": "read-other-001", "operation": "word.read", "args": map[string]any{}})
	for _, foreign := range []struct {
		id    string
		owner *identity.Scope
	}{{"conversation-2", nil}, {document.conversationID, func() *identity.Scope { scope := identity.LocalScope("other"); return &scope }()}} {
		_, err := service.link(foreign.id, document.cwd, foreign.owner, nil).Execute(context.Background(), request)
		officeExpectError(t, err, "DOCUMENT_NOT_FOUND")
	}
	_, err = link.Execute(context.Background(), json.RawMessage(`{"action":"open","path":"../outside.docx"}`))
	if err == nil {
		t.Fatal("agent escaped its conversation workspace")
	}
}

func TestOfficeOneEditorInstanceAndExtensionFailsClosed(t *testing.T) {
	service, document, _ := officeTestService(t)
	instance := officeTestReady(t, service, document)
	second := officeTestBridge(t, service, document, "POST", "ready", map[string]any{"instanceId": officeNewID(), "capabilities": officeTestCapabilities("word")}, "")
	if second.Code != 409 {
		t.Fatal("a second editor stole the active queue")
	}
	if response := officeTestBridge(t, service, document, "GET", "next", nil, "another-instance"); response.Code != 409 {
		t.Fatal("foreign executor polled a document queue")
	}
	response := httptest.NewRecorder()
	(&Server{office: service}).writeOfficeConfig(response, document, officeNewID(), [32]byte{})
	if response.Code != 409 {
		t.Fatal("config let a second canvas be created after executor binding")
	}
	unsupported := officeTestCapabilities("word")
	unsupported["readPreservesHistory"] = false
	result := officeTestBridge(t, service, document, "POST", "ready", map[string]any{"instanceId": instance, "capabilities": unsupported}, "")
	if result.Code != 200 || document.view().Ready || !document.view().InstanceBound || document.view().Error == "" {
		t.Fatal("plain editor incorrectly advertised safe agent operations")
	}
	_, err := service.command(context.Background(), document, "unsupported-001", "word.read", json.RawMessage("{}"))
	officeExpectError(t, err, "ENGINE_EXTENSION_REQUIRED")
	stale := officeTestCapabilities("word")
	delete(stale, "historyWithoutRead")
	result = officeTestBridge(t, service, document, "POST", "ready", map[string]any{"instanceId": instance, "capabilities": stale}, "")
	if result.Code != 200 || document.view().Ready {
		t.Fatal("cached adapter without history-without-read support advertised ready")
	}
	_, err = service.command(context.Background(), document, "unsupported-002", "history.undo", json.RawMessage("{}"))
	officeExpectError(t, err, "ENGINE_EXTENSION_REQUIRED")
}

func TestOfficeCommandDeduplicationAndPrivateResultFiltering(t *testing.T) {
	service, document, _ := officeTestService(t)
	instance := officeTestReady(t, service, document)
	reply := make(chan json.RawMessage, 2)
	failures := make(chan error, 2)
	run := func() {
		data, err := service.command(context.Background(), document, "read-live-001", "word.read", json.RawMessage("{}"))
		reply <- data
		failures <- err
	}
	go run()
	officeTestQueued(t, document, "read-live-001")
	go run()
	officeTestDispatch(t, service, document, instance, "read-live-001")
	result := officeTestBridge(t, service, document, "POST", "results", map[string]any{"instanceId": instance, "operationId": "read-live-001", "ok": true, "result": map[string]any{"kind": "word", "text": "unsaved human text", "config": map[string]any{"token": document.bridgeToken}, "bridgeToken": document.bridgeToken}}, "")
	if result.Code != 200 {
		t.Fatal("valid live receipt was rejected")
	}
	first, second := <-reply, <-reply
	if <-failures != nil || <-failures != nil || !bytes.Equal(first, second) || !bytes.Contains(first, []byte("unsaved human text")) || bytes.Contains(first, []byte(document.bridgeToken)) || bytes.Contains(first, []byte(`"config"`)) {
		t.Fatal("duplicate result or secret filtering contract failed")
	}
	cached, err := service.command(context.Background(), document, "read-live-001", "word.read", json.RawMessage("{}"))
	if err != nil || !bytes.Equal(first, cached) {
		t.Fatal("finished operation was replayed")
	}
	document.mu.Lock()
	count := len(document.queue)
	document.mu.Unlock()
	if count != 1 {
		t.Fatal("same operation dispatched twice")
	}
	_, err = service.command(context.Background(), document, "read-live-001", "capabilities", json.RawMessage("{}"))
	officeExpectError(t, err, "OPERATION_ID_CONFLICT")
}

func TestOfficeWordReplaceReadReferencePreservesExactTextAndClientIdentity(t *testing.T) {
	service, document, _ := officeTestService(t)
	instance := officeTestReady(t, service, document)
	type outcome struct {
		result json.RawMessage
		err    error
	}
	start := func(id, operation string, args json.RawMessage) <-chan outcome {
		reply := make(chan outcome, 1)
		go func() {
			result, err := service.command(context.Background(), document, id, operation, args)
			reply <- outcome{result, err}
		}()
		return reply
	}
	const readID = "read_reference_001"
	readReply := start(readID, "word.read", json.RawMessage("{}"))
	officeTestQueued(t, document, readID)
	officeTestDispatch(t, service, document, instance, readID)
	text := "target\n手动输入的内容必须保留。\n"
	response := officeTestBridge(t, service, document, "POST", "results", map[string]any{"instanceId": instance, "operationId": readID, "ok": true, "result": map[string]any{"kind": "word", "text": text}}, "")
	if read := <-readReply; response.Code != 200 || read.err != nil {
		t.Fatal("real completed Word read receipt was not retained")
	}
	document.mu.Lock()
	beforeRetained := document.retainedBytes
	document.mu.Unlock()
	args := json.RawMessage(`{"search":"target","replacement":"updated","expectedReadId":"read_reference_001","expectedCount":1}`)
	const replaceID = "replace_reference_001"
	first := start(replaceID, "word.replace", args)
	op := officeTestQueued(t, document, replaceID)
	second := start(replaceID, "word.replace", json.RawMessage(`{"expectedCount":1,"expectedReadId":"read_reference_001","replacement":"updated","search":"target"}`))
	document.mu.Lock()
	resolved, err := officeWordReplaceArguments(op.args)
	if err != nil || resolved.ExpectedText == nil || *resolved.ExpectedText != text || resolved.ExpectedReadID != nil || op.fingerprint != officeFingerprint("word.replace", args) || document.retainedBytes != beforeRetained+len(op.args) || document.reservedBytes != officeMaxJSON {
		document.mu.Unlock()
		t.Fatal("read reference lost exact text, client identity, or resolved-byte quota")
	}
	document.mu.Unlock()
	response = officeTestBridge(t, service, document, "GET", "next", nil, instance)
	var dispatch struct {
		Command struct {
			OperationID string                `json:"operationId"`
			Args        officeWordReplaceArgs `json:"args"`
		} `json:"command"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &dispatch) != nil || dispatch.Command.OperationID != replaceID || dispatch.Command.Args.ExpectedText == nil || *dispatch.Command.Args.ExpectedText != text || dispatch.Command.Args.ExpectedReadID != nil {
		t.Fatal("native editor did not receive the original exact-text guard")
	}
	response = officeTestBridge(t, service, document, "POST", "results", map[string]any{"instanceId": instance, "operationId": replaceID, "ok": true, "result": map[string]any{"kind": "word", "text": strings.Replace(text, "target", "updated", 1)}}, "")
	a, b := <-first, <-second
	if response.Code != 200 || a.err != nil || b.err != nil || !bytes.Equal(a.result, b.result) {
		t.Fatal("same client request was not deduplicated while in flight")
	}
	// Replaying a completed client request must not reinterpret its reference.
	// Change the retained fixture receipt to prove the cached branch runs first.
	document.mu.Lock()
	document.operations[readID].name = "history.undo"
	queueCount := len(document.queue)
	document.mu.Unlock()
	cached, err := service.command(context.Background(), document, replaceID, "word.replace", args)
	if err != nil || !bytes.Equal(cached, a.result) {
		t.Fatal("finished replay tried to resolve its old read receipt again")
	}
	fullTextArgs, _ := json.Marshal(map[string]any{"search": "target", "replacement": "updated", "expectedText": text, "expectedCount": 1})
	_, err = service.command(context.Background(), document, replaceID, "word.replace", fullTextArgs)
	officeExpectError(t, err, "OPERATION_ID_CONFLICT")
	_, err = service.command(context.Background(), document, replaceID, "word.replace", json.RawMessage(`{"search":"target","replacement":"updated","expectedReadId":"read_reference_002","expectedCount":1}`))
	officeExpectError(t, err, "OPERATION_ID_CONFLICT")
	document.mu.Lock()
	if len(document.queue) != queueCount {
		document.mu.Unlock()
		t.Fatal("replay or conflicting request created another mutation")
	}
	document.mu.Unlock()
	response = officeTestBridge(t, service, document, "GET", "next", nil, instance)
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"command":null`)) {
		t.Fatal("completed reference mutation dispatched a second time")
	}
}

func TestOfficeWordReadReferenceStillGuardsLaterHumanInput(t *testing.T) {
	service, document, _ := officeTestService(t)
	instance := officeTestReady(t, service, document)
	const readID = "stale_read_reference"
	readText := "target\n原来的手动内容。\n"
	readReply := make(chan json.RawMessage, 1)
	go func() {
		result, _ := service.command(context.Background(), document, readID, "word.read", json.RawMessage("{}"))
		readReply <- result
	}()
	officeTestQueued(t, document, readID)
	officeTestDispatch(t, service, document, instance, readID)
	response := officeTestBridge(t, service, document, "POST", "results", map[string]any{"instanceId": instance, "operationId": readID, "ok": true, "result": map[string]any{"kind": "word", "text": readText}}, "")
	if response.Code != 200 || !bytes.Contains(<-readReply, []byte(`"ok":true`)) {
		t.Fatal("Word read fixture failed")
	}
	// The fake engine's live canvas now contains another human edit. It must
	// receive the older read text as the precondition, not this newer content.
	currentText := readText + "读取后又输入的内容。\n"
	response = officeTestBridge(t, service, document, "POST", "state", map[string]any{"instanceId": instance, "dirty": true}, "")
	if response.Code != 200 {
		t.Fatal("later human edit was not recorded")
	}
	args := json.RawMessage(`{"search":"target","replacement":"updated","expectedReadId":"stale_read_reference","expectedCount":1}`)
	const replaceID = "stale_replace_reference"
	reply := make(chan json.RawMessage, 1)
	go func() {
		result, _ := service.command(context.Background(), document, replaceID, "word.replace", args)
		reply <- result
	}()
	op := officeTestQueued(t, document, replaceID)
	officeTestDispatch(t, service, document, instance, replaceID)
	document.mu.Lock()
	resolved, err := officeWordReplaceArguments(op.args)
	document.mu.Unlock()
	if err != nil || resolved.ExpectedText == nil || *resolved.ExpectedText != readText || *resolved.ExpectedText == currentText {
		t.Fatal("reference silently rebased onto a later human edit")
	}
	response = officeTestBridge(t, service, document, "POST", "results", map[string]any{"instanceId": instance, "operationId": replaceID, "ok": false, "error": map[string]any{"code": "PRECONDITION_FAILED", "message": "Current text differs from the read receipt."}}, "")
	result := <-reply
	view := document.view()
	if response.Code != 200 || !bytes.Contains(result, []byte("PRECONDITION_FAILED")) || view.Blocked || view.DirtyEpoch != 1 || !view.Dirty {
		t.Fatal("stale guard failure became a successful or extra mutation")
	}
	cached, err := service.command(context.Background(), document, replaceID, "word.replace", args)
	if err != nil || !bytes.Equal(cached, result) {
		t.Fatal("failed guarded mutation was retried rather than cached")
	}
}

func TestOfficeWordReadReferenceRejectsUntrustedOrIncompleteReceipts(t *testing.T) {
	const readID = "rejected_read_reference"
	validResult, _ := json.Marshal(map[string]any{"ok": true, "operationId": readID, "result": map[string]any{"kind": "word", "text": "target\n"}})
	for _, test := range []struct {
		name     string
		mutate   func(*officeOperation)
		missing  bool
		foreign  bool
		args     json.RawMessage
		wantCode string
	}{
		{name: "missing", missing: true},
		{name: "another-session", foreign: true},
		{name: "unfinished", mutate: func(op *officeOperation) { op.finished = false }},
		{name: "not-dispatched", mutate: func(op *officeOperation) { op.dispatched = false }},
		{name: "not-word-read", mutate: func(op *officeOperation) { op.name = "sheet.read" }},
		{name: "failed", mutate: func(op *officeOperation) {
			op.result = json.RawMessage(`{"ok":false,"operationId":"rejected_read_reference","error":{"code":"DOCUMENT_LIMIT"}}`)
		}},
		{name: "wrong-kind", mutate: func(op *officeOperation) {
			op.result = json.RawMessage(`{"ok":true,"operationId":"rejected_read_reference","result":{"kind":"cell","text":"target"}}`)
		}},
		{name: "missing-text", mutate: func(op *officeOperation) {
			op.result = json.RawMessage(`{"ok":true,"operationId":"rejected_read_reference","result":{"kind":"word"}}`)
		}},
		{name: "non-string-text", mutate: func(op *officeOperation) {
			op.result = json.RawMessage(`{"ok":true,"operationId":"rejected_read_reference","result":{"kind":"word","text":false}}`)
		}},
		{name: "mismatched-receipt-id", mutate: func(op *officeOperation) {
			op.result = json.RawMessage(`{"ok":true,"operationId":"other_read_reference","result":{"kind":"word","text":"target"}}`)
		}},
		{name: "malformed-receipt", mutate: func(op *officeOperation) { op.result = json.RawMessage(`{"ok":true`) }},
		{name: "missing-receipt-id", mutate: func(op *officeOperation) {
			op.result = json.RawMessage(`{"ok":true,"result":{"kind":"word","text":"target"}}`)
		}},
		{name: "both-conditions", args: json.RawMessage(`{"search":"target","replacement":"updated","expectedText":"target","expectedReadId":"rejected_read_reference","expectedCount":1}`), wantCode: "INVALID_ARGUMENT"},
		{name: "null-other-condition", args: json.RawMessage(`{"search":"target","replacement":"updated","expectedText":null,"expectedReadId":"rejected_read_reference","expectedCount":1}`), wantCode: "INVALID_ARGUMENT"},
		{name: "null-reference", args: json.RawMessage(`{"search":"target","replacement":"updated","expectedReadId":null,"expectedCount":1}`), wantCode: "INVALID_ARGUMENT"},
		{name: "invalid-id", args: json.RawMessage(`{"search":"target","replacement":"updated","expectedReadId":"bad","expectedCount":1}`), wantCode: "INVALID_ARGUMENT"},
		{name: "neither-condition", args: json.RawMessage(`{"search":"target","replacement":"updated","expectedCount":1}`), wantCode: "INVALID_ARGUMENT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, document, _ := officeTestService(t)
			officeTestReady(t, service, document)
			op := &officeOperation{id: readID, name: "word.read", dispatched: true, finished: true, result: append(json.RawMessage(nil), validResult...), done: make(chan struct{})}
			if test.mutate != nil {
				test.mutate(op)
			}
			if test.foreign {
				_, other, _ := officeTestService(t)
				other.mu.Lock()
				other.operations[readID] = op
				other.mu.Unlock()
			} else if !test.missing {
				document.mu.Lock()
				document.operations[readID] = op
				document.mu.Unlock()
			}
			args := test.args
			if args == nil {
				args = json.RawMessage(`{"search":"target","replacement":"updated","expectedReadId":"rejected_read_reference","expectedCount":1}`)
			}
			_, err := service.command(context.Background(), document, "rejected_replace_reference", "word.replace", args)
			code := test.wantCode
			if code == "" {
				code = "INVALID_READ_REFERENCE"
			}
			officeExpectError(t, err, code)
			document.mu.Lock()
			if len(document.queue) != 0 || document.operations["rejected_replace_reference"] != nil || document.reservedBytes != 0 {
				document.mu.Unlock()
				t.Fatal("invalid reference reserved or dispatched a mutation")
			}
			document.mu.Unlock()
		})
	}
}

func TestOfficeWordReadReferenceChargesResolvedArgumentsAndRejectsExpansion(t *testing.T) {
	for _, boundary := range []string{"resolved-body-limit", "resolved-retained-quota"} {
		t.Run(boundary, func(t *testing.T) {
			service, document, _ := officeTestService(t)
			officeTestReady(t, service, document)
			const readID = "expanded_read_reference"
			text := strings.Repeat("a", 8000) + "\n"
			search, replacement := "a", "b"
			wantCode := "OPERATION_LIMIT"
			if boundary == "resolved-body-limit" {
				text = strings.Repeat("<", 160000) + "\n"
				search, replacement = strings.Repeat("<", 16000), strings.Repeat(">", 16000)
				wantCode = "INVALID_ARGUMENT"
			}
			receipt, _ := json.Marshal(map[string]any{"ok": true, "operationId": readID, "result": map[string]any{"kind": "word", "text": text}})
			args, _ := json.Marshal(map[string]any{"search": search, "replacement": replacement, "expectedReadId": readID, "expectedCount": 1})
			if len(receipt) > officeMaxJSON || len(args) > officeMaxJSON {
				t.Fatal("fixture exceeded the existing read or client-body limit")
			}
			document.mu.Lock()
			document.operations[readID] = &officeOperation{id: readID, name: "word.read", dispatched: true, finished: true, result: receipt}
			if boundary == "resolved-retained-quota" {
				// The small client reference would fit; the expanded native
				// exact-text arguments must be charged before admission.
				document.retainedBytes = officeMaxRetainedBytes - officeMaxJSON - len(args) - 1
			} else {
				document.retainedBytes = len(receipt)
			}
			beforeRetained := document.retainedBytes
			document.mu.Unlock()
			_, err := service.command(context.Background(), document, "expanded_replace_reference", "word.replace", args)
			officeExpectError(t, err, wantCode)
			document.mu.Lock()
			if document.retainedBytes != beforeRetained || document.reservedBytes != 0 || len(document.queue) != 0 || document.operations["expanded_replace_reference"] != nil {
				document.mu.Unlock()
				t.Fatal("rejected expanded request consumed quota or queued a mutation")
			}
			document.mu.Unlock()
		})
	}
}

func TestOfficeLateReadReceiptKeepsFailureImmutableWithoutBlockingDocument(t *testing.T) {
	service, document, _ := officeTestService(t)
	instance := officeTestReady(t, service, document)
	reply := make(chan json.RawMessage, 1)
	go func() {
		data, _ := service.command(context.Background(), document, "late-read-001", "word.read", json.RawMessage("{}"))
		reply <- data
	}()
	op := officeTestQueued(t, document, "late-read-001")
	officeTestDispatch(t, service, document, instance, op.id)
	document.mu.Lock()
	op.deadline = time.Now().Add(-time.Second)
	document.mu.Unlock()
	result := officeTestBridge(t, service, document, "POST", "results", map[string]any{"instanceId": instance, "operationId": op.id, "ok": true, "result": map[string]any{"kind": "word", "text": "late"}}, "")
	if result.Code != 200 || document.view().Blocked || document.view().Dirty || !bytes.Contains(<-reply, []byte("READ_TIMEOUT")) {
		t.Fatal("late read receipt replaced the failed read or blocked editing")
	}
	if err := service.closeDocument(document); err != nil {
		t.Fatal("a timed-out read made a clean document impossible to close")
	}
}

func TestOfficeCancelledBeforeDispatchIsNotExecuted(t *testing.T) {
	service, document, _ := officeTestService(t)
	instance := officeTestReady(t, service, document)
	ctx, cancel := context.WithCancel(context.Background())
	reply := make(chan error, 1)
	go func() {
		_, err := service.command(ctx, document, "cancel-read-001", "word.read", json.RawMessage("{}"))
		reply <- err
	}()
	op := officeTestQueued(t, document, "cancel-read-001")
	cancel()
	if !errors.Is(<-reply, context.Canceled) {
		t.Fatal("cancelled caller did not return")
	}
	document.mu.Lock()
	finished, dispatched := op.finished, op.dispatched
	document.mu.Unlock()
	if !finished || dispatched || document.view().Blocked {
		t.Fatal("undispatched cancellation changed the editor")
	}
	result := officeTestBridge(t, service, document, "GET", "next", nil, instance)
	if result.Code != 200 || !bytes.Contains(result.Body.Bytes(), []byte(`"command":null`)) {
		t.Fatal("cancelled action reached the editor")
	}
}

func officeTestStartSave(t *testing.T, service *officeService, document *officeDocumentSession, instance, id string) <-chan error {
	t.Helper()
	reply := make(chan error, 1)
	go func() { _, err := service.save(context.Background(), document, id); reply <- err }()
	officeTestQueued(t, document, id)
	officeTestDispatch(t, service, document, instance, id)
	return reply
}

func TestOfficeSaveWritesOneAtomicExportAndPreservesLaterDirty(t *testing.T) {
	service, document, file := officeTestService(t)
	instance := officeTestReady(t, service, document)
	updated := officeTestPackage(t, "agent and human")
	started, release := make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		close(started)
		<-release
		w.Write(updated)
	}))
	defer engine.Close()
	service.cfg.browserOrigin, service.cfg.serviceOrigin, service.cfg.serviceURL = engine.URL, engine.URL, engine.URL
	document.mu.Lock()
	document.dirtyEpoch = 1
	document.mu.Unlock()
	first := officeTestStartSave(t, service, document, instance, "save-once-001")
	second := make(chan error, 1)
	go func() { _, err := service.save(context.Background(), document, "save-once-001"); second <- err }()
	exported := make(chan error, 1)
	go func() {
		exported <- service.exportCurrent(context.Background(), document, instance, "save-once-001", "docx", engine.URL+"/cache/files/data/"+document.key+"_123/output.docx")
	}()
	<-started
	err := service.exportCurrent(context.Background(), document, instance, "save-once-001", "docx", engine.URL+"/cache/files/data/"+document.key+"_123/output.docx")
	officeExpectError(t, err, "STALE_EXPORT")
	state := officeTestBridge(t, service, document, "POST", "state", map[string]any{"instanceId": instance, "dirty": true}, "")
	if state.Code != 200 {
		t.Fatal("later human dirty notification rejected")
	}
	close(release)
	if <-exported != nil || <-first != nil || <-second != nil {
		t.Fatal("canonical save failed")
	}
	current, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(current, updated) || requests.Load() != 1 {
		t.Fatal("save did not persist exactly one export")
	}
	view := document.view()
	if view.Revision != 1 || view.PersistedEpoch != 1 || view.DirtyEpoch != 2 || !view.Dirty || view.SaveState != "dirty" {
		t.Fatal("save incorrectly cleared a later human edit")
	}
	if err := service.closeDocument(document); err == nil {
		t.Fatal("dirty editor was closed")
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0640 {
		t.Fatal("save changed document permissions")
	}
}

func TestOfficeExportRejectsExternalOriginAndRedirect(t *testing.T) {
	service, document, file := officeTestService(t)
	instance := officeTestReady(t, service, document)
	original, _ := os.ReadFile(file)
	reply := officeTestStartSave(t, service, document, instance, "save-origin-001")
	if err := service.exportCurrent(context.Background(), document, instance, "save-origin-001", "docx", "http://unapproved.invalid/file"); err == nil {
		t.Fatal("external download origin accepted")
	}
	if <-reply == nil || document.view().SaveState != "error" {
		t.Fatal("rejected origin left the canonical save pending")
	}
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://unapproved.invalid/file", 302)
	}))
	defer engine.Close()
	service.cfg.browserOrigin, service.cfg.serviceOrigin, service.cfg.serviceURL = engine.URL, engine.URL, engine.URL
	reply = officeTestStartSave(t, service, document, instance, "save-redirect-001")
	if err := service.exportCurrent(context.Background(), document, instance, "save-redirect-001", "docx", engine.URL+"/cache/files/data/"+document.key+"_123/output.docx"); err == nil {
		t.Fatal("export redirect was followed")
	}
	if <-reply == nil {
		t.Fatal("failed export was reported saved")
	}
	current, _ := os.ReadFile(file)
	if !bytes.Equal(current, original) {
		t.Fatal("rejected export changed the file")
	}
}

func TestOfficeSaveRejectsFileReplacementSymlinkAndMovedDirectory(t *testing.T) {
	for _, mutation := range []string{"in-place-content", "replace-inode", "symlink", "move-parent"} {
		t.Run(mutation, func(t *testing.T) {
			service, document, file := officeTestService(t)
			instance := officeTestReady(t, service, document)
			original, _ := os.ReadFile(file)
			newFile := officeTestPackage(t, "other actor")
			updated := officeTestPackage(t, "agent export")
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(updated) }))
			defer engine.Close()
			service.cfg.browserOrigin, service.cfg.serviceOrigin, service.cfg.serviceURL = engine.URL, engine.URL, engine.URL
			reply := officeTestStartSave(t, service, document, instance, "stale-save-001")
			preservedFile := file
			expected := newFile
			switch mutation {
			case "in-place-content":
				if err := os.WriteFile(file, newFile, 0640); err != nil {
					t.Fatal(err)
				}
			case "replace-inode":
				expected = original
				temporary := file + ".other"
				if err := os.WriteFile(temporary, original, 0640); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(temporary, file); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				outside := filepath.Join(t.TempDir(), "outside.docx")
				if err := os.WriteFile(outside, newFile, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, file); err != nil {
					t.Fatal(err)
				}
				preservedFile = outside
			case "move-parent":
				parent := filepath.Dir(file)
				if err := os.Rename(parent, parent+"-moved"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.RemoveAll(parent + "-moved") })
				if err := os.Mkdir(parent, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, newFile, 0640); err != nil {
					t.Fatal(err)
				}
			}
			if err := service.exportCurrent(context.Background(), document, instance, "stale-save-001", "docx", engine.URL+"/cache/files/data/"+document.key+"_123/output.docx"); err == nil {
				t.Fatal("stale file was overwritten")
			}
			if <-reply == nil {
				t.Fatal("stale save reported success")
			}
			current, _ := os.ReadFile(preservedFile)
			if !bytes.Equal(current, expected) {
				t.Fatal("conflict save changed another actor's file")
			}
		})
	}
}

func TestOfficeSaveFinalDeadlineFenceAndClosedCapTombstones(t *testing.T) {
	service, document, file := officeTestService(t)
	instance := officeTestReady(t, service, document)
	original, _ := os.ReadFile(file)
	reply := officeTestStartSave(t, service, document, instance, "deadline-save-001")
	document.mu.Lock()
	op := document.exportIntent
	op.deadline = time.Now().Add(-time.Second)
	document.mu.Unlock()
	err := service.commitExport(document, op, instance, officeTestPackage(t, "late export"))
	officeExpectError(t, err, "STALE_EXPORT")
	service.expire(document, op)
	if <-reply == nil {
		t.Fatal("expired save reported success")
	}
	current, _ := os.ReadFile(file)
	if !bytes.Equal(current, original) {
		t.Fatal("late export landed after its deadline")
	}
	// Use clean new sessions to verify retirement remains bounded.
	otherService, first, otherFile := officeTestService(t)
	if err := otherService.closeDocument(first); err != nil {
		t.Fatal(err)
	}
	oldID := first.id
	for i := 0; i < 70; i++ {
		document, err := otherService.open("conversation-1", first.cwd, nil, nil, otherFile, "sample.docx", nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := otherService.closeDocument(document); err != nil {
			t.Fatal(err)
		}
	}
	otherService.mu.Lock()
	retained, old := len(otherService.sessions), otherService.sessions[oldID]
	otherService.mu.Unlock()
	if retained > 64 || old != nil {
		t.Fatal("closed capability tombstones were not bounded")
	}
}

func TestOfficeAutomaticCallbacksNeverWriteAndRequireMatchingJWT(t *testing.T) {
	service, document, file := officeTestService(t)
	original, _ := os.ReadFile(file)
	callback := func(payload, body map[string]any) *httptest.ResponseRecorder {
		token, err := service.signJWT(payload)
		if err != nil {
			t.Fatal(err)
		}
		body["token"] = token
		data, _ := json.Marshal(body)
		request := httptest.NewRequest("POST", "/office/callbacks/"+document.id+"/"+document.fileToken, bytes.NewReader(data))
		request.SetPathValue("oid", document.id)
		request.SetPathValue("cap", document.fileToken)
		response := httptest.NewRecorder()
		(&Server{office: service}).handleOfficeCallback(response, request)
		return response
	}
	body := map[string]any{"key": document.key, "status": 2, "url": "http://unapproved.invalid/export"}
	if response := callback(body, map[string]any{"key": document.key, "status": 2, "url": "http://unapproved.invalid/export"}); response.Code != 200 {
		t.Fatal("signed acknowledgement rejected")
	}
	if response := callback(body, map[string]any{"key": document.key, "status": 6, "url": "http://unapproved.invalid/export"}); response.Code != 403 {
		t.Fatal("unsigned callback status change accepted")
	}
	current, _ := os.ReadFile(file)
	if !bytes.Equal(current, original) || document.view().Revision != 0 {
		t.Fatal("automatic callback overwrote canonical storage")
	}
}

func TestOfficeHTTPAccountAndConversationAuthorization(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) {
		return llm.Response{}, errors.New("Office host API must not use a model")
	})
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte(strings.Repeat("s", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	h.psrv.opts.Office = &OfficeOptions{BrowserURL: officeTestHealthyEngine(t), PublicURL: h.srv.URL, JWTSecretFile: secret}
	if err := h.psrv.initOffice(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.psrv.closeOffice)
	alice := h.createAs(aliceToken)
	bob := h.createAs(bobToken)
	session, err := h.mgr.Get(alice)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(session.cwd, "account.docx")
	if err := os.WriteFile(file, officeTestPackage(t, "account"), 0600); err != nil {
		t.Fatal(err)
	}
	request := func(token, method, path string, body any) (int, []byte) {
		var reader io.Reader
		if body != nil {
			data, _ := json.Marshal(body)
			reader = bytes.NewReader(data)
		}
		req, _ := http.NewRequest(method, h.srv.URL+path, reader)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := h.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, data
	}
	instance := officeNewID()
	code, body := request(aliceToken, "POST", "/api/office/sessions", map[string]string{"path": "account.docx", "conversation_id": alice, "instanceId": instance})
	var result struct {
		Session officeSessionView `json:"session"`
	}
	if code != 200 || json.Unmarshal(body, &result) != nil || result.Session.ID == "" {
		t.Fatal("account could not open its own document")
	}
	for _, target := range []struct {
		token, path string
		status      int
	}{{bobToken, "/api/office/sessions/" + result.Session.ID + "/status", 404}, {aliceToken, "/api/office/sessions/" + result.Session.ID + "/status?conversation_id=" + bob, 404}, {"", "/api/office/sessions/" + result.Session.ID + "/status", 401}} {
		if got, _ := request(target.token, "GET", target.path, nil); got != target.status {
			t.Fatal("Office public API exposed another account or conversation")
		}
	}
	ready := officeTestBridge(t, h.psrv.office, h.psrv.office.sessions[result.Session.ID], "POST", "ready", map[string]any{"instanceId": instance, "capabilities": officeTestCapabilities("word")}, "")
	if ready.Code != 200 {
		t.Fatal("claimed viewer could not complete its real-shaped capabilities handshake")
	}
	if got, _ := request(aliceToken, "POST", "/api/office/sessions", map[string]string{"path": "account.docx", "conversation_id": bob, "instanceId": officeNewID()}); got != 404 {
		t.Fatal("another account conversation authorized a file")
	}
	if got, _ := request(aliceToken, "POST", "/api/office/sessions/"+result.Session.ID+"/close", map[string]any{}); got != 200 {
		t.Fatal("clean session close failed")
	}
	var complete map[string]any
	_ = json.Unmarshal(body, &complete)
	bridge := complete["bridgeURL"].(string)
	bridge = strings.TrimPrefix(bridge, h.srv.URL)
	if got, _ := request("", "GET", bridge+"scope.js", nil); got != 409 {
		t.Fatal("closed bridge capability stayed usable")
	}
	// Reopen, then prove Parent Manager.Delete enforces the same document gate.
	instance = officeNewID()
	code, body = request(aliceToken, "POST", "/api/office/sessions", map[string]string{"path": "account.docx", "conversation_id": alice, "instanceId": instance})
	if code != 200 || json.Unmarshal(body, &result) != nil {
		t.Fatal("account could not reopen a saved document")
	}
	h.psrv.office.mu.Lock()
	active := h.psrv.office.sessions[result.Session.ID]
	h.psrv.office.mu.Unlock()
	if response := officeTestBridge(t, h.psrv.office, active, "POST", "ready", map[string]any{"instanceId": instance, "capabilities": officeTestCapabilities("word")}, ""); response.Code != 200 {
		t.Fatal("viewer could not finish handshake")
	}
	if response := officeTestBridge(t, h.psrv.office, active, "POST", "state", map[string]any{"instanceId": instance, "dirty": true}, ""); response.Code != 200 {
		t.Fatal("human dirty change did not reach host")
	}
	if got, _ := request(aliceToken, "DELETE", "/api/sessions/"+alice, nil); got != 409 {
		t.Fatal("conversation delete discarded an unsaved Office document")
	}
	session.mu.Lock()
	closing := session.closing
	session.mu.Unlock()
	if closing || active.view().Closed || !h.psrv.office.conversationActive(alice) {
		t.Fatal("rejected delete changed the live conversation or editor")
	}
	active.mu.Lock()
	active.persistedEpoch = active.dirtyEpoch
	active.mu.Unlock()
	if got, _ := request(aliceToken, "DELETE", "/api/sessions/"+alice, nil); got != 204 {
		t.Fatal("clean conversation delete failed")
	}
	if !active.view().Closed || h.psrv.office.conversationActive(alice) {
		t.Fatal("deleted conversation retained an Office capability")
	}
}

func TestOfficeEngineMayReadOnlyItsScopedBridgeAssets(t *testing.T) {
	service, document, _ := officeTestService(t)
	document.browserOrigin = "http://pogo.invalid"
	s := &Server{office: service, opts: ServerOptions{Token: "test-token"}, mux: http.NewServeMux()}
	s.officeRoutes()
	handler := s.withOrigin(s.withToken(s.mux))
	path := "/office/bridge/" + document.id + "/" + document.bridgeToken + "/"
	for _, test := range []struct {
		method, path, origin, site string
		want                       int
		cors                       bool
	}{
		{"GET", path + "config.json", document.browserOrigin, "same-origin", 200, false},
		{"OPTIONS", path + "config.json", document.browserOrigin, "same-origin", 405, false},
		{"GET", path + "scope.js", "http://unapproved.invalid", "cross-site", 403, false},
		{"GET", "/api/office/config", service.cfg.browserOrigin, "cross-site", 403, false},
		{"POST", path + "ready", service.cfg.browserOrigin, "cross-site", 403, false},
		{"GET", path + "next", service.cfg.browserOrigin, "cross-site", 403, false},
	} {
		request := httptest.NewRequest(test.method, "http://pogo.invalid"+test.path, nil)
		request.Header.Set("Origin", test.origin)
		request.Header.Set("Sec-Fetch-Site", test.site)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("bridge origin boundary %s expected %d, got %d", test.method+" "+strings.TrimPrefix(test.path, path), test.want, response.Code)
		}
		if response.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("same-origin editor unexpectedly exposed cross-origin bridge assets")
		}
	}
}

type officeBlockingJournal struct{ entered, release chan struct{} }

func (j *officeBlockingJournal) BeforeChange(string, []byte, bool) { close(j.entered); <-j.release }

func TestOfficeJournalDelayCannotBypassDeadlineContextOrFileConflict(t *testing.T) {
	for _, condition := range []string{"deadline", "cancelled-context", "external-write", "directory-renamed"} {
		t.Run(condition, func(t *testing.T) {
			service, document, file := officeTestService(t)
			instance := officeTestReady(t, service, document)
			original, _ := os.ReadFile(file)
			expected := original
			movedDirectory := ""
			replacement := officeTestPackage(t, "external writer")
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(officeTestPackage(t, "late agent export")) }))
			defer engine.Close()
			service.cfg.browserOrigin, service.cfg.serviceOrigin, service.cfg.serviceURL = engine.URL, engine.URL, engine.URL
			journal := &officeBlockingJournal{entered: make(chan struct{}), release: make(chan struct{})}
			document.journal = journal
			if condition == "deadline" {
				service.cfg.saveTimeout = 500 * time.Millisecond
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			saveResult := make(chan error, 1)
			go func() { _, err := service.save(ctx, document, "journal-save-001"); saveResult <- err }()
			op := officeTestQueued(t, document, "journal-save-001")
			officeTestDispatch(t, service, document, instance, op.id)
			exportResult := make(chan error, 1)
			go func() {
				exportResult <- service.exportCurrent(context.Background(), document, instance, op.id, "docx", engine.URL+"/cache/files/data/"+document.key+"_123/output.docx")
			}()
			released := false
			release := func() {
				if !released {
					close(journal.release)
					released = true
				}
			}
			defer release()
			select {
			case <-journal.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("save did not reach blocking journal")
			}
			switch condition {
			case "deadline":
				time.Sleep(time.Until(op.deadline) + 20*time.Millisecond)
			case "cancelled-context":
				cancel()
			case "external-write":
				if err := os.WriteFile(file, replacement, 0640); err != nil {
					t.Fatal(err)
				}
				expected = replacement
			case "directory-renamed":
				movedDirectory = filepath.Dir(file) + "-moved"
				if err := os.Rename(filepath.Dir(file), movedDirectory); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(movedDirectory) })
				if err := os.Mkdir(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, replacement, 0640); err != nil {
					t.Fatal(err)
				}
				expected = replacement
			}
			release()
			if <-exportResult == nil || <-saveResult == nil {
				t.Fatal("journal delay bypassed a final commit fence")
			}
			current, _ := os.ReadFile(file)
			if !bytes.Equal(current, expected) {
				t.Fatal("journal-delayed save changed a protected file")
			}
			if movedDirectory != "" {
				movedFile, err := os.ReadFile(filepath.Join(movedDirectory, filepath.Base(file)))
				if err != nil || !bytes.Equal(movedFile, original) || document.view().Revision != 0 {
					t.Fatal("journal-delayed save committed to the old moved directory")
				}
			}
		})
	}
}

func TestOfficeUndispatchedExportExpiryReleasesSaveState(t *testing.T) {
	service, document, _ := officeTestService(t)
	instance := officeTestReady(t, service, document)
	reply := make(chan error, 1)
	go func() { _, err := service.save(context.Background(), document, "expired-export-001"); reply <- err }()
	op := officeTestQueued(t, document, "expired-export-001")
	document.mu.Lock()
	op.deadline = time.Now().Add(-time.Second)
	document.mu.Unlock()
	result := officeTestBridge(t, service, document, "GET", "next", nil, instance)
	if result.Code != 200 || <-reply == nil {
		t.Fatal("undispatched expired save did not finish")
	}
	document.mu.Lock()
	saving := document.saving
	document.mu.Unlock()
	if saving || document.view().Blocked {
		t.Fatal("undispatched save expiry permanently locked the editor")
	}
	if err := service.closeDocument(document); err != nil {
		t.Fatal("clean editor could not close after undispatched export expiry")
	}
}

func TestOfficeConversationDeleteChecksAllDocumentsBeforeRetiring(t *testing.T) {
	service, first, file := officeTestService(t)
	link := service.link(first.conversationID, first.cwd, nil, nil)
	other := filepath.Join(filepath.Dir(file), "other.docx")
	if err := os.WriteFile(other, officeTestPackage(t, "other"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := service.open(first.conversationID, first.cwd, nil, nil, other, "other.docx", nil)
	if err != nil {
		t.Fatal(err)
	}
	second.mu.Lock()
	second.dirtyEpoch = 1
	second.mu.Unlock()
	officeExpectError(t, service.beforeDeleteConversation(first.conversationID), "UNSAVED_DOCUMENT")
	if first.view().Closed || second.view().Closed || !service.conversationActive(first.conversationID) {
		t.Fatal("failed delete partially retired a clean sibling document")
	}
	if _, err := link.Execute(context.Background(), json.RawMessage(`{"action":"list"}`)); err != nil {
		t.Fatal("rejected delete revoked the original agent role")
	}
	second.mu.Lock()
	second.dirtyEpoch = 0
	second.mu.Unlock()
	if err := service.beforeDeleteConversation(first.conversationID); err != nil {
		t.Fatal(err)
	}
	if !first.view().Closed || !second.view().Closed || service.conversationActive(first.conversationID) {
		t.Fatal("successful delete left a document active")
	}
	_, err = link.Execute(context.Background(), json.RawMessage(`{"action":"open","path":"sample.docx"}`))
	officeExpectError(t, err, "CONVERSATION_CLOSED")
}

func TestOfficeSessionRevocationFinishesPendingAndBlocksOldRole(t *testing.T) {
	service, document, _ := officeTestService(t)
	instance := officeTestReady(t, service, document)
	link := service.link(document.conversationID, document.cwd, nil, nil)
	done := make(chan json.RawMessage, 1)
	go func() {
		reply, _ := service.command(context.Background(), document, "revoked-read-001", "word.read", json.RawMessage("{}"))
		done <- reply
	}()
	officeTestQueued(t, document, "revoked-read-001")
	officeTestDispatch(t, service, document, instance, "revoked-read-001")
	service.revokeConversation(document.conversationID)
	select {
	case result := <-done:
		if !bytes.Contains(result, []byte("CONVERSATION_CLOSED")) {
			t.Fatal("pending operation did not receive explicit revocation")
		}
	case <-time.After(time.Second):
		t.Fatal("revoked operation remained pending")
	}
	if service.conversationActive(document.conversationID) || !document.view().Closed {
		t.Fatal("revoked document retained an active file lease")
	}
	_, err := link.Execute(context.Background(), json.RawMessage(`{"action":"open","path":"sample.docx"}`))
	officeExpectError(t, err, "CONVERSATION_CLOSED")
	if response := officeTestBridge(t, service, document, "POST", "state", map[string]any{"instanceId": instance, "dirty": true}, ""); response.Code != 409 {
		t.Fatal("revoked iframe changed closed document state")
	}
}

func TestOfficeScopedDetachIsInstanceBoundAndNeverDiscardsDirty(t *testing.T) {
	service, document, file := officeTestService(t)
	instance := officeTestReady(t, service, document)
	detach := func(id string) *httptest.ResponseRecorder {
		return officeTestBridge(t, service, document, "POST", "detach", map[string]string{"instanceId": id}, "")
	}
	if response := detach(officeNewID()); response.Code != 409 {
		t.Fatal("foreign page detached the active editor")
	}
	officeTestBridge(t, service, document, "POST", "state", map[string]any{"instanceId": instance, "dirty": true}, "")
	if response := detach(instance); response.Code != 409 || document.view().Closed {
		t.Fatal("unload detached an unsaved document")
	}
	document.mu.Lock()
	document.persistedEpoch = document.dirtyEpoch
	document.mu.Unlock()
	if response := detach(instance); response.Code != 200 || !document.view().Closed {
		t.Fatal("clean page could not detach before refresh")
	}
	reopened, err := service.open(document.conversationID, document.cwd, nil, nil, file, "sample.docx", nil)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.id == document.id || reopened.bridgeToken == document.bridgeToken {
		t.Fatal("reopen reused a stale editor capability")
	}
	if response := detach(instance); response.Code != 409 {
		t.Fatal("late unload retained access to a retired session")
	}
	if reopened.view().Closed {
		t.Fatal("old unload closed the replacement editor")
	}
}

func TestOfficeBootstrapClaimsViewerBeforeEditorCanBecomeDirty(t *testing.T) {
	service, document, _ := officeTestService(t)
	server := &Server{office: service}
	instance := officeNewID()
	response := httptest.NewRecorder()
	server.writeOfficeConfig(response, document, instance, [32]byte{})
	var packageInfo struct {
		InstanceOwned bool              `json:"instanceOwned"`
		Session       officeSessionView `json:"session"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &packageInfo) != nil || !packageInfo.InstanceOwned || !packageInfo.Session.InstanceBound || packageInfo.Session.Ready {
		t.Fatal("config did not claim the viewer before editor initialization")
	}
	if response := officeTestBridge(t, service, document, "POST", "state", map[string]any{"instanceId": instance, "dirty": true}, ""); response.Code != 200 || !document.view().Dirty {
		t.Fatal("early human edit before plugin ready was dropped")
	}
	if err := service.closeDocument(document); err == nil {
		t.Fatal("early dirty editor could be closed")
	}
	second := httptest.NewRecorder()
	server.writeOfficeConfig(second, document, officeNewID(), [32]byte{})
	if second.Code != 409 {
		t.Fatal("second canvas obtained config during initial handshake")
	}
	same := httptest.NewRecorder()
	server.writeOfficeConfig(same, document, instance, [32]byte{})
	if same.Code != 200 {
		t.Fatal("same viewer could not repeat an incomplete bootstrap")
	}
}

func TestOfficeSaveSharesLedgerLimitsAndKeepsOneFinalExport(t *testing.T) {
	for _, quota := range []string{"record-count", "retained-bytes"} {
		t.Run(quota, func(t *testing.T) {
			service, document, file := officeTestService(t)
			instance := officeTestReady(t, service, document)
			var exports atomic.Int32
			updated := officeTestPackage(t, "bounded final export")
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				exports.Add(1)
				w.Write(updated)
			}))
			defer engine.Close()
			service.cfg.browserOrigin, service.cfg.serviceOrigin, service.cfg.serviceURL = engine.URL, engine.URL, engine.URL

			document.mu.Lock()
			count := officeMaxOperations - 1
			result := json.RawMessage("{\"ok\":true}")
			if quota == "retained-bytes" {
				count = 15
				result = json.RawMessage("\"" + strings.Repeat("x", officeMaxJSON-4) + "\"")
			}
			for index := 0; index < count; index++ {
				id := "quota_seed_" + strconv.Itoa(index)
				done := make(chan struct{})
				close(done)
				op := &officeOperation{id: id, name: "word.read", args: json.RawMessage("{}"), result: result, done: done, finished: true}
				document.operations[id] = op
				document.queue = append(document.queue, op)
				document.retainedBytes += len(op.args) + len(op.result)
			}
			document.dirtyEpoch = 1
			document.mu.Unlock()

			save := func(id string) {
				reply := officeTestStartSave(t, service, document, instance, id)
				if err := service.exportCurrent(context.Background(), document, instance, id, "docx", engine.URL+"/cache/files/data/"+document.key+"_123/output.docx"); err != nil {
					t.Fatal(err)
				}
				if err := <-reply; err != nil {
					t.Fatal(err)
				}
			}
			expectedExports := int32(1)
			if quota == "record-count" {
				save("quota_normal_save")
				expectedExports++
			}
			_, err := service.command(context.Background(), document, "quota_rejected_read", "word.read", json.RawMessage("{}"))
			officeExpectError(t, err, "OPERATION_LIMIT")
			save("quota_final_save")
			// The same ID is still idempotent even after the reserved slot is
			// consumed; a different ID must not create another ledger entry.
			cached, err := service.save(context.Background(), document, "quota_final_save")
			if err != nil || cached.Dirty || exports.Load() != expectedExports {
				t.Fatal("final save retry was not a bounded cached result")
			}
			_, err = service.save(context.Background(), document, "quota_extra_save")
			officeExpectError(t, err, "OPERATION_LIMIT")
			document.mu.Lock()
			if !document.finalSaveUsed || len(document.operations) > officeMaxOperations+1 || document.reservedBytes != 0 || document.retainedBytes > officeMaxRetainedBytes+officeFinalSaveBytes {
				document.mu.Unlock()
				t.Fatal("saving bypassed the bounded shared ledger")
			}
			document.mu.Unlock()
			persisted, err := os.ReadFile(file)
			if err != nil || !bytes.Equal(persisted, updated) {
				t.Fatal("reserved final save did not persist the current document")
			}
			if err := service.closeDocument(document); err != nil {
				t.Fatal("quota-exhausted saved document could not close")
			}
			reopened, err := service.open(document.conversationID, document.cwd, nil, nil, file, document.panelPath, nil)
			if err != nil || reopened.id == document.id || reopened.retainedBytes != 0 || reopened.reservedBytes != 0 || reopened.finalSaveUsed || len(reopened.operations) != 0 {
				t.Fatal("reopening did not start a fresh bounded ledger")
			}
		})
	}
}

func TestOfficeResultReservationBoundsExpandedReceipts(t *testing.T) {
	service, document, _ := officeTestService(t)
	instance := officeTestReady(t, service, document)
	reply := make(chan json.RawMessage, 1)
	go func() {
		result, _ := service.command(context.Background(), document, "bounded_result_001", "history.undo", json.RawMessage("{}"))
		reply <- result
	}()
	op := officeTestQueued(t, document, "bounded_result_001")
	officeTestDispatch(t, service, document, instance, op.id)
	document.mu.Lock()
	if document.reservedBytes != officeMaxJSON {
		document.mu.Unlock()
		t.Fatal("pending result had no byte reservation")
	}
	service.finishLocked(document, op, map[string]any{"ok": true, "result": strings.Repeat("<", officeMaxJSON/3)})
	document.mu.Unlock()
	var outcome struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(<-reply, &outcome) != nil || outcome.OK || outcome.Error.Code != "OUTCOME_UNKNOWN" {
		t.Fatal("oversized escaped mutation result was reported as confirmed")
	}
	document.mu.Lock()
	defer document.mu.Unlock()
	if !document.blocked || document.reservedBytes != 0 || document.retainedBytes > officeMaxJSON {
		t.Fatal("oversized receipt bypassed its reservation or unknown-outcome fence")
	}
}
