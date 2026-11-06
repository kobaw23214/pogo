package webservice

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Options struct {
	Root           string
	Installation   string
	Runner         Runner
	Gateway        Gateway
	OnChange       func(Record)
	PollInterval   time.Duration
	HealthInterval time.Duration
	StartTimeout   time.Duration
	Now            func() time.Time
}

type operation struct {
	Action     string    `json:"action"`
	Digest     string    `json:"digest"`
	ServiceID  string    `json:"service_id"`
	Generation int       `json:"generation"`
	Status     string    `json:"status"`
	Created    time.Time `json:"created"`
	Error      *Error    `json:"error,omitempty"`
}
type diskState struct {
	Version          int                        `json:"version"`
	Installation     string                     `json:"installation"`
	Records          map[string]Record          `json:"records"`
	History          map[string]map[int]Record  `json:"history"`
	Operations       map[string]operation       `json:"operations"`
	Deleted          map[string]bool            `json:"deleted"`
	DeletedOwners    map[string]bool            `json:"deleted_owners"`
	DeletingServices map[string]bool            `json:"deleting_services"`
	PreviewDomains   []string                   `json:"preview_domains,omitempty"`
	Policy           *Policy                    `json:"policy,omitempty"`
	PolicyRestarts   map[string]SettingsRestart `json:"policy_restarts"`
}

type Manager struct {
	mu                sync.Mutex
	settingsMu        sync.Mutex
	options           Options
	disk              diskState
	ctx               context.Context
	cancel            context.CancelFunc
	wg                sync.WaitGroup
	closed            bool
	locks             map[string]*sync.Mutex
	tails             map[string]chan struct{}
	busy              map[string]int
	launchCancel      map[string]context.CancelFunc
	grants            map[string]Grant
	grantEpoch        uint64
	nextHealth        map[string]time.Time
	failures          map[string]int
	successes         map[string]int
	profiles          []string
	workspaceResolver func(owner, session string) (string, error)
}

var serviceName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
var operationName = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func NewManager(o Options) (*Manager, error) {
	if o.Runner == nil || o.Gateway == nil {
		return nil, Fail("FEATURE_DISABLED", "runner and gateway are required")
	}
	if !filepath.IsAbs(o.Root) {
		return nil, errors.New("web service private root must be absolute")
	}
	if err := os.MkdirAll(o.Root, 0700); err != nil {
		return nil, err
	}
	for p := filepath.Clean(o.Root); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("web service private storage must not traverse symlinks")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if err := os.Chmod(o.Root, 0700); err != nil {
		return nil, err
	}
	if o.PollInterval <= 0 {
		o.PollInterval = time.Second
	}
	if o.HealthInterval <= 0 {
		o.HealthInterval = 5 * time.Second
	}
	if o.StartTimeout <= 0 {
		o.StartTimeout = 60 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{options: o, ctx: ctx, cancel: cancel, locks: map[string]*sync.Mutex{}, tails: map[string]chan struct{}{}, busy: map[string]int{}, launchCancel: map[string]context.CancelFunc{}, grants: map[string]Grant{}, nextHealth: map[string]time.Time{}, failures: map[string]int{}, successes: map[string]int{}, profiles: []string{"python-web"}}
	m.disk = diskState{Version: 1, Installation: o.Installation, Records: map[string]Record{}, History: map[string]map[int]Record{}, Operations: map[string]operation{}, Deleted: map[string]bool{}, DeletedOwners: map[string]bool{}}
	data, err := os.ReadFile(filepath.Join(o.Root, "registry.json"))
	if err == nil {
		if len(data) > 16<<20 {
			return nil, errors.New("service registry is too large")
		}
		if err = json.Unmarshal(data, &m.disk); err != nil {
			return nil, err
		}
		if m.disk.Version != 1 || m.disk.Records == nil || m.disk.History == nil || m.disk.Operations == nil || m.disk.Deleted == nil {
			return nil, errors.New("invalid service registry")
		}
		if o.Installation != "" && o.Installation != m.disk.Installation {
			return nil, errors.New("service installation does not match registry")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if m.disk.Installation == "" {
		m.disk.Installation = randomID("inst_")
	}
	if m.disk.DeletedOwners == nil {
		m.disk.DeletedOwners = map[string]bool{}
	}
	if m.disk.DeletingServices == nil {
		m.disk.DeletingServices = map[string]bool{}
	}
	if m.disk.Policy == nil {
		policy := DefaultPolicy()
		m.disk.Policy = &policy
	}
	if err := m.disk.Policy.Validate(); err != nil {
		return nil, err
	}
	if m.disk.PolicyRestarts == nil {
		m.disk.PolicyRestarts = map[string]SettingsRestart{}
	}
	for id, r := range m.disk.Records {
		if id != r.ServiceID || r.Installation != m.disk.Installation || r.Generation < 1 || r.Owner == "" || r.Session == "" {
			return nil, errors.New("invalid service identity in registry")
		}
		if r.LeaseID == "" {
			r.LeaseID = randomID("lease_")
		}
		if r.DisplayName == "" {
			r.DisplayName = displayName(r)
		}
		if m.disk.DeletingServices[id] {
			r.State = "stopping"
			r.Phase = "deleting"
		} else if active(r.State) && r.State != "stopping" {
			r.State = "reconciling"
			r.Revision++
			r.UpdatedAt = o.Now()
		}
		m.disk.Records[id] = r
	}
	if err = m.write(m.disk); err != nil {
		cancel()
		return nil, err
	}
	m.wg.Add(1)
	go m.maintain()
	return m, nil
}

func (m *Manager) Root() string { return m.options.Root }
func (m *Manager) Preflight(ctx context.Context) error {
	if err := m.options.Runner.Preflight(ctx); err != nil {
		return err
	}
	if runner, ok := m.options.Runner.(interface {
		Profiles(context.Context) ([]string, error)
	}); ok {
		profiles, err := runner.Profiles(ctx)
		if err != nil {
			return err
		}
		available := []string{}
		for _, profile := range profiles {
			if profile == "python-web" || profile == "node-web" {
				available = append(available, profile)
			}
		}
		m.mu.Lock()
		m.profiles = available
		m.mu.Unlock()
	}
	err := m.options.Gateway.Preflight(ctx)
	if err != nil {
		m.invalidateGrantCache()
	}
	return err
}

// A gateway control failure can replace authority for every service, not just
// the one whose health check first observed it. Never replay an old receipt
// after that boundary, including one being opened concurrently.
func (m *Manager) invalidateGrantCache() {
	m.mu.Lock()
	m.grantEpoch++
	m.grants = map[string]Grant{}
	m.mu.Unlock()
}

type boundBackend struct {
	manager *Manager
	scope   Scope
}

func (m *Manager) Bind(s Scope) Backend { return &boundBackend{m, s} }
func (b *boundBackend) Execute(ctx context.Context, r Request) (json.RawMessage, error) {
	return b.manager.Execute(ctx, b.scope, r)
}

func randomID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b[:])
}
func digest(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func scopeKey(s Scope) string { return digest([]string{s.Owner, s.Session}) }
func active(s string) bool {
	return s != "stopped" && s != "failed" && s != "interrupted" && s != "expired"
}

// PublicRecord deliberately omits the internal runner address and ownership
// wire fields. Callers must never publish Record directly to a user/event hub.
func PublicRecord(r Record) json.RawMessage {
	data, _ := json.Marshal(r)
	var v map[string]any
	_ = json.Unmarshal(data, &v)
	for _, k := range []string{"owner", "session", "installation", "instance", "control_id", "workspace"} {
		delete(v, k)
	}
	v["name"] = displayName(r)
	v["slot_name"] = r.Spec.Name
	v["profile"] = r.Spec.Profile
	v["project_root"] = r.Spec.ProjectRoot
	data, _ = json.Marshal(v)
	return data
}
func receipt(r Record, opID string, accepted bool) json.RawMessage {
	var v map[string]any
	_ = json.Unmarshal(PublicRecord(r), &v)
	if opID != "" {
		v["operation_id"] = opID
		v["accepted"] = accepted
	}
	b, _ := json.Marshal(v)
	return b
}

func operationReceipt(r Record, id string, op operation) json.RawMessage {
	var v map[string]any
	_ = json.Unmarshal(receipt(r, id, true), &v)
	v["operation_status"] = op.Status
	if op.Error != nil {
		v["operation_error"] = op.Error
	}
	b, _ := json.Marshal(v)
	return b
}

func normalizeSpec(s Spec) (Spec, error) {
	return normalizeSpecWithPolicy(s, DefaultPolicy())
}
func normalizeSpecWithPolicy(s Spec, policy Policy) (Spec, error) {
	if !serviceName.MatchString(s.Name) {
		return s, Fail("INVALID_ARGUMENT", "name must be 1-48 lowercase letters, digits or hyphens")
	}
	var err error
	s.ProjectRoot, err = projectRelative(s.ProjectRoot)
	if err != nil {
		return s, err
	}
	if s.Profile == "" {
		s.Profile = "python-web"
	}
	if s.Profile != "python-web" && s.Profile != "node-web" {
		return s, Fail("PROFILE_UNAVAILABLE", "this deployment supports python-web and node-web runtime profiles")
	}
	if s.DisplayName != "" {
		s.DisplayName, err = normalizeDisplayName(s.DisplayName)
		if err != nil {
			return s, err
		}
	}
	if len(s.StartArgv) == 0 || len(s.StartArgv) > 64 {
		return s, Fail("INVALID_ARGUMENT", "start_argv is required")
	}
	for _, argv := range [][]string{s.StartArgv, s.BuildArgv} {
		for _, arg := range argv {
			if arg == "" || len(arg) > 8192 || strings.ContainsRune(arg, 0) {
				return s, Fail("INVALID_ARGUMENT", "invalid argv token")
			}
			if (strings.Contains(arg, "{{") || strings.Contains(arg, "}}")) && arg != "{{HOST}}" && arg != "{{PORT}}" {
				return s, Fail("INVALID_ARGUMENT", "only complete HOST and PORT template tokens are supported")
			}
		}
	}
	if s.HealthPath == "" {
		s.HealthPath = "/"
	}
	u, err := url.ParseRequestURI(s.HealthPath)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(s.HealthPath, "/") || strings.HasPrefix(s.HealthPath, "//") || len(s.HealthPath) > 1024 {
		return s, Fail("INVALID_ARGUMENT", "health_path must be a local HTTP path")
	}
	if s.LeaseSeconds == 0 {
		s.LeaseSeconds = policy.DefaultLeaseSeconds
	}
	if s.LeaseSeconds < 1 || s.LeaseSeconds > MaxLeaseSeconds {
		return s, Fail("INVALID_ARGUMENT", "lease_seconds must be positive and fit within the supported duration range")
	}
	return s, nil
}
func specDigest(s Spec) string { s.LeaseSeconds = 0; s.DisplayName = ""; return digest(s) }

func (m *Manager) Execute(ctx context.Context, scope Scope, req Request) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if scope.Owner == "" || scope.Session == "" || strings.ContainsRune(scope.Owner+scope.Session, 0) {
		return nil, Fail("PERMISSION_DENIED", "host scope is required")
	}
	if req.Action == "list" || req.Action == "status" || req.Action == "logs" {
		return m.read(ctx, scope, req)
	}
	if req.Action != "start" && req.Action != "stop" && req.Action != "restart" && req.Action != "renew" && req.Action != "open" && req.Action != "revoke" && req.Action != "rename" {
		return nil, Fail("INVALID_ARGUMENT", "unsupported web service action")
	}
	if !operationName.MatchString(req.OperationID) {
		return nil, Fail("INVALID_ARGUMENT", "operation_id is required")
	}
	inputDigest := digest(req)
	workspace := scope.Workspace
	var err error
	if req.Action != "rename" {
		workspace, err = canonicalWorkspace(scope.Workspace, m.options.Root)
		if err != nil {
			return nil, err
		}
	}
	scope.Workspace = workspace
	if req.Action == "start" {
		m.mu.Lock()
		policy := m.policyLocked()
		m.mu.Unlock()
		req.Spec, err = normalizeSpecWithPolicy(req.Spec, policy)
		if err != nil {
			return nil, err
		}
	}
	if req.Action == "renew" && (req.LeaseSeconds < 1 || req.LeaseSeconds > MaxLeaseSeconds) {
		return nil, Fail("INVALID_ARGUMENT", "lease_seconds must be positive and fit within the supported duration range")
	}
	if req.Action == "rename" {
		req.DisplayName, err = normalizeDisplayName(req.DisplayName)
		if err != nil {
			return nil, err
		}
	}
	key := scopeKey(scope) + ":" + req.OperationID
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, Fail("FEATURE_DISABLED", "service manager is closed")
	}
	if m.disk.Deleted[scopeKey(scope)] || m.disk.DeletedOwners[scope.Owner] {
		m.mu.Unlock()
		return nil, Fail("NOT_FOUND", "conversation has been deleted")
	}
	if req.Action == "start" || req.Replacement != nil {
		profile := req.Profile
		if req.Replacement != nil {
			profile = req.Replacement.Profile
		}
		available := false
		for _, supported := range m.profiles {
			if supported == profile {
				available = true
			}
		}
		if !available {
			m.mu.Unlock()
			return nil, Fail("PROFILE_UNAVAILABLE", "the selected website runtime profile is not installed on this platform")
		}
	}
	if m.disk.DeletingServices[req.ServiceID] {
		_, err := m.ownedLocked(scope, req.ServiceID)
		m.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, deletionPending()
	}
	if op, ok := m.disk.Operations[key]; ok {
		if m.disk.DeletingServices[op.ServiceID] {
			m.mu.Unlock()
			return nil, deletionPending()
		}
		if op.Digest != inputDigest {
			m.mu.Unlock()
			return nil, Fail("IDEMPOTENCY_CONFLICT", "operation_id was already used with different arguments")
		}
		if op.Status == "expired" || m.now().Sub(op.Created) > 7*24*time.Hour {
			m.mu.Unlock()
			return nil, Fail("OPERATION_RESULT_EXPIRED", "operation detail has expired; it was not repeated")
		}
		r, exists := m.disk.Records[op.ServiceID]
		if !exists {
			m.mu.Unlock()
			return nil, Fail("OPERATION_RESULT_EXPIRED", "operation details are no longer available")
		}
		if req.Action == "open" {
			if m.lifecycleURLs() && op.Status == "completed" && r.LeaseID != "" && r.State == "ready" && m.now().Before(r.LeaseExpiresAt) {
				lock := m.serviceLockLocked(r.ServiceID)
				m.mu.Unlock()
				lock.Lock()
				defer lock.Unlock()
				current, exists := m.current(r.ServiceID)
				if !exists || current.State != "ready" || !m.now().Before(current.LeaseExpiresAt) {
					return nil, Fail("SERVICE_NOT_READY", "website is not ready")
				}
				return m.open(ctx, scope, current, key)
			}
			g, ok := m.grants[key]
			valid := ok && g.Generation == r.Generation && r.State == "ready" && m.now().Before(g.ExpiresAt)
			m.mu.Unlock()
			if !valid && (op.Status == "accepted" || op.Status == "running") {
				return operationReceipt(r, req.OperationID, op), nil
			}
			if !valid {
				return nil, Fail("GRANT_EXPIRED", "use a new operation_id to open a new preview")
			}
			b, _ := json.Marshal(g)
			return b, nil
		}
		m.mu.Unlock()
		return operationReceipt(r, req.OperationID, op), nil
	}
	var record Record
	if req.Action == "start" {
		for _, r := range m.disk.Records {
			if r.Owner == scope.Owner && r.Session == scope.Session && r.Spec.Name == req.Name {
				record = r
				break
			}
		}
		if m.disk.DeletingServices[record.ServiceID] {
			m.mu.Unlock()
			return nil, deletionPending()
		}
		if record.ServiceID != "" && active(record.State) {
			if record.SpecDigest != specDigest(req.Spec) {
				m.mu.Unlock()
				return nil, Fail("SERVICE_ALREADY_EXISTS", "use restart to replace this service")
			}
			op := operation{Action: req.Action, Digest: inputDigest, ServiceID: record.ServiceID, Generation: record.Generation, Status: "completed", Created: m.now()}
			err = m.commitLocked(func(d *diskState) { d.Operations[key] = op })
			m.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return receipt(record, req.OperationID, true), nil
		}
		if err = m.quotaLocked(scope); err != nil {
			m.mu.Unlock()
			return nil, err
		}
		now := m.now()
		old := record
		if record.ServiceID == "" {
			record = Record{SchemaVersion: 1, ServiceID: randomID("ws_"), Owner: scope.Owner, Session: scope.Session, Installation: m.disk.Installation, CreatedAt: now}
		}
		record.Generation++
		record.ControlID = key
		record.Revision++
		record.Spec = req.Spec
		if req.DisplayName != "" || record.DisplayName == "" {
			record.DisplayName = req.DisplayName
			if record.DisplayName == "" {
				record.DisplayName = req.Name
			}
		}
		record.Workspace = workspace
		record.LeaseID = randomID("lease_")
		record.Runtime = m.policyLocked().Runtime()
		record.SpecDigest = specDigest(req.Spec)
		record.SourceDigest = ""
		record.Instance = Instance{}
		record.State = "preparing"
		record.Phase = "snapshot"
		record.LastError = nil
		record.StorageWarning = nil
		record.LastHealth = nil
		record.TerminalTarget = ""
		record.UpdatedAt = now
		record.LeaseExpiresAt = now.Add(time.Duration(req.LeaseSeconds) * time.Second)
		op := operation{Action: req.Action, Digest: inputDigest, ServiceID: record.ServiceID, Generation: record.Generation, Status: "accepted", Created: now}
		err = m.commitLocked(func(d *diskState) {
			if old.ServiceID != "" {
				remember(d, old)
			}
			d.Records[record.ServiceID] = record
			d.Operations[key] = op
		})
		if err == nil {
			m.queueLocked(record.ServiceID, func() { m.launch(scope, record.ServiceID, record.Generation, key) })
		}
		m.mu.Unlock()
		if err != nil {
			return nil, err
		}
		m.notify(record)
		return receipt(record, req.OperationID, true), nil
	}
	record, err = m.ownedLocked(scope, req.ServiceID)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if req.Action == "rename" {
		record.DisplayName = req.DisplayName
		record.Revision++
		record.UpdatedAt = m.now()
		op := operation{Action: req.Action, Digest: inputDigest, ServiceID: record.ServiceID, Generation: record.Generation, Status: "completed", Created: m.now()}
		err = m.commitLocked(func(d *diskState) { d.Records[record.ServiceID] = record; d.Operations[key] = op })
		m.mu.Unlock()
		if err != nil {
			return nil, err
		}
		m.notify(record)
		return receipt(record, req.OperationID, true), nil
	}
	if req.Action == "open" && (!m.now().Before(record.LeaseExpiresAt) || record.State != "ready") {
		m.mu.Unlock()
		return nil, Fail("SERVICE_NOT_READY", "service is not ready")
	}
	if req.Action == "renew" && (record.State == "stopping" || record.State == "reconciling" || !active(record.State) || !m.now().Before(record.LeaseExpiresAt)) {
		m.mu.Unlock()
		return nil, Fail("LEASE_EXPIRED", "service cannot be renewed in its current state")
	}
	if req.Action == "restart" {
		if !m.now().Before(record.LeaseExpiresAt) {
			m.mu.Unlock()
			return nil, Fail("LEASE_EXPIRED", "start a new lease for this service")
		}
		if record.State == "stopping" {
			m.mu.Unlock()
			return nil, Fail("SERVICE_NOT_READY", "service is already stopping")
		}
		if !active(record.State) {
			if err = m.quotaLocked(scope); err != nil {
				m.mu.Unlock()
				return nil, err
			}
		}
	}
	if req.Replacement != nil {
		replacement := *req.Replacement
		replacement.Name = record.Spec.Name
		replacement.LeaseSeconds = record.Spec.LeaseSeconds
		replacement, err = normalizeSpec(replacement)
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		req.Replacement = &replacement
	}
	op := operation{Action: req.Action, Digest: inputDigest, ServiceID: record.ServiceID, Generation: record.Generation, Status: "accepted", Created: m.now()}
	if req.Action == "restart" {
		op.Generation++
	}
	if req.Action == "stop" || req.Action == "restart" {
		record.ControlID = key
		record.State = "stopping"
		record.TerminalTarget = "stopped"
		record.Revision++
		record.UpdatedAt = m.now()
	}
	err = m.commitLocked(func(d *diskState) { d.Records[record.ServiceID] = record; d.Operations[key] = op })
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if req.Action == "open" || req.Action == "revoke" {
		lock := m.serviceLockLocked(record.ServiceID)
		m.mu.Unlock()
		lock.Lock()
		defer lock.Unlock()
		if req.Action == "open" {
			return m.open(ctx, scope, record, key)
		}
		err = m.options.Gateway.CloseGrant(ctx, record, req.GrantID)
		if err == nil {
			m.mu.Lock()
			for cachedKey, grant := range m.grants {
				if grant.ServiceID == record.ServiceID && grant.Generation == record.Generation && grant.GrantID == req.GrantID {
					delete(m.grants, cachedKey)
				}
			}
			m.mu.Unlock()
		}
		m.completeOp(key, err)
		if err != nil {
			return nil, err
		}
		return receipt(record, req.OperationID, true), nil
	}
	if cancel := m.launchCancel[record.ServiceID]; cancel != nil && (req.Action == "stop" || req.Action == "restart") {
		cancel()
	}
	m.queueLocked(record.ServiceID, func() {
		operationRecord := record
		if req.Action == "stop" || req.Action == "restart" {
			current, ok := m.current(record.ServiceID)
			if !ok || current.ControlID != key {
				m.completeOp(key, context.Canceled)
				return
			}
			operationRecord = current
		}
		switch req.Action {
		case "stop":
			m.stop(operationRecord, key, "stopped")
		case "restart":
			m.restart(scope, operationRecord, key, req.Replacement)
		case "renew":
			m.renew(operationRecord, key, req.LeaseSeconds)
		}
	})
	m.mu.Unlock()
	m.notify(record)
	return receipt(record, req.OperationID, true), nil
}

func (m *Manager) read(ctx context.Context, s Scope, q Request) (json.RawMessage, error) {
	m.mu.Lock()
	if m.disk.Deleted[scopeKey(s)] || m.disk.DeletedOwners[s.Owner] {
		m.mu.Unlock()
		return nil, Fail("NOT_FOUND", "conversation not found")
	}
	if q.Action == "list" {
		rows := []json.RawMessage{}
		for _, r := range m.disk.Records {
			if r.Owner == s.Owner && r.Session == s.Session {
				rows = append(rows, PublicRecord(r))
			}
		}
		quota := m.quotaUsageLocked(s)
		defaultLeaseSeconds := m.policyLocked().DefaultLeaseSeconds
		m.mu.Unlock()
		b, _ := json.Marshal(map[string]any{"services": rows, "quota": quota, "default_lease_seconds": defaultLeaseSeconds})
		return b, nil
	}
	r, err := m.ownedLocked(s, q.ServiceID)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if q.Action == "status" {
		var latest operation
		latestID := ""
		for key, op := range m.disk.Operations {
			if op.ServiceID == r.ServiceID && (latestID == "" || op.Created.After(latest.Created)) {
				latest = op
				latestID = strings.TrimPrefix(key, scopeKey(s)+":")
			}
		}
		m.mu.Unlock()
		if latestID != "" {
			return operationReceipt(r, latestID, latest), nil
		}
		return PublicRecord(r), nil
	}
	if q.Generation > 0 && q.Generation != r.Generation {
		old, ok := m.disk.History[r.ServiceID][q.Generation]
		if !ok {
			m.mu.Unlock()
			return nil, Fail("NOT_FOUND", "generation not found")
		}
		r = old
	}
	m.mu.Unlock()
	if q.LimitBytes < 0 || q.LimitBytes > 65536 {
		return nil, Fail("INVALID_ARGUMENT", "log limit must be at most 64 KiB")
	}
	if q.LimitBytes == 0 {
		q.LimitBytes = 16384
	}
	page, err := m.options.Runner.Logs(ctx, r, q.Cursor, q.LimitBytes)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(page)
	return b, nil
}

func (m *Manager) ownedLocked(s Scope, id string) (Record, error) {
	r, ok := m.disk.Records[id]
	if !ok || r.Owner != s.Owner || r.Session != s.Session {
		return Record{}, Fail("NOT_FOUND", "service not found")
	}
	return r, nil
}

type QuotaCount struct {
	Current int `json:"current"`
	Limit   int `json:"limit"`
}
type QuotaUsage struct {
	Session QuotaCount `json:"session"`
	User    QuotaCount `json:"user"`
	Total   QuotaCount `json:"total"`
}

func (m *Manager) quotaUsageLocked(s Scope) QuotaUsage {
	host, owner, session := 0, 0, 0
	for _, r := range m.disk.Records {
		if m.countsActiveLocked(r) {
			host++
			if r.Owner == s.Owner {
				owner++
				if r.Session == s.Session {
					session++
				}
			}
		}
	}
	policy := m.policyLocked()
	return QuotaUsage{Session: QuotaCount{session, policy.MaxPerSession}, User: QuotaCount{owner, policy.MaxPerUser}, Total: QuotaCount{host, policy.MaxTotal}}
}
func (m *Manager) quotaLocked(s Scope) error {
	usage := m.quotaUsageLocked(s)
	for _, cap := range []struct {
		scope          string
		current, limit int
		message        string
	}{
		{"session", usage.Session.Current, usage.Session.Limit, "This conversation has reached its website limit. Stop an existing website or ask an administrator to increase the limit."},
		{"user", usage.User.Current, usage.User.Limit, "Your account has reached its website limit. Stop an existing website or ask an administrator to increase the limit."},
		{"total", usage.Total.Current, usage.Total.Limit, "The platform has reached its website limit. Stop an existing website or ask an administrator to increase the limit."},
	} {
		if cap.current >= cap.limit {
			return &Error{Code: "QUOTA_EXCEEDED", Message: cap.message, Details: map[string]any{"scope": cap.scope, "current": cap.current, "limit": cap.limit}}
		}
	}
	return nil
}
func (m *Manager) now() time.Time { return m.options.Now().UTC() }
func (m *Manager) notify(r Record) {
	if m.options.OnChange != nil {
		m.options.OnChange(r)
	}
}
func remember(d *diskState, r Record) {
	if d.History[r.ServiceID] == nil {
		d.History[r.ServiceID] = map[int]Record{}
	}
	d.History[r.ServiceID][r.Generation] = r
}
func (m *Manager) serviceLockLocked(id string) *sync.Mutex {
	if m.locks[id] == nil {
		m.locks[id] = &sync.Mutex{}
	}
	return m.locks[id]
}
func (m *Manager) queueLocked(id string, f func()) {
	lock := m.serviceLockLocked(id)
	previous := m.tails[id]
	done := make(chan struct{})
	m.tails[id] = done
	m.busy[id]++
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer close(done)
		defer func() { m.mu.Lock(); m.busy[id]--; m.mu.Unlock() }()
		if previous != nil {
			select {
			case <-previous:
			case <-m.ctx.Done():
				return
			}
		}
		lock.Lock()
		defer lock.Unlock()
		if m.ctx.Err() == nil {
			f()
		}
	}()
}

func (m *Manager) write(d diskState) error {
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if len(data) > 16<<20 {
		return errors.New("service registry exceeds storage bound")
	}
	f, err := os.CreateTemp(m.options.Root, ".registry-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(m.options.Root, "registry.json")); err != nil {
		return err
	}
	if dir, openErr := os.Open(m.options.Root); openErr == nil {
		err = dir.Sync()
		_ = dir.Close()
	}
	return err
}
func (m *Manager) commitLocked(change func(*diskState)) error {
	data, err := json.Marshal(m.disk)
	if err != nil {
		return err
	}
	var next diskState
	if err = json.Unmarshal(data, &next); err != nil {
		return err
	}
	change(&next)
	if err = m.write(next); err != nil {
		return err
	}
	m.disk = next
	return nil
}
func (m *Manager) saveRecord(r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.disk.Records[r.ServiceID]
	if m.disk.DeletingServices[r.ServiceID] {
		return deletionPending()
	}
	if !ok || current.Generation != r.Generation {
		return Fail("STALE_GENERATION", "service generation changed")
	}
	if current.ControlID != r.ControlID {
		return Fail("STALE_OPERATION", "a newer service control request was accepted")
	}
	if current.State == "stopping" && r.State != "stopping" && active(r.State) {
		return Fail("STALE_GENERATION", "service is stopping")
	}
	// Metadata edits can happen while a health or launch operation is in flight.
	// A stale runtime observation must never undo the user's current title.
	r.DisplayName = current.DisplayName
	if r.Workspace == "" {
		r.Workspace = current.Workspace
	}
	r.Revision = current.Revision + 1
	r.UpdatedAt = m.now()
	return m.commitLocked(func(d *diskState) { d.Records[r.ServiceID] = r })
}
func (m *Manager) current(id string) (Record, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.disk.Records[id]
	return r, ok
}
func (m *Manager) completeOp(key string, err error) {
	if key == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = m.commitLocked(func(d *diskState) {
		if op, ok := d.Operations[key]; ok {
			op.Status = "completed"
			if err != nil {
				op.Status = "failed"
				op.Error = asError(err)
				if errors.Is(err, context.Canceled) {
					op.Status = "cancelled"
					op.Error = &Error{Code: "OPERATION_CANCELLED", Message: "operation was cancelled or superseded"}
				}
			}
			d.Operations[key] = op
		}
	})
}
func asError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		v := *e
		return &v
	}
	return &Error{Code: "RUNNER_UNAVAILABLE", Message: "managed service operation failed", Retryable: true}
}

func (m *Manager) launch(s Scope, id string, generation int, key string) {
	m.mu.Lock()
	deleted := m.disk.Deleted[scopeKey(s)] || m.disk.DeletedOwners[s.Owner] || m.disk.DeletingServices[id]
	m.mu.Unlock()
	if deleted {
		m.completeOp(key, context.Canceled)
		return
	}
	r, ok := m.current(id)
	if !ok || r.Generation != generation || r.State == "stopping" {
		m.completeOp(key, context.Canceled)
		return
	}
	controlID := r.ControlID
	ctx, cancel := context.WithTimeout(m.ctx, 600*time.Second)
	defer cancel()
	defer func() {
		if errors.Is(ctx.Err(), context.Canceled) && m.ctx.Err() == nil {
			m.completeOp(key, context.Canceled)
		}
	}()
	m.mu.Lock()
	m.launchCancel[id] = cancel
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.launchCancel, id); m.mu.Unlock() }()
	archive, source, err := snapshotContext(ctx, s.Workspace, r.Spec.ProjectRoot)
	if err == nil {
		r.SourceDigest = source
		r.Phase = "starting"
		err = m.saveRecord(r)
	}
	if err == nil {
		r, _ = m.current(id)
		instance, startErr := m.options.Runner.Start(ctx, Launch{Record: r, Archive: archive})
		r.Instance = instance
		err = startErr
		if saveErr := m.saveInstance(r); err == nil {
			err = saveErr
		}
	}
	if err != nil {
		if m.ctx.Err() != nil {
			return
		}
		m.failAndStop(r, key, err)
		return
	}
	r, _ = m.current(id)
	if r.State == "stopping" || ctx.Err() != nil {
		return
	}
	r.State = "starting"
	r.Phase = "http"
	if err = m.saveRecord(r); err != nil {
		return
	}
	r, _ = m.current(id)
	if err = m.options.Gateway.Apply(ctx, r); err != nil {
		m.failAndStop(r, key, err)
		return
	}
	deadline := m.now().Add(m.options.StartTimeout)
	// The runner enforces storage independently, including during HTTP startup.
	// Observe before and after the probe so a stopped/deleted writer's durable
	// cause wins over a later generic readiness timeout (or a stale HTTP 2xx).
	observeRuntime := func(previous Record) (Record, error, bool) {
		inspectCtx, inspectCancel := context.WithTimeout(ctx, 5*time.Second)
		state, inspectErr := m.options.Runner.Inspect(inspectCtx, previous)
		inspectCancel()
		current, exists := m.current(id)
		if !exists || current.Generation != generation || current.ControlID != controlID || current.State == "stopping" || !active(current.State) || ctx.Err() != nil {
			return Record{}, nil, false
		}
		if inspectErr != nil {
			return current, inspectErr, true
		}
		applyStorageStatus(&current, state)
		if storageFailure(state.LastError) || storageWarning(state.LastError) && !state.Running {
			current.LastError = asError(state.LastError)
			if m.saveRecord(current) == nil {
				m.failAndStop(current, key, state.LastError)
			}
			return Record{}, nil, false
		}
		if !state.Exists || !state.Running {
			if state.Storage == nil || m.saveRecord(current) == nil {
				m.failAndStop(current, key, Fail("SERVICE_EXITED", "website process exited before HTTP readiness"))
			}
			return Record{}, nil, false
		}
		return current, nil, true
	}
	for m.ctx.Err() == nil && ctx.Err() == nil {
		current, exists := m.current(id)
		if !exists || current.Generation != generation || current.ControlID != controlID || current.State == "stopping" {
			return
		}
		r = current
		var inspectErr error
		var observed bool
		r, inspectErr, observed = observeRuntime(r)
		if !observed {
			return
		}
		var health Health
		var checkErr error
		if inspectErr == nil {
			checkCtx, checkCancel := context.WithTimeout(ctx, 3*time.Second)
			health, checkErr = m.options.Gateway.Probe(checkCtx, r)
			checkCancel()
			r, inspectErr, observed = observeRuntime(r)
			if !observed {
				return
			}
		}
		if inspectErr == nil && checkErr == nil && health.Generation == generation && health.UpstreamStatus >= 200 && health.UpstreamStatus < 300 && health.GatewayStatus >= 200 && health.GatewayStatus < 300 {
			r.State = "ready"
			r.Phase = ""
			r.LastHealth = &health
			r.LastError = nil
			if err = m.saveRecord(r); err != nil {
				return
			}
			r, _ = m.current(id)
			if err = m.options.Gateway.Apply(ctx, r); err != nil {
				m.failAndStop(r, key, err)
				return
			}
			m.completeOp(key, nil)
			m.notify(r)
			return
		}
		if !m.now().Before(deadline) {
			why := inspectErr
			if why == nil {
				why = Fail("HEALTH_CHECK_FAILED", "service did not become HTTP-ready before the deadline")
			}
			m.failAndStop(r, key, why)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(min(m.options.PollInterval, time.Second)):
		}
	}
}
func (m *Manager) saveInstance(r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.disk.Records[r.ServiceID]
	if !ok || cur.Generation != r.Generation {
		return Fail("STALE_GENERATION", "instance generation changed")
	}
	cur.Instance = r.Instance
	cur.SourceDigest = r.SourceDigest
	cur.Revision++
	cur.UpdatedAt = m.now()
	return m.commitLocked(func(d *diskState) { d.Records[r.ServiceID] = cur })
}
func (m *Manager) failAndStop(r Record, key string, why error) {
	current, ok := m.current(r.ServiceID)
	if !ok || current.Generation != r.Generation || current.ControlID != r.ControlID {
		return
	}
	if current.State == "stopping" {
		m.completeOp(key, context.Canceled)
		return
	}
	current.State = "stopping"
	current.TerminalTarget = "failed"
	current.LastError = asError(why)
	if current.LastError.Code == "LEASE_EXPIRED" || !m.now().Before(current.LeaseExpiresAt) {
		current.TerminalTarget = "expired"
	}
	if m.saveRecord(current) != nil {
		return
	}
	m.stop(current, key, current.TerminalTarget)
}

func (m *Manager) stop(r Record, key, target string) {
	ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
	defer cancel()
	revokeErr := m.options.Gateway.Revoke(ctx, r)
	stopErr := m.options.Runner.Stop(ctx, r)
	if m.ctx.Err() != nil {
		return
	}
	cur, ok := m.current(r.ServiceID)
	if !ok || cur.Generation != r.Generation || cur.ControlID != r.ControlID {
		return
	}
	if revokeErr != nil || stopErr != nil {
		cur.State = "stopping"
		cur.TerminalTarget = target
		if cur.LastError != nil && (cur.LastError.Code == "STORAGE_LIMIT_EXCEEDED" || cur.LastError.Code == "STORAGE_CHECK_UNAVAILABLE") {
			cause := *cur.LastError
			cause.Retryable = true
			cur.LastError = &cause
		} else {
			cur.LastError = &Error{Code: "STOP_UNCONFIRMED", Message: "service stop or access revocation was not confirmed", Retryable: true}
		}
		_ = m.saveRecord(cur)
		m.completeOp(key, cur.LastError)
		m.notify(cur)
		return
	}
	cur.State = target
	cur.Phase = ""
	cur.TerminalTarget = ""
	if cur.LastError != nil && (cur.LastError.Code == "STORAGE_LIMIT_EXCEEDED" || cur.LastError.Code == "STORAGE_CHECK_UNAVAILABLE") {
		cause := *cur.LastError
		cause.Retryable = false
		cur.LastError = &cause
	}
	_ = m.saveRecord(cur)
	if target == "failed" || (target == "expired" && key != "") {
		failure := cur.LastError
		if failure == nil {
			failure = &Error{Code: "START_FAILED", Message: "service failed"}
		}
		m.completeOp(key, failure)
	} else {
		m.completeOp(key, nil)
	}
	m.notify(cur)
}
func (m *Manager) restart(s Scope, old Record, key string, replacement *Spec) {
	m.stop(old, "", "stopped")
	r, ok := m.current(old.ServiceID)
	if !ok || r.State != "stopped" || !m.now().Before(r.LeaseExpiresAt) {
		m.completeOp(key, Fail("STOP_UNCONFIRMED", "old generation was not stopped"))
		return
	}
	m.mu.Lock()
	if m.disk.Deleted[scopeKey(s)] || m.disk.DeletedOwners[s.Owner] || m.disk.DeletingServices[r.ServiceID] || m.disk.Records[r.ServiceID].ControlID != key {
		m.mu.Unlock()
		m.completeOp(key, context.Canceled)
		return
	}
	old = r
	r.Generation++
	r.Revision++
	r.Instance = Instance{}
	r.State = "preparing"
	r.Phase = "snapshot"
	r.SourceDigest = ""
	r.LastError = nil
	r.LastHealth = nil
	r.TerminalTarget = ""
	if replacement != nil {
		r.Spec = *replacement
		r.SpecDigest = specDigest(*replacement)
	}
	r.UpdatedAt = m.now()
	r.Runtime = m.policyLocked().Runtime()
	err := m.commitLocked(func(d *diskState) { remember(d, old); d.Records[r.ServiceID] = r })
	m.mu.Unlock()
	if err != nil {
		m.completeOp(key, err)
		return
	}
	m.launch(s, r.ServiceID, r.Generation, key)
}
func (m *Manager) renew(old Record, key string, seconds int) {
	r, ok := m.current(old.ServiceID)
	if !ok || r.Generation != old.Generation || r.State == "stopping" || r.State == "reconciling" || !active(r.State) || !m.now().Before(r.LeaseExpiresAt) {
		m.completeOp(key, Fail("LEASE_EXPIRED", "service cannot be renewed"))
		return
	}
	r.LeaseExpiresAt = m.now().Add(time.Duration(seconds) * time.Second)
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	defer cancel()
	err := m.options.Runner.Renew(ctx, r)
	if err == nil {
		err = m.saveRecord(r)
	}
	if err == nil {
		r, _ = m.current(r.ServiceID)
		err = m.options.Gateway.Apply(ctx, r)
		if err == nil && m.lifecycleURLs() {
			m.mu.Lock()
			for key, grant := range m.grants {
				if grant.ServiceID == r.ServiceID {
					grant.ExpiresAt = r.LeaseExpiresAt
					grant.LeaseExpiresAt = r.LeaseExpiresAt
					m.grants[key] = grant
				}
			}
			m.mu.Unlock()
		}
	}
	m.completeOp(key, err)
	if err == nil {
		m.notify(r)
	}
}
func (m *Manager) open(ctx context.Context, s Scope, r Record, key string) (json.RawMessage, error) {
	m.mu.Lock()
	if m.lifecycleURLs() {
		current, exists := m.disk.Records[r.ServiceID]
		if !exists || current.State != "ready" || !m.now().Before(current.LeaseExpiresAt) || m.disk.Deleted[scopeKey(s)] || m.disk.DeletedOwners[s.Owner] {
			m.mu.Unlock()
			return nil, Fail("SERVICE_NOT_READY", "website is not ready")
		}
		r = current
	}
	r.OperationID = strings.TrimPrefix(key, scopeKey(s)+":")
	epoch := m.grantEpoch
	m.mu.Unlock()
	// Names and other metadata can advance the registry revision independently
	// of periodic health checks. Publish that current revision before opening so
	// an immediate open after a rename never depends on the next health tick.
	if m.lifecycleURLs() {
		if err := m.options.Gateway.Apply(ctx, r); err != nil {
			m.invalidateGrantCache()
			m.completeOp(key, err)
			return nil, err
		}
	}
	g, err := m.options.Gateway.Open(ctx, r)
	if err != nil {
		var cause *Error
		if errors.As(err, &cause) && (cause.Code == "GATEWAY_UNAVAILABLE" || cause.Code == "CONTROL_SUPERSEDED") {
			m.invalidateGrantCache()
		}
		m.completeOp(key, err)
		return nil, err
	}
	m.mu.Lock()
	if epoch != m.grantEpoch {
		m.mu.Unlock()
		m.cleanupOpenGrant(ctx, r, g.GrantID)
		err = Fail("GRANT_EXPIRED", "gateway authority changed while opening preview; use a new operation_id")
		m.completeOp(key, err)
		return nil, err
	}
	cur, ok := m.disk.Records[r.ServiceID]
	if !ok || cur.Generation != r.Generation || cur.State != "ready" || m.disk.Deleted[scopeKey(s)] || m.disk.DeletedOwners[s.Owner] {
		m.mu.Unlock()
		m.cleanupOpenGrant(ctx, r, g.GrantID)
		err = Fail("SERVICE_NOT_READY", "service changed while opening preview")
		m.completeOp(key, err)
		return nil, err
	}
	u, parseErr := url.Parse(g.PreviewURL)
	pathPreview := parseErr == nil && IsPathPreviewURL(u)
	if parseErr != nil || !validManagedPreviewURL(u) {
		m.mu.Unlock()
		m.cleanupOpenGrant(ctx, r, g.GrantID)
		err = Fail("GATEWAY_UNAVAILABLE", "gateway returned an invalid preview origin")
		m.completeOp(key, err)
		return nil, err
	}
	if !pathPreview {
		domain := strings.ToLower(strings.SplitN(u.Hostname(), ".", 2)[1])
		known := false
		for _, d := range m.disk.PreviewDomains {
			if d == domain {
				known = true
			}
		}
		if !known {
			if err := m.commitLocked(func(d *diskState) { d.PreviewDomains = append(d.PreviewDomains, domain) }); err != nil {
				m.mu.Unlock()
				m.cleanupOpenGrant(ctx, r, g.GrantID)
				m.completeOp(key, err)
				return nil, err
			}
		}
	}
	m.grants[key] = g
	m.mu.Unlock()
	m.completeOp(key, nil)
	b, _ := json.Marshal(g)
	return b, nil
}

func (m *Manager) cleanupOpenGrant(ctx context.Context, r Record, grantID string) {
	// Legacy opens allocate a separate temporary grant. Lifecycle opens return
	// an address shared by every viewer for this lease; CloseGrant explicitly
	// rotates that whole address. A failed or superseded open must never revoke
	// an already shared website address as an incidental cleanup side effect.
	if m.lifecycleURLs() && r.LeaseID != "" {
		return
	}
	_ = m.options.Gateway.CloseGrant(ctx, r, grantID)
}

var previewURLPattern = regexp.MustCompile(`(?i)https?://[^\s"'<>)]+`)

// RedactPreviewURLs also works after restart: it remembers only the configured
// domains learned from signed grants, never persists bearer URLs or tokens.
func (m *Manager) RedactPreviewURLs(text string) string {
	text = RedactPathPreviewURLs(text)
	if m == nil {
		return text
	}
	m.mu.Lock()
	domains := append([]string(nil), m.disk.PreviewDomains...)
	m.mu.Unlock()
	return previewURLPattern.ReplaceAllStringFunc(text, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return raw
		}
		host := strings.ToLower(u.Hostname())
		for _, domain := range domains {
			suffix := "." + domain
			if strings.HasSuffix(host, suffix) {
				label := strings.TrimSuffix(host, suffix)
				if label != "" && !strings.Contains(label, ".") {
					return "web-service:preview-redacted"
				}
			}
		}
		return raw
	})
}

func (m *Manager) maintain() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.options.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.sweep()
		}
	}
}
func (m *Manager) sweep() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	now := m.now()
	for id, r := range m.disk.Records {
		if m.busy[id] > 0 {
			continue
		}
		if m.disk.DeletingServices[id] {
			m.queueLocked(id, func() { _ = m.attemptServiceDeletion(id) })
			continue
		}
		if m.disk.Deleted[scopeKey(Scope{Owner: r.Owner, Session: r.Session})] || m.disk.DeletedOwners[r.Owner] {
			m.queueLocked(id, func() {
				ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
				defer cancel()
				_ = m.deleteRecord(ctx, r)
			})
			continue
		}
		if restart, pending := m.disk.PolicyRestarts[id]; pending && (restart.State == "accepted" || restart.State == "running") && "settings:"+restart.OperationID == r.ControlID {
			key := r.ControlID
			m.queueLocked(id, func() { m.runSettingsRestart(id, key) })
			continue
		}
		if !active(r.State) {
			continue
		}
		if !now.Before(r.LeaseExpiresAt) {
			r.State = "stopping"
			r.TerminalTarget = "expired"
			if err := m.commitLocked(func(d *diskState) { d.Records[id] = r }); err == nil {
				m.queueLocked(id, func() { m.stop(r, "", "expired") })
			}
			continue
		}
		if r.State == "stopping" {
			target := r.TerminalTarget
			if target == "" {
				target = "stopped"
			}
			m.queueLocked(id, func() { m.stop(r, "", target) })
			continue
		}
		if !now.Before(m.nextHealth[id]) {
			m.nextHealth[id] = now.Add(m.options.HealthInterval)
			m.queueLocked(id, func() { m.check(r) })
		}
	}
	for key, g := range m.grants {
		if !now.Before(g.ExpiresAt) {
			delete(m.grants, key)
		}
	}
}

// Temporary measurement failures are independent of process/HTTP readiness.
// Older runners returned them in LastError, so accept that wire shape too.
func applyStorageStatus(rec *Record, state RuntimeStatus) {
	if state.Storage != nil {
		rec.Storage = state.Storage
	}
	rec.StorageWarning = state.StorageWarning
	if storageWarning(state.LastError) {
		warning := *state.LastError
		warning.Retryable = true
		rec.StorageWarning = &warning
	}
	if state.Running && storageWarning(rec.LastError) {
		rec.LastError = nil
	}
}

func (m *Manager) check(old Record) {
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	defer cancel()
	state, err := m.options.Runner.Inspect(ctx, old)
	r, ok := m.current(old.ServiceID)
	if !ok || r.Generation != old.Generation || r.State == "stopping" {
		return
	}
	if err != nil {
		r.State = "reconciling"
		r.LastError = asError(err)
		_ = m.saveRecord(r)
		r, _ = m.current(r.ServiceID)
		_ = m.options.Gateway.Apply(ctx, r)
		m.notify(r)
		return
	}
	applyStorageStatus(&r, state)
	if storageFailure(state.LastError) || storageWarning(state.LastError) && !state.Running {
		// A runner may already have stopped/deleted the container independently.
		// Preserve its durable reason before absence is classified as interrupted;
		// Stop still confirms cleanup and revocation before releasing the quota.
		if m.saveRecord(r) == nil {
			m.failAndStop(r, "", state.LastError)
		}
		return
	}
	if !state.Exists {
		r.State = "stopping"
		r.TerminalTarget = "interrupted"
		_ = m.saveRecord(r)
		m.stop(r, "", "interrupted")
		return
	}
	r.Instance = state.Instance
	if !state.Running {
		m.failAndStop(r, "", Fail("SERVICE_EXITED", "website process exited"))
		return
	}
	if r.State == "reconciling" {
		if err = m.options.Gateway.Preflight(ctx); err != nil {
			m.invalidateGrantCache()
			r.LastError = asError(err)
			_ = m.saveRecord(r)
			return
		}
	}
	if err = m.options.Gateway.Apply(ctx, r); err != nil {
		m.invalidateGrantCache()
		r.State = "reconciling"
		r.LastError = asError(err)
		_ = m.saveRecord(r)
		return
	}
	h, err := m.options.Gateway.Probe(ctx, r)
	good := err == nil && h.UpstreamStatus >= 200 && h.UpstreamStatus < 300 && h.GatewayStatus >= 200 && h.GatewayStatus < 300
	m.mu.Lock()
	if good {
		m.successes[r.ServiceID]++
		m.failures[r.ServiceID] = 0
	} else {
		m.failures[r.ServiceID]++
		m.successes[r.ServiceID] = 0
	}
	successes, failures := m.successes[r.ServiceID], m.failures[r.ServiceID]
	m.mu.Unlock()
	if good {
		r.LastHealth = &h
		if r.State == "reconciling" || r.State == "starting" || r.State == "preparing" || (r.State == "degraded" && successes >= 2) {
			r.State = "ready"
			r.LastError = nil
		}
	} else if failures >= 3 || r.State == "reconciling" {
		r.State = "degraded"
		r.LastError = &Error{Code: "HEALTH_CHECK_FAILED", Message: "website HTTP health check failed", Retryable: true}
	}
	if m.saveRecord(r) == nil {
		r, _ = m.current(r.ServiceID)
		_ = m.options.Gateway.Apply(ctx, r)
		m.notify(r)
	}
}

func (m *Manager) DeleteSession(ctx context.Context, owner, sid string) error {
	s := Scope{Owner: owner, Session: sid}
	m.mu.Lock()
	err := m.commitLocked(func(d *diskState) { d.Deleted[scopeKey(s)] = true })
	records := []Record{}
	for _, r := range m.disk.Records {
		if r.Owner == owner && r.Session == sid {
			records = append(records, r)
			if c := m.launchCancel[r.ServiceID]; c != nil {
				c()
			}
		}
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	for _, r := range records {
		m.mu.Lock()
		lock := m.serviceLockLocked(r.ServiceID)
		m.mu.Unlock()
		lock.Lock()
		if current, ok := m.current(r.ServiceID); ok {
			r = current
		}
		err = m.deleteRecord(ctx, r)
		lock.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// DeleteOwner fences even previously bound/in-flight Agent backends before
// attempting cleanup. It changes no account/session/source-file retention.
func (m *Manager) DeleteOwner(ctx context.Context, owner string) error {
	if owner == "" {
		return Fail("PERMISSION_DENIED", "owner is required")
	}
	m.mu.Lock()
	err := m.commitLocked(func(d *diskState) { d.DeletedOwners[owner] = true })
	sessions := map[string]bool{}
	for _, r := range m.disk.Records {
		if r.Owner == owner {
			sessions[r.Session] = true
		}
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	var failures []error
	for sid := range sessions {
		if err := m.DeleteSession(ctx, owner, sid); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (m *Manager) deleteRecord(ctx context.Context, r Record) error {
	current, exists := m.current(r.ServiceID)
	if !exists {
		return nil
	}
	if current.Generation != r.Generation {
		return Fail("STALE_GENERATION", "cleanup generation changed")
	}
	// Even an unavailable gateway must not prevent stopping the runtime. Data
	// is removed only after both shutdown and access revocation are confirmed.
	revokeErr := m.options.Gateway.Revoke(ctx, r)
	stopErr := m.options.Runner.Stop(ctx, r)
	if err := errors.Join(revokeErr, stopErr); err != nil {
		return err
	}
	if err := m.options.Runner.DeleteData(ctx, r); err != nil {
		return err
	}
	m.mu.Lock()
	current, exists = m.disk.Records[r.ServiceID]
	if !exists || current.Generation != r.Generation {
		m.mu.Unlock()
		return Fail("STALE_GENERATION", "cleanup generation changed")
	}
	err := m.commitLocked(func(d *diskState) {
		delete(d.Records, r.ServiceID)
		delete(d.History, r.ServiceID)
		delete(d.PolicyRestarts, r.ServiceID)
		delete(d.DeletingServices, r.ServiceID)
	})
	if err == nil {
		for key, grant := range m.grants {
			if grant.ServiceID == r.ServiceID {
				delete(m.grants, key)
			}
		}
		delete(m.nextHealth, r.ServiceID)
		delete(m.failures, r.ServiceID)
		delete(m.successes, r.ServiceID)
	}
	m.mu.Unlock()
	if err == nil {
		// This transient final view prevents late pre-delete events from bringing
		// the service back in another open client. It is never stored as a site.
		current.State, current.Phase = "stopped", "deleted"
		current.LastError = nil
		current.TerminalTarget = ""
		current.Revision++
		current.UpdatedAt = m.now()
		m.notify(current)
	}
	return err
}
func (m *Manager) Close() error {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
	return nil
}

var _ Backend = (*boundBackend)(nil)
