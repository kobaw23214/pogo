//go:build !linux

package tools

import "os/exec"

// A private session must never silently become an unconfined host command.
func WrapScopedCommand(*exec.Cmd, *CommandScope) error {
	return ErrScopedCommandUnavailable
}
