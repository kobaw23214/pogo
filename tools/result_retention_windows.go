//go:build windows

package tools

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// Lock one byte of the same never-unlinked file used by the Unix path. A
// nonblocking LockFileEx retry loop makes cancellation and the ten-second
// deadline effective without leaving a pending asynchronous lock request.
// Files inherit the trusted scope directory's Windows access control; 0600 is
// also supplied for portable callers, but is not treated as a Windows ACL.
func lockResultLifecycle(ctx context.Context, root *os.Root) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var file *os.File
	for attempt := 0; attempt < 3; attempt++ {
		before, err := root.Lstat(resultLifecycleLockName)
		flags := os.O_RDWR
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
		if statErr != nil || afterErr != nil || !opened.Mode().IsRegular() || !after.Mode().IsRegular() || !os.SameFile(opened, after) || (before != nil && !os.SameFile(before, opened)) {
			file.Close()
			return nil, errors.New("result lifecycle lock changed while opening")
		}
		break
	}
	if file == nil {
		return nil, errors.New("result lifecycle lock could not be opened")
	}
	handle := windows.Handle(file.Fd())
	fileType, err := windows.GetFileType(handle)
	if err != nil || fileType != windows.FILE_TYPE_DISK {
		file.Close()
		return nil, errors.New("result lifecycle lock must be a regular disk file")
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil || information.NumberOfLinks != 1 || information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		file.Close()
		return nil, errors.New("result lifecycle lock must not be a hard link or reparse point")
	}
	if err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		file.Close()
		return nil, err
	}
	overlapped := &windows.Overlapped{}
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
		if err == nil {
			var once sync.Once
			return func() {
				once.Do(func() {
					_ = windows.UnlockFileEx(handle, 0, 1, 0, overlapped)
					_ = file.Close()
				})
			}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
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
