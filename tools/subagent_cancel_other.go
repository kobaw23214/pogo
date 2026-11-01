//go:build !unix

package tools

import (
	"os/exec"
	"time"
)

func configureSubagentCancellation(cmd *exec.Cmd) func() {
	cmd.Cancel = func() error { return KillGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second
	return func() {}
}
