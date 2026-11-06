package webservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitSettingsRestart(t *testing.T, m *Manager, id, state string) SettingsRestart {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range m.Settings().Restarting {
			if r.ServiceID == id && r.State == state {
				return r
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("settings restart never reached %s: %+v", state, m.Settings().Restarting)
	return SettingsRestart{}
}

func TestWebsitePolicyHotDefaultsAndQuotaDoNotRestartExistingWebsite(t *testing.T) {
	m, s, runner, _ := managerFixture(t)
	ctx := context.Background()
	p := DefaultPolicy()
	p.DefaultLeaseSeconds = 24 * 3600
	if _, err := m.UpdateSettings(ctx, p); err != nil {
		t.Fatal(err)
	}
	b, err := m.Execute(ctx, s, startRequest("first", "first"))
	if err != nil {
		t.Fatal(err)
	}
	r := waitManager(t, m, s, decodeRecord(t, b).ServiceID, "ready")
	if remaining := time.Until(r.LeaseExpiresAt); remaining < 23*time.Hour || remaining > 24*time.Hour {
		t.Fatalf("configured default lease not used: %s", remaining)
	}
	p.DefaultLeaseSeconds = 3600
	p.MaxPerSession = 1
	update, err := m.UpdateSettings(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Restarted) != 0 || len(update.Effects.RestartRequired) != 0 {
		t.Fatalf("hot settings caused restart: %+v", update)
	}
	current, _ := m.current(r.ServiceID)
	if current.Generation != r.Generation || !current.LeaseExpiresAt.Equal(r.LeaseExpiresAt) {
		t.Fatal("existing website was changed by new defaults")
	}
	_, err = m.Execute(ctx, s, startRequest("second", "second"))
	requireCode(t, err, "QUOTA_EXCEEDED")
	e := asError(err)
	if e.Details["scope"] != "session" || e.Details["current"] != 1 || e.Details["limit"] != 1 {
		t.Fatalf("quota lacks actionable details: %+v", e)
	}
	list, err := m.Execute(ctx, s, Request{Action: "list"})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		DefaultLeaseSeconds int        `json:"default_lease_seconds"`
		Quota               QuotaUsage `json:"quota"`
	}
	if err = json.Unmarshal(list, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.DefaultLeaseSeconds != 3600 || payload.Quota.Session != (QuotaCount{1, 1}) {
		t.Fatalf("wrong UI defaults/quota: %+v", payload)
	}
	runner.mu.Lock()
	starts := runner.starts
	runner.mu.Unlock()
	if starts != 1 {
		t.Fatalf("hot settings started %d runtimes", starts)
	}
}

func TestWebsitePolicyRuntimeSaveRestartsAllActiveWebsitesPreservingLifecycle(t *testing.T) {
	m, s, runner, _ := managerFixture(t)
	ctx := context.Background()
	before := []Record{}
	for _, name := range []string{"first", "second"} {
		b, err := m.Execute(ctx, s, startRequest("start-"+name, name))
		if err != nil {
			t.Fatal(err)
		}
		before = append(before, waitManager(t, m, s, decodeRecord(t, b).ServiceID, "ready"))
	}
	p := DefaultPolicy()
	p.CPUs = 2
	p.MemoryMiB = 1024
	p.InternalPort = 9090
	update, err := m.UpdateSettings(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Restarted) != 2 || len(update.Effects.RestartRequired) != 3 {
		t.Fatalf("missing restart effects: %+v", update)
	}
	for _, old := range before {
		status := waitSettingsRestart(t, m, old.ServiceID, "completed")
		if status.Workspace != "" {
			t.Fatal("private workspace leaked in settings status")
		}
		current, _ := m.current(old.ServiceID)
		if current.Generation != old.Generation+1 || current.LeaseID != old.LeaseID || !current.LeaseExpiresAt.Equal(old.LeaseExpiresAt) || current.Runtime != p.Runtime() {
			t.Fatalf("runtime restart changed lifecycle or missed settings: %+v", current)
		}
	}
	runner.mu.Lock()
	deletes, starts := runner.deleted, runner.starts
	runner.mu.Unlock()
	if deletes != 0 || starts != 4 {
		t.Fatalf("wrong restart/data effects: deleted=%d starts=%d", deletes, starts)
	}
}

func TestWebsitePolicyRuntimeRestartFailureIsVisible(t *testing.T) {
	m, s, runner, _ := managerFixture(t)
	ctx := context.Background()
	b, err := m.Execute(ctx, s, startRequest("start", "site"))
	if err != nil {
		t.Fatal(err)
	}
	old := waitManager(t, m, s, decodeRecord(t, b).ServiceID, "ready")
	runner.mu.Lock()
	runner.stopFailure = true
	runner.mu.Unlock()
	p := DefaultPolicy()
	p.CPUs = 2
	if _, err = m.UpdateSettings(ctx, p); err != nil {
		t.Fatal(err)
	}
	status := waitSettingsRestart(t, m, old.ServiceID, "failed")
	if status.Error == nil || m.Settings().Settings.CPUs != 2 {
		t.Fatalf("restart failure or saved settings hidden: %+v", status)
	}
	r, _ := m.current(old.ServiceID)
	if r.Generation != old.Generation || r.State != "stopping" {
		t.Fatalf("unconfirmed runtime was replaced: %+v", r)
	}
	runner.mu.Lock()
	runner.stopFailure = false
	runner.mu.Unlock()
}

func TestWebsiteRenamePreservesSlotRuntimeAndSurvivesStaleObservation(t *testing.T) {
	m, s, _, _ := managerFixture(t)
	ctx := context.Background()
	b, err := m.Execute(ctx, s, startRequest("start", "stable-slot"))
	if err != nil {
		t.Fatal(err)
	}
	old := waitManager(t, m, s, decodeRecord(t, b).ServiceID, "ready")
	observation, _ := m.current(old.ServiceID)
	b, err = m.Execute(ctx, s, Request{Action: "rename", OperationID: "rename", ServiceID: old.ServiceID, Spec: Spec{DisplayName: "  我的测试网站  "}})
	if err != nil {
		t.Fatal(err)
	}
	var view map[string]any
	if err = json.Unmarshal(b, &view); err != nil {
		t.Fatal(err)
	}
	if view["name"] != "我的测试网站" || view["slot_name"] != "stable-slot" || view["workspace"] != nil {
		t.Fatalf("wrong public metadata: %+v", view)
	}
	if err = m.saveRecord(observation); err != nil {
		t.Fatal(err)
	}
	r, _ := m.current(old.ServiceID)
	if displayName(r) != "我的测试网站" || r.Spec.Name != "stable-slot" || r.SpecDigest != old.SpecDigest || r.Generation != old.Generation || r.LeaseID != old.LeaseID || !r.LeaseExpiresAt.Equal(old.LeaseExpiresAt) {
		t.Fatalf("rename lost runtime identity: %+v", r)
	}
	for _, invalid := range []string{"", strings.Repeat("中", 81), "hello\nworld", "title\t", "x\u2028y"} {
		_, err = m.Execute(ctx, s, Request{Action: "rename", OperationID: "invalid-" + digest(invalid), ServiceID: old.ServiceID, Spec: Spec{DisplayName: invalid}})
		requireCode(t, err, "INVALID_ARGUMENT")
	}
}

func TestWebsitePolicyLongRenewalAndPrivateWorkspacePersistence(t *testing.T) {
	m, s, _, _ := managerFixture(t)
	ctx := context.Background()
	q := startRequest("start", "site")
	q.LeaseSeconds = 24 * 3600
	b, err := m.Execute(ctx, s, q)
	if err != nil {
		t.Fatal(err)
	}
	old := waitManager(t, m, s, decodeRecord(t, b).ServiceID, "ready")
	if _, err = m.Execute(ctx, s, Request{Action: "renew", OperationID: "renew", ServiceID: old.ServiceID, Spec: Spec{LeaseSeconds: 7 * 24 * 3600}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, _ := m.current(old.ServiceID)
		if time.Until(r.LeaseExpiresAt) > 6*24*time.Hour {
			if r.LeaseID != old.LeaseID || r.Workspace != s.Workspace {
				t.Fatal("renew changed website identity")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("long renewal was not applied")
}

func TestWebsitePolicyLegacyMigrationAndWorkspaceResolver(t *testing.T) {
	old, s, _, _ := managerFixture(t)
	ctx := context.Background()
	b, err := old.Execute(ctx, s, startRequest("start", "site"))
	if err != nil {
		t.Fatal(err)
	}
	r := waitManager(t, old, s, decodeRecord(t, b).ServiceID, "ready")
	r, _ = old.current(r.ServiceID)
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	old.disk.Policy = nil
	old.disk.PolicyRestarts = nil
	legacy := old.disk.Records[r.ServiceID]
	legacy.LeaseID = ""
	legacy.Runtime = RuntimeConfig{}
	legacy.Workspace = ""
	legacy.DisplayName = ""
	old.disk.Records[r.ServiceID] = legacy
	if err = old.write(old.disk); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(old.options)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.SetWorkspaceResolver(func(owner, session string) (string, error) {
		if owner != s.Owner || session != s.Session {
			t.Error("resolver received wrong identity")
		}
		return s.Workspace, nil
	})
	waitManager(t, m, s, r.ServiceID, "ready")
	current, _ := m.current(r.ServiceID)
	if current.LeaseID == "" || current.Runtime != (RuntimeConfig{}) || current.Generation != r.Generation || m.Settings().Settings != DefaultPolicy() {
		t.Fatalf("legacy migration changed runtime or missed defaults: %+v", current)
	}
	p := DefaultPolicy()
	p.CPUs = 2
	if _, err = m.UpdateSettings(ctx, p); err != nil {
		t.Fatal(err)
	}
	waitSettingsRestart(t, m, r.ServiceID, "completed")
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(m.Root(), "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved diskState
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Policy == nil || *saved.Policy != p || saved.Records[r.ServiceID].Workspace != s.Workspace {
		t.Fatal("settings/recovery workspace were not persisted")
	}
}

func TestWebsitePolicyRecoversRestartIntentAfterConfirmedStop(t *testing.T) {
	old, s, runner, _ := managerFixture(t)
	ctx := context.Background()
	b, err := old.Execute(ctx, s, startRequest("start", "site"))
	if err != nil {
		t.Fatal(err)
	}
	r := waitManager(t, old, s, decodeRecord(t, b).ServiceID, "ready")
	r, _ = old.current(r.ServiceID)
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	p := DefaultPolicy()
	p.MemoryMiB = 1024
	key := "settings:recover"
	r.State = "stopped"
	r.ControlID = key
	old.disk.Policy = &p
	old.disk.Records[r.ServiceID] = r
	old.disk.PolicyRestarts[r.ServiceID] = SettingsRestart{ServiceID: r.ServiceID, Name: displayName(r), State: "running", OperationID: "recover", Workspace: s.Workspace}
	old.disk.Operations[key] = operation{Action: "restart", ServiceID: r.ServiceID, Generation: r.Generation + 1, Status: "accepted", Created: time.Now()}
	if err = runner.Stop(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err = old.write(old.disk); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(old.options)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	waitSettingsRestart(t, m, r.ServiceID, "completed")
	current, _ := m.current(r.ServiceID)
	if current.Generation != r.Generation+1 || current.Runtime != p.Runtime() || current.LeaseID != r.LeaseID || !current.LeaseExpiresAt.Equal(r.LeaseExpiresAt) {
		t.Fatalf("crash recovery lost lifecycle: %+v", current)
	}
}

type profilePolicyRunner struct{ *memoryRunner }

func (*profilePolicyRunner) Profiles(context.Context) ([]string, error) {
	return []string{"python-web", "node-web"}, nil
}

func TestWebsitePolicyProfilesComeFromHostCapabilities(t *testing.T) {
	old, s, runner, _ := managerFixture(t)
	_ = old.Close()
	o := old.options
	o.Runner = &profilePolicyRunner{runner}
	m, err := NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	q := startRequest("node", "node")
	q.Profile = "node-web"
	q.StartArgv = []string{"node", "app.js"}
	_, err = m.Execute(context.Background(), s, q)
	requireCode(t, err, "PROFILE_UNAVAILABLE")
	if err = m.Preflight(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Profiles(), []string{"python-web", "node-web"}) {
		t.Fatal("host profiles not cached")
	}
	b, err := m.Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	waitManager(t, m, s, decodeRecord(t, b).ServiceID, "ready")
}

func TestWebsitePolicyConcurrentSaveIncludesNewStartsAndDoesNotReviveStoppedWebsite(t *testing.T) {
	m, s, _, _ := managerFixture(t)
	ctx := context.Background()
	b, err := m.Execute(ctx, s, startRequest("first", "first"))
	if err != nil {
		t.Fatal(err)
	}
	first := waitManager(t, m, s, decodeRecord(t, b).ServiceID, "ready")
	m.mu.Lock()
	r := m.disk.Records[first.ServiceID]
	r.Workspace = ""
	m.disk.Records[first.ServiceID] = r
	m.mu.Unlock()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	m.SetWorkspaceResolver(func(owner, session string) (string, error) {
		once.Do(func() { close(entered) })
		<-release
		return s.Workspace, nil
	})
	policy := DefaultPolicy()
	policy.CPUs = 2
	type result struct {
		update SettingsUpdate
		err    error
	}
	finished := make(chan result, 1)
	go func() { u, e := m.UpdateSettings(ctx, policy); finished <- result{u, e} }()
	<-entered
	secondData, err := m.Execute(ctx, s, startRequest("second", "second"))
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	second := waitManager(t, m, s, decodeRecord(t, secondData).ServiceID, "ready")
	if _, err = m.Execute(ctx, s, Request{Action: "stop", OperationID: "stop-first", ServiceID: first.ServiceID}); err != nil {
		close(release)
		t.Fatal(err)
	}
	waitManager(t, m, s, first.ServiceID, "stopped")
	close(release)
	got := <-finished
	if got.err != nil {
		t.Fatal(got.err)
	}
	if len(got.update.Restarted) != 1 || got.update.Restarted[0].ServiceID != second.ServiceID {
		t.Fatalf("concurrent save missed new start or revived user stop: %+v", got.update)
	}
	waitSettingsRestart(t, m, second.ServiceID, "completed")
	current, _ := m.current(first.ServiceID)
	if current.State != "stopped" || current.Generation != first.Generation {
		t.Fatalf("settings revived user-stopped site: %+v", current)
	}
}

func TestWebsitePolicyConcurrentSavesApplyLatestRuntime(t *testing.T) {
	m, s, _, _ := managerFixture(t)
	ctx := context.Background()
	b, err := m.Execute(ctx, s, startRequest("start", "site"))
	if err != nil {
		t.Fatal(err)
	}
	original := waitManager(t, m, s, decodeRecord(t, b).ServiceID, "ready")
	m.mu.Lock()
	r := m.disk.Records[original.ServiceID]
	r.Workspace = ""
	m.disk.Records[original.ServiceID] = r
	m.mu.Unlock()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	m.SetWorkspaceResolver(func(owner, session string) (string, error) {
		once.Do(func() { close(entered) })
		<-release
		return s.Workspace, nil
	})
	first := DefaultPolicy()
	first.CPUs = 2
	second := first
	second.MemoryMiB = 1024
	finished := make(chan error, 2)
	go func() { _, e := m.UpdateSettings(ctx, first); finished <- e }()
	<-entered
	go func() { _, e := m.UpdateSettings(ctx, second); finished <- e }()
	close(release)
	for range 2 {
		if e := <-finished; e != nil {
			t.Fatal(e)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := m.current(original.ServiceID)
		if current.State == "ready" && current.Runtime == second.Runtime() {
			if current.LeaseID != original.LeaseID || !current.LeaseExpiresAt.Equal(original.LeaseExpiresAt) {
				t.Fatal("concurrent settings changed lease")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("latest saved runtime was not applied: %+v", m.Settings())
}

type lifecyclePolicyGateway struct {
	*memoryGateway
	opens int
}

func (*lifecyclePolicyGateway) LifecycleURLs() bool { return true }
func (g *lifecyclePolicyGateway) Open(_ context.Context, r Record) (Grant, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	current, ok := g.routes[r.ServiceID]
	if !ok || current.Revision != r.Revision {
		return Grant{}, Fail("STALE_ROUTE", "route revision changed")
	}
	if r.OperationID == "" {
		return Grant{}, errors.New("open operation identity missing")
	}
	g.opens++
	return Grant{ServiceID: r.ServiceID, Generation: r.Generation, GrantID: r.LeaseID, PreviewURL: "https://" + digest(r.LeaseID)[:48] + ".preview.test:19770/", ExpiresAt: r.LeaseExpiresAt, LeaseExpiresAt: r.LeaseExpiresAt}, nil
}

func TestWebsiteRenameCanOpenImmediatelyAndLifecycleReceiptTracksRenewal(t *testing.T) {
	old, s, _, base := managerFixture(t)
	_ = old.Close()
	o := old.options
	o.PollInterval = time.Hour
	o.HealthInterval = time.Hour
	gateway := &lifecyclePolicyGateway{memoryGateway: base}
	o.Gateway = gateway
	m, err := NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx := context.Background()
	b, err := m.Execute(ctx, s, startRequest("start", "site"))
	if err != nil {
		t.Fatal(err)
	}
	r := waitManager(t, m, s, decodeRecord(t, b).ServiceID, "ready")
	open := Request{Action: "open", OperationID: "stable-open", ServiceID: r.ServiceID}
	firstRaw, err := m.Execute(ctx, s, open)
	if err != nil {
		t.Fatal(err)
	}
	var first Grant
	if err = json.Unmarshal(firstRaw, &first); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Execute(ctx, s, Request{Action: "rename", OperationID: "rename", ServiceID: r.ServiceID, Spec: Spec{DisplayName: "重命名网站"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Execute(ctx, s, Request{Action: "open", OperationID: "renamed-open", ServiceID: r.ServiceID}); err != nil {
		t.Fatalf("rename needs a health tick to open: %v", err)
	}
	if _, err = m.Execute(ctx, s, Request{Action: "renew", OperationID: "long-renew", ServiceID: r.ServiceID, Spec: Spec{LeaseSeconds: 48 * 3600}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := m.current(r.ServiceID)
		if time.Until(current.LeaseExpiresAt) > 47*time.Hour {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	replayed, err := m.Execute(ctx, s, open)
	if err != nil {
		t.Fatal(err)
	}
	var after Grant
	if err = json.Unmarshal(replayed, &after); err != nil {
		t.Fatal(err)
	}
	if after.PreviewURL != first.PreviewURL || !after.ExpiresAt.Equal(after.LeaseExpiresAt) || time.Until(after.ExpiresAt) < 47*time.Hour {
		t.Fatalf("lifecycle receipt did not follow renewal: %+v", after)
	}
	// A main-process restart loses in-memory open receipts, but the same
	// authenticated operation must be able to retrieve the lifecycle URL.
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	o.PollInterval = 5 * time.Millisecond
	reloaded, err := NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	waitManager(t, reloaded, s, r.ServiceID, "ready")
	againRaw, err := reloaded.Execute(ctx, s, open)
	if err != nil {
		t.Fatal(err)
	}
	var again Grant
	if err = json.Unmarshal(againRaw, &again); err != nil {
		t.Fatal(err)
	}
	if again.PreviewURL != first.PreviewURL {
		t.Fatal("main restart changed lifecycle URL")
	}
}

func TestWebsitePolicyUnavailableSourceDoesNotStopRunningWebsite(t *testing.T) {
	for _, kind := range []string{"deleted-workspace", "symlink-workspace", "unreadable-workspace", "deleted-project", "symlink-project"} {
		t.Run(kind, func(t *testing.T) {
			m, s, runner, _ := managerFixture(t)
			ctx := context.Background()
			b, err := m.Execute(ctx, s, startRequest("start", "site"))
			if err != nil {
				t.Fatal(err)
			}
			before := waitManager(t, m, s, decodeRecord(t, b).ServiceID, "ready")
			switch kind {
			case "deleted-workspace":
				if err = os.RemoveAll(s.Workspace); err != nil {
					t.Fatal(err)
				}
			case "symlink-workspace":
				moved := s.Workspace + "-original"
				if err = os.Rename(s.Workspace, moved); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(moved, s.Workspace); err != nil {
					t.Fatal(err)
				}
			case "unreadable-workspace":
				if os.Geteuid() == 0 {
					t.Skip("root bypasses filesystem read permissions")
				}
				if err = os.Chmod(s.Workspace, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(s.Workspace, 0700) })
			case "deleted-project":
				if err = os.RemoveAll(filepath.Join(s.Workspace, "app")); err != nil {
					t.Fatal(err)
				}
			case "symlink-project":
				from, to := filepath.Join(s.Workspace, "app"), filepath.Join(s.Workspace, "app-original")
				if err = os.Rename(from, to); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(to, from); err != nil {
					t.Fatal(err)
				}
			}
			policy := DefaultPolicy()
			policy.CPUs = 2
			update, err := m.UpdateSettings(ctx, policy)
			if err != nil {
				t.Fatal(err)
			}
			if len(update.Restarted) != 1 || update.Restarted[0].State != "failed" || update.Restarted[0].Error == nil || update.Restarted[0].Error.Code != "WORKSPACE_UNAVAILABLE" {
				t.Fatalf("source failure was not reported: %+v", update)
			}
			current, _ := m.current(before.ServiceID)
			if current.State != "ready" || current.Generation != before.Generation || current.Runtime != before.Runtime || current.LeaseID != before.LeaseID || !current.LeaseExpiresAt.Equal(before.LeaseExpiresAt) {
				t.Fatalf("running website was changed despite unavailable source: %+v", current)
			}
			runner.mu.Lock()
			_, running := runner.instances[before.ServiceID]
			starts, deletes := runner.starts, runner.deleted
			runner.mu.Unlock()
			if !running || starts != 1 || deletes != 0 {
				t.Fatalf("runtime/data changed: running=%v starts=%d deletes=%d", running, starts, deletes)
			}
		})
	}
}

type delayedLifecyclePolicyGateway struct {
	*lifecyclePolicyGateway
	entered    chan struct{}
	release    chan struct{}
	closeCalls int
}

func (g *delayedLifecyclePolicyGateway) Open(ctx context.Context, r Record) (Grant, error) {
	if r.OperationID == "delayed" {
		close(g.entered)
		select {
		case <-g.release:
		case <-ctx.Done():
			return Grant{}, ctx.Err()
		}
	}
	grant, err := g.lifecyclePolicyGateway.Open(ctx, r)
	if err != nil {
		return Grant{}, err
	}
	g.mu.Lock()
	version := g.closeCalls
	g.mu.Unlock()
	grant.PreviewURL = "https://" + digest(r.LeaseID + strconv.Itoa(version))[:48] + ".preview.test:19770/"
	return grant, nil
}
func (g *delayedLifecyclePolicyGateway) CloseGrant(context.Context, Record, string) error {
	g.mu.Lock()
	g.closeCalls++
	g.mu.Unlock()
	return nil
}

func TestWebsiteFailedOpenDoesNotRotateSharedLifecycleAddress(t *testing.T) {
	old, s, _, base := managerFixture(t)
	_ = old.Close()
	gateway := &delayedLifecyclePolicyGateway{lifecyclePolicyGateway: &lifecyclePolicyGateway{memoryGateway: base}, entered: make(chan struct{}), release: make(chan struct{})}
	o := old.options
	o.Gateway = gateway
	o.PollInterval = time.Hour
	o.HealthInterval = time.Hour
	m, err := NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx := context.Background()
	b, err := m.Execute(ctx, s, startRequest("start", "site"))
	if err != nil {
		t.Fatal(err)
	}
	r := waitManager(t, m, s, decodeRecord(t, b).ServiceID, "ready")
	open := func(id string) Grant {
		t.Helper()
		data, err := m.Execute(ctx, s, Request{Action: "open", OperationID: id, ServiceID: r.ServiceID})
		if err != nil {
			t.Fatal(err)
		}
		var grant Grant
		if err = json.Unmarshal(data, &grant); err != nil {
			t.Fatal(err)
		}
		return grant
	}
	shared := open("shared")
	finished := make(chan error, 1)
	go func() {
		_, e := m.Execute(ctx, s, Request{Action: "open", OperationID: "delayed", ServiceID: r.ServiceID})
		finished <- e
	}()
	<-gateway.entered
	m.invalidateGrantCache()
	close(gateway.release)
	requireCode(t, <-finished, "GRANT_EXPIRED")
	gateway.mu.Lock()
	closes := gateway.closeCalls
	gateway.mu.Unlock()
	if closes != 0 {
		t.Fatalf("failed open revoked the shared website address %d times", closes)
	}
	if after := open("after-failure"); after.PreviewURL != shared.PreviewURL {
		t.Fatal("failed open changed the website address")
	}
	if _, err = m.Execute(ctx, s, Request{Action: "revoke", OperationID: "explicit-revoke", ServiceID: r.ServiceID, GrantID: shared.GrantID}); err != nil {
		t.Fatal(err)
	}
	afterRevoke := open("after-explicit-revoke")
	gateway.mu.Lock()
	closes = gateway.closeCalls
	gateway.mu.Unlock()
	if closes != 1 || afterRevoke.PreviewURL == shared.PreviewURL {
		t.Fatal("explicit access revocation did not rotate the website address")
	}
}
