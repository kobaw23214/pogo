//go:build linux

package main

import (
	"golang.org/x/sys/unix"
	"net"
	"os"
	"syscall"
)

func ownedFile(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func sameUID(c *net.UnixConn) bool {
	raw, err := c.SyscallConn()
	if err != nil {
		return false
	}
	var good bool
	if raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		good = e == nil && cred.Uid == uint32(os.Geteuid())
	}) != nil {
		return false
	}
	return good
}
func monotonicNS() int64 {
	var t unix.Timespec
	if unix.ClockGettime(unix.CLOCK_MONOTONIC, &t) != nil {
		return 0
	}
	return t.Nano()
}
