// Package testfs holds filesystem helpers shared by tests.
package testfs

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// SealedTempDir returns a t.TempDir for a tree the code under test may seal
// read-only, as the skill package store does for installed packages (0o555
// directories). os.RemoveAll cannot unlink entries of a read-only directory
// as a non-root user, which turned every such test into a cleanup failure on
// developer machines. Owner write access is restored on directories before the
// testing package removes the tree; the cleanup is registered after TempDir's,
// so it runs first.
func SealedTempDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() { RestoreWritable(dir) })
	return dir
}

// RestoreWritable gives the owner write access to every directory under root.
// File modes are left alone (unlinking needs only a writable parent), and
// symbolic links are never followed.
func RestoreWritable(root string) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
}
