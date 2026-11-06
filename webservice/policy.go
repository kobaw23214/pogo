package webservice

import (
	"context"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxLeaseSeconds is the largest whole-second lease representable by a Go
// time.Duration. Leases have no product-level two-hour cap.
const MaxLeaseSeconds = 9223372036

type RuntimeConfig struct {
	InternalPort int     `json:"internal_port"`
	CPUs         float64 `json:"cpus"`
	MemoryMiB    int     `json:"memory_mib"`
}

func (r RuntimeConfig) Validate() error {
	if r.InternalPort < 1024 || r.InternalPort > 65535 || math.IsNaN(r.CPUs) || math.IsInf(r.CPUs, 0) || r.CPUs < .1 || r.CPUs > 64 || r.MemoryMiB < 64 || r.MemoryMiB > 65536 {
		return Fail("INVALID_SETTINGS", "website runtime requires port 1024–65535, CPU 0.1–64 and memory 64–65536 MiB")
	}
	return nil
}

func (r RuntimeConfig) Normalized() (RuntimeConfig, error) {
	if r == (RuntimeConfig{}) {
		r = DefaultPolicy().Runtime()
	}
	return r, r.Validate()
}

type Policy struct {
	DefaultLeaseSeconds int     `json:"default_lease_seconds"`
	MaxPerSession       int     `json:"max_per_session"`
	MaxPerUser          int     `json:"max_per_user"`
	MaxTotal            int     `json:"max_total"`
	InternalPort        int     `json:"internal_port"`
	CPUs                float64 `json:"cpus"`
	MemoryMiB           int     `json:"memory_mib"`
}

func DefaultPolicy() Policy {
	return Policy{DefaultLeaseSeconds: 7200, MaxPerSession: 2, MaxPerUser: 4, MaxTotal: 16, InternalPort: 8080, CPUs: 1, MemoryMiB: 512}
}

func (p Policy) Runtime() RuntimeConfig {
	return RuntimeConfig{InternalPort: p.InternalPort, CPUs: p.CPUs, MemoryMiB: p.MemoryMiB}
}

func (p Policy) Validate() error {
	if p.DefaultLeaseSeconds < 1 || p.DefaultLeaseSeconds > MaxLeaseSeconds {
		return Fail("INVALID_SETTINGS", "default website lifetime must be a positive representable number of seconds")
	}
	if p.MaxPerSession < 1 || p.MaxPerSession > 10000 || p.MaxPerUser < 1 || p.MaxPerUser > 10000 || p.MaxTotal < 1 || p.MaxTotal > 10000 {
		return Fail("INVALID_SETTINGS", "website concurrency limits must be between 1 and 10000")
	}
	return p.Runtime().Validate()
}

type PolicyUsage struct {
	Total    int            `json:"total"`
	Users    map[string]int `json:"users"`
	Sessions map[string]int `json:"sessions"`
}

type SettingsEffects struct {
	Immediate       []string `json:"immediate"`
	RestartRequired []string `json:"restart_required"`
}

type SettingsRestart struct {
	ServiceID   string `json:"service_id"`
	Name        string `json:"name"`
	State       string `json:"state"`
	OperationID string `json:"operation_id,omitempty"`
	Error       *Error `json:"error,omitempty"`
	// Workspace is private recovery state, omitted from every public snapshot.
	Workspace string `json:"workspace,omitempty"`
}

type SettingsState struct {
	Settings   Policy            `json:"settings"`
	Usage      PolicyUsage       `json:"usage"`
	Profiles   []string          `json:"profiles"`
	Restarting []SettingsRestart `json:"restarting"`
}

type SettingsUpdate struct {
	Settings  Policy            `json:"settings"`
	Effects   SettingsEffects   `json:"effects"`
	Restarted []SettingsRestart `json:"restarted"`
}

func displayName(r Record) string {
	if r.DisplayName != "" {
		return r.DisplayName
	}
	if r.Spec.DisplayName != "" {
		return r.Spec.DisplayName
	}
	return r.Spec.Name
}

func normalizeDisplayName(name string) (string, error) {
	for _, c := range name {
		if unicode.IsControl(c) || c == '\u2028' || c == '\u2029' {
			return "", Fail("INVALID_ARGUMENT", "website name cannot contain line breaks or control characters")
		}
	}
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 80 {
		return "", Fail("INVALID_ARGUMENT", "website name must contain 1–80 characters")
	}
	return name, nil
}

func (m *Manager) policyLocked() Policy {
	if m.disk.Policy == nil {
		return DefaultPolicy()
	}
	return *m.disk.Policy
}

// SetWorkspaceResolver supplies the host-owned directory lookup for records
// created before private workspace recovery metadata was persisted.
func (m *Manager) SetWorkspaceResolver(resolve func(owner, session string) (string, error)) {
	m.mu.Lock()
	m.workspaceResolver = resolve
	m.mu.Unlock()
}

func (m *Manager) Profiles() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.profiles...)
}

func (b *boundBackend) Profiles() []string { return b.manager.Profiles() }

func (m *Manager) lifecycleURLs() bool {
	g, ok := m.options.Gateway.(interface{ LifecycleURLs() bool })
	return ok && g.LifecycleURLs()
}

func (m *Manager) countsActiveLocked(r Record) bool {
	if active(r.State) {
		return true
	}
	if restart, ok := m.disk.PolicyRestarts[r.ServiceID]; ok && (restart.State == "accepted" || restart.State == "running") && m.now().Before(r.LeaseExpiresAt) {
		return true
	}
	if op, ok := m.disk.Operations[r.ControlID]; ok && op.Action == "restart" && (op.Status == "accepted" || op.Status == "running") && m.busy[r.ServiceID] > 0 {
		return true
	}
	return false
}

func (m *Manager) Settings() SettingsState {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := SettingsState{Settings: m.policyLocked(), Profiles: append([]string(nil), m.profiles...), Usage: PolicyUsage{Users: map[string]int{}, Sessions: map[string]int{}}, Restarting: []SettingsRestart{}}
	for _, r := range m.disk.Records {
		if m.countsActiveLocked(r) {
			s.Usage.Total++
			s.Usage.Users[r.Owner]++
			s.Usage.Sessions[r.Session]++
		}
	}
	for _, restart := range m.disk.PolicyRestarts {
		restart.Workspace = ""
		if r, ok := m.disk.Records[restart.ServiceID]; ok {
			restart.Name = displayName(r)
		}
		s.Restarting = append(s.Restarting, restart)
	}
	sort.Slice(s.Restarting, func(i, j int) bool { return s.Restarting[i].ServiceID < s.Restarting[j].ServiceID })
	return s
}

func policyEffects(old, next Policy) SettingsEffects {
	e := SettingsEffects{Immediate: []string{}, RestartRequired: []string{}}
	if old.DefaultLeaseSeconds != next.DefaultLeaseSeconds {
		e.Immediate = append(e.Immediate, "default_lease_seconds")
	}
	if old.MaxPerSession != next.MaxPerSession {
		e.Immediate = append(e.Immediate, "max_per_session")
	}
	if old.MaxPerUser != next.MaxPerUser {
		e.Immediate = append(e.Immediate, "max_per_user")
	}
	if old.MaxTotal != next.MaxTotal {
		e.Immediate = append(e.Immediate, "max_total")
	}
	if old.InternalPort != next.InternalPort {
		e.RestartRequired = append(e.RestartRequired, "internal_port")
	}
	if old.CPUs != next.CPUs {
		e.RestartRequired = append(e.RestartRequired, "cpus")
	}
	if old.MemoryMiB != next.MemoryMiB {
		e.RestartRequired = append(e.RestartRequired, "memory_mib")
	}
	return e
}

// Resolve and check source access before a settings change stops a healthy
// runtime. The running container can still serve its own snapshot when its
// original host directory was removed, made unreadable, or replaced by a link.
func restartWorkspace(workspace, project, private string) (string, error) {
	workspace, err := canonicalWorkspace(workspace, private)
	if err != nil {
		return "", err
	}
	project, err = projectRelative(project)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return "", err
	}
	defer root.Close()
	part := ""
	for _, component := range strings.Split(filepath.ToSlash(project), "/") {
		part = filepath.Join(part, component)
		info, err := root.Lstat(part)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", Fail("INVALID_PROJECT_ROOT", "website project directory is unavailable")
		}
	}
	dir, err := root.Open(project)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	if _, err := dir.Readdirnames(1); err != nil && err != io.EOF {
		return "", err
	}
	return workspace, nil
}

// UpdateSettings persists policy and each restart intent together. Runtime
// changes enqueue managed website restarts; the returned accepted states and
// Settings().Restarting expose progress without tying recovery to an HTTP call.
func (m *Manager) UpdateSettings(ctx context.Context, policy Policy) (SettingsUpdate, error) {
	m.settingsMu.Lock()
	defer m.settingsMu.Unlock()
	if err := ctx.Err(); err != nil {
		return SettingsUpdate{}, err
	}
	if err := policy.Validate(); err != nil {
		return SettingsUpdate{}, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return SettingsUpdate{}, Fail("FEATURE_DISABLED", "service manager is closed")
	}
	effects := policyEffects(m.policyLocked(), policy)
	resolve := m.workspaceResolver
	candidates := []Record{}
	if len(effects.RestartRequired) > 0 {
		for _, r := range m.disk.Records {
			if active(r.State) && r.State != "stopping" && m.now().Before(r.LeaseExpiresAt) {
				candidates = append(candidates, r)
			}
		}
	}
	m.mu.Unlock()
	workspaces := map[string]string{}
	failures := map[string]*Error{}
	for _, r := range candidates {
		workspace := r.Workspace
		var err error
		if workspace == "" && resolve != nil {
			workspace, err = resolve(r.Owner, r.Session)
		}
		if err == nil {
			workspace, err = restartWorkspace(workspace, r.Spec.ProjectRoot, m.options.Root)
		}
		if err != nil {
			failures[r.ServiceID] = &Error{Code: "WORKSPACE_UNAVAILABLE", Message: "website settings were saved, but its original project directory could not be resolved for restart", Retryable: true}
		} else {
			workspaces[r.ServiceID] = workspace
		}
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return SettingsUpdate{}, Fail("FEATURE_DISABLED", "service manager is closed")
	}
	// Starts and ordinary restarts may have completed while legacy workspace
	// lookups ran. Include every current eligible generation in this one save.
	candidates = candidates[:0]
	if len(effects.RestartRequired) > 0 {
		for _, r := range m.disk.Records {
			if active(r.State) && r.State != "stopping" && m.now().Before(r.LeaseExpiresAt) && r.Runtime != policy.Runtime() {
				candidates = append(candidates, r)
				workspace := r.Workspace
				if workspace == "" {
					workspace = workspaces[r.ServiceID]
				}
				if workspace != "" {
					// Recheck the current generation, including starts accepted while
					// legacy lookups ran. A nonempty persisted path is not evidence
					// that it remains safe or readable.
					validated, err := restartWorkspace(workspace, r.Spec.ProjectRoot, m.options.Root)
					if err != nil {
						workspaces[r.ServiceID] = ""
						failures[r.ServiceID] = &Error{Code: "WORKSPACE_UNAVAILABLE", Message: "website settings were saved, but its original project directory is unavailable for restart; the running website was preserved", Retryable: true}
					} else {
						workspaces[r.ServiceID] = validated
						delete(failures, r.ServiceID)
					}
				}
				if workspaces[r.ServiceID] == "" && failures[r.ServiceID] == nil {
					failures[r.ServiceID] = &Error{Code: "WORKSPACE_UNAVAILABLE", Message: "website settings were saved, but its project directory could not be resolved for restart", Retryable: true}
				}
			}
		}
	}
	update := SettingsUpdate{Settings: policy, Effects: effects, Restarted: []SettingsRestart{}}
	queued := []SettingsRestart{}
	changed := []Record{}
	if err := m.commitLocked(func(d *diskState) {
		d.Policy = &policy
		for _, previous := range candidates {
			r, ok := d.Records[previous.ServiceID]
			if !ok || r.Generation != previous.Generation || r.ControlID != previous.ControlID || !active(r.State) || r.State == "stopping" || !m.now().Before(r.LeaseExpiresAt) {
				continue
			}
			restart := SettingsRestart{ServiceID: r.ServiceID, Name: displayName(r), State: "accepted", OperationID: randomID("settings_"), Workspace: workspaces[r.ServiceID]}
			if failure := failures[r.ServiceID]; failure != nil {
				restart.State = "failed"
				restart.Error = failure
			} else {
				r.Workspace = restart.Workspace
				r.ControlID = "settings:" + restart.OperationID
				r.State = "stopping"
				r.TerminalTarget = "stopped"
				r.Revision++
				r.UpdatedAt = m.now()
				d.Records[r.ServiceID] = r
				d.Operations[r.ControlID] = operation{Action: "restart", Digest: digest(policy), ServiceID: r.ServiceID, Generation: r.Generation + 1, Status: "accepted", Created: m.now()}
				queued = append(queued, restart)
				changed = append(changed, r)
			}
			d.PolicyRestarts[r.ServiceID] = restart
			public := restart
			public.Workspace = ""
			update.Restarted = append(update.Restarted, public)
		}
	}); err != nil {
		m.mu.Unlock()
		return SettingsUpdate{}, err
	}
	for _, restart := range queued {
		if cancel := m.launchCancel[restart.ServiceID]; cancel != nil {
			cancel()
		}
		id, key := restart.ServiceID, "settings:"+restart.OperationID
		m.queueLocked(id, func() { m.runSettingsRestart(id, key) })
	}
	m.mu.Unlock()
	for _, r := range changed {
		m.notify(r)
	}
	return update, nil
}

func (m *Manager) runSettingsRestart(id, key string) {
	m.mu.Lock()
	r, ok := m.disk.Records[id]
	restart, pending := m.disk.PolicyRestarts[id]
	if !ok || !pending || "settings:"+restart.OperationID != key || r.ControlID != key {
		m.mu.Unlock()
		return
	}
	restart.State = "running"
	if err := m.commitLocked(func(d *diskState) { d.PolicyRestarts[id] = restart }); err != nil {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	// A crash can occur after a successful launch but before this progress row
	// was finalized. Recover that row without restarting the website again.
	m.mu.Lock()
	op := m.disk.Operations[key]
	policy := m.policyLocked()
	m.mu.Unlock()
	scope := Scope{Owner: r.Owner, Session: r.Session, Workspace: restart.Workspace}
	if !(op.Status == "completed" && r.Runtime == policy.Runtime() && (r.State == "ready" || r.State == "reconciling")) {
		m.restart(scope, r, key, nil)
	} else if r.State == "reconciling" {
		m.check(r)
	}
	current, exists := m.current(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	latest, ok := m.disk.PolicyRestarts[id]
	if !ok || latest.OperationID != restart.OperationID {
		return
	}
	latest.State = "failed"
	if exists && current.ControlID == key && current.State == "ready" {
		latest.State = "completed"
		latest.Error = nil
	} else if exists {
		latest.Error = current.LastError
	}
	if latest.State == "failed" && latest.Error == nil {
		latest.Error = &Error{Code: "SETTINGS_RESTART_FAILED", Message: "website restart did not complete; check its status and logs", Retryable: true}
	}
	_ = m.commitLocked(func(d *diskState) { d.PolicyRestarts[id] = latest })
}
