package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// RemoveResultScopeSpill permanently removes only the namespace selected by a
// trusted host identity, including result records, metadata, locks and partial
// writes. It does not delete shared legacy spill files or another identity's
// namespace. Empty identities are rejected and missing directories are a no-op;
// cleanup never creates a directory or renews an artifact's lease/retention.
//
// The host must stop and drain the session (including background writers) before
// calling this function. Existing result operations are synchronized through the
// lifecycle lock, but this is not a permanent tombstone: a registry used after
// deletion could create the namespace again. An error can follow partial cleanup
// and must be reported separately from transcript deletion; retrying is safe.
func RemoveResultScopeSpill(base, scope string) error {
	if strings.TrimSpace(scope) == "" {
		return errors.New("cannot delete spill for an empty result scope")
	}
	if base == "" {
		base = DefaultSpillDir()
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		return fmt.Errorf("resolve spill cleanup root: %w", err)
	}
	before, err := os.Lstat(abs)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect spill cleanup root: %w", err)
	}
	if !before.IsDir() {
		return errors.New("spill cleanup root is not a regular directory")
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return fmt.Errorf("open spill cleanup root: %w", err)
	}
	defer root.Close()
	opened, statErr := root.Stat(".")
	after, afterErr := os.Lstat(abs)
	if statErr != nil || afterErr != nil || !after.IsDir() || !os.SameFile(before, opened) || !os.SameFile(before, after) {
		return errors.New("spill cleanup root changed while opening")
	}
	scopes, _, err := openResultCleanupDirectory(root, "scopes")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open spill scopes for cleanup: %w", err)
	}
	defer scopes.Close()
	sum := sha256.Sum256([]byte(scope))
	name := hex.EncodeToString(sum[:])
	target, identity, err := openResultCleanupDirectory(scopes, name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open session spill for cleanup: %w", err)
	}
	defer target.Close()
	unlock, err := lockResultScopeCleanup(target)
	if err != nil {
		return fmt.Errorf("wait for session spill operations before cleanup: %w", err)
	}
	defer unlock()
	// Recheck the scope entry before deleting even the pinned contents. A
	// replacement is never traversed: all recursion is relative to target.
	current, err := scopes.Lstat(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !current.IsDir() || !os.SameFile(identity, current) {
		return errors.New("session spill directory changed before cleanup")
	}
	if err := removeResultScopeContents(target); err != nil {
		return fmt.Errorf("session spill cleanup is incomplete: %w", err)
	}
	current, err = scopes.Lstat(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !current.IsDir() || !os.SameFile(identity, current) {
		return errors.New("session spill directory changed during cleanup")
	}
	// Remove, never RemoveAll, at the scopes level. Even a last-instant
	// replacement cannot make us recursively erase a different directory.
	if err := scopes.Remove(name); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove emptied session spill directory: %w", err)
	}
	return nil
}

func openResultCleanupDirectory(parent *os.Root, name string) (*os.Root, os.FileInfo, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !before.IsDir() {
		return nil, nil, errors.New("spill cleanup refuses symbolic links and non-directories")
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	opened, statErr := root.Stat(".")
	after, afterErr := parent.Lstat(name)
	if statErr != nil || afterErr != nil || !after.IsDir() || !os.SameFile(before, opened) || !os.SameFile(before, after) {
		root.Close()
		return nil, nil, errors.New("spill cleanup directory changed while opening")
	}
	return root, before, nil
}

// Missing/corrupt legacy layouts do not need a new lock file. A real result
// store's existing lifecycle lock makes deletion wait for current Save/Load/
// retain/prune operations, irrespective of its active or explicit retention.
func lockResultScopeCleanup(target *os.Root) (func(), error) {
	info, err := target.Lstat("results")
	if os.IsNotExist(err) || err == nil && !info.IsDir() {
		return func() {}, nil
	}
	if err != nil {
		return nil, err
	}
	results, _, err := openResultCleanupDirectory(target, "results")
	if err != nil {
		return nil, err
	}
	info, err = results.Lstat(resultLifecycleLockName)
	if os.IsNotExist(err) || err == nil && !info.Mode().IsRegular() {
		results.Close()
		return func() {}, nil
	}
	if err != nil {
		results.Close()
		return nil, err
	}
	unlock, err := lockResultLifecycle(context.Background(), results)
	if err != nil {
		results.Close()
		return nil, err
	}
	return func() {
		unlock()
		results.Close()
	}, nil
}

func removeResultScopeContents(root *os.Root) error {
	for {
		// Reopen from the beginning after each batch. Deleting entries may
		// reshuffle native directory offsets; continuing an old listing could
		// otherwise silently skip remaining files on some filesystems.
		listing, err := root.Open(".")
		if err != nil {
			return err
		}
		entries, readErr := listing.ReadDir(256)
		listing.Close()
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		if len(entries) == 0 {
			return nil
		}
		var failures []error
		for _, entry := range entries {
			// Root.RemoveAll unlinks symlinks and special files rather than
			// following them; each recursive open stays beneath this pinned
			// session directory. It never reopens the scopes/<hash> pathname.
			if err := root.RemoveAll(entry.Name()); err != nil && !os.IsNotExist(err) {
				failures = append(failures, err)
			}
		}
		if len(failures) != 0 {
			return errors.Join(failures...)
		}
	}
}
