package web

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func TestBrowserBookmarkReadDoesNotArmOrTouchWorker(t *testing.T) {
	m := bookmarkManager(t)
	owner := identity.LocalScope("alice")
	_, err := m.replaceBrowserBookmarks(&owner, 0, []browserBookmark{{ID: "one", Title: "Saved", URL: "https://saved.example/"}})
	if err != nil {
		t.Fatal(err)
	}
	g := NewBrowserGate(nil)
	workerCalls, metadataCalls := 0, 0
	g.executor = browserGateExecutor{execute: func(context.Context, json.RawMessage) (tools.BrowserReply, error) {
		workerCalls++
		return tools.BrowserReply{}, errors.New("fixture worker disconnected")
	}}
	g.bookmarks = func(ctx context.Context, query string, limit, offset int) (tools.BrowserReply, error) {
		metadataCalls++
		if query != "Saved" || limit != tools.DefaultBrowserBookmarkLimit || offset != 0 {
			t.Fatal("query was not normalized")
		}
		return m.browserBookmarkReply(&owner, query, limit, offset)
	}
	for _, refused := range []bool{false, true} {
		g.armRefused = refused
		for _, link := range []tools.BrowserLink{g, subPane{g}} {
			reply, err := link.Execute(context.Background(), json.RawMessage(`{"action":"bookmarks","query":"  Saved  "}`))
			if err != nil || !reply.OK || len(reply.Data) == 0 || reply.URL != "" || g.Armed() || workerCalls != 0 {
				t.Fatalf("metadata read changed page authority: %+v %v", reply, err)
			}
		}
	}
	if metadataCalls != 4 {
		t.Fatalf("metadata calls=%d", metadataCalls)
	}
	if _, err := g.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`)); !errors.Is(err, ErrBrowserNotArmed) || workerCalls != 0 {
		t.Fatal("page read escaped gate")
	}
	g.Arm()
	if _, err := g.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`)); err == nil || workerCalls != 1 {
		t.Fatal("armed page action did not use the original worker")
	}
}

func TestBrowserBookmarkReadRejectsInvalidAndCancelledCalls(t *testing.T) {
	g := NewBrowserGate(nil)
	g.executor = browserGateExecutor{execute: func(context.Context, json.RawMessage) (tools.BrowserReply, error) {
		t.Fatal("worker must not run")
		return tools.BrowserReply{}, nil
	}}
	calls := 0
	g.bookmarks = func(context.Context, string, int, int) (tools.BrowserReply, error) {
		calls++
		return tools.BrowserReply{OK: true}, nil
	}
	for _, raw := range []string{
		`{"action":"bookmarks","account_id":"other"}`, `{"action":"bookmarks","url":"https://example.com"}`,
		`{"action":"bookmarks","limit":0}`, `{"action":"bookmarks","offset":-1}`, `{"action":"bookmarks","actions":[{"action":"navigate","url":"https://example.com"}]}`,
	} {
		if _, err := g.Execute(context.Background(), json.RawMessage(raw)); err == nil || calls != 0 {
			t.Fatalf("invalid metadata reached provider: %s", raw)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Execute(ctx, json.RawMessage(`{"action":"bookmarks"}`)); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("cancelled metadata reached provider")
	}
	g.Arm()
	if _, err := (&tools.Browser{Link: g}).Execute(context.Background(), json.RawMessage(`{"action":"batch","actions":[{"action":"bookmarks"}]}`)); err == nil || calls != 0 {
		t.Fatal("metadata batch accepted")
	}
}

func TestSessionBindsBookmarksToTrustedOwnerAndSubagent(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return plainTurn() }, func(c *Config) { c.BrowserURL, c.BrowserToken = "http://127.0.0.1:9", "test-only-token" })
	alice, bob := identity.LocalScope("alice"), identity.LocalScope("bob")
	for i, owner := range []*identity.Scope{&alice, &bob} {
		_, err := h.mgr.replaceBrowserBookmarks(owner, 0, []browserBookmark{{ID: "one", Title: []string{"Alice saved", "Bob saved"}[i], URL: []string{"https://alice.example/", "https://bob.example/"}[i]}})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i, token := range []string{aliceToken, bobToken} {
		s := h.session(h.createAs(token))
		if s.browser.bookmarks == nil || s.browser.Armed() {
			t.Fatal("session metadata provider unavailable or armed")
		}
		for _, link := range []tools.BrowserLink{s.browser, subPane{s.browser}} {
			reply, err := link.Execute(context.Background(), json.RawMessage(`{"action":"bookmarks"}`))
			var view browserBookmarkView
			if err != nil || json.Unmarshal(reply.Data, &view) != nil || len(view.Bookmarks) != 1 || view.Bookmarks[0].Title != []string{"Alice saved", "Bob saved"}[i] {
				t.Fatal("session or subagent did not inherit trusted account")
			}
		}
	}
}
