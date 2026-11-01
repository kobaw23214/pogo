//go:build linux

package skillruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func NewLocalExecutor(ctx context.Context, root string) (*LocalExecutor, error) {
	platform := currentPlatform()
	if platform.issue != nil {
		return nil, platform.issue
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, executionError("SANDBOX_UNAVAILABLE", "本地 Skill 沙箱缺少 bubblewrap，请使用包含运行器的 Pogo 镜像")
	}
	helper, err := exec.LookPath("pogo-skill-runner")
	if err != nil {
		return nil, executionError("RUNNER_UNAVAILABLE", "本地 Skill 运行器缺失，请重新构建 %s 的 Pogo 镜像", platform.host)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	e := &LocalExecutor{root: root, workDir: filepath.Join(root, "local-calls"), bwrap: bwrap, helper: helper, platform: platform.spec, capacity: make(chan struct{}, 1)}
	e.memoryBudgetMiB = localMemoryBudget()
	if err := os.MkdirAll(e.workDir, 0700); err != nil {
		return nil, err
	}
	// Pin the actual runner and namespace tool, not just an executable path.
	hash := sha256.New()
	_, _ = io.WriteString(hash, platform.host+"\n")
	dependencies := append([]string{helper, bwrap, "/etc/os-release", "/bin/sh"}, platform.spec.systemLibraries()...)
	for _, name := range dependencies {
		f, err := os.Open(name)
		if err != nil {
			return nil, executionError("RUNTIME_DEPENDENCY_UNAVAILABLE", "%s 运行依赖不可用：%s (%v)", platform.host, name, err)
		}
		_, err = io.Copy(hash, f)
		_ = f.Close()
		if err != nil {
			return nil, err
		}
	}
	e.identity = "local-bwrap-v1:" + hex.EncodeToString(hash.Sum(nil))
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args := append(e.baseArgs(), "--remount-ro", "/", "--", "/pogo-runner", "--probe")
	cmd := exec.CommandContext(ctx, e.bwrap, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	stdout := &boundedCapture{max: 4096, cancel: cancel}
	stderr := &boundedCapture{max: 4096, cancel: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return nil, executionError("SANDBOX_UNAVAILABLE", "本地 Skill 沙箱不可用（用户/PID/网络命名空间或挂载权限）：%s", strings.TrimSpace(stderr.buf.String()))
	}
	var reply localResult
	if json.Unmarshal(stdout.buf.Bytes(), &reply) != nil || reply.Protocol != localProtocol || reply.Error != "" {
		return nil, executionError("RUNNER_UNAVAILABLE", "本地 Skill 运行器握手失败，未启用非隔离执行")
	}
	return e, nil
}

// Build an empty filesystem view. Never bind /, HOME, the pkg warehouse,
// Docker sockets or Pogo's credentials into the tool's namespace.
func (e *LocalExecutor) baseArgs() []string {
	a := []string{"--unshare-user", "--unshare-pid", "--unshare-net", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try", "--new-session", "--die-with-parent", "--as-pid-1", "--cap-drop", "ALL", "--clearenv"}
	for _, dir := range []string{"/usr", "/bin", "/lib", "/lib64"} {
		if _, err := os.Stat(dir); err == nil {
			a = append(a, "--ro-bind", dir, dir)
		}
	}
	if _, err := os.Stat("/etc/ld.so.cache"); err == nil {
		a = append(a, "--ro-bind", "/etc/ld.so.cache", "/etc/ld.so.cache")
	}
	a = append(a, "--ro-bind", e.helper, "/pogo-runner", "--proc", "/proc", "--dev", "/dev", "--size", "536870912", "--tmpfs", "/tmp", "--chdir", "/tmp")
	return append(a, "--setenv", "HOME", "/tmp", "--setenv", "TMPDIR", "/tmp", "--setenv", "PATH", "/runtime/bin:/usr/bin:/bin", "--setenv", "LANG", "C.UTF-8")
}

func (e *LocalExecutor) ExecuteDetailed(ctx context.Context, in Execution) (DetailedResult, error) {
	var empty DetailedResult
	if e == nil || in.IsolationProfile != LocalProfile || !validLimits(in.Limits) {
		return empty, errors.New("PROVIDER_NOT_READY: local sandbox profile or limits invalid")
	}
	if err := hostCeiling(LocalProfile, structLimits(in.Limits)); err != nil {
		return empty, err
	}
	if e.memoryBudgetMiB > 0 && in.Limits.MemoryMiB > e.memoryBudgetMiB {
		return empty, errors.New("CONTAINER_MEMORY_BUDGET: 提高 POGO_MEMORY_LIMIT 后重启")
	}
	select {
	case e.capacity <- struct{}{}:
		defer func() { <-e.capacity }()
	case <-ctx.Done():
		return empty, ctx.Err()
	}
	entry := strings.TrimPrefix(in.Entrypoint, "./")
	if entry == in.Entrypoint || filepath.IsAbs(entry) || strings.ContainsAny(entry, "\\\x00\r\n") || strings.Contains(entry, "..") {
		return empty, errors.New("INVALID_MANIFEST: invalid entrypoint")
	}
	if _, err := confinedPath(e.root, in.RuntimeDir); err != nil {
		return empty, err
	}
	program := filepath.Join(in.RuntimeDir, filepath.FromSlash(entry))
	if _, err := confinedPath(in.RuntimeDir, program); err != nil {
		return empty, err
	}
	fi, err := os.Lstat(program)
	if err != nil || !fi.Mode().IsRegular() {
		return empty, errors.New("ARTIFACT_MISSING: provider executable")
	}
	f, err := os.Open(program)
	if err != nil {
		return empty, err
	}
	var header [20]byte
	_, err = io.ReadFull(f, header[:])
	_ = f.Close()
	if err != nil {
		return empty, executionError("INVALID_MANIFEST", "provider ELF 头不完整：%v", err)
	}
	if err := e.platform.validateELFHeader(header[:]); err != nil {
		return empty, err
	}
	callDir, err := os.MkdirTemp(e.workDir, "local-")
	if err != nil {
		return empty, err
	}
	defer os.RemoveAll(callDir)
	inputDir := filepath.Join(callDir, "input")
	if err := os.Mkdir(inputDir, 0700); err != nil {
		return empty, err
	}
	if in.Request.Context != nil {
		copy := *in.Request.Context
		copy.Files = append([]ProcessFile{}, copy.Files...)
		copy.Assets = append([]ProcessAsset{}, copy.Assets...)
		in.Request.Context = &copy
		if copy.OutputDirectory != nil {
			return empty, errors.New("UNSUPPORTED_EFFECT: output directory must be host generated")
		}
		filePointers := map[string]bool{}
		inputBytes := 0
		for i := range copy.Files {
			file := &copy.Files[i]
			data, ok := in.Files[file.InputPointer]
			inputBytes += len(data)
			if !ok || inputBytes > 32<<20 || filePointers[file.InputPointer] {
				return empty, errors.New("RESOURCE_NOT_AUTHORIZED: input file missing or too large")
			}
			filePointers[file.InputPointer] = true
			name := strconv.Itoa(i) + ".bin"
			if err := os.WriteFile(filepath.Join(inputDir, name), data, 0400); err != nil {
				return empty, err
			}
			file.Path = "/input/" + name
		}
		if len(filePointers) != len(in.Files) {
			return empty, errors.New("RESOURCE_NOT_AUTHORIZED: undeclared input file")
		}
		assetIDs := map[string]bool{}
		for i := range copy.Assets {
			a := &copy.Assets[i]
			if !assetIDPattern.MatchString(a.AssetID) || in.Assets[a.AssetID] == "" || assetIDs[a.AssetID] {
				return empty, errors.New("RESOURCE_NOT_AUTHORIZED: unauthorized asset")
			}
			assetIDs[a.AssetID] = true
			a.Path = "/assets/" + a.AssetID
		}
		if len(assetIDs) != len(in.Assets) {
			return empty, errors.New("RESOURCE_NOT_AUTHORIZED: undeclared asset mapping")
		}
		if in.CollectArtifacts {
			dest := "/output"
			copy.OutputDirectory = &dest
		}
	} else if in.CollectArtifacts || len(in.Files) > 0 || len(in.Assets) > 0 {
		return empty, errors.New("UNSUPPORTED_EFFECT: invoke context is required")
	}
	request, err := marshalOutboundRequest(&in.Request)
	if err != nil {
		return empty, err
	}
	if len(request) > 1<<20 {
		return empty, errors.New("INVALID_INPUT: request exceeds 1 MiB")
	}
	args := e.baseArgs()
	args = append(args, "--ro-bind", in.RuntimeDir, "/runtime", "--ro-bind", inputDir, "/input")
	for _, group := range []struct {
		source map[string]string
		dest   string
	}{{in.Assets, "/assets"}} {
		ids := make([]string, 0, len(group.source))
		for id := range group.source {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if !assetIDPattern.MatchString(id) {
				return empty, errors.New("invalid resource ID")
			}
			if _, err := confinedPath(e.root, group.source[id]); err != nil {
				return empty, err
			}
			args = append(args, "--ro-bind", group.source[id], group.dest+"/"+id)
		}
	}
	hostname, err := os.Hostname()
	if err != nil {
		return empty, err
	}
	for name, data := range map[string]string{"hosts": "127.0.0.1 localhost pogo-Skill " + hostname + "\n::1 localhost\n", "nsswitch.conf": "hosts: files\n", "passwd": fmt.Sprintf("pogo:x:%d:%d:pogo:/tmp:/bin/false\n", os.Getuid(), os.Getgid())} {
		file := filepath.Join(callDir, name)
		if err := os.WriteFile(file, []byte(data), 0400); err != nil {
			return empty, err
		}
		args = append(args, "--ro-bind", file, "/etc/"+name)
	}
	if in.CollectArtifacts {
		args = append(args, "--size", strconv.Itoa(maxArtifactTotalBytes), "--tmpfs", "/output")
	}
	args = append(args, "--remount-ro", "/", "--", "/pogo-runner", "--entrypoint", "/runtime/"+entry, "--memory-mib", strconv.Itoa(in.Limits.MemoryMiB), "--cpu-milli", strconv.Itoa(in.Limits.CPUMilliCores), "--timeout-seconds", strconv.Itoa(in.Limits.TimeoutSeconds), "--max-output-bytes", strconv.Itoa(in.Limits.MaxOutputBytes))
	if in.CollectArtifacts {
		args = append(args, "--artifacts")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(in.Limits.TimeoutSeconds+10)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.bwrap, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.Stdin = bytes.NewReader(request)
	stdout := &boundedCapture{max: 52 << 20, cancel: cancel}
	stderr := &boundedCapture{max: 64 << 10, cancel: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	if stdout.overflow || stderr.overflow {
		return empty, errors.New("OUTPUT_INVALID: local runner output exceeded limit")
	}
	if ctx.Err() != nil {
		return empty, fmt.Errorf("CANCELLED_OR_TIMEOUT: %w", ctx.Err())
	}
	if err != nil {
		return empty, fmt.Errorf("TOOL_FAILED: local sandbox: %s", strings.TrimSpace(stderr.buf.String()))
	}
	var out localResult
	if err := json.Unmarshal(stdout.buf.Bytes(), &out); err != nil || out.Protocol != localProtocol {
		return empty, errors.New("OUTPUT_INVALID: local runner protocol")
	}
	if out.Error != "" {
		return empty, errors.New(out.Error)
	}
	if len(out.Result.Output) > in.Limits.MaxOutputBytes || len(out.Result.Artifacts) > maxArtifactFiles {
		return empty, errors.New("OUTPUT_INVALID: local result bounds")
	}
	total := 0
	for _, artifact := range out.Result.Artifacts {
		if len(artifact.Data) > maxArtifactFileBytes {
			return empty, errors.New("artifact exceeds limit")
		}
		total += len(artifact.Data)
	}
	if total > maxArtifactTotalBytes {
		return empty, errors.New("artifacts exceed total limit")
	}
	return out.Result, nil
}

// Admission control reserves headroom for Pogo and tmpfs. It is not a per-call
// kernel reservation; the entire container still shares its memory ceiling.
func localMemoryBudget() int {
	for _, name := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		value, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil || value >= 1<<60 {
			continue
		}
		mib := int(value >> 20)
		return max(1, mib-max(512, mib/4))
	}
	return 0
}
