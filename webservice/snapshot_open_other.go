//go:build !unix

package webservice

import "os"

// Non-Unix hosts do not expose POSIX FIFOs. Root still confines path resolution;
// the caller immediately checks the opened file's type and directory identity.
func openSnapshotFile(root *os.Root, name string) (*os.File, error) { return root.Open(name) }
