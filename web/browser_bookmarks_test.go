package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/identity"
)

func bookmarkManager(t *testing.T) *Manager {
	t.Helper()
	return &Manager{cfg: Config{SessionDir: t.TempDir()}}
}

func TestBrowserBookmarksRoundTripOwnershipAndQuery(t *testing.T) {
	m := bookmarkManager(t)
	alice, bob := identity.LocalScope("alice"), identity.LocalScope("bob")
	initial, err := m.browserBookmarks(&alice, "")
	if err != nil || initial.Status != "not_initialized" || initial.Revision != 0 || initial.Bookmarks == nil || initial.AccountID == "" {
		t.Fatalf("initial account state = %+v, %v", initial, err)
	}
	entries := []browserBookmark{{ID: "one", Title: "  百度  ", URL: "https://example.test/Baidu"}, {ID: "two", Title: "GitLab", URL: "http://192.0.2.1:8929/root/project"}}
	saved, err := m.replaceBrowserBookmarks(&alice, 0, entries)
	if err != nil || saved.Status != "ready" || saved.Revision != 1 || saved.AccountID != initial.AccountID || saved.Bookmarks[0].Title != "百度" {
		t.Fatalf("saved account state = %+v, %v", saved, err)
	}
	// Other accounts, other projects for the same subject, and classic
	// single-user mode each remain explicitly uninitialized.
	otherProject := alice
	otherProject.ProjectID = "different-project"
	for _, owner := range []*identity.Scope{&bob, &otherProject, nil} {
		view, err := m.browserBookmarks(owner, "")
		if err != nil || view.Status != "not_initialized" || len(view.Bookmarks) != 0 || view.AccountID == saved.AccountID {
			t.Fatal("another ownership namespace read the account's bookmarks")
		}
	}
	query, err := m.browserBookmarks(&alice, "  BAIDU  ")
	if err != nil || query.Total != 2 || query.Matched != 1 || len(query.Bookmarks) != 1 || query.Bookmarks[0].ID != "one" || query.Query != "BAIDU" {
		t.Fatalf("case-insensitive URL query = %+v, %v", query, err)
	}
	query, err = m.browserBookmarks(&alice, "不存在")
	if err != nil || query.Status != "ready" || query.Total != 2 || query.Matched != 0 || query.Bookmarks == nil {
		t.Fatalf("empty match must not mean uninitialized = %+v, %v", query, err)
	}
	reloaded := &Manager{cfg: m.cfg}
	view, err := reloaded.browserBookmarks(&alice, "")
	if err != nil || view.Revision != 1 || len(view.Bookmarks) != 2 {
		t.Fatal("saved account bookmarks did not survive a manager restart")
	}
	path, _ := browserBookmarkPath(m.cfg.SessionDir, &alice)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("bookmark store must remain private on disk")
	}
	// Deleting the final entry intentionally initializes an empty account;
	// that cannot be confused with local bookmarks that were never synced.
	view, err = m.replaceBrowserBookmarks(&alice, 1, []browserBookmark{})
	if err != nil || view.Status != "ready" || view.Revision != 2 || view.Total != 0 {
		t.Fatal("explicit empty account bookmark set was lost")
	}
}

func TestBrowserBookmarksCompareAndSwapDoesNotOverwrite(t *testing.T) {
	m := bookmarkManager(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"first", "second"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := m.replaceBrowserBookmarks(nil, 0, []browserBookmark{{ID: name, Title: name, URL: "https://example.test/"}})
			results <- err
		}(name)
	}
	wg.Wait()
	close(results)
	ok, conflict := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, errBrowserBookmarksConflict):
			conflict++
		default:
			t.Fatal(err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal("two windows overwrote the same bookmark revision")
	}
	view, err := m.browserBookmarks(nil, "")
	if err != nil || view.Revision != 1 || len(view.Bookmarks) != 1 {
		t.Fatal("CAS did not retain exactly the successful write")
	}
}

func TestBrowserBookmarksValidationAndCorruptionPreservation(t *testing.T) {
	good := browserBookmark{ID: "id", Title: "Title", URL: "https://example.test/"}
	for _, tc := range []struct {
		name  string
		items []browserBookmark
	}{
		{"null array", nil},
		{"duplicate IDs", []browserBookmark{good, good}},
		{"internal URL", []browserBookmark{{ID: "id", Title: "Title", URL: "chrome://bookmarks/"}}},
		{"JavaScript URL", []browserBookmark{{ID: "id", Title: "Title", URL: "javascript:alert(1)"}}},
		{"credential URL", []browserBookmark{{ID: "id", Title: "Title", URL: "https://name:password@example.test/"}}},
		{"relative URL", []browserBookmark{{ID: "id", Title: "Title", URL: "/panels/site/"}}},
		{"blank host", []browserBookmark{{ID: "id", Title: "Title", URL: "https:///test"}}},
		{"space URL", []browserBookmark{{ID: "id", Title: "Title", URL: "https://example.test/a b"}}},
		{"comma host", []browserBookmark{{ID: "id", Title: "Title", URL: "https://www,baidu.com/"}}},
		{"underscore host", []browserBookmark{{ID: "id", Title: "Title", URL: "https://invalid_host.test/"}}},
		{"numeric IPv4 host", []browserBookmark{{ID: "id", Title: "Title", URL: "https://999.999.999.999/"}}},
		{"invalid IPv6 host", []browserBookmark{{ID: "id", Title: "Title", URL: "https://[invalid]/"}}},
		{"port limit", []browserBookmark{{ID: "id", Title: "Title", URL: "https://example.test:65536/"}}},
		{"title control", []browserBookmark{{ID: "id", Title: "a\x00b", URL: good.URL}}},
		{"title limit", []browserBookmark{{ID: "id", Title: strings.Repeat("中", 121), URL: good.URL}}},
		{"ID limit", []browserBookmark{{ID: strings.Repeat("a", 161), Title: "Title", URL: good.URL}}},
		{"URL limit", []browserBookmark{{ID: "id", Title: "Title", URL: good.URL + strings.Repeat("x", 4096)}}},
		{"entry count", make([]browserBookmark, 257)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateBrowserBookmarks(tc.items); err == nil {
				t.Fatal("invalid saved navigation target was accepted")
			}
		})
	}
	m := bookmarkManager(t)
	if _, err := m.browserBookmarks(&identity.Scope{}, ""); err == nil {
		t.Fatal("invalid ownership scope reached the classic single-user namespace")
	}
	if _, err := m.browserBookmarks(nil, strings.Repeat("中", 201)); err == nil {
		t.Fatal("oversized query accepted")
	}
	for _, query := range []string{"\tSite", "Site\n", "\x00"} {
		if _, err := m.browserBookmarks(nil, query); err == nil {
			t.Fatal("control characters at the query boundaries were trimmed instead of rejected")
		}
	}
	for _, target := range []string{"http://localhost:19777/", "https://[::1]:8443/path", "https://xn--fiqs8s.test/", "https://example.test./path"} {
		if _, err := validateBrowserBookmarks([]browserBookmark{{ID: "id", Title: "Valid target", URL: target}}); err != nil {
			t.Fatalf("canonical browser host rejected: %v", err)
		}
	}
	if _, err := m.replaceBrowserBookmarks(nil, 0, []browserBookmark{good}); err != nil {
		t.Fatal(err)
	}
	path, _ := browserBookmarkPath(m.cfg.SessionDir, nil)
	bad := []byte(`{"version":1,"revision":1,"bookmarks":"private-corrupt-content"}`)
	if err := os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	for _, op := range []func() error{
		func() error { _, err := m.browserBookmarks(nil, ""); return err },
		func() error { _, err := m.replaceBrowserBookmarks(nil, 1, []browserBookmark{}); return err },
	} {
		if err := op(); err == nil || strings.Contains(err.Error(), "private-corrupt-content") {
			t.Fatal("corrupt account data must fail closed without exposing content")
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != string(bad) {
		t.Fatal("failed read or save discarded the existing bookmark file")
	}
}

func TestBrowserBookmarksAPIAuthenticationIsolationAndCAS(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	path := "/api/browser/bookmarks"
	res := h.doAs("", http.MethodGet, path, "")
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous bookmark read: %d", res.StatusCode)
	}
	read := func(token string) browserBookmarkView {
		t.Helper()
		res := h.doAs(token, http.MethodGet, path, "")
		defer res.Body.Close()
		var view browserBookmarkView
		if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&view) != nil || res.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("bookmark read failed: %d", res.StatusCode)
		}
		return view
	}
	alice, bob := read(aliceToken), read(bobToken)
	if alice.AccountID == "" || alice.AccountID == bob.AccountID || alice.Status != "not_initialized" {
		t.Fatal("account namespace is not explicit or isolated")
	}
	payload, _ := json.Marshal(map[string]any{"account_id": alice.AccountID, "revision": 0, "bookmarks": []browserBookmark{{ID: "alice-one", Title: "Alice navigation", URL: "https://example.test/alice"}}})
	res = h.doAs(aliceToken, http.MethodPut, path, string(payload))
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("initial save: %d", res.StatusCode)
	}
	for _, token := range []string{aliceToken, bobToken} {
		res = h.doAs(token, http.MethodPut, path, string(payload))
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusConflict {
			t.Fatalf("stale revision or switched identity accepted: %d", res.StatusCode)
		}
		if token == bobToken && strings.Contains(string(raw), "Alice navigation") {
			t.Fatal("identity mismatch returned another account's bookmarks")
		}
	}
	if read(bobToken).Status != "not_initialized" || len(read(aliceToken).Bookmarks) != 1 {
		t.Fatal("write crossed account boundaries")
	}
	for _, body := range []string{
		`null`,
		`{"revision":0,"bookmarks":[]}`,
		`{"account_id":"unused","bookmarks":[]}`,
		`{"account_id":"unused","revision":0,"bookmarks":[],"owner":"bob"}`,
		`{"account_id":"unused","revision":0,"bookmarks":[]} {}`,
	} {
		res = h.doAs(aliceToken, http.MethodPut, path, body)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid body accepted: %d", res.StatusCode)
		}
	}
	for _, body := range []string{
		fmt.Sprintf(`{"account_id":%q,"revision":1,"bookmarks":null}`, alice.AccountID),
		fmt.Sprintf(`{"account_id":%q,"revision":1,"bookmarks":[{"id":"one","title":"Site","url":"javascript:alert(1)"}]}`, alice.AccountID),
		fmt.Sprintf(`{"account_id":%q,"revision":1,"bookmarks":[{"id":"one","title":"Site","url":"https://example.test/","source":"page"}]}`, alice.AccountID),
		fmt.Sprintf(`{"account_id":%q,"revision":-1,"bookmarks":[]}`, alice.AccountID),
		fmt.Sprintf(`{"account_id":%q,"revision":9007199254740991,"bookmarks":[]}`, alice.AccountID),
	} {
		res = h.doAs(aliceToken, http.MethodPut, path, body)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid scoped bookmark payload accepted: %d", res.StatusCode)
		}
	}
	if read(aliceToken).Revision != 1 {
		t.Fatal("invalid API writes mutated saved bookmarks")
	}
	request, _ := http.NewRequest(http.MethodPut, h.srv.URL+path, strings.NewReader(string(payload)))
	request.Header.Set("Authorization", "Bearer "+aliceToken)
	request.Header.Set("Origin", "https://unrelated.test")
	res, err := h.srv.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin bookmark write: %d", res.StatusCode)
	}
}

func TestBrowserBookmarkToolReplyIsMetadataWithoutWebEvidence(t *testing.T) {
	m := bookmarkManager(t)
	owner := identity.LocalScope("alice")
	if _, err := m.replaceBrowserBookmarks(&owner, 0, []browserBookmark{{ID: "bookmark-one", Title: "Example", URL: "https://example.test/"}}); err != nil {
		t.Fatal(err)
	}
	reply, err := m.browserBookmarkReply(&owner, "Example", 20, 0)
	if err != nil || !reply.OK || reply.URL != "" || reply.Title != "" || reply.Text != "" || reply.Screenshot != nil || len(reply.Evidence) != 0 {
		t.Fatal("account navigation metadata became a webpage observation")
	}
	var view browserBookmarkView
	if json.Unmarshal(reply.Data, &view) != nil || view.Source != "pogo_bookmarks" || view.ObservationScope != "pogo_workspace" || view.Scope != "account" || view.Status != "ready" || view.Total != 1 || view.Matched != 1 {
		t.Fatal("bookmark metadata lost its source or ownership provenance")
	}
}

func TestBrowserBookmarkToolPaginationAndCurrentRevision(t *testing.T) {
	m := bookmarkManager(t)
	entries := make([]browserBookmark, 45)
	for i := range entries {
		entries[i] = browserBookmark{ID: fmt.Sprintf("entry-%d", i), Title: fmt.Sprintf("Saved site %d", i), URL: fmt.Sprintf("https://example.test/%d", i)}
	}
	if _, err := m.replaceBrowserBookmarks(nil, 0, entries); err != nil {
		t.Fatal(err)
	}
	read := func(query string, limit, offset int) browserBookmarkView {
		t.Helper()
		reply, err := m.browserBookmarkReply(nil, query, limit, offset)
		var view browserBookmarkView
		if err != nil || json.Unmarshal(reply.Data, &view) != nil {
			t.Fatalf("bookmark page: %v", err)
		}
		return view
	}
	first := read("", 0, 0)
	if first.Limit != 20 || first.Offset != 0 || first.Total != 45 || first.Matched != 45 || len(first.Bookmarks) != 20 || first.NextOffset == nil || *first.NextOffset != 20 {
		t.Fatal("default page dropped the complete account count or continuation")
	}
	last := read("", 20, 40)
	if len(last.Bookmarks) != 5 || last.NextOffset != nil || last.Bookmarks[0].ID != "entry-40" || last.Offset != 40 {
		t.Fatal("last page included duplicate entries or a false continuation")
	}
	query := read("Saved site 4", 2, 0)
	if query.Total != 45 || query.Matched != 6 || len(query.Bookmarks) != 2 || query.NextOffset == nil || *query.NextOffset != 2 {
		t.Fatal("filtered page count confused matches with the entire account")
	}
	if _, err := m.replaceBrowserBookmarks(nil, 1, []browserBookmark{}); err != nil {
		t.Fatal(err)
	}
	empty := read("", 20, 0)
	if empty.Status != "ready" || empty.Revision != 2 || empty.Total != 0 || len(empty.Bookmarks) != 0 {
		t.Fatal("Agent kept a stale copy after the human deleted bookmarks")
	}
	for _, p := range [][2]int{{51, 0}, {-1, 0}, {20, -1}} {
		if _, err := m.browserBookmarkReply(nil, "", p[0], p[1]); err == nil {
			t.Fatal("invalid tool pagination accepted")
		}
	}
}
