//go:build unix

package webservice

import (
	"os"
	"syscall"
)

// A regular file can be replaced by a FIFO between listing and opening.
// Nonblocking open lets the subsequent identity/type check reject it safely.
func openSnapshotFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
