package web

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/yosukeno/pogo/session"
)

// Retained children are not public root sessions. Resolve their transcript only
// through an authenticated parent and its owner-bound registry. A read never
// calls Manager.Get(child), acquires its execution lease, or resumes work.
func (s *Server) handleSubagentTranscript(w http.ResponseWriter, r *http.Request) {
	parent, ok := s.session(w, r)
	if !ok {
		return
	}
	if parent.retainedSubagents == nil {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	child, err := parent.retainedSubagents.Lookup(r.PathValue("agentID"))
	if err != nil {
		// Missing and foreign identities have the same response; registry paths
		// and owners are never exposed through an error to a different parent.
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	store, err := session.Open(child.Session)
	if err != nil {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	hub := NewHub()
	hub.Seed(store.TimedMessages(""))
	hub.SetCompactions(store.Compactions(""))
	usage, _ := store.UsageTotals()
	hub.SetUsage(usage)
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": strings.TrimSuffix(filepath.Base(child.Session), ".jsonl"),
		"snapshot":   hub.Snapshot(),
	})
}
