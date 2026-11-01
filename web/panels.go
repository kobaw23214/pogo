package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"unicode"
)

// Runtime panel registration: the browser UI's add-shortcut dialog offers to
// route a cross-origin URL through the built-in reverse proxy, so the page it
// opens is same-origin (the element picker needs that; see BrowserPane). This
// file is the server half of that handshake — the flag (-web-panel) stays the
// operator's baked-in channel, POST /api/panels is the UI's runtime one.

// sanitizePanelName turns a naming hint (usually the shortcut card's title)
// into a panel name: anything that is not a letter, digit or -_.~ becomes a
// dash, because the name is also a URL path segment under /panels/. CJK
// survives — 样本库 is a legal segment and a legal flag name today.
func sanitizePanelName(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_.~", r):
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if runes := []rune(out); len(runes) > 24 {
		out = string(runes[:24])
	}
	return out
}

// mapLoopbackHost rewrites a loopback host to mapping (preserving port), for
// deployments where the server runs in a container and a URL the browser user
// spelled as "localhost" actually lives on the host — Docker Desktop's
// host.docker.internal. Empty mapping leaves the URL untouched, which is the
// right thing whenever server and browser share a network vantage point.
func mapLoopbackHost(rawURL, mapping string) string {
	if mapping == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	host := u.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return rawURL
	}
	if port := u.Port(); port != "" {
		u.Host = net.JoinHostPort(mapping, port)
	} else {
		u.Host = mapping
	}
	return u.String()
}

// registerPanel adds a panel at runtime. Idempotent per URL: a URL the server
// already serves (flag or runtime) returns the existing panel unchanged, so
// re-adding or re-editing a shortcut never grows the panel set.
func (s *Server) registerPanel(hint, rawURL string) (Panel, error) {
	return s.registerPanelMode(hint, rawURL, "")
}

func (s *Server) registerPanelMode(hint, rawURL, mode string) (Panel, error) {
	return s.registerPanelOptions(hint, rawURL, mode, false)
}

func (s *Server) registerPanelOptions(hint, rawURL, mode string, preserveExisting bool) (Panel, error) {
	if mode != "" && mode != "passthrough" && mode != "compat" {
		return Panel{}, fmt.Errorf("unknown panel proxy mode %q", mode)
	}
	sourceURL := rawURL
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Panel{}, fmt.Errorf("invalid panel url %q: want absolute http(s)", rawURL)
	}
	rawURL = mapLoopbackHost(rawURL, s.opts.PanelLoopbackMap)

	s.panelMu.Lock()
	defer s.panelMu.Unlock()
	// Trailing-slash-insensitive: http://h and http://h/ are the same panel,
	// and the user's typing varies.
	norm := strings.TrimSuffix(rawURL, "/")
	for _, p := range append(append([]Panel{}, s.opts.Panels...), s.userPanels...) {
		if strings.TrimSuffix(p.URL, "/") == norm {
			if !preserveExisting && mode != "" && panelMode(p) != mode {
				for i := range s.userPanels {
					if s.userPanels[i].Name == p.Name {
						p.ProxyMode, p.SourceURL = mode, sourceURL
						s.userPanels[i] = p
						s.panelProxies[p.Name] = newPanelProxy(p)
						s.savePanelStore()
						return p, nil
					}
				}
				return Panel{}, fmt.Errorf("panel %q is configured at startup; change its proxy_mode in server configuration", p.Name)
			}
			return p, nil
		}
	}

	name := sanitizePanelName(hint)
	if name == "" {
		name = sanitizePanelName(u.Host)
	}
	if name == "" {
		name = "panel"
	}
	final := name
	for i := 2; ; i++ {
		_, taken := s.panelProxies[final]
		if !taken {
			break
		}
		if i >= 100 {
			return Panel{}, fmt.Errorf("cannot derive a free panel name from %q", hint)
		}
		final = fmt.Sprintf("%s-%d", name, i)
	}

	p := Panel{Name: final, URL: rawURL, ProxyMode: mode}
	if rawURL != sourceURL {
		p.SourceURL = sourceURL
	}
	// Belt and braces: the sanitized name must clear the same bar the flag
	// sets, or the route grammar would be broken by construction.
	if err := validatePanel(p); err != nil {
		return Panel{}, err
	}
	s.panelProxies[p.Name] = newPanelProxy(p)
	s.userPanels = append(s.userPanels, p)
	s.savePanelStore()
	return p, nil
}

// handlePanelRegister is the runtime half of -web-panel. Token-gated like every
// /api route; the trust bar is the one the Panel doc states — a registered
// backend becomes same-origin with a page that holds the token — except the
// decision moves from the operator's command line to whoever holds the token
// in the UI.
func (s *Server) handlePanelRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL              string `json:"url"`
		Name             string `json:"name"`
		ProxyMode        string `json:"proxy_mode"`
		PreserveExisting bool   `json:"preserve_existing"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<10))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if json.Unmarshal(body, &req) != nil || strings.TrimSpace(req.URL) == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("want {url, name?}"))
		return
	}
	p, err := s.registerPanelOptions(strings.TrimSpace(req.Name), strings.TrimSpace(req.URL), req.ProxyMode, req.PreserveExisting)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": p.Name, "path": "/panels/" + p.Name + "/", "entry": panelEntryPath(p), "proxy_mode": panelMode(p)})
}

// removePanel revokes a runtime registration: the proxy route, the in-memory
// entry and the persisted store all go in one step, so a deleted panel is
// immediately unreachable and does not come back after a restart. Flag-supplied
// panels (opts.Panels) are the operator's baked-in decision and are refused —
// removing those means editing the command line, not calling the API.
func (s *Server) removePanel(name string) error {
	if name == "" {
		return fmt.Errorf("panel name is required")
	}
	s.panelMu.Lock()
	defer s.panelMu.Unlock()
	for _, p := range s.opts.Panels {
		if p.Name == name {
			return fmt.Errorf("panel %q is supplied by -web-panel; remove it from the command line instead", name)
		}
	}
	for i, p := range s.userPanels {
		if p.Name != name {
			continue
		}
		s.userPanels = append(s.userPanels[:i], s.userPanels[i+1:]...)
		delete(s.panelProxies, name)
		s.savePanelStore()
		return nil
	}
	return os.ErrNotExist
}

// handlePanelDelete is the revoke half of the registration handshake: same
// token bar as POST /api/panels, because deleting a panel withdraws a
// same-origin grant exactly like adding one creates it.
func (s *Server) handlePanelDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.removePanel(name); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeErr(w, http.StatusNotFound, fmt.Errorf("no registered panel named %q", name))
			return
		}
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "deleted": true})
}

// loadPanelStore reads runtime-registered panels written by an earlier run.
// Entries whose name a -web-panel flag already claims are dropped — the flag
// is the operator's decision and must win — as are entries that no longer
// validate. A corrupt or missing file starts clean rather than failing boot.
func (s *Server) loadPanelStore() {
	if s.opts.PanelStore == "" {
		return
	}
	data, err := os.ReadFile(s.opts.PanelStore)
	if err != nil {
		return // first run
	}
	var st struct {
		Panels []Panel `json:"panels"`
	}
	if json.Unmarshal(data, &st) != nil {
		return // corrupt: the next registration rewrites it
	}
	for _, p := range st.Panels {
		if _, dup := s.panelProxies[p.Name]; dup {
			continue
		}
		if validatePanel(p) != nil {
			continue
		}
		s.panelProxies[p.Name] = newPanelProxy(p)
		s.userPanels = append(s.userPanels, p)
	}
}

// savePanelStore persists the runtime-added set (flag panels are re-supplied
// by config on every start and deliberately not stored). Best-effort: a write
// failure costs persistence across restarts, not the registration itself.
// Caller holds panelMu.
func (s *Server) savePanelStore() {
	if s.opts.PanelStore == "" {
		return
	}
	st := struct {
		Panels []Panel `json:"panels"`
	}{Panels: s.userPanels}
	if st.Panels == nil {
		st.Panels = []Panel{}
	}
	data, err := json.MarshalIndent(&st, "", " ")
	if err != nil {
		s.warnf("panel store: %v", err)
		return
	}
	tmp := s.opts.PanelStore + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		s.warnf("panel store: %v", err)
		return
	}
	if err := os.Rename(tmp, s.opts.PanelStore); err != nil {
		s.warnf("panel store: %v", err)
	}
}

func (s *Server) warnf(format string, args ...any) {
	if s.opts.Logger != nil {
		s.opts.Logger.Printf(format, args...)
	}
}
