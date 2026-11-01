package sentinelexec

import (
	"os/exec"

	"github.com/yosukeno/pogo/tools"
)

func strictWrap(cmd *exec.Cmd, scope *tools.CommandScope) error {
	if err := tools.WrapScopedCommand(cmd, scope); err != nil {
		return err
	}
	// CommandScope owns namespace and environment construction. Sentinel's
	// long-lived sources additionally need bounded private scratch mounts.
	args := make([]string, 0, len(cmd.Args)+4)
	for i, arg := range cmd.Args {
		if arg == "--" {
			args = append(args, cmd.Args[i:]...)
			break
		}
		if arg == "--tmpfs" {
			args = append(args, "--size", "67108864")
		}
		args = append(args, arg)
	}
	cmd.Args = args
	return nil
}
