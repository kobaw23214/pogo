package web

// Answer links are navigation hints, not a filesystem authority. Resolve the
// exact session-relative path first; only an explicitly requested bare-name
// lookup may search below that same session's workspace.
import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/yosukeno/pogo/tools"
)

type fileLinkCandidate struct {
	Path         string `json:"path"`
	RelativePath string `json:"relative_path"`
	Name         string `json:"name"`
}

type fileLinkResolution struct {
	Status     string              `json:"status"`
	Candidates []fileLinkCandidate `json:"candidates"`
	Complete   bool                `json:"complete"`
}

type fileLinkSearchLimits struct{ entries, candidates int }

var defaultFileLinkSearchLimits = fileLinkSearchLimits{entries: 20_000, candidates: 50}

func validFileLinkPath(value string) bool {
	if value == "" || len(value) > 4096 || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\\:") || !fs.ValidPath(value) || path.Clean(value) != value || value == "." {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r >= '\u202a' && r <= '\u202e' || r >= '\u2066' && r <= '\u2069' {
			return false
		}
	}
	return true
}

func (s *Server) handleResolveSessionFileLink(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	rel := query.Get("path")
	nameOnly := query.Get("basename") == "1"
	if len(query["path"]) != 1 || len(query["basename"]) > 1 || query.Get("basename") != "" && !nameOnly || !validFileLinkPath(rel) || nameOnly && strings.Contains(rel, "/") {
		writeErr(w, http.StatusBadRequest, errors.New("file link requires a canonical session-relative path; basename lookup accepts one bare filename"))
		return
	}
	sess.mu.Lock()
	cwd, closing := sess.cwd, sess.closing
	sess.mu.Unlock()
	if closing {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	root, err := tools.Resolve(s.requestRoot(r), ".")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("file workspace is unavailable"))
		return
	}
	cwd, err = tools.Resolve(root, cwd)
	if err != nil {
		writeErr(w, http.StatusForbidden, errors.New("session workspace is outside the caller's file workspace"))
		return
	}
	if info, e := os.Stat(cwd); e != nil || !info.IsDir() {
		writeErr(w, http.StatusNotFound, errors.New("session workspace is unavailable"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, err := resolveSessionFileLink(ctx, root, cwd, rel, nameOnly, defaultFileLinkSearchLimits)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("file link is outside the session workspace"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func resolveSessionFileLink(ctx context.Context, root, cwd, rel string, nameOnly bool, limits fileLinkSearchLimits) (fileLinkResolution, error) {
	result := fileLinkResolution{Status: "not_found", Candidates: []fileLinkCandidate{}, Complete: true}
	callerRoot, err := os.OpenRoot(root)
	if err != nil {
		result.Status, result.Complete = "incomplete", false
		return result, nil
	}
	defer callerRoot.Close()
	workspaceRel, err := filepath.Rel(root, cwd)
	if err != nil || !filepath.IsLocal(workspaceRel) {
		return result, errors.New("session workspace escaped the caller root")
	}
	workspace, err := callerRoot.OpenRoot(workspaceRel)
	if err != nil {
		result.Status, result.Complete = "incomplete", false
		return result, nil
	}
	defer workspace.Close()
	candidate := func(relative string) (fileLinkCandidate, bool, error) {
		_, err := tools.Resolve(cwd, relative)
		if err != nil {
			return fileLinkCandidate{}, false, err
		}
		// Use the held workspace handle, so a symlink swap after the
		// canonical check cannot turn even a metadata lookup into an escape.
		info, err := workspace.Stat(filepath.FromSlash(relative))
		if err != nil {
			return fileLinkCandidate{}, false, err
		}
		if !info.Mode().IsRegular() {
			return fileLinkCandidate{}, false, nil
		}
		// Keep the validated lexical name. A safe in-workspace file symlink
		// may have a different target basename; the common file API repeats
		// its own canonical root checks when the user actually opens it.
		panelPath, err := filepath.Rel(root, filepath.Join(cwd, filepath.FromSlash(relative)))
		if err != nil || !filepath.IsLocal(panelPath) {
			return fileLinkCandidate{}, false, errors.New("file path escaped the caller root")
		}
		return fileLinkCandidate{Path: filepath.ToSlash(panelPath), RelativePath: relative, Name: path.Base(relative)}, true, nil
	}
	if exact, regular, err := candidate(rel); err == nil {
		if regular {
			result.Status = "exact"
			result.Candidates = append(result.Candidates, exact)
		}
		return result, nil // an existing directory is not a different file
	} else if !errors.Is(err, fs.ErrNotExist) {
		if errors.Is(err, fs.ErrPermission) {
			result.Status, result.Complete = "incomplete", false
			return result, nil
		}
		return result, err
	}
	if !nameOnly || strings.Contains(rel, "/") {
		return result, nil
	}

	directories := []string{"."}
	visited, stopped := 0, false
	for len(directories) != 0 && !stopped {
		if ctx.Err() != nil {
			result.Complete = false
			break
		}
		dir := directories[len(directories)-1]
		directories = directories[:len(directories)-1]
		before, e := workspace.Lstat(dir)
		if e != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			result.Complete = false
			continue
		}
		handle, e := workspace.Open(dir)
		if e != nil {
			result.Complete = false
			continue
		}
		after, e := handle.Stat()
		if e != nil || !after.IsDir() || !os.SameFile(before, after) {
			handle.Close()
			result.Complete = false
			continue
		}
		for !stopped {
			// Read in bounded batches rather than materializing every name in
			// one unusually large directory before applying the entry limit.
			entries, readErr := handle.ReadDir(128)
			for _, entry := range entries {
				if ctx.Err() != nil || visited >= limits.entries {
					result.Complete, stopped = false, true
					break
				}
				visited++
				relative := path.Join(dir, entry.Name())
				if entry.IsDir() {
					if !validFileLinkPath(relative) {
						result.Complete = false
						continue
					}
					directories = append(directories, relative)
					continue
				}
				if entry.Name() != rel {
					continue
				}
				found, regular, err := candidate(relative)
				if err != nil {
					// Outside symlinks are intentionally ineligible. Other
					// races/unreadable files prevent a uniqueness claim.
					if _, denied := tools.Resolve(cwd, relative); denied == nil {
						result.Complete = false
					}
					continue
				}
				if regular {
					if len(result.Candidates) >= limits.candidates {
						result.Complete, stopped = false, true
						break
					}
					result.Candidates = append(result.Candidates, found)
				}
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					result.Complete = false
				}
				break
			}
		}
		handle.Close()
	}
	sort.Slice(result.Candidates, func(i, j int) bool { return result.Candidates[i].RelativePath < result.Candidates[j].RelativePath })
	switch {
	case !result.Complete:
		result.Status = "incomplete"
	case len(result.Candidates) == 1:
		result.Status = "unique"
	case len(result.Candidates) > 1:
		result.Status = "ambiguous"
	}
	return result, nil
}
