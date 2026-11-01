package web

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

func TestNativeBrowserDesktopDisplayModeAllowlist(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		allowed bool
	}{
		{`{"display_mode":"desktop"}`, true},
		{`{}`, false}, {`{"display_mode":"remote"}`, false},
		{`{"display_mode":true}`, false}, {`{"display_mode":{"mode":"desktop"}}`, false},
	} {
		state, err := nativeBrowserState([]byte(tc.raw), false)
		if err != nil {
			t.Fatal(err)
		}
		_, present := state["display_mode"]
		if present != tc.allowed || (present && string(state["display_mode"]) != `"desktop"`) {
			t.Fatalf("display mode allowlist failed for %s", tc.raw)
		}
	}
}

func TestNativeBrowserDesktopRetiredFrameDoesNotRecreateContext(t *testing.T) {
	puts, deletes := 0, 0
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			puts++
			_, _ = io.WriteString(w, `{"ok":true}`)
		case r.Method == http.MethodDelete:
			deletes++
			_, _ = io.WriteString(w, `{"ok":true}`)
		case strings.HasSuffix(r.URL.Path, "/frame"):
			w.WriteHeader(http.StatusGone)
			_, _ = io.WriteString(w, `{"error":"desktop display requires the native application"}`)
		default:
			_, _ = io.WriteString(w, `{"display_mode":"desktop"}`)
		}
	})
	b := newNativeBrowser(cfg)
	_, err := b.Frame(context.Background())
	var remote *nativeBrowserHTTPError
	if !errors.As(err, &remote) || remote.status != http.StatusGone {
		t.Fatal("retired frame status was not preserved")
	}
	state, err := b.State(context.Background())
	if err != nil || string(state["display_mode"]) != `"desktop"` || puts != 1 || deletes != 0 {
		t.Fatal("retired frame destroyed or recreated the desktop context")
	}
}

func TestNativeBrowserDesktopProfileIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions", "same-session.jsonl")
	alice, bob := identity.LocalScope("alice"), identity.LocalScope("bob")
	bind := func(path string, owner *identity.Scope) *nativeBrowser {
		b := newNativeBrowser(nil)
		b.bindSessionProfile("same-session", path, owner)
		if b.profileError != nil {
			t.Fatal(b.profileError)
		}
		key, err := hex.DecodeString(b.profileKey)
		if err != nil || len(key) != 32 || b.profileKey != strings.ToLower(b.profileKey) {
			t.Fatal("profile key is not an opaque SHA-256 hex digest")
		}
		return b
	}
	a := bind(path, &alice)
	reopened := bind(dir+"/sessions/child/../same-session.jsonl", &alice)
	if a.profileKey != reopened.profileKey {
		t.Fatal("same canonical store and owner lost their stable profile")
	}
	if a.id == reopened.id {
		t.Fatal("profile persistence must not make worker execution IDs stable")
	}
	for _, other := range []*nativeBrowser{
		bind(filepath.Join(dir, "another-store", "same-session.jsonl"), &alice),
		bind(filepath.Join(dir, "sessions", "another-session.jsonl"), &alice),
		bind(path, &bob), bind(path, nil),
	} {
		if a.profileKey == other.profileKey {
			t.Fatal("different store/session/account scopes shared a desktop profile")
		}
	}
	project := alice
	project.ProjectID = "other-project"
	if a.profileKey == bind(path, &project).profileKey {
		t.Fatal("project scope was lost in desktop profile identity")
	}
}

func TestNativeBrowserDesktopMetadataIsPrivatePUTOnly(t *testing.T) {
	var bodies []string
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("unexpected worker operation %s", r.Method)
		}
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	plain := newNativeBrowser(cfg)
	if err := plain.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	bound := newNativeBrowser(cfg)
	storePath := filepath.Join(t.TempDir(), "private-account", "session.jsonl")
	owner := identity.LocalScope("private-account")
	bound.bindSessionProfile("session", storePath, &owner)
	if err := bound.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0] != "{}" {
		t.Fatal("unbound constructor changed its legacy empty PUT contract")
	}
	var metadata map[string]string
	if json.Unmarshal([]byte(bodies[1]), &metadata) != nil || len(metadata) != 2 || metadata["client_session_id"] != "session" || metadata["profile_key"] != bound.profileKey {
		t.Fatal("trusted profile metadata was not carried in PUT")
	}
	if strings.Contains(bodies[1], storePath) || strings.Contains(bodies[1], "private-account") || strings.Contains(bodies[1], owner.SubjectID) || strings.Contains(bodies[1], bound.id) {
		t.Fatal("PUT exposed a private path, account identity or conflated worker identity")
	}
	public, err := nativeBrowserState([]byte(`{"active_tab":"t1","client_session_id":"private-session","profile_key":"private-profile","path":"private-path"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"client_session_id", "profile_key", "path"} {
		if _, exists := public[key]; exists {
			t.Fatal("worker profile metadata escaped into public browser state")
		}
	}
}

func TestManagerDesktopProfileSurvivesStoreReopen(t *testing.T) {
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"ok":true}`) })
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() }, func(c *Config) { c.BrowserURL, c.BrowserToken = cfg.url, cfg.token })
	sid := h.createSession()
	original := h.session(sid)
	if original.nativeBrowser.clientSessionID != sid || original.nativeBrowser.profileKey == "" {
		t.Fatal("manager omitted owning session metadata")
	}
	reopened, err := session.Open(original.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	restored := h.mgr.newSession(reopened, original.cfg, reopened.TimedMessages(""), original.cwd, llm.Usage{}, llm.Usage{}, original.owner)
	t.Cleanup(restored.Close)
	if restored.nativeBrowser.profileKey != original.nativeBrowser.profileKey || restored.nativeBrowser.id == original.nativeBrowser.id {
		t.Fatal("manager reconstruction lost stable profile or reused worker execution identity")
	}
	other := h.session(h.createSession())
	if other.nativeBrowser.profileKey == original.nativeBrowser.profileKey {
		t.Fatal("different manager sessions shared a desktop profile")
	}
}
