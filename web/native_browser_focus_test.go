package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/tools"
)

const focusTestContext = "ctx_012345678901234567890123"

func TestNativeBrowserTabFocusIdentity(t *testing.T) {
	validState := `{"context_ref":"` + focusTestContext + `","generation":2,"lease_token":"never-copy-this"}`
	validReply := tools.BrowserReply{OK: true, ActiveTab: "t2", Version: "2:t2:3:0:revision:0:1280x800", URL: "https://fixture.invalid/new"}
	for _, tc := range []struct {
		name      string
		op        string
		state     string
		status    int
		reply     tools.BrowserReply
		wantReads int64
		wantFocus bool
	}{
		{name: "new", op: `{"action":"tab","tab_action":"new","url":"https://fixture.invalid/new"}`, wantReads: 1, wantFocus: true},
		{name: "switch", op: `{"action":"tab","tab_action":"switch","target":"t2"}`, wantReads: 1, wantFocus: true},
		{name: "claim", op: `{"action":"tab","tab_action":"claim","target":"t2","context_ref":"` + focusTestContext + `","control_epoch":3}`, wantReads: 1, wantFocus: true},
		{name: "default switch", op: `{"action":"tab","target":"t2"}`, wantReads: 1, wantFocus: true},
		{name: "batch tab", op: `{"action":"batch","actions":[{"action":"snapshot"},{"action":"tab","tab_action":"new"}]}`, wantReads: 1, wantFocus: true},
		{name: "failed tab", op: `{"action":"tab","tab_action":"new"}`, reply: tools.BrowserReply{OK: false, ActiveTab: "t2", Version: validReply.Version, Error: "navigation failed"}, wantReads: 1},
		{name: "cancelled generation", op: `{"action":"tab","tab_action":"new"}`, reply: tools.BrowserReply{OK: true, ActiveTab: "t2", Version: "3:t2:3:0:revision:0:1280x800"}, wantReads: 1},
		{name: "contradictory tab", op: `{"action":"tab","tab_action":"new"}`, reply: tools.BrowserReply{OK: true, ActiveTab: "t2", Version: "2:t1:3:0:revision:0:1280x800"}, wantReads: 1},
		{name: "missing version", op: `{"action":"tab","tab_action":"new"}`, reply: tools.BrowserReply{OK: true, ActiveTab: "t2"}, wantReads: 1},
		{name: "unbounded version", op: `{"action":"tab","tab_action":"new"}`, reply: tools.BrowserReply{OK: true, ActiveTab: "t2", Version: "2:t2:" + strings.Repeat("x", 256)}, wantReads: 1},
		{name: "noncanonical generation", op: `{"action":"tab","tab_action":"new"}`, reply: tools.BrowserReply{OK: true, ActiveTab: "t2", Version: "02:t2:3"}, wantReads: 1},
		{name: "invalid active tab", op: `{"action":"tab","tab_action":"new"}`, reply: tools.BrowserReply{OK: true, ActiveTab: "../t2", Version: "2:../t2:3"}, wantReads: 1},
		{name: "state missing context", op: `{"action":"tab","tab_action":"new"}`, state: `{"generation":2}`, wantReads: 1},
		{name: "state malformed context", op: `{"action":"tab","tab_action":"new"}`, state: `{"generation":2,"context_ref":"worker-id-is-private"}`, wantReads: 1},
		{name: "state fractional generation", op: `{"action":"tab","tab_action":"new"}`, state: `{"generation":2.5,"context_ref":"` + focusTestContext + `"}`, wantReads: 1},
		{name: "state unsafe generation", op: `{"action":"tab","tab_action":"new"}`, state: `{"generation":9007199254740992,"context_ref":"` + focusTestContext + `"}`, wantReads: 1},
		{name: "state zero generation", op: `{"action":"tab","tab_action":"new"}`, state: `{"generation":0,"context_ref":"` + focusTestContext + `"}`, wantReads: 1},
		{name: "state malformed", op: `{"action":"tab","tab_action":"new"}`, state: `{`, wantReads: 1},
		{name: "state unavailable", op: `{"action":"tab","tab_action":"new"}`, status: http.StatusServiceUnavailable, wantReads: 1},
		{name: "missing state does not recreate", op: `{"action":"tab","tab_action":"new"}`, status: http.StatusNotFound, wantReads: 1},
		{name: "closed state does not recreate", op: `{"action":"tab","tab_action":"new"}`, status: http.StatusGone, wantReads: 1},
		{name: "worker contradicts captured context", op: `{"action":"tab","tab_action":"new"}`, reply: tools.BrowserReply{OK: true, ActiveTab: "t2", Version: validReply.Version, ContextRef: "ctx_999999999999999999999999"}, wantReads: 1},
		{name: "worker confirms captured context", op: `{"action":"tab","tab_action":"new"}`, reply: tools.BrowserReply{OK: true, ActiveTab: "t2", Version: validReply.Version, ContextRef: focusTestContext}, wantReads: 1, wantFocus: true},
		{name: "snapshot no extra read", op: `{"action":"snapshot"}`},
		{name: "navigate no extra read", op: `{"action":"navigate","url":"https://fixture.invalid/new"}`},
		{name: "tabs no extra read", op: `{"action":"tabs"}`},
		{name: "non-tab batch no extra read", op: `{"action":"batch","actions":[{"action":"snapshot"},{"action":"find","text":"value"}]}`},
		{name: "unverified worker context dropped", op: `{"action":"snapshot"}`, reply: tools.BrowserReply{OK: true, ActiveTab: "t2", Version: validReply.Version, ContextRef: focusTestContext}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := tc.state
			if state == "" {
				state = validState
			}
			reply := tc.reply
			if !reply.OK && reply.Error == "" && reply.ActiveTab == "" && reply.Version == "" && reply.ContextRef == "" {
				reply = validReply
			}
			var reads, executes, creates, unexpected atomic.Int64
			cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/state"):
					reads.Add(1)
					if tc.status != 0 {
						w.WriteHeader(tc.status)
					}
					_, _ = w.Write([]byte(state))
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/execute"):
					executes.Add(1)
					var sent map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
						t.Errorf("invalid operation: %v", err)
					}
					var original map[string]json.RawMessage
					_ = json.Unmarshal([]byte(tc.op), &original)
					if string(sent["action"]) != string(original["action"]) || string(sent["url"]) != string(original["url"]) || sent["op_id"] == nil {
						t.Error("focus metadata changed the dispatched operation")
					}
					if string(sent["context_ref"]) != string(original["context_ref"]) || string(sent["control_epoch"]) != string(original["control_epoch"]) || sent["lease_token"] != nil {
						t.Error("focus metadata added execution authority")
					}
					_ = json.NewEncoder(w).Encode(reply)
				case r.Method == http.MethodPut:
					creates.Add(1)
					_, _ = w.Write([]byte(`{"ok":true}`))
				default:
					unexpected.Add(1)
					t.Errorf("metadata issued unexpected %s request", r.Method)
					w.WriteHeader(http.StatusBadRequest)
				}
			})
			b := newNativeBrowser(cfg)
			got, err := b.Execute(context.Background(), json.RawMessage(tc.op))
			if err != nil {
				t.Fatalf("optional metadata blocked operation: %v", err)
			}
			wantContext := ""
			if tc.wantFocus {
				wantContext = focusTestContext
			}
			if got.ContextRef != wantContext {
				t.Fatalf("focus context = %q, want %q", got.ContextRef, wantContext)
			}
			if got.OK != reply.OK || got.URL != reply.URL || got.ActiveTab != reply.ActiveTab || got.Version != reply.Version {
				t.Error("focus metadata changed the operation result")
			}
			if reads.Load() != tc.wantReads || executes.Load() != 1 || creates.Load() != 1 || unexpected.Load() != 0 {
				t.Fatalf("reads=%d executes=%d creates=%d unexpected=%d", reads.Load(), executes.Load(), creates.Load(), unexpected.Load())
			}
		})
	}
}

func TestNativeBrowserTabFocusMetadataBudget(t *testing.T) {
	var executes atomic.Int64
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/state"):
			<-r.Context().Done()
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/execute"):
			executes.Add(1)
			_ = json.NewEncoder(w).Encode(tools.BrowserReply{OK: true, ActiveTab: "t2", Version: "1:t2:1"})
		case r.Method == http.MethodPut:
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Errorf("unexpected request %s", r.Method)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	got, err := newNativeBrowser(cfg).Execute(ctx, json.RawMessage(`{"action":"tab","tab_action":"new"}`))
	if err != nil || !got.OK || got.ContextRef != "" || executes.Load() != 1 {
		t.Fatalf("metadata timeout changed execution: reply=%+v err=%v executes=%d", got, err, executes.Load())
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("optional metadata consumed the page operation deadline")
	}
}

func TestNativeBrowserTabFocusReadsFreshContextForEachOperation(t *testing.T) {
	contexts := []string{focusTestContext, "ctx_999999999999999999999999"}
	var index atomic.Int64
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/state"):
			_ = json.NewEncoder(w).Encode(map[string]any{"context_ref": contexts[index.Load()], "generation": 1})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/execute"):
			_ = json.NewEncoder(w).Encode(tools.BrowserReply{OK: true, ActiveTab: "t2", Version: "1:t2:1"})
			index.Add(1)
		case r.Method == http.MethodPut:
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Errorf("unexpected request %s", r.Method)
		}
	})
	b := newNativeBrowser(cfg)
	for _, want := range contexts {
		got, err := b.Execute(context.Background(), json.RawMessage(`{"action":"tab","tab_action":"new"}`))
		if err != nil || got.ContextRef != want {
			t.Fatalf("reused generation/tab confused contexts: reply=%+v err=%v want=%s", got, err, want)
		}
	}
}
