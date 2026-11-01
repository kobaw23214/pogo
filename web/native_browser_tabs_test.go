package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const nativeSheetsContext = "ctx_1234567890abcdef12345678"

func nativeSheetsInput(raw string) string {
	var body map[string]any
	_ = json.Unmarshal([]byte(raw), &body)
	body["context_ref"], body["target_tab"], body["generation"] = nativeSheetsContext, "t1", 1
	data, _ := json.Marshal(body)
	return string(data)
}
func nativeSheetsState(tab string) map[string]any {
	return map[string]any{"context_ref": nativeSheetsContext, "viewed_tab": tab, "active_tab": "t1", "control": "agent", "generation": 1, "frame_identity": "1:" + tab + ":1", "url": "https://" + tab + ".test/", "lease_token": "private-lease", "worker_id": "private-worker"}
}
func TestNativeBrowserBoundViewsNeverRecoverOrChangeAgent(t *testing.T) {
	var mutation, states atomic.Int32
	status := atomic.Int32{}
	status.Store(200)
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mutation.Add(1)
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		if r.URL.Query().Get("tab_id") != "t2" || r.URL.Query().Get("context_ref") != nativeSheetsContext {
			t.Error("binding was lost")
		}
		if status.Load() != 200 {
			writeErr(w, int(status.Load()), errors.New("missing binding"))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/frame") {
			w.Header().Set("X-Browser-Frame-Identity", "1:t2:1")
			w.Header().Set("X-Browser-Context-Ref", nativeSheetsContext)
			w.Header().Set("X-Browser-Viewed-Tab", "t2")
			_, _ = w.Write([]byte{0xff, 0xd8, 0xff, 0})
			return
		}
		states.Add(1)
		writeJSON(w, 200, nativeSheetsState("t2"))
	})
	b := newNativeBrowser(cfg)
	target := nativeBrowserTarget{TabID: "t2", ContextRef: nativeSheetsContext}
	state, err := b.State(context.Background(), target)
	if err != nil || string(state["active_tab"]) != `"t1"` || string(state["viewed_tab"]) != `"t2"` {
		t.Fatalf("selected view lost separation: %v %v", state, err)
	}
	frame, err := b.Frame(context.Background(), target)
	if err != nil || frame.ViewedTab != "t2" || frame.ContextRef != nativeSheetsContext {
		t.Fatalf("selected frame mismatch: %+v %v", frame, err)
	}
	for _, code := range []int32{404, 409, 410} {
		status.Store(code)
		for _, read := range []func() error{func() error { _, e := b.State(context.Background(), target); return e }, func() error { _, e := b.Frame(context.Background(), target); return e }} {
			var remote *nativeBrowserHTTPError
			if err := read(); !errors.As(err, &remote) || remote.status != int(code) {
				t.Fatalf("binding failure hidden: %v", err)
			}
		}
	}
	if mutation.Load() != 0 || states.Load() != 1 {
		t.Fatalf("bound read recreated/cancelled/switched context: %d", mutation.Load())
	}
}
func TestNativeBrowserBoundFramesRejectForeignPixels(t *testing.T) {
	for _, wrong := range []string{"context", "tab", "identity"} {
		t.Run(wrong, func(t *testing.T) {
			cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
				contextRef, tab, identity := nativeSheetsContext, "t2", "1:t2:1"
				switch wrong {
				case "context":
					contextRef = "ctx_aaaaaaaaaaaaaaaaaaaaaaaa"
				case "tab":
					tab = "t1"
				case "identity":
					identity = "1:t1:1"
				}
				w.Header().Set("X-Browser-Context-Ref", contextRef)
				w.Header().Set("X-Browser-Viewed-Tab", tab)
				w.Header().Set("X-Browser-Frame-Identity", identity)
				_, _ = w.Write([]byte{0xff, 0xd8, 0xff, 0})
			})
			if _, err := newNativeBrowser(cfg).Frame(context.Background(), nativeBrowserTarget{TabID: "t2", ContextRef: nativeSheetsContext}); err == nil {
				t.Fatal("foreign frame accepted")
			}
		})
	}
}
func TestNativeBrowserTabsAPIRequiresOwnershipBindingAndHumanClose(t *testing.T) {
	var requests, cancels atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/tabs") {
			cancels.Add(1)
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		var input nativeBrowserTabsInput
		_ = json.NewDecoder(r.Body).Decode(&input)
		if input.Action == "close" && input.LeaseToken != "human-lease" {
			writeErr(w, 403, errors.New("lease invalid"))
			return
		}
		state := nativeSheetsState("t2")
		result := map[string]any{"tab_id": "t2", "context_ref": nativeSheetsContext, "state": state, "private": "must-not-leak"}
		if input.Action == "new" {
			state["control"], state["control_epoch"] = "human", 1
			result["lease_token"] = "created-human-lease"
		}
		writeJSON(w, 200, result)
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.mgr.cfg.browser = cfg
	sid := h.createAs(aliceToken)
	path := "/api/sessions/" + sid + "/browser/tabs"
	body := `{"action":"new","op_id":"sheet-new-1","context_ref":"` + nativeSheetsContext + `"}`
	for _, tc := range []struct {
		token string
		code  int
	}{{"", 401}, {bobToken, 404}} {
		res := h.doAs(tc.token, "POST", path, body)
		res.Body.Close()
		if res.StatusCode != tc.code {
			t.Fatalf("ownership %d", res.StatusCode)
		}
	}
	request, _ := http.NewRequest("POST", h.srv.URL+path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+aliceToken)
	request.Header.Set("Origin", "https://other.test")
	res, err := h.srv.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("origin accepted")
	}
	for _, bad := range []string{`{}`, `{"action":"new","op_id":"x"}`, strings.Replace(body, `"sheet-new-1"`, `"../escape"`, 1), strings.Replace(body, `"action":"new"`, `"action":"new","url":"https://other.test"`, 1), strings.Replace(body, `"action":"new"`, `"action":"close","tab_id":"t2"`, 1), strings.Replace(body, `"action":"new"`, `"action":"new","lease_token":"secret"`, 1)} {
		res := h.doAs(aliceToken, "POST", path, bad)
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatalf("malformed request accepted %d %s", res.StatusCode, bad)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid/foreign request reached worker")
	}
	res = h.doAs(aliceToken, "POST", path, body)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || strings.Contains(string(raw), "private") || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid public result %d %s", res.StatusCode, raw)
	}
	var created nativeBrowserTabsResult
	if json.Unmarshal(raw, &created) != nil || created.LeaseToken != "created-human-lease" || created.State["lease_token"] != nil || string(created.State["control"]) != `"human"` {
		t.Fatalf("authenticated new page lost its exact human lease: %s", raw)
	}
	closeBody := `{"action":"close","op_id":"sheet-close-1","context_ref":"` + nativeSheetsContext + `","tab_id":"t2","lease_token":"wrong"}`
	res = h.doAs(aliceToken, "POST", path, closeBody)
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("worker lease refusal lost")
	}
	res = h.doAs(aliceToken, "POST", path, strings.Replace(closeBody, `"wrong"`, `"human-lease"`, 1))
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal("human close rejected")
	}
	if cancels.Load() != 0 {
		t.Fatal("tab management implicitly created context/cancelled browser")
	}
}

func TestNativeBrowserHumanNewTabLeaseRequiresExactCreatedPage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any, map[string]any)
		valid  bool
	}{
		{name: "exact human created page", valid: true},
		{name: "another viewed page", change: func(_ map[string]any, s map[string]any) { s["viewed_tab"] = "t1" }},
		{name: "another context", change: func(_ map[string]any, s map[string]any) { s["context_ref"] = "ctx_999999999999999999999999" }},
		{name: "different frame page", change: func(_ map[string]any, s map[string]any) { s["frame_identity"] = "1:t1:1" }},
		{name: "agent page cannot grant human lease", change: func(_ map[string]any, s map[string]any) { s["control"] = "agent" }},
		{name: "human missing lease", change: func(r map[string]any, _ map[string]any) { delete(r, "lease_token") }},
		{name: "lease whitespace", change: func(r map[string]any, _ map[string]any) { r["lease_token"] = "bad lease" }},
		{name: "epoch missing", change: func(_ map[string]any, s map[string]any) { delete(s, "control_epoch") }},
		{name: "epoch null", change: func(_ map[string]any, s map[string]any) { s["control_epoch"] = nil }},
		{name: "epoch negative", change: func(_ map[string]any, s map[string]any) { s["control_epoch"] = -1 }},
		{name: "epoch fractional", change: func(_ map[string]any, s map[string]any) { s["control_epoch"] = 1.5 }},
		{name: "epoch unsafe", change: func(_ map[string]any, s map[string]any) { s["control_epoch"] = 9007199254740992 }},
		{name: "generation missing", change: func(_ map[string]any, s map[string]any) { delete(s, "generation") }},
		{name: "generation zero", change: func(_ map[string]any, s map[string]any) { s["generation"] = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := nativeSheetsState("t2")
			state["control"], state["control_epoch"] = "human", 1
			response := map[string]any{"tab_id": "t2", "context_ref": nativeSheetsContext, "lease_token": "human-created-lease", "state": state}
			if tc.change != nil {
				tc.change(response, state)
			}
			cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/tabs") {
					t.Error("new tab lease performed unrelated control/reset")
				}
				writeJSON(w, 200, response)
			})
			result, err := newNativeBrowser(cfg).Tabs(context.Background(), nativeBrowserTabsInput{Action: "new", OpID: "human-new", ContextRef: nativeSheetsContext})
			if tc.valid {
				if err != nil || result.LeaseToken != "human-created-lease" || result.State["lease_token"] != nil {
					t.Fatalf("valid human page not returned: result=%+v err=%v", result, err)
				}
			} else if err == nil || result != nil {
				t.Fatalf("foreign or incomplete human lease accepted: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestNativeBrowserPopupPublicAncestryNeverExposesLeases(t *testing.T) {
	for _, tc := range []struct {
		opener string
		epoch  any
		valid  bool
	}{{"t1", 0, true}, {"t1", 3, true}, {"t2", 3, false}, {"../t1", 3, false}, {"t1", nil, false}, {"t1", -1, false}, {"t1", 1.5, false}, {"t1", 9007199254740992, false}} {
		raw, _ := json.Marshal([]any{map[string]any{"id": "t2", "url": "https://example.test/", "control": "human", "control_epoch": 3, "opener_tab": tc.opener, "inherited_control_epoch": tc.epoch, "lease_token": "private-human-lease", "worker_id": "private-worker", "context_ref": "private-context"}})
		public := nativeBrowserPublicTabs(raw)
		var rows []map[string]any
		if json.Unmarshal(public, &rows) != nil || len(rows) != 1 || strings.Contains(string(public), "private") || strings.Contains(string(public), "lease") {
			t.Fatalf("popup public state leaked authority: %s", public)
		}
		for _, field := range []string{"opener_tab", "inherited_control_epoch"} {
			if _, present := rows[0][field]; present != tc.valid {
				t.Fatalf("popup ancestry validity %s mismatch: %s", field, public)
			}
		}
	}
}
func TestNativeBrowserNewTabDoesNotWaitForGoAgentOrCancelOnTimeout(t *testing.T) {
	started := make(chan struct{})
	var cancelCalls atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tabs") {
			_, _ = io.Copy(io.Discard, r.Body)
			close(started)
			<-r.Context().Done()
			return
		}
		cancelCalls.Add(1)
	})
	b := newNativeBrowser(cfg)
	if err := b.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer b.release()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := b.Tabs(ctx, nativeBrowserTabsInput{Action: "new", OpID: "new-sheet", ContextRef: nativeSheetsContext})
		done <- err
	}()
	nativeWait(t, started)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("new page waited for Go execution lock")
	}
	if cancelCalls.Load() != 0 {
		t.Fatal("background page cancelled Agent")
	}
}
func TestNativeBrowserTargetQueriesAndInputsFailClosed(t *testing.T) {
	var requests atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeJSON(w, 200, nativeSheetsState("t2"))
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.mgr.cfg.browser = cfg
	sid := h.createAs(aliceToken)
	base := "/api/sessions/" + sid + "/browser"
	for _, suffix := range []string{"?tab_id=&context_ref=", "?tab_id=t2", "?context_ref=" + nativeSheetsContext, "?tab_id=t2&context_ref=invalid", "?tab_id=t2&tab_id=t1&context_ref=" + nativeSheetsContext} {
		for _, path := range []string{base, base + "/frame"} {
			res := h.doAs(aliceToken, "GET", path+suffix, "")
			res.Body.Close()
			if res.StatusCode != 400 {
				t.Fatalf("invalid target accepted: %d", res.StatusCode)
			}
		}
	}
	for _, body := range []string{`{"type":"key","key":"Enter","lease_token":"x"}`, `{"type":"key","key":"Enter","lease_token":"x","target_tab":"t2","context_ref":"` + nativeSheetsContext + `"}`, `{"type":"text","text":"hello","lease_token":"x","target_tab":"t2","context_ref":"` + nativeSheetsContext + `","generation":0}`} {
		res := h.doAs(aliceToken, "POST", base+"/input", body)
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatal("unbound mutation accepted")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid binding reached worker")
	}
}

func TestNativeBrowserSheetStateAllowlistAndErrorClassification(t *testing.T) {
	state, err := nativeBrowserState([]byte(`{"context_ref":"ctx_1234567890abcdef12345678","viewed_tab":"t2","worker_id":"private","lease_token":"secret"}`), false)
	if err != nil || string(state["context_ref"]) != `"ctx_1234567890abcdef12345678"` || string(state["viewed_tab"]) != `"t2"` || state["worker_id"] != nil || state["lease_token"] != nil {
		t.Fatal("sheet state public fields invalid")
	}
	state, err = nativeBrowserState([]byte(`{"context_ref":"private-worker-id","viewed_tab":"../private"}`), false)
	if err != nil || state["context_ref"] != nil || state["viewed_tab"] != nil {
		t.Fatal("unvalidated binding leaked")
	}
	for _, code := range []int{404, 409, 410, 429} {
		recorder := httptest.NewRecorder()
		writeNativeBrowserError(recorder, &nativeBrowserHTTPError{status: code, message: "binding unavailable"})
		if recorder.Code != code {
			t.Fatalf("binding refusal %d mapped to %d", code, recorder.Code)
		}
	}
}

func TestNativeBrowserControlBindingDoesNotRecreateOrCancelWrongContext(t *testing.T) {
	var requests, creates, modelCancels atomic.Int32
	status := atomic.Int32{}
	status.Store(409)
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method == http.MethodPut {
			creates.Add(1)
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		var input nativeBrowserControl
		_ = json.NewDecoder(r.Body).Decode(&input)
		if input.ContextRef != nativeSheetsContext || input.TabID != "t1" {
			t.Error("control binding lost")
		}
		if status.Load() != 200 {
			writeErr(w, int(status.Load()), errors.New("context changed"))
			return
		}
		state := nativeSheetsState("t1")
		state["lease_token"] = "human-lease"
		state["control"] = "human"
		writeJSON(w, 200, state)
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.mgr.cfg.browser = cfg
	sid := h.createAs(aliceToken)
	sess := h.session(sid)
	sess.mu.Lock()
	sess.compactCancel = func() { modelCancels.Add(1) }
	sess.mu.Unlock()
	path := "/api/sessions/" + sid + "/browser/control"
	for _, body := range []string{`{"mode":"human"}`, `{"mode":"human","context_ref":"` + nativeSheetsContext + `"}`, `{"mode":"human","context_ref":"` + nativeSheetsContext + `","tab_id":"../t1"}`} {
		res := h.doAs(aliceToken, "POST", path, body)
		res.Body.Close()
		if res.StatusCode != 400 || requests.Load() != 0 {
			t.Fatal("unbound control reached worker")
		}
	}
	body := `{"mode":"human","context_ref":"` + nativeSheetsContext + `","tab_id":"t1"}`
	for _, code := range []int32{404, 409, 410} {
		status.Store(code)
		res := h.doAs(aliceToken, "POST", path, body)
		res.Body.Close()
		if res.StatusCode != int(code) {
			t.Fatalf("stale context status %d", res.StatusCode)
		}
	}
	if creates.Load() != 0 || modelCancels.Load() != 0 {
		t.Fatal("stale context control recreated/cancelled another browser")
	}
	status.Store(200)
	res := h.doAs(aliceToken, "POST", path, body)
	res.Body.Close()
	if res.StatusCode != 200 || modelCancels.Load() != 0 {
		t.Fatal("tab takeover cancelled the model instead of letting it choose another tab")
	}
}

func TestNativeBrowserTabOperationCodesRemainExplicit(t *testing.T) {
	for _, code := range []string{"TAB_OPERATION_PENDING", "TAB_OPERATION_OUTCOME_UNKNOWN", "TAB_OPERATION_REJECTED", "TAB_OPERATION_ID_CONFLICT", "private-worker-data"} {
		t.Run(code, func(t *testing.T) {
			cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, 409, map[string]string{"error": "tab operation unavailable", "code": code})
			})
			_, err := newNativeBrowser(cfg).Tabs(context.Background(), nativeBrowserTabsInput{Action: "new", OpID: "same-op", ContextRef: nativeSheetsContext})
			res := httptest.NewRecorder()
			writeNativeBrowserError(res, err)
			var body map[string]any
			_ = json.Unmarshal(res.Body.Bytes(), &body)
			if code == "private-worker-data" {
				if body["code"] != nil {
					t.Fatal("private worker code leaked")
				}
			} else if body["code"] != code {
				t.Fatal("tab ambiguity/rejection code lost")
			}
		})
	}
}
