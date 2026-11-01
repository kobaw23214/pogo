package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
)

// These requests traverse the full Server.Handler, including maintenance,
// ordinary account/Origin authorization, and the native Office routes.
func TestOfficeReleaseMaintenanceScopedCapabilities(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) {
		return llm.Response{}, errors.New("maintenance admission must not use a model")
	})
	secret := filepath.Join(t.TempDir(), "office-jwt")
	if err := os.WriteFile(secret, []byte(strings.Repeat("s", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	h.psrv.opts.Office = &OfficeOptions{BrowserURL: officeTestHealthyEngine(t), PublicURL: h.srv.URL, JWTSecretFile: secret}
	if err := h.psrv.initOffice(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.psrv.closeOffice)
	conversation := h.createAs(aliceToken)
	session := h.session(conversation)
	original := officeTestPackage(t, "manual content")
	for _, name := range []string{"account.docx", "second.docx"} {
		if err := os.WriteFile(filepath.Join(session.cwd, name), original, 0600); err != nil {
			t.Fatal(err)
		}
	}
	request := func(method, path, bearer, release string, body any, headers map[string]string) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = bytes.NewReader(data)
		}
		req, err := http.NewRequest(method, h.srv.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if release != "" {
			req.Header.Set("X-Pogo-Release", release)
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		res, err := h.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, data
	}
	open := func(path, instance, release string) *officeDocumentSession {
		t.Helper()
		code, data := request("POST", "/api/office/sessions", aliceToken, release, map[string]string{"path": path, "conversation_id": conversation, "instanceId": instance}, nil)
		var result struct {
			Session officeSessionView `json:"session"`
		}
		if code != 200 || json.Unmarshal(data, &result) != nil || result.Session.ID == "" {
			t.Fatalf("authorized open returned %d", code)
		}
		h.psrv.office.mu.Lock()
		d := h.psrv.office.sessions[result.Session.ID]
		h.psrv.office.mu.Unlock()
		return d
	}
	writeMarker := func(token string) {
		t.Helper()
		data, _ := json.Marshal(map[string]string{"token": token})
		if err := os.WriteFile(h.mgr.releaseMaintenancePath(), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	filePath := func(d *officeDocumentSession) string { return "/office/files/" + d.id + "/" + d.fileToken }
	callbackPath := func(d *officeDocumentSession) string { return "/office/callbacks/" + d.id + "/" + d.fileToken }
	configPath := func(d *officeDocumentSession, instance string) string {
		return "/api/office/sessions/" + d.id + "/config?instanceId=" + instance
	}
	grantOf := func(d *officeDocumentSession) [32]byte {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.releaseGrant
	}
	instance := officeNewID()
	document := open("account.docx", instance, "")
	secondInstance := officeNewID()
	second := open("second.docx", secondInstance, "")
	zero := [32]byte{}
	if grantOf(document) != zero || grantOf(second) != zero {
		t.Fatal("ordinary editor config attached a maintenance grant")
	}
	token := strings.Repeat("a", 64)
	writeMarker(token)
	for _, route := range []string{filePath(document), filePath(second)} {
		if code, _ := request("GET", route, "", "", nil, nil); code != 503 {
			t.Fatal("unbound pre-maintenance capability passed the gate")
		}
	}
	for _, tc := range []struct {
		bearer, path string
		want         int
	}{
		{"", configPath(document, instance), 401},
		{bobToken, configPath(document, instance), 404},
		{aliceToken, configPath(document, officeNewID()), 409},
	} {
		if code, _ := request("GET", tc.path, tc.bearer, token, nil, nil); code != tc.want {
			t.Fatalf("unauthorized config returned %d, want %d", code, tc.want)
		}
		if grantOf(document) != zero {
			t.Fatal("rejected config attached a maintenance grant")
		}
	}
	if code, _ := request("POST", "/api/office/sessions", bobToken, token, map[string]string{"path": "account.docx", "conversation_id": conversation, "instanceId": instance}, nil); code != 404 {
		t.Fatal("release header bypassed another account's conversation ownership")
	}
	code, config := request("GET", configPath(document, instance), aliceToken, token, nil, nil)
	if code != 200 || grantOf(document) != sha256.Sum256([]byte(token)) || bytes.Contains(config, []byte(token)) || bytes.Contains(config, []byte("releaseGrant")) {
		t.Fatal("authenticated config did not bind a private epoch grant")
	}
	if code, data := request("GET", filePath(document), "", "", nil, nil); code != 200 || !bytes.Equal(data, original) {
		t.Fatal("DocumentServer could not fetch its authorized document")
	}
	// An invalid release header is not another credential; a correctly scoped
	// file capability still uses only the epoch grant and normal file checks.
	if code, _ := request("GET", filePath(document), "", "wrong-header", nil, nil); code != 200 {
		t.Fatal("valid scoped download could not retry without an operator header")
	}
	payload := map[string]any{"key": document.key, "status": 2, "url": "http://unapproved.invalid/export"}
	signed, err := h.psrv.office.signJWT(payload)
	if err != nil {
		t.Fatal(err)
	}
	callback := map[string]any{"key": document.key, "status": 2, "url": "http://unapproved.invalid/export", "token": signed}
	for repeat := 0; repeat < 2; repeat++ {
		if code, data := request("POST", callbackPath(document), "", "", callback, nil); code != 200 || !bytes.Contains(data, []byte(`"error":1`)) {
			t.Fatal("signed callback did not reach its scoped handler or falsely acknowledged an unpreserved export")
		}
	}
	for _, body := range []map[string]any{
		{"key": document.key, "status": 2},
		{"key": document.key, "status": 6, "url": "http://unapproved.invalid/export", "token": signed},
	} {
		if code, _ := request("POST", callbackPath(document), "", "", body, nil); code != 403 {
			t.Fatal("maintenance exception bypassed callback signature validation")
		}
	}
	wrongKey := map[string]any{"key": second.key, "status": 2}
	wrongKey["token"], err = h.psrv.office.signJWT(wrongKey)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := request("POST", callbackPath(document), "", "", wrongKey, nil); code != 403 {
		t.Fatal("signed callback for another document passed identity validation")
	}
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/office/config"},
		{"GET", "/api/office/sessions/" + document.id + "/status"},
		{"POST", "/api/office/sessions/" + document.id + "/commands"},
		{"POST", "/api/office/sessions/" + document.id + "/save"},
		{"GET", "/office/bridge/" + document.id + "/" + document.bridgeToken + "/config.json"},
		{"GET", "/office/bridge/" + document.id + "/" + document.bridgeToken + "/next"},
		{"POST", "/office/bridge/" + document.id + "/" + document.bridgeToken + "/ready"},
		{"GET", filePath(second)},
		{"GET", "/office/files/" + second.id + "/" + document.fileToken},
		{"GET", "/office/files/" + document.id + "/" + second.fileToken},
		{"GET", "/office/files/" + document.id + "/" + document.bridgeToken},
		{"GET", filePath(document) + "x"},
		{"HEAD", filePath(document)},
		{"POST", filePath(document)},
		{"GET", callbackPath(document)},
		{"GET", filePath(document) + "/"},
		{"GET", filePath(document) + "/extra"},
		{"GET", strings.Replace(filePath(document), "/office/", "/%6fffice/", 1)},
		{"GET", filePath(document) + "?release=" + token},
		{"GET", filePath(document) + "?token=" + token},
	} {
		if code, _ := request(tc.method, tc.path, aliceToken, "", nil, nil); code != 503 {
			t.Fatalf("capability expanded maintenance scope: %s returned %d", tc.method+" "+tc.path, code)
		}
	}
	if code, _ := request("GET", filePath(document), "", "", nil, map[string]string{"Origin": "http://unapproved.invalid"}); code != 403 {
		t.Fatal("maintenance exception bypassed ordinary Origin validation")
	}
	if code, _ := request("POST", callbackPath(document), "", "", callback, map[string]string{"Sec-Fetch-Site": "cross-site"}); code != 403 {
		t.Fatal("maintenance exception bypassed ordinary cross-site write validation")
	}
	current, err := os.ReadFile(filepath.Join(session.cwd, "account.docx"))
	if err != nil || !bytes.Equal(current, original) || document.view().Revision != 0 {
		t.Fatal("maintenance callback changed the user's canonical file")
	}
	// The POST-open path must bind the same grant, even for an existing document
	// originally opened without maintenance authorization.
	if got := open("second.docx", secondInstance, token); got != second || grantOf(second) != sha256.Sum256([]byte(token)) {
		t.Fatal("authenticated open did not bind its own existing document")
	}
	if code, _ := request("GET", filePath(second), "", "", nil, nil); code != 200 {
		t.Fatal("document opened through maintenance could not be downloaded")
	}
	// Rotating the maintenance epoch invalidates all old document grants until
	// their rightful owner requests another config through the new header.
	nextToken := strings.Repeat("b", 64)
	writeMarker(nextToken)
	for _, route := range []string{filePath(document), callbackPath(document), filePath(second)} {
		method := "GET"
		if strings.Contains(route, "/callbacks/") {
			method = "POST"
		}
		if code, _ := request(method, route, "", "", callback, nil); code != 503 {
			t.Fatal("old maintenance epoch capability was replayed")
		}
	}
	if code, _ := request("GET", configPath(document, instance), aliceToken, token, nil, nil); code != 503 {
		t.Fatal("old operator maintenance token was replayed")
	}
	if code, _ := request("GET", configPath(document, officeNewID()), aliceToken, nextToken, nil, nil); code != 409 || grantOf(document) != sha256.Sum256([]byte(token)) {
		t.Fatal("failed config updated the old maintenance epoch")
	}
	if code, _ := request("GET", configPath(document, instance), aliceToken, nextToken, nil, nil); code != 200 {
		t.Fatal("rightful owner could not rebind the new maintenance epoch")
	}
	if code, _ := request("GET", filePath(document), "", "", nil, nil); code != 200 {
		t.Fatal("new maintenance grant was not usable")
	}
	if code, _ := request("POST", "/api/office/sessions/"+document.id+"/close", aliceToken, nextToken, map[string]any{}, nil); code != 200 || grantOf(document) != zero {
		t.Fatal("closing a document did not revoke its scoped grant")
	}
	for _, route := range []string{filePath(document), callbackPath(document)} {
		method := "GET"
		if strings.Contains(route, "/callbacks/") {
			method = "POST"
		}
		if code, _ := request(method, route, "", "", callback, nil); code != 503 {
			t.Fatal("closed document capability passed maintenance")
		}
	}
	reopened := open("account.docx", officeNewID(), nextToken)
	if reopened.id == document.id || reopened.fileToken == document.fileToken {
		t.Fatal("reopened document reused a retired capability")
	}
	if code, _ := request("GET", "/office/files/"+reopened.id+"/"+document.fileToken, "", "", nil, nil); code != 503 {
		t.Fatal("old file capability was replayed against a new document session")
	}
	if code, _ := request("GET", filePath(reopened), "", "", nil, nil); code != 200 {
		t.Fatal("new document session could not use its own scoped capability")
	}
	h.psrv.office.revokeConversation(conversation)
	if code, _ := request("GET", filePath(reopened), "", "", nil, nil); code != 503 || grantOf(reopened) != zero {
		t.Fatal("revoked conversation retained a usable maintenance grant")
	}
}

func TestOfficeReleaseMaintenanceRequiresLiveLeaseAndValidMarker(t *testing.T) {
	service, document, _ := officeTestService(t)
	m := &Manager{cfg: Config{SessionDir: filepath.Join(t.TempDir(), "sessions")}}
	s := &Server{mgr: m, office: service, mux: http.NewServeMux()}
	s.officeRoutes()
	token := strings.Repeat("c", 64)
	marker := m.releaseMaintenancePath()
	if err := os.WriteFile(marker, []byte(`{"token":"`+token+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	document.mu.Lock()
	document.releaseGrant = sha256.Sum256([]byte(token))
	document.mu.Unlock()
	request := func() int {
		r := httptest.NewRequest("GET", "/office/files/"+document.id+"/"+document.fileToken, nil)
		w := httptest.NewRecorder()
		s.withReleaseMaintenance(s.mux).ServeHTTP(w, r)
		return w.Code
	}
	if got := request(); got != 200 {
		t.Fatalf("valid scoped live lease returned %d", got)
	}
	service.mu.Lock()
	delete(service.active, document.absolute)
	service.mu.Unlock()
	if request() != 503 {
		t.Fatal("inactive document lease passed the maintenance gate")
	}
	service.mu.Lock()
	service.active[document.absolute] = document.id
	service.closed = true
	service.mu.Unlock()
	if request() != 503 {
		t.Fatal("stopped Office service passed the maintenance gate")
	}
	service.mu.Lock()
	service.closed = false
	service.mu.Unlock()
	for _, content := range []string{"", "{}", `{"token":"bad"}`, strings.Repeat("x", 4097)} {
		if err := os.WriteFile(marker, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if request() != 503 {
			t.Fatal("malformed maintenance marker admitted a scoped capability")
		}
	}
}
