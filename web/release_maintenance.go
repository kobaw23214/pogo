package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type releaseMaintenanceGrantKey struct{}

// Only a request admitted with the current maintenance header receives this
// digest. It cannot reconstruct the secret; delegation is limited to a
// matching active document's file download and signed callback.
func releaseMaintenanceGrant(r *http.Request) [32]byte {
	grant, _ := r.Context().Value(releaseMaintenanceGrantKey{}).([32]byte)
	return grant
}

// releaseMaintenancePath is outside user workspaces and transcripts. Operators
// create this private marker before a cold release, then remove it explicitly
// only after acceptance. A malformed/unreadable marker stays closed.
func (m *Manager) releaseMaintenancePath() string {
	if m.cfg.SessionDir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(m.cfg.SessionDir), "release-maintenance.json")
}

func (m *Manager) releaseMaintenance() (active bool, token string) {
	path := m.releaseMaintenancePath()
	if path == "" {
		return false, ""
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return false, ""
	}
	if err != nil {
		return true, ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return true, ""
	}
	var marker struct {
		Token string `json:"token"`
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 || json.Unmarshal(data, &marker) != nil || len(marker.Token) != 64 || strings.Trim(marker.Token, "0123456789abcdef") != "" {
		return true, ""
	}
	return true, marker.Token
}

// The release header only passes maintenance; ordinary ownership, account,
// Origin and MCP authorization still run. It is not an authentication token.
func (s *Server) withReleaseMaintenance(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active, token := s.mgr.releaseMaintenance()
		supplied := r.Header.Get("X-Pogo-Release")
		headerGranted := active && token != "" && subtle.ConstantTimeCompare([]byte(supplied), []byte(token)) == 1
		if active && !headerGranted && (token == "" || !s.officeReleaseCapabilityAllowed(r, sha256.Sum256([]byte(token)))) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Retry-After", "30")
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Pogo is temporarily unavailable during maintenance"})
			return
		}
		if headerGranted {
			r = r.WithContext(context.WithValue(r.Context(), releaseMaintenanceGrantKey{}, sha256.Sum256([]byte(token))))
		}
		// Never forward the release secret to a reverse proxy, logger or handler.
		r.Header.Del("X-Pogo-Release")
		next.ServeHTTP(w, r)
	})
}

// DocumentServer cannot attach the operator's release header. An authenticated
// editor config may instead bind its unguessable file capability to this one
// maintenance epoch. This is not a general Office or browser API exemption;
// the normal file and callback handlers still check capabilities and JWTs.
func (s *Server) officeReleaseCapabilityAllowed(r *http.Request, grant [32]byte) bool {
	if s.office == nil || r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		return false
	}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) != 5 || parts[0] != "" || parts[1] != "office" || !officeSessionID.MatchString(parts[3]) {
		return false
	}
	if !((r.Method == http.MethodGet && parts[2] == "files") || (r.Method == http.MethodPost && parts[2] == "callbacks")) {
		return false
	}
	zero := [32]byte{}
	if subtle.ConstantTimeCompare(grant[:], zero[:]) == 1 {
		return false
	}
	// Preserve the service -> document lock order used by open/close/revoke.
	s.office.mu.Lock()
	defer s.office.mu.Unlock()
	d := s.office.sessions[parts[3]]
	if s.office.closed || d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.closed && d.role != nil && !d.role.revoked.Load() && s.office.active[d.absolute] == d.id &&
		officeEqual(parts[4], d.fileToken) && subtle.ConstantTimeCompare(d.releaseGrant[:], zero[:]) != 1 &&
		subtle.ConstantTimeCompare(d.releaseGrant[:], grant[:]) == 1
}
