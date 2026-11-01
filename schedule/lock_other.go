//go:build !unix

package schedule

import "fmt"

// Scheduled jobs have no backend outside unix anyway (Detect refuses first);
// these exist so the package still compiles there.
func lockFile(fd uintptr) error {
	return fmt.Errorf("scheduled jobs are not supported on this platform")
}
func unlockFile(fd uintptr) error { return nil }
