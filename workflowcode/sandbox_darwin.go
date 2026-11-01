//go:build darwin

package workflowcode

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func sbplString(value string) string { raw, _ := json.Marshal(value); return string(raw) }

func sandboxCommand(ctx context.Context, spec interpreterSpec) (*exec.Cmd, func(), error) {
	const sandbox = "/usr/bin/sandbox-exec"
	if _, err := os.Stat(sandbox); err != nil {
		return nil, func() {}, fail("sandbox_unavailable", "系统未提供代码隔离执行器，未降级执行")
	}
	reads := []string{"(literal \"/\")", "(subpath \"/System/Library\")", "(subpath \"/usr/lib\")", "(subpath \"/Library/Apple/System/Library\")", "(literal \"/dev/null\")", "(literal \"/dev/urandom\")", "(literal " + sbplString(spec.executable) + ")"}
	for _, directory := range spec.readDirs {
		reads = append(reads, "(subpath "+sbplString(directory)+")")
	}
	ancestorPaths := map[string]bool{}
	for _, path := range append([]string{spec.executable}, spec.readDirs...) {
		for directory := filepath.Dir(path); directory != "/" && directory != "."; directory = filepath.Dir(directory) {
			ancestorPaths[directory] = true
		}
	}
	var metadata []string
	for path := range ancestorPaths {
		metadata = append(metadata, "(literal "+sbplString(path)+")")
	}
	sort.Strings(metadata)
	// No file-write, network, process-fork or other process-exec permission.
	// Exact parent metadata lets the loader resolve the interpreter; metadata
	// of arbitrary host files remains denied. The only user-home file that can be read is the selected
	// interpreter binary (for example a Node installation managed by nvm).
	profile := "(version 1) (deny default) (allow process-exec (literal " + sbplString(spec.executable) + ")) (allow file-read-metadata " + strings.Join(metadata, " ") + ") (allow file-read* " + strings.Join(reads, " ") + ") (allow sysctl-read)"
	args := []string{"-p", profile, spec.executable}
	if spec.language == "python" {
		args = append(args, "-I", "-S", "-B", "-c", pythonWrapper)
	} else {
		args = append(args, "--no-addons", "--no-global-search-paths", "--max-old-space-size=128", "--disable-proto=throw", "-e", javascriptWrapper)
	}
	cmd := exec.CommandContext(ctx, sandbox, args...)
	cmd.Dir = "/"
	cmd.Env = cleanEnvironment()
	return cmd, func() {}, nil
}

func residentMemory(pid int) (int64, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/ps", "-o", "rss=", "-p", strconv.Itoa(pid))
	cmd.Env = cleanEnvironment()
	data, err := cmd.Output()
	if err != nil {
		return 0, false
	}
	kib, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return kib * 1024, err == nil
}
