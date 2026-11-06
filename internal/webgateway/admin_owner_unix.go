//go:build unix

package webgateway

import (
	"errors"
	"os"
	"syscall"
)

func socketOwnedByProcess(info os.FileInfo) error {
	if info == nil {
		return errors.New("gateway: private management path ownership cannot be verified")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return errors.New("gateway: private management path ownership cannot be verified")
	}
	if int(stat.Uid) != os.Geteuid() {
		return errors.New("gateway: private management path owner does not match process")
	}
	return nil
}

func socketManagementSupported() error { return nil }
