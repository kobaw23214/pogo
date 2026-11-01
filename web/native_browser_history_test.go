package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestNativeBrowserBackRequiresBoundHumanInputAndPreservesHistoryState(t *testing.T) {
	var received []nativeBrowserInput
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/input") || r.Method != http.MethodPost {
			t.Errorf("unexpected worker request %s %s", r.Method, r.URL.Path)
		}
		var input nativeBrowserInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		received = append(received, input)
		state := nativeSheetsState("t2")
		state["can_go_back"] = true
		writeJSON(w, 200, state)
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.mgr.cfg.browser = cfg
	sid := h.createAs(aliceToken)
	path := "/api/sessions/" + sid + "/browser/input"
	for _, body := range []string{`{"type":"back"}`, `{"type":"back","lease_token":"lease","target_tab":"t2","context_ref":"` + nativeSheetsContext + `","generation":0}`} {
		res := h.doAs(aliceToken, http.MethodPost, path, body)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("unbound back accepted: %d", res.StatusCode)
		}
	}
	if len(received) != 0 {
		t.Fatal("unbound history mutation reached the worker")
	}
	body := `{"type":"back","lease_token":"lease","target_tab":"t2","context_ref":"` + nativeSheetsContext + `","generation":1}`
	res := h.doAs(aliceToken, http.MethodPost, path, body)
	defer res.Body.Close()
	var state map[string]json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || len(received) != 1 || received[0].Type != "back" || received[0].TargetTab != "t2" || received[0].ContextRef != nativeSheetsContext || received[0].LeaseToken != "lease" || received[0].Generation != 1 || string(state["can_go_back"]) != "true" {
		t.Fatalf("bound back contract changed: status=%d received=%#v", res.StatusCode, received)
	}
}

func TestNativeBrowserHistoryStateOnlyExposesBooleans(t *testing.T) {
	for _, raw := range []string{"true", "false", `"private"`, "null", "1"} {
		state, err := nativeBrowserState([]byte(`{"can_go_back":`+raw+`}`), false)
		if err != nil {
			t.Fatal(err)
		}
		if raw == "true" || raw == "false" {
			if string(state["can_go_back"]) != raw {
				t.Fatal("observed history boundary lost")
			}
		} else if state["can_go_back"] != nil {
			t.Fatal("invalid history metadata exposed")
		}
	}
}
