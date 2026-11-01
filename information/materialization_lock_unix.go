//go:build unix

package information

import (
	"os"
	"runtime"
	"syscall"
)

const materializationReuseSupported = runtime.GOOS == "linux" || runtime.GOOS == "darwin"

// A real file lock joins the web server and its separately executed subagents.
func lockMaterialization(file *os.File) (func(), bool, error) {
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }, true, nil
	}
}
