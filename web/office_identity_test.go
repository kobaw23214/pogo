package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
)

type officeLostConfigResponse struct{ header http.Header }

func (w *officeLostConfigResponse) Header() http.Header     { return w.header }
func (*officeLostConfigResponse) WriteHeader(int)           {}
func (*officeLostConfigResponse) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestOfficeOpenLostConfigResponseRecoversOnlySameOwnedInstance(t *testing.T) {
	var healthy atomic.Bool
	var probes atomic.Int32
	healthy.Store(true)
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		body := officeReadinessTestBody()
		body["ready"] = healthy.Load()
		writeJSON(w, http.StatusOK, body)
	}))
	defer engine.Close()
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return llm.Response{}, nil })
	secret := filepath.Join(t.TempDir(), "jwt")
	if err := os.WriteFile(secret, []byte(strings.Repeat("s", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	h.psrv.opts.Office = &OfficeOptions{ServiceURL: engine.URL, PublicURL: h.srv.URL, JWTSecretFile: secret}
	if err := h.psrv.initOffice(); err != nil {
		t.Fatal(err)
	}
	defer h.psrv.closeOffice()
	conversation := h.createAs(aliceToken)
	session := h.session(conversation)
	file := filepath.Join(session.cwd, "lost-response.docx")
	original := officeTestPackage(t, "preserve saved content")
	if err := os.WriteFile(file, original, 0600); err != nil {
		t.Fatal(err)
	}
	instance := "original_open_instance_001"
	request := func(conversationID, instanceID string) *http.Request {
		body, _ := json.Marshal(map[string]string{"path": "lost-response.docx", "conversation_id": conversationID, "instanceId": instanceID})
		r := httptest.NewRequest(http.MethodPost, h.srv.URL+"/api/office/sessions", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+aliceToken)
		return r
	}
	// The server claims the editor but cannot deliver its first response.
	h.psrv.Handler().ServeHTTP(&officeLostConfigResponse{header: make(http.Header)}, request(conversation, instance))
	document := h.psrv.office.findDocument(file)
	if document == nil {
		t.Fatal("initial request did not create its claim")
	}
	document.mu.Lock()
	id, bootstrap, bridge := document.id, document.bootstrapToken, document.bridgeToken
	document.canvasMounted, document.blocked = true, true
	document.dirtyEpoch, document.persistedEpoch = 3, 1
	document.mu.Unlock()
	if bootstrap == "" {
		t.Fatal("initial request did not bind a bootstrap capability")
	}
	healthy.Store(false)
	officeReadinessTestExpire(h.psrv.office)
	initialProbes := probes.Load()
	for _, test := range []struct {
		instance string
		status   int
	}{
		{instance, http.StatusOK},
		{"foreign_open_instance_001", http.StatusConflict},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		r := request(conversation, test.instance).WithContext(ctx)
		response := httptest.NewRecorder()
		h.psrv.Handler().ServeHTTP(response, r)
		cancel()
		if response.Code != test.status {
			t.Fatalf("claim retry status: %d %s", response.Code, response.Body)
		}
		if test.status == http.StatusOK {
			var reply struct {
				Session        officeSessionView `json:"session"`
				BootstrapToken string            `json:"bootstrapToken"`
				BridgeToken    string            `json:"bridgeToken"`
			}
			if json.Unmarshal(response.Body.Bytes(), &reply) != nil || reply.Session.ID != id || reply.BootstrapToken != bootstrap || reply.BridgeToken != bridge || !reply.Session.Dirty || !reply.Session.Blocked || !reply.Session.CanvasMounted {
				t.Fatal("same-instance retry replaced its claim or cleared protected state")
			}
		}
	}
	if probes.Load() != initialProbes || h.psrv.office.findDocument(file) != document {
		t.Fatal("claim recovery waited for readiness or created a replacement")
	}
	if err := h.psrv.office.releaseBootstrap(document, instance, bootstrap, officeFileRef(document.owner, document.panelPath), document.bootEpoch); err == nil {
		t.Fatal("claim recovery weakened bootstrap release protection")
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("claim recovery modified the document")
	}
}

func TestOfficeFileIdentitySurvivesEditorAndServiceRestart(t *testing.T) {
	service, document, file := officeTestService(t)
	first := document.view()
	if first.FileRef == "" || first.BootEpoch == "" || strings.Contains(first.FileRef, document.cwd) {
		t.Fatal("missing or filesystem-dependent public file identity")
	}
	if err := service.closeDocument(document); err != nil {
		t.Fatal(err)
	}
	reopened, err := service.open(document.conversationID, document.cwd, nil, document.hub, file, first.Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	next := reopened.view()
	if next.ID == first.ID || next.FileRef != first.FileRef || next.BootEpoch != first.BootEpoch {
		t.Fatal("reopening changed stable file identity or retained stale editor identity")
	}
	other := &officeService{cfg: service.cfg, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}, batchDir: service.batchDir}
	t.Cleanup(other.close)
	afterRestart, err := other.open(document.conversationID, document.cwd, nil, document.hub, file, first.Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted := afterRestart.view()
	if restarted.FileRef != first.FileRef || restarted.BootEpoch == first.BootEpoch || restarted.BootEpoch == "" {
		t.Fatal("service restart did not preserve file identity with a new boot epoch")
	}
	alice, bob := identity.LocalScope("alice"), identity.LocalScope("bob")
	if officeFileRef(&alice, "agents/a/uploads/test.xlsx") == officeFileRef(&bob, "agents/a/uploads/test.xlsx") {
		t.Fatal("same named files from distinct owners share an identity")
	}
}

func officeBootstrapClaim(t *testing.T, service *officeService, d *officeDocumentSession) (string, string) {
	t.Helper()
	instance := officeNewID()
	recorder := httptest.NewRecorder()
	(&Server{office: service}).writeOfficeConfig(recorder, d, instance, [32]byte{})
	var reply struct {
		BootstrapToken string `json:"bootstrapToken"`
	}
	if recorder.Code != 200 || json.Unmarshal(recorder.Body.Bytes(), &reply) != nil || reply.BootstrapToken == "" {
		t.Fatal("bootstrap config failed")
	}
	return instance, reply.BootstrapToken
}

func TestOfficeBootstrapReleaseRequiresExactUnusedClaim(t *testing.T) {
	for _, test := range []struct {
		name  string
		alter func(*officeDocumentSession, *string, *string, *string, *string)
	}{
		{"foreign instance", func(_ *officeDocumentSession, i, _, _, _ *string) { *i = officeNewID() }},
		{"foreign token", func(_ *officeDocumentSession, _, token, _, _ *string) { *token = "another-bootstrap-token" }},
		{"foreign file", func(_ *officeDocumentSession, _, _, ref, _ *string) { *ref = "another-file" }},
		{"stale boot", func(_ *officeDocumentSession, _, _, _, epoch *string) { *epoch = officeNewID() }},
		{"mounted", func(d *officeDocumentSession, _, _, _, _ *string) { d.canvasMounted = true }},
		{"ready", func(d *officeDocumentSession, _, _, _, _ *string) { d.ready = true }},
		{"dirty", func(d *officeDocumentSession, _, _, _, _ *string) { d.dirtyEpoch++ }},
		{"saved history", func(d *officeDocumentSession, _, _, _, _ *string) { d.dirtyEpoch++; d.persistedEpoch++; d.revision++ }},
		{"unknown", func(d *officeDocumentSession, _, _, _, _ *string) { d.blocked = true }},
		{"pending", func(d *officeDocumentSession, _, _, _, _ *string) { d.queue = []*officeOperation{{}} }},
		{"prior operation", func(d *officeDocumentSession, _, _, _, _ *string) {
			d.operations["finished"] = &officeOperation{finished: true}
		}},
		{"save", func(d *officeDocumentSession, _, _, _, _ *string) { d.saving = true }},
		{"export", func(d *officeDocumentSession, _, _, _, _ *string) { d.exportBusy = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, d, _ := officeTestService(t)
			instance, token := officeBootstrapClaim(t, service, d)
			view := d.view()
			ref, epoch := view.FileRef, view.BootEpoch
			d.mu.Lock()
			test.alter(d, &instance, &token, &ref, &epoch)
			d.mu.Unlock()
			if err := service.releaseBootstrap(d, instance, token, ref, epoch); err == nil || d.view().Closed {
				t.Fatal("unsafe bootstrap release retired a document")
			}
		})
	}
	service, d, file := officeTestService(t)
	before, _ := os.ReadFile(file)
	instance, token := officeBootstrapClaim(t, service, d)
	view := d.view()
	for attempt := 0; attempt < 2; attempt++ {
		if err := service.releaseBootstrap(d, instance, token, view.FileRef, view.BootEpoch); err != nil {
			t.Fatal(err)
		}
	}
	if !d.view().Closed {
		t.Fatal("safe release retained an orphan claim")
	}
	reopened, err := service.open(d.conversationID, d.cwd, d.owner, d.hub, file, d.panelPath, nil)
	if err != nil || reopened.id == d.id {
		t.Fatal("retry did not get a fresh editor session")
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("bootstrap release modified the saved file")
	}
}

func TestOfficeMountedSignalProtectsCanvasBeforePluginReady(t *testing.T) {
	service, d, _ := officeTestService(t)
	instance, token := officeBootstrapClaim(t, service, d)
	if reply := officeTestBridge(t, service, d, "POST", "mounted", map[string]string{"instanceId": "foreign-instance"}, ""); reply.Code != 409 {
		t.Fatal("foreign canvas marked a claim mounted")
	}
	if reply := officeTestBridge(t, service, d, "POST", "mounted", map[string]string{"instanceId": instance}, ""); reply.Code != 200 {
		t.Fatal("legitimate canvas mounting failed")
	}
	view := d.view()
	if !view.CanvasMounted || view.Ready {
		t.Fatal("canvas mounting incorrectly claimed agent readiness")
	}
	officeExpectError(t, service.releaseBootstrap(d, instance, token, view.FileRef, view.BootEpoch), "BOOTSTRAP_RELEASE_UNSAFE")
}

func TestOfficeOwnedLegacyPathsAndBootstrapReleaseAuthorization(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return llm.Response{}, nil })
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
	file := filepath.Join(session.cwd, "manual.docx")
	if err := os.WriteFile(file, officeTestPackage(t, "manual input"), 0600); err != nil {
		t.Fatal(err)
	}
	canonical, err := officePanelPath(h.mgr.UserRoot(*session.owner), file)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := officePanelPath(h.mgr.Cwd(), file)
	if err != nil {
		t.Fatal(err)
	}
	request := func(token, method, path string, body any) (int, []byte) {
		t.Helper()
		data, _ := json.Marshal(body)
		response := h.doAs(token, method, path, string(data))
		defer response.Body.Close()
		result, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, result
	}
	instance := officeNewID()
	code, raw := request(aliceToken, "POST", "/api/office/sessions", map[string]string{"path": legacy, "conversation_id": conversation, "instanceId": instance})
	var opened struct {
		Session        officeSessionView `json:"session"`
		BootstrapToken string            `json:"bootstrapToken"`
		RequestedPath  string            `json:"requestedPath"`
	}
	if code != 200 || json.Unmarshal(raw, &opened) != nil || opened.Session.Path != canonical || opened.RequestedPath != legacy {
		t.Fatalf("legacy owner path did not bind to canonical workspace identity: code=%d", code)
	}
	code, raw = request(aliceToken, "GET", "/api/office/sessions?conversation_id="+conversation+"&path="+url.QueryEscape(legacy), nil)
	var identified struct {
		Sessions      []officeSessionView `json:"sessions"`
		RequestedPath string              `json:"requestedPath"`
		FileRef       string              `json:"fileRef"`
	}
	if code != 200 || json.Unmarshal(raw, &identified) != nil || len(identified.Sessions) != 1 || identified.RequestedPath != legacy || identified.FileRef != opened.Session.FileRef || identified.Sessions[0].FileRef != identified.FileRef {
		t.Fatal("authorized legacy recovery lookup did not bind the requested file")
	}
	toolRaw, err := h.psrv.office.link(conversation, session.cwd, session.owner, session.hub).Execute(context.Background(), json.RawMessage(`{"action":"open","path":"manual.docx"}`))
	if err != nil {
		t.Fatal(err)
	}
	var agent struct {
		Session officeSessionView `json:"session"`
	}
	if json.Unmarshal(toolRaw, &agent) != nil || agent.Session.Path != canonical || agent.Session.FileRef != opened.Session.FileRef {
		t.Fatal("agent and UI disagree on file identity")
	}
	for _, forbidden := range []string{"bootstrapToken", opened.BootstrapToken} {
		if bytes.Contains(toolRaw, []byte(forbidden)) {
			t.Fatal("agent metadata leaked a bootstrap capability")
		}
	}
	bobScope := identity.LocalScope("bob")
	bobPrefix, _ := h.mgr.UserRootRel(bobScope)
	if code, _ := request(aliceToken, "POST", "/api/office/sessions", map[string]string{"path": bobPrefix + "/manual.docx", "conversation_id": conversation, "instanceId": officeNewID()}); code != 400 {
		t.Fatal("foreign legacy prefix accepted")
	}
	if code, _ := request(aliceToken, "GET", "/api/office/sessions?conversation_id="+conversation+"&path="+url.QueryEscape(bobPrefix+"/manual.docx"), nil); code != 400 {
		t.Fatal("recovery lookup accepted a foreign owner prefix")
	}
	configPath := "/api/office/sessions/" + opened.Session.ID + "/config?instanceId=" + instance + "&path=other.docx"
	if code, _ := request(aliceToken, "GET", configPath, nil); code != 400 {
		t.Fatal("config path rebind accepted a different file")
	}
	releaseBody := map[string]string{"instanceId": instance, "bootstrapToken": opened.BootstrapToken, "fileRef": opened.Session.FileRef, "bootEpoch": opened.Session.BootEpoch}
	releasePath := "/api/office/sessions/" + opened.Session.ID + "/release"
	if code, _ := request(bobToken, "POST", releasePath, releaseBody); code != 404 {
		t.Fatal("foreign owner released or discovered a claim")
	}
	if code, _ := request(aliceToken, "POST", releasePath, releaseBody); code != http.StatusOK {
		t.Fatal("owned bootstrap release failed")
	}
}
