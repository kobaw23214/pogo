package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestShellSchedulerSharesLimitsAndFairness(t *testing.T) {
	s := NewShellScheduler(ShellSchedulerConfig{Global: 2, User: 1, Session: 1, Queue: 3})
	first, err := s.acquire(context.Background(), "alice", "one")
	if err != nil {
		t.Fatal(err)
	}
	second := make(chan func(), 1)
	secondErr := make(chan error, 1)
	go func() {
		release, err := s.acquire(context.Background(), "alice", "two")
		if err != nil {
			secondErr <- err
			return
		}
		second <- release
	}()
	third, err := s.acquire(context.Background(), "bob", "three")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-second:
		t.Fatal("second task for the same user bypassed the user quota")
	default:
	}
	third()
	select {
	case <-second:
		t.Fatal("a queued task started before the user's running task finished")
	default:
	}
	first()
	select {
	case release := <-second:
		release()
	case err := <-secondErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("queued task was not admitted after a release")
	}
}

func TestShellSchedulerQueueFullAndCancellation(t *testing.T) {
	s := NewShellScheduler(ShellSchedulerConfig{Global: 1, User: 1, Session: 1, Queue: 1})
	first, err := s.acquire(context.Background(), "alice", "one")
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan error, 1)
	go func() {
		_, err := s.acquire(ctx, "alice", "one")
		waiting <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		queued := len(s.queue)
		s.mu.Unlock()
		if queued == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("request did not enter the queue")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := s.acquire(context.Background(), "bob", "two"); !errors.Is(err, ErrShellQueueFull) {
		t.Fatalf("queue overflow = %v, want ErrShellQueueFull", err)
	}
	cancel()
	if err := <-waiting; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled queue request = %v, want context.Canceled", err)
	}
	first()
	next, err := s.acquire(context.Background(), "bob", "two")
	if err != nil {
		t.Fatal(err)
	}
	next()
}

func TestShellSchedulerSessionLimit(t *testing.T) {
	s := NewShellScheduler(ShellSchedulerConfig{Global: 3, User: 3, Session: 1, Queue: 2})
	first, err := s.acquire(context.Background(), "alice", "same")
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	other, err := s.acquire(context.Background(), "alice", "other")
	if err != nil {
		t.Fatal(err)
	}
	other()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.acquire(ctx, "alice", "same"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("session wait = %v, want deadline", err)
	}
}

func TestBashQueuedTaskStartsOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash process fixture requires a Unix shell")
	}
	scheduler := NewShellScheduler(ShellSchedulerConfig{Global: 1, User: 1, Session: 1, Queue: 2})
	cwd := t.TempDir()
	newRunner := func(session string) *ShellJobRunner {
		r, err := NewShellJobRunner(context.Background(), filepath.Join(cwd, session), ShellJobHooks{}, ShellJobOptions{Scheduler: scheduler, Owner: "alice", Session: session})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(r.Close)
		return r
	}
	firstRunner, secondRunner := newRunner("one"), newRunner("two")
	firstBash := &Bash{Cwd: cwd, Jobs: firstRunner}
	secondBash := &Bash{Cwd: cwd, Jobs: secondRunner}
	if _, err := firstBash.Execute(context.Background(), json.RawMessage(`{"command":"printf first >> first.txt; sleep 0.1","run_in_background":true}`)); err != nil {
		t.Fatal(err)
	}
	result, err := secondBash.Execute(context.Background(), json.RawMessage(`{"command":"printf second >> second.txt","run_in_background":true}`))
	if err != nil {
		t.Fatal(err)
	}
	details := result.Details.(BashDetails)
	if details.Status != "queued" {
		t.Fatalf("second command status = %q, want queued", details.Status)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		record, err := secondRunner.Get(details.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if record.Status != "running" && record.Status != "queued" {
			if record.Status != "completed" {
				t.Fatalf("second command failed: %+v", record)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		data, err := os.ReadFile(filepath.Join(cwd, name))
		if err != nil || string(data) != name[:len(name)-4] {
			t.Fatalf("%s = %q, %v", name, data, err)
		}
	}
}

func TestBashStopQueuedTaskDoesNotExecute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash process fixture requires a Unix shell")
	}
	scheduler := NewShellScheduler(ShellSchedulerConfig{Global: 1, User: 1, Session: 1, Queue: 2})
	cwd := t.TempDir()
	newRunner := func(session string) *ShellJobRunner {
		r, err := NewShellJobRunner(context.Background(), filepath.Join(cwd, session), ShellJobHooks{}, ShellJobOptions{Scheduler: scheduler, Owner: "alice", Session: session})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(r.Close)
		return r
	}
	firstRunner, secondRunner := newRunner("one"), newRunner("two")
	firstBash := &Bash{Cwd: cwd, Jobs: firstRunner}
	if _, err := firstBash.Execute(context.Background(), json.RawMessage(`{"command":"printf first >> first.txt; sleep 0.3","run_in_background":true}`)); err != nil {
		t.Fatal(err)
	}
	secondBash := &Bash{Cwd: cwd, Jobs: secondRunner}
	result, err := secondBash.Execute(context.Background(), json.RawMessage(`{"command":"printf second >> second.txt","run_in_background":true}`))
	if err != nil {
		t.Fatal(err)
	}
	id := result.Details.(BashDetails).TaskID
	if err := secondRunner.Stop(id, "cancel queued test"); err != nil {
		t.Fatal(err)
	}
	record, err := secondRunner.Get(id)
	if err != nil || record.Status != "cancelled" {
		t.Fatalf("queued cancellation = %+v, %v", record, err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "second.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled queued command executed: %v", err)
	}
}
