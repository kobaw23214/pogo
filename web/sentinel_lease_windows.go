//go:build windows

package web

import (
	"errors"
	"os"
)

func lockSentinelRuntime(string) (*os.File, error) {
	return nil, errors.New("哨兵运行时需要受支持的进程锁和 Linux 脚本沙箱")
}
