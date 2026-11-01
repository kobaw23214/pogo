package web

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/tools"
)

var errBrowserDeviceNotBound = errors.New("Desktop browser device is not bound to this account")

func (cfg *Config) bindBrowserDesktopOwner() error {
	name := cfg.BrowserDesktopOwner
	if name == "" {
		return nil
	}
	if len(name) > 64 || strings.TrimSpace(name) != name || !utf8.ValidString(name) || !validLoginName(name) {
		return errors.New("POGO_BROWSER_DESKTOP_OWNER must be an exact local account username")
	}
	if cfg.browser == nil {
		return errors.New("POGO_BROWSER_DESKTOP_OWNER requires POGO_BROWSER_URL")
	}
	owner := identity.LocalScope(name)
	cfg.browserDesktopOwner = &owner
	return nil
}

// A desktop device is not a shared server browser. All scope dimensions must
// match; a missing owner or a matching subject in another project is denied.
func (m *Manager) browserAllowed(owner *identity.Scope) bool {
	bound := m.cfg.browserDesktopOwner
	return bound == nil || (owner != nil && *owner == *bound)
}

func (s *Server) browserAllowedRequest(r *http.Request) bool {
	owner, ok := s.ownerScope(r)
	if !ok {
		return s.mgr.browserAllowed(nil)
	}
	return s.mgr.browserAllowed(&owner)
}

func browserLinkIf(enabled bool, gate *BrowserGate) tools.BrowserLink {
	if !enabled {
		return nil
	}
	return gate
}

func writeBrowserDeviceNotBound(w http.ResponseWriter, status int) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": errBrowserDeviceNotBound.Error(), "code": "BROWSER_DEVICE_NOT_BOUND"})
}
