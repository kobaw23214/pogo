package web

// Workspace file API behind the file panel: directory listings,
// file contents, and a path index for quick-open. Every path goes through
// tools.Resolve, the same canonical escape check the agent's own tools get —
// the browser must not see further than the model.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yosukeno/pogo/tools"
)

const (
	// maxContentBytes and maxContentLines bound a preview: enough to read a
	// source file, not enough to wedge the tab on a generated monster.
	maxContentBytes = 256 << 10
	maxContentLines = 5000
	// maxListEntries keeps `ls node_modules`-shaped directories from
	// serialising thousands of names nobody scrolls through.
	maxListEntries = 500
	// maxIndexEntries bounds the quick-open index; beyond it the fuzzy search
	// degrades to "type a longer query", which is the right nudge anyway.
	maxIndexEntries = 20000
)

// listSkip is hidden from directory listings. .git is noise for a file panel
// (thousands of objects nobody browses), not a security boundary — the escape
// check is the boundary.
var listSkip = map[string]bool{".git": true}

// indexSkip additionally drops dependency and build-output directories from
// the quick-open index: they would drown the project's own files. This is a
// built-in noise list, deliberately not .gitignore parsing — stdlib-only, and
// the tree still lists everything for the rare excursion into node_modules.
var indexSkip = map[string]bool{".git": true, "node_modules": true, "dist": true}

type fileEntry struct {
	Name    string `json:"name"`
	Dir     bool   `json:"dir"`
	Size    int64  `json:"size"`
	MtimeMS int64  `json:"mtime_ms"`
}

// resolvePanelPath answers the path query parameter or rejects the request.
func (s *Server) resolvePanelPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	rel := r.URL.Query().Get("path")
	if rel == "" {
		rel = "."
	}
	abs, err := tools.Resolve(s.requestRoot(r), s.panelRelativePath(r, rel))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return "", false
	}
	return abs, true
}

func (s *Server) panelRelativePath(r *http.Request, rel string) string {
	// Multi-user path de-doubling: the panel sends session-cwd-relative paths
	// (e.g. "users/<key>/sub"), because that is what the workspace field
	// carries — a value computed against the SERVER root. But requestRoot for
	// an owned scope is already the user root (<cwd>/users/<key>), so without
	// stripping, Resolve would answer <root>/users/<key>/users/<key>/… and
	// every panel listing 404s. Strip the scope root's own cwd-relative
	// prefix when the caller sent it; scope-relative paths pass through.
	if scope, ok := s.ownerScope(r); ok {
		if userRel, isRoot := s.mgr.UserRootRel(scope); isRoot {
			for _, p := range []string{userRel + "/", userRel} {
				if rel == p {
					rel = "."
					break
				}
				if strings.HasPrefix(rel, p+"/") {
					rel = strings.TrimPrefix(rel, p+"/")
					break
				}
			}
		}
	}
	return rel
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	abs, ok := s.resolvePanelPath(w, r)
	if !ok {
		return
	}
	info, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		writeErr(w, http.StatusNotFound, err)
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err)
		return
	case !info.IsDir():
		writeErr(w, http.StatusBadRequest, errors.New("not a directory: use /api/files/content"))
		return
	}

	dirents, err := os.ReadDir(abs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	entries := make([]fileEntry, 0, min(len(dirents), maxListEntries))
	truncated := false
	for _, d := range dirents {
		if listSkip[d.Name()] {
			continue
		}
		if len(entries) >= maxListEntries {
			truncated = true
			break
		}
		e := fileEntry{Name: d.Name(), Dir: d.IsDir()}
		// Info follows symlinks: a link to a directory opens like one, same as
		// the ls tool's marker. A failed stat just leaves zeroes — a broken
		// link is still worth listing.
		if fi, err := d.Info(); err == nil {
			e.Size = fi.Size()
			e.MtimeMS = fi.ModTime().UnixMilli()
			e.Dir = fi.IsDir()
		}
		entries = append(entries, e)
	}
	// Directories first, then case-insensitive alphabetical with a
	// case-sensitive tiebreak — GitHub's order, and a stable one.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir
		}
		a, b := strings.ToLower(entries[i].Name), strings.ToLower(entries[j].Name)
		if a == b {
			return entries[i].Name < entries[j].Name
		}
		return a < b
	})

	rel := filepath.Clean(r.URL.Query().Get("path"))
	writeJSON(w, http.StatusOK, map[string]any{
		"path":      filepath.ToSlash(rel),
		"entries":   entries,
		"truncated": truncated,
	})
}

func (s *Server) handleFileContent(w http.ResponseWriter, r *http.Request) {
	abs, ok := s.resolvePanelPath(w, r)
	if !ok {
		return
	}
	f, err := os.Open(abs)
	if errors.Is(err, fs.ErrNotExist) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if info.IsDir() {
		writeErr(w, http.StatusBadRequest, errors.New("is a directory: use /api/files"))
		return
	}

	if r.URL.Query().Get("raw") == "1" {
		s.serveRaw(w, r, f, info)
		return
	}

	// One bounded read covers both the binary sniff and the text preview.
	head, err := io.ReadAll(io.LimitReader(f, maxContentBytes+1))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, filePreview(r.URL.Query().Get("path"), head, info))
}

// filePreview shares the bounded text/binary response shape with private files.
func filePreview(rel string, head []byte, info os.FileInfo) map[string]any {
	truncated := len(head) > maxContentBytes
	if truncated {
		head = head[:maxContentBytes]
	}

	sniff := head
	if len(sniff) > 8<<10 {
		sniff = sniff[:8<<10]
	}
	mime := http.DetectContentType(sniff)
	if strings.IndexByte(string(sniff), 0) >= 0 || !strings.HasPrefix(mime, "text/") {
		return map[string]any{
			"path":   rel,
			"binary": true,
			"mime":   mime,
			"size":   info.Size(),
		}
	}

	text := string(head)
	by := ""
	if lines := strings.Split(text, "\n"); len(lines) > maxContentLines {
		text = strings.Join(lines[:maxContentLines], "\n")
		by = "lines"
	} else if truncated {
		by = "bytes"
	}
	return map[string]any{
		"path":         rel,
		"text":         text,
		"truncated":    by != "",
		"truncated_by": by,
		"size":         info.Size(),
		"mtime_ms":     info.ModTime().UnixMilli(),
	}
}

// serveRaw streams a file for previews. Inline serving stays limited to
// sniffed images, PDFs, audio and video that the browser renders as media,
// never as HTML: the response rides on this origin, where a served HTML or
// SVG-forged page would run with access to the stored token. nosniff plus the
// sniffed type closes that branch; the sandbox CSP takes away any origin reach
// a viewer might expose, and ServeContent supports bounded range streaming.
//
// download=1 is the other direction: the attachment disposition makes the
// browser save instead of render, whatever the bytes are, which is what makes
// it safe to hand out docx/xlsx and any other binary through the same door —
// the preview tab's download button.
func (s *Server) serveRaw(w http.ResponseWriter, r *http.Request, f *os.File, info os.FileInfo) {
	serveRawFile(w, r, f, info)
}

func serveRawFile(w http.ResponseWriter, r *http.Request, f io.ReadSeeker, info os.FileInfo) {
	head := make([]byte, 512)
	n, _ := f.Read(head)
	mime := http.DetectContentType(head[:n])
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Get("download") == "1" {
		if !strings.HasPrefix(mime, "image/") && mime != "application/pdf" && !strings.HasPrefix(mime, "text/") {
			mime = "application/octet-stream"
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Content-Disposition", attachmentDisposition(info.Name()))
		http.ServeContent(w, r, info.Name(), info.ModTime(), f)
		return
	}
	switch {
	case strings.HasPrefix(mime, "image/"):
	case mime == "application/pdf":
		w.Header().Set("Content-Type", "application/pdf")
		// allow-scripts is for the browser's own PDF viewer implementation
		// (pdf.js needs it); the document itself is never parsed as HTML
		// (nosniff + application/pdf), so no page script can run here.
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts")
		w.Header().Set("Content-Disposition", "inline")
	case strings.HasPrefix(mime, "audio/") || strings.HasPrefix(mime, "video/") || mime == "application/ogg":
		if mime == "application/ogg" {
			mime = "audio/ogg"
		}
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("Content-Disposition", "inline")
	default:
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("raw inline preview is limited to images, PDFs, audio and video"))
		return
	}
	w.Header().Set("Content-Type", mime)
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

// attachmentDisposition quotes a download filename per RFC 6266: an ASCII
// fallback that is always safe to quote, plus filename* for the real name
// (uploads keep their original, often non-ASCII, spelling).
func attachmentDisposition(name string) string {
	ascii := strings.Map(func(r rune) rune {
		switch {
		case r >= 0x20 && r < 0x7f && r != '"' && r != '\\':
			return r
		default:
			return '_'
		}
	}, name)
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", ascii, url.PathEscape(name))
}

func (s *Server) handleFileIndex(w http.ResponseWriter, r *http.Request) {
	root := s.requestRoot(r)
	paths := make([]string, 0, 1024)
	capped := false
	// WalkDir does not follow symlinked directories, so the index cannot
	// escape the root through links and cannot loop.
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable corners are not worth failing the index over
		}
		if d.IsDir() {
			if indexSkip[d.Name()] && p != root {
				return filepath.SkipDir
			}
			return nil
		}
		if len(paths) >= maxIndexEntries {
			capped = true
			return fs.SkipAll
		}
		rel, err := filepath.Rel(root, p)
		if err == nil {
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(paths)
	writeJSON(w, http.StatusOK, map[string]any{
		"paths":  paths,
		"capped": capped,
	})
}

// handleFileMkdir creates one directory under the workspace root, for the
// workspace picker's "new folder" button. Same Resolve sandbox as the other
// file handlers, and single-level on purpose: a missing parent is a 404, not
// a reason to silently materialise a whole path — that is Finder's model too.
func (s *Server) handleFileMkdir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	abs, err := tools.Resolve(s.requestRoot(r), body.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	switch err := os.Mkdir(abs, 0o755); {
	case err == nil:
		writeJSON(w, http.StatusCreated, map[string]any{"path": filepath.ToSlash(body.Path)})
	case errors.Is(err, fs.ErrExist):
		writeErr(w, http.StatusConflict, errors.New("already exists: "+body.Path))
	case errors.Is(err, fs.ErrNotExist):
		writeErr(w, http.StatusNotFound, errors.New("parent directory does not exist: "+body.Path))
	default:
		writeErr(w, http.StatusInternalServerError, err)
	}
}

// handleFileSave replaces a file's content at the user's own hand, from the
// file panel. It deliberately does NOT pass through the approval policy: the
// gate guards the model, and this request can only come from the token holder
// on loopback — the user. The path still goes through the same Resolve
// sandbox, and the write is journaled like an agent's, so the workspace
// changes view shows it.
type fileSaveRequest struct {
	Path        string `json:"path"`
	Text        string `json:"text"`
	BaseMtimeMS int64  `json:"base_mtime_ms"`
	Force       bool   `json:"force"`
}

func (s *Server) handleFileSave(w http.ResponseWriter, r *http.Request) {
	var body fileSaveRequest
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.ContainsRune(body.Text, 0) {
		writeErr(w, http.StatusBadRequest, errors.New("text contains NUL: this editor is for text files"))
		return
	}
	abs, err := tools.Resolve(s.requestRoot(r), body.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	_ = tools.WithFileMutationLock(abs, func() error {
		s.saveFileContentLocked(w, r, abs, body)
		return nil
	})
}

func (s *Server) saveFileContentLocked(w http.ResponseWriter, r *http.Request, abs string, body fileSaveRequest) {
	info, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		writeErr(w, http.StatusNotFound, err)
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err)
		return
	case info.IsDir():
		writeErr(w, http.StatusBadRequest, errors.New("is a directory"))
		return
	}

	// One bounded read covers the binary sniff and the journal pre-image; past
	// 2MB the journal marks the base as unkept, which is the honest answer.
	current, err := readHead(abs, 2<<20)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	sniff := current
	if len(sniff) > 8<<10 {
		sniff = sniff[:8<<10]
	}
	if strings.IndexByte(string(sniff), 0) >= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("binary files are not editable here"))
		return
	}

	// Optimistic concurrency: the file must not have moved since the preview
	// read it. 409 carries the current mtime so the client can offer an
	// informed override rather than a blind one.
	if !body.Force && info.ModTime().UnixMilli() != body.BaseMtimeMS {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":            "file changed on disk since it was read",
			"current_mtime_ms": info.ModTime().UnixMilli(),
		})
		return
	}

	s.mgr.Journal().BeforeChange(abs, current, true)

	// Atomic replace preserving the permission bits: a temp file in the same
	// directory (so the same filesystem), then rename over the original.
	tmp, err := os.CreateTemp(filepath.Dir(abs), "."+filepath.Base(abs)+".tmp-*")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename lands
	if _, err := tmp.WriteString(body.Text); err != nil {
		tmp.Close()
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := tmp.Close(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := os.Rename(tmpName, abs); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	newInfo, err := os.Stat(abs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":     body.Path,
		"size":     newInfo.Size(),
		"mtime_ms": newInfo.ModTime().UnixMilli(),
	})
}

func readHead(abs string, limit int64) ([]byte, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}
