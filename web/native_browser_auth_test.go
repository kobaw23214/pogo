package web

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

const nativeAuthPublicFixture = `{"mode":"automatic","status":"credentials_present","origin":"https://example.test","cookie_available":true,"bearer_storage_keys":["access_token"],"reason":"credential_selection_required","credential_ref":"private-reference","storage_key":"private-selection","token_values":{"access_token":"private-token"},"secret":"private-secret","cookies":[{"name":"session","value":"private-cookie"}],"authorization":"Bearer private-header","paths":["/private-scope"],"fields":["private-field"],"expires_at":1800000000000}`

func TestNativeBrowserAuthRetiredRouteKeepsOwnershipAndCSRFWithoutWorker(t *testing.T) {
	var requests atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.mgr.cfg.browser = cfg
	sid := h.createAs(aliceToken)
	path := "/api/sessions/" + sid + "/browser/auth"
	for _, tc := range []struct {
		token string
		code  int
	}{{"", 401}, {bobToken, 404}} {
		res := h.doAs(tc.token, "POST", path, `{"action":"grant"}`)
		res.Body.Close()
		if res.StatusCode != tc.code {
			t.Fatalf("ownership: got %d want %d", res.StatusCode, tc.code)
		}
	}
	req, _ := http.NewRequest("POST", h.srv.URL+path, strings.NewReader(`{"action":"grant"}`))
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	req.Header.Set("Origin", "https://unrelated.test")
	res, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin mutation accepted: %d", res.StatusCode)
	}
	for _, body := range []string{`{"action":"grant","token":"private-token"}`, `{"action":"revoke"}`, `null`, `not JSON private-token`} {
		res := h.doAs(aliceToken, "POST", path, body)
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusGone || strings.Contains(string(raw), "private-token") || res.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("retired endpoint response: %d %s", res.StatusCode, raw)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("retired authorization created or contacted the worker")
	}
}

func TestNativeBrowserAuthStateGETSanitizesCredentialsAndPreservesOwnership(t *testing.T) {
	var gets atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			writeJSON(w, 200, map[string]bool{"ok": true})
		case strings.HasSuffix(r.URL.Path, "/state"):
			gets.Add(1)
			writeJSON(w, 200, map[string]any{"control": "agent", "url": "https://example.test/", "api_access": json.RawMessage(nativeAuthPublicFixture), "lease_token": "private-lease", "worker_token": "private-worker"})
		default:
			writeJSON(w, 200, map[string]bool{"ok": true})
		}
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.mgr.cfg.browser = cfg
	sid := h.createAs(aliceToken)
	path := "/api/sessions/" + sid + "/browser"
	for _, token := range []string{"", bobToken} {
		res := h.doAs(token, "GET", path, "")
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized && res.StatusCode != http.StatusNotFound {
			t.Fatalf("non-owner could read authorization scope: %d", res.StatusCode)
		}
	}
	if gets.Load() != 0 {
		t.Fatal("non-owner status read reached worker")
	}
	res := h.doAs(aliceToken, "GET", path, "")
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var reply map[string]json.RawMessage
	if res.StatusCode != http.StatusOK || json.Unmarshal(raw, &reply) != nil || reply["api_access"] != nil || strings.Contains(string(raw), "private-") || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("public status must retain safe scope only: status=%d", res.StatusCode)
	}
	// Invalid future worker shapes are omitted rather than forwarded verbatim.
	state, err := nativeBrowserState([]byte(`{"control":"agent","api_access":{"status":"active","secret":"private-token"}}`), false)
	if err != nil || state["api_access"] != nil || string(state["control"]) != `"agent"` {
		t.Fatal("malformed grant contaminated the public browser state")
	}
}
