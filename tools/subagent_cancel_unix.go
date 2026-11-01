//go:build unix

package tools

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

func configureSubagentCancellation(cmd *exec.Cmd) func() {
	var mu sync.Mutex
	var timer *time.Timer
	finished := false
	cmd.Cancel = func() error {
		mu.Lock()
		defer mu.Unlock()
		if finished || cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				return err
			}
		}
		timer = time.AfterFunc(2*time.Second, func() {
			mu.Lock()
			defer mu.Unlock()
			if !finished {
				_ = KillGroup(cmd)
			}
		})
		return nil
	}
	// Let the group kill run before os/exec's direct-child fallback.
	cmd.WaitDelay = 3 * time.Second
	return func() {
		mu.Lock()
		defer mu.Unlock()
		finished = true
		if timer != nil {
			timer.Stop()
		}
	}
}
