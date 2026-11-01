package web

// Operator-only, read-only browsing of the spill tree. This deliberately does
// not use ResultStore.Load: inspecting an artifact must not renew its lease or
// otherwise change when the normal retention policy can remove it.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
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

	"github.com/yosukeno/pogo/tools"
)

const (
	spillBrowsePageMax     = 200
	spillBrowseScanMax     = 100000
	spillBrowsePageScanMax = 4096
	spillBrowseFileMax     = 64 << 10
)

var errSpillBrowseUnsafe = errors.New("spill browsing only supports regular files and directories without symbolic links")

type spillBrowseEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Type       string `json:"type"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modified_at"`
}

type spillBrowseEntries struct {
	Root             string             `json:"root"`
	Path             string             `json:"path"`
	Parent           string             `json:"parent"`
	ReadOnly         bool               `json:"read_only"`
	Exists           bool               `json:"exists"`
	Entries          []spillBrowseEntry `json:"entries"`
	Offset           int                `json:"offset"`
	Limit            int                `json:"limit"`
	NextOffset       *int               `json:"next_offset"`
	Complete         bool               `json:"complete"`
	Truncated        bool               `json:"truncated"`
	Skipped          int                `json:"skipped"`
	ScanLimitReached bool               `json:"scan_limit_reached"`
}

type spillBrowseFile struct {
	Root       string `json:"root"`
	Path       string `json:"path"`
	Name       string `json:"name"`
	ReadOnly   bool   `json:"read_only"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modified_at"`
	Offset     int64  `json:"offset"`
	Bytes      int    `json:"bytes"`
	NextOffset *int64 `json:"next_offset"`
	Complete   bool   `json:"complete"`
	Truncated  bool   `json:"truncated"`
	Binary     bool   `json:"binary"`
	Encoding   string `json:"encoding"`
	Content    string `json:"content"`
}

func spillBrowseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// Only slash-delimited, canonical relative paths are accepted. A path is never
// joined onto an absolute root before access; pinned os.Root handles enforce
// the root boundary even if a directory is renamed while a request runs.
func spillBrowsePath(query url.Values, file bool) (string, error) {
	for key, values := range query {
		if key != "path" && key != "offset" && key != "limit" && key != "token" {
			return "", fmt.Errorf("unknown spill browse parameter %q", key)
		}
		if len(values) != 1 {
			return "", fmt.Errorf("spill browse parameter %q must appear once", key)
		}
	}
	rel := query.Get("path")
	if len(rel) > 4096 || strings.ContainsAny(rel, "\\\x00:") || !utf8.ValidString(rel) {
		return "", errors.New("invalid spill relative path")
	}
	if rel == "" || rel == "." {
		if file {
			return "", errors.New("a spill file path is required")
		}
		return "", nil
	}
	if !fs.ValidPath(rel) || filepath.IsAbs(rel) {
		return "", errors.New("spill path must be a canonical relative path")
	}
	return rel, nil
}

func spillBrowseNumber(query url.Values, key string, fallback, minimum, maximum int64) (int64, error) {
	raw, present := query[key]
	if !present {
		return fallback, nil
	}
	if len(raw) != 1 || raw[0] == "" {
		return 0, fmt.Errorf("invalid spill %s", key)
	}
	n, err := strconv.ParseInt(raw[0], 10, 64)
	if err != nil || n < minimum || n > maximum {
		return 0, fmt.Errorf("spill %s must be between %d and %d", key, minimum, maximum)
	}
	return n, nil
}

func spillBrowseRoot() (string, *os.Root, error) {
	abs, err := filepath.Abs(tools.DefaultSpillDir())
	if err != nil {
		return "", nil, err
	}
	before, err := os.Lstat(abs)
	if err != nil {
		return abs, nil, err
	}
	if !before.IsDir() {
		return abs, nil, errSpillBrowseUnsafe
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return abs, nil, err
	}
	opened, err := root.Stat(".")
	after, afterErr := os.Lstat(abs)
	if err != nil || afterErr != nil || !after.IsDir() || !os.SameFile(before, opened) || !os.SameFile(after, opened) {
		root.Close()
		return abs, nil, errSpillBrowseUnsafe
	}
	return abs, root, nil
}

// Open each directory component separately and compare the inode opened with
// its lstat. In particular, os.Root's in-root symlink support is not exposed by
// this API. Closing the previous handle keeps resource use bounded by depth 1.
func spillBrowseDirectory(root *os.Root, rel string) (*os.Root, error) {
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	if rel == "" {
		return current, nil
	}
	for _, name := range strings.Split(rel, "/") {
		before, statErr := current.Lstat(name)
		if statErr != nil {
			current.Close()
			return nil, statErr
		}
		if !before.IsDir() {
			current.Close()
			return nil, errSpillBrowseUnsafe
		}
		next, openErr := current.OpenRoot(name)
		if openErr != nil {
			current.Close()
			return nil, openErr
		}
		opened, openedErr := next.Stat(".")
		after, afterErr := current.Lstat(name)
		current.Close()
		if openedErr != nil || afterErr != nil || !after.IsDir() || !os.SameFile(before, opened) || !os.SameFile(after, opened) {
			next.Close()
			return nil, errSpillBrowseUnsafe
		}
		current = next
	}
	return current, nil
}

func spillBrowseError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		writeErr(w, http.StatusNotFound, errors.New("spill path no longer exists; refresh the directory"))
	case errors.Is(err, fs.ErrPermission), errors.Is(err, errSpillBrowseUnsafe):
		writeErr(w, http.StatusForbidden, errors.New("spill path is unavailable for read-only browsing"))
	default:
		writeErr(w, http.StatusInternalServerError, errors.New("could not read the spill path"))
	}
}

func (s *Server) handleSpillEntries(w http.ResponseWriter, r *http.Request) {
	spillBrowseHeaders(w)
	query := r.URL.Query()
	rel, err := spillBrowsePath(query, false)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	offset, err := spillBrowseNumber(query, "offset", 0, 0, spillBrowseScanMax)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	limit, err := spillBrowseNumber(query, "limit", 100, 1, spillBrowsePageMax)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	abs, root, err := spillBrowseRoot()
	view := spillBrowseEntries{Root: abs, Path: rel, ReadOnly: true, Entries: []spillBrowseEntry{}, Offset: int(offset), Limit: int(limit)}
	if rel != "" {
		view.Parent = path.Dir(rel)
		if view.Parent == "." {
			view.Parent = ""
		}
	}
	if errors.Is(err, fs.ErrNotExist) && rel == "" {
		view.Complete = true
		writeJSON(w, http.StatusOK, view)
		return
	}
	if err != nil {
		spillBrowseError(w, err)
		return
	}
	defer root.Close()
	dir, err := spillBrowseDirectory(root, rel)
	if err != nil {
		spillBrowseError(w, err)
		return
	}
	defer dir.Close()
	file, err := dir.Open(".")
	if err != nil {
		spillBrowseError(w, err)
		return
	}
	defer file.Close()
	view.Exists = true
	// Offset counts native directory entries, including rejected special files.
	// Never load/sort the complete directory merely to serve a short page.
	for skipped := int64(0); skipped < offset; {
		if r.Context().Err() != nil {
			return
		}
		count := min(int64(256), offset-skipped)
		entries, readErr := file.ReadDir(int(count))
		skipped += int64(len(entries))
		if readErr == io.EOF {
			view.Complete = true
			writeJSON(w, http.StatusOK, view)
			return
		}
		if readErr != nil {
			spillBrowseError(w, readErr)
			return
		}
	}
	position := int(offset)
	for scanned := 0; len(view.Entries) < int(limit) && scanned < spillBrowsePageScanMax && position < spillBrowseScanMax; scanned++ {
		if r.Context().Err() != nil {
			return
		}
		entries, readErr := file.ReadDir(1)
		if readErr == io.EOF {
			view.Complete = true
			break
		}
		if readErr != nil {
			spillBrowseError(w, readErr)
			return
		}
		position++
		name := entries[0].Name()
		info, infoErr := dir.Lstat(name)
		if infoErr != nil || (!info.IsDir() && !info.Mode().IsRegular()) || !utf8.ValidString(name) {
			view.Skipped++
			continue
		}
		kind := "file"
		if info.IsDir() {
			kind = "directory"
		}
		view.Entries = append(view.Entries, spillBrowseEntry{Name: name, Path: path.Join(rel, name), Type: kind, Size: info.Size(), ModifiedAt: info.ModTime().UTC().Format(time.RFC3339Nano)})
	}
	if !view.Complete {
		_, peekErr := file.ReadDir(1)
		if peekErr == io.EOF {
			view.Complete = true
		} else if peekErr != nil {
			spillBrowseError(w, peekErr)
			return
		} else if position >= spillBrowseScanMax {
			view.ScanLimitReached = true
		} else {
			view.NextOffset = &position
		}
	}
	view.Truncated = !view.Complete
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleSpillFile(w http.ResponseWriter, r *http.Request) {
	spillBrowseHeaders(w)
	query := r.URL.Query()
	rel, err := spillBrowsePath(query, true)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	offset, err := spillBrowseNumber(query, "offset", 0, 0, 1<<62)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Four bytes permit any UTF-8 rune, so a text page always makes progress.
	limit, err := spillBrowseNumber(query, "limit", 32<<10, 4, spillBrowseFileMax)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	abs, root, err := spillBrowseRoot()
	if err != nil {
		spillBrowseError(w, err)
		return
	}
	defer root.Close()
	parent := path.Dir(rel)
	if parent == "." {
		parent = ""
	}
	dir, err := spillBrowseDirectory(root, parent)
	if err != nil {
		spillBrowseError(w, err)
		return
	}
	defer dir.Close()
	name := path.Base(rel)
	before, err := dir.Lstat(name)
	if err != nil {
		spillBrowseError(w, err)
		return
	}
	if !before.Mode().IsRegular() {
		spillBrowseError(w, errSpillBrowseUnsafe)
		return
	}
	// NONBLOCK prevents a concurrently substituted FIFO/device from hanging
	// before fstat can reject it. The inode comparisons also reject an in-root
	// symlink swapped in between lstat and the open.
	file, err := dir.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		spillBrowseError(w, err)
		return
	}
	defer file.Close()
	opened, statErr := file.Stat()
	after, afterErr := dir.Lstat(name)
	if statErr != nil || afterErr != nil || !opened.Mode().IsRegular() || !after.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(after, opened) {
		spillBrowseError(w, errSpillBrowseUnsafe)
		return
	}
	if offset > opened.Size() {
		writeErr(w, http.StatusBadRequest, errors.New("spill offset is beyond the end of the file"))
		return
	}
	data := make([]byte, int(min(limit, opened.Size()-offset)))
	n, readErr := file.ReadAt(data, offset)
	if readErr != nil && readErr != io.EOF {
		spillBrowseError(w, readErr)
		return
	}
	if readErr == io.EOF && n < len(data) {
		writeErr(w, http.StatusConflict, errors.New("spill file changed during reading; refresh the preview"))
		return
	}
	data = data[:n]
	complete := offset+int64(n) >= opened.Size() || readErr == io.EOF
	text, binary := spillBrowseText(data, complete)
	view := spillBrowseFile{Root: abs, Path: rel, Name: name, ReadOnly: true, Size: opened.Size(), ModifiedAt: opened.ModTime().UTC().Format(time.RFC3339Nano), Offset: offset, Encoding: "utf-8", Binary: binary}
	if binary {
		view.Bytes = n
	} else {
		view.Bytes = len(text)
		view.Content = string(text)
	}
	next := offset + int64(view.Bytes)
	view.Complete = complete && next >= offset+int64(n)
	view.Truncated = !view.Complete
	if !view.Complete {
		view.NextOffset = &next
	}
	writeJSON(w, http.StatusOK, view)
}

func spillBrowseText(data []byte, complete bool) ([]byte, bool) {
	for i := 0; i < len(data); {
		b := data[i]
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' && b != '\f' && b != '\b' {
			return nil, true
		}
		if !utf8.FullRune(data[i:]) && !complete {
			return data[:i], false
		}
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size == 1 {
			return nil, true
		}
		i += size
	}
	return data, false
}
