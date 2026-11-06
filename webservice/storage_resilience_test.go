package webservice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type storageRoundTripFunc func(*http.Request) (*http.Response, error)

func (f storageRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func failStorageRuntimeExec(r *DockerRunner, rec Record) func() {
	name, _, _, _, _ := r.names(rec)
	original := r.http.Transport
	var failing atomic.Bool
	failing.Store(true)
	r.http.Transport = storageRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if failing.Load() && strings.Contains(req.URL.Path, "/exec/meter-exec-"+name+"/start") {
			return nil, errors.New("fixture runtime exec unavailable")
		}
		return original.RoundTrip(req)
	})
	// Confirmed quota enforcement may still be cleaning up on another goroutine.
	// Toggle only the injected failure, never the client's live Transport field.
	return func() { failing.Store(false) }
}

func TestSlowRuntimeDoesNotStarveSiblingVolumeMeasurement(t *testing.T) {
	r, _ := newFakeDocker(t)
	a := startStorageFixture(t, r, runnerRecord())
	b := runnerRecord()
	b.ServiceID = "sibling-after-slow-runtime"
	b = startStorageFixture(t, r, b)
	if err := r.ReconcileStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _ := r.Inspect(context.Background(), a)
	name, _, _, _, _ := r.names(a)
	original := r.http.Transport
	r.http.Transport = storageRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/exec/meter-exec-"+name+"/start") {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		return original.RoundTrip(req)
	})
	if err := r.ReconcileStorage(context.Background()); err == nil {
		t.Fatal("slow runtime failure hidden")
	}
	broken, err := r.Inspect(context.Background(), a)
	if err != nil || !broken.Running || !storageWarning(broken.StorageWarning) || !broken.Storage.CheckedAt.Equal(before.Storage.CheckedAt) {
		t.Fatalf("slow runtime lost last valid measurement: %+v %v", broken, err)
	}
	healthy, err := r.Inspect(context.Background(), b)
	if err != nil || !healthy.Running || healthy.LastError != nil || healthy.StorageWarning != nil || healthy.Storage == nil || !healthy.Storage.CheckedAt.After(before.Storage.CheckedAt) {
		t.Fatalf("slow runtime starved sibling measurement: %+v %v", healthy, err)
	}
}

func TestStorageRuntimeFailureDoesNotSpreadToSibling(t *testing.T) {
	r, _ := newFakeDocker(t)
	a := startStorageFixture(t, r, runnerRecord())
	b := runnerRecord()
	b.ServiceID = "healthy-sibling"
	b = startStorageFixture(t, r, b)
	if err := r.ReconcileStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
	restore := failStorageRuntimeExec(r, a)
	defer restore()
	if err := r.ReconcileStorage(context.Background()); err == nil {
		t.Fatal("incomplete runtime scan was hidden")
	}
	broken, err := r.Inspect(context.Background(), a)
	if err != nil || !broken.Running || broken.LastError != nil || !storageWarning(broken.StorageWarning) {
		t.Fatalf("bad runtime scan changed availability: %+v %v", broken, err)
	}
	healthy, err := r.Inspect(context.Background(), b)
	if err != nil || !healthy.Running || healthy.LastError != nil || healthy.StorageWarning != nil || healthy.Storage == nil {
		t.Fatalf("sibling inherited unrelated scan failure: %+v %v", healthy, err)
	}
}

func TestRuntimeWarningCannotHideConfirmedDataOrOwnerExcess(t *testing.T) {
	for _, scope := range []string{"data", "owner", "workspace"} {
		t.Run(scope, func(t *testing.T) {
			r, f := newFakeDocker(t)
			a := startStorageFixture(t, r, runnerRecord())
			b := runnerRecord()
			b.ServiceID = "quota-sibling"
			b = startStorageFixture(t, r, b)
			if err := r.ReconcileStorage(context.Background()); err != nil {
				t.Fatal(err)
			}
			before, _ := r.Inspect(context.Background(), a)
			_, _, work, _, data := r.names(a)
			restore := failStorageRuntimeExec(r, a)
			defer restore()
			if scope == "data" {
				r.opts.DataBytesLimit = 100
			} else if scope == "owner" {
				r.opts.OwnerDataBytesLimit = 100
			} else {
				r.opts.RuntimeBytesLimit = 100
			}
			f.mu.Lock()
			if scope == "workspace" {
				f.storageSizes[work] = 101
			} else {
				f.storageSizes[data] = 101
			}
			f.mu.Unlock()
			_ = r.ReconcileStorage(context.Background())
			status := waitStorageStop(t, r, a, "STORAGE_LIMIT_EXCEEDED")
			if status.Storage == nil || !status.Storage.CheckedAt.Equal(before.Storage.CheckedAt) {
				t.Fatal("mixed incomplete scan replaced the last full usage")
			}
			field := "verified_data_bytes"
			if scope == "workspace" {
				field = "verified_workspace_bytes"
			}
			if status.LastError.Details[field] != float64(101) {
				t.Fatalf("confirmed quota evidence lost: %+v", status.LastError)
			}
			if scope == "owner" {
				waitStorageStop(t, r, b, "STORAGE_LIMIT_EXCEEDED")
			} else {
				healthy, err := r.Inspect(context.Background(), b)
				if err != nil || !healthy.Running || healthy.LastError != nil || healthy.StorageWarning != nil {
					t.Fatalf("site-specific excess spread to sibling: %+v %v", healthy, err)
				}
			}
		})
	}
}

func TestVerifiedStorageCountersDoNotWrapIntoTemporaryWarnings(t *testing.T) {
	r, f := newFakeDocker(t)
	rec := startStorageFixture(t, r, runnerRecord())
	name, _, work, _, _ := r.names(rec)
	f.mu.Lock()
	f.storageTmp[name] = int64(^uint64(0)>>1) - 1
	f.storageSizes[work] = 100
	f.mu.Unlock()
	if err := r.ReconcileStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := waitStorageStop(t, r, rec, "STORAGE_LIMIT_EXCEEDED")
	if status.Storage == nil || status.Storage.RuntimeBytes != int64(^uint64(0)>>1) || status.StorageWarning != nil {
		t.Fatalf("confirmed huge storage became an unknown warning: %+v", status)
	}
}

func TestStorageLegacyUnavailableSnapshotAndStaleUsageRemainNonfatal(t *testing.T) {
	r, _ := newFakeDocker(t)
	rec := startStorageFixture(t, r, runnerRecord())
	usage := &StorageUsage{RuntimeBytes: 123, DataBytes: 45, CheckedAt: time.Now().Add(-time.Hour)}
	data, _ := json.Marshal(storageSnapshot{Labels: r.labels(rec, true), Usage: usage, Issue: storageUnavailable(), CheckedAt: time.Now().Add(-time.Hour)})
	if err := os.WriteFile(r.storagePath(rec), data, 0600); err != nil {
		t.Fatal(err)
	}
	status, err := r.Inspect(context.Background(), rec)
	if err != nil || !status.Running || status.LastError != nil || !storageWarning(status.StorageWarning) || status.Storage.RuntimeBytes != 123 {
		t.Fatalf("legacy warning became fatal: %+v %v", status, err)
	}
	if err := r.ReconcileStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = r.Inspect(context.Background(), rec)
	if err != nil || !status.Running || status.LastError != nil || status.StorageWarning != nil {
		t.Fatalf("legacy warning latched after success: %+v %v", status, err)
	}
	if err := r.writeStorageSnapshot(rec, usage, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := r.loadStorageSnapshot(rec)
	snapshot.CheckedAt = time.Now().Add(-time.Hour)
	data, _ = json.Marshal(snapshot)
	if err := os.WriteFile(r.storagePath(rec), data, 0600); err != nil {
		t.Fatal(err)
	}
	status, err = r.Inspect(context.Background(), rec)
	if err != nil || !status.Running || status.LastError != nil || !storageWarning(status.StorageWarning) {
		t.Fatalf("stale snapshot stopped service: %+v %v", status, err)
	}
}

func TestStorageSnapshotWriteFailureWarnsWithoutStoppingHealthyWriter(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("ordinary filesystem permission enforcement")
	}
	r, _ := newFakeDocker(t)
	rec := startStorageFixture(t, r, runnerRecord())
	if err := r.ReconcileStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _ := r.Inspect(context.Background(), rec)
	if err := os.Chmod(r.opts.StateDir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(r.opts.StateDir, 0700)
	if err := r.ReconcileStorage(context.Background()); err == nil {
		t.Fatal("write failure hidden")
	}
	status, err := r.Inspect(context.Background(), rec)
	if err != nil || !status.Running || status.LastError != nil || !storageWarning(status.StorageWarning) || !status.Storage.CheckedAt.Equal(before.Storage.CheckedAt) {
		t.Fatalf("write failure destroyed valid state: %+v %v", status, err)
	}
	if err := os.Chmod(r.opts.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := r.ReconcileStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = r.Inspect(context.Background(), rec)
	if err != nil || !status.Running || status.StorageWarning != nil {
		t.Fatalf("successful persistence retained warning: %+v %v", status, err)
	}
}

func TestManagerStorageWarningsKeepRouteAndClearAfterRetry(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "warning-field", true: "legacy-last-error"}[legacy], func(t *testing.T) {
			old, scope, base, _ := managerFixture(t)
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
			value, err := m.Execute(context.Background(), scope, startRequest("start", "site"))
			if err != nil {
				t.Fatal(err)
			}
			rec := waitManager(t, m, scope, decodeRecord(t, value).ServiceID, "ready")
			rec, _ = m.current(rec.ServiceID)
			usage := &StorageUsage{RuntimeBytes: 500, DataBytes: 50, OwnerDataBytes: 50, CheckedAt: time.Now()}
			rec.Storage = usage
			if err := m.saveRecord(rec); err != nil {
				t.Fatal(err)
			}
			open := Request{Action: "open", ServiceID: rec.ServiceID, OperationID: "warning-route"}
			if _, err = m.Execute(context.Background(), scope, open); err != nil {
				t.Fatal(err)
			}
			warning := storageUnavailable()
			status := &RuntimeStatus{Exists: true, Running: true, Instance: rec.Instance, StorageWarning: warning}
			if legacy {
				status.LastError = warning
				status.StorageWarning = nil
			}
			runner.statusMu.Lock()
			runner.status = status
			runner.statusMu.Unlock()
			m.check(rec)
			current, _ := m.current(rec.ServiceID)
			if current.State != "ready" || current.LastError != nil || !storageWarning(current.StorageWarning) || current.Storage.RuntimeBytes != 500 {
				t.Fatalf("measurement warning changed readiness: %+v", current)
			}
			if _, err = m.Execute(context.Background(), scope, open); err != nil {
				t.Fatalf("warning revoked website: %v", err)
			}
			runner.statusMu.Lock()
			runner.status = &RuntimeStatus{Exists: true, Running: true, Instance: rec.Instance, Storage: usage}
			runner.statusMu.Unlock()
			m.check(current)
			current, _ = m.current(rec.ServiceID)
			if current.State != "ready" || current.StorageWarning != nil || current.LastError != nil {
				t.Fatalf("successful retry failed to clear warning: %+v", current)
			}
		})
	}
}
