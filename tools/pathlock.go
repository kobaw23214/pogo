package tools

import (
	"path/filepath"
	"strings"
	"sync"
)

// fileMutations serializes changes to the same file or overlapping directory.
//
// It is package-global on purpose. A per-registry lock would let two sessions of
// the Web server edit one file concurrently and lose an update, and the whole
// point of the lock is that the filesystem is shared state no matter how many
// agents are running.
//
// This is the Go equivalent of pi's withFileMutationQueue.
var fileMutations = &pathLocks{m: map[string]*pathEntry{}}

type pathEntry struct{ token byte }

// pathLocks retains only active locks. Ancestor/descendant exclusion also joins
// directory rename/delete to all existing file saves, reads and agent writes.
type pathLocks struct {
	mu      sync.Mutex
	m       map[string]*pathEntry
	changed *sync.Cond
}

func (p *pathLocks) acquire(key string) *pathEntry {
	key = filepath.Clean(key)
	p.mu.Lock()
	if p.changed == nil {
		p.changed = sync.NewCond(&p.mu)
	}
	for {
		busy := false
		for held := range p.m {
			if pathsOverlap(held, key) {
				busy = true
				break
			}
		}
		if !busy {
			break
		}
		p.changed.Wait()
	}
	e := &pathEntry{}
	p.m[key] = e
	p.mu.Unlock()
	return e
}

func (p *pathLocks) release(key string, e *pathEntry) {
	p.mu.Lock()
	if p.m[filepath.Clean(key)] == e {
		delete(p.m, filepath.Clean(key))
	}
	p.changed.Broadcast()
	p.mu.Unlock()
}

// A directory operation must exclude writes to every descendant, while files
// in independent branches remain concurrent. Waiting requests do not reserve
// keys: existing source -> sibling-copy lock nesting can finish without a
// waiting directory mutation deadlocking its second lock.
func pathsOverlap(a, b string) bool {
	within := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	}
	return within(a, b) || within(b, a)
}

// withFileLock runs fn while holding the lock for path.
//
// The key is the canonical path, so `./a.go`, `a.go`, an absolute path, and a
// symlink to the same file all serialize against each other. Keying on the raw
// string instead would make the lock decorative.
func withFileLock[T any](path string, fn func() (T, error)) (T, error) {
	key := canonical(path) // does filesystem work; must happen before locking
	e := fileMutations.acquire(key)
	defer fileMutations.release(key, e)
	return fn()
}

// WithFileMutationLock joins host editor saves to the write/edit tools' queue.
// The caller must still authorize the path and recheck its expected file while
// holding this lock; serialization is not a filesystem permission grant.
func WithFileMutationLock(path string, fn func() error) error {
	_, err := withFileLock(path, func() (struct{}, error) {
		return struct{}{}, fn()
	})
	return err
}
