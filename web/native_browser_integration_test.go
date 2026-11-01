package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// Opt-in integration with a real, separately started browser-runtime worker.
// The fixture refuses iframe embedding. No external website or account is used.
func TestNativeBrowserRealRuntime(t *testing.T) {
	endpoint := os.Getenv("POGO_NATIVE_BROWSER_TEST_URL")
	if endpoint == "" {
		t.Skip("set POGO_NATIVE_BROWSER_TEST_URL and POGO_NATIVE_BROWSER_TEST_TOKEN for real Chromium integration")
	}
	var mu sync.Mutex
	var submitted []string
	loginReads := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/debug-worker.js" {
			w.Header().Set("Content-Type", "application/javascript")
			fmt.Fprint(w, `self.fixtureWorkerMarker = "worker-via-go-bridge";`)
			return
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Pogo-View-Fixture", "original-header-still-readable")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'nonce-fixture'; worker-src 'self'; frame-ancestors 'none'")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/submit" {
			_ = r.ParseForm()
			mu.Lock()
			submitted = append(submitted, r.FormValue("account"))
			mu.Unlock()
			fmt.Fprint(w, "<h1>Submitted exactly once</h1>")
			return
		}
		if r.URL.Path == "/login" {
			mu.Lock()
			loginReads++
			mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "fixture_login", Value: "signed-in", Path: "/", HttpOnly: true})
		}
		status := "guest"
		if cookie, err := r.Cookie("fixture_login"); err == nil {
			status = cookie.Value
		}
		fmt.Fprintf(w, `<!doctype html><html><head><title>Original native fixture</title></head><body><h1>Native fixture %s</h1><form action="/submit" method="post"><label>Account <input name="account" id="account" autofocus></label><button id="submit">Submit once</button></form><a href="/other" target="_blank">Open another tab</a></body></html>`, status)
	}))
	defer upstream.Close()
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() }, func(c *Config) {
		c.BrowserURL, c.BrowserToken = endpoint, os.Getenv("POGO_NATIVE_BROWSER_TEST_TOKEN")
	})
	a, b := h.createSession(), h.createSession()
	sa, sb := h.session(a), h.session(b)
	sa.Browser().Arm()
	sb.Browser().Arm()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	op := func(s *Session, args any) tools.BrowserReply {
		t.Helper()
		raw, _ := json.Marshal(args)
		r, err := s.Browser().Execute(ctx, raw)
		if err != nil || !r.OK {
			t.Fatalf("browser operation %s: reply=%+v error=%v", raw, r, err)
		}
		return r
	}
	if sa.hub.Subscribers() != 0 {
		t.Fatal("fixture unexpectedly has SSE clients")
	}
	r := op(sa, map[string]any{"action": "navigate", "url": upstream.URL + "/login"})
	if r.Backend != "native" || r.URL != upstream.URL+"/login" {
		t.Fatalf("did not visit original URL: %+v", r)
	}
	// Exercise the complete model tool -> BrowserGate -> Go bridge -> real CDP
	// path, including structured data returning to the model. No separate
	// browser, profile, SSE client or direct browser endpoint is involved.
	browserTool := &tools.Browser{Link: sa.Browser(), ImageMode: tools.BrowserImageNone, SpillDir: t.TempDir()}
	var listed tools.Result
	for _, check := range []struct{ args, want string }{
		{`{"action":"devtools","method":"help"}`, `"domains"`},
		{`{"action":"devtools","method":"DOM.getDocument","params":{"depth":1}}`, `"root"`},
		{`{"action":"devtools","method":"Runtime.evaluate","params":{"expression":"document.title","returnByValue":true}}`, "Original native fixture"},
		{`{"action":"devtools","method":"network.list","params":{"limit":20}}`, upstream.URL + "/login"},
	} {
		observed, err := browserTool.Execute(ctx, json.RawMessage(check.args))
		if err != nil || strings.Contains(observed.Text, "browser devtools failed:") || !strings.Contains(observed.Text, check.want) {
			t.Fatalf("real CDP tool failed %s: %s %v", check.args, observed.Text, err)
		}
		if strings.Contains(check.args, "network.list") {
			listed = observed
		}
	}
	// The model sees a compact list, while the UI/audit retains the original
	// executor data. Reading its detail must not repeat the website request.
	detail := listed.Details.(map[string]any)["devtools_data"].(json.RawMessage)
	if strings.Contains(listed.Text, "original-header-still-readable") || !strings.Contains(string(detail), "original-header-still-readable") {
		t.Fatal("network projection did not separate model text from full audit detail")
	}
	var network struct {
		Items []struct {
			ID  string `json:"request_id"`
			URL string `json:"url"`
		} `json:"items"`
	}
	if err := json.Unmarshal(detail, &network); err != nil {
		t.Fatal(err)
	}
	requestID := ""
	for _, item := range network.Items {
		if item.URL == upstream.URL+"/login" {
			requestID = item.ID
		}
	}
	if requestID == "" {
		t.Fatal("compact list lost its recorded login request")
	}
	readArgs, _ := json.Marshal(map[string]any{"action": "devtools", "method": "network.read", "params": map[string]any{"request_id": requestID}})
	full, err := browserTool.Execute(ctx, readArgs)
	if err != nil || !strings.Contains(full.Text, "original-header-still-readable") {
		t.Fatalf("recorded header was not readable: %s %v", full.Text, err)
	}
	mu.Lock()
	reads := loginReads
	mu.Unlock()
	if reads != 1 {
		t.Fatalf("view/detail caused website request replay: %d login GETs", reads)
	}
	denied, err := browserTool.Execute(ctx, json.RawMessage(`{"action":"devtools","method":"Target.getTargets"}`))
	if err != nil || !strings.Contains(denied.Text, "DEVTOOLS_METHOD_DENIED") {
		t.Fatalf("browser-global access crossed the bridge: %s %v", denied.Text, err)
	}
	// Resolve a worker through the public inventory, then send the exact opaque
	// reference through the model-facing Go schema and transport.
	op(sa, map[string]any{"action": "devtools", "method": "Runtime.evaluate", "params": map[string]any{"expression": `globalThis.fixtureWorker = new Worker('/debug-worker.js'); 'created'`, "returnByValue": true}})
	var workerTarget string
	waitFor(t, 5*time.Second, func() bool {
		reply := op(sa, map[string]any{"action": "devtools", "method": "targets.list"})
		var inventory struct {
			Items []struct {
				Ref   string `json:"target_ref"`
				Type  string `json:"type"`
				URL   string `json:"url"`
				Ready bool   `json:"ready"`
			} `json:"items"`
		}
		if json.Unmarshal(reply.Data, &inventory) != nil {
			t.Fatalf("invalid target inventory: %s", reply.Data)
		}
		for _, item := range inventory.Items {
			if item.Type == "worker" && item.URL == upstream.URL+"/debug-worker.js" && item.Ready {
				workerTarget = item.Ref
				return true
			}
		}
		return false
	})
	workerArgs, _ := json.Marshal(map[string]any{"action": "devtools", "method": "Runtime.evaluate", "target": workerTarget,
		"params": map[string]any{"expression": "self.fixtureWorkerMarker", "returnByValue": true}})
	workerResult, err := browserTool.Execute(ctx, workerArgs)
	if err != nil || !strings.Contains(workerResult.Text, "worker-via-go-bridge") {
		t.Fatalf("target selection was lost through Go: %s %v", workerResult.Text, err)
	}
	foreignTool := &tools.Browser{Link: sb.Browser(), ImageMode: tools.BrowserImageNone}
	foreignResult, err := foreignTool.Execute(ctx, workerArgs)
	if err != nil || !strings.Contains(foreignResult.Text, "DEVTOOLS_TARGET_NOT_AVAILABLE") || strings.Contains(foreignResult.Text, "worker-via-go-bridge") {
		t.Fatalf("foreign worker target reached another session: %s %v", foreignResult.Text, err)
	}
	r = op(sa, map[string]any{"action": "navigate", "url": upstream.URL + "/"})
	if !strings.Contains(r.Text, "signed-in") {
		t.Fatal("same browser context lost login cookie")
	}
	r = op(sb, map[string]any{"action": "navigate", "url": upstream.URL + "/"})
	if !strings.Contains(r.Text, "guest") || strings.Contains(r.Text, "signed-in") {
		t.Fatal("login cookie leaked across sessions")
	}
	shot, err := (&tools.Browser{Link: sa.Browser()}).Execute(ctx, json.RawMessage(`{"action":"screenshot"}`))
	if err != nil || len(shot.Images) != 1 {
		t.Fatalf("screenshot did not become model image: %+v %v", shot, err)
	}
	if _, err := os.Stat(filepath.Join(sa.cwd, shot.Images[0].ImageRef)); err != nil {
		t.Fatal(err)
	}
	frame := h.do(http.MethodGet, "/api/sessions/"+a+"/browser/frame", "")
	frameBytes, _ := io.ReadAll(frame.Body)
	frame.Body.Close()
	if frame.StatusCode != 200 || frame.Header.Get("X-Browser-Version") == "" || len(frameBytes) < 100 || frameBytes[0] != 0xff {
		t.Fatalf("invalid viewer frame status=%d bytes=%d", frame.StatusCode, len(frameBytes))
	}
	var frameOverlay nativeBrowserOverlay
	overlayJSON, err := base64.RawURLEncoding.DecodeString(frame.Header.Get("X-Browser-Overlay"))
	if err != nil || json.Unmarshal(overlayJSON, &frameOverlay) != nil || frameOverlay.Version != frame.Header.Get("X-Browser-Version") || frameOverlay.LastAction == nil || frameOverlay.LastAction.Action != "screenshot" {
		t.Fatal("real worker frame lost its version-bound action overlay")
	}
	controlBody, _ := json.Marshal(map[string]string{"mode": "human", "context_ref": frame.Header.Get("X-Browser-Context-Ref"), "tab_id": frame.Header.Get("X-Browser-Viewed-Tab")})
	control := h.do(http.MethodPost, "/api/sessions/"+a+"/browser/control", string(controlBody))
	var state struct {
		Lease      string `json:"lease_token"`
		ContextRef string `json:"context_ref"`
		ActiveTab  string `json:"active_tab"`
		Generation int64  `json:"generation"`
		URL        string `json:"url"`
	}
	if err := json.NewDecoder(control.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	control.Body.Close()
	if control.StatusCode != 200 || state.Lease == "" || state.URL != upstream.URL+"/" {
		t.Fatalf("takeover did not preserve same page: %+v status=%d", state, control.StatusCode)
	}
	if reply, err := sa.Browser().Execute(ctx, json.RawMessage(`{"action":"click","target":"#submit"}`)); err == nil && reply.OK {
		t.Fatal("agent executed during human control")
	}
	input := func(body map[string]any) {
		t.Helper()
		body["lease_token"] = state.Lease
		body["context_ref"], body["target_tab"], body["generation"] = state.ContextRef, state.ActiveTab, state.Generation
		raw, _ := json.Marshal(body)
		h.post("/api/sessions/"+a+"/browser/input", string(raw), 200)
	}
	input(map[string]any{"type": "text", "text": "原生浏览器验收"})
	observation := h.do(http.MethodGet, "/api/sessions/"+a+"/browser/frame", "")
	_, _ = io.Copy(io.Discard, observation.Body)
	observation.Body.Close()
	focusJSON, _ := base64.RawURLEncoding.DecodeString(observation.Header.Get("X-Browser-Overlay"))
	var focusOverlay nativeBrowserOverlay
	if observation.StatusCode != 200 || json.Unmarshal(focusJSON, &focusOverlay) != nil || focusOverlay.Focus == nil || !focusOverlay.Focus.Editable || focusOverlay.LastAction == nil || focusOverlay.LastAction.Actor != "human" || focusOverlay.LastAction.Action != "input_text" {
		t.Fatal("human text input lost its focus/activity overlay through Go")
	}
	input(map[string]any{"type": "key", "key": "Enter"})
	waitFor(t, 5*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(submitted) == 1 })
	mu.Lock()
	values := append([]string(nil), submitted...)
	mu.Unlock()
	if len(values) != 1 || values[0] != "原生浏览器验收" {
		t.Fatalf("human input did not arrive exactly once: %v", values)
	}
	back, _ := json.Marshal(map[string]string{"mode": "agent", "lease_token": state.Lease, "context_ref": state.ContextRef, "tab_id": state.ActiveTab})
	h.post("/api/sessions/"+a+"/browser/control", string(back), 200)
	// The server records the POST before Chromium has committed its response.
	// Wait only for the result; never replay the already-submitted Enter input.
	op(sa, map[string]any{"action": "wait", "text": "Submitted exactly once", "seconds": 5})
	r = op(sa, map[string]any{"action": "snapshot"})
	if !strings.Contains(r.Text, "Submitted exactly once") {
		t.Fatalf("handoff lost browser result: %+v", r)
	}
	t.Log("PASS: original URL with frame-ancestors none; no SSE dependency; context cookies isolated; page CDP and worker target selection through the model tool; foreign target rejected; screenshot stored as model image; authenticated JPEG/version; same-page human takeover; agent blocked; Chinese input/submission once; return control preserves result")
}
