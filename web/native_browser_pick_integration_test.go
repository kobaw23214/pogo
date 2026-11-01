package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
)

func TestNativeBrowserRealPicker(t *testing.T) {
	endpoint := os.Getenv("POGO_NATIVE_BROWSER_TEST_URL")
	if endpoint == "" {
		t.Skip("set POGO_NATIVE_BROWSER_TEST_URL and POGO_NATIVE_BROWSER_TEST_TOKEN for real Chromium integration")
	}
	var mutations atomic.Int32
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/activated" || r.URL.Path == "/hovered" {
			mutations.Add(1)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		_, _ = io.WriteString(w, `<!doctype html><title>只读元素选取</title>
<form action="/activated" method="post"><button style="position:absolute;left:20px;top:20px;width:200px;height:50px" onmouseenter="fetch('/hovered')">提交订单</button></form>
<input aria-label="私密输入" type="password" value="never-export-password" style="position:absolute;left:20px;top:120px;width:200px;height:50px">
<input type="hidden" value="never-export-hidden">`)
	}))
	defer site.Close()
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() }, func(c *Config) {
		c.BrowserURL, c.BrowserToken = endpoint, os.Getenv("POGO_NATIVE_BROWSER_TEST_TOKEN")
	})
	sid := h.createSession()
	sess := h.session(sid)
	sess.Browser().Arm()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	navigate := func(url string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"action": "navigate", "url": url})
		res, err := sess.Browser().Execute(ctx, raw)
		if err != nil || !res.OK {
			t.Fatalf("fixture navigation: %+v %v", res, err)
		}
	}
	navigate(site.URL)
	base := "/api/sessions/" + sid + "/browser"
	frame := h.do("GET", base+"/frame", "")
	_, _ = io.Copy(io.Discard, frame.Body)
	frame.Body.Close()
	version := frame.Header.Get("X-Browser-Version")
	if frame.StatusCode != 200 || version == "" {
		t.Fatal("missing versioned screenshot")
	}
	var state struct {
		ActiveTab  string `json:"active_tab"`
		ContextRef string `json:"context_ref"`
		Control    string `json:"control"`
		Version    string `json:"version"`
	}
	res := h.do("GET", base, "")
	_ = json.NewDecoder(res.Body).Decode(&state)
	res.Body.Close()
	if state.Control != "agent" {
		t.Fatal("fixture must start without a human lease")
	}
	pick := func(y int) nativeBrowserPickResult {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"x": 40, "y": y, "version": version, "tab_id": state.ActiveTab, "context_ref": state.ContextRef})
		res := h.do("POST", base+"/pick", string(raw))
		data, _ := io.ReadAll(res.Body)
		res.Body.Close()
		var result nativeBrowserPickResult
		if res.StatusCode != 200 || json.Unmarshal(data, &result) != nil || result.Element == nil || result.Version != version || result.TabID != state.ActiveTab || strings.Contains(string(data), "never-export") {
			t.Fatalf("read-only pick failed: %d %s", res.StatusCode, data)
		}
		return result
	}
	first, repeat := pick(40), pick(40)
	if first.Element.Tag != "button" || first.Element.Name != "提交订单" || first.Element.Ref != repeat.Element.Ref {
		t.Fatal("repeated selection must describe the same button")
	}
	password := pick(140)
	if password.Element.Tag != "input" || password.Element.Text != "" {
		t.Fatal("password selection must describe the control without its value")
	}
	res = h.do("GET", base, "")
	_ = json.NewDecoder(res.Body).Decode(&state)
	res.Body.Close()
	if mutations.Load() != 0 || state.Control != "agent" || state.Version != version {
		t.Fatal("selection clicked, hovered, stole control or changed the page")
	}
	navigate(site.URL + "/changed")
	raw, _ := json.Marshal(map[string]any{"x": 40, "y": 40, "version": version, "tab_id": state.ActiveTab, "context_ref": state.ContextRef})
	h.post(base+"/pick", string(raw), http.StatusConflict)
	t.Log("PASS: authenticated read-only element selection; no human lease; stable observation reference; password excluded; no hover/click/control change; stale frame rejected")
}
