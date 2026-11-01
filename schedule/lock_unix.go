//go:build unix

package schedule

import "syscall"

// lockFile takes an exclusive, non-blocking flock on an already-open file.
// syscall.Flock exists on both darwin and linux; it is per open-file
// description, so a second process (or a second open in the same process)
// contending for the same path fails here rather than queueing — which is
// exactly the "already running, skip" semantics a scheduler overlap wants.
func lockFile(fd uintptr) error {
	return syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockFile(fd uintptr) error {
	return syscall.Flock(int(fd), syscall.LOCK_UN)
}
