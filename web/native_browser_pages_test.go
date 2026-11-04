package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yosukeno/pogo/tools"
)

func TestNativeBrowserPagesRequireWorkerCapabilitiesBeforeDispatch(t *testing.T) {
	for _, action := range []string{
		`{"action":"open_pages","urls":["https://fixture.invalid/"]}`,
		`{"action":"observe_pages","pages":[{"tab_id":"t2","context_ref":"` + focusTestContext + `","control_epoch":0}]}`,
		`{"action":"snapshot","tab_id":"t2","context_ref":"` + focusTestContext + `","control_epoch":0}`,
	} {
		t.Run(action, func(t *testing.T) {
			var reads, dispatches, uploads atomic.Int64
			cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/state"):
					reads.Add(1)
					_, _ = w.Write([]byte(`{"context_ref":"` + focusTestContext + `","generation":1}`))
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/execute"):
					dispatches.Add(1)
					_, _ = w.Write([]byte(`{"ok":true}`))
				case r.Method == http.MethodPut:
					_, _ = w.Write([]byte(`{"ok":true}`))
				default:
					uploads.Add(1)
					w.WriteHeader(http.StatusBadRequest)
				}
			})
			_, err := newNativeBrowser(cfg).Execute(context.Background(), json.RawMessage(action))
			if err == nil || !strings.Contains(err.Error(), "does not support") || reads.Load() != 1 || dispatches.Load() != 0 || uploads.Load() != 0 {
				t.Fatalf("old worker silently accepted new contract: err=%v reads=%d dispatches=%d unexpected=%d", err, reads.Load(), dispatches.Load(), uploads.Load())
			}
		})
	}
}

func TestNativeBrowserExplicitPageBindingDispatchesOnceWithoutClaimAuthority(t *testing.T) {
	var dispatches atomic.Int64
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/state"):
			_, _ = w.Write([]byte(`{"context_ref":"` + focusTestContext + `","generation":1,"agent_capabilities":{"explicit_page_binding":true}}`))
		case r.Method == http.MethodPut:
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/execute"):
			dispatches.Add(1)
			var sent map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Error(err)
			}
			if len(sent) != 5 || string(sent["action"]) != `"snapshot"` || string(sent["tab_id"]) != `"t2"` || string(sent["context_ref"]) != `"`+focusTestContext+`"` || string(sent["control_epoch"]) != "0" || sent["op_id"] == nil {
				t.Errorf("host altered page identity or added claim authority: %s", sent)
			}
			_ = json.NewEncoder(w).Encode(tools.BrowserReply{OK: true, ActiveTab: "t2", Version: "1:t2:1:0", Text: "Bound page"})
		default:
			t.Errorf("unexpected browser request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	reply, err := newNativeBrowser(cfg).Execute(context.Background(), json.RawMessage(`{"action":"snapshot","tab_id":"t2","context_ref":"`+focusTestContext+`","control_epoch":0}`))
	if err != nil || !reply.OK || reply.ActiveTab != "t2" || reply.Text != "Bound page" || dispatches.Load() != 1 {
		t.Fatalf("bound action failed or replayed: reply=%+v err=%v dispatches=%d", reply, err, dispatches.Load())
	}
}
