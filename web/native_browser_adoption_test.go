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

func TestNativeBrowserUnownedAdoptionValidatesFreshHumanBindingBeforeDispatch(t *testing.T) {
	valid := `{"mode":"human","context_ref":"` + nativeSheetsContext + `","tab_id":"t2","adopt_if_unowned":true,"generation":4,"control_epoch":0}`
	var requests atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/control") {
			t.Error("unowned adoption created, reset, read or cancelled the context")
		}
		var sent map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&sent) != nil || len(sent) != 6 || string(sent["adopt_if_unowned"]) != "true" || string(sent["generation"]) != "4" || string(sent["control_epoch"]) != "0" || string(sent["tab_id"]) != `"t2"` || string(sent["context_ref"]) != `"`+nativeSheetsContext+`"` || sent["lease_token"] != nil || sent["force"] != nil {
			t.Error("conditional adoption altered the observed fences or added force/lease")
		}
		state := nativeSheetsState("t2")
		state["control"], state["control_epoch"], state["generation"], state["frame_identity"] = "human", 1, 4, "4:t2:1"
		state["lease_token"] = "adopted-only-this-page"
		writeJSON(w, 200, state)
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.mgr.cfg.browser = cfg
	sid := h.createAs(aliceToken)
	route := "/api/sessions/" + sid + "/browser/control"
	for _, tc := range []struct {
		token string
		code  int
	}{{"", 401}, {bobToken, 404}} {
		res := h.doAs(tc.token, "POST", route, valid)
		res.Body.Close()
		if res.StatusCode != tc.code {
			t.Fatalf("another account could adopt a page: %d", res.StatusCode)
		}
	}
	for _, bad := range []string{
		strings.Replace(valid, `"adopt_if_unowned":true`, `"adopt_if_unowned":false`, 1),
		strings.Replace(valid, `"adopt_if_unowned":true`, `"adopt_if_unowned":null`, 1),
		strings.Replace(valid, `"adopt_if_unowned":true`, `"adopt_if_unowned":"true"`, 1),
		strings.Replace(valid, `"adopt_if_unowned":true`, `"adopt_if_unowned":1`, 1),
		strings.Replace(valid, `,"adopt_if_unowned":true`, "", 1),
		strings.Replace(valid, `,"generation":4`, "", 1),
		strings.Replace(valid, `,"control_epoch":0`, "", 1),
		strings.Replace(valid, `"generation":4`, `"generation":null`, 1),
		strings.Replace(valid, `"generation":4`, `"generation":0`, 1),
		strings.Replace(valid, `"generation":4`, `"generation":-1`, 1),
		strings.Replace(valid, `"generation":4`, `"generation":4.5`, 1),
		strings.Replace(valid, `"generation":4`, `"generation":9007199254740992`, 1),
		strings.Replace(valid, `"control_epoch":0`, `"control_epoch":null`, 1),
		strings.Replace(valid, `"control_epoch":0`, `"control_epoch":1`, 1),
		strings.Replace(valid, `"control_epoch":0`, `"control_epoch":-1`, 1),
		strings.Replace(valid, `"control_epoch":0`, `"control_epoch":"0"`, 1),
		strings.Replace(valid, `"mode":"human"`, `"mode":"agent"`, 1),
		strings.Replace(valid, `"control_epoch":0`, `"control_epoch":0,"force":true`, 1),
		strings.Replace(valid, `"control_epoch":0`, `"control_epoch":0,"lease_token":"existing-owner-lease"`, 1),
		strings.Replace(valid, `"tab_id":"t2"`, `"tab_id":"../t2"`, 1),
		strings.Replace(valid, nativeSheetsContext, "private-worker-id", 1),
		strings.Replace(valid, `"control_epoch":0`, `"control_epoch":0,"url":"https://other.test"`, 1),
	} {
		res := h.doAs(aliceToken, "POST", route, bad)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("invalid conditional adoption accepted: HTTP%d", res.StatusCode)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid or foreign adoption reached the worker")
	}
	res := h.doAs(aliceToken, "POST", route, valid)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var state map[string]json.RawMessage
	_ = json.Unmarshal(raw, &state)
	if res.StatusCode != 200 || requests.Load() != 1 || string(state["lease_token"]) != `"adopted-only-this-page"` || string(state["control_epoch"]) != "1" || string(state["generation"]) != "4" || string(state["viewed_tab"]) != `"t2"` {
		t.Fatal("exact conditional adoption was not acknowledged")
	}
}

func TestNativeBrowserUnownedAdoptionRejectsRacedOrUnboundLeaseReplies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"other viewed page", func(s map[string]any) { s["viewed_tab"] = "t1" }},
		{"another context", func(s map[string]any) { s["context_ref"] = "ctx_999999999999999999999999" }},
		{"foreign frame", func(s map[string]any) { s["frame_identity"] = "4:t1:1" }},
		{"generation changed", func(s map[string]any) { s["generation"] = 5 }},
		{"generation missing", func(s map[string]any) { delete(s, "generation") }},
		{"generation null", func(s map[string]any) { s["generation"] = nil }},
		{"not human anymore", func(s map[string]any) { s["control"] = "agent" }},
		{"epoch unchanged", func(s map[string]any) { s["control_epoch"] = 0 }},
		{"later owner", func(s map[string]any) { s["control_epoch"] = 2 }},
		{"epoch missing", func(s map[string]any) { delete(s, "control_epoch") }},
		{"epoch null", func(s map[string]any) { s["control_epoch"] = nil }},
		{"lease missing", func(s map[string]any) { delete(s, "lease_token") }},
		{"lease whitespace", func(s map[string]any) { s["lease_token"] = "invalid lease" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			state := nativeSheetsState("t2")
			state["control"], state["control_epoch"], state["generation"], state["frame_identity"], state["lease_token"] = "human", 1, 4, "4:t2:1", "new-bound-lease"
			tc.change(state)
			cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/control") {
					t.Error("adoption retried or issued an unrelated reset/read")
				}
				writeJSON(w, 200, state)
			})
			generation, epoch := int64(4), int64(0)
			result, err := newNativeBrowser(cfg).Control(context.Background(), nativeBrowserControl{Mode: "human", TabID: "t2", ContextRef: nativeSheetsContext, AdoptIfUnowned: true, Generation: &generation, ControlEpoch: &epoch})
			if err == nil || result != nil || calls.Load() != 1 {
				t.Fatal("raced/foreign lease granted input authority or triggered fallback")
			}
		})
	}
}

func TestNativeBrowserUnownedAdoptionDefiniteRefusalDoesNotRetryOrCancel(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusGone} {
		var calls atomic.Int32
		cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/control") {
				t.Error("adoption forced a reset or cancellation")
			}
			writeErr(w, status, errors.New("observed unowned page binding changed"))
		})
		generation, epoch := int64(4), int64(0)
		browser := newNativeBrowser(cfg)
		// Safe human adoption does not wait for an independent queued Agent op.
		if err := browser.acquire(context.Background()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		done := make(chan error, 1)
		go func() {
			_, err := browser.Control(ctx, nativeBrowserControl{Mode: "human", TabID: "t2", ContextRef: nativeSheetsContext, AdoptIfUnowned: true, Generation: &generation, ControlEpoch: &epoch})
			done <- err
		}()
		err := <-done
		cancel()
		browser.release()
		var remote *nativeBrowserHTTPError
		if !errors.As(err, &remote) || remote.status != status || calls.Load() != 1 {
			t.Fatal("definite refusal changed ownership or hid its status")
		}
	}
}
