package tools

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Pruning is by mtime age, and nothing else: the files are content-addressed
// and shared across sessions, so there is no reference count to consult.

// writeAged plants a file with a chosen mtime, because the retention decision
// reads exactly that one field.
func writeAged(t *testing.T, dir, name string, size int, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	data := make([]byte, size)
	for i := range data {
		data[i] = 'x'
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPruneSpillRemovesOldKeepsNew(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := writeAged(t, dir, "bash-aaaaaa.log", 100, now.Add(-10*24*time.Hour))
	fresh := writeAged(t, dir, "bash-bbbbbb.log", 50, now.Add(-2*24*time.Hour))
	// A name that is not a spill file must be left alone however old it is.
	other := writeAged(t, dir, "notes.txt", 10, now.Add(-365*24*time.Hour))

	res, err := PruneSpill(dir, 7*24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 1 || res.Bytes != 100 {
		t.Errorf("PruneResult = %+v, want {Removed:1 Bytes:100}", res)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the 10-day-old file survived a 7-day retention")
	}
	for _, p := range []string{fresh, other} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should have been kept: %v", filepath.Base(p), err)
		}
	}
}

// A .partial-* leftover is a crashed Spill's temp file. It goes after an hour
// even when the retention window is years, because nothing will ever rename
// it into place — but a fresh one is a live writer's and is left alone.
func TestPruneSpillHandlesPartialLeftovers(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	stale := writeAged(t, dir, ".partial-123456", 200, now.Add(-2*time.Hour))
	live := writeAged(t, dir, ".partial-999999", 200, now)

	res, err := PruneSpill(dir, 3650*24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 1 || res.Bytes != 200 {
		t.Errorf("PruneResult = %+v, want {Removed:1 Bytes:200}", res)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a 2-hour-old .partial-* survived")
	}
	if _, err := os.Stat(live); err != nil {
		t.Error("a just-created .partial-* was removed from under its writer")
	}
}

func TestPruneSpillEmptyAndMissingDirs(t *testing.T) {
	res, err := PruneSpill(t.TempDir(), 7*24*time.Hour, time.Now())
	if err != nil || res != (PruneResult{}) {
		t.Errorf("empty dir: %+v, %v", res, err)
	}
	// A directory that was never created is "nothing spilled yet", the normal
	// state of a fresh machine — zero, not an error.
	res, err = PruneSpill(filepath.Join(t.TempDir(), "nope"), 7*24*time.Hour, time.Now())
	if err != nil || res != (PruneResult{}) {
		t.Errorf("missing dir: %+v, %v", res, err)
	}
}

func TestSpillDiskUsage(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeAged(t, dir, "bash-aaaaaa.log", 100, now)
	writeAged(t, dir, "read-bbbbbb.log", 23, now)
	writeAged(t, dir, ".partial-123456", 999, now) // in-flight, not content

	files, bytes, err := SpillDiskUsage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 || bytes != 123 {
		t.Errorf("usage = %d files, %d bytes; want 2 files, 123 bytes", files, bytes)
	}

	files, bytes, err = SpillDiskUsage(filepath.Join(t.TempDir(), "nope"))
	if err != nil || files != 0 || bytes != 0 {
		t.Errorf("missing dir: %d files, %d bytes, %v", files, bytes, err)
	}
}
