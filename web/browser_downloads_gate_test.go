package web

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/yosukeno/pogo/tools"
)

func TestBrowserDownloadsGateReadsExistingMetadataWithoutArming(t *testing.T) {
	g := NewBrowserGate(nil)
	calls := 0
	g.executor = browserGateExecutor{execute: func(_ context.Context, raw json.RawMessage) (tools.BrowserReply, error) {
		if tools.BrowserActionOf(raw) != tools.BrowserActionDownloads {
			t.Fatal("metadata bypass dispatched a page operation")
		}
		calls++
		return tools.BrowserReply{OK: true, Data: json.RawMessage(`{"supported":true,"downloads":[]}`)}, nil
	}}
	for _, refused := range []bool{false, true} {
		g.armRefused = refused
		for _, link := range []tools.BrowserLink{g, subPane{g}} {
			reply, err := link.Execute(context.Background(), json.RawMessage(`{"action":"downloads"}`))
			if err != nil || !reply.OK || g.Armed() {
				t.Fatalf("read-only download list asked for control or failed: %+v %v", reply, err)
			}
		}
	}
	if calls != 4 {
		t.Fatalf("read request was repeated: %d", calls)
	}
	if _, err := g.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`)); !errors.Is(err, ErrBrowserNotArmed) || calls != 4 {
		t.Fatal("download list granted page access")
	}
	for _, raw := range []string{`{"action":"downloads","target":"t1"}`, `{"action":"downloads","url":"https://example.com"}`, `{"action":"downloads","path":"other.xlsx"}`, `{"action":"downloads","actions":[{"action":"navigate","url":"https://example.com"}]}`} {
		if _, err := g.Execute(context.Background(), json.RawMessage(raw)); err == nil || calls != 4 {
			t.Fatalf("invalid direct metadata request bypassed page gate: %s", raw)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.ExecuteSub(ctx, json.RawMessage(`{"action":"downloads"}`)); !errors.Is(err, context.Canceled) || calls != 4 {
		t.Fatal("cancelled download metadata reached executor")
	}
	if _, err := NewBrowserGate(nil).Execute(context.Background(), json.RawMessage(`{"action":"downloads"}`)); err == nil {
		t.Fatal("unconfigured runtime pretended to have a download list")
	}
}
