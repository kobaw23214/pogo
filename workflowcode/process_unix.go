//go:build linux || darwin

package workflowcode

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func osPath(path string) (string, error) { return filepath.EvalSymlinks(path) }

func resolveInterpreter(path, language string) (string, error) {
	resolved, err := osPath(path)
	if err != nil {
		return "", err
	}
	if language == "python" {
		if root := pythonFrameworkRoot(resolved); root != "" {
			// Framework Python's bin executable is a posix_spawn launcher on
			// macOS. Select its actual executable so process-fork stays denied.
			candidate := filepath.Join(root, "Resources", "Python.app", "Contents", "MacOS", "Python")
			if final, err := osPath(candidate); err == nil {
				return final, nil
			}
		}
	}
	return resolved, nil
}

func pythonFrameworkRoot(path string) string {
	marker := "/Python.framework/Versions/"
	if at := strings.Index(path, marker); at >= 0 {
		rest := path[at+len(marker):]
		if end := strings.IndexByte(rest, '/'); end > 0 {
			return path[:at+len(marker)+end]
		}
	}
	return ""
}

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process.Pid) }
}
func killProcessGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

// This is a sampled RSS budget, not a per-node cgroup guarantee. The enclosing
// deployment still owns its hard resource ceiling.
func monitorMemory(ctx context.Context, pid int, done <-chan struct{}, exceeded chan<- bool, cancel context.CancelFunc) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			if bytes, ok := residentMemory(pid); ok && bytes > maxMemoryBytes {
				select {
				case exceeded <- true:
				default:
				}
				cancel()
				return
			}
		}
	}
}
