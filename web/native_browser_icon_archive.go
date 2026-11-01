package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/identity"
)

const browserIconArchiveEntries = 64
const browserIconArchiveBytes = 1 << 20
const browserIconArchiveFileLimit = 2 << 20
const browserIconArchiveRetryDelay = 5 * time.Second

type nativeBrowserIcon struct {
	URL     string `json:"url"`
	Favicon string `json:"favicon"`
}

type browserIconArchiveDisk struct {
	Version int                 `json:"version"`
	Binding string              `json:"binding"`
	Icons   []nativeBrowserIcon `json:"icons"`
}

type browserIconArchive struct {
	mu         sync.Mutex
	transcript string
	path       string
	binding    string
	loaded     bool
	icons      []nativeBrowserIcon
	dirty      bool
	retryAfter time.Time
}

func browserIconArchivePath(transcript string) string { return transcript + ".browser-icons.json" }

func newBrowserIconArchive(transcript string, owner *identity.Scope) *browserIconArchive {
	abs, err := filepath.Abs(transcript)
	if err != nil {
		abs = filepath.Clean(transcript)
	}
	bound, _ := json.Marshal(struct {
		Path  string
		Owner *identity.Scope
	}{filepath.Clean(abs), owner})
	digest := sha256.Sum256(bound)
	return &browserIconArchive{transcript: transcript, path: browserIconArchivePath(transcript), binding: hex.EncodeToString(digest[:]), icons: []nativeBrowserIcon{}}
}

// Only exact page URLs join citations. Query stays intact, fragment is a
// document-local location, and credentials/non-web URLs never enter the store.
func browserIconURL(value string) string {
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\r\n\t") {
		return ""
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" {
		return ""
	}
	u.Fragment, u.RawFragment = "", ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

func validArchivedBrowserIcon(icon nativeBrowserIcon) (nativeBrowserIcon, int, bool) {
	icon.URL = browserIconURL(icon.URL)
	if icon.URL == "" {
		return nativeBrowserIcon{}, 0, false
	}
	raw, _ := json.Marshal(icon.Favicon)
	value, valid := nativeBrowserFavicon(raw)
	if !valid {
		return nativeBrowserIcon{}, 0, false
	}
	_, encoded, _ := strings.Cut(value, ";base64,")
	size := base64.StdEncoding.DecodedLen(len(encoded))
	if strings.HasSuffix(encoded, "==") {
		size -= 2
	} else if strings.HasSuffix(encoded, "=") {
		size--
	}
	icon.Favicon = value
	return icon, size, true
}

func regularBrowserArchiveFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func (a *browserIconArchive) load() {
	if a.loaded {
		return
	}
	a.loaded = true
	if !regularBrowserArchiveFile(a.transcript) || !regularBrowserArchiveFile(a.path) {
		return
	}
	f, err := os.Open(a.path)
	if err != nil {
		return
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, browserIconArchiveFileLimit+1))
	if err != nil || len(raw) > browserIconArchiveFileLimit {
		return
	}
	var disk browserIconArchiveDisk
	if json.Unmarshal(raw, &disk) != nil || disk.Version != 1 || disk.Binding != a.binding || len(disk.Icons) > browserIconArchiveEntries {
		return
	}
	seen, total := map[string]bool{}, 0
	icons := make([]nativeBrowserIcon, 0, len(disk.Icons))
	for _, entry := range disk.Icons {
		icon, size, valid := validArchivedBrowserIcon(entry)
		if !valid || seen[icon.URL] || total+size > browserIconArchiveBytes {
			return // A malformed store is not a partial trusted archive.
		}
		seen[icon.URL] = true
		total += size
		icons = append(icons, icon)
	}
	a.icons = icons
}

func (a *browserIconArchive) read() []nativeBrowserIcon {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !regularBrowserArchiveFile(a.transcript) {
		return []nativeBrowserIcon{}
	}
	a.load()
	return append([]nativeBrowserIcon{}, a.icons...)
}

// Invalid/empty observations never erase a previously observed site icon.
// Unchanged live state does no disk I/O beyond this holder's initial load.
func (a *browserIconArchive) remember(entries []nativeBrowserIcon) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.load()
	changed := false
	for _, entry := range entries {
		icon, _, valid := validArchivedBrowserIcon(entry)
		if !valid {
			continue
		}
		at := -1
		for index, previous := range a.icons {
			if previous.URL == icon.URL {
				at = index
				if previous.Favicon == icon.Favicon {
					at = -2
				}
				break
			}
		}
		if at == -2 {
			continue
		}
		if at >= 0 {
			a.icons = append(a.icons[:at], a.icons[at+1:]...)
		}
		a.icons = append(a.icons, icon)
		changed = true
	}
	a.dirty = a.dirty || changed
	total := 0
	for _, icon := range a.icons {
		_, size, _ := validArchivedBrowserIcon(icon)
		total += size
	}
	for len(a.icons) > browserIconArchiveEntries || total > browserIconArchiveBytes {
		_, size, _ := validArchivedBrowserIcon(a.icons[0])
		total -= size
		a.icons = a.icons[1:]
	}
	if !a.dirty || time.Now().Before(a.retryAfter) {
		return
	}
	a.retryAfter = time.Now().Add(browserIconArchiveRetryDelay)
	if !regularBrowserArchiveFile(a.transcript) {
		return
	}
	if info, err := os.Lstat(a.path); err == nil && !info.Mode().IsRegular() {
		return
	}
	raw, err := json.Marshal(browserIconArchiveDisk{Version: 1, Binding: a.binding, Icons: a.icons})
	if err != nil || len(raw) > browserIconArchiveFileLimit {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(a.path), ".browser-icons-*")
	if err != nil {
		return
	}
	name := f.Name()
	defer os.Remove(name)
	if f.Chmod(0o600) != nil {
		f.Close()
		return
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil && regularBrowserArchiveFile(a.transcript) {
		if os.Rename(name, a.path) == nil {
			a.dirty = false
			a.retryAfter = time.Time{}
		}
	}
}

func (b *nativeBrowser) captureIcons(icons []nativeBrowserIcon) {
	b.mu.Lock()
	capture, closed := b.rememberIcons, b.closed
	b.mu.Unlock()
	if !closed && capture != nil && len(icons) > 0 {
		capture(icons)
	}
}

func (b *nativeBrowser) captureStateIcons(state map[string]json.RawMessage) {
	var icons []nativeBrowserIcon
	_ = json.Unmarshal(state["tabs"], &icons)
	var selected nativeBrowserIcon
	_ = json.Unmarshal(state["url"], &selected.URL)
	_ = json.Unmarshal(state["favicon"], &selected.Favicon)
	if selected.Favicon != "" {
		icons = append(icons, selected)
	}
	b.captureIcons(icons)
}

func (s *Server) handleBrowserIcons(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sid")
	if !validID(id) || !s.ownsSession(r, id) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	transcript := s.mgr.transcriptPath(id)
	if !regularBrowserArchiveFile(transcript) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	var owner *identity.Scope
	if s.mgr.cfg.Catalog != nil {
		if record, err := s.mgr.cfg.Catalog.OwnerOf(r.Context(), id); err == nil {
			scope := record.Scope
			owner = &scope
		}
	}
	archive := newBrowserIconArchive(transcript, owner)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"icons": archive.read()})
}
