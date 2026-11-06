package webservice

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

type delayedStopRunner struct {
	*memoryRunner
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (r *delayedStopRunner) Stop(ctx context.Context, rec Record) error {
	wait := false
	r.once.Do(func() { wait = true; close(r.entered) })
	if wait {
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.memoryRunner.Stop(ctx, rec)
}

func TestManagerLaterStopSupersedesPendingRestart(t *testing.T) {
	old, s, base, gateway := managerFixture(t)
	_ = old.Close()
	runner := &delayedStopRunner{memoryRunner: base, entered: make(chan struct{}), release: make(chan struct{})}
	o := old.options
	o.Runner = runner
	m, err := NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	_ = gateway
	b, err := m.Execute(context.Background(), s, startRequest("start", "site"))
	if err != nil {
		t.Fatal(err)
	}
	id := decodeRecord(t, b).ServiceID
	waitManager(t, m, s, id, "ready")
	_, err = m.Execute(context.Background(), s, Request{Action: "restart", ServiceID: id, OperationID: "restart"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.entered:
	case <-time.After(time.Second):
		t.Fatal("restart never entered stop")
	}
	_, err = m.Execute(context.Background(), s, Request{Action: "stop", ServiceID: id, OperationID: "last-stop"})
	if err != nil {
		t.Fatal(err)
	}
	close(runner.release)
	r := waitManager(t, m, s, id, "stopped")
	if r.Generation != 1 {
		t.Fatalf("older restart created generation %d after stop", r.Generation)
	}
	base.mu.Lock()
	starts := base.starts
	live := len(base.instances)
	base.mu.Unlock()
	if starts != 1 || live != 0 {
		t.Fatalf("starts=%d live=%d", starts, live)
	}
}

type deletionFailureRunner struct {
	*memoryRunner
	lock sync.Mutex
	fail bool
}

func (r *deletionFailureRunner) DeleteData(ctx context.Context, rec Record) error {
	r.lock.Lock()
	fail := r.fail
	r.lock.Unlock()
	if fail {
		return errors.New("storage unavailable")
	}
	return r.memoryRunner.DeleteData(ctx, rec)
}
func TestManagerRetriesDeletionOfAlreadyStoppedService(t *testing.T) {
	old, s, base, _ := managerFixture(t)
	_ = old.Close()
	runner := &deletionFailureRunner{memoryRunner: base, fail: true}
	o := old.options
	o.Runner = runner
	m, err := NewManager(o)
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
	_, err = m.Execute(context.Background(), s, Request{Action: "stop", ServiceID: id, OperationID: "stop"})
	if err != nil {
		t.Fatal(err)
	}
	waitManager(t, m, s, id, "stopped")
	if err = m.DeleteSession(context.Background(), s.Owner, s.Session); err == nil {
		t.Fatal("deletion failure hidden")
	}
	runner.lock.Lock()
	runner.fail = false
	runner.lock.Unlock()
	end := time.Now().Add(time.Second)
	for time.Now().Before(end) {
		if _, ok := m.current(id); !ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("terminal record prevented tombstone retry")
}

type delayedOpenGateway struct {
	*memoryGateway
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	count   int
}

func (g *delayedOpenGateway) Open(ctx context.Context, r Record) (Grant, error) {
	g.mu.Lock()
	g.count++
	g.mu.Unlock()
	g.once.Do(func() { close(g.entered) })
	select {
	case <-g.release:
	case <-ctx.Done():
		return Grant{}, ctx.Err()
	}
	return g.memoryGateway.Open(ctx, r)
}
func TestManagerConcurrentOpenReportsPendingWithoutResigning(t *testing.T) {
	old, s, _, base := managerFixture(t)
	_ = old.Close()
	g := &delayedOpenGateway{memoryGateway: base, entered: make(chan struct{}), release: make(chan struct{})}
	o := old.options
	o.Gateway = g
	m, err := NewManager(o)
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
	q := Request{Action: "open", ServiceID: id, OperationID: "open"}
	finished := make(chan error, 1)
	go func() { _, err := m.Execute(context.Background(), s, q); finished <- err }()
	<-g.entered
	b, err = m.Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	var pending map[string]any
	_ = json.Unmarshal(b, &pending)
	if pending["operation_status"] != "accepted" {
		t.Fatalf("not pending: %s", b)
	}
	close(g.release)
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	if _, err = m.Execute(context.Background(), s, q); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	calls := g.count
	g.mu.Unlock()
	if calls != 1 {
		t.Fatalf("open calls=%d", calls)
	}
}

type renewalFailureRunner struct{ *memoryRunner }

func (r *renewalFailureRunner) Renew(context.Context, Record) error {
	return Fail("LEASE_UPDATE_FAILED", "lease was not confirmed")
}
func TestManagerAsynchronousOperationFailureRemainsVisible(t *testing.T) {
	old, s, base, _ := managerFixture(t)
	_ = old.Close()
	o := old.options
	o.Runner = &renewalFailureRunner{base}
	m, err := NewManager(o)
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
	q := Request{Action: "renew", ServiceID: id, OperationID: "renew", Spec: Spec{LeaseSeconds: 30}}
	if _, err = m.Execute(context.Background(), s, q); err != nil {
		t.Fatal(err)
	}
	end := time.Now().Add(time.Second)
	for time.Now().Before(end) {
		b, err = m.Execute(context.Background(), s, q)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		_ = json.Unmarshal(b, &result)
		if result["operation_status"] == "failed" {
			e, ok := result["operation_error"].(map[string]any)
			if !ok || e["code"] != "LEASE_UPDATE_FAILED" {
				t.Fatalf("missing operation cause: %s", b)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("accepted renewal failure remained invisible")
}

type unavailableGateway struct {
	*memoryGateway
	failService string
}

func (g *unavailableGateway) Apply(ctx context.Context, r Record) error {
	g.mu.Lock()
	fail := r.ServiceID == g.failService
	g.mu.Unlock()
	if fail {
		return Fail("CONTROL_SUPERSEDED", "gateway restarted")
	}
	return g.memoryGateway.Apply(ctx, r)
}

func TestManagerGatewayResetInvalidatesAllServiceGrantReceipts(t *testing.T) {
	old, s, _, base := managerFixture(t)
	_ = old.Close()
	g := &unavailableGateway{memoryGateway: base}
	o := old.options
	o.Gateway = g
	o.PollInterval, o.HealthInterval = time.Hour, time.Hour
	m, err := NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	var ids []string
	var opens []Request
	for _, name := range []string{"first", "second"} {
		b, err := m.Execute(context.Background(), s, startRequest("start-"+name, name))
		if err != nil {
			t.Fatal(err)
		}
		id := decodeRecord(t, b).ServiceID
		waitManager(t, m, s, id, "ready")
		q := Request{Action: "open", ServiceID: id, OperationID: "open-" + name}
		if _, err = m.Execute(context.Background(), s, q); err != nil {
			t.Fatal(err)
		}
		ids, opens = append(ids, id), append(opens, q)
	}
	g.mu.Lock()
	g.failService = ids[0]
	g.mu.Unlock()
	r, _ := m.current(ids[0])
	m.check(r)
	for _, q := range opens {
		_, err := m.Execute(context.Background(), s, q)
		requireCode(t, err, "GRANT_EXPIRED")
	}
}

func TestManagerGatewayResetCannotCacheAnInFlightOpen(t *testing.T) {
	old, s, _, base := managerFixture(t)
	_ = old.Close()
	g := &delayedOpenGateway{memoryGateway: base, entered: make(chan struct{}), release: make(chan struct{})}
	o := old.options
	o.Gateway = g
	m, err := NewManager(o)
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
	q := Request{Action: "open", ServiceID: id, OperationID: "open"}
	finished := make(chan error, 1)
	go func() { _, err := m.Execute(context.Background(), s, q); finished <- err }()
	<-g.entered
	m.invalidateGrantCache()
	close(g.release)
	requireCode(t, <-finished, "GRANT_EXPIRED")
	_, err = m.Execute(context.Background(), s, q)
	requireCode(t, err, "GRANT_EXPIRED")
}

type invalidOriginGateway struct{ *memoryGateway }

func (g *invalidOriginGateway) Open(ctx context.Context, r Record) (Grant, error) {
	grant, err := g.memoryGateway.Open(ctx, r)
	grant.PreviewURL = "http://localhost/"
	return grant, err
}

func TestManagerInvalidPreviewOriginCompletesFailedOpen(t *testing.T) {
	old, s, _, base := managerFixture(t)
	_ = old.Close()
	o := old.options
	o.Gateway = &invalidOriginGateway{base}
	m, err := NewManager(o)
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
	q := Request{Action: "open", ServiceID: id, OperationID: "open"}
	_, err = m.Execute(context.Background(), s, q)
	requireCode(t, err, "GATEWAY_UNAVAILABLE")
	_, err = m.Execute(context.Background(), s, q)
	requireCode(t, err, "GRANT_EXPIRED")
}

func TestManagerRevokedGrantCannotReplayItsOldOpenReceipt(t *testing.T) {
	m, s, _, _ := managerFixture(t)
	ctx := context.Background()
	var ids []string
	var opens []Request
	for _, name := range []string{"first", "second"} {
		b, err := m.Execute(ctx, s, startRequest("start-"+name, name))
		if err != nil {
			t.Fatal(err)
		}
		id := decodeRecord(t, b).ServiceID
		waitManager(t, m, s, id, "ready")
		q := Request{Action: "open", ServiceID: id, OperationID: "open-" + name}
		if _, err = m.Execute(ctx, s, q); err != nil {
			t.Fatal(err)
		}
		ids, opens = append(ids, id), append(opens, q)
	}
	if _, err := m.Execute(ctx, s, Request{Action: "revoke", ServiceID: ids[0], GrantID: "grant", OperationID: "revoke-first"}); err != nil {
		t.Fatal(err)
	}
	_, err := m.Execute(ctx, s, opens[0])
	requireCode(t, err, "GRANT_EXPIRED")
	if _, err = m.Execute(ctx, s, opens[1]); err != nil {
		t.Fatalf("another service's grant was removed: %v", err)
	}
}
