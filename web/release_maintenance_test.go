package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReleaseMaintenanceIngressAndAuthentication(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	path := h.mgr.releaseMaintenancePath()
	token := strings.Repeat("a", 64)
	marker, _ := json.Marshal(map[string]string{"token": token})
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, marker, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		route, auth, release string
		want                 int
	}{
		{"/api/sessions", aliceToken, "", 503},
		{"/api/sessions", aliceToken, "bad", 503},
		{"/api/sessions", "", token, 401},
		{"/api/sessions", aliceToken, token, 200},
		{"/mcp", aliceToken, "", 503},
		{"/panels/anything/", aliceToken, "", 503},
		{"/api/auth/status", "", "", 503},
	} {
		req, _ := http.NewRequest("GET", h.srv.URL+tc.route, nil)
		req.Header.Set("Authorization", "Bearer "+tc.auth)
		req.Header.Set("X-Pogo-Release", tc.release)
		res, err := h.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.want {
			t.Fatalf("%s got %d want %d", tc.route, res.StatusCode, tc.want)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	res := h.doAs(aliceToken, "GET", "/api/sessions", "")
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("release did not reopen ingress: %d", res.StatusCode)
	}
}

func TestReleaseMaintenanceMalformedMarkerAndSecretStripping(t *testing.T) {
	m := &Manager{cfg: Config{SessionDir: filepath.Join(t.TempDir(), "sessions")}}
	path := m.releaseMaintenancePath()
	for _, data := range []string{"", `{}`, `{"token":"short"}`, strings.Repeat("x", 4097)} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		active, token := m.releaseMaintenance()
		if !active || token != "" {
			t.Fatal("invalid maintenance marker opened ingress")
		}
		if next := m.fireDuePass(time.Now(), true); next != nil {
			t.Fatal("maintenance scheduler should not fire or read jobs")
		}
	}
	token := strings.Repeat("b", 64)
	os.WriteFile(path, []byte(`{"token":"`+token+`"}`), 0600)
	s := &Server{mgr: m}
	called := false
	handler := s.withReleaseMaintenance(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Header.Get("X-Pogo-Release") != "" {
			t.Fatal("secret forwarded downstream")
		}
		w.WriteHeader(204)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Pogo-Release", token)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if !called || res.Code != 204 {
		t.Fatal("operator request did not reach normal handler")
	}
}
