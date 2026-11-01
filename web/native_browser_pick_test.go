package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func nativePickInputFixture() nativeBrowserPickInput {
	x, y := 12.0, 20.0
	return nativeBrowserPickInput{X: &x, Y: &y, Version: "1:t1:1:1", TabID: "t1", ContextRef: nativeSheetsContext}
}

const nativePickFixture = `{"context_ref":"ctx_1234567890abcdef12345678","version":"1:t1:1:1","tab_id":"t1","url":"https://example.test/","title":"Fixture","viewport":{"width":1280,"height":800},"element":{"ref":"p1","tag":"button","role":"button","name":"Submit","text":"Submit","rect":{"x":10,"y":10,"width":100,"height":30},"value":"private-input","html":"private-html","lease_token":"private-lease"},"worker_token":"private-token"}`

func TestNativeBrowserPickPublicShapeAndVersion(t *testing.T) {
	input := nativePickInputFixture()
	result, err := decodeNativeBrowserPick([]byte(nativePickFixture), input)
	if err != nil || result.Element == nil || result.Element.Ref != "p1" {
		t.Fatalf("valid observation rejected: %+v %v", result, err)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "value") {
		t.Fatal("worker private fields leaked into page context")
	}
	for _, tc := range []struct{ name, old, replacement string }{
		{"version", `"1:t1:1:1"`, `"1:t1:2:1"`},
		{"tab", `"tab_id":"t1"`, `"tab_id":"t2"`},
		{"outside", `"x":10`, `"x":1270`},
		{"wrong-element", `"x":10`, `"x":100`},
		{"viewport", `"width":1280`, `"width":10`},
		{"action-ref", `"p1"`, `"e1"`},
		{"text-limit", `"text":"Submit"`, `"text":"` + strings.Repeat("字", 801) + `"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeNativeBrowserPick([]byte(strings.Replace(nativePickFixture, tc.old, tc.replacement, 1)), input); err == nil {
				t.Fatal("unsafe or stale observation accepted")
			}
		})
	}
	var noHit map[string]any
	_ = json.Unmarshal([]byte(nativePickFixture), &noHit)
	noHit["element"] = nil
	raw, _ = json.Marshal(noHit)
	if result, err := decodeNativeBrowserPick(raw, input); err != nil || result.Element != nil {
		t.Fatal("empty hit must remain empty, never invent an element")
	}
}

func TestNativeBrowserPickAuthorizationAndValidation(t *testing.T) {
	var requests, picks, sideEffects atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch {
		case r.Method == http.MethodPut:
			writeJSON(w, 200, map[string]bool{"ok": true})
		case strings.HasSuffix(r.URL.Path, "/pick"):
			picks.Add(1)
			var input nativeBrowserPickInput
			if json.NewDecoder(r.Body).Decode(&input) != nil || !validNativePickInput(input) {
				t.Error("bad worker pick request")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, nativePickFixture)
		default:
			sideEffects.Add(1)
			writeJSON(w, 200, map[string]bool{"ok": true})
		}
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.mgr.cfg.browser = cfg
	sid := h.createAs(aliceToken)
	path := "/api/sessions/" + sid + "/browser/pick"
	body := `{"context_ref":"ctx_1234567890abcdef12345678","x":12,"y":20,"version":"1:t1:1:1","tab_id":"t1"}`
	for _, tc := range []struct {
		token string
		code  int
	}{{"", 401}, {bobToken, 404}} {
		res := h.doAs(tc.token, "POST", path, body)
		res.Body.Close()
		if res.StatusCode != tc.code {
			t.Fatalf("pick authorization: got %d want %d", res.StatusCode, tc.code)
		}
	}
	req, _ := http.NewRequest("POST", h.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	req.Header.Set("Origin", "https://other-origin.test")
	res, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("cross-origin pick got %d", res.StatusCode)
	}
	for _, invalid := range []string{
		`{}`, `null`, `{"x":null,"y":20,"version":"v1","tab_id":"t1"}`,
		`{"x":-1,"y":20,"version":"v1","tab_id":"t1"}`,
		`{"x":12,"y":8192,"version":"v1","tab_id":"t1"}`,
		`{"x":12,"y":20,"version":"v1","tab_id":"../t1"}`,
		`{"x":12,"y":20,"version":"v1","tab_id":"t1","lease_token":"ignored?"}`,
		body + `{}`,
	} {
		res := h.doAs(aliceToken, "POST", path, invalid)
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatalf("invalid pick accepted: %d %s", res.StatusCode, invalid)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("unauthorized or malformed pick reached the worker")
	}
	res = h.doAs(aliceToken, "POST", path, body)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || picks.Load() != 1 || sideEffects.Load() != 0 || strings.Contains(string(raw), "private-") || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("read-only selection failed: status=%d picks=%d sideEffects=%d body=%s", res.StatusCode, picks.Load(), sideEffects.Load(), raw)
	}
}

func TestNativeBrowserPickTimeoutDoesNotCancelOrStealControl(t *testing.T) {
	started := make(chan struct{})
	var sideEffects atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			writeJSON(w, 200, map[string]bool{"ok": true})
		case strings.HasSuffix(r.URL.Path, "/pick"):
			_, _ = io.Copy(io.Discard, r.Body)
			close(started)
			<-r.Context().Done()
		default:
			sideEffects.Add(1)
			writeJSON(w, 200, map[string]bool{"ok": true})
		}
	})
	b := newNativeBrowser(cfg)
	t.Cleanup(b.Close)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := b.Pick(ctx, nativePickInputFixture()); done <- err }()
	nativeWait(t, started)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lost read cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read did not abort")
	}
	if sideEffects.Load() != 0 {
		t.Fatal("read cancellation invoked a browser action")
	}
	// A background observation must not wait for the unrelated Agent queue.
	cfg2 := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/pick") {
			t.Error("picker attempted a mutation")
		}
		_, _ = io.WriteString(w, nativePickFixture)
	})
	other := newNativeBrowser(cfg2)
	if err := other.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer other.release()
	if _, err := other.Pick(context.Background(), nativePickInputFixture()); err != nil {
		t.Fatalf("read-only pick waited for unrelated Agent: %v", err)
	}
}
