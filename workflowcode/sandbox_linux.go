//go:build linux

package workflowcode

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func sandboxCommand(ctx context.Context, spec interpreterSpec) (*exec.Cmd, func(), error) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, func() {}, fail("sandbox_unavailable", "缺少bubblewrap代码隔离执行器，未降级执行")
	}
	filter, err := processFilterForArch(runtime.GOARCH)
	if err != nil {
		return nil, func() {}, err
	}
	file, err := os.CreateTemp("", "pogo-code-seccomp-*")
	if err != nil {
		return nil, func() {}, fail("sandbox_unavailable", "无法建立代码隔离策略")
	}
	cleanup := func() { file.Close(); os.Remove(file.Name()) }
	if _, err = file.Write(filter); err != nil {
		cleanup()
		return nil, func() {}, fail("sandbox_unavailable", "无法写入代码隔离策略")
	}
	if _, err = file.Seek(0, 0); err != nil {
		cleanup()
		return nil, func() {}, fail("sandbox_unavailable", "无法读取代码隔离策略")
	}
	args := []string{"--die-with-parent", "--new-session", "--unshare-all", "--cap-drop", "ALL", "--clearenv", "--proc", "/proc", "--dev", "/dev", "--dir", "/tmp", "--chdir", "/", "--seccomp", "4"}
	// Start from bubblewrap's empty root. Never bind the host root, HOME,
	// workspace, application data, credentials, or a Docker socket.
	seen := map[string]bool{}
	for _, directory := range append([]string{"/usr/lib", "/lib", "/lib64"}, spec.readDirs...) {
		resolved, err := filepath.EvalSymlinks(directory)
		if err != nil || seen[directory] {
			continue
		}
		if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
			continue
		}
		seen[directory] = true
		args = append(args, "--ro-bind", resolved, directory)
	}
	args = append(args, "--ro-bind", spec.executable, spec.executable)
	args = append(args, "--remount-ro", "/", "--remount-ro", "/proc", "--remount-ro", "/dev")
	for _, env := range cleanEnvironment() {
		name, value, _ := strings.Cut(env, "=")
		args = append(args, "--setenv", name, value)
	}
	args = append(args, "--", spec.executable)
	if spec.language == "python" {
		args = append(args, "-I", "-S", "-B", "-c", pythonWrapper)
	} else {
		args = append(args, "--no-addons", "--no-global-search-paths", "--max-old-space-size=128", "--disable-proto=throw", "-e", javascriptWrapper)
	}
	cmd := exec.CommandContext(ctx, bwrap, args...)
	cmd.Dir = "/"
	cmd.Env = cleanEnvironment()
	// Execute prepends the result channel as FD 3. This policy becomes FD 4.
	cmd.ExtraFiles = []*os.File{file}
	return cmd, cleanup, nil
}

func residentMemory(pid int) (int64, bool) {
	seen := map[int]bool{}
	var total int64
	var read func(int)
	read = func(current int) {
		if current <= 0 || seen[current] || len(seen) > 16 {
			return
		}
		seen[current] = true
		root := fmt.Sprintf("/proc/%d", current)
		if data, err := os.ReadFile(root + "/status"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "VmRSS:") {
					fields := strings.Fields(line)
					if len(fields) > 1 {
						kib, _ := strconv.ParseInt(fields[1], 10, 64)
						total += kib * 1024
					}
				}
			}
		}
		if data, err := os.ReadFile(fmt.Sprintf("%s/task/%d/children", root, current)); err == nil {
			for _, value := range strings.Fields(string(data)) {
				child, _ := strconv.Atoi(value)
				read(child)
			}
		}
	}
	read(pid)
	return total, len(seen) > 0
}
