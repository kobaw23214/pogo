//go:build unix

package mcpobserve

import (
	"os"
	"syscall"
)

// exec inherits blocking descriptors. Make only this verified pipe pollable so
// the reader can enforce its deadline; never change arbitrary file descriptors.
func observationPipeFile(fd int) *os.File {
	var info syscall.Stat_t
	if syscall.Fstat(fd, &info) != nil || info.Mode&syscall.S_IFMT != syscall.S_IFIFO {
		_ = syscall.Close(fd)
		return nil
	}
	if syscall.SetNonblock(fd, true) != nil {
		_ = syscall.Close(fd)
		return nil
	}
	return os.NewFile(uintptr(fd), "inherited-subagent-observation")
}
