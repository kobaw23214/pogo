//go:build !linux && !darwin

package workflowcode

import (
	"context"
	"os/exec"
)

func sandboxCommand(context.Context, interpreterSpec) (*exec.Cmd, func(), error) {
	return nil, func() {}, fail("sandbox_unavailable", "当前系统不支持代码隔离执行器，未降级执行")
}
func resolveInterpreter(path, language string) (string, error)                             { return path, nil }
func pythonFrameworkRoot(string) string                                                    { return "" }
func osPath(path string) (string, error)                                                   { return path, nil }
func configureProcess(*exec.Cmd)                                                           {}
func killProcessGroup(int) error                                                           { return nil }
func monitorMemory(context.Context, int, <-chan struct{}, chan<- bool, context.CancelFunc) {}
