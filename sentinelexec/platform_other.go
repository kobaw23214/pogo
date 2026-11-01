//go:build !unix

package sentinelexec

import "os/exec"

func setProcessGroup(*exec.Cmd) {}
