package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/yosukeno/pogo/memory"
)

type memoryCatalog struct {
	Scope     string        `json:"scope"`
	Directory string        `json:"directory"`
	Notes     []memory.Note `json:"notes"`
}

func sessionMemoryRoot(sess *Session) (root, scope string, err error) {
	if sess.memoryDir != "" {
		return sess.memoryDir, "agent", nil
	}
	if store := sess.mgr.workspaceFor(sess.root).memory; store != nil {
		if roots := store.Roots(); len(roots) > 0 {
			return roots[0], "user", nil
		}
	}
	return "", "", errors.New("当前会话没有可用的记忆目录")
}

func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	root, scope, err := sessionMemoryRoot(sess)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		notes, err := memory.ListNotes(root)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		sort.SliceStable(notes, func(i, j int) bool {
			if !notes[i].Modified.Equal(notes[j].Modified) {
				return notes[i].Modified.After(notes[j].Modified)
			}
			return notes[i].Path < notes[j].Path
		})
		writeJSON(w, http.StatusOK, memoryCatalog{Scope: scope, Directory: root, Notes: notes})

	case http.MethodPut:
		if s.rejectBackgroundTaskWrite(w, sess.ID) {
			return
		}
		var input struct {
			Path    string          `json:"path"`
			Type    memory.NoteType `json:"type"`
			Content string          `json:"content"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, memory.MaxManagedNoteBytes+16<<10)
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		unlock := memory.LockNoteRoot(root)
		defer unlock()
		note, err := memory.SaveNote(root, memory.Note{Path: strings.TrimSpace(input.Path), Type: input.Type, Content: input.Content, SourceSession: sess.ID, Origin: "manual"}, true)
		if err != nil {
			writeMemoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, note)

	case http.MethodDelete:
		if s.rejectBackgroundTaskWrite(w, sess.ID) {
			return
		}
		unlock := memory.LockNoteRoot(root)
		defer unlock()
		if err := memory.DeleteNote(root, r.URL.Query().Get("path")); err != nil {
			writeMemoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func writeMemoryError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, memory.ErrNoteNotFound):
		status = http.StatusNotFound
	case errors.Is(err, memory.ErrInvalidNote), errors.Is(err, memory.ErrSensitiveNote):
		status = http.StatusBadRequest
	case errors.Is(err, memory.ErrNoteTooLarge):
		status = http.StatusRequestEntityTooLarge
	}
	writeErr(w, status, err)
}
