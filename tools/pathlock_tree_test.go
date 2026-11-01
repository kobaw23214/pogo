package tools

import (
	"path/filepath"
	"testing"
	"time"
)

func lockMustWait(t *testing.T, acquired <-chan struct{}) {
	t.Helper()
	select {
	case <-acquired:
		t.Fatal("overlapping mutation entered concurrently")
	case <-time.After(30 * time.Millisecond):
	}
}

func TestPathLocksExcludeAncestorAndDescendantButNotSibling(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		p := &pathLocks{m: map[string]*pathEntry{}}
		dir := filepath.Join(t.TempDir(), "reports")
		first, next := dir, filepath.Join(dir, "nested", "review.txt")
		if reverse {
			first, next = next, first
		}
		held := p.acquire(first)
		entered, done := make(chan struct{}), make(chan struct{})
		go func() { e := p.acquire(next); close(entered); p.release(next, e); close(done) }()
		lockMustWait(t, entered)
		other := filepath.Join(filepath.Dir(dir), "reports-other", "sibling.txt")
		e := p.acquire(other)
		p.release(other, e)
		p.release(first, held)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("overlapping waiter did not resume")
		}
		if len(p.m) != 0 {
			t.Fatal("lock entries leaked")
		}
	}
}

func TestPathLocksPendingDirectoryDoesNotDeadlockNestedSiblingCopy(t *testing.T) {
	p := &pathLocks{m: map[string]*pathEntry{}}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.xlsx")
	target := filepath.Join(dir, "copy.xlsx")
	s := p.acquire(source)
	dirEntered, done := make(chan struct{}), make(chan struct{})
	go func() { d := p.acquire(dir); close(dirEntered); p.release(dir, d); close(done) }()
	lockMustWait(t, dirEntered)
	copyDone := make(chan struct{})
	go func() { c := p.acquire(target); p.release(target, c); close(copyDone) }()
	select {
	case <-copyDone:
	case <-time.After(time.Second):
		t.Fatal("waiting directory blocked an existing copy's sibling lock")
	}
	p.release(source, s)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("directory did not resume")
	}
}
