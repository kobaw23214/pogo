package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func authoringCreate(t *testing.T, h *muHarness, token string) string {
	t.Helper()
	response := h.doAs(token, "POST", "/api/browser/authoring-sessions", `{}`)
	defer response.Body.Close()
	var result struct {
		ID        string `json:"session_id"`
		ExpiresIn int    `json:"expires_in"`
	}
	if response.StatusCode != http.StatusCreated || json.NewDecoder(response.Body).Decode(&result) != nil || !isBrowserAuthoringID(result.ID) || result.ExpiresIn != int(browserAuthoringIdleTTL.Seconds()) {
		t.Fatalf("invalid browser authoring creation: status=%d, %+v", response.StatusCode, result)
	}
	return result.ID
}

func authoringStatus(t *testing.T, h *muHarness, token, method, path, body string, want int) string {
	t.Helper()
	response := h.doAs(token, method, path, body)
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != want {
		t.Fatalf("%s %s status=%d want=%d: %s", method, path, response.StatusCode, want, data)
	}
	return string(data)
}

func TestBrowserAuthoringOwnedHiddenAndBrowserOnly(t *testing.T) {
	var puts, deletes atomic.Int32
	worker := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			puts.Add(1)
			var metadata map[string]string
			_ = json.NewDecoder(r.Body).Decode(&metadata)
			if !isBrowserAuthoringID(metadata["client_session_id"]) || len(metadata["profile_key"]) != 64 {
				t.Error("authoring lacked fresh private browser profile identity")
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
		case r.Method == http.MethodDelete:
			deletes.Add(1)
			writeJSON(w, 200, map[string]bool{"ok": true})
		case strings.HasSuffix(r.URL.Path, "/state"):
			writeJSON(w, 200, nativeSheetsState("t1"))
		case strings.HasSuffix(r.URL.Path, "/pick"):
			_, _ = io.WriteString(w, nativePickFixture)
		default:
			writeJSON(w, 200, map[string]bool{"ok": true})
		}
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("must not execute")))
	h.mgr.cfg.browser = worker
	t.Cleanup(h.psrv.closeBrowserAuthoring)
	before, _ := os.ReadDir(h.mgr.cfg.SessionDir)
	id := authoringCreate(t, h, aliceToken)
	if puts.Load() != 0 {
		t.Fatal("creating the editor unexpectedly launched Chromium")
	}
	entry := h.psrv.browserAuthoring[id]
	if entry.session.store != nil || entry.session.agent != nil || entry.session.cwd != "" || entry.session.nativeBrowser.downloads != nil || entry.session.nativeBrowser.uploadRoot != "" || entry.session.nativeBrowser.storeScreenshot != nil {
		t.Fatal("browser authoring acquired conversation or workspace capabilities")
	}
	if len(h.mgr.sessions) != 0 {
		t.Fatal("hidden authoring inserted a Manager conversation")
	}
	if _, err := h.mgr.cfg.Catalog.Get(t.Context(), *entry.owner, id); err == nil {
		t.Fatal("authoring wrote a catalog conversation")
	}
	if list := authoringStatus(t, h, aliceToken, "GET", "/api/sessions", "", 200); strings.Contains(list, id) {
		t.Fatal("authoring leaked into normal conversation list")
	}
	if list := authoringStatus(t, h, bobToken, "GET", "/api/browser/authoring-sessions", "", 200); strings.Contains(list, id) || !strings.Contains(list, `"active":0`) {
		t.Fatal("authoring list leaked another owner")
	}
	if list := authoringStatus(t, h, aliceToken, "GET", "/api/browser/authoring-sessions", "", 200); !strings.Contains(list, id) || !strings.Contains(list, `"active":1`) {
		t.Fatal("owned authoring absent from release observable list")
	}
	base := "/api/sessions/" + id
	authoringStatus(t, h, bobToken, "GET", base+"/browser", "", 404)
	authoringStatus(t, h, bobToken, "DELETE", "/api/browser/authoring-sessions/"+id, "", 404)
	for _, endpoint := range []string{"control", "input", "pick", "pick/preview", "tabs"} {
		authoringStatus(t, h, bobToken, "POST", base+"/browser/"+endpoint, `{}`, 404)
	}
	for _, route := range []string{"", "/attachments", "/transcript", "/export", "/export.md", "/terminal", "/stream"} {
		authoringStatus(t, h, aliceToken, "GET", base+route, "", 404)
	}
	authoringStatus(t, h, aliceToken, "POST", base+"/messages", `{"prompt":"run"}`, 404)
	authoringStatus(t, h, aliceToken, "POST", base+"/control", `{"action":"cancel"}`, 404)
	authoringStatus(t, h, aliceToken, "GET", base+"/browser", "", 200)
	if puts.Load() != 1 {
		t.Fatal("normal browser state did not use existing worker lifecycle")
	}
	pick := authoringStatus(t, h, aliceToken, "POST", base+"/browser/pick", `{"context_ref":"`+nativeSheetsContext+`","x":12,"y":20,"version":"1:t1:1:1","tab_id":"t1"}`, 200)
	if !strings.Contains(pick, `"text":"Submit"`) || strings.Contains(pick, "private-") {
		t.Fatal("existing fenced public element picker was not reused")
	}
	downloads := authoringStatus(t, h, aliceToken, "GET", base+"/browser/downloads", "", 200)
	if !strings.Contains(downloads, `"supported":false`) {
		t.Fatal("browser authoring unexpectedly enabled workspace download import")
	}
	status, ticket := liveTicketRequest(t, h, bobToken, id, h.srv.URL, `{"context_ref":"`+nativeSheetsContext+`","tab_id":"t1"}`)
	if status != 404 || ticket != "" {
		t.Fatal("foreign owner obtained live ticket")
	}
	status, ticket = liveTicketRequest(t, h, aliceToken, id, h.srv.URL, `{"context_ref":"`+nativeSheetsContext+`","tab_id":"t1"}`)
	if status != 200 || ticket == "" {
		t.Fatalf("owned authoring could not use native live pipeline: %d", status)
	}
	authoringStatus(t, h, aliceToken, "DELETE", "/api/browser/authoring-sessions/"+id, "", 204)
	if deletes.Load() != 1 || h.psrv.browserLiveTickets[ticket].sid != "" {
		t.Fatal("editor cancellation did not close context and revoke live ticket")
	}
	authoringStatus(t, h, aliceToken, "GET", base+"/browser", "", 404)
	after, _ := os.ReadDir(h.mgr.cfg.SessionDir)
	transcripts := func(files []os.DirEntry) int {
		n := 0
		for _, file := range files {
			if strings.HasSuffix(file.Name(), ".jsonl") || file.Name() == ".browser-authoring" {
				n++
			}
		}
		return n
	}
	if transcripts(before) != transcripts(after) || len(h.rec.systems()) != 0 {
		t.Fatal("browser authoring wrote a transcript, profile namespace file or invoked a model")
	}
}

func TestBrowserAuthoringTTLAndShutdownAreFenced(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("must not execute")))
	h.mgr.cfg.browser = nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })
	t.Cleanup(h.psrv.closeBrowserAuthoring)
	id := authoringCreate(t, h, aliceToken)
	entry := h.psrv.browserAuthoring[id]
	// A stale timer callback after a heartbeat must leave the active editor open.
	h.psrv.expireBrowserAuthoring(id, entry)
	if h.psrv.browserAuthoring[id] != entry {
		t.Fatal("stale expiration removed a refreshed editor")
	}
	h.psrv.browserAuthoringMu.Lock()
	entry.lastUsed = time.Now().Add(-browserAuthoringIdleTTL - time.Second)
	h.psrv.browserAuthoringMu.Unlock()
	h.psrv.expireBrowserAuthoring(id, entry)
	authoringStatus(t, h, aliceToken, "GET", "/api/sessions/"+id+"/browser", "", 404)
	if !entry.session.nativeBrowser.closed {
		t.Fatal("idle expiry did not release browser")
	}
	newID := authoringCreate(t, h, aliceToken)
	newEntry := h.psrv.browserAuthoring[newID]
	// A callback for a retired editor cannot retire another editor.
	h.psrv.expireBrowserAuthoring(newID, entry)
	if h.psrv.browserAuthoring[newID] != newEntry {
		t.Fatal("stale timer closed a different editor")
	}
	h.psrv.browserAuthoringMu.Lock()
	newEntry.created = time.Now().Add(-browserAuthoringMaxTTL - time.Second)
	h.psrv.browserAuthoringMu.Unlock()
	authoringStatus(t, h, aliceToken, "GET", "/api/browser/authoring-sessions", "", 200)
	if !newEntry.session.nativeBrowser.closed || len(h.psrv.browserAuthoring) != 0 {
		t.Fatal("release observable list did not prune absolute-expired editor")
	}
	last := authoringCreate(t, h, aliceToken)
	lastEntry := h.psrv.browserAuthoring[last]
	h.psrv.closeBrowserLive()
	if !lastEntry.session.nativeBrowser.closed || len(h.psrv.browserAuthoring) != 0 {
		t.Fatal("server shutdown left authoring alive")
	}
	authoringStatus(t, h, aliceToken, "POST", "/api/browser/authoring-sessions", `{}`, 503)
}

func TestBrowserAuthoringRejectsCallerAuthorityAndBoundsConcurrentEditors(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("must not execute")))
	h.mgr.cfg.browser = nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })
	t.Cleanup(h.psrv.closeBrowserAuthoring)
	for _, body := range []string{`null`, `[]`, `{"owner":"bob"}`, `{"workspace":"../bob"}`, `{"model":"k3"}`, `{"session_id":"existing"}`, `{"operation_id":null}`, `{"operation_id":"not-a-uuid"}`, `{"operation_id":"12345678-1234-4234-8234-123456789abc","owner":"bob"}`, `{}` + `{}`} {
		authoringStatus(t, h, aliceToken, "POST", "/api/browser/authoring-sessions", body, 400)
	}
	var created, bounded atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := h.doAs(aliceToken, "POST", "/api/browser/authoring-sessions", `{}`)
			defer response.Body.Close()
			switch response.StatusCode {
			case http.StatusCreated:
				created.Add(1)
			case http.StatusTooManyRequests:
				bounded.Add(1)
			default:
				t.Errorf("unexpected concurrent create status %d", response.StatusCode)
			}
		}()
	}
	wg.Wait()
	if created.Load() != browserAuthoringOwnerLimit || bounded.Load() != 8-browserAuthoringOwnerLimit {
		t.Fatalf("per-owner limit raced: created=%d bounded=%d", created.Load(), bounded.Load())
	}
	// Alice's cap is not charged to another isolated owner.
	_ = authoringCreate(t, h, bobToken)
}

func TestBrowserAuthoringHonorsBoundDesktopOwner(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("must not execute")))
	h.mgr.cfg.browser = nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })
	alice := h.psrv.localIdentity("alice").Scope
	h.mgr.cfg.browserDesktopOwner = &alice
	t.Cleanup(h.psrv.closeBrowserAuthoring)
	_ = authoringCreate(t, h, aliceToken)
	authoringStatus(t, h, bobToken, "POST", "/api/browser/authoring-sessions", `{}`, 403)
}

func TestBrowserAuthoringSingleUserStillHasNoConversationAccess(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("must not execute")))
	h.mgr.cfg.browser = nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, nativeSheetsState("t1")) })
	server, err := NewServer(h.mgr, ServerOptions{Token: h.token})
	if err != nil {
		t.Fatal(err)
	}
	h.srv.Close()
	h.srv = httptest.NewServer(server.Handler())
	t.Cleanup(h.srv.Close)
	t.Cleanup(server.closeBrowserAuthoring)
	response := h.do("POST", "/api/browser/authoring-sessions", `{}`)
	var created struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil || response.StatusCode != 201 || created.SessionID == "" {
		t.Fatalf("single-user create: %d %v", response.StatusCode, err)
	}
	response.Body.Close()
	for _, method := range []string{"GET", "DELETE"} {
		response = h.do(method, "/api/sessions/"+created.SessionID, "")
		response.Body.Close()
		if response.StatusCode != 404 {
			t.Fatalf("authoring ID reached ordinary single-user session %s: %d", method, response.StatusCode)
		}
	}
	response = h.do("POST", "/api/sessions/"+created.SessionID+"/messages", `{"prompt":"run"}`)
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal("single-user authoring acquired model execution")
	}
}

func TestBrowserAuthoringConcurrentCloseOnlyReleasesOwnContextOnce(t *testing.T) {
	var deletes atomic.Int32
	worker := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/state") {
			writeJSON(w, 200, nativeSheetsState("t1"))
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("must not execute")))
	h.mgr.cfg.browser = worker
	t.Cleanup(h.psrv.closeBrowserAuthoring)
	id := authoringCreate(t, h, aliceToken)
	other := authoringCreate(t, h, aliceToken)
	entry := h.psrv.browserAuthoring[id]
	otherEntry := h.psrv.browserAuthoring[other]
	authoringStatus(t, h, aliceToken, "GET", "/api/sessions/"+id+"/browser", "", 200)
	var wg sync.WaitGroup
	var closed atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := h.doAs(aliceToken, "DELETE", "/api/browser/authoring-sessions/"+id, "")
			response.Body.Close()
			if response.StatusCode == 204 {
				closed.Add(1)
			} else if response.StatusCode != 404 {
				t.Errorf("unexpected close status %d", response.StatusCode)
			}
		}()
	}
	wg.Wait()
	if closed.Load() != 1 || deletes.Load() != 1 || !entry.session.nativeBrowser.closed || otherEntry.session.nativeBrowser.closed {
		t.Fatal("racing cleanup duplicated deletion or closed another editor")
	}
	authoringStatus(t, h, aliceToken, "GET", "/api/sessions/"+id+"/browser", "", 404)
}

func TestBrowserAuthoringCreateRetryIsIdempotentAndOwnerScoped(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("must not execute")))
	h.mgr.cfg.browser = nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })
	t.Cleanup(h.psrv.closeBrowserAuthoring)
	body := `{"operation_id":"12345678-1234-4234-8234-123456789abc"}`
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := map[string]bool{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := h.doAs(aliceToken, "POST", "/api/browser/authoring-sessions", body)
			defer response.Body.Close()
			var created struct {
				ID string `json:"session_id"`
			}
			if response.StatusCode != 201 || json.NewDecoder(response.Body).Decode(&created) != nil || created.ID == "" {
				t.Errorf("idempotent creation returned %d", response.StatusCode)
				return
			}
			mu.Lock()
			ids[created.ID] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(ids) != 1 || len(h.psrv.browserAuthoring) != 1 || len(h.psrv.browserAuthoringOperations) != 1 {
		t.Fatal("retry after a lost creation response allocated another browser editor")
	}
	var aliceID string
	for id := range ids {
		aliceID = id
	}
	other := authoringStatus(t, h, bobToken, "POST", "/api/browser/authoring-sessions", body, 201)
	if strings.Contains(other, aliceID) || len(h.psrv.browserAuthoring) != 2 {
		t.Fatal("equal operation IDs shared an editor across owners")
	}
	authoringStatus(t, h, aliceToken, "DELETE", "/api/browser/authoring-sessions/"+aliceID, "", 204)
	authoringStatus(t, h, aliceToken, "POST", "/api/browser/authoring-sessions", body, 410)
	if len(h.psrv.browserAuthoring) != 1 {
		t.Fatal("retry resurrected a closed editor")
	}
}

func TestBrowserAuthoringExpiredOperationDoesNotResurrectAndTombstonesAreBounded(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("must not execute")))
	h.mgr.cfg.browser = nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })
	t.Cleanup(h.psrv.closeBrowserAuthoring)
	const operation = "12345678-1234-4234-8234-123456789abc"
	body := `{"operation_id":"` + operation + `"}`
	created := authoringStatus(t, h, aliceToken, "POST", "/api/browser/authoring-sessions", body, 201)
	var parsed struct {
		ID string `json:"session_id"`
	}
	_ = json.Unmarshal([]byte(created), &parsed)
	h.psrv.browserAuthoringMu.Lock()
	entry := h.psrv.browserAuthoring[parsed.ID]
	entry.lastUsed = time.Now().Add(-browserAuthoringIdleTTL - time.Second)
	h.psrv.browserAuthoringMu.Unlock()
	authoringStatus(t, h, aliceToken, "POST", "/api/browser/authoring-sessions", body, 410)
	if !entry.session.nativeBrowser.closed || len(h.psrv.browserAuthoring) != 0 {
		t.Fatal("expired operation was recreated")
	}
	// A forgotten/closed key is retained for a finite window. Cleanup does not
	// discard a live editor's deduplication key, and the map has explicit caps.
	h.psrv.browserAuthoringMu.Lock()
	for _, operation := range h.psrv.browserAuthoringOperations {
		operation.expires = time.Now().Add(-time.Second)
	}
	h.psrv.browserAuthoringMu.Unlock()
	_ = authoringCreate(t, h, aliceToken)
	if len(h.psrv.browserAuthoringOperations) != 0 {
		t.Fatal("expired operation tombstones were retained indefinitely")
	}
	if browserAuthoringOperationOwnerLimit < browserAuthoringOwnerLimit || browserAuthoringOperationLimit < browserAuthoringTotalLimit {
		t.Fatal("operation caps are inconsistent with active editor caps")
	}
}
