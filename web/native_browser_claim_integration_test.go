package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// Opt-in: the model-facing Go tool, authenticated viewer API and real Chromium
// worker share the same session. Fixtures use no external website or account.
func TestNativeBrowserRealClaimAndHumanSheets(t *testing.T) {
	endpoint := os.Getenv("POGO_NATIVE_BROWSER_TEST_URL")
	if endpoint == "" {
		t.Skip("set POGO_NATIVE_BROWSER_TEST_URL and POGO_NATIVE_BROWSER_TEST_TOKEN for real Chromium integration")
	}
	var authorizationReads, saved atomic.Int32
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/save" {
			if cookie, err := r.Cookie("fixture_login"); err != nil || cookie.Value != "signed-in" {
				t.Error("claimed page lost the shared browser login cookie")
			}
			saved.Add(1)
			fmt.Fprint(w, "saved once")
			return
		}
		if r.URL.Path == "/parent" {
			http.SetCookie(w, &http.Cookie{Name: "fixture_login", Value: "signed-in", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		}
		if strings.HasPrefix(r.URL.Path, "/authorization") {
			authorizationReads.Add(1)
		}
		status := "guest"
		if cookie, err := r.Cookie("fixture_login"); err == nil {
			status = cookie.Value
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		fmt.Fprintf(w, `<!doctype html><title>%s</title><h1>Fixture %s</h1>
<button id="popup" style="position:absolute;left:20px;top:20px;width:240px;height:60px" onclick="window.open('/authorization')">Authorization popup</button>
<input id="draft" aria-label="Draft" autofocus style="position:absolute;left:20px;top:110px;width:240px;height:48px">
<button id="save" style="position:absolute;left:20px;top:190px" onclick="fetch('/save').then(()=>document.querySelector('#result').textContent='saved once')">Save once</button><p id="result"></p>`, r.URL.Path, status)
	}))
	defer site.Close()
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() }, func(c *Config) {
		c.BrowserURL, c.BrowserToken = endpoint, os.Getenv("POGO_NATIVE_BROWSER_TEST_TOKEN")
	})
	sid := h.createSession()
	sess := h.session(sid)
	sess.Browser().Arm()
	browser := &tools.Browser{Link: sess.Browser(), ImageMode: tools.BrowserImageNone, SpillDir: t.TempDir()}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	execute := func(args any) tools.Result {
		t.Helper()
		raw, _ := json.Marshal(args)
		result, err := browser.Execute(ctx, raw)
		if err != nil || result.Details.(map[string]any)["ok"] != true {
			t.Fatalf("model-facing browser operation failed: result=%s err=%v", result.Text, err)
		}
		return result
	}
	base := "/api/sessions/" + sid + "/browser"
	post := func(path string, body any, wantStatus int) map[string]json.RawMessage {
		t.Helper()
		raw, _ := json.Marshal(body)
		response := h.do("POST", base+path, string(raw))
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != wantStatus {
			t.Fatalf("viewer %s status=%d want=%d", path, response.StatusCode, wantStatus)
		}
		var result map[string]json.RawMessage
		if json.Unmarshal(data, &result) != nil {
			t.Fatal("invalid viewer response")
		}
		return result
	}
	type tabRow struct {
		ID           string `json:"id"`
		URL          string `json:"url"`
		ContextRef   string `json:"context_ref"`
		Control      string `json:"control"`
		ControlEpoch int64  `json:"control_epoch"`
	}
	tabs := func() []tabRow {
		t.Helper()
		result := execute(map[string]string{"action": "tabs"})
		at := strings.Index(result.Text, "\n[")
		var rows []tabRow
		if at < 0 || json.Unmarshal([]byte(result.Text[at+1:]), &rows) != nil {
			t.Fatalf("model cannot read current tabs binding: %s", result.Text)
		}
		return rows
	}
	initial := execute(map[string]string{"action": "navigate", "url": site.URL + "/parent"}).Details.(map[string]any)
	parentID := initial["active_tab"].(string)
	observed := tabs()[0]
	if observed.ID != parentID || observed.ContextRef == "" || observed.Control != "agent" {
		t.Fatal("initial Agent page was not observed with its current context")
	}
	human := post("/control", map[string]string{"mode": "human", "context_ref": observed.ContextRef, "tab_id": parentID}, 200)
	var lease string
	var generation, parentEpoch int64
	_ = json.Unmarshal(human["lease_token"], &lease)
	_ = json.Unmarshal(human["generation"], &generation)
	_ = json.Unmarshal(human["control_epoch"], &parentEpoch)
	if lease == "" || generation < 1 {
		t.Fatal("human takeover did not return a current lease")
	}
	agentID := execute(map[string]string{"action": "tab", "tab_action": "new", "url": site.URL + "/agent"}).Details.(map[string]any)["active_tab"].(string)
	input := func(tab string, body map[string]any, wantStatus int) {
		t.Helper()
		body["lease_token"], body["context_ref"], body["target_tab"], body["generation"] = lease, observed.ContextRef, tab, generation
		if body["type"] == "click" {
			query := url.Values{"tab_id": {tab}, "context_ref": {observed.ContextRef}}.Encode()
			frame := h.do("GET", base+"/frame?"+query, "")
			_, _ = io.Copy(io.Discard, frame.Body)
			frame.Body.Close()
			body["version"] = frame.Header.Get("X-Browser-Version")
			if frame.StatusCode != 200 || body["version"] == "" {
				t.Fatal("human coordinate input needs a fresh bound frame")
			}
		}
		post("/input", body, wantStatus)
	}
	// This is human input through the actual viewer API, not Agent/CDP script
	// creation. A functional window.open popup remains a real additional Page.
	input(parentID, map[string]any{"type": "click", "x": 40, "y": 40}, 200)
	var popup tabRow
	waitFor(t, 5*time.Second, func() bool {
		for _, row := range tabs() {
			if row.ID != parentID && row.ID != agentID && row.URL == site.URL+"/authorization" {
				popup = row
				return row.Control == "human"
			}
		}
		return false
	})
	if popup.ContextRef != observed.ContextRef || popup.ControlEpoch != parentEpoch {
		t.Fatal("human popup changed its inherited control binding")
	}
	// Create a genuine second history entry and human draft before claiming.
	input(popup.ID, map[string]any{"type": "navigate", "url": site.URL + "/authorization/step2"}, 200)
	input(popup.ID, map[string]any{"type": "click", "x": 40, "y": 130}, 200)
	input(popup.ID, map[string]any{"type": "text", "text": "human-saved-draft"}, 200)
	for _, row := range tabs() {
		if row.ID == popup.ID {
			popup = row
		}
	}
	claim := map[string]any{"action": "tab", "tab_action": "claim", "target": popup.ID, "context_ref": popup.ContextRef, "control_epoch": popup.ControlEpoch}
	for _, wrong := range []map[string]any{
		{"action": "tab", "tab_action": "claim", "target": popup.ID, "context_ref": "ctx_000000000000000000000000", "control_epoch": popup.ControlEpoch},
		{"action": "tab", "tab_action": "claim", "target": popup.ID, "context_ref": popup.ContextRef, "control_epoch": popup.ControlEpoch + 1},
	} {
		raw, _ := json.Marshal(wrong)
		result, err := browser.Execute(ctx, raw)
		if err != nil || result.Details.(map[string]any)["ok"] != false || result.Details.(map[string]any)["control"] != nil {
			t.Fatalf("stale claim granted control: result=%s err=%v", result.Text, err)
		}
	}
	claimed := execute(claim)
	details := claimed.Details.(map[string]any)
	if details["tab_action"] != "claim" || details["active_tab"] != popup.ID || details["context_ref"] != popup.ContextRef || details["control"] != "agent" || details["control_epoch"] != popup.ControlEpoch+1 {
		t.Fatalf("real Go claim lost its guarded UI receipt: %+v", details)
	}
	if details["url"] != site.URL+"/authorization/step2" || !strings.Contains(claimed.Text, "signed-in") || authorizationReads.Load() != 2 {
		t.Fatal("claim rebuilt or navigated the human page, or lost its login state")
	}
	retained := execute(map[string]any{"action": "devtools", "method": "Runtime.evaluate", "params": map[string]any{"expression": `({draft:document.querySelector('#draft').value,hasOpener:window.opener!==null,historyLength:history.length})`, "returnByValue": true}})
	if !strings.Contains(retained.Text, "human-saved-draft") || !strings.Contains(retained.Text, `"hasOpener":true`) || !strings.Contains(retained.Text, `"historyLength":2`) {
		t.Fatalf("claim discarded draft, opener or history: %s", retained.Text)
	}
	input(popup.ID, map[string]any{"type": "text", "text": "stale human input"}, http.StatusForbidden)
	// A sibling's same inherited lease remains valid; claim affects only popup.
	input(parentID, map[string]any{"type": "click", "x": 40, "y": 130}, 200)
	input(parentID, map[string]any{"type": "text", "text": "sibling remains human"}, 200)
	execute(map[string]string{"action": "click", "target": "#save"})
	execute(map[string]any{"action": "wait", "text": "saved once", "seconds": 5})
	if saved.Load() != 1 {
		t.Fatalf("claim task replayed a write: saved=%d", saved.Load())
	}
	rows := tabs()
	if len(rows) != 3 {
		t.Fatal("claim created another Sheet")
	}
	for _, row := range rows {
		if row.ContextRef != popup.ContextRef || row.ID == parentID && (row.Control != "human" || row.ControlEpoch != parentEpoch || row.URL != site.URL+"/parent") || row.ID == agentID && row.Control != "agent" || row.ID == popup.ID && (row.Control != "agent" || row.ControlEpoch != popup.ControlEpoch+1) {
			t.Fatal("claim changed unrelated ownership or page binding")
		}
	}
	// The human + endpoint carries only its own new Page lease, never changes
	// the Agent target, and strips authority from ordinary state/tab metadata.
	createdRaw := post("/tabs", map[string]string{"action": "new", "op_id": "human-claim-fixture-new", "context_ref": popup.ContextRef}, 200)
	var created nativeBrowserTabsResult
	raw, _ := json.Marshal(createdRaw)
	if json.Unmarshal(raw, &created) != nil || created.LeaseToken == "" || created.LeaseToken == lease || string(created.State["control"]) != `"human"` || string(created.State["active_tab"]) != `"`+popup.ID+`"` || created.State["lease_token"] != nil {
		t.Fatal("human + did not produce an independently bound human page")
	}
	query := url.Values{"tab_id": {created.TabID}, "context_ref": {created.ContextRef}}.Encode()
	response := h.do("GET", base+"?"+query, "")
	public, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || strings.Contains(string(public), created.LeaseToken) || strings.Contains(string(public), lease) {
		t.Fatal("ordinary state leaked a human lease")
	}
	t.Log("PASS: model tool -> gate -> Go bridge -> real Chromium; human popup inherits human control; tabs expose fresh context/epoch without lease; stale claim refused; guarded claim receipt reaches UI; same URL, login, draft, opener and history preserved; old input denied, sibling still human; one Agent save; human + independently leased and background")
}
