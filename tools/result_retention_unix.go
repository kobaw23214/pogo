//go:build unix

package tools

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

// The lock inode is never removed. Save/read/retain and the sweeper therefore
// serialize across both goroutines and independent Pogo processes. Only a
// verified private regular file is locked; a FIFO or symlink cannot hang/open
// an unrelated object. Lock acquisition is bounded even for background callers.
func lockResultLifecycle(ctx context.Context, root *os.Root) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var file *os.File
	for attempt := 0; attempt < 3; attempt++ {
		before, err := root.Lstat(resultLifecycleLockName)
		flags := os.O_RDWR | syscall.O_NONBLOCK
		if os.IsNotExist(err) {
			flags |= os.O_CREATE | os.O_EXCL
		} else if err != nil || !before.Mode().IsRegular() {
			return nil, errors.New("result lifecycle lock is not a regular file")
		}
		file, err = root.OpenFile(resultLifecycleLockName, flags, 0600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		opened, statErr := file.Stat()
		after, afterErr := root.Lstat(resultLifecycleLockName)
		if statErr != nil || afterErr != nil || !after.Mode().IsRegular() || !os.SameFile(opened, after) || (before != nil && !os.SameFile(before, opened)) {
			file.Close()
			return nil, errors.New("result lifecycle lock changed while opening")
		}
		break
	}
	if file == nil {
		return nil, errors.New("result lifecycle lock could not be opened")
	}
	fd := int(file.Fd())
	if err := adoptEditLeaseFD(fd); err != nil {
		file.Close()
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = file.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN && err != syscall.EINTR {
			file.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
