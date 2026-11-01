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

var nativePreviewTokenFixture = "pv_" + strings.Repeat("a", 48)
var nativePreviewFixture = `{"context_ref":"ctx_1234567890abcdef12345678","version":"1:t1:1:1","tab_id":"t1","viewport":{"width":1280,"height":800},"element":{"tag":"button","role":"button","rect":{"x":10,"y":10,"width":100,"height":30},"name":"private-name","text":"private-text","ref":"p42","backend_node_id":987,"lease_token":"private-lease"},"preview_token":"` + nativePreviewTokenFixture + `","expires_in_ms":3000,"url":"https://private.test/","title":"private-title","worker_token":"private-token"}`

func TestNativeBrowserPickPreviewPublicGeometryAndBindings(t *testing.T) {
	input := nativePickInputFixture()
	result, err := decodeNativeBrowserPickPreview([]byte(nativePreviewFixture), input)
	if err != nil || result.Element == nil || result.Element.Tag != "button" || result.PreviewToken != nativePreviewTokenFixture {
		t.Fatalf("preview rejected: %v", err)
	}
	raw, _ := json.Marshal(result)
	for _, secret := range []string{"private-", "p42", "backend_node_id", "987", "lease_token", "frame_url", "\"url\"", "\"title\"", "\"text\"", "\"name\""} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("preview leaked forbidden field %s", secret)
		}
	}
	for _, tc := range []struct{ old, replacement string }{
		{`"1:t1:1:1"`, `"1:t1:1:2"`}, {`"tab_id":"t1"`, `"tab_id":"t2"`}, {`"ctx_1234567890abcdef12345678"`, `"ctx_aaaaaaaaaaaaaaaaaaaaaaaa"`},
		{`"x":10`, `"x":100`}, {`"width":1280`, `"width":10`}, {`"tag":"button"`, `"tag":""`}, {`"expires_in_ms":3000`, `"expires_in_ms":3001`}, {`"expires_in_ms":3000`, `"expires_in_ms":null`}, {nativePreviewTokenFixture, "pv_bad"},
		{`"role":"button"`, `"role":"` + strings.Repeat("字", 81) + `"`},
	} {
		if _, err := decodeNativeBrowserPickPreview([]byte(strings.Replace(nativePreviewFixture, tc.old, tc.replacement, 1)), input); err == nil {
			t.Fatalf("invalid preview accepted: %s", tc.old)
		}
	}
	var empty map[string]any
	_ = json.Unmarshal([]byte(nativePreviewFixture), &empty)
	empty["element"] = nil
	delete(empty, "preview_token")
	delete(empty, "expires_in_ms")
	raw, _ = json.Marshal(empty)
	if result, err := decodeNativeBrowserPickPreview(raw, input); err != nil || result.Element != nil || result.PreviewToken != "" {
		t.Fatal("empty hit invented authority")
	}
	empty["preview_token"] = nativePreviewTokenFixture
	raw, _ = json.Marshal(empty)
	if _, err := decodeNativeBrowserPickPreview(raw, input); err == nil {
		t.Fatal("empty hit carried a commit token")
	}
}

func TestNativeBrowserPickPreviewAuthorizationAndCommitTokenForwarding(t *testing.T) {
	var requests, previews, commits atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var input nativeBrowserPickInput
		_ = json.NewDecoder(r.Body).Decode(&input)
		switch r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:] {
		case "preview":
			previews.Add(1)
			if !validNativePickPreviewInput(input) {
				t.Error("invalid preview forwarded")
			}
			_, _ = io.WriteString(w, nativePreviewFixture)
		case "pick":
			commits.Add(1)
			var token string
			_ = json.Unmarshal(input.PreviewToken, &token)
			if token != nativePreviewTokenFixture {
				t.Error("commit token lost or changed")
			}
			_, _ = io.WriteString(w, nativePickFixture)
		default:
			t.Error("read attempted a context mutation")
		}
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.mgr.cfg.browser = cfg
	sid := h.createAs(aliceToken)
	base := "/api/sessions/" + sid + "/browser/pick"
	body := `{"context_ref":"` + nativeSheetsContext + `","x":12,"y":20,"version":"1:t1:1:1","tab_id":"t1"}`
	for _, tc := range []struct {
		token string
		want  int
	}{{"", 401}, {bobToken, 404}} {
		res := h.doAs(tc.token, "POST", base+"/preview", body)
		res.Body.Close()
		if res.StatusCode != tc.want {
			t.Fatal("preview ownership bypass")
		}
	}
	req, _ := http.NewRequest("POST", h.srv.URL+base+"/preview", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	req.Header.Set("Origin", "https://other.test")
	res, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("preview Origin bypass")
	}
	withToken := strings.TrimSuffix(body, "}") + `,"preview_token":"` + nativePreviewTokenFixture + `"}`
	for _, invalid := range []string{`{}`, `null`, withToken, strings.TrimSuffix(body, "}") + `,"lease_token":"secret"}`} {
		res := h.doAs(aliceToken, "POST", base+"/preview", invalid)
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatal("invalid preview accepted")
		}
	}
	for _, value := range []string{`null`, `""`, `"pv_bad"`, `42`, `[]`, `{}`} {
		res := h.doAs(aliceToken, "POST", base, strings.TrimSuffix(body, "}")+`,"preview_token":`+value+`}`)
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatal("malformed commit token downgraded to strict pick")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid requests reached worker")
	}
	res = h.doAs(aliceToken, "POST", base+"/preview", body)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || previews.Load() != 1 || strings.Contains(string(raw), "private-") || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("valid preview response failed")
	}
	res = h.doAs(aliceToken, "POST", base, withToken)
	raw, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || commits.Load() != 1 || strings.Contains(string(raw), "preview_token") {
		t.Fatal("token-bound strict commit failed or echoed private token")
	}
}

func TestNativeBrowserPickPreviewDoesNotSeizeQueueOrCancelOnTimeout(t *testing.T) {
	var sideEffects atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/pick/preview") {
			sideEffects.Add(1)
			return
		}
		_, _ = io.WriteString(w, nativePreviewFixture)
	})
	b := newNativeBrowser(cfg)
	if err := b.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer b.release()
	if _, err := b.PickPreview(context.Background(), nativePickInputFixture()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	slow := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/pick/preview") {
			sideEffects.Add(1)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	})
	observer := newNativeBrowser(slow)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := observer.PickPreview(ctx, nativePickInputFixture()); done <- err }()
	nativeWait(t, started)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("preview cancellation lost")
		}
	case <-time.After(time.Second):
		t.Fatal("preview did not cancel")
	}
	if sideEffects.Load() != 0 {
		t.Fatal("preview created/cancelled an execution context")
	}
	b.mu.Lock()
	b.blocked = "unknown action"
	b.mu.Unlock()
	if _, err := b.PickPreview(context.Background(), nativePickInputFixture()); err == nil {
		t.Fatal("preview bypassed unknown context fence")
	}
}

func TestNativeBrowserPickPendingStateAndErrorCodeRemainExplicit(t *testing.T) {
	for _, value := range []string{"true", "false"} {
		state, err := nativeBrowserState([]byte(`{"pick_pending":`+value+`}`), false)
		if err != nil || string(state["pick_pending"]) != value {
			t.Fatal("pick pending boolean lost")
		}
	}
	for _, value := range []string{`"true"`, `null`, `{}`, `1`} {
		state, err := nativeBrowserState([]byte(`{"pick_pending":`+value+`}`), false)
		if err != nil || state["pick_pending"] != nil {
			t.Fatal("invalid pick pending assertion accepted")
		}
	}
	for _, status := range []int{409, 504} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var sideEffects atomic.Int32
			cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/pick/preview") && !strings.HasSuffix(r.URL.Path, "/pick") {
					sideEffects.Add(1)
					return
				}
				writeJSON(w, status, map[string]string{"error": "observation still pending", "code": "PICK_OBSERVATION_PENDING"})
			})
			h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
			h.mgr.cfg.browser = cfg
			sid := h.createAs(aliceToken)
			input, _ := json.Marshal(nativePickInputFixture())
			for _, suffix := range []string{"/pick", "/pick/preview"} {
				res := h.doAs(aliceToken, "POST", "/api/sessions/"+sid+"/browser"+suffix, string(input))
				var body map[string]any
				_ = json.NewDecoder(res.Body).Decode(&body)
				res.Body.Close()
				if res.StatusCode != status || body["code"] != "PICK_OBSERVATION_PENDING" {
					t.Fatalf("pending read status/code lost: %d %+v", res.StatusCode, body)
				}
			}
			b := h.session(sid).nativeBrowser
			b.mu.Lock()
			blocked := b.blocked
			cleanup := b.cleanupRequired
			b.mu.Unlock()
			if sideEffects.Load() != 0 || blocked != "" || cleanup {
				t.Fatal("read-only pending result cancelled or fenced execution context")
			}
		})
	}
}
