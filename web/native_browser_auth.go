package web

import (
	"errors"
	"net/http"
)

// Keep an explicit retirement response for older clients. Ownership is checked
// before revealing route state; this never creates or contacts a browser worker.
func (s *Server) handleBrowserAuth(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	if !s.mgr.browserAllowed(sess.owner) {
		writeBrowserDeviceNotBound(w, http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeErr(w, http.StatusGone, errors.New("browser credential helpers have been retired; use page actions and devtools in the current browser session"))
}
