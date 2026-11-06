package webservice

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func awaitServiceAbsent(t *testing.T, m *Manager, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := m.current(id); !ok {
			return
		}
		time.Sleep(time.Millisecond * 5)
	}
	t.Fatal("deleted service is still retained")
}

func TestManagerDeleteServiceIsScopedAndLeavesSourcesAndSiblings(t *testing.T) {
	m, s, runner, gateway := managerFixture(t)
	b, err := m.Execute(context.Background(), s, startRequest("start-target", "target"))
	if err != nil {
		t.Fatal(err)
	}
	id := decodeRecord(t, b).ServiceID
	waitManager(t, m, s, id, "ready")
	_, err = m.Execute(context.Background(), s, Request{Action: "open", ServiceID: id, OperationID: "old-open"})
	if err != nil {
		t.Fatal(err)
	}
	otherScope := s
	otherScope.Session = "another-session"
	var others []string
	for _, scope := range []Scope{s, otherScope} {
		b, err := m.Execute(context.Background(), scope, startRequest("start-sibling", "sibling"))
		if err != nil {
			t.Fatal(err)
		}
		other := decodeRecord(t, b).ServiceID
		waitManager(t, m, scope, other, "ready")
		others = append(others, other)
	}
	for _, scope := range []Scope{{Owner: "other-owner", Session: s.Session}, otherScope} {
		requireCode(t, m.DeleteService(context.Background(), scope, id), "NOT_FOUND")
	}
	if err := m.DeleteService(context.Background(), s, id); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteService(context.Background(), s, id); err != nil {
		t.Fatal("repeated delete failed:", err)
	}
	awaitServiceAbsent(t, m, id)
	for _, sibling := range others {
		if r, ok := m.current(sibling); !ok || r.State != "ready" {
			t.Fatal("deleted an unrelated service")
		}
	}
	if _, err := os.Stat(filepath.Join(s.Workspace, "app", "app.py")); err != nil {
		t.Fatal("workspace source was removed:", err)
	}
	runner.mu.Lock()
	deleted, live := runner.deleted, len(runner.instances)
	runner.mu.Unlock()
	if deleted != 1 || live != 2 {
		t.Fatalf("deleted data=%d live runtimes=%d", deleted, live)
	}
	gateway.mu.Lock()
	_, routeExists := gateway.routes[id]
	gateway.mu.Unlock()
	if routeExists {
		t.Fatal("deleted website route still exists")
	}
	m.mu.Lock()
	_, historyExists := m.disk.History[id]
	for _, grant := range m.grants {
		if grant.ServiceID == id {
			t.Error("deleted website grant is cached")
		}
	}
	m.mu.Unlock()
	if historyExists {
		t.Fatal("deleted website history is retained")
	}
	_, err = m.Execute(context.Background(), s, startRequest("start-target", "target"))
	requireCode(t, err, "OPERATION_RESULT_EXPIRED")
	b, err = m.Execute(context.Background(), s, startRequest("new-start", "target"))
	if err != nil || decodeRecord(t, b).ServiceID == id {
		t.Fatalf("new website did not get a fresh identity: %s %v", b, err)
	}
}

func TestManagerDeleteServiceFailureSurvivesRestartAndRetries(t *testing.T) {
	old, s, base, _ := managerFixture(t)
	_ = old.Close()
	runner := &deletionFailureRunner{memoryRunner: base, fail: true}
	o := old.options
	o.Runner, o.PollInterval = runner, time.Hour
	m, err := NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Execute(context.Background(), s, startRequest("start", "site"))
	if err != nil {
		t.Fatal(err)
	}
	id := decodeRecord(t, b).ServiceID
	waitManager(t, m, s, id, "ready")
	requireCode(t, m.DeleteService(context.Background(), s, id), "SERVICE_DELETE_PENDING")
	r, ok := m.current(id)
	if !ok || r.State != "stopping" || r.Phase != "deleting" || r.LastError == nil {
		t.Fatalf("failed deletion lost its visible pending state: %+v", r)
	}
	for _, req := range []Request{
		startRequest("start", "site"), startRequest("new-start", "site"),
		{Action: "restart", ServiceID: id, OperationID: "restart"},
		{Action: "rename", ServiceID: id, Spec: Spec{DisplayName: "new"}, OperationID: "rename"},
		{Action: "open", ServiceID: id, OperationID: "open"},
	} {
		_, err := m.Execute(context.Background(), s, req)
		requireCode(t, err, "SERVICE_DELETE_PENDING")
	}
	_ = m.Close()
	data, err := os.ReadFile(filepath.Join(o.Root, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk diskState
	if err := json.Unmarshal(data, &disk); err != nil || !disk.DeletingServices[id] {
		t.Fatal("deletion intent was not persisted")
	}
	runner.lock.Lock()
	runner.fail = false
	runner.lock.Unlock()
	o.PollInterval = time.Millisecond * 5
	restarted, err := NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	awaitServiceAbsent(t, restarted, id)
	if err := restarted.DeleteService(context.Background(), s, id); err != nil {
		t.Fatal(err)
	}
}

func TestManagerDeleteServiceCancellationKeepsAcceptedCleanup(t *testing.T) {
	old, s, base, _ := managerFixture(t)
	_ = old.Close()
	runner := &delayedStopRunner{memoryRunner: base, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(runner.release) })
	o := old.options
	o.Runner = runner
	m, err := NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release.Do(func() { close(runner.release) }); _ = m.Close() }()
	b, err := m.Execute(context.Background(), s, startRequest("start", "site"))
	if err != nil {
		t.Fatal(err)
	}
	id := decodeRecord(t, b).ServiceID
	waitManager(t, m, s, id, "ready")
	if _, err := m.Execute(context.Background(), s, Request{Action: "open", ServiceID: id, OperationID: "cached-open"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.DeleteService(ctx, s, id) }()
	select {
	case <-runner.entered:
	case <-time.After(time.Second):
		t.Fatal("delete did not start")
	}
	cancel()
	select {
	case err := <-done:
		requireCode(t, err, "SERVICE_DELETE_PENDING")
	case <-time.After(time.Second):
		t.Fatal("cancelled request kept waiting on the service lock")
	}
	_, err = m.Execute(context.Background(), s, Request{Action: "open", ServiceID: id, OperationID: "cached-open"})
	requireCode(t, err, "SERVICE_DELETE_PENDING")
	release.Do(func() { close(runner.release) })
	awaitServiceAbsent(t, m, id)
}

type delayedDeleteStartRunner struct {
	*memoryRunner
	entered chan struct{}
	release chan struct{}
}

func (r *delayedDeleteStartRunner) Start(ctx context.Context, launch Launch) (Instance, error) {
	close(r.entered)
	// Simulate a process adapter returning a late instance despite cancellation.
	<-r.release
	return r.memoryRunner.Start(context.WithoutCancel(ctx), launch)
}

func TestManagerDeleteServiceFencesLateStart(t *testing.T) {
	old, s, base, _ := managerFixture(t)
	_ = old.Close()
	runner := &delayedDeleteStartRunner{memoryRunner: base, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(runner.release) })
	o := old.options
	o.Runner = runner
	m, err := NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release.Do(func() { close(runner.release) }); _ = m.Close() }()
	b, err := m.Execute(context.Background(), s, startRequest("start", "site"))
	if err != nil {
		t.Fatal(err)
	}
	id := decodeRecord(t, b).ServiceID
	select {
	case <-runner.entered:
	case <-time.After(time.Second):
		t.Fatal("launch did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*20)
	defer cancel()
	requireCode(t, m.DeleteService(ctx, s, id), "SERVICE_DELETE_PENDING")
	release.Do(func() { close(runner.release) })
	awaitServiceAbsent(t, m, id)
	base.mu.Lock()
	remaining := len(base.instances)
	base.mu.Unlock()
	if remaining != 0 {
		t.Fatal("late launch escaped deletion")
	}
}

func TestManagerDeleteServiceSupersedesPendingRestart(t *testing.T) {
	old, s, base, _ := managerFixture(t)
	_ = old.Close()
	runner := &delayedStopRunner{memoryRunner: base, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(runner.release) })
	o := old.options
	o.Runner = runner
	m, err := NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release.Do(func() { close(runner.release) }); _ = m.Close() }()
	b, err := m.Execute(context.Background(), s, startRequest("start", "site"))
	if err != nil {
		t.Fatal(err)
	}
	id := decodeRecord(t, b).ServiceID
	waitManager(t, m, s, id, "ready")
	if _, err := m.Execute(context.Background(), s, Request{Action: "restart", ServiceID: id, OperationID: "restart"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.entered:
	case <-time.After(time.Second):
		t.Fatal("restart did not enter shutdown")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	requireCode(t, m.DeleteService(ctx, s, id), "SERVICE_DELETE_PENDING")
	release.Do(func() { close(runner.release) })
	awaitServiceAbsent(t, m, id)
	base.mu.Lock()
	starts := base.starts
	base.mu.Unlock()
	if starts != 1 {
		t.Fatal("superseded restart launched another runtime")
	}
}

func TestManagerDeleteServicePublishesDeletionOnlyAfterConfirmedCleanup(t *testing.T) {
	old, s, base, _ := managerFixture(t)
	_ = old.Close()
	runner := &deletionFailureRunner{memoryRunner: base, fail: true}
	o := old.options
	o.Runner, o.PollInterval = runner, time.Hour
	deleted := make(chan Record, 4)
	var m *Manager
	o.OnChange = func(r Record) {
		if r.Phase == "deleted" {
			if _, exists := m.current(r.ServiceID); exists {
				t.Error("published deletion before committing record removal")
			}
			deleted <- r
		}
	}
	var err error
	m, err = NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	b, err := m.Execute(context.Background(), s, startRequest("start", "site"))
	if err != nil {
		t.Fatal(err)
	}
	id := decodeRecord(t, b).ServiceID
	waitManager(t, m, s, id, "ready")
	requireCode(t, m.DeleteService(context.Background(), s, id), "SERVICE_DELETE_PENDING")
	pending, _ := m.current(id)
	select {
	case <-deleted:
		t.Fatal("cleanup failure falsely published deletion")
	default:
	}
	runner.lock.Lock()
	runner.fail = false
	runner.lock.Unlock()
	if err := m.DeleteService(context.Background(), s, id); err != nil {
		t.Fatal(err)
	}
	select {
	case final := <-deleted:
		if final.ServiceID != id || final.State != "stopped" || final.Revision <= pending.Revision {
			t.Fatalf("invalid final deletion receipt: %+v", final)
		}
	case <-time.After(time.Second):
		t.Fatal("confirmed deletion did not notify other clients")
	}
}
