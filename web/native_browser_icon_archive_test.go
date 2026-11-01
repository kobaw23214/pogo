package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/tools"
)

const archiveTestIcon = "data:image/svg+xml;base64,PHN2Zz48L3N2Zz4="

func archiveTranscript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversation.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBrowserIconArchiveRetainsExactURLHistoryAcrossMissingIconsAndReload(t *testing.T) {
	transcript := archiveTranscript(t)
	owner := identity.LocalScope("archive-owner")
	a := newBrowserIconArchive(transcript, &owner)
	a.remember([]nativeBrowserIcon{{"https://site.test/page?q=one#section", archiveTestIcon}, {"https://site.test/page?q=two", archiveTestIcon}, {"about:blank", archiveTestIcon}, {"javascript:alert(1)", archiveTestIcon}, {"https://user:secret@site.test/", archiveTestIcon}})
	icons := a.read()
	if len(icons) != 2 || icons[0].URL != "https://site.test/page?q=one" || icons[1].URL != "https://site.test/page?q=two" {
		t.Fatalf("exact URL archive mixed query/credentials: %+v", icons)
	}
	before, _ := os.ReadFile(a.path)
	info, _ := os.Stat(a.path)
	if info.Mode().Perm() != 0o600 {
		t.Fatal("archive is not private")
	}
	a.remember([]nativeBrowserIcon{{"https://site.test/page?q=one#other", archiveTestIcon}, {"https://site.test/page?q=two", ""}})
	a.remember(nil)
	after, _ := os.ReadFile(a.path)
	infoAfter, _ := os.Stat(a.path)
	if !bytes.Equal(before, after) || !info.ModTime().Equal(infoAfter.ModTime()) {
		t.Fatal("unchanged or missing live icons rewrote/lost historical metadata")
	}
	if got := newBrowserIconArchive(transcript, &owner).read(); len(got) != 2 {
		t.Fatal("new session holder did not load archived icons")
	}
	other := identity.LocalScope("another-owner")
	if len(newBrowserIconArchive(transcript, &other).read()) != 0 {
		t.Fatal("owner binding leaked old tenant icons")
	}
	if err := os.Remove(transcript); err != nil {
		t.Fatal(err)
	}
	if len(a.read()) != 0 || len(newBrowserIconArchive(transcript, &owner).read()) != 0 {
		t.Fatal("deleted transcript left readable archive metadata")
	}
}

func TestBrowserIconArchiveBoundsAndCorruptOrForeignStores(t *testing.T) {
	transcript := archiveTranscript(t)
	a := newBrowserIconArchive(transcript, nil)
	entries := make([]nativeBrowserIcon, 100)
	for index := range entries {
		entries[index] = nativeBrowserIcon{fmt.Sprintf("https://site.test/%d", index), archiveTestIcon}
	}
	a.remember(entries)
	if icons := a.read(); len(icons) != browserIconArchiveEntries || icons[0].URL != "https://site.test/36" {
		t.Fatal("entry limit did not retain the bounded newest set")
	}
	large := "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, nativeBrowserFaviconLimit))
	for index := 0; index < 24; index++ {
		a.remember([]nativeBrowserIcon{{fmt.Sprintf("https://large.test/%d", index), large}})
	}
	total := 0
	for _, icon := range a.read() {
		_, size, valid := validArchivedBrowserIcon(icon)
		if !valid {
			t.Fatal("invalid icon stored")
		}
		total += size
	}
	if total > browserIconArchiveBytes || len(a.read()) != 16 {
		t.Fatalf("total bytes unbounded: %d", total)
	}
	raw, _ := os.ReadFile(a.path)
	foreign := archiveTranscript(t)
	if err := os.WriteFile(browserIconArchivePath(foreign), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if len(newBrowserIconArchive(foreign, nil).read()) != 0 {
		t.Fatal("copied foreign session/path archive was accepted")
	}
	for _, corrupt := range [][]byte{[]byte("{broken"), bytes.Repeat([]byte("x"), browserIconArchiveFileLimit+1), []byte(`{"version":2,"icons":[]}`)} {
		if err := os.WriteFile(a.path, corrupt, 0o600); err != nil {
			t.Fatal(err)
		}
		if len(newBrowserIconArchive(transcript, nil).read()) != 0 {
			t.Fatal("corrupted store did not fail safely")
		}
	}
	if err := os.Remove(a.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(browserIconArchivePath(foreign), a.path); err != nil {
		t.Fatal(err)
	}
	if len(newBrowserIconArchive(transcript, nil).read()) != 0 {
		t.Fatal("symlink archive escaped its session path")
	}
}

func TestBrowserIconArchiveRetriesSameBytesAfterWriteFailure(t *testing.T) {
	transcript := archiveTranscript(t)
	a := newBrowserIconArchive(transcript, nil)
	if err := os.Mkdir(a.path, 0o700); err != nil {
		t.Fatal(err)
	}
	entries := []nativeBrowserIcon{{"https://retry.test/", archiveTestIcon}}
	a.remember(entries)
	if !a.dirty || a.retryAfter.IsZero() {
		t.Fatal("failed save lost its retry obligation")
	}
	if err := os.Remove(a.path); err != nil {
		t.Fatal(err)
	}
	a.remember(entries)
	if _, err := os.Stat(a.path); !os.IsNotExist(err) {
		t.Fatal("failure retries were not rate limited")
	}
	// Advance this holder's retry deadline rather than sleeping a full retry
	// interval; the same input must still write after the filesystem recovers.
	a.retryAfter = time.Now().Add(-time.Millisecond)
	a.remember(entries)
	if a.dirty || len(newBrowserIconArchive(transcript, nil).read()) != 1 {
		t.Fatal("same bytes did not persist after a failed write recovered")
	}
	missing := newBrowserIconArchive(filepath.Join(t.TempDir(), "missing.jsonl"), nil)
	for index := 0; index < 100; index++ {
		missing.remember([]nativeBrowserIcon{{fmt.Sprintf("https://missing.test/%d", index), archiveTestIcon}})
	}
	if len(missing.icons) > browserIconArchiveEntries {
		t.Fatal("missing transcript bypassed the in-memory bound")
	}
}

func TestNativeBrowserIconArchiveCapturesStateAndExecuteAcrossContextReplacement(t *testing.T) {
	transcript := archiveTranscript(t)
	archive := newBrowserIconArchive(transcript, nil)
	var mode atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/execute") {
			writeJSON(w, 200, tools.BrowserReply{OK: true, URL: "https://observation.test/view?case=one#fragment", Text: "Observed text", Favicon: archiveTestIcon})
			return
		}
		state := nativeSheetsState("t1")
		if mode.Load() == 0 {
			state["tabs"] = []map[string]any{{"id": "t1", "url": "https://page.test/a", "title": "A", "favicon": archiveTestIcon}, {"id": "t2", "url": "https://page.test/b", "title": "B", "favicon": archiveTestIcon}}
		} else {
			state["context_ref"] = "ctx_999999999999999999999999"
			state["tabs"] = []map[string]any{{"id": "t1", "url": "about:blank", "title": ""}}
		}
		writeJSON(w, 200, state)
	})
	b := newNativeBrowser(cfg)
	b.rememberIcons = archive.remember
	if _, err := b.State(context.Background()); err != nil {
		t.Fatal(err)
	}
	mode.Store(1)
	if _, err := b.State(context.Background()); err != nil {
		t.Fatal(err)
	}
	reply, err := b.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`))
	if err != nil || reply.Text != "Observed text" {
		t.Fatal("icon persistence changed browser observation")
	}
	icons := newBrowserIconArchive(transcript, nil).read()
	if len(icons) != 3 || icons[2].URL != "https://observation.test/view?case=one" {
		t.Fatal("state, new context or tool observation lost prior icons")
	}
}

func TestBrowserIconsEndpointLoadsWithoutBrowserAndIsolatesTenantsAndDelete(t *testing.T) {
	var requests atomic.Int32
	worker := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeJSON(w, 200, nativeSheetsState("t1"))
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.mgr.cfg.browser = worker
	alice, bob := h.createAs(aliceToken), h.createAs(bobToken)
	sess := h.session(alice)
	archive := newBrowserIconArchive(sess.store.Path(), sess.owner)
	archive.remember([]nativeBrowserIcon{{"https://archive-only.test/", archiveTestIcon}})
	// Recreate the Go session object from disk. Icon reads must neither load a
	// new Go runtime nor call the still-configured worker.
	h.mgr.evictAll()
	get := func(token, sid string) (int, []byte) {
		res := h.doAs(token, "GET", "/api/sessions/"+sid+"/browser/icons", "")
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, raw
	}
	status, raw := get(aliceToken, alice)
	if status != 200 || !bytes.Contains(raw, []byte(archiveTestIcon)) || requests.Load() != 0 || h.mgr.sessions[alice] != nil {
		t.Fatalf("metadata fetch invoked browser/runtime: %d, requests=%d", status, requests.Load())
	}
	if status, raw := get(bobToken, alice); status != 404 || bytes.Contains(raw, []byte(archiveTestIcon)) {
		t.Fatal("another tenant read private icon archive")
	}
	if status, raw := get(bobToken, bob); status != 200 || !bytes.Equal(bytes.TrimSpace(raw), []byte(`{"icons":[]}`)) {
		t.Fatal("empty tenant inherited another tenant archive")
	}
	if err := h.mgr.Delete(alice); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(archive.path); !os.IsNotExist(err) {
		t.Fatal("session deletion left the icon sidecar")
	}
	if status, _ := get(aliceToken, alice); status != 404 {
		t.Fatal("deleted session metadata remained reachable")
	}
}

func TestNativeBrowserLifecycleCodesAreExplicitAndAllowlisted(t *testing.T) {
	for _, code := range []string{"BROWSER_CONTEXT_CHANGED", "BROWSER_TAB_CLOSED", "BROWSER_SESSION_EXPIRED", "not-a-public-code"} {
		cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 409, map[string]string{"error": "bound context rejected", "code": code})
		})
		b := newNativeBrowser(cfg)
		_, err := b.State(context.Background(), nativeBrowserTarget{ContextRef: nativeSheetsContext, TabID: "t1"})
		var remote *nativeBrowserHTTPError
		if !errors.As(err, &remote) || remote.status != 409 {
			t.Fatal("typed binding refusal changed status")
		}
		if code == "not-a-public-code" && remote.code != "" || code != "not-a-public-code" && remote.code != code {
			t.Fatal("lifecycle code allowlist changed")
		}
	}
}
