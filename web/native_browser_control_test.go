package web

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/yosukeno/pogo/tools"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeBrowserControlRequiresExplicitPageAndMatchingResponse(t *testing.T) {
	var calls atomic.Int32
	response := atomic.Value{}
	response.Store("t2")
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/control") {
			t.Error("control implicitly created a context")
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		var in nativeBrowserControl
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.TabID != "t1" || in.ContextRef != nativeSheetsContext {
			t.Error("bound control target lost")
		}
		state := nativeSheetsState(response.Load().(string))
		state["control"] = "human"
		state["lease_token"] = "lease-t1"
		writeJSON(w, 200, state)
	})
	b := newNativeBrowser(cfg)
	for _, in := range []nativeBrowserControl{{Mode: "human"}, {Mode: "human", ContextRef: nativeSheetsContext}, {Mode: "human", ContextRef: nativeSheetsContext, TabID: "../t1"}, {Mode: "agent", ContextRef: nativeSheetsContext, TabID: "t1"}} {
		if _, err := b.Control(context.Background(), in); err == nil {
			t.Fatal("unbound or invalid control accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid control reached worker")
	}
	in := nativeBrowserControl{Mode: "human", ContextRef: nativeSheetsContext, TabID: "t1"}
	if state, err := b.Control(context.Background(), in); err == nil || state != nil {
		t.Fatal("another tab's control response acknowledged")
	}
	response.Store("t1")
	state, err := b.Control(context.Background(), in)
	if err != nil || string(state["viewed_tab"]) != `"t1"` || string(state["lease_token"]) != `"lease-t1"` {
		t.Fatal("matching tab control failed")
	}
}

func TestNativeBrowserTabTakeoverDoesNotBlockIndependentAgentOperation(t *testing.T) {
	started, allowFinish := make(chan struct{}), make(chan struct{})
	var cancels atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/execute"):
			close(started)
			<-allowFinish
			writeJSON(w, 200, tools.BrowserReply{OK: true, ActiveTab: "t2"})
		case strings.HasSuffix(r.URL.Path, "/control"):
			var in nativeBrowserControl
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.TabID != "t1" {
				t.Error("takeover target changed")
			}
			state := nativeSheetsState("t1")
			state["active_tab"] = "t2"
			state["control"] = "human"
			state["lease_token"] = "lease-t1"
			writeJSON(w, 200, state)
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			cancels.Add(1)
			writeJSON(w, 200, map[string]bool{"ok": true})
		default:
			writeJSON(w, 200, map[string]bool{"ok": true})
		}
	})
	b := newNativeBrowser(cfg)
	b.created = true
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		reply, err := b.Execute(ctx, json.RawMessage(`{"action":"snapshot"}`))
		if err == nil && (!reply.OK || reply.ActiveTab != "t2") {
			err = errors.New("Agent page changed")
		}
		finished <- err
	}()
	nativeWait(t, started)
	state, err := b.Control(ctx, nativeBrowserControl{Mode: "human", ContextRef: nativeSheetsContext, TabID: "t1"})
	close(allowFinish)
	if err != nil || string(state["active_tab"]) != `"t2"` {
		t.Fatalf("independent takeover waited or switched Agent target: %v", err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if cancels.Load() != 0 {
		t.Fatal("independent Agent operation globally cancelled")
	}
}

func TestNativeBrowserTabTakeoverCannotClearUncertainContextFence(t *testing.T) {
	var requests atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	b := newNativeBrowser(cfg)
	b.blocked = "context cancellation is unconfirmed; blocked"
	for _, mode := range []string{"human", "agent"} {
		_, err := b.Control(context.Background(), nativeBrowserControl{Mode: mode, ContextRef: nativeSheetsContext, TabID: "t2", LeaseToken: "lease", Force: mode == "human"})
		if err == nil || !strings.Contains(err.Error(), "blocked") {
			t.Fatal("tab control escaped unconfirmed context cancellation")
		}
	}
	if requests.Load() != 0 || b.blocked == "" {
		t.Fatal("partial takeover cleared full context fence")
	}
}

func TestNativeBrowserStateTabsExposeOnlyPublicOwnershipMetadata(t *testing.T) {
	raw := []byte(`{"context_ref":"ctx_1234567890abcdef12345678","viewed_tab":"t1","control":"human","control_epoch":2,"lease_token":"hidden-top-lease","tabs":[{"id":"t1","url":"https://one.test","title":"One","control":"human","control_epoch":2,"lease_token":"hidden-tab-lease","worker_id":"private-worker","target_ref":"private-target"},{"id":"t2","control":"agent","control_epoch":0},{"id":"t3","control":"unknown","control_epoch":-1},{"id":"../private","lease_token":"hidden-invalid"},{"id":"t1","title":"duplicate"}]}`)
	state, err := nativeBrowserState(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(state)
	if strings.Contains(string(encoded), "hidden") || strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "duplicate") {
		t.Fatal("tab list leaked private data or duplicate identities")
	}
	var rows []map[string]any
	if json.Unmarshal(state["tabs"], &rows) != nil || len(rows) != 3 {
		t.Fatal("tab list shape changed")
	}
	if rows[0]["control"] != "human" || rows[0]["control_epoch"] != float64(2) || rows[1]["control"] != "agent" || rows[1]["control_epoch"] != float64(0) {
		t.Fatal("per-page control metadata missing")
	}
	if _, ok := rows[2]["control"]; ok {
		t.Fatal("unknown control grants authority")
	}
	if _, ok := rows[2]["control_epoch"]; ok {
		t.Fatal("invalid epoch accepted")
	}
}
