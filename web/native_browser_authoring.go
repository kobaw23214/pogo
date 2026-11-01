package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/yosukeno/pogo/identity"
)

const browserAuthoringPrefix = "browser-authoring-"
const browserAuthoringIdleTTL = 20 * time.Minute
const browserAuthoringMaxTTL = 2 * time.Hour
const browserAuthoringOwnerLimit = 2
const browserAuthoringTotalLimit = 32
const browserAuthoringOperationOwnerLimit = 32
const browserAuthoringOperationLimit = 512

var browserAuthoringOperationID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)

// Browser authoring is deliberately not a conversation. The stub is resolved
// only by browserRouteSession: it never enters Manager.sessions, the catalog,
// a transcript, a workspace, a model, or the conversation tool registry.
type browserAuthoringSession struct {
	session      *Session
	owner        *identity.Scope
	created      time.Time
	lastUsed     time.Time
	timer        *time.Timer
	operationKey string
}

type browserAuthoringOperation struct {
	owner     *identity.Scope
	sessionID string
	closed    bool
	expires   time.Time
}

func browserAuthoringOperationKey(owner *identity.Scope, operationID string) string {
	if operationID == "" {
		return ""
	}
	encoded, _ := json.Marshal(struct {
		Owner     *identity.Scope `json:"owner"`
		Operation string          `json:"operation"`
	}{owner, operationID})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func isBrowserAuthoringID(id string) bool { return strings.HasPrefix(id, browserAuthoringPrefix) }

func (s *Server) browserAuthoringOwner(r *http.Request) (*identity.Scope, bool) {
	if !s.multiuser() {
		return nil, true
	}
	owner, ok := s.ownerScope(r)
	if !ok || !owner.Valid() {
		return nil, false
	}
	return &owner, true
}

func sameBrowserAuthoringOwner(a, b *identity.Scope) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func (s *Server) handleCreateBrowserAuthoring(w http.ResponseWriter, r *http.Request) {
	// No caller-selected owner, workspace, model or existing session is accepted.
	var empty map[string]json.RawMessage
	if err := decodeNativeBrowserBody(r, &empty); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	operationID := ""
	if raw, exists := empty["operation_id"]; exists {
		if json.Unmarshal(raw, &operationID) != nil || !browserAuthoringOperationID.MatchString(operationID) {
			writeErr(w, http.StatusBadRequest, errors.New("browser authoring operation_id must be a lowercase UUIDv4"))
			return
		}
	}
	if empty == nil || len(empty) > 1 || len(empty) == 1 && operationID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("browser authoring creation accepts only an optional operation_id"))
		return
	}
	owner, ok := s.browserAuthoringOwner(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("browser authoring requires an account"))
		return
	}
	if !s.mgr.browserAllowed(owner) {
		writeBrowserDeviceNotBound(w, http.StatusForbidden)
		return
	}
	if s.mgr.cfg.browser == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("native browser service is not configured"))
		return
	}
	const entropyBytes = 24
	id, now := browserAuthoringPrefix+randomID(entropyBytes), time.Now()
	browser := newNativeBrowser(s.mgr.cfg.browser)
	// This is a namespace only, not a transcript file or a created directory.
	// The fresh random path prevents sharing a conversation's browser profile.
	browser.bindSessionProfile(id, filepath.Join(s.mgr.cfg.SessionDir, ".browser-authoring", id), owner)
	stub := &Session{ID: id, mgr: s.mgr, owner: owner, nativeBrowser: browser, lastUsed: now}
	operationKey := browserAuthoringOperationKey(owner, operationID)
	entry := &browserAuthoringSession{session: stub, owner: owner, created: now, lastUsed: now, operationKey: operationKey}
	s.browserAuthoringMu.Lock()
	if s.browserAuthoringClosed {
		s.browserAuthoringMu.Unlock()
		writeErr(w, http.StatusServiceUnavailable, errors.New("browser authoring is shutting down"))
		return
	}
	for key, operation := range s.browserAuthoringOperations {
		if operation.closed && !operation.expires.After(now) {
			delete(s.browserAuthoringOperations, key)
		}
	}
	if operation := s.browserAuthoringOperations[operationKey]; operationKey != "" && operation != nil {
		active := s.browserAuthoring[operation.sessionID]
		if !operation.closed && active != nil && now.Sub(active.lastUsed) < browserAuthoringIdleTTL && now.Sub(active.created) < browserAuthoringMaxTTL {
			active.lastUsed = now
			remaining := min(browserAuthoringIdleTTL, browserAuthoringMaxTTL-now.Sub(active.created))
			active.timer.Reset(remaining)
			s.browserAuthoringMu.Unlock()
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusCreated, map[string]any{"session_id": active.session.ID, "expires_in": int(remaining.Seconds())})
			return
		}
		if active != nil {
			s.retireBrowserAuthoringLocked(operation.sessionID, active)
		}
		operation.closed, operation.expires = true, now.Add(browserAuthoringIdleTTL)
		s.browserAuthoringMu.Unlock()
		if active != nil {
			s.closeBrowserAuthoringEntry(active)
		}
		writeErr(w, http.StatusGone, errors.New("browser reference editor has closed; start a new operation"))
		return
	}
	ownedOperations := 0
	for _, operation := range s.browserAuthoringOperations {
		if sameBrowserAuthoringOwner(operation.owner, owner) {
			ownedOperations++
		}
	}
	if operationKey != "" && (ownedOperations >= browserAuthoringOperationOwnerLimit || len(s.browserAuthoringOperations) >= browserAuthoringOperationLimit) {
		s.browserAuthoringMu.Unlock()
		writeErr(w, http.StatusTooManyRequests, errors.New("too many recent browser reference editors; retry later"))
		return
	}
	owned := 0
	for _, current := range s.browserAuthoring {
		if sameBrowserAuthoringOwner(current.owner, owner) {
			owned++
		}
	}
	if owned >= browserAuthoringOwnerLimit || len(s.browserAuthoring) >= browserAuthoringTotalLimit {
		s.browserAuthoringMu.Unlock()
		writeErr(w, http.StatusTooManyRequests, errors.New("too many open browser reference editors; close an existing editor first"))
		return
	}
	if s.browserAuthoring == nil {
		s.browserAuthoring = make(map[string]*browserAuthoringSession)
	}
	s.browserAuthoring[id] = entry
	if operationKey != "" {
		if s.browserAuthoringOperations == nil {
			s.browserAuthoringOperations = make(map[string]*browserAuthoringOperation)
		}
		s.browserAuthoringOperations[operationKey] = &browserAuthoringOperation{owner: owner, sessionID: id, expires: now.Add(browserAuthoringMaxTTL + browserAuthoringIdleTTL)}
	}
	entry.timer = time.AfterFunc(browserAuthoringIdleTTL, func() { s.expireBrowserAuthoring(id, entry) })
	s.browserAuthoringMu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"session_id": id, "expires_in": int(browserAuthoringIdleTTL.Seconds())})
}

func (s *Server) browserRouteSession(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	id := r.PathValue("sid")
	if !isBrowserAuthoringID(id) {
		return s.session(w, r)
	}
	owner, ok := s.browserAuthoringOwner(r)
	if !ok {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return nil, false
	}
	now := time.Now()
	s.browserAuthoringMu.Lock()
	entry := s.browserAuthoring[id]
	if entry == nil || !sameBrowserAuthoringOwner(entry.owner, owner) || s.browserAuthoringClosed {
		s.browserAuthoringMu.Unlock()
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return nil, false
	}
	if now.Sub(entry.lastUsed) >= browserAuthoringIdleTTL || now.Sub(entry.created) >= browserAuthoringMaxTTL {
		s.retireBrowserAuthoringLocked(id, entry)
		s.browserAuthoringMu.Unlock()
		s.closeBrowserAuthoringEntry(entry)
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return nil, false
	}
	entry.lastUsed = now
	entry.timer.Reset(min(browserAuthoringIdleTTL, browserAuthoringMaxTTL-now.Sub(entry.created)))
	s.browserAuthoringMu.Unlock()
	return entry.session, true
}

func (s *Server) handleDeleteBrowserAuthoring(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.browserAuthoringOwner(r)
	id := r.PathValue("sid")
	s.browserAuthoringMu.Lock()
	entry := s.browserAuthoring[id]
	if !ok || entry == nil || !sameBrowserAuthoringOwner(entry.owner, owner) {
		s.browserAuthoringMu.Unlock()
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	s.retireBrowserAuthoringLocked(id, entry)
	s.browserAuthoringMu.Unlock()
	s.closeBrowserAuthoringEntry(entry)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// The owned list makes otherwise hidden authoring contexts observable to
// release/logout guards without exposing another workspace's browser handles.
func (s *Server) handleListBrowserAuthoring(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.browserAuthoringOwner(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("browser authoring requires an account"))
		return
	}
	type item struct {
		SessionID string `json:"session_id"`
		ExpiresIn int    `json:"expires_in"`
	}
	items := []item{}
	expired := []*browserAuthoringSession{}
	now := time.Now()
	s.browserAuthoringMu.Lock()
	for id, entry := range s.browserAuthoring {
		if !sameBrowserAuthoringOwner(entry.owner, owner) {
			continue
		}
		remaining := min(browserAuthoringIdleTTL-now.Sub(entry.lastUsed), browserAuthoringMaxTTL-now.Sub(entry.created))
		if remaining <= 0 {
			s.retireBrowserAuthoringLocked(id, entry)
			expired = append(expired, entry)
			continue
		}
		items = append(items, item{SessionID: id, ExpiresIn: int(remaining.Seconds())})
	}
	s.browserAuthoringMu.Unlock()
	for _, entry := range expired {
		s.closeBrowserAuthoringEntry(entry)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"sessions": items, "active": len(items)})
}

func (s *Server) expireBrowserAuthoring(id string, expected *browserAuthoringSession) {
	now := time.Now()
	s.browserAuthoringMu.Lock()
	entry := s.browserAuthoring[id]
	if entry != expected {
		s.browserAuthoringMu.Unlock()
		return
	}
	remaining := min(browserAuthoringIdleTTL-now.Sub(entry.lastUsed), browserAuthoringMaxTTL-now.Sub(entry.created))
	if remaining > 0 {
		entry.timer.Reset(remaining)
		s.browserAuthoringMu.Unlock()
		return
	}
	s.retireBrowserAuthoringLocked(id, entry)
	s.browserAuthoringMu.Unlock()
	s.closeBrowserAuthoringEntry(entry)
}

func (s *Server) retireBrowserAuthoringLocked(id string, entry *browserAuthoringSession) {
	delete(s.browserAuthoring, id)
	entry.timer.Stop()
	if operation := s.browserAuthoringOperations[entry.operationKey]; operation != nil {
		operation.closed = true
		operation.expires = time.Now().Add(browserAuthoringIdleTTL)
	}
}

func (s *Server) closeBrowserAuthoringEntry(entry *browserAuthoringSession) {
	id := entry.session.ID
	s.browserLiveMu.Lock()
	for token, ticket := range s.browserLiveTickets {
		if ticket.sid == id {
			delete(s.browserLiveTickets, token)
		}
	}
	var connections []*nativeBrowserLiveConnection
	for connection := range s.browserLiveConnections {
		if connection.sid == id {
			connections = append(connections, connection)
		}
	}
	s.browserLiveMu.Unlock()
	for _, connection := range connections {
		connection.close()
	}
	entry.session.nativeBrowser.Close()
}

func (s *Server) closeBrowserAuthoring() {
	s.browserAuthoringMu.Lock()
	s.browserAuthoringClosed = true
	entries := s.browserAuthoring
	s.browserAuthoring = nil
	s.browserAuthoringOperations = nil
	for _, entry := range entries {
		entry.timer.Stop()
	}
	s.browserAuthoringMu.Unlock()
	for _, entry := range entries {
		s.closeBrowserAuthoringEntry(entry)
	}
}
