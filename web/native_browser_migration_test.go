package web

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNativeBrowserURLMigrationIsSeparateFromNavigation(t *testing.T) {
	s := &Server{opts: ServerOptions{Panels: []Panel{
		{Name: "compat", URL: "https://original.test/app/?entry=A#/home", ProxyMode: "compat"},
		{Name: "plain", URL: "https://original.test/base/", ProxyMode: "passthrough"},
	}}}
	for _, tc := range []struct{ input, want string }{
		{"/panels/compat/", "https://original.test/app/?entry=A#/home"},
		{"/panels/compat/orders?q=1#detail", "https://original.test/orders?q=1#detail"},
		{"/panels/plain/orders?q=2#item", "https://original.test/base/orders?q=2#item"},
		{"/panels/compat/foo%2Fbar?q=a%2Fb#x", "https://original.test/foo%2Fbar?q=a%2Fb#x"},
		{"/panels/plain/foo%2Fbar", "https://original.test/base/foo%2Fbar"},
		{"https://other.test/path?q=3#x", "https://other.test/path?q=3#x"},
	} {
		got, err := s.migrateBrowserURL(tc.input)
		if err != nil || got != tc.want {
			t.Fatalf("migrate %q = %q, %v; want %q", tc.input, got, err, tc.want)
		}
		if strings.HasPrefix(tc.input, "/panels/") {
			if _, err := s.nativeNavigationURL(tc.input); err == nil {
				t.Fatal("live navigation still accepts a proxy shortcut")
			}
		}
	}
	for _, input := range []string{"/panels/missing/a", "//other.test/panels/compat/a", "/api/sessions", "javascript:alert(1)", "https://user:pass@other.test/"} {
		if _, err := s.migrateBrowserURL(input); err == nil {
			t.Fatalf("invalid/unresolved saved URL accepted: %q", input)
		}
	}
}

func TestNativeBrowserMigrationAPIAndUnavailableState(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.psrv.opts.Panels = []Panel{{Name: "saved", URL: "https://original.test/app/", ProxyMode: "compat"}}
	for _, tc := range []struct {
		token, body string
		status      int
	}{
		{"", `{"urls":["/panels/saved/"]}`, http.StatusUnauthorized},
		{aliceToken, `{"urls":[]}`, http.StatusBadRequest},
		{aliceToken, `{"urls":["https://original.test"],"extra":true}`, http.StatusBadRequest},
		{aliceToken, `{"urls":["/panels/saved/item?q=1#x","/panels/missing/"]}`, http.StatusOK},
	} {
		res := h.doAs(tc.token, "POST", "/api/browser/migrate-urls", tc.body)
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != tc.status {
			t.Fatalf("migration HTTP %d want %d: %s", res.StatusCode, tc.status, raw)
		}
		if tc.status == http.StatusOK {
			var result struct {
				Mappings []struct {
					URL      string `json:"url"`
					Resolved string `json:"resolved_url"`
					Error    string `json:"error"`
				} `json:"mappings"`
			}
			if json.Unmarshal(raw, &result) != nil || len(result.Mappings) != 2 || result.Mappings[0].Resolved != "https://original.test/item?q=1#x" || result.Mappings[1].Error == "" {
				t.Fatalf("migration lost a resolved or repairable entry: %s", raw)
			}
			if res.Header.Get("Cache-Control") != "no-store" {
				t.Fatal("migration response can be cached")
			}
		}
	}
	sid := h.createAs(aliceToken)
	base := "/api/sessions/" + sid + "/browser"
	res := h.doAs(aliceToken, "GET", base, "")
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"backend":"unavailable"`) || strings.Contains(string(raw), "legacy") {
		t.Fatalf("missing worker must report unavailable: %d %s", res.StatusCode, raw)
	}
	res = h.doAs(aliceToken, "POST", base+"/control", `{"mode":"human"}`)
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("missing worker control HTTP %d", res.StatusCode)
	}
}
