package web

// Office is a live editor service, separate from read-only file rendering.
// Every editor belongs to an authenticated conversation and a checked file.
// Agent links carry that trusted identity; they never receive bridge secrets.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/tools"
)

const officeMaxBytes = 20 << 20
const officeMaxJSON = 1 << 20
const officeMaxOperations = 512
const officeMaxRetainedBytes = 16 << 20

// A full normal ledger still has one bounded export slot so its current
// canvas can be saved before the editor is closed and reopened.
const officeFinalSaveBytes = officeMaxJSON + 256

var officeOperationID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,100}$`)
var officeSessionID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

// OfficeOptions is operator configuration, never a model-authored URL.
// Nil and an entirely empty environment leave Office disabled.
type OfficeOptions struct {
	BrowserURL     string
	ServiceURL     string
	PublicURL      string
	FetchURL       string
	JWTSecretFile  string
	CommandTimeout time.Duration
	SaveTimeout    time.Duration
}

type officeConfig struct {
	browserURL, serviceURL, publicURL, fetchURL string
	browserOrigin, serviceOrigin                string
	secret                                      []byte
	client                                      *http.Client
	commandTimeout, saveTimeout                 time.Duration
}

func officeEnvironment() *OfficeOptions {
	options := &OfficeOptions{
		BrowserURL:    os.Getenv("POGO_OFFICE_BROWSER_URL"),
		ServiceURL:    os.Getenv("POGO_OFFICE_SERVICE_URL"),
		PublicURL:     os.Getenv("POGO_OFFICE_PUBLIC_URL"),
		FetchURL:      os.Getenv("POGO_OFFICE_FETCH_URL"),
		JWTSecretFile: os.Getenv("POGO_OFFICE_JWT_SECRET_FILE"),
	}
	if options.BrowserURL == "" && options.ServiceURL == "" && options.PublicURL == "" && options.FetchURL == "" && options.JWTSecretFile == "" {
		return nil
	}
	return options
}

func officeBaseURL(value, label string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("%s must be an operator-configured http(s) origin without credentials, path, query or fragment", label)
	}
	return u.Scheme + "://" + u.Host, nil
}

func parseOfficeConfig(options *OfficeOptions) (*officeConfig, error) {
	if options == nil {
		return nil, nil
	}
	cfg := &officeConfig{}
	var err error
	// BrowserURL is retained only as an operator-side legacy upstream fallback.
	// No tool-container address is ever returned to a browser.
	legacyBrowser := ""
	if options.BrowserURL != "" {
		if legacyBrowser, err = officeBaseURL(options.BrowserURL, "POGO_OFFICE_BROWSER_URL"); err != nil {
			return nil, err
		}
	}
	// PublicURL is optional legacy operator metadata. Browser-facing URLs and
	// credentials follow the authorized editor request, never this origin.
	if options.PublicURL != "" {
		if cfg.publicURL, err = officeBaseURL(options.PublicURL, "POGO_OFFICE_PUBLIC_URL"); err != nil {
			return nil, err
		}
	}
	service, fetch := options.ServiceURL, options.FetchURL
	if service == "" {
		service = legacyBrowser
	}
	if fetch == "" {
		// Preserve old, explicitly configured server-side fetch origins. New
		// installations supply their internal Pogo address without PublicURL.
		fetch = cfg.publicURL
	}
	if cfg.serviceURL, err = officeBaseURL(service, "POGO_OFFICE_SERVICE_URL"); err != nil {
		return nil, err
	}
	if cfg.fetchURL, err = officeBaseURL(fetch, "POGO_OFFICE_FETCH_URL"); err != nil {
		return nil, err
	}
	secret, err := os.ReadFile(options.JWTSecretFile)
	if err != nil {
		return nil, errors.New("Office JWT secret file is unavailable")
	}
	secret = bytes.TrimSpace(secret)
	if len(secret) < 32 || len(secret) > 8192 || bytes.ContainsAny(secret, "\r\n") {
		return nil, errors.New("Office JWT secret must be 32 to 8192 bytes without line breaks")
	}
	cfg.secret = secret
	if cfg.serviceURL == cfg.publicURL {
		return nil, errors.New("Office ServiceURL must name an internal editor upstream, not Pogo PublicURL")
	}
	cfg.browserURL = officeGatewayPrefix
	cfg.serviceOrigin = cfg.serviceURL
	cfg.commandTimeout, cfg.saveTimeout = options.CommandTimeout, options.SaveTimeout
	if cfg.commandTimeout <= 0 {
		cfg.commandTimeout = 35 * time.Second
	}
	if cfg.saveTimeout <= 0 {
		cfg.saveTimeout = 35 * time.Second
	}
	cfg.client = &http.Client{Timeout: officeOpenHTTPBudget, Transport: officeUpstreamTransport(), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return cfg, nil
}

// Opening a cold editor must not hit an unrelated 10/25/30-second transport
// deadline first. Short health checks still carry their own request context;
// command/save deadlines remain independent and unchanged.
func officeUpstreamTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: officeOpenHTTPBudget, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = officeOpenHTTPBudget
	transport.ResponseHeaderTimeout = officeOpenHTTPBudget
	return transport
}

func (s *Server) initOffice() error {
	options := s.opts.Office
	if options == nil {
		options = officeEnvironment()
	}
	s.officeConfigured = options != nil || os.Getenv("POGO_OFFICE_UNAVAILABLE_REASON") == "config_error"
	cfg, err := parseOfficeConfig(options)
	if err != nil {
		// Optional editor configuration must never prevent core startup.
		// Keep paths, secrets and operator URLs out of client diagnostics.
		s.officeConfigError = true
		return nil
	}
	if cfg == nil {
		s.officeConfigError = s.officeConfigured
		return nil
	}
	if s.mgr.officeForSession != nil {
		return errors.New("Office is already configured for this Manager")
	}
	s.office = &officeService{cfg: cfg, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}, manager: s.mgr}
	s.office.gateway = newOfficeGateway(s)
	if err := s.office.restoreEditorJournal(); err != nil {
		s.office.editorJournalError = true
	}
	s.mgr.officeForSession = s.office.link
	s.mgr.officeCommandProtection = s.office.commandProtectionManifest
	s.mgr.officeRegisterDocument = s.office.registerCommandProtectedPath
	s.mgr.officeBeforeDelete = s.office.beforeDeleteConversation
	s.mgr.officeRevokeSession = s.office.revokeConversation
	s.mgr.officeSessionActive = s.office.conversationActive
	return nil
}

func (s *Server) closeOffice() {
	if s.office != nil {
		if s.office.gateway != nil {
			s.office.gateway.close()
		}
		s.office.close()
	}
}

type officeError struct {
	status        int
	code, message string
}

func (e *officeError) Error() string                    { return e.message }
func officeFail(status int, code, message string) error { return &officeError{status, code, message} }
func (e *officeError) OfficeToolError() json.RawMessage {
	reply := officeErrorReply(e)
	// Only the bound tool's validation failures make this no-write claim. A
	// malformed HTTP receipt can occur after an unrelated command was sent.
	switch e.code {
	case "INVALID_ARGUMENT", "WRONG_EDITOR", "UNSUPPORTED_OPERATION", "UNSUPPORTED_TEXT_SHAPE", "INVALID_READ_REFERENCE":
		reply["writeOccurred"] = false
	}
	encoded, _ := json.Marshal(reply)
	return encoded
}
func officeErrorReply(err error) map[string]any {
	var typed *officeError
	if errors.As(err, &typed) {
		return map[string]any{"ok": false, "error": map[string]any{"code": typed.code, "message": typed.message}}
	}
	return map[string]any{"ok": false, "error": map[string]any{"code": "OFFICE_ERROR", "message": "Office 操作失败，当前画布内容仍保留。"}}
}

type officeSessionView struct {
	ReadOnly            bool                `json:"readOnly,omitempty"`
	ReplacementPending  bool                `json:"replacementPending"`
	SyncRevision        uint64              `json:"syncRevision"`
	CanvasVersion       string              `json:"canvasVersion"`
	DiskVersion         string              `json:"diskVersion"`
	ExternalChange      bool                `json:"externalChange"`
	ReloadAvailable     bool                `json:"reloadAvailable"`
	VersionError        string              `json:"versionError,omitempty"`
	ReplacedBy          string              `json:"replacedBy,omitempty"`
	ReloadConfirmed     bool                `json:"reloadConfirmed"`
	Batch               *officeFreezeView   `json:"batch,omitempty"`
	ID                  string              `json:"id"`
	DocumentID          string              `json:"documentId"`
	ConversationID      string              `json:"conversationId"`
	Path                string              `json:"path"`
	FileRef             string              `json:"fileRef"`
	BootEpoch           string              `json:"bootEpoch"`
	Title               string              `json:"title"`
	FileType            string              `json:"fileType"`
	Editor              string              `json:"editor"`
	Generation          uint64              `json:"generation"`
	Ready               bool                `json:"ready"`
	InstanceBound       bool                `json:"instanceBound"`
	CanvasMounted       bool                `json:"canvasMounted"`
	Closed              bool                `json:"closed"`
	Dirty               bool                `json:"dirty"`
	DirtyEpoch          uint64              `json:"dirtyEpoch"`
	PersistedEpoch      uint64              `json:"persistedEpoch"`
	SaveState           string              `json:"saveState"`
	Revision            uint64              `json:"revision"`
	SavedAt             string              `json:"savedAt,omitempty"`
	Blocked             bool                `json:"blocked"`
	Recovery            *officeRecoveryView `json:"recovery,omitempty"`
	Error               string              `json:"error,omitempty"`
	Capabilities        json.RawMessage     `json:"capabilities,omitempty"`
	SupportedOperations []string            `json:"supportedOperations"`
	Pending             bool                `json:"pending"`
	QueueDepth          int                 `json:"queueDepth"`
	SavePhase           string              `json:"savePhase"`
	Activity            officeActivityView  `json:"activity,omitzero"`
	LeaseEpoch          uint64              `json:"leaseEpoch"`
	LastSeenAt          string              `json:"lastSeenAt,omitempty"`
	LeaseExpiresAt      string              `json:"leaseExpiresAt,omitempty"`
	ConnectionState     string              `json:"connectionState"`
	RecoverySealed      bool                `json:"recoverySealed"`
	RecoveryCopy        *officeRecoveryCopy `json:"recoveryCopy,omitempty"`
}

type officeOperation struct {
	id, name, fingerprint string
	args                  json.RawMessage
	dispatched, finished  bool
	result                json.RawMessage
	done                  chan struct{}
	timer                 *time.Timer
	epoch                 uint64
	cancel                context.CancelFunc
	deadline              time.Time
	fetchContext          context.Context
	requestContext        context.Context
	resultReservation     int
	activitySequence      uint64
	committedRevision     uint64
	saveShared            bool
	prerequisites         []*officeOperation
	unknown, reconciled   bool
	unknownDirtyEpoch     uint64
	recoveryReadRevision  uint64
	reconciliationResult  json.RawMessage
}

type officeDocumentSession struct {
	readOnly                                                           bool
	informationSource                                                  *informationOfficeViewSource
	informationSnapshot                                                *informationOfficeViewSnapshot
	viewCreatedAt                                                      time.Time
	viewTimer                                                          *time.Timer
	leaseEpoch                                                         uint64
	lastSeenAt, leaseExpiresAt                                         time.Time
	restarted, recoverySealed                                          bool
	recoveryCopy                                                       *officeRecoveryCopy
	journalError                                                       string
	legacyImported                                                     bool
	legacyEvidenceFile, legacyEvidenceSHA256                           string
	handoffFromSessionID, handoffFingerprint                           string
	journalFrozen, retirementAuthorized                                bool
	callbackOnly                                                       bool
	diskInspectMu                                                      sync.Mutex
	diskObservedAt                                                     time.Time
	diskSignature                                                      string
	diskReadCount                                                      uint64 // observable for cache regression checks; guarded by mu
	replacementPending                                                 bool
	syncRevision                                                       uint64
	diskVersion, versionError, replacedBy                              string
	externalChange, reloadConfirmed                                    bool
	freeze                                                             *officeFreeze
	batchLease                                                         string
	mu                                                                 sync.Mutex
	id, documentID, conversationID, cwd, absolute, relative, panelPath string
	title, fileType, editor, key, bridgeToken, fileToken, instanceID   string
	browserOrigin                                                      string // bound to the authenticated viewer instance, guarded by mu
	bootEpoch, bootstrapToken                                          string
	owner                                                              *identity.Scope
	hub                                                                *Hub
	generation, dirtyEpoch, persistedEpoch, revision                   uint64
	ready, closed, blocked, saving, exportBusy                         bool
	canvasMounted                                                      bool
	savedAt, saveState, lastError                                      string
	capabilities                                                       json.RawMessage
	snapshot                                                           []byte
	baseHash                                                           string
	baseInfo, parentInfo                                               os.FileInfo
	parent                                                             *os.Root
	operations                                                         map[string]*officeOperation
	queue                                                              []*officeOperation
	exportIntent                                                       *officeOperation
	journal                                                            tools.Journal
	retainedBytes, reservedBytes                                       int
	finalSaveUsed                                                      bool
	role                                                               *officeRole
	recoveryRevision                                                   uint64
	recoveryChanged                                                    chan struct{}
	recoveryNeedsRead                                                  bool
	recoveryReadTarget                                                 *officeRecoveryReadTarget
	// A config issued through the release gate may admit only this document's
	// file download and signed callback during the same maintenance epoch.
	releaseGrant    [32]byte
	activityVersion uint64
	activity        []officeActivity
}

func (d *officeDocumentSession) viewLocked() officeSessionView {
	return d.viewProjectionLocked(true)
}

func (d *officeDocumentSession) viewProjectionLocked(includeActivity bool) officeSessionView {
	queueDepth := 0
	for _, op := range d.queue {
		if !op.finished {
			queueDepth++
		}
	}
	savePhase := ""
	if d.saving {
		savePhase = "queued"
		if d.exportIntent != nil && d.exportIntent.dispatched {
			savePhase = "exporting"
		}
		if d.exportBusy {
			savePhase = "committing"
		}
	}
	operations := []string{}
	var cap officeCapabilities
	if d.ready && !d.closed && !d.restarted && !d.recoverySealed && json.Unmarshal(d.capabilities, &cap) == nil && cap.safe() {
		operations = append(operations, "capabilities")
		if d.freeze == nil && d.batchLease == "" {
			if cap.SelectionRead {
				operations = append(operations, "selection.read")
			}
			if d.editor == "word" {
				operations = append(operations, "word.read")
				if !d.blocked && !d.externalChange && !d.replacementPending && cap.WordReplace {
					operations = append(operations, "word.replace")
				}
			} else if d.editor == "cell" {
				if cap.SheetList {
					operations = append(operations, "sheet.list")
				}
				operations = append(operations, "sheet.read")
				if !d.blocked && !d.externalChange && !d.replacementPending && cap.SheetApply {
					operations = append(operations, "sheet.apply")
				}
			}
			if !d.blocked && !d.externalChange && !d.replacementPending && cap.NativeUndo {
				operations = append(operations, "history.undo")
			}
			if !d.blocked && !d.externalChange && !d.replacementPending && cap.NativeRedo {
				operations = append(operations, "history.redo")
			}
		}
	}
	view := officeSessionView{ReplacementPending: d.replacementPending, SyncRevision: d.syncRevision, CanvasVersion: d.baseHash, DiskVersion: d.diskVersion, ExternalChange: d.externalChange, VersionError: d.versionError, ReplacedBy: d.replacedBy, ReloadConfirmed: d.reloadConfirmed, ReloadAvailable: d.externalChange && !d.closed && !d.blocked && d.dirtyEpoch == d.persistedEpoch && !d.saving && !d.exportBusy && queueDepth == 0 && d.freeze == nil, Batch: d.freezeViewLocked(), ID: d.id, DocumentID: d.documentID, ConversationID: d.conversationID,
		Path: d.panelPath, FileRef: officeFileRef(d.owner, d.panelPath), BootEpoch: d.bootEpoch, Title: d.title, FileType: d.fileType, Editor: d.editor, Generation: d.generation,
		Ready: d.ready && !d.restarted && !d.recoverySealed && !d.closed, InstanceBound: d.instanceID != "", CanvasMounted: d.canvasMounted, Closed: d.closed, Dirty: d.dirtyEpoch > d.persistedEpoch,
		DirtyEpoch: d.dirtyEpoch, PersistedEpoch: d.persistedEpoch, SaveState: d.saveState,
		Revision: d.revision, SavedAt: d.savedAt, Blocked: d.blocked, Recovery: d.recoveryViewLocked(), Error: d.lastError,
		Capabilities: append(json.RawMessage(nil), d.capabilities...), SupportedOperations: operations, Pending: queueDepth > 0, QueueDepth: queueDepth, SavePhase: savePhase}
	view.ReadOnly = d.readOnly
	if d.readOnly {
		view.SupportedOperations = []string{}
		if d.ready && !d.closed && cap.safe() {
			view.SupportedOperations = append(view.SupportedOperations, "capabilities")
			if cap.SelectionRead {
				view.SupportedOperations = append(view.SupportedOperations, "selection.read")
			}
		}
	}
	if includeActivity {
		view.Activity = d.activityViewLocked()
	}
	view.LeaseEpoch, view.ConnectionState, view.RecoverySealed = d.leaseEpoch, d.connectionStateLocked(time.Now()), d.recoverySealed
	if !d.lastSeenAt.IsZero() {
		view.LastSeenAt = d.lastSeenAt.UTC().Format(time.RFC3339Nano)
	}
	if !d.leaseExpiresAt.IsZero() {
		view.LeaseExpiresAt = d.leaseExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	if d.recoveryCopy != nil {
		copy := *d.recoveryCopy
		view.RecoveryCopy = &copy
	}
	return view
}
func (d *officeDocumentSession) view() officeSessionView {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.viewLocked()
}

func (d *officeDocumentSession) metadataView() officeSessionView {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.viewProjectionLocked(false)
}

type officeService struct {
	viewReceipts         map[string]informationOfficeViewReceipt
	readinessMu          sync.Mutex
	readinessUntil       time.Time
	readinessReady       bool
	readinessPending     chan struct{}
	openReadinessPending chan struct{}
	openReadinessNext    time.Time
	batchMu              sync.Mutex
	versionMu            sync.Mutex
	commandProtectionMu  sync.Mutex
	commandProtection    map[string]*officeCommandProtectionState
	batches              map[string]*officeBatch
	batchLeases          map[string]string // guarded by mu; canonical absolute path -> batch UUID
	batchDir             string            // private receipt root; tests may inject a private temporary directory
	mu                   sync.Mutex
	cfg                  *officeConfig
	sessions             map[string]*officeDocumentSession
	active               map[string]string
	generation           uint64
	closed               bool
	manager              *Manager
	tombstones           []string
	roles                map[string]*officeRole
	bootEpoch            string
	gateway              *officeGateway
	editorJournalError   bool
	recoveryArchiveMu    sync.Mutex
}

type officeRole struct{ revoked atomic.Bool }

type officeLink struct {
	service             *officeService
	conversationID, cwd string
	owner               *identity.Scope
	hub                 *Hub
	role                *officeRole
	commandSession      *Session // trusted command permission, bound only during tool registration
	commandEnabled      bool
}

func officeCopyOwner(owner *identity.Scope) *identity.Scope {
	if owner == nil {
		return nil
	}
	copy := *owner
	return &copy
}
func officeSameOwner(a, b *identity.Scope) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
func officeHash(data []byte) string { hash := sha256.Sum256(data); return hex.EncodeToString(hash[:]) }
func officeEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (o *officeService) link(id, cwd string, owner *identity.Scope, hub *Hub) tools.OfficeLink {
	o.mu.Lock()
	role := o.roleLocked(id)
	o.mu.Unlock()
	return &officeLink{service: o, conversationID: id, cwd: cwd, owner: officeCopyOwner(owner), hub: hub, role: role}
}

func (o *officeService) roleLocked(id string) *officeRole {
	if o.roles == nil {
		o.roles = map[string]*officeRole{}
	}
	role := o.roles[id]
	if role == nil {
		role = &officeRole{}
		if o.closed {
			role.revoked.Store(true)
		}
		o.roles[id] = role
	}
	return role
}

type officeToolRequest struct {
	BatchID           string          `json:"batch_id,omitempty"`
	ExpectedVersion   string          `json:"expected_version,omitempty"`
	Action            string          `json:"action"`
	Path              string          `json:"path,omitempty"`
	DocumentSessionID string          `json:"document_session_id,omitempty"`
	OperationID       string          `json:"operation_id,omitempty"`
	Operation         string          `json:"operation,omitempty"`
	Args              json.RawMessage `json:"args,omitempty"`
	Script            string          `json:"script,omitempty"`
	Timeout           float64         `json:"timeout,omitempty"`
	VersionID         string          `json:"version_id,omitempty"`
	RestoreSide       string          `json:"restore_side,omitempty"`
	Cursor            string          `json:"cursor,omitempty"`
	Limit             int             `json:"limit,omitempty"`
}

func officeDecodeJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return officeFail(400, "INVALID_ARGUMENT", "Office 请求字段不正确。")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return officeFail(400, "INVALID_ARGUMENT", "Office 请求只能包含一个 JSON 对象。")
	}
	return nil
}

func (l *officeLink) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if l.role.revoked.Load() {
		return nil, officeFail(409, "CONVERSATION_CLOSED", "当前会话的 Office 操作身份已撤销。")
	}
	var request officeToolRequest
	if len(raw) > officeMaxJSON {
		return nil, officeFail(413, "REQUEST_LIMIT", "Office 请求过大。")
	}
	if err := officeDecodeJSON(raw, &request); err != nil {
		return nil, err
	}
	var reply any
	switch request.Action {
	case "transform":
		return l.executeTransform(ctx, request)
	case "versions":
		page, err := l.service.listVersions(ctx, l, request.Path, request.Cursor, request.Limit)
		if err != nil {
			return nil, err
		}
		return json.Marshal(page)
	case "restore_version":
		restored, err := l.service.restoreVersionSide(ctx, l, request.Path, request.VersionID, request.ExpectedVersion, request.OperationID, request.RestoreSide, officeActivityAgent)
		if err != nil {
			return nil, err
		}
		return json.Marshal(restored)
	case "batch_prepare", "batch_commit", "batch_abort", "batch_status", "reload":
		return l.executeBatch(ctx, request)
	case "list":
		reply = map[string]any{"sessions": l.service.list(l.conversationID, l.owner)}
	case "open":
		absolute, err := l.resolvePath(request.Path)
		if err != nil {
			return nil, officeFail(400, "PATH_REJECTED", "文档不在当前会话工作区。")
		}
		panelPath, err := officePanelPath(l.workspaceRoot(), absolute)
		if err != nil {
			return nil, err
		}
		if err := l.service.waitEditorReady(ctx); err != nil {
			return nil, err
		}
		var journal tools.Journal
		if l.service.manager != nil {
			journal = l.service.manager.JournalFor(l.workspaceRoot()).ForSession(l.conversationID)
		}
		d, err := l.service.openRole(l.conversationID, l.cwd, l.owner, l.hub, absolute, panelPath, journal, l.role)
		if err != nil {
			return nil, err
		}
		view := d.metadataView()
		if l.hub != nil {
			l.hub.Publish(Event{Type: "office_open", OfficeDocumentSessionID: view.ID, OfficeConversationID: l.conversationID, OfficePath: view.Path, OfficeFileType: view.FileType})
		}
		reply = map[string]any{"session": view}
	case "command", "save", "close":
		d, err := l.service.forLink(l, request.DocumentSessionID)
		if err != nil {
			return nil, err
		}
		switch request.Action {
		case "command":
			result, err := l.service.command(ctx, d, request.OperationID, request.Operation, request.Args)
			if err != nil {
				return nil, err
			}
			return result, nil
		case "save":
			view, err := l.service.save(ctx, d, request.OperationID)
			if err != nil {
				return nil, err
			}
			// Display history is delivered to the editor, not repeated in the
			// model context after every save.
			view.Activity = officeActivityView{}
			// An idempotent save receipt describes that operation's committed
			// version. Later human input may be dirty even when its receipt was
			// clean; expose current state without replaying an export.
			reply = map[string]any{"ok": true, "session": view, "savedRevision": view.Revision, "savedEpoch": view.PersistedEpoch, "currentSession": d.metadataView()}
		case "close":
			if err := l.service.closeDocument(d); err != nil {
				return nil, err
			}
			reply = map[string]any{"ok": true, "session": d.metadataView()}
		}
	default:
		return nil, officeFail(400, "INVALID_ACTION", "Office action 必须是 list、open、command、save 或 close。")
	}
	return json.Marshal(reply)
}

func (o *officeService) forLink(l *officeLink, id string) (*officeDocumentSession, error) {
	o.mu.Lock()
	d := o.sessions[id]
	o.mu.Unlock()
	if d == nil || d.conversationID != l.conversationID || d.cwd != l.cwd || !officeSameOwner(d.owner, l.owner) || d.role != l.role || l.role.revoked.Load() {
		return nil, officeFail(404, "DOCUMENT_NOT_FOUND", "此文档不属于当前会话。")
	}
	return d, nil
}

// OfficeCompletion supplies in-process loop bookkeeping from the same scoped
// host ledger used for editor/save operations. It does no I/O and never infers
// agent work from a document's dirty flag (which also includes human edits).
func (l *officeLink) OfficeCompletion(documentSessionID, operationID string) *tools.OfficeCompletionReceipt {
	d, err := l.service.forLink(l, documentSessionID)
	if err != nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	receipt := &tools.OfficeCompletionReceipt{DocumentSessionID: d.id, PersistedEpoch: d.persistedEpoch, Blocked: d.blocked}
	if op := d.operations[operationID]; op != nil && op.finished && officeMutation(op.name) {
		// epoch is nonzero only after a successful native mutation receipt.
		receipt.MutationEpoch = op.epoch
	}
	return receipt
}

func (o *officeService) signJWT(payload any) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body := header + "." + base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, o.cfg.secret)
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (o *officeService) verifyJWT(token string) (map[string]json.RawMessage, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(parts[0]) > 200 || len(parts[1]) > officeMaxJSON {
		return nil, officeFail(403, "INVALID_SIGNATURE", "编辑服务签名无效。")
	}
	h, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, officeFail(403, "INVALID_SIGNATURE", "编辑服务签名无效。")
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if json.Unmarshal(h, &header) != nil || header.Alg != "HS256" {
		return nil, officeFail(403, "INVALID_SIGNATURE", "编辑服务签名无效。")
	}
	mac := hmac.New(sha256.New, o.cfg.secret)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if !officeEqual(parts[2], base64.RawURLEncoding.EncodeToString(mac.Sum(nil))) {
		return nil, officeFail(403, "INVALID_SIGNATURE", "编辑服务签名无效。")
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, officeFail(403, "INVALID_SIGNATURE", "编辑服务签名无效。")
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(b, &claims) != nil {
		return nil, officeFail(403, "INVALID_SIGNATURE", "编辑服务签名无效。")
	}
	if value := claims["exp"]; len(value) > 0 {
		var exp int64
		if json.Unmarshal(value, &exp) != nil || exp < time.Now().Unix() {
			return nil, officeFail(403, "INVALID_SIGNATURE", "编辑服务签名已过期。")
		}
	}
	return claims, nil
}

func officeNewID() string { return uuid.NewString() }

func officePanelPath(root, absolute string) (string, error) {
	canonical, err := tools.Resolve(root, ".")
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(canonical, absolute)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", officeFail(400, "PATH_REJECTED", "文档不在当前账号工作区。")
	}
	return filepath.ToSlash(relative), nil
}
