package webservice

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryRunner struct {
	mu          sync.Mutex
	instances   map[string]Record
	starts      int
	stopFailure bool
	deleted     int
}

func newMemoryRunner() *memoryRunner                    { return &memoryRunner{instances: map[string]Record{}} }
func (f *memoryRunner) Preflight(context.Context) error { return nil }
func (f *memoryRunner) Start(_ context.Context, l Launch) (Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	r := l.Record
	r.Instance = Instance{ID: r.ServiceID, NetworkID: "private-net", Upstream: "127.0.0.1:8080"}
	f.instances[r.ServiceID] = r
	return r.Instance, nil
}
func (f *memoryRunner) Inspect(_ context.Context, r Record) (RuntimeStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	x, ok := f.instances[r.ServiceID]
	return RuntimeStatus{Exists: ok, Running: ok, Instance: x.Instance}, nil
}
func (f *memoryRunner) Stop(_ context.Context, r Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopFailure {
		return errors.New("not confirmed")
	}
	delete(f.instances, r.ServiceID)
	return nil
}
func (f *memoryRunner) Renew(_ context.Context, r Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.instances[r.ServiceID]; !ok {
		return errors.New("not running")
	}
	f.instances[r.ServiceID] = r
	return nil
}
func (f *memoryRunner) Logs(_ context.Context, r Record, _ string, _ int) (LogPage, error) {
	return LogPage{Generation: r.Generation, Text: "fixture output", EOF: !active(r.State)}, nil
}
func (f *memoryRunner) DeleteData(context.Context, Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted++
	return nil
}

type memoryGateway struct {
	mu        sync.Mutex
	failProbe bool
	routes    map[string]Record
}

func (f *memoryGateway) Preflight(context.Context) error { return nil }
func (f *memoryGateway) Apply(_ context.Context, r Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[r.ServiceID] = r
	return nil
}
func (f *memoryGateway) Revoke(_ context.Context, r Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.routes, r.ServiceID)
	return nil
}
func (f *memoryGateway) Probe(_ context.Context, r Record) (Health, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failProbe {
		return Health{}, errors.New("not HTTP ready")
	}
	return Health{Generation: r.Generation, CheckedAt: time.Now(), RelativePath: r.Spec.HealthPath, UpstreamStatus: 200, GatewayStatus: 200, Result: "passed"}, nil
}
func (f *memoryGateway) Open(_ context.Context, r Record) (Grant, error) {
	return Grant{ServiceID: r.ServiceID, Generation: r.Generation, GrantID: "grant", PreviewURL: "https://temporary.preview.test:19770/", ExpiresAt: time.Now().Add(time.Minute), LeaseExpiresAt: r.LeaseExpiresAt}, nil
}
func (f *memoryGateway) CloseGrant(context.Context, Record, string) error { return nil }

func managerFixture(t *testing.T) (*Manager, Scope, *memoryRunner, *memoryGateway) {
	t.Helper()
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "app"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "app", "app.py"), []byte("print('hello')"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := newMemoryRunner()
	gateway := &memoryGateway{routes: map[string]Record{}}
	m, err := NewManager(Options{Root: filepath.Join(base, "private"), Installation: "test-installation", Runner: runner, Gateway: gateway, PollInterval: 5 * time.Millisecond, HealthInterval: 20 * time.Millisecond, StartTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, Scope{Owner: "owner-a", Session: "session-a", Workspace: workspace}, runner, gateway
}
func startRequest(op, name string) Request {
	return Request{Action: "start", OperationID: op, Spec: Spec{Name: name, ProjectRoot: "app", Profile: "python-web", StartArgv: []string{"python3", "app.py"}}}
}
func decodeRecord(t *testing.T, data json.RawMessage) Record {
	t.Helper()
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return r
}
func waitManager(t *testing.T, m *Manager, s Scope, id, state string) Record {
	t.Helper()
	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		b, err := m.Execute(context.Background(), s, Request{Action: "status", ServiceID: id})
		if err == nil {
			r := decodeRecord(t, b)
			if r.State == state {
				return r
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	r, _ := m.current(id)
	t.Fatalf("waiting for %s, got %+v", state, r)
	return Record{}
}
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestManagerConcurrentStartIsIdempotentAndScoped(t *testing.T) {
	m, s, runner, _ := managerFixture(t)
	const n = 8
	var wg sync.WaitGroup
	ids := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := m.Execute(context.Background(), s, startRequest("same-op", "site"))
			if err != nil {
				t.Error(err)
				return
			}
			var r Record
			_ = json.Unmarshal(b, &r)
			ids <- r.ServiceID
		}()
	}
	wg.Wait()
	close(ids)
	id := ""
	for got := range ids {
		if id == "" {
			id = got
		}
		if id != got {
			t.Fatal("duplicate service")
		}
	}
	waitManager(t, m, s, id, "ready")
	runner.mu.Lock()
	count := runner.starts
	runner.mu.Unlock()
	if count != 1 {
		t.Fatalf("starts=%d", count)
	}
	q := startRequest("same-op", "changed")
	_, err := m.Execute(context.Background(), s, q)
	requireCode(t, err, "IDEMPOTENCY_CONFLICT")
	other := s
	other.Owner = "other"
	_, err = m.Execute(context.Background(), other, Request{Action: "status", ServiceID: id})
	requireCode(t, err, "NOT_FOUND")
	b, err := m.Execute(context.Background(), s, Request{Action: "status", ServiceID: id})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"owner-a", "session-a", "private-net", "127.0.0.1", "test-installation"} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatalf("public result leaked %q: %s", secret, b)
		}
	}
}

func TestManagerRestartKeepsIdentityAndDoesNotRenewLease(t *testing.T) {
	m, s, runner, _ := managerFixture(t)
	b, err := m.Execute(context.Background(), s, startRequest("start", "site"))
	if err != nil {
		t.Fatal(err)
	}
	r := waitManager(t, m, s, decodeRecord(t, b).ServiceID, "ready")
	oldDigest := r.SourceDigest
	lease := r.LeaseExpiresAt
	if err = os.WriteFile(filepath.Join(s.Workspace, "app", "app.py"), []byte("print('new version')"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = m.Execute(context.Background(), s, Request{Action: "restart", OperationID: "restart", ServiceID: r.ServiceID})
	if err != nil {
		t.Fatal(err)
	}
	r = waitManager(t, m, s, r.ServiceID, "ready")
	if r.Generation != 2 || r.SourceDigest == oldDigest || !r.LeaseExpiresAt.Equal(lease) {
		t.Fatalf("restart contract: %+v", r)
	}
	runner.mu.Lock()
	count := runner.starts
	runner.mu.Unlock()
	if count != 2 {
		t.Fatalf("starts=%d", count)
	}
}

func TestManagerFailureDoesNotClaimReadyOrLeakQuota(t *testing.T) {
	m, s, runner, gateway := managerFixture(t)
	gateway.mu.Lock()
	gateway.failProbe = true
	gateway.mu.Unlock()
	b, err := m.Execute(context.Background(), s, startRequest("start", "site"))
	if err != nil {
		t.Fatal(err)
	}
	r := waitManager(t, m, s, decodeRecord(t, b).ServiceID, "failed")
	if r.LastError == nil || r.LastError.Code != "HEALTH_CHECK_FAILED" {
		t.Fatalf("missing health cause: %+v", r)
	}
	runner.mu.Lock()
	count := len(runner.instances)
	runner.mu.Unlock()
	if count != 0 {
		t.Fatal("failed runtime still alive")
	}
}

func TestManagerUnconfirmedStopRetainsQuota(t *testing.T) {
	m, s, runner, _ := managerFixture(t)
	var first string
	for _, name := range []string{"one", "two"} {
		b, err := m.Execute(context.Background(), s, startRequest(name, name))
		if err != nil {
			t.Fatal(err)
		}
		r := waitManager(t, m, s, decodeRecord(t, b).ServiceID, "ready")
		if first == "" {
			first = r.ServiceID
		}
	}
	runner.mu.Lock()
	runner.stopFailure = true
	runner.mu.Unlock()
	_, err := m.Execute(context.Background(), s, Request{Action: "stop", OperationID: "stop", ServiceID: first})
	if err != nil {
		t.Fatal(err)
	}
	waitManager(t, m, s, first, "stopping")
	_, err = m.Execute(context.Background(), s, startRequest("third", "three"))
	requireCode(t, err, "QUOTA_EXCEEDED")
	runner.mu.Lock()
	runner.stopFailure = false
	runner.mu.Unlock()
	waitManager(t, m, s, first, "stopped")
}

func TestManagerCloseAndReloadAdoptsExistingRuntime(t *testing.T) {
	m, s, runner, gateway := managerFixture(t)
	b, err := m.Execute(context.Background(), s, startRequest("start", "site"))
	if err != nil {
		t.Fatal(err)
	}
	r := waitManager(t, m, s, decodeRecord(t, b).ServiceID, "ready")
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	count := len(runner.instances)
	runner.mu.Unlock()
	if count != 1 {
		t.Fatal("close killed website")
	}
	o := m.options
	o.OnChange = nil
	next, err := NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	waitManager(t, next, s, r.ServiceID, "ready")
	runner.mu.Lock()
	starts := runner.starts
	runner.mu.Unlock()
	if starts != 1 {
		t.Fatal("recovery reran code")
	}
	_ = gateway
}

func TestManagerLeaseExpiryAndDeletion(t *testing.T) {
	m, s, runner, _ := managerFixture(t)
	q := startRequest("start", "site")
	q.LeaseSeconds = 1
	b, err := m.Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	id := decodeRecord(t, b).ServiceID
	waitManager(t, m, s, id, "ready")
	waitManager(t, m, s, id, "expired")
	_, err = m.Execute(context.Background(), s, Request{Action: "renew", OperationID: "late", ServiceID: id, Spec: Spec{LeaseSeconds: 7200}})
	requireCode(t, err, "LEASE_EXPIRED")
	// The owner card must request a fresh lease through start, rather than
	// renewing an expired generation. The retained slot/data identity survives.
	q = startRequest("relaunch", "site")
	q.LeaseSeconds = 7200
	b, err = m.Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	relaunched := decodeRecord(t, b)
	if relaunched.ServiceID != id || relaunched.Generation != 2 {
		t.Fatalf("relaunch lost the retained slot: %+v", relaunched)
	}
	waitManager(t, m, s, id, "ready")
	if err = m.DeleteSession(context.Background(), s.Owner, s.Session); err != nil {
		t.Fatal(err)
	}
	_, err = m.Execute(context.Background(), s, startRequest("after-delete", "new"))
	requireCode(t, err, "NOT_FOUND")
	runner.mu.Lock()
	deleted := runner.deleted
	runner.mu.Unlock()
	if deleted != 1 {
		t.Fatalf("data cleanups=%d", deleted)
	}
}

func TestManagerDeletionRetryKeepsTombstone(t *testing.T) {
	m, s, runner, _ := managerFixture(t)
	b, err := m.Execute(context.Background(), s, startRequest("start", "site"))
	if err != nil {
		t.Fatal(err)
	}
	id := decodeRecord(t, b).ServiceID
	waitManager(t, m, s, id, "ready")
	runner.mu.Lock()
	runner.stopFailure = true
	runner.mu.Unlock()
	if err = m.DeleteSession(context.Background(), s.Owner, s.Session); err == nil {
		t.Fatal("unconfirmed deletion reported success")
	}
	runner.mu.Lock()
	runner.stopFailure = false
	runner.mu.Unlock()
	end := time.Now().Add(time.Second)
	for time.Now().Before(end) {
		if _, ok := m.current(id); !ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("tombstone did not retry cleanup")
}

func TestSnapshotExcludesSecretsAndRejectsSymlinkEscape(t *testing.T) {
	workspace := t.TempDir()
	for name, data := range map[string]string{"app.py": "print(1)", ".env": "SECRET=x", "private.key": "secret", "config.json": "{}"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	archive, hash, err := snapshot(workspace, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != 64 {
		t.Fatal(hash)
	}
	reader := tar.NewReader(bytes.NewReader(archive))
	names := []string{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
	}
	if strings.Join(names, ",") != "app.py,config.json" {
		t.Fatal(names)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	_ = os.WriteFile(outside, []byte("secret"), 0600)
	if err = os.Symlink(outside, filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	_, _, err = snapshot(workspace, ".")
	requireCode(t, err, "INVALID_PROJECT_ROOT")
}

func TestSnapshotRejectsReplacedWorkspaceAndCancellation(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	outside := filepath.Join(parent, "outside")
	_ = os.Mkdir(workspace, 0700)
	_ = os.Mkdir(outside, 0700)
	_ = os.WriteFile(filepath.Join(outside, "app.py"), []byte("outside"), 0600)
	if err := os.Remove(workspace); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, workspace); err != nil {
		t.Fatal(err)
	}
	_, err := canonicalWorkspace(workspace, filepath.Join(parent, "private"))
	requireCode(t, err, "INVALID_PROJECT_ROOT")
	_, _, err = snapshot(workspace, ".")
	requireCode(t, err, "INVALID_PROJECT_ROOT")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = snapshotContext(ctx, outside, ".")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("snapshot cancellation not respected: %v", err)
	}
}
