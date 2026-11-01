package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/yosukeno/pogo/tools"
)

type nativeBrowserControl struct {
	ContextRef     string                       `json:"context_ref,omitempty"`
	TabID          string                       `json:"tab_id"`
	Mode           string                       `json:"mode"`
	LeaseToken     string                       `json:"lease_token,omitempty"`
	Force          bool                         `json:"force,omitempty"`
	AdoptIfUnowned nativeBrowserControlAdoption `json:"adopt_if_unowned,omitempty"`
	Generation     *int64                       `json:"generation,omitempty"`
	ControlEpoch   *int64                       `json:"control_epoch,omitempty"`
}

// Explicit adoption must not collapse a null/false flag into an ordinary
// manual takeover after decoding. Missing stays false and preserves the
// existing control contract; a supplied flag can only mean conditional adopt.
type nativeBrowserControlAdoption bool

func (adoption *nativeBrowserControlAdoption) UnmarshalJSON(raw []byte) error {
	if !bytes.Equal(bytes.TrimSpace(raw), []byte("true")) {
		return errors.New("adopt_if_unowned must be true when provided")
	}
	*adoption = true
	return nil
}

type nativeBrowserInput struct {
	Type       string   `json:"type"`
	ContextRef string   `json:"context_ref"`
	TargetTab  string   `json:"target_tab"`
	Generation int64    `json:"generation"`
	LeaseToken string   `json:"lease_token"`
	X          *float64 `json:"x,omitempty"`
	Y          *float64 `json:"y,omitempty"`
	Text       *string  `json:"text,omitempty"`
	Key        string   `json:"key,omitempty"`
	DeltaY     *float64 `json:"deltaY,omitempty"`
	DeltaX     *float64 `json:"deltaX,omitempty"`
	URL        string   `json:"url,omitempty"`
	Accept     *bool    `json:"accept,omitempty"`
	TabID      string   `json:"tab_id,omitempty"`
	TabAction  string   `json:"tab_action,omitempty"`
	Version    string   `json:"version,omitempty"`
}

func (s *Server) nativeBrowserRoutes() {
	s.mux.HandleFunc("POST /api/browser/authoring-sessions", s.handleCreateBrowserAuthoring)
	s.mux.HandleFunc("GET /api/browser/authoring-sessions", s.handleListBrowserAuthoring)
	s.mux.HandleFunc("DELETE /api/browser/authoring-sessions/{sid}", s.handleDeleteBrowserAuthoring)
	s.mux.HandleFunc("GET /api/manage/release/browser-live", s.admin(s.handleReleaseBrowserLive))
	s.mux.HandleFunc("POST /api/browser/migrate-urls", s.handleBrowserMigrateURLs)
	s.mux.HandleFunc("GET /api/sessions/{sid}/browser", s.handleBrowserState)
	s.mux.HandleFunc("POST /api/sessions/{sid}/browser", s.handleBrowserState)
	s.mux.HandleFunc("GET /api/sessions/{sid}/browser/icons", s.handleBrowserIcons)
	s.mux.HandleFunc("GET /api/sessions/{sid}/browser/downloads", s.handleBrowserDownloads)
	s.mux.HandleFunc("GET /api/sessions/{sid}/browser/frame", s.handleBrowserFrame)
	s.mux.HandleFunc("POST /api/sessions/{sid}/browser/live-ticket", s.handleBrowserLiveTicket)
	s.mux.HandleFunc("POST /api/sessions/{sid}/browser/media-relay-ticket", s.handleBrowserMediaRelayTicket)
	s.mux.HandleFunc("GET /api/sessions/{sid}/browser/live", s.handleBrowserLive)
	s.mux.HandleFunc("POST /api/sessions/{sid}/browser/control", s.handleBrowserControl)
	s.mux.HandleFunc("POST /api/sessions/{sid}/browser/input", s.handleBrowserInput)
	s.mux.HandleFunc("POST /api/sessions/{sid}/browser/pick", s.handleBrowserPick)
	s.mux.HandleFunc("POST /api/sessions/{sid}/browser/pick/preview", s.handleBrowserPickPreview)
	s.mux.HandleFunc("POST /api/sessions/{sid}/browser/tabs", s.handleBrowserTabs)
	s.mux.HandleFunc("POST /api/sessions/{sid}/browser/auth", s.handleBrowserAuth)
}

func (s *Server) nativeBrowserSession(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	sess, ok := s.browserRouteSession(w, r)
	if !ok {
		return nil, false
	}
	if !s.mgr.browserAllowed(sess.owner) {
		writeBrowserDeviceNotBound(w, http.StatusForbidden)
		return nil, false
	}
	if sess.nativeBrowser == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("native browser service is not configured"))
		return nil, false
	}
	return sess, true
}

func (s *Server) handleBrowserState(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.browserRouteSession(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !s.mgr.browserAllowed(sess.owner) {
		writeJSON(w, http.StatusOK, map[string]string{"backend": "unavailable", "error": errBrowserDeviceNotBound.Error(), "code": "BROWSER_DEVICE_NOT_BOUND"})
		return
	}
	if sess.nativeBrowser == nil {
		writeJSON(w, http.StatusOK, map[string]string{"backend": "unavailable", "error": "Native browser service is not configured"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), DefaultBrowserOpTimeout)
	defer cancel()
	target, err := parseNativeBrowserTarget(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	state, err := sess.nativeBrowser.State(ctx, target)
	if err != nil {
		writeNativeBrowserError(w, err)
		return
	}
	// This is a server authorization fact, never a worker-provided assertion.
	if s.mgr.cfg.browserDesktopOwner != nil {
		state["device_owner_bound"] = json.RawMessage("true")
	}
	sess.touch()
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleBrowserFrame(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.nativeBrowserSession(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), DefaultBrowserOpTimeout)
	defer cancel()
	target, err := parseNativeBrowserTarget(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	frame, err := sess.nativeBrowser.Frame(ctx, target)
	if err != nil {
		writeNativeBrowserError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Browser-Frame-Identity", frame.Identity)
	if frame.ContextRef != "" {
		w.Header().Set("X-Browser-Context-Ref", frame.ContextRef)
	}
	if frame.ViewedTab != "" {
		w.Header().Set("X-Browser-Viewed-Tab", frame.ViewedTab)
	}
	if frame.Version != "" {
		w.Header().Set("X-Browser-Version", frame.Version)
	}
	if frame.Overlay != "" {
		w.Header().Set("X-Browser-Overlay", frame.Overlay)
	}
	_, _ = w.Write(frame.Data)
}

func decodeNativeBrowserBody(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid browser request: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("browser request must contain exactly one JSON object")
	}
	return nil
}

func (s *Server) handleBrowserControl(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.nativeBrowserSession(w, r)
	if !ok {
		return
	}
	var input nativeBrowserControl
	if err := decodeNativeBrowserBody(r, &input); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !validNativeBrowserControl(input) {
		writeErr(w, http.StatusBadRequest, errors.New("control requires a bound page and human or agent mode; returning control needs its human lease; adopt_if_unowned requires human mode, fresh generation and control_epoch zero without force or lease"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), DefaultBrowserOpTimeout)
	defer cancel()
	state, err := sess.nativeBrowser.Control(ctx, input)
	if err != nil {
		writeNativeBrowserError(w, err)
		return
	}
	// The worker fences only this tab's current atomic action. Keep the model
	// run alive so it can use another Agent-owned tab, including an action
	// already running there. A tab takeover is not a whole-task cancellation.
	w.Header().Set("Cache-Control", "no-store")
	sess.touch()
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleBrowserInput(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.nativeBrowserSession(w, r)
	if !ok {
		return
	}
	var input nativeBrowserInput
	if err := decodeNativeBrowserBody(r, &input); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	valid := input.LeaseToken != "" && validNativeContextRef(input.ContextRef) && nativeTabID.MatchString(input.TargetTab) && input.Generation > 0 && input.Generation <= 1<<53-1
	switch input.Type {
	case "click":
		valid = valid && input.X != nil && input.Y != nil && input.Version != ""
	case "key":
		valid = valid && input.Key != ""
	case "text":
		valid = valid && input.Text != nil
	case "scroll":
		valid = valid && (input.DeltaY != nil || input.DeltaX != nil)
	case "navigate":
		resolved, err := s.nativeNavigationURL(input.URL)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		input.URL = resolved
	case "back":
		// The worker traverses this bound tab's actual Chromium history. The
		// same generation and human lease checks apply as for navigation.
	case "dialog":
		valid = valid && input.Accept != nil
	case "tab":
		// Sheets manage tabs through /tabs. Viewing a Sheet is read-only and
		// never selects the Agent's execution target through /input.
		valid = false
	default:
		valid = false
	}
	if !valid {
		writeErr(w, http.StatusBadRequest, errors.New("invalid browser input or missing human lease token"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), DefaultBrowserOpTimeout)
	defer cancel()
	state, err := sess.nativeBrowser.Input(ctx, input)
	if err != nil {
		writeNativeBrowserError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	sess.touch()
	writeJSON(w, http.StatusOK, state)
}

// Live navigation never resolves old proxy shortcuts. They can only be
// converted by the explicit data-migration endpoint below.
func (s *Server) nativeNavigationURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return "", errors.New("browser navigation requires an original http(s) URL without embedded credentials")
	}
	return u.String(), nil
}

// This is a read-only saved-data converter. It never starts a worker, opens a
// page, creates a reverse proxy or dispatches an agent/browser operation.
func (s *Server) handleBrowserMigrateURLs(w http.ResponseWriter, r *http.Request) {
	var input struct {
		URLs []string `json:"urls"`
	}
	if err := decodeNativeBrowserBody(r, &input); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(input.URLs) == 0 || len(input.URLs) > 256 {
		writeErr(w, http.StatusBadRequest, errors.New("migration requires 1 to 256 saved URLs"))
		return
	}
	type mapping struct {
		URL         string `json:"url"`
		ResolvedURL string `json:"resolved_url,omitempty"`
		Error       string `json:"error,omitempty"`
	}
	mappings := make([]mapping, 0, len(input.URLs))
	for _, raw := range input.URLs {
		item := mapping{URL: raw}
		if len(raw) > 8192 {
			item.Error = "saved URL exceeds migration limit"
		} else if resolved, err := s.migrateBrowserURL(raw); err != nil {
			item.Error = err.Error()
		} else {
			item.ResolvedURL = resolved
		}
		mappings = append(mappings, item)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"mappings": mappings})
}

func (s *Server) migrateBrowserURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", errors.New("invalid browser navigation URL")
	}
	if u.IsAbs() {
		if (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil {
			return u.String(), nil
		}
		return "", errors.New("browser navigation requires an http(s) URL without embedded credentials")
	}
	if u.Host == "" && strings.HasPrefix(u.Path, "/panels/") {
		encodedName, rest, _ := strings.Cut(strings.TrimPrefix(u.EscapedPath(), "/panels/"), "/")
		name, err := url.PathUnescape(encodedName)
		if err != nil {
			return "", errors.New("invalid saved panel name")
		}
		s.panelMu.RLock()
		panels := append(append([]Panel{}, s.opts.Panels...), s.userPanels...)
		s.panelMu.RUnlock()
		for _, panel := range panels {
			if panel.Name != name {
				continue
			}
			target, err := url.Parse(panel.URL)
			if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" || target.User != nil {
				break
			}
			if rest != "" {
				if panelMode(panel) == "compat" {
					target.RawPath = "/" + rest
				} else {
					target.RawPath = strings.TrimRight(target.EscapedPath(), "/") + "/" + rest
				}
				target.Path, err = url.PathUnescape(target.RawPath)
				if err != nil {
					return "", errors.New("invalid saved panel path")
				}
			}
			if u.RawQuery != "" {
				target.RawQuery = u.RawQuery
			}
			if u.Fragment != "" {
				target.Fragment = u.Fragment
			}
			return target.String(), nil
		}
	}
	return "", errors.New("saved shortcut cannot be resolved; enter its original http(s) site address")
}

func writeNativeBrowserError(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway
	var remote *nativeBrowserHTTPError
	if errors.As(err, &remote) && (remote.status == http.StatusBadRequest || remote.status == http.StatusForbidden || remote.status == http.StatusConflict || remote.status == http.StatusNotFound || remote.status == http.StatusGone || remote.status == http.StatusTooManyRequests) {
		code = remote.status
	}
	if errors.Is(err, context.DeadlineExceeded) {
		code = http.StatusGatewayTimeout
	}
	if remote != nil && remote.status == http.StatusGatewayTimeout && remote.code == "PICK_OBSERVATION_PENDING" {
		code = http.StatusGatewayTimeout
	}
	if remote != nil && remote.code != "" {
		writeJSON(w, code, map[string]string{"error": err.Error(), "code": remote.code})
		return
	}
	writeErr(w, code, err)
}

func (s *Session) storeBrowserScreenshot(shot *tools.BrowserScreenshot) error {
	if shot.Data == "" {
		return errors.New("screenshot payload is empty")
	}
	if len(shot.Data) > 12<<20 {
		return errors.New("screenshot exceeds the size limit")
	}
	data, err := base64.StdEncoding.DecodeString(shot.Data)
	if err != nil {
		return errors.New("screenshot is not valid base64")
	}
	mime := http.DetectContentType(data)
	ext := ".jpg"
	if mime == "image/png" {
		ext = ".png"
	} else if mime != "image/jpeg" {
		return errors.New("screenshot must be PNG or JPEG")
	}
	if w, h, ok := imageDims(data, mime); !ok || w <= 0 || h <= 0 {
		return errors.New("screenshot image is invalid")
	}
	meta, err := saveAttachment(s.cwd, "browser", "screenshot-"+randomID(12)+ext, mime, map[string]bool{".jpg": true, ".png": true}, 8<<20, bytes.NewReader(data))
	if err != nil {
		return err
	}
	noteSessionUpload(s.cwd, s.ID, meta.Path)
	shot.ImageRef, shot.Data, shot.Mime = meta.Path, "", meta.Mime
	shot.Width, shot.Height = meta.W, meta.H
	s.mu.Lock()
	s.images = append(s.images, tools.ImageHandle{Ref: meta.Path, Name: meta.Name})
	if len(s.images) > 50 {
		s.images = append([]tools.ImageHandle(nil), s.images[len(s.images)-50:]...)
	}
	s.mu.Unlock()
	s.hub.Publish(Event{Type: EvAttachmentUploaded, Attachment: &meta})
	return nil
}
