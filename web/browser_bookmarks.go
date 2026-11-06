package web

// Pogo bookmarks belong to the authenticated account and, in Studio, one
// explicit Agent. They never fall back to another Agent or the legacy account
// library. They are not Chromium profile data. The human UI writes this store;
// the browser tool only reads it. Keeping it outside the Agent workspace prevents a page or file
// tool from silently replacing the account's saved navigation targets.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/tools"
)

const (
	browserBookmarksMaxBody  = 2 << 20
	browserBookmarksMaxCount = 256
	browserBookmarksMaxTitle = 120
	browserBookmarksMaxURL   = 4096
	browserBookmarksMaxID    = 160
	// Revisions are also read by JavaScript clients; stay inside its exact
	// integer range rather than returning a rounded compare-and-swap value.
	browserBookmarksMaxRevision int64 = 1<<53 - 1
)

var errBrowserBookmarksConflict = errors.New("bookmarks changed in another window; reload before saving")

// All operations re-read the bounded file inside this lock, including CAS.
// No session keeps a stale copy, so a newly saved bookmark is immediately
// available to the account's other sessions and the Agent's next query.
var browserBookmarksMu sync.Mutex

type browserBookmark struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

type browserBookmarkDocument struct {
	Version   int               `json:"version"`
	Revision  int64             `json:"revision"`
	Bookmarks []browserBookmark `json:"bookmarks"`
}

type browserBookmarkView struct {
	Source           string            `json:"source"`
	Scope            string            `json:"scope"`
	ObservationScope string            `json:"observation_scope"`
	AccountID        string            `json:"account_id"`
	OwnerAccountID   string            `json:"owner_account_id"`
	ScopeID          string            `json:"scope_id"`
	AgentID          string            `json:"agent_id,omitempty"`
	Status           string            `json:"status"`
	Revision         int64             `json:"revision"`
	Bookmarks        []browserBookmark `json:"bookmarks"`
	Total            int               `json:"total"`
	Matched          int               `json:"matched"`
	Query            string            `json:"query,omitempty"`
	Offset           int               `json:"offset"`
	Limit            int               `json:"limit,omitempty"`
	NextOffset       *int              `json:"next_offset,omitempty"`
}

func browserBookmarkAccountID(owner *identity.Scope, agentIDs ...string) (string, error) {
	if owner != nil && !owner.Valid() {
		return "", errors.New("bookmark ownership is unavailable")
	}
	// Include every scope dimension. A nil owner has its own explicit
	// single-user namespace and cannot collide with any local account.
	raw, err := json.Marshal(struct {
		Version string          `json:"version"`
		Owner   *identity.Scope `json:"owner"`
	}{"pogo-bookmarks-v1", owner})
	if err != nil {
		return "", err
	}
	if len(agentIDs) > 1 {
		return "", errors.New("invalid bookmark Agent scope")
	}
	if len(agentIDs) == 1 && agentIDs[0] != "" {
		if !validAgentDirName(agentIDs[0]) {
			return "", errors.New("invalid bookmark Agent scope")
		}
		raw, err = json.Marshal(struct {
			Version string          `json:"version"`
			Owner   *identity.Scope `json:"owner"`
			Agent   string          `json:"agent"`
		}{"pogo-bookmarks-agent-v1", owner, agentIDs[0]})
		if err != nil {
			return "", err
		}
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func browserBookmarkPath(dir string, owner *identity.Scope, agentIDs ...string) (string, error) {
	if dir == "" {
		return "", errors.New("bookmark storage is unavailable")
	}
	account, err := browserBookmarkAccountID(owner, agentIDs...)
	if err != nil {
		return "", err
	}
	if len(agentIDs) == 1 && agentIDs[0] != "" {
		return filepath.Join(dir, "browser-bookmarks", "agents", account+".json"), nil
	}
	return filepath.Join(dir, "browser-bookmarks", account+".json"), nil
}

func printableBookmarkValue(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// UI address parsing converts internationalized hostnames to their ASCII
// form before saving. Validate that canonical HTTP host here too: net/url
// alone accepts commas, invalid IPv6 and ports that Chromium cannot open.
func validBrowserBookmarkHost(u *url.URL) bool {
	host := strings.TrimSuffix(u.Hostname(), ".")
	if host == "" || len(host) > 253 {
		return false
	}
	if port := u.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 0 || value > 65535 {
			return false
		}
	}
	if strings.Contains(host, ":") {
		return net.ParseIP(host) != nil
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	// A numeric final label is interpreted by the browser as an IPv4 address.
	// Saved UI addresses already use its canonical four-octet spelling.
	last := labels[len(labels)-1]
	if strings.Trim(last, "0123456789") == "" && net.ParseIP(host) == nil {
		return false
	}
	return true
}

func validateBrowserBookmarks(input []browserBookmark) ([]browserBookmark, error) {
	if input == nil || len(input) > browserBookmarksMaxCount {
		return nil, errors.New("bookmarks must be an array of at most 256 entries")
	}
	out := make([]browserBookmark, 0, len(input))
	ids := make(map[string]bool, len(input))
	for _, entry := range input {
		entry.Title, entry.URL = strings.TrimSpace(entry.Title), strings.TrimSpace(entry.URL)
		if entry.ID == "" || len(entry.ID) > browserBookmarksMaxID || strings.TrimSpace(entry.ID) != entry.ID || !printableBookmarkValue(entry.ID) || ids[entry.ID] {
			return nil, errors.New("bookmark IDs must be distinct non-empty strings of at most 160 bytes")
		}
		if entry.Title == "" || utf8.RuneCountInString(entry.Title) > browserBookmarksMaxTitle || !printableBookmarkValue(entry.Title) {
			return nil, errors.New("bookmark titles must contain 1 to 120 characters without control characters")
		}
		if entry.URL == "" || len(entry.URL) > browserBookmarksMaxURL || !printableBookmarkValue(entry.URL) || strings.IndexFunc(entry.URL, unicode.IsSpace) >= 0 {
			return nil, errors.New("bookmark URLs must be absolute HTTP(S) addresses of at most 4096 bytes")
		}
		u, err := url.Parse(entry.URL)
		if err != nil || u == nil || u.Hostname() == "" || u.Opaque != "" || u.User != nil || u.Scheme != "http" && u.Scheme != "https" || !validBrowserBookmarkHost(u) {
			return nil, errors.New("bookmark URLs must be absolute HTTP(S) addresses without embedded credentials")
		}
		ids[entry.ID] = true
		out = append(out, entry)
	}
	return out, nil
}

func readBrowserBookmarkFile(path string) (browserBookmarkView, error) {
	view := browserBookmarkView{Source: "pogo_bookmarks", Scope: "account", ObservationScope: "pogo_workspace", AccountID: strings.TrimSuffix(filepath.Base(path), ".json"), ScopeID: strings.TrimSuffix(filepath.Base(path), ".json"), Status: "not_initialized", Bookmarks: []browserBookmark{}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return view, nil
	}
	if err != nil {
		return browserBookmarkView{}, errors.New("saved bookmarks could not be read; the existing file has been preserved")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, browserBookmarksMaxBody+1))
	if err != nil || len(raw) > browserBookmarksMaxBody {
		return browserBookmarkView{}, errors.New("saved bookmarks exceed the read limit or could not be read; the existing file has been preserved")
	}
	var doc browserBookmarkDocument
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var extra any
	if dec.Decode(&doc) != nil || dec.Decode(&extra) != io.EOF || doc.Version != 1 || doc.Revision < 1 || doc.Revision > browserBookmarksMaxRevision {
		return browserBookmarkView{}, errors.New("saved bookmarks are invalid; the existing file has been preserved")
	}
	entries, err := validateBrowserBookmarks(doc.Bookmarks)
	if err != nil {
		return browserBookmarkView{}, errors.New("saved bookmarks are invalid; the existing file has been preserved")
	}
	view.Status, view.Revision, view.Bookmarks = "ready", doc.Revision, entries
	view.Total, view.Matched = len(entries), len(entries)
	return view, nil
}

func (m *Manager) browserBookmarks(owner *identity.Scope, query string, agentIDs ...string) (browserBookmarkView, error) {
	agentID, err := m.bookmarkAgentID(agentIDs)
	if err != nil {
		return browserBookmarkView{}, err
	}
	path, err := browserBookmarkPath(m.cfg.SessionDir, owner, agentID)
	if err != nil {
		return browserBookmarkView{}, err
	}
	if !printableBookmarkValue(query) {
		return browserBookmarkView{}, errors.New("bookmark query must contain at most 200 characters without control characters")
	}
	query = strings.TrimSpace(query)
	if utf8.RuneCountInString(query) > tools.MaxBrowserBookmarkQueryRunes {
		return browserBookmarkView{}, errors.New("bookmark query must contain at most 200 characters without control characters")
	}
	browserBookmarksMu.Lock()
	view, err := readBrowserBookmarkFile(path)
	view = bookmarkScopedView(view, agentID, owner)
	browserBookmarksMu.Unlock()
	if err != nil {
		return browserBookmarkView{}, err
	}
	if query != "" {
		needle := strings.ToLower(query)
		matches := make([]browserBookmark, 0)
		for _, entry := range view.Bookmarks {
			if strings.Contains(strings.ToLower(entry.Title), needle) || strings.Contains(strings.ToLower(entry.URL), needle) {
				matches = append(matches, entry)
			}
		}
		view.Bookmarks, view.Matched, view.Query = matches, len(matches), query
	}
	return view, nil
}

func (m *Manager) replaceBrowserBookmarks(owner *identity.Scope, revision int64, entries []browserBookmark, agentIDs ...string) (browserBookmarkView, error) {
	agentID, err := m.bookmarkAgentID(agentIDs)
	if err != nil {
		return browserBookmarkView{}, err
	}
	path, err := browserBookmarkPath(m.cfg.SessionDir, owner, agentID)
	if err != nil {
		return browserBookmarkView{}, err
	}
	if revision < 0 || revision >= browserBookmarksMaxRevision {
		return browserBookmarkView{}, errors.New("bookmark revision is outside the supported range")
	}
	entries, err = validateBrowserBookmarks(entries)
	if err != nil {
		return browserBookmarkView{}, err
	}
	browserBookmarksMu.Lock()
	defer browserBookmarksMu.Unlock()
	current, err := readBrowserBookmarkFile(path)
	current = bookmarkScopedView(current, agentID, owner)
	if err != nil {
		return browserBookmarkView{}, err
	}
	if current.Revision != revision {
		return current, errBrowserBookmarksConflict
	}
	var encoded bytes.Buffer
	enc := json.NewEncoder(&encoded)
	// This is a private JSON file, never embedded HTML. Keeping URL ampersands
	// literal avoids expanding otherwise valid bounded addresses sixfold.
	enc.SetEscapeHTML(false)
	err = enc.Encode(browserBookmarkDocument{Version: 1, Revision: revision + 1, Bookmarks: entries})
	if err != nil || encoded.Len() > browserBookmarksMaxBody {
		return browserBookmarkView{}, errors.New("bookmark set exceeds the storage limit")
	}
	if err := writePrivateFileAtomic(path, encoded.Bytes()); err != nil {
		return browserBookmarkView{}, errors.New("bookmarks could not be saved; the previous revision has been preserved")
	}
	return bookmarkScopedView(browserBookmarkView{Source: "pogo_bookmarks", Scope: "account", ObservationScope: "pogo_workspace", AccountID: current.AccountID, Status: "ready", Revision: revision + 1, Bookmarks: entries, Total: len(entries), Matched: len(entries)}, agentID, owner), nil
}

// browserBookmarkReply is session-bound by its trusted owner at construction.
// It neither creates a worker nor touches a page, and carries no webpage
// evidence, screenshot or page URL. A saved address is a navigation target;
// its current content is not verified by reading this store.
func (m *Manager) browserBookmarkReply(owner *identity.Scope, query string, limit, offset int, agentIDs ...string) (tools.BrowserReply, error) {
	if limit == 0 {
		limit = tools.DefaultBrowserBookmarkLimit
	}
	if limit < 1 || limit > tools.MaxBrowserBookmarkLimit || offset < 0 {
		return tools.BrowserReply{}, errors.New("bookmark page requires limit 1 to 50 and a non-negative offset")
	}
	view, err := m.browserBookmarks(owner, query, agentIDs...)
	if err != nil {
		return tools.BrowserReply{}, err
	}
	view.Limit, view.Offset = limit, offset
	if offset >= len(view.Bookmarks) {
		view.Bookmarks = []browserBookmark{}
	} else {
		end := offset + min(limit, len(view.Bookmarks)-offset)
		if end < len(view.Bookmarks) {
			view.NextOffset = &end
		}
		view.Bookmarks = view.Bookmarks[offset:end]
	}
	raw, err := json.Marshal(view)
	if err != nil {
		return tools.BrowserReply{}, err
	}
	return tools.BrowserReply{OK: true, Data: raw}, nil
}

func (s *Server) browserBookmarkRoutes() {
	s.mux.HandleFunc("GET /api/browser/bookmarks", s.handleBrowserBookmarks)
	s.mux.HandleFunc("PUT /api/browser/bookmarks", s.handlePutBrowserBookmarks)
}

func (s *Server) browserBookmarkOwner(r *http.Request) *identity.Scope {
	if owner, ok := s.ownerScope(r); ok {
		return &owner
	}
	return nil
}

func (s *Server) handleBrowserBookmarks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	agentID, ok := s.browserBookmarkRequestAgent(w, r)
	if !ok {
		return
	}
	view, err := s.mgr.browserBookmarks(s.browserBookmarkOwner(r), "", agentID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handlePutBrowserBookmarks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	agentID, ok := s.browserBookmarkRequestAgent(w, r)
	if !ok {
		return
	}
	var input struct {
		AccountID string            `json:"account_id"`
		Revision  *int64            `json:"revision"`
		Bookmarks []browserBookmark `json:"bookmarks"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, browserBookmarksMaxBody))
	dec.DisallowUnknownFields()
	var extra any
	if dec.Decode(&input) != nil || dec.Decode(&extra) != io.EOF || input.Revision == nil || input.AccountID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("bookmark request must contain exactly one object with account_id, revision and bookmarks"))
		return
	}
	owner := s.browserBookmarkOwner(r)
	account, err := browserBookmarkAccountID(owner, agentID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if input.AccountID != account {
		view, err := s.mgr.browserBookmarks(owner, "", agentID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": "bookmark account changed; reload before saving", "code": "BOOKMARK_ACCOUNT_CHANGED", "current": view})
		return
	}
	if *input.Revision < 0 || *input.Revision >= browserBookmarksMaxRevision {
		writeErr(w, http.StatusBadRequest, errors.New("bookmark revision is outside the supported range"))
		return
	}
	if _, err := validateBrowserBookmarks(input.Bookmarks); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	view, err := s.mgr.replaceBrowserBookmarks(owner, *input.Revision, input.Bookmarks, agentID)
	if errors.Is(err, errBrowserBookmarksConflict) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "BOOKMARK_REVISION_CONFLICT", "revision": view.Revision, "current": view})
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// Agent existence is rechecked on every metadata read/write, including an old
// conversation's tool closure. A deleted Agent cannot expose a legacy library.
func (m *Manager) bookmarkAgentID(ids []string) (string, error) {
	if len(ids) > 1 {
		return "", errors.New("invalid bookmark Agent scope")
	}
	if len(ids) == 0 || ids[0] == "" {
		return "", nil
	}
	id := ids[0]
	if !validAgentDirName(id) || m.studio == nil {
		return "", ErrNotFound
	}
	a, ok := m.studio.get(id)
	if !ok || (a.Status != "ready" && a.Status != "draft") {
		return "", ErrNotFound
	}
	return id, nil
}

func bookmarkScopedView(view browserBookmarkView, agentID string, owner *identity.Scope) browserBookmarkView {
	view.ScopeID = view.AccountID
	view.OwnerAccountID, _ = browserBookmarkAccountID(owner)
	if agentID != "" {
		view.Scope = "agent"
		view.AgentID = agentID
	}
	return view
}

func (s *Server) browserBookmarkRequestAgent(w http.ResponseWriter, r *http.Request) (string, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("无效的收藏范围参数"))
		return "", false
	}
	values, present := query["agent_id"]
	if !present {
		return "", true
	}
	if len(values) != 1 || values[0] == "" || !validAgentDirName(values[0]) {
		writeErr(w, http.StatusBadRequest, errors.New("请指定有效的 Agent 收藏范围"))
		return "", false
	}
	id, err := s.mgr.bookmarkAgentID(values)
	if err != nil {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return "", false
	}
	a, exists := s.mgr.studio.get(id)
	if !exists || (a.Status != "ready" && !s.adminRequest(r)) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return "", false
	}
	return id, true
}
