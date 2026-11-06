package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yosukeno/pogo/tools"
	"github.com/yosukeno/pogo/webservice"
)

// prepareWebServicesHost keeps host control storage outside all file-tool roots
// and installs true command masking, including inherited subagent processes.
func (m *Manager) prepareWebServicesHost() error {
	if m.cfg.WebServices == nil {
		return nil
	}
	m.cfg.WebServices.SetWorkspaceResolver(func(owner, id string) (string, error) {
		sess, err := m.Get(id)
		if err != nil {
			return "", err
		}
		if sessionResultOwnerKey(sess.owner) != owner {
			return "", webservice.Fail("PERMISSION_DENIED", "website workspace owner does not match")
		}
		return sess.cwd, nil
	})
	dirs := append([]string{m.cfg.WebServices.Root()}, m.cfg.WebServicePrivateDirs...)
	roots := append([]string{m.cfg.Cwd}, m.readRoots...)
	for _, dir := range dirs {
		p, err := filepath.Abs(dir)
		if err != nil || filepath.Dir(p) == p {
			return errors.New("managed Web control directories must be private absolute paths")
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil || filepath.Clean(real) != filepath.Clean(p) {
			return errors.New("managed Web control directory must not traverse symlinks")
		}
		for _, root := range roots {
			r, err := filepath.Abs(root)
			if err != nil {
				return err
			}
			if realRoot, err := filepath.EvalSymlinks(r); err == nil {
				r = realRoot
			}
			if pathContains(r, p) || pathContains(p, r) {
				return errors.New("managed Web control directories must not overlap workspace or read roots")
			}
		}
	}
	m.webServicePrivateRelease = tools.RegisterHostPrivateDirs(dirs)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.cfg.WebServices.Preflight(ctx); err != nil {
		// The main workbench and static preview remain usable during an optional
		// runner/gateway outage. No callable managed-service schema is exposed.
		fmt.Fprintf(os.Stderr, "managed Web services unavailable: %v\n", err)
		return nil
	}
	m.webServicesReady = true
	return nil
}

func pathContains(root, p string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (m *Manager) webServiceBackend(s *Session) webservice.Backend {
	if m.cfg.WebServices == nil || !m.webServicesReady || s == nil {
		return nil
	}
	if meta := s.store.Meta(); meta != nil && (meta.AgentWorkflow != nil || meta.BackgroundTask != nil || meta.Sentinel != nil) {
		return nil
	}
	if s.studioID != "" && !s.studioWeb {
		return nil
	}
	backend := m.cfg.WebServices.Bind(webservice.Scope{Owner: sessionResultOwnerKey(s.owner), Session: s.ID, Workspace: s.cwd})
	if s.studioID == "" {
		return backend
	}
	return &webServiceOwnerLinks{Backend: backend, agentID: s.studioID, sessionID: s.ID}
}

// Owner links select a service in an authenticated conversation; they are not
// access grants. Build them from the bound host identity, never request headers
// or tool-provided URLs, and leave pending operation receipts untouched.
type webServiceOwnerLinks struct {
	webservice.Backend
	agentID   string
	sessionID string
}

func (b *webServiceOwnerLinks) Profiles() []string {
	if source, ok := b.Backend.(interface{ Profiles() []string }); ok {
		return source.Profiles()
	}
	return []string{"python-web"}
}

func (b *webServiceOwnerLinks) Execute(ctx context.Context, request webservice.Request) (json.RawMessage, error) {
	data, err := b.Backend.Execute(ctx, request)
	if err != nil || request.Action != "open" {
		return data, err
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(data, &result) != nil {
		return data, nil
	}
	var previewURL string
	if json.Unmarshal(result["preview_url"], &previewURL) != nil || previewURL == "" {
		return data, nil
	}
	ownerURL := url.URL{Path: "/a/" + b.agentID, RawQuery: url.Values{
		"c":           {b.sessionID},
		"web_service": {request.ServiceID},
	}.Encode()}
	result["service_url"], _ = json.Marshal(ownerURL.String())
	return json.Marshal(result)
}

func (s *Server) webServiceRoutes() {
	s.mux.HandleFunc("GET /api/web-services/settings", s.admin(s.handleWebServiceSettings))
	s.mux.HandleFunc("PUT /api/web-services/settings", s.admin(s.handleWebServiceSettings))
	const prefix = "/api/sessions/{sid}/web-services"
	s.mux.HandleFunc("GET "+prefix, s.handleWebServices)
	s.mux.HandleFunc("POST "+prefix, s.handleWebServices)
	s.mux.HandleFunc("GET "+prefix+"/{serviceID}", s.handleWebServices)
	s.mux.HandleFunc("DELETE "+prefix+"/{serviceID}", s.handleDeleteWebService)
	s.mux.HandleFunc("GET "+prefix+"/{serviceID}/logs", s.handleWebServices)
	for _, action := range []string{"previews", "stop", "restart", "renew", "rename"} {
		s.mux.HandleFunc("POST "+prefix+"/{serviceID}/"+action, s.handleWebServices)
	}
	s.mux.HandleFunc("DELETE "+prefix+"/{serviceID}/previews/{grantID}", s.handleWebServices)
}

func (s *Server) handleDeleteWebService(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	if s.mgr.webServiceBackend(sess) == nil {
		writeWebServiceErr(w, http.StatusServiceUnavailable, webservice.Fail("FEATURE_DISABLED", "managed Web services are not available in this conversation"))
		return
	}
	// This route grants only the authenticated human owner's cleanup authority.
	// No owner, workspace or executable action is accepted from request data.
	err := s.mgr.cfg.WebServices.DeleteService(r.Context(), webservice.Scope{Owner: sessionResultOwnerKey(sess.owner), Session: sess.ID}, r.PathValue("serviceID"))
	if err != nil {
		status := http.StatusServiceUnavailable
		var execution *webservice.Error
		if errors.As(err, &execution) {
			switch execution.Code {
			case "NOT_FOUND", "PERMISSION_DENIED":
				status = http.StatusNotFound
			case "INVALID_ARGUMENT":
				status = http.StatusBadRequest
			}
		}
		writeWebServiceErr(w, status, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleWebServiceSettings(w http.ResponseWriter, r *http.Request) {
	manager := s.mgr.cfg.WebServices
	if manager == nil {
		writeWebServiceErr(w, http.StatusServiceUnavailable, webservice.Fail("FEATURE_DISABLED", "managed websites are not configured on this deployment"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"state": manager.Settings(), "available_profiles": manager.Profiles()})
		return
	}
	var settings webservice.Policy
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid website settings"))
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeErr(w, http.StatusBadRequest, errors.New("expected one settings object"))
		return
	}
	result, err := manager.UpdateSettings(r.Context(), settings)
	if err != nil {
		writeWebServiceErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleWebServices(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	backend := s.mgr.webServiceBackend(sess)
	if backend == nil {
		writeWebServiceErr(w, http.StatusServiceUnavailable, webservice.Fail("FEATURE_DISABLED", "managed Web services are not available in this conversation"))
		return
	}
	request := webservice.Request{ServiceID: r.PathValue("serviceID")}
	if r.Method == http.MethodGet {
		if request.ServiceID == "" {
			request.Action = "list"
		} else if strings.HasSuffix(r.URL.Path, "/logs") {
			request.Action = "logs"
		} else {
			request.Action = "status"
		}
		request.Cursor = r.URL.Query().Get("cursor")
		for key, target := range map[string]*int{"limit_bytes": &request.LimitBytes, "generation": &request.Generation} {
			if value := r.URL.Query().Get(key); value != "" {
				n, err := strconv.Atoi(value)
				if err != nil {
					writeErr(w, http.StatusBadRequest, errors.New("invalid numeric query parameter"))
					return
				}
				*target = n
			}
		}
	} else if r.Method == http.MethodDelete {
		request.Action = "revoke"
		request.GrantID = r.PathValue("grantID")
	} else {
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		d.DisallowUnknownFields()
		if err := d.Decode(&request); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := d.Decode(new(any)); err != io.EOF {
			writeErr(w, http.StatusBadRequest, errors.New("expected one JSON object"))
			return
		}
		if request.ServiceID != "" && request.ServiceID != r.PathValue("serviceID") {
			writeErr(w, http.StatusBadRequest, errors.New("service_id is selected by the request path"))
			return
		}
		switch {
		case r.PathValue("serviceID") == "":
			request.Action = "start"
		case strings.HasSuffix(r.URL.Path, "/previews"):
			request.Action = "open"
		case strings.HasSuffix(r.URL.Path, "/stop"):
			request.Action = "stop"
		case strings.HasSuffix(r.URL.Path, "/restart"):
			request.Action = "restart"
		case strings.HasSuffix(r.URL.Path, "/renew"):
			request.Action = "renew"
		case strings.HasSuffix(r.URL.Path, "/rename"):
			request.Action = "rename"
		}
		request.ServiceID = r.PathValue("serviceID")
	}
	if request.Action != "list" && request.Action != "status" && request.Action != "logs" && request.OperationID == "" {
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		request.OperationID = hex.EncodeToString(id)
	}
	// API identity is always the authenticated owner, even if the Agent's tool
	// grant was revoked. Owner management is deliberately not an Agent bypass.
	if request.Action != "revoke" {
		raw, _ := json.Marshal(request)
		if err := (&tools.WebService{}).ValidateArgs(raw); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	data, err := backend.Execute(r.Context(), request)
	if err != nil {
		status := http.StatusBadRequest
		var execution *webservice.Error
		if errors.As(err, &execution) {
			switch execution.Code {
			case "NOT_FOUND", "SERVICE_NOT_FOUND", "PERMISSION_DENIED":
				status = http.StatusNotFound
			case "FEATURE_DISABLED", "RUNNER_UNAVAILABLE", "GATEWAY_UNAVAILABLE":
				status = http.StatusServiceUnavailable
			case "IDEMPOTENCY_CONFLICT", "SERVICE_ALREADY_EXISTS":
				status = http.StatusConflict
			}
		}
		writeWebServiceErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

// Keep the legacy error string for existing API callers while giving service
// controls a stable code for recovery (for example, reopening an expired grant).
func writeWebServiceErr(w http.ResponseWriter, status int, err error) {
	var execution *webservice.Error
	if errors.As(err, &execution) {
		writeJSON(w, status, map[string]any{"error": execution.Error(), "code": execution.Code, "details": execution.Details})
		return
	}
	writeErr(w, status, err)
}

// ManagedWebServiceChanged publishes only the sanitized view, never a grant URL
// or private owner/upstream fields. Cold conversations query the durable store.
func (m *Manager) ManagedWebServiceChanged(record webservice.Record) {
	m.mu.Lock()
	sess := m.sessions[record.Session]
	closed := m.closed
	m.mu.Unlock()
	if closed || sess == nil || sessionResultOwnerKey(sess.owner) != record.Owner {
		return
	}
	sess.hub.Publish(Event{Type: EvWebServiceChanged, WebService: webservice.PublicRecord(record)})
}
