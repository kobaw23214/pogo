package web

// HTML artifacts have their own short-lived read-only capability. They never
// enter the authenticated raw-file endpoint or acquire the application's origin:
// response CSP sandboxing also applies when opened as a top-level remote tab.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/yosukeno/pogo/identity"
)

const (
	htmlPreviewPrefix     = "/api/html-previews/"
	htmlPreviewTTL        = 30 * time.Minute
	htmlPreviewOwnerLimit = 16
	htmlPreviewTotalLimit = 128
	htmlPreviewHTMLLimit  = 8 << 20
	htmlPreviewAssetLimit = 32 << 20
)

type htmlPreviewCapability struct {
	id         string
	token      string
	sessionID  string
	transcript string
	owner      *identity.Scope
	root       *os.Root
	expires    time.Time
	timer      *time.Timer
	live       *htmlPreviewLiveState
}

type htmlPreviewView struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	BrowserURL  string `json:"browser_url"`
	ExpiresAt   string `json:"expires_at"`
	ReadOnly    bool   `json:"read_only"`
	ProjectRoot string `json:"project_root"`
	Path        string `json:"path"`
}

func validHTMLPreviewHex(value string, bytes int) bool {
	if len(value) != bytes*2 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// Narrow authentication/Origin exception: a capability can only read one asset
// route. In particular it never authorizes management, session or file APIs.
func isHTMLPreviewAssetRequest(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
		return false
	}
	if !strings.HasPrefix(r.URL.Path, htmlPreviewPrefix) {
		return false
	}
	remainder := strings.TrimPrefix(r.URL.Path, htmlPreviewPrefix)
	token, asset, found := strings.Cut(remainder, "/")
	return found && validHTMLPreviewHex(token, 32) && validHTMLPreviewRelative(asset, false)
}

func validHTMLPreviewRelative(value string, directory bool) bool {
	if directory && (value == "" || value == ".") {
		return true
	}
	if len(value) == 0 || len(value) > 4096 || !utf8.ValidString(value) || !fs.ValidPath(value) || strings.ContainsAny(value, "\\\x00:") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if strings.HasPrefix(component, ".") || component == "node_modules" || component == "__pycache__" {
			return false
		}
	}
	return true
}

func htmlPreviewRandom(bytes int) (string, error) {
	data := make([]byte, bytes)
	if _, err := rand.Read(data); err != nil {
		return "", errors.New("could not allocate an HTML preview capability")
	}
	return hex.EncodeToString(data), nil
}

// Match the file panel's workspace/service-relative representation, then apply
// the tighter conversation cwd boundary. Resolution is lexical; actual access
// below uses pinned handles and refuses every symlink component.
func (s *Server) htmlPreviewFilePath(r *http.Request, sess *Session, requested string) (string, error) {
	if requested == "" || len(requested) > 4096 || !utf8.ValidString(requested) || strings.ContainsAny(requested, "\\\x00") {
		return "", errors.New("an HTML artifact path is required")
	}
	requested = s.panelRelativePath(r, requested)
	absolute := requested
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(s.requestRoot(r), filepath.FromSlash(requested))
	}
	absolute = filepath.Clean(absolute)
	conversationRelative, err := filepath.Rel(sess.cwd, absolute)
	if err != nil || !filepath.IsLocal(conversationRelative) {
		return "", errors.New("HTML preview must stay inside this conversation's workspace")
	}
	workspaceRelative, err := filepath.Rel(s.requestRoot(r), absolute)
	if err != nil || !filepath.IsLocal(workspaceRelative) || !validHTMLPreviewRelative(filepath.ToSlash(workspaceRelative), true) {
		return "", errors.New("HTML preview path is not available")
	}
	return filepath.ToSlash(workspaceRelative), nil
}

func (s *Server) handleCreateHTMLPreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	var body struct {
		Path        string `json:"path"`
		ProjectRoot string `json:"project_root,omitempty"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid HTML preview request"))
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeErr(w, http.StatusBadRequest, errors.New("HTML preview accepts one JSON object"))
		return
	}
	entry, err := s.htmlPreviewFilePath(r, sess, body.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	extension := strings.ToLower(path.Ext(entry))
	if extension != ".html" && extension != ".htm" {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("HTML preview requires an .html or .htm artifact"))
		return
	}
	project := path.Dir(entry)
	if body.ProjectRoot != "" {
		project, err = s.htmlPreviewFilePath(r, sess, body.ProjectRoot)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	entryRelative, err := filepath.Rel(filepath.FromSlash(project), filepath.FromSlash(entry))
	if err != nil || !filepath.IsLocal(entryRelative) || !validHTMLPreviewRelative(filepath.ToSlash(entryRelative), false) {
		writeErr(w, http.StatusBadRequest, errors.New("HTML artifact must be inside its preview project"))
		return
	}
	workspace, err := openHTMLPreviewWorkspace(s.requestRoot(r))
	if err != nil {
		htmlPreviewFileError(w, err)
		return
	}
	root, err := openHTMLPreviewDirectory(workspace, project)
	workspace.Close()
	if err != nil {
		htmlPreviewFileError(w, err)
		return
	}
	file, info, err := openHTMLPreviewFile(root, filepath.ToSlash(entryRelative))
	if err != nil {
		root.Close()
		htmlPreviewFileError(w, err)
		return
	}
	file.Close()
	if info.Size() > htmlPreviewHTMLLimit {
		root.Close()
		writeErr(w, http.StatusRequestEntityTooLarge, errors.New("HTML artifact exceeds the 8 MiB preview limit"))
		return
	}
	identifier, err := htmlPreviewRandom(16)
	if err != nil {
		root.Close()
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	token, err := htmlPreviewRandom(32)
	if err != nil {
		root.Close()
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	origin, err := htmlPreviewBrowserOrigin(r)
	if err != nil {
		root.Close()
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	capability := &htmlPreviewCapability{id: identifier, token: token, sessionID: sess.ID, transcript: sess.store.Path(), root: root, expires: time.Now().Add(htmlPreviewTTL)}
	capability.live = newHTMLPreviewLiveState(filepath.ToSlash(entryRelative), info)
	if sess.owner != nil {
		owner := *sess.owner
		capability.owner = &owner
	}
	s.htmlPreviewMu.Lock()
	if s.htmlPreviewClosed {
		s.htmlPreviewMu.Unlock()
		root.Close()
		writeErr(w, http.StatusServiceUnavailable, errors.New("HTML previews are closed"))
		return
	}
	if s.htmlPreviews == nil {
		s.htmlPreviews = make(map[string]*htmlPreviewCapability)
	}
	ownerCount := 0
	for _, active := range s.htmlPreviews {
		if sameBrowserAuthoringOwner(active.owner, capability.owner) {
			ownerCount++
		}
	}
	if len(s.htmlPreviews) >= htmlPreviewTotalLimit || ownerCount >= htmlPreviewOwnerLimit {
		s.htmlPreviewMu.Unlock()
		root.Close()
		writeErr(w, http.StatusTooManyRequests, errors.New("close an existing HTML preview before opening another"))
		return
	}
	s.htmlPreviews[token] = capability
	capability.timer = time.AfterFunc(htmlPreviewTTL, func() { s.revokeHTMLPreview(token) })
	s.htmlPreviewMu.Unlock()
	relativeURL := htmlPreviewPrefix + token + "/" + escapeHTMLPreviewPath(filepath.ToSlash(entryRelative))
	writeJSON(w, http.StatusCreated, htmlPreviewView{ID: identifier, URL: relativeURL, BrowserURL: origin + relativeURL, ExpiresAt: capability.expires.UTC().Format(time.RFC3339Nano), ReadOnly: true, ProjectRoot: project, Path: entry})
}

func escapeHTMLPreviewPath(relative string) string {
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func htmlPreviewRequestOrigin(r *http.Request) (string, error) {
	if r.Host == "" || strings.ContainsAny(r.Host, "\r\n\t /\\'\";@%?#") {
		return "", errors.New("invalid HTML preview request authority")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	parsed, err := url.Parse(scheme + "://" + r.Host)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.Path != "" {
		return "", errors.New("invalid HTML preview request authority")
	}
	if host := parsed.Hostname(); net.ParseIP(host) == nil {
		for _, character := range host {
			if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '-' || character == '_') {
				return "", errors.New("invalid HTML preview request authority")
			}
		}
	}
	return parsed.String(), nil
}

func htmlPreviewBrowserOrigin(r *http.Request) (string, error) {
	// The HTTP connection can arrive on a private proxy-only network that the
	// native browser does not join. Prefer the operator's browser-reachable
	// service origin; forwarded headers and the viewer's localhost are not a
	// trustworthy or routable substitute for that explicit address.
	if configured := os.Getenv("POGO_HTML_PREVIEW_BROWSER_ORIGIN"); configured != "" {
		return htmlPreviewConfiguredBrowserOrigin(configured)
	}
	if address, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && address.Network() != "unix" {
		host, port, err := net.SplitHostPort(address.String())
		if err == nil && port != "" {
			ip := net.ParseIP(host)
			if ip != nil && !ip.IsUnspecified() {
				copy := r.Clone(r.Context())
				copy.Host = net.JoinHostPort(host, port)
				return htmlPreviewRequestOrigin(copy)
			}
		}
	}
	return htmlPreviewRequestOrigin(r)
}

func htmlPreviewConfiguredBrowserOrigin(value string) (string, error) {
	invalid := errors.New("invalid POGO_HTML_PREVIEW_BROWSER_ORIGIN; expected an http(s) origin without credentials, path, query or fragment")
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.ContainsAny(value, "?#") {
		return "", invalid
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", invalid
		}
	} else if strings.HasSuffix(parsed.Host, ":") {
		return "", invalid
	}
	request := &http.Request{Host: parsed.Host}
	if _, err := htmlPreviewRequestOrigin(request); err != nil {
		return "", invalid
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func (s *Server) revokeHTMLPreview(token string) {
	s.htmlPreviewMu.Lock()
	defer s.htmlPreviewMu.Unlock()
	if capability := s.htmlPreviews[token]; capability != nil {
		delete(s.htmlPreviews, token)
		if capability.timer != nil {
			capability.timer.Stop()
		}
		capability.live.close()
		capability.root.Close()
	}
}

func (s *Server) closeHTMLPreviews() {
	s.htmlPreviewMu.Lock()
	defer s.htmlPreviewMu.Unlock()
	s.htmlPreviewClosed = true
	for token, capability := range s.htmlPreviews {
		delete(s.htmlPreviews, token)
		if capability.timer != nil {
			capability.timer.Stop()
		}
		capability.live.close()
		capability.root.Close()
	}
}

func (s *Server) handleDeleteHTMLPreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(w, r); !ok {
		return
	}
	id := r.PathValue("previewID")
	if !validHTMLPreviewHex(id, 16) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	owner, _ := s.ownerScope(r)
	s.htmlPreviewMu.Lock()
	var token string
	for key, capability := range s.htmlPreviews {
		ownerMatches := capability.owner == nil && !s.multiuser() || capability.owner != nil && *capability.owner == owner
		if capability.id == id && capability.sessionID == r.PathValue("sid") && ownerMatches {
			token = key
			break
		}
	}
	s.htmlPreviewMu.Unlock()
	if token == "" {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	s.revokeHTMLPreview(token)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) htmlPreviewSessionValid(ctx context.Context, capability *htmlPreviewCapability) bool {
	s.mgr.mu.Lock()
	closed := s.mgr.closed || s.mgr.deleting[capability.sessionID]
	s.mgr.mu.Unlock()
	if closed {
		return false
	}
	if capability.owner != nil {
		if s.mgr.cfg.Catalog == nil {
			return false
		}
		if _, err := s.mgr.cfg.Catalog.Get(ctx, *capability.owner, capability.sessionID); err != nil {
			return false
		}
		s.auth.mu.RLock()
		found := false
		for username := range s.auth.users {
			if identity.LocalScope(username) == *capability.owner {
				found = true
				break
			}
		}
		s.auth.mu.RUnlock()
		if !found {
			return false
		}
	}
	info, err := os.Lstat(capability.transcript)
	return err == nil && info.Mode().IsRegular()
}

func (s *Server) handleHTMLPreviewAsset(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !isHTMLPreviewAssetRequest(r) {
		http.NotFound(w, r)
		return
	}
	token := r.PathValue("capability")
	relative := r.PathValue("asset")
	if !validHTMLPreviewRelative(relative, false) {
		http.NotFound(w, r)
		return
	}
	// Only the private live-control resources need readable generic 404s:
	// after expiry/revocation the sandbox can stop its fallback poll loop.
	// This still grants no file access and never applies to application APIs.
	if relative == htmlPreviewLiveEvents || relative == htmlPreviewLiveRevision {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	}
	s.htmlPreviewMu.Lock()
	capability := s.htmlPreviews[token]
	if capability == nil || !time.Now().Before(capability.expires) || s.htmlPreviewClosed {
		s.htmlPreviewMu.Unlock()
		if capability != nil {
			s.revokeHTMLPreview(token)
		}
		http.NotFound(w, r)
		return
	}
	root, err := capability.root.OpenRoot(".")
	s.htmlPreviewMu.Unlock()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	if !s.htmlPreviewSessionValid(r.Context(), capability) {
		s.revokeHTMLPreview(token)
		http.NotFound(w, r)
		return
	}
	mime, allowed := htmlPreviewAssetMIME(relative)
	if relative == htmlPreviewLiveScript {
		mime, allowed = "text/javascript; charset=utf-8", true
	} else if relative == htmlPreviewLiveEvents {
		mime, allowed = "text/event-stream; charset=utf-8", true
	} else if relative == htmlPreviewLiveRevision {
		mime, allowed = "application/json; charset=utf-8", true
	}
	if !allowed {
		http.NotFound(w, r)
		return
	}
	_, err = htmlPreviewRequestOrigin(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Omit the scheme in CSP host-sources: a TLS-terminating reverse proxy
	// delivers HTTPS to the user while the trusted app listener is HTTP. The
	// browser matches its own document scheme without trusting Forwarded.
	assetRoot := r.Host + htmlPreviewPrefix + token + "/"
	policy := "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline' 'unsafe-eval' blob: " + assetRoot + "; style-src 'unsafe-inline' " + assetRoot + "; img-src data: blob: " + assetRoot + "; font-src data: " + assetRoot + "; media-src data: blob: " + assetRoot + "; connect-src " + assetRoot + "; base-uri 'none'; form-action 'none'; object-src 'none'; frame-src 'none'; worker-src 'none'; manifest-src 'none'; frame-ancestors 'self'"
	w.Header().Set("Content-Security-Policy", policy)
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), clipboard-read=(), clipboard-write=()")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if relative == htmlPreviewLiveEvents {
		s.serveHTMLPreviewEvents(w, r, capability, root)
		return
	}
	if relative == htmlPreviewLiveScript {
		serveHTMLPreviewLiveScript(w, r)
		return
	}
	if relative == htmlPreviewLiveRevision {
		s.serveHTMLPreviewRevision(w, r, capability, root)
		return
	}
	file, info, err := openHTMLPreviewFile(root, relative)
	if err != nil {
		// Remember a safe, supported asset that is not available yet. A later
		// tool write can then invalidate a page whose first CSS/JS load failed.
		capability.live.noteAsset(relative, nil)
		htmlPreviewFileError(w, err)
		return
	}
	defer file.Close()
	maximum := int64(htmlPreviewAssetLimit)
	if strings.HasPrefix(mime, "text/html") {
		maximum = htmlPreviewHTMLLimit
	}
	if info.Size() > maximum {
		writeErr(w, http.StatusRequestEntityTooLarge, errors.New("asset exceeds the HTML preview limit"))
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", "inline")
	revision := capability.live.noteAsset(relative, info)
	if strings.HasPrefix(mime, "text/html") {
		serveHTMLPreviewWithLiveScript(w, r, file, info, token, revision, capability.expires)
		return
	}
	// Bound this response to the size checked above even if an authorized
	// author concurrently appends to the live artifact during ServeContent.
	http.ServeContent(w, r, info.Name(), info.ModTime(), io.NewSectionReader(file, 0, info.Size()))
}

func htmlPreviewAssetMIME(relative string) (string, bool) {
	name := strings.ToLower(path.Base(relative))
	switch name {
	case "users.json", "providers.json", "prefs.json", "mcp.json", "secrets.json", "package.json", "package-lock.json", "tsconfig.json", "composer.json":
		return "", false
	}
	mime := map[string]string{
		".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8",
		".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
		".json": "application/json; charset=utf-8", ".csv": "text/csv; charset=utf-8", ".txt": "text/plain; charset=utf-8",
		".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".avif": "image/avif", ".ico": "image/x-icon", ".svg": "image/svg+xml",
		".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf", ".otf": "font/otf",
		".mp3": "audio/mpeg", ".ogg": "audio/ogg", ".wav": "audio/wav", ".mp4": "video/mp4", ".webm": "video/webm", ".wasm": "application/wasm",
	}[strings.ToLower(path.Ext(relative))]
	return mime, mime != ""
}

func htmlPreviewFileError(w http.ResponseWriter, err error) {
	if errors.Is(err, fs.ErrNotExist) {
		writeErr(w, http.StatusNotFound, errors.New("HTML preview file is unavailable"))
		return
	}
	writeErr(w, http.StatusForbidden, errors.New("HTML preview requires regular files inside its project without symbolic links"))
}

func openHTMLPreviewWorkspace(absolute string) (*os.Root, error) {
	before, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, errors.New("preview workspace is not a directory")
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, err
	}
	opened, statErr := root.Stat(".")
	after, afterErr := os.Lstat(absolute)
	if statErr != nil || afterErr != nil || !after.IsDir() || !os.SameFile(before, opened) || !os.SameFile(before, after) {
		root.Close()
		return nil, errors.New("preview workspace changed while opening")
	}
	return root, nil
}

func openHTMLPreviewDirectory(parent *os.Root, relative string) (*os.Root, error) {
	if !validHTMLPreviewRelative(relative, true) {
		return nil, errors.New("invalid preview directory")
	}
	current, err := parent.OpenRoot(".")
	if err != nil || relative == "" || relative == "." {
		return current, err
	}
	for _, name := range strings.Split(relative, "/") {
		before, statErr := current.Lstat(name)
		if statErr != nil || !before.IsDir() {
			current.Close()
			if statErr != nil {
				return nil, statErr
			}
			return nil, errors.New("preview directory is a link or non-directory")
		}
		next, openErr := current.OpenRoot(name)
		if openErr != nil {
			current.Close()
			return nil, openErr
		}
		opened, openedErr := next.Stat(".")
		after, afterErr := current.Lstat(name)
		current.Close()
		if openedErr != nil || afterErr != nil || !after.IsDir() || !os.SameFile(before, opened) || !os.SameFile(before, after) {
			next.Close()
			return nil, errors.New("preview directory changed while opening")
		}
		current = next
	}
	return current, nil
}

func openHTMLPreviewFile(root *os.Root, relative string) (*os.File, os.FileInfo, error) {
	if !validHTMLPreviewRelative(relative, false) {
		return nil, nil, errors.New("invalid preview asset")
	}
	parent, err := openHTMLPreviewDirectory(root, path.Dir(relative))
	if err != nil {
		return nil, nil, err
	}
	defer parent.Close()
	name := path.Base(relative)
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, nil, errors.New("preview asset is not a regular file")
	}
	file, err := parent.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	opened, statErr := file.Stat()
	after, afterErr := parent.Lstat(name)
	if statErr != nil || afterErr != nil || !opened.Mode().IsRegular() || !after.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(before, after) {
		file.Close()
		return nil, nil, errors.New("preview asset changed while opening")
	}
	return file, opened, nil
}
