//go:build !windows

package web

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func lockSentinelRuntime(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("哨兵已有其他监管进程，未启动重复监听: %w", err)
	}
	return f, nil
}
