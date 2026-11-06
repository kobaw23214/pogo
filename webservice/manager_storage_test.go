package webservice

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

type storageStatusRunner struct {
	*memoryRunner
	statusMu sync.Mutex
	status   *RuntimeStatus
}

type startupStorageGateway struct {
	*memoryGateway
	once    sync.Once
	entered chan Record
	release chan struct{}
	healthy bool
}

func (g *startupStorageGateway) Probe(ctx context.Context, rec Record) (Health, error) {
	g.once.Do(func() { g.entered <- rec })
	select {
	case <-g.release:
	case <-ctx.Done():
		return Health{}, ctx.Err()
	}
	if !g.healthy {
		return Health{}, Fail("HEALTH_CHECK_FAILED", "startup fixture has no HTTP response")
	}
	return g.memoryGateway.Probe(ctx, rec)
}

func TestManagerStartupStorageCausePrecedesReadinessTimeout(t *testing.T) {
	for _, tc := range []struct {
		name, code        string
		gone, stopFailure bool
	}{
		{"limit-live", "STORAGE_LIMIT_EXCEEDED", false, false},
		{"limit-already-gone", "STORAGE_LIMIT_EXCEEDED", true, false},
		{"unavailable-already-gone", "STORAGE_CHECK_UNAVAILABLE", true, false},
		{"limit-stop-unconfirmed", "STORAGE_LIMIT_EXCEEDED", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old, scope, base, baseGateway := managerFixture(t)
			_ = old.Close()
			runner := &storageStatusRunner{memoryRunner: base}
			gateway := &startupStorageGateway{memoryGateway: baseGateway, entered: make(chan Record, 1), release: make(chan struct{})}
			opts := old.options
			opts.Runner, opts.Gateway = runner, gateway
			opts.PollInterval, opts.HealthInterval, opts.StartTimeout = time.Hour, time.Hour, time.Hour
			m, err := NewManager(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			request := startRequest("start", "site")
			b, err := m.Execute(context.Background(), scope, request)
			if err != nil {
				t.Fatal(err)
			}
			id := decodeRecord(t, b).ServiceID
			var starting Record
			select {
			case starting = <-gateway.entered:
			case <-time.After(time.Second):
				t.Fatal("launch never began its HTTP probe")
			}
			usage := &StorageUsage{RuntimeBytes: 8192, DataBytes: 4096, OwnerDataBytes: 12288, CheckedAt: time.Now().UTC()}
			cause := &Error{Code: tc.code, Message: "storage enforcement during HTTP startup", Phase: "storage"}
			runner.statusMu.Lock()
			runner.status = &RuntimeStatus{Exists: !tc.gone, Running: !tc.gone, Instance: starting.Instance, Storage: usage, LastError: cause}
			runner.statusMu.Unlock()
			base.mu.Lock()
			base.stopFailure = tc.stopFailure
			if tc.gone {
				delete(base.instances, id)
			}
			base.mu.Unlock()
			close(gateway.release)
			target := "failed"
			if tc.stopFailure {
				target = "stopping"
			}
			current := waitManager(t, m, scope, id, target)
			m.mu.Lock()
			finished := m.tails[id]
			m.mu.Unlock()
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("startup storage cleanup did not settle promptly")
			}
			current, _ = m.current(id)
			if current.LastError == nil || current.LastError.Code != tc.code || current.LastError.Message != cause.Message || current.Storage == nil || current.Storage.OwnerDataBytes != usage.OwnerDataBytes || current.LastHealth != nil {
				t.Fatalf("startup lost the runner storage cause or became ready: %+v", current)
			}
			gateway.mu.Lock()
			_, routed := gateway.routes[id]
			gateway.mu.Unlock()
			if routed {
				t.Fatal("failed startup retained a route")
			}
			b, err = m.Execute(context.Background(), scope, request)
			if err != nil {
				t.Fatal(err)
			}
			var receipt struct {
				Error *Error `json:"operation_error"`
			}
			if err = json.Unmarshal(b, &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Error == nil || receipt.Error.Code != tc.code {
				t.Fatalf("start receipt lost storage cause: %s", b)
			}
			if tc.stopFailure {
				if current.TerminalTarget != "failed" || !current.LastError.Retryable || !active(current.State) {
					t.Fatalf("stop not confirmed but quota released: %+v", current)
				}
				if _, err = m.Execute(context.Background(), scope, startRequest("second", "second")); err != nil {
					t.Fatal(err)
				}
				_, err = m.Execute(context.Background(), scope, startRequest("third", "third"))
				requireCode(t, err, "QUOTA_EXCEEDED")
				base.mu.Lock()
				base.stopFailure = false
				base.mu.Unlock()
				m.stop(current, "", "failed")
				current, _ = m.current(id)
				if current.State != "failed" || current.LastError.Code != tc.code || current.LastError.Retryable {
					t.Fatalf("confirmed cleanup lost storage cause: %+v", current)
				}
			}
		})
	}
}

func TestManagerStartupDoesNotTrustHTTPAfterProcessExit(t *testing.T) {
	old, scope, base, baseGateway := managerFixture(t)
	_ = old.Close()
	runner := &storageStatusRunner{memoryRunner: base}
	gateway := &startupStorageGateway{memoryGateway: baseGateway, entered: make(chan Record, 1), release: make(chan struct{}), healthy: true}
	opts := old.options
	opts.Runner, opts.Gateway = runner, gateway
	opts.PollInterval, opts.HealthInterval = time.Hour, time.Hour
	m, err := NewManager(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	b, err := m.Execute(context.Background(), scope, startRequest("start", "site"))
	if err != nil {
		t.Fatal(err)
	}
	id := decodeRecord(t, b).ServiceID
	select {
	case <-gateway.entered:
	case <-time.After(time.Second):
		t.Fatal("probe did not begin")
	}
	runner.statusMu.Lock()
	runner.status = &RuntimeStatus{Exists: true, Running: false, ExitCode: 1}
	runner.statusMu.Unlock()
	close(gateway.release)
	r := waitManager(t, m, scope, id, "failed")
	if r.LastError == nil || r.LastError.Code != "SERVICE_EXITED" || r.LastHealth != nil {
		t.Fatalf("HTTP 2xx hid an exited process: %+v", r)
	}
}

type startupInspectBarrierRunner struct {
	*storageStatusRunner
	once    sync.Once
	entered chan time.Duration
	release chan struct{}
	stopMu  sync.Mutex
	stops   int
}

func (r *startupInspectBarrierRunner) Inspect(ctx context.Context, rec Record) (RuntimeStatus, error) {
	r.once.Do(func() {
		deadline, ok := ctx.Deadline()
		if !ok {
			r.entered <- 0
		} else {
			r.entered <- time.Until(deadline)
		}
		<-r.release
	})
	return r.storageStatusRunner.Inspect(ctx, rec)
}
func (r *startupInspectBarrierRunner) Stop(ctx context.Context, rec Record) error {
	r.stopMu.Lock()
	r.stops++
	r.stopMu.Unlock()
	return r.memoryRunner.Stop(ctx, rec)
}

func TestManagerStartupIgnoresSupersededRuntimeInspection(t *testing.T) {
	for _, boundary := range []string{"generation", "control", "stop"} {
		t.Run(boundary, func(t *testing.T) {
			old, scope, base, gateway := managerFixture(t)
			_ = old.Close()
			runner := &startupInspectBarrierRunner{storageStatusRunner: &storageStatusRunner{memoryRunner: base}, entered: make(chan time.Duration, 1), release: make(chan struct{})}
			opts := old.options
			opts.Runner = runner
			opts.PollInterval, opts.HealthInterval = time.Hour, time.Hour
			m, err := NewManager(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			var release sync.Once
			unblock := func() { release.Do(func() { close(runner.release) }) }
			defer unblock()
			b, err := m.Execute(context.Background(), scope, startRequest("start", "site"))
			if err != nil {
				t.Fatal(err)
			}
			id := decodeRecord(t, b).ServiceID
			select {
			case budget := <-runner.entered:
				if budget < 4*time.Second || budget > 5*time.Second {
					t.Fatalf("Inspect must cover the four-second meter but stay bounded: %s", budget)
				}
			case <-time.After(time.Second):
				t.Fatal("startup inspection did not begin")
			}
			// Inspection must not hold the manager lock while the runner is slow.
			read := make(chan error, 1)
			go func() {
				_, err := m.Execute(context.Background(), scope, Request{Action: "status", ServiceID: id})
				read <- err
			}()
			select {
			case err = <-read:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("runner Inspect blocked unrelated manager reads")
			}
			if boundary == "stop" {
				_, err = m.Execute(context.Background(), scope, Request{Action: "stop", ServiceID: id, OperationID: "new-stop"})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				m.mu.Lock()
				err = m.commitLocked(func(d *diskState) {
					current := d.Records[id]
					current.ControlID = "new-control"
					if boundary == "generation" {
						current.Generation++
					}
					d.Records[id] = current
				})
				m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			runner.statusMu.Lock()
			runner.status = &RuntimeStatus{Storage: &StorageUsage{DataBytes: 999}, LastError: &Error{Code: "STORAGE_LIMIT_EXCEEDED", Message: "late old observation"}}
			runner.statusMu.Unlock()
			unblock()
			if boundary == "stop" {
				current := waitManager(t, m, scope, id, "stopped")
				if current.LastError != nil || current.Storage != nil {
					t.Fatalf("late Inspect overwrote the newer stop: %+v", current)
				}
			} else {
				m.mu.Lock()
				finished := m.tails[id]
				m.mu.Unlock()
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Fatal("superseded startup inspection did not finish")
				}
				current, _ := m.current(id)
				if current.State != "starting" || current.ControlID != "new-control" || current.LastError != nil || current.Storage != nil {
					t.Fatalf("late Inspect changed a newer control/generation: %+v", current)
				}
				runner.stopMu.Lock()
				stops := runner.stops
				runner.stopMu.Unlock()
				if stops != 0 {
					t.Fatalf("late Inspect stopped a newer instance %d times", stops)
				}
			}
			gateway.mu.Lock()
			routed := gateway.routes[id]
			gateway.mu.Unlock()
			if routed.State == "ready" {
				t.Fatal("late inspection resurrected a ready route")
			}
		})
	}
}

func (r *storageStatusRunner) Inspect(ctx context.Context, rec Record) (RuntimeStatus, error) {
	r.statusMu.Lock()
	status := r.status
	r.statusMu.Unlock()
	if status != nil {
		return *status, nil
	}
	return r.memoryRunner.Inspect(ctx, rec)
}

func TestManagerStorageEnforcementPreservesCauseAndConfirmsStop(t *testing.T) {
	for _, tc := range []struct {
		code        string
		gone        bool
		stopFailure bool
	}{
		{"STORAGE_LIMIT_EXCEEDED", true, false},
		{"STORAGE_LIMIT_EXCEEDED", false, true},
	} {
		t.Run(tc.code+map[bool]string{true: "-already-removed", false: "-live"}[tc.gone], func(t *testing.T) {
			old, s, base, gateway := managerFixture(t)
			_ = old.Close()
			runner := &storageStatusRunner{memoryRunner: base}
			opts := old.options
			opts.Runner = runner
			opts.PollInterval, opts.HealthInterval = time.Hour, time.Hour
			m, err := NewManager(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			ctx := context.Background()
			b, err := m.Execute(ctx, s, startRequest("start", "site"))
			if err != nil {
				t.Fatal(err)
			}
			id := decodeRecord(t, b).ServiceID
			r := waitManager(t, m, s, id, "ready")
			open := Request{Action: "open", ServiceID: id, OperationID: "open"}
			if _, err = m.Execute(ctx, s, open); err != nil {
				t.Fatal(err)
			}
			usage := &StorageUsage{RuntimeBytes: 1024, DataBytes: 2048, OwnerDataBytes: 4096, CheckedAt: time.Now().UTC()}
			cause := &Error{Code: tc.code, Message: "trusted storage monitor stopped this service", Phase: "storage"}
			runner.statusMu.Lock()
			runner.status = &RuntimeStatus{Exists: !tc.gone, Running: !tc.gone, Instance: r.Instance, Storage: usage, LastError: cause}
			runner.statusMu.Unlock()
			base.mu.Lock()
			base.stopFailure = tc.stopFailure
			base.mu.Unlock()
			m.check(r)
			current, _ := m.current(id)
			if current.LastError == nil || current.LastError.Code != tc.code || current.LastError.Message != cause.Message || current.Storage == nil || current.Storage.OwnerDataBytes != 4096 {
				t.Fatalf("storage reason/measurement lost: %+v", current)
			}
			gateway.mu.Lock()
			_, routed := gateway.routes[id]
			gateway.mu.Unlock()
			if routed {
				t.Fatal("storage failure retained the preview route")
			}
			_, err = m.Execute(ctx, s, open)
			requireCode(t, err, "GRANT_EXPIRED")
			if tc.stopFailure {
				if current.State != "stopping" || current.TerminalTarget != "failed" || !current.LastError.Retryable || !active(current.State) {
					t.Fatalf("unconfirmed stop released quota: %+v", current)
				}
				base.mu.Lock()
				base.stopFailure = false
				base.mu.Unlock()
				m.stop(current, "", "failed")
				current, _ = m.current(id)
			}
			if current.State != "failed" || current.LastError.Code != tc.code || current.LastError.Retryable {
				t.Fatalf("confirmed storage stop not terminal: %+v", current)
			}
		})
	}
}
