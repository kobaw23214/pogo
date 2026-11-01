package tools

import (
	"context"
	"io"
	"os"
	"strings"
	"time"
)

// This file is the other half of spill.go's lifecycle: Spill decides where
// oversized output goes, PruneSpill decides when it leaves.
//
// Legacy spills retain their mtime-age policy. Scoped stored observations can
// also carry bounded explicit retention or a short active-read lease. Neither
// is a permanent reference count: an old transcript can still name a result
// after its explicit expiry, and a missing result must not replay the action.
//
// Automatic sweeping lives with the web Manager (the one long-lived process);
// the CLI gets -spill-prune as the manual form, same shape as
// -checkpoints-prune. This file holds only the pure mechanics both call.

// PruneResult is what one pass over the spill directory did.
type PruneResult struct {
	Removed int
	Bytes   int64
}

// partialMaxAge is how long an abandoned .partial-* temp file may lie around
// before any pass removes it, regardless of the configured retention. Spill
// already removes its own temp on every failure path it controls; what
// survives is the crash case — a process killed between create-temp and
// rename — and an hour is long enough that a live writer's temp is never
// mistaken for a dead one's.
const partialMaxAge = time.Hour

// PruneSpill removes every managed *.log in dir whose mtime is older than maxAge,
// plus any .partial-* leftover older than partialMaxAge, and reports what
// went. A missing directory is a zero result, not an error: nothing was ever
// spilled there, which is a normal state on a fresh machine.
// A non-positive maxAge disables the pass, including partial-file cleanup.
//
// now is injected rather than read so a test can age a file without sleeping.
// A file that disappears or refuses to delete mid-pass is skipped, not
// reported: the directory is shared with live writers, and the next pass
// retries — one stubborn file must not stop the rest of the sweep.
func PruneSpill(dir string, maxAge time.Duration, now time.Time) (PruneResult, error) {
	var res PruneResult
	if maxAge <= 0 {
		return res, nil
	}
	cutoff := now.Add(-maxAge)
	partialCutoff := now.Add(-partialMaxAge)
	err := walkManagedSpill(dir, func(root *os.Root, name string, info os.FileInfo) {
		isLog := strings.HasSuffix(name, ".log")
		age := partialCutoff
		if isLog {
			age = cutoff
		}
		if !info.ModTime().Before(age) {
			return
		}
		id := storedResultIDFromFilename(name)
		if id != "" {
			// Use the same process-safe lock as Save/Load/Retain. Busy storage
			// is retried next sweep instead of deleting beneath a live read.
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			unlock, err := lockResultLifecycle(ctx, root)
			cancel()
			if err != nil {
				return
			}
			defer unlock()
			metadata, metaErr := readResultLifecycle(context.Background(), root, id)
			if metaErr != nil && !os.IsNotExist(metaErr) {
				return // unreadable lifecycle data is not permission to delete
			}
			if metaErr == nil && protectedResultLifecycle(metadata, now) {
				return
			}
		}
		// Recheck live writers immediately before deletion. Lstat and Remove
		// operate on the pinned containing directory; a replaced directory
		// path cannot redirect either operation elsewhere.
		current, statErr := root.Lstat(name)
		if statErr != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) || !current.ModTime().Before(age) {
			return
		}
		if removeErr := root.Remove(name); removeErr != nil {
			return
		}
		res.Removed++
		res.Bytes += current.Size()
		if id != "" {
			if metadata, err := openResultMetadata(root, false); err == nil {
				_ = metadata.Remove(id + ".json")
				_ = metadata.Close()
			}
		}
	})
	return res, err
}

// SpillDiskUsage counts the managed *.log files in dir and their total size — the
// number the settings dialog shows so "how much is this costing" never needs
// a shell. Same missing-directory rule as PruneSpill: zero, not an error.
// .partial-* files are not counted: they are in-flight writes, not content
// anyone can read back.
func SpillDiskUsage(dir string) (files int, bytes int64, err error) {
	err = walkManagedSpill(dir, func(_ *os.Root, name string, info os.FileInfo) {
		if !strings.HasSuffix(name, ".log") {
			return
		}
		files++
		bytes += info.Size()
	})
	return files, bytes, err
}

// The spill root also contains session-owned namespaces and tool-result
// records. Only these reserved layouts belong to the sweeper; it must never
// turn into a general recursive deletion of user directories beneath dir:
//
//	*.log, .partial-*
//	scopes/<hex>/{*.log,.partial-*,results/...}
//	results/{*.log,.partial-*,<hex>/{*.log,.partial-*}}
//
// Scope hashes are SHA-256 (64 hex digits); unscoped registry nonces are 16
// bytes (32 hex digits). Directory handles and inode checks ensure a rename
// or symlink substitution does not redirect traversal after inspection.
const maxManagedSpillDepth = 12

type managedSpillLocation uint8

const (
	spillRootLocation managedSpillLocation = iota
	spillScopesLocation
	spillNamespaceLocation
	spillResultsLocation
	spillResultNamespaceLocation
)

func walkManagedSpill(dir string, visit func(*os.Root, string, os.FileInfo)) error {
	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil {
		return err
	}
	current, err := os.Lstat(dir)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, opened) || !os.SameFile(info, current) {
		return nil // raced directory replacement; a future pass can retry
	}
	return walkManagedSpillRoot(root, spillRootLocation, 0, visit)
}

func walkManagedSpillRoot(root *os.Root, location managedSpillLocation, depth int, visit func(*os.Root, string, os.FileInfo)) error {
	listing, err := root.Open(".")
	if err != nil {
		return err
	}
	defer listing.Close()
	for {
		entries, readErr := listing.ReadDir(256)
		for _, entry := range entries {
			name := entry.Name()
			next, childAllowed := managedSpillChild(location, name)
			isFile := location != spillScopesLocation && (strings.HasSuffix(name, ".log") || strings.HasPrefix(name, ".partial-"))
			if !childAllowed && !isFile {
				continue
			}
			info, statErr := root.Lstat(name)
			if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if info.IsDir() {
				if !childAllowed || depth >= maxManagedSpillDepth {
					continue
				}
				child, openErr := root.OpenRoot(name)
				if openErr != nil {
					continue
				}
				opened, childStatErr := child.Stat(".")
				current, currentErr := root.Lstat(name)
				if childStatErr == nil && currentErr == nil && current.Mode()&os.ModeSymlink == 0 && os.SameFile(info, opened) && os.SameFile(info, current) {
					// An unreadable or concurrently removed child must not stop
					// cleanup/statistics for independent namespaces.
					_ = walkManagedSpillRoot(child, next, depth+1, visit)
				}
				_ = child.Close()
				continue
			}
			if isFile && info.Mode().IsRegular() {
				visit(root, name, info)
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func managedSpillChild(location managedSpillLocation, name string) (managedSpillLocation, bool) {
	switch location {
	case spillRootLocation:
		if name == "scopes" {
			return spillScopesLocation, true
		}
		if name == "results" {
			return spillResultsLocation, true
		}
	case spillScopesLocation:
		if managedSpillHexNamespace(name) {
			return spillNamespaceLocation, true
		}
	case spillNamespaceLocation:
		if name == "results" {
			return spillResultsLocation, true
		}
	case spillResultsLocation:
		if managedSpillHexNamespace(name) {
			return spillResultNamespaceLocation, true
		}
	}
	return 0, false
}

func managedSpillHexNamespace(name string) bool {
	if len(name) != 32 && len(name) != 64 {
		return false
	}
	for _, c := range name {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
