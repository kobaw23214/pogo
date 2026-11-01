package taskqueue

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func request(task string) Request {
	return Request{TaskID: task, Source: "sentinel", SourceID: "source:" + task, Trigger: "event", TriggerID: "event:1",
		DefinitionHash: "v1:definition", OwnerID: "workspace:1", AgentID: "agent:1", Title: "后台任务", Payload: json.RawMessage(" {\n  \"records\": [1, 2] \n} ")}
}

func admit(t *testing.T, s *Store, req Request) Run {
	t.Helper()
	r, created, err := s.Enqueue(req)
	if err != nil || !created {
		t.Fatalf("admit: created=%v, err=%v", created, err)
	}
	return r
}

func claimed(t *testing.T, s *Store, req Request) Run {
	t.Helper()
	r := admit(t, s, req)
	c, err := s.Claim(r.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func getRun(t *testing.T, s *Store, id string) Run {
	t.Helper()
	r, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func expectError(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("want errors.Is(%v), got %v", target, err)
	}
}

func TestDiskDurabilityAndOpaquePayload(t *testing.T) {
	s, path := testStore(t)
	var journal string
	var synchronous int
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" || synchronous != 2 {
		t.Fatalf("not durable: %s %d", journal, synchronous)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("database permissions: %v %v", info, err)
	}
	req := request("durable")
	req.AgentID = "" // Legacy administrator CLI prompt jobs remain supported.
	expected := bytes.Clone(req.Payload)
	r := admit(t, s, req)
	req.Payload[0] = '\t'
	r.Payload[0] = '\n'
	if !bytes.Equal(getRun(t, s, r.ID).Payload, expected) {
		t.Fatal("returned or caller-owned payload changed the stored envelope")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	r = getRun(t, reopened, r.ID)
	if r.Status != Queued || r.Revision != 1 || r.Attempt != 1 || r.AgentID != "" || r.WorkflowID != "" || !bytes.Equal(r.Payload, expected) {
		t.Fatalf("reopen changed admission: %+v", r)
	}
	copyReq := request("durable")
	copyReq.AgentID = ""
	again, created, err := reopened.Enqueue(copyReq)
	if err != nil || created || again.ID != r.ID {
		t.Fatalf("reopen idempotency: %v %v %+v", created, err, again)
	}
}

func TestRejectMemoryAndNonregularPaths(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", " ", ":memory:", "file::memory:", "FILE:/tmp/q?mode=memory", dir, link, "nul\x00db"} {
		t.Run(fmt.Sprintf("%q", path), func(t *testing.T) {
			s, err := Open(path)
			if err == nil {
				s.Close()
				t.Fatal("invalid persistent target accepted")
			}
			expectError(t, err, ErrConflict)
		})
	}
	// Query punctuation is a literal file name, never an injected SQLite URI.
	literal := filepath.Join(dir, "q?mode=memory#other.sqlite")
	s, err := Open(literal)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(literal); err != nil {
		t.Fatal("literal path was interpreted as URI options", err)
	}
}

func TestAdmissionValidationNeverEchoesPayload(t *testing.T) {
	s, _ := testStore(t)
	secret := "SHOULD_NOT_APPEAR_IN_ERRORS"
	cases := map[string]func(*Request){
		"empty task":             func(r *Request) { r.TaskID = " " },
		"empty source":           func(r *Request) { r.Source = "" },
		"empty source identity":  func(r *Request) { r.SourceID = "" },
		"empty trigger":          func(r *Request) { r.Trigger = "" },
		"empty trigger identity": func(r *Request) { r.TriggerID = "" },
		"empty definition":       func(r *Request) { r.DefinitionHash = "" },
		"empty owner":            func(r *Request) { r.OwnerID = "" },
		"empty title":            func(r *Request) { r.Title = "" },
		"metadata bound":         func(r *Request) { r.Title = strings.Repeat("x", 1025) },
		"invalid UTF8 metadata":  func(r *Request) { r.OwnerID = string([]byte{0xff}) },
		"NUL metadata":           func(r *Request) { r.Source = "source\x00bad" },
		"past epoch":             func(r *Request) { r.DueAt = -1 },
		"invalid JSON":           func(r *Request) { r.Payload = []byte("{\"secret\":\"" + secret + "\"") },
		"NaN":                    func(r *Request) { r.Payload = []byte(`{"value":NaN}`) },
		"invalid UTF8 payload":   func(r *Request) { r.Payload = []byte{'"', 0xff, '"'} },
		"oversize payload":       func(r *Request) { r.Payload = []byte(`"` + strings.Repeat("x", MaxPayloadBytes-1) + `"`) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := request(name)
			r.Payload = []byte(`{"secret":"` + secret + `"}`)
			mutate(&r)
			_, _, err := s.Enqueue(r)
			expectError(t, err, ErrConflict)
			if strings.Contains(err.Error(), secret) {
				t.Fatal("validation echoed opaque payload")
			}
		})
	}
	for i, payload := range []json.RawMessage{nil, []byte("null"), []byte(`"` + strings.Repeat("x", MaxPayloadBytes-2) + `"`)} {
		r := request(fmt.Sprintf("boundary%d", i))
		r.Payload = payload
		admit(t, s, r)
	}
	runs, err := s.List(Filter{})
	if err != nil || len(runs) != 3 {
		t.Fatalf("invalid admissions persisted: %d %v", len(runs), err)
	}
}

func TestEnqueueImmutableIdempotency(t *testing.T) {
	s, _ := testStore(t)
	req := request("immutable")
	r := admit(t, s, req)
	changes := map[string]func(*Request){
		"source":                    func(r *Request) { r.Source = "schedule" },
		"source id":                 func(r *Request) { r.SourceID += ":other" },
		"trigger":                   func(r *Request) { r.Trigger = "manual" },
		"hash":                      func(r *Request) { r.DefinitionHash += "x" },
		"owner":                     func(r *Request) { r.OwnerID = "workspace:2" },
		"agent":                     func(r *Request) { r.AgentID = "agent:2" },
		"workflow":                  func(r *Request) { r.WorkflowID = "workflow:2" },
		"title":                     func(r *Request) { r.Title += "2" },
		"retry":                     func(r *Request) { r.RetryOf = "other" },
		"due":                       func(r *Request) { r.DueAt = 1 },
		"opaque payload whitespace": func(r *Request) { r.Payload = append(bytes.Clone(r.Payload), ' ') },
	}
	for name, mutate := range changes {
		t.Run(name, func(t *testing.T) {
			copyReq := req
			mutate(&copyReq)
			_, created, err := s.Enqueue(copyReq)
			expectError(t, err, ErrConflict)
			if created || !reflect.DeepEqual(getRun(t, s, r.ID), r) {
				t.Fatal("duplicate trigger overwrote immutable run")
			}
		})
	}
	c, err := s.Claim(r.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.Finish(c.ID, c.Revision, Outcome{Status: Completed, Summary: "actually finished"})
	if err != nil {
		t.Fatal(err)
	}
	again, created, err := s.Enqueue(req)
	if err != nil || created || !reflect.DeepEqual(again, done) {
		t.Fatalf("duplicate resurrected terminal run: created=%v err=%v run=%+v", created, err, again)
	}
}

func TestConcurrentDuplicateEnqueueAcrossConnections(t *testing.T) {
	s, path := testStore(t)
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	stores := []*Store{s, other}
	var wg sync.WaitGroup
	var createdCount atomic.Int32
	ids := make(chan string, 24)
	errs := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, created, err := stores[i%2].Enqueue(request("shared"))
			if created {
				createdCount.Add(1)
			}
			if err != nil {
				errs <- err
			} else {
				ids <- r.ID
			}
		}(i)
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("duplicate enqueue minted multiple run identities")
		}
	}
	if createdCount.Load() != 1 {
		t.Fatalf("created %d admissions", createdCount.Load())
	}
}

func TestConcurrentClaimCapacityAndPerTaskIsolation(t *testing.T) {
	s, path := testStore(t)
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	stores := []*Store{s, other}
	runs := make([]Run, 12)
	for i := range runs {
		runs[i] = admit(t, s, request(fmt.Sprintf("task:%d", i)))
	}
	var wg sync.WaitGroup
	claimedRuns := make(chan Run, len(runs))
	errs := make(chan error, len(runs))
	for i, run := range runs {
		wg.Add(1)
		go func(i int, run Run) {
			defer wg.Done()
			claimed, err := stores[i%2].Claim(run.ID, 3)
			if err != nil {
				if !errors.Is(err, ErrBusy) {
					errs <- err
				}
				return
			}
			claimedRuns <- claimed
		}(i, run)
	}
	wg.Wait()
	close(claimedRuns)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	claims := []Run{}
	for r := range claimedRuns {
		claims = append(claims, r)
	}
	if len(claims) != 3 {
		t.Fatalf("global limit admitted %d", len(claims))
	}
	queuedCount := 0
	for _, r := range runs {
		current := getRun(t, other, r.ID)
		if current.Status == Queued {
			queuedCount++
		} else if current.Status != Running || current.Revision != 2 || current.StartedAt < current.CreatedAt {
			t.Fatalf("claim not durably recorded before execution: %+v", current)
		}
	}
	if queuedCount != 9 {
		t.Fatalf("capacity failure lost queued work: %d", queuedCount)
	}
	if _, err := s.Finish(claims[0].ID, claims[0].Revision, Outcome{Status: Completed}); err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if getRun(t, s, run.ID).Status == Queued {
			if _, err := other.Claim(run.ID, 3); err != nil {
				t.Fatal("freed capacity not reusable", err)
			}
			break
		}
	}
	// A single definition cannot overlap, even when the global limit is free.
	first := request("one-task")
	second := first
	second.TriggerID = "event:2"
	a := admit(t, s, first)
	b := admit(t, s, second)
	if _, err := s.Claim(a.ID, 100); err != nil {
		t.Fatal(err)
	}
	_, err = other.Claim(b.ID, 100)
	expectError(t, err, ErrBusy)
	if getRun(t, s, b.ID).Status != Queued {
		t.Fatal("per-task capacity lost work")
	}
	future := request("future")
	future.DueAt = time.Now().Add(time.Hour).UnixMilli()
	f := admit(t, s, future)
	_, err = s.Claim(f.ID, 100)
	expectError(t, err, ErrBusy)
	if getRun(t, s, f.ID).Revision != f.Revision {
		t.Fatal("future task was claimed early")
	}
}

func TestRestartRecoveryIsExplicitAndDoesNotRetry(t *testing.T) {
	s, path := testStore(t)
	running := claimed(t, s, request("running"))
	queued := admit(t, s, request("queued"))
	bound, err := s.AttachSession(running.ID, running.Revision, "conversation:recover")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := getRun(t, reopened, running.ID); !reflect.DeepEqual(got, bound) {
		t.Fatal("Open mutated running work before controller lease")
	}
	if err := reopened.Recover(); err != nil {
		t.Fatal(err)
	}
	interrupted := getRun(t, reopened, running.ID)
	if interrupted.Status != Interrupted || interrupted.Revision != running.Revision+1 || interrupted.EndReason != "controller_restart" || interrupted.SessionID != bound.SessionID {
		t.Fatalf("active recovery: %+v", interrupted)
	}
	if got := getRun(t, reopened, queued.ID); !reflect.DeepEqual(got, queued) {
		t.Fatal("recovery changed queued work")
	}
	_, err = reopened.Finish(running.ID, running.Revision, Outcome{Status: Completed, SessionID: bound.SessionID})
	expectError(t, err, ErrConflict)
	if err := reopened.Recover(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(getRun(t, reopened, running.ID), interrupted) {
		t.Fatal("repeated recovery mutated terminal work")
	}
	runs, err := reopened.List(Filter{})
	if err != nil || len(runs) != 2 {
		t.Fatalf("recovery blindly created/retried work: %d %v", len(runs), err)
	}
}

func TestRevisionReceiptsAndSessionBinding(t *testing.T) {
	s, _ := testStore(t)
	q := admit(t, s, request("receipts"))
	_, err := s.AttachSession(q.ID, q.Revision, "too-early")
	expectError(t, err, ErrConflict)
	_, err = s.Finish(q.ID, q.Revision, Outcome{Status: Completed})
	expectError(t, err, ErrConflict)
	c, err := s.Claim(q.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.AttachSession(c.ID, q.Revision, "wrong-generation")
	expectError(t, err, ErrConflict)
	_, err = s.Finish(c.ID, q.Revision, Outcome{Status: Completed})
	expectError(t, err, ErrConflict)
	_, err = s.Cancel(c.ID, q.Revision)
	expectError(t, err, ErrConflict)
	for _, session := range []string{"", strings.Repeat("s", 257), "s\x00bad", string([]byte{0xff})} {
		_, err := s.AttachSession(c.ID, c.Revision, session)
		expectError(t, err, ErrConflict)
	}
	bound, err := s.AttachSession(c.ID, c.Revision, "session:1")
	if err != nil || bound.SessionID != "session:1" || bound.Revision != c.Revision {
		t.Fatalf("bind: %+v %v", bound, err)
	}
	again, err := s.AttachSession(c.ID, c.Revision, "session:1")
	if err != nil || !reflect.DeepEqual(again, bound) {
		t.Fatal("same binding not idempotent", err)
	}
	_, err = s.AttachSession(c.ID, c.Revision, "session:other")
	expectError(t, err, ErrConflict)
	_, err = s.Finish(c.ID, c.Revision, Outcome{Status: Completed, SessionID: "session:other"})
	expectError(t, err, ErrConflict)
	out := Outcome{Status: Completed, Summary: "actual completion", EndReason: "done"}
	done, err := s.Finish(c.ID, c.Revision, out)
	if err != nil || done.SessionID != bound.SessionID || done.Revision != c.Revision+1 || done.FinishedAt < done.StartedAt {
		t.Fatalf("terminal receipt: %+v %v", done, err)
	}
	again, err = s.Finish(c.ID, c.Revision, out)
	if err != nil || !reflect.DeepEqual(again, done) {
		t.Fatal("identical receipt not idempotent", err)
	}
	out.Summary = "changed receipt"
	_, err = s.Finish(c.ID, c.Revision, out)
	expectError(t, err, ErrConflict)
	_, err = s.Finish(c.ID, done.Revision, Outcome{Status: Completed})
	expectError(t, err, ErrConflict)
	_, err = s.AttachSession(c.ID, c.Revision, "session:1")
	expectError(t, err, ErrConflict)
}

func TestCancellationIntentIsNotStoppedReceipt(t *testing.T) {
	s, _ := testStore(t)
	q := admit(t, s, request("cancel-before-start"))
	cancelled, err := s.Cancel(q.ID, q.Revision)
	if err != nil || cancelled.Status != Cancelled || !cancelled.CancelRequested || cancelled.StartedAt != 0 || cancelled.FinishedAt < q.CreatedAt {
		t.Fatalf("queued cancellation: %+v %v", cancelled, err)
	}
	_, err = s.Claim(q.ID, 1)
	expectError(t, err, ErrConflict)
	c := claimed(t, s, request("real-worker"))
	pending, err := s.Cancel(c.ID, c.Revision)
	if err != nil || pending.Status != Running || !pending.CancelRequested || pending.Revision != c.Revision || pending.FinishedAt != 0 {
		t.Fatalf("cancellation fabricated stop: %+v %v", pending, err)
	}
	again, err := s.Cancel(c.ID, c.Revision)
	if err != nil || !reflect.DeepEqual(again, pending) {
		t.Fatal("running cancellation intent not idempotent", err)
	}
	next := admit(t, s, request("next"))
	_, err = s.Claim(next.ID, 1)
	expectError(t, err, ErrBusy)
	done, err := s.Finish(c.ID, c.Revision, Outcome{Status: Completed, Summary: "side effect already completed"})
	if err != nil || done.Status != Completed || !done.CancelRequested {
		t.Fatal("cancellation intent overwrote actual completed receipt", err)
	}
	_, err = s.Cancel(c.ID, done.Revision)
	expectError(t, err, ErrConflict)
	next, err = s.Claim(next.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cancel(next.ID, next.Revision); err != nil {
		t.Fatal(err)
	}
	stopped, err := s.Finish(next.ID, next.Revision, Outcome{Status: Cancelled, EndReason: "worker_confirmed_stop"})
	if err != nil || stopped.Status != Cancelled {
		t.Fatal("real stop receipt rejected", err)
	}
}

func TestExplicitRetryPreservesHistoryAndIsConcurrentIdempotent(t *testing.T) {
	s, path := testStore(t)
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	c := claimed(t, s, request("retry"))
	failed, err := s.Finish(c.ID, c.Revision, Outcome{Status: Failed, SessionID: "old-session", Summary: "partial side effect", Error: "source unavailable", EndReason: "worker_error"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Retry(failed.ID, c.Revision)
	expectError(t, err, ErrConflict)
	var wg sync.WaitGroup
	children := make(chan Run, 16)
	errs := make(chan error, 16)
	before := time.Now().UnixMilli()
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := s
			if i%2 == 1 {
				store = other
			}
			child, err := store.Retry(failed.ID, failed.Revision)
			if err != nil {
				errs <- err
			} else {
				children <- child
			}
		}(i)
	}
	wg.Wait()
	close(children)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var child Run
	for current := range children {
		if child.ID == "" {
			child = current
		} else if !reflect.DeepEqual(child, current) {
			t.Fatal("concurrent retry minted different children")
		}
	}
	if child.ID == failed.ID || child.RetryOf != failed.ID || child.Trigger != "retry" || child.TriggerID != fmt.Sprintf("retry:%s:%d", failed.ID, failed.Revision) || child.Attempt != failed.Attempt+1 || child.DueAt < before || child.DueAt > time.Now().UnixMilli() || child.Status != Queued || child.SessionID != "" {
		t.Fatalf("retry child: %+v", child)
	}
	if !reflect.DeepEqual(getRun(t, s, failed.ID), failed) || !bytes.Equal(child.Payload, failed.Payload) {
		t.Fatal("retry modified historic receipt or source payload")
	}
	child, err = s.Claim(child.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	child, err = s.Finish(child.ID, child.Revision, Outcome{Status: Failed, Error: "second failure"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Retry(failed.ID, failed.Revision)
	if err != nil || !reflect.DeepEqual(again, child) {
		t.Fatal("retrying original revived its terminal child", err)
	}
	grandchild, err := s.Retry(child.ID, child.Revision)
	if err != nil || grandchild.Attempt != 3 || grandchild.RetryOf != child.ID || grandchild.ID == child.ID {
		t.Fatalf("retry chain: %+v %v", grandchild, err)
	}
	_, err = s.Retry(grandchild.ID, grandchild.Revision)
	expectError(t, err, ErrConflict)
	all, err := s.List(Filter{TaskID: failed.TaskID})
	if err != nil || len(all) != 3 {
		t.Fatalf("retry history: %d %v", len(all), err)
	}
}

func TestRetryOnlyFailedOrInterruptedAndDoesNotOverwriteCollision(t *testing.T) {
	s, _ := testStore(t)
	for _, status := range []string{Queued, Running, Completed, Cancelled, Failed, Interrupted} {
		t.Run(status, func(t *testing.T) {
			r := admit(t, s, request("retry-status:"+status))
			if status != Queued {
				var err error
				r, err = s.Claim(r.ID, 100)
				if err != nil {
					t.Fatal(err)
				}
				if status != Running {
					r, err = s.Finish(r.ID, r.Revision, Outcome{Status: status})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			_, err := s.Retry(r.ID, r.Revision)
			if status == Failed || status == Interrupted {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				expectError(t, err, ErrConflict)
			}
		})
	}
	c := claimed(t, s, request("collision"))
	f, err := s.Finish(c.ID, c.Revision, Outcome{Status: Failed})
	if err != nil {
		t.Fatal(err)
	}
	fake := f.Request
	fake.TriggerID = fmt.Sprintf("retry:%s:%d", f.ID, f.Revision)
	fake.Trigger = "retry"
	fake.Title = "conflicting immutable content"
	bogus := admit(t, s, fake)
	_, err = s.Retry(f.ID, f.Revision)
	expectError(t, err, ErrConflict)
	if !reflect.DeepEqual(getRun(t, s, bogus.ID), bogus) {
		t.Fatal("retry overwrote conflicting existing trigger")
	}
}

func TestScopedListingAndMissingIdentities(t *testing.T) {
	s, _ := testStore(t)
	requests := []Request{request("shared:one"), request("shared:two"), request("other-agent"), request("other-owner")}
	requests[0].DueAt = 9
	requests[1].DueAt = 3
	requests[2].AgentID = "agent:2"
	requests[3].OwnerID = "workspace:2"
	for _, req := range requests {
		admit(t, s, req)
	}
	runs, err := s.List(Filter{OwnerID: "workspace:1", AgentID: "agent:1"})
	if err != nil || len(runs) != 2 || runs[0].DueAt != 3 || runs[1].DueAt != 9 {
		t.Fatalf("scope/order: %+v %v", runs, err)
	}
	c, err := s.Claim(runs[0].ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finish(c.ID, c.Revision, Outcome{Status: Completed}); err != nil {
		t.Fatal(err)
	}
	active, err := s.List(Filter{OwnerID: "workspace:1", AgentID: "agent:1", ActiveOnly: true})
	if err != nil || len(active) != 1 || active[0].TaskID != requests[0].TaskID {
		t.Fatalf("active scope: %+v %v", active, err)
	}
	limited, err := s.List(Filter{Limit: 2})
	if err != nil || len(limited) != 2 {
		t.Fatal("trusted listing limit", err)
	}
	none, err := s.List(Filter{TaskID: "not-present"})
	if err != nil || none == nil || len(none) != 0 {
		t.Fatal("missing filter should return empty list", err)
	}
	_, err = s.List(Filter{Limit: -1})
	expectError(t, err, ErrConflict)
	for _, op := range []func() error{
		func() error { _, err := s.Get("absent"); return err },
		func() error { _, err := s.Claim("absent", 1); return err },
		func() error { _, err := s.Cancel("absent", 1); return err },
		func() error { _, err := s.AttachSession("absent", 1, "session"); return err },
		func() error { _, err := s.Finish("absent", 1, Outcome{Status: Completed}); return err },
		func() error { _, err := s.Retry("absent", 1); return err },
	} {
		expectError(t, op(), ErrNotFound)
	}
}

func TestTerminalOutcomeValidationDoesNotAdvance(t *testing.T) {
	s, _ := testStore(t)
	c := claimed(t, s, request("bad-outcome"))
	for _, out := range []Outcome{
		{Status: Queued}, {Status: Running}, {Status: ""},
		{Status: Failed, SessionID: strings.Repeat("x", 257)},
		{Status: Completed, Summary: strings.Repeat("x", 64<<10+1)},
		{Status: Failed, Error: strings.Repeat("x", 16<<10+1)},
		{Status: Failed, EndReason: strings.Repeat("x", 257)},
		{Status: Failed, Error: string([]byte{0xff})},
	} {
		_, err := s.Finish(c.ID, c.Revision, out)
		expectError(t, err, ErrConflict)
		if !reflect.DeepEqual(getRun(t, s, c.ID), c) {
			t.Fatal("invalid outcome changed active run")
		}
	}
}

func TestReleaseOnlyKnownUnstartedClaimAndKeepsImmutableRequest(t *testing.T) {
	s, path := testStore(t)
	req := request("defer-known-not-started")
	c := claimed(t, s, req)
	if _, err := s.AttachSession(c.ID, c.Revision, "busy-session"); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour).UnixMilli()
	released, err := s.Release(c.ID, c.Revision, future)
	if err != nil || released.Status != Queued || released.Revision != c.Revision+1 || released.StartedAt != 0 || released.SessionID != "" || released.Attempt != c.Attempt || !reflect.DeepEqual(released.Request, req) {
		t.Fatalf("release: %+v %v", released, err)
	}
	_, err = s.Finish(c.ID, c.Revision, Outcome{Status: Completed})
	expectError(t, err, ErrConflict)
	_, err = s.AttachSession(c.ID, c.Revision, "late-worker")
	expectError(t, err, ErrConflict)
	_, err = s.Release(c.ID, c.Revision, 0)
	expectError(t, err, ErrConflict)
	_, err = s.Claim(c.ID, 1)
	expectError(t, err, ErrBusy)
	// Restart must retain the scheduling gate separately from Request.DueAt=0.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, err = reopened.Claim(c.ID, 1)
	expectError(t, err, ErrBusy)
	again, created, err := reopened.Enqueue(req)
	if err != nil || created || !reflect.DeepEqual(again, released) {
		t.Fatal("release broke immutable admission idempotency", err)
	}
	// Another known unstarted claim released immediately can bind anew.
	next := claimed(t, reopened, request("immediate-release"))
	if _, err := reopened.Release(next.ID, next.Revision, 0); err != nil {
		t.Fatal(err)
	}
	newClaim, err := reopened.Claim(next.ID, 1)
	if err != nil || newClaim.Revision != next.Revision+2 {
		t.Fatalf("reclaim: %+v %v", newClaim, err)
	}
	_, err = reopened.AttachSession(next.ID, next.Revision, "old-worker")
	expectError(t, err, ErrConflict)
	_, err = reopened.Finish(next.ID, next.Revision, Outcome{Status: Completed})
	expectError(t, err, ErrConflict)
	if _, err := reopened.AttachSession(newClaim.ID, newClaim.Revision, "new-session"); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Cancel(newClaim.ID, newClaim.Revision); err != nil {
		t.Fatal(err)
	}
	_, err = reopened.Release(newClaim.ID, newClaim.Revision, 0)
	expectError(t, err, ErrConflict)
	if getRun(t, reopened, newClaim.ID).Status != Running {
		t.Fatal("release erased outstanding cancellation intent")
	}
}

func TestQueueCrashHelper(t *testing.T) {
	path := os.Getenv("POGO_TASKQUEUE_CRASH_TEST_DB")
	if path == "" {
		t.Skip("subprocess helper")
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Intentionally no Close: the parent kills this process after receiving the
	// committed claim and binding. Recovery must depend on disk, not cleanup.
	r := claimed(t, s, request("process-crash"))
	r, err = s.AttachSession(r.ID, r.Revision, "crashed-conversation")
	if err != nil {
		t.Fatal(err)
	}
	queued := admit(t, s, request("process-surviving-queue"))
	encoded, err := json.Marshal([]string{r.ID, queued.ID})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(string(encoded))
	<-time.After(time.Minute) // Keep a timer so the runtime cannot exit as deadlocked.
}

func TestAbruptProcessDeathRetainsClaimAndPendingWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crash.sqlite")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQueueCrashHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), "POGO_TASKQUEUE_CRASH_TEST_DB="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	line, err := bufio.NewReader(stdout).ReadBytes('\n')
	if err != nil {
		t.Fatalf("child did not commit ready receipt: %v %s", err, stderr.String())
	}
	var committed []string
	if err := json.Unmarshal(line, &committed); err != nil || len(committed) != 2 {
		t.Fatalf("child receipt invalid: %q %v", line, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("helper unexpectedly exited cleanly")
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	running := getRun(t, s, committed[0])
	queued := getRun(t, s, committed[1])
	if running.Status != Running || running.Revision != 2 || running.SessionID != "crashed-conversation" || !reflect.DeepEqual(running.Request, request("process-crash")) {
		t.Fatalf("durable active receipt was lost after abrupt process death: %+v", running)
	}
	if queued.Status != Queued || queued.Revision != 1 || queued.StartedAt != 0 || !reflect.DeepEqual(queued.Request, request("process-surviving-queue")) {
		t.Fatalf("durable pending admission was lost after abrupt process death: %+v", queued)
	}
	if err := s.Recover(); err != nil {
		t.Fatal(err)
	}
	if got := getRun(t, s, committed[0]); got.Status != Interrupted || got.SessionID != "crashed-conversation" {
		t.Fatalf("crash recovery failed: %+v", got)
	}
	if got := getRun(t, s, committed[1]); !reflect.DeepEqual(got, queued) {
		t.Fatal("crash recovery consumed untouched queued work")
	}
}
