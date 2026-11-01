//go:build darwin

package main

import (
	"golang.org/x/sys/unix"
	"net"
	"os"
	"syscall"
	"time"
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
		cred, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		good = e == nil && cred.Uid == uint32(os.Geteuid())
	}) != nil {
		return false
	}
	return good
}

// Native frame ingestion is Linux-only; Darwin is for local IPC/Pion tests.
func monotonicNS() int64 { return time.Now().UnixNano() }
