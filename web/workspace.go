package web

// The workspace-changes API compares journaled files with their first-touch
// pre-images. It works in ordinary directories without consulting a repository.
// Writes made only through commands or outside editors are not journaled.

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"github.com/yosukeno/pogo/diff"
	"github.com/yosukeno/pogo/tools"
)

// maxDiffSides caps the content fed into the differ; beyond it the changes
// list still names the file and its stats, only the patch is withheld —
// limiting rendering, never navigation.
const maxDiffSides = 2 << 20

// maxPatchRows keeps a monster patch from wedging the tab.
const maxPatchRows = 5000

type workspaceChange struct {
	Path   string `json:"path"`
	Status string `json:"status"` // added | modified | deleted
	// Source identifies journaled writes, including saves from the file panel.
	Source        string `json:"source"`
	Added         int    `json:"added"`
	Removed       int    `json:"removed"`
	FirstMS       int64  `json:"first_ms"`
	LastMS        int64  `json:"last_ms"`
	Sid           string `json:"sid"`
	BaseAvailable bool   `json:"base_available"`
}

// wsView is the request-scoped workspace the files and workspace
// endpoints resolve against: the caller's own root and its journal in
// multi-user mode, the server root's pair otherwise. Everything below goes
// through it, so one user's changes panel can only ever see one user's tree.
type wsView struct {
	root    string
	journal *tools.DirJournal
}

// view builds the caller's workspace view.
func (s *Server) view(r *http.Request) wsView {
	if scope, ok := s.ownerScope(r); ok {
		root := s.mgr.UserRoot(scope)
		return wsView{root: root, journal: s.mgr.JournalFor(root)}
	}
	return wsView{root: s.mgr.Cwd(), journal: s.mgr.Journal()}
}

// requestRoot is view's root for handlers that only need the directory (the
// files panel's path resolution).
func (s *Server) requestRoot(r *http.Request) string {
	if scope, ok := s.ownerScope(r); ok {
		return s.mgr.UserRoot(scope)
	}
	return s.mgr.Cwd()
}

// changeOf reads both sides of a journaled path and summarises the difference.
// The zero change ("" base, missing current, and friends) is reported with
// ok=false so the list stays free of noise.
func (v wsView) changeOf(e tools.JournalEntry) (workspaceChange, bool) {
	out := workspaceChange{
		Path:    e.Path,
		Source:  "tool",
		FirstMS: e.FirstMS,
		LastMS:  e.LastMS,
		Sid:     e.Sid,
	}
	base, ok := v.journal.Base(e.Path)
	if !ok {
		base = nil
		e.NoBase = true
	}
	out.BaseAvailable = !e.NoBase

	abs, err := tools.Resolve(v.root, e.Path)
	if err != nil {
		return out, false // journaled paths are in-root by construction
	}
	current, readErr := os.ReadFile(abs)
	switch {
	case errors.Is(readErr, fs.ErrNotExist):
		if e.Created {
			return out, false // created and then deleted: a net zero
		}
		out.Status = "deleted"
	case readErr != nil:
		return out, false
	case e.Created:
		out.Status = "added"
	default:
		out.Status = "modified"
	}

	if out.BaseAvailable {
		if out.Status == "modified" && string(base) == string(current) {
			return out, false // a bash revert put the content back: not a change
		}
		out.Added, out.Removed = diff.Stat(string(base), string(current))
	}
	return out, true
}

func (s *Server) handleWorkspaceChanges(w http.ResponseWriter, r *http.Request) {
	v := s.view(r)
	out := make([]workspaceChange, 0)
	for _, e := range v.journal.List() {
		if c, ok := v.changeOf(e); ok {
			out = append(out, c)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": out})
}

func (s *Server) handleWorkspaceDiff(w http.ResponseWriter, r *http.Request) {
	v := s.view(r)
	rel := r.URL.Query().Get("path")
	e, ok := v.journal.Entry(rel)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("not a journaled path: "+rel))
		return
	}
	c, ok := v.changeOf(e)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("no net change for "+rel))
		return
	}
	resp := map[string]any{
		"path":           c.Path,
		"status":         c.Status,
		"added":          c.Added,
		"removed":        c.Removed,
		"base_available": c.BaseAvailable,
	}
	if !c.BaseAvailable {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	base, _ := v.journal.Base(rel)
	abs, _ := tools.Resolve(v.root, rel)
	current, _ := os.ReadFile(abs) // missing reads as empty: a deleted file
	if len(base)+len(current) > maxDiffSides {
		resp["too_big"] = true
		writeJSON(w, http.StatusOK, resp)
		return
	}
	patch := diff.Unified(rel, string(base), string(current), diff.DefaultContext)
	if strings.Count(patch, "\n") > maxPatchRows {
		resp["too_big"] = true
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp["patch"] = patch
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleWorkspaceJournalClear(w http.ResponseWriter, r *http.Request) {
	if err := s.view(r).journal.Clear(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
