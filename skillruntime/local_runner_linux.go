//go:build linux

package skillruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// RunLocalRunner is only entered as PID 1 of a newly created PID namespace.
// This guard is essential: kill(-1) must never target Pogo's own namespace.
func RunLocalRunner(args []string) int {
	if os.Getpid() != 1 || os.Getppid() != 0 {
		fmt.Fprintln(os.Stderr, "Skill runner requires a private PID namespace as PID 1")
		return 1
	}
	flags := flag.NewFlagSet("pogo-skill-runner", flag.ContinueOnError)
	probe := flags.Bool("probe", false, "namespace handshake")
	entry := flags.String("entrypoint", "", "verified provider path")
	memory := flags.Int("memory-mib", 0, "supervised RSS limit")
	cpu := flags.Int("cpu-milli", 0, "supervised CPU rate")
	timeout := flags.Int("timeout-seconds", 0, "wall clock timeout")
	output := flags.Int("max-output-bytes", 0, "stdout limit")
	artifacts := flags.Bool("artifacts", false, "collect output")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return 1
	}
	clockTicks, err := localClockTicks()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *probe {
		// /proc must describe this PID namespace, not the outer container.
		if target, err := os.Readlink("/proc/self"); err != nil || target != "1" {
			return 1
		}
		_ = json.NewEncoder(os.Stdout).Encode(localResult{Protocol: localProtocol})
		return 0
	}
	limits := ExecutorLimits{TimeoutSeconds: *timeout, MemoryMiB: *memory, CPUMilliCores: *cpu, MaxOutputBytes: *output}
	if !validLimits(limits) || hostCeiling(LocalProfile, structLimits(limits)) != nil || !strings.HasPrefix(*entry, "/runtime/") || filepath.Clean(*entry) != *entry {
		return 1
	}
	// These inherited rlimits are hard limits. Memory/CPU/process-count control
	// below is explicitly sampled supervision, never advertised as a cgroup.
	if err := syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{}); err != nil {
		return 1
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{Cur: 1024, Max: 1024}); err != nil {
		return 1
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 512 << 20, Max: 512 << 20}); err != nil {
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(limits.TimeoutSeconds)*time.Second)
	defer cancel()
	defer syscall.Kill(-1, syscall.SIGKILL)
	result, err := runLocalProvider(ctx, *entry, limits, *artifacts, clockTicks)
	reply := localResult{Protocol: localProtocol, Result: result}
	if err != nil {
		reply.Result = DetailedResult{}
		reply.Error = err.Error()
	}
	if json.NewEncoder(os.Stdout).Encode(reply) != nil {
		return 1
	}
	return 0
}

func runLocalProvider(parent context.Context, entry string, limits ExecutorLimits, artifacts bool, clockTicks uint64) (DetailedResult, error) {
	var empty DetailedResult
	request, err := io.ReadAll(io.LimitReader(os.Stdin, (1<<20)+1))
	if err != nil || len(request) > 1<<20 {
		return empty, errors.New("INVALID_INPUT: process request too large")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stdout := &boundedCapture{max: limits.MaxOutputBytes, cancel: cancel}
	stderr := &boundedCapture{max: 64 << 10, cancel: cancel}
	cmd := exec.CommandContext(ctx, entry)
	cmd.Dir = "/tmp"
	threads := strconv.Itoa(max(1, (limits.CPUMilliCores+999)/1000))
	cmd.Env = append(os.Environ(), "OMP_NUM_THREADS="+threads, "OPENBLAS_NUM_THREADS="+threads, "MKL_NUM_THREADS="+threads, "JAVA_TOOL_OPTIONS=-XX:ActiveProcessorCount="+threads)
	cmd.Stdin = bytes.NewReader(request)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return empty, fmt.Errorf("TOOL_FAILED: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	monitor := time.NewTicker(100 * time.Millisecond)
	defer monitor.Stop()
	started := time.Now()
	var runErr error
	finished := false
loop:
	for {
		select {
		case runErr = <-done:
			finished = true
			break loop
		case <-ctx.Done():
			runErr = fmt.Errorf("CANCELLED_OR_TIMEOUT: %w", ctx.Err())
			break loop
		case <-monitor.C:
			usage, err := namespaceUsage()
			if err != nil {
				runErr = fmt.Errorf("RESOURCE_MONITOR_FAILED: %w", err)
				break loop
			}
			if usage.memory > uint64(limits.MemoryMiB)*(1<<20) {
				runErr = errors.New("MEMORY_LIMIT_EXCEEDED: supervised RSS limit")
				break loop
			}
			if usage.threads > 128 {
				runErr = errors.New("PROCESS_LIMIT_EXCEEDED: supervised thread limit")
				break loop
			}
			// Use the kernel's clock tick frequency on both supported
			// architectures. This is sampled supervision, not a cgroup quota.
			cpuSeconds := float64(usage.ticks) / float64(clockTicks)
			target := time.Duration(cpuSeconds * float64(time.Second) * 1000 / float64(limits.CPUMilliCores))
			pause := target - time.Since(started) - 100*time.Millisecond
			if pause > 0 {
				_ = syscall.Kill(-1, syscall.SIGSTOP)
				timer := time.NewTimer(min(pause, 200*time.Millisecond))
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					runErr = ctx.Err()
					_ = syscall.Kill(-1, syscall.SIGCONT)
					break loop
				case runErr = <-done:
					timer.Stop()
					finished = true
					_ = syscall.Kill(-1, syscall.SIGCONT)
					break loop
				}
				_ = syscall.Kill(-1, syscall.SIGCONT)
			}
		}
	}
	// Stop all descendants before reading /output. PID 1 is excluded from
	// kill(-1); the parent process is not visible in this PID namespace.
	_ = syscall.Kill(-1, syscall.SIGKILL)
	cancel()
	if !finished {
		<-done
	}
	if err := reapLocalChildren(); err != nil {
		return empty, err
	}
	if stdout.overflow || stderr.overflow {
		return empty, errors.New("OUTPUT_INVALID: provider output exceeds limit")
	}
	if runErr != nil {
		return empty, fmt.Errorf("TOOL_FAILED: %v: %s", runErr, strings.TrimSpace(stderr.buf.String()))
	}
	result := DetailedResult{Output: append([]byte(nil), stdout.buf.Bytes()...)}
	if artifacts {
		result.Artifacts, err = collectLocalArtifacts()
		if err != nil {
			return empty, err
		}
	}
	return result, nil
}

func localClockTicks() (uint64, error) {
	auxv, err := unix.Auxv()
	if err != nil {
		return 0, fmt.Errorf("RESOURCE_MONITOR_UNAVAILABLE: kernel auxiliary vector: %w", err)
	}
	return clockTicksFromAuxv(auxv)
}

func clockTicksFromAuxv(auxv [][2]uintptr) (uint64, error) {
	const atClockTicks = 17 // Linux AT_CLKTCK, in clock ticks per second.
	for _, pair := range auxv {
		if pair[0] == atClockTicks && pair[1] > 0 && pair[1] <= 1000000 {
			return uint64(pair[1]), nil
		}
	}
	return 0, errors.New("RESOURCE_MONITOR_UNAVAILABLE: kernel clock tick frequency is unavailable")
}

type localUsage struct {
	memory, ticks uint64
	threads       int
}

func namespaceUsage() (localUsage, error) {
	var total localUsage
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return total, err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid < 1 {
			continue
		}
		raw, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return total, err
		}
		end := strings.LastIndexByte(string(raw), ')')
		if end < 0 {
			return total, errors.New("invalid proc stat")
		}
		fields := strings.Fields(string(raw[end+1:]))
		if len(fields) < 22 {
			return total, errors.New("incomplete proc stat")
		}
		for _, index := range []int{11, 12, 13, 14} {
			v, err := strconv.ParseUint(fields[index], 10, 64)
			if err != nil {
				return total, err
			}
			if pid != 1 || index >= 13 {
				total.ticks += v
			}
		}
		if pid == 1 {
			continue
		}
		rss, err := strconv.ParseUint(fields[21], 10, 64)
		if err != nil {
			return total, err
		}
		threads, err := strconv.Atoi(fields[17])
		if err != nil {
			return total, err
		}
		total.memory += rss * uint64(os.Getpagesize())
		total.threads += threads
	}
	return total, nil
}

func reapLocalChildren() error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if errors.Is(err, syscall.ECHILD) {
			return nil
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if pid == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	return errors.New("EXECUTOR_CLEANUP_FAILED: process tree did not exit")
}

func collectLocalArtifacts() ([]CollectedArtifact, error) {
	var result []CollectedArtifact
	var total int64
	entries := 0
	err := filepath.WalkDir("/output", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > 4096 {
			return errors.New("OUTPUT_INVALID: too many artifact entries")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("OUTPUT_INVALID: artifact must be a regular file")
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
			return errors.New("OUTPUT_INVALID: hardlinked artifact")
		}
		if info.Size() > maxArtifactFileBytes || total+info.Size() > maxArtifactTotalBytes || len(result) >= maxArtifactFiles {
			return errors.New("OUTPUT_INVALID: artifact size/count limit")
		}
		rel, err := filepath.Rel("/output", name)
		if err != nil {
			return err
		}
		rel, err = cleanArtifactPath(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(f, maxArtifactFileBytes+1))
		_ = f.Close()
		if err != nil {
			return err
		}
		if int64(len(data)) != info.Size() {
			return errors.New("OUTPUT_INVALID: artifact changed during collection")
		}
		total += int64(len(data))
		result = append(result, CollectedArtifact{Path: rel, Data: data})
		return nil
	})
	return result, err
}
