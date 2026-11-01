//go:build linux

package sentinelexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type procSample struct {
	pid, parent, threads int
	rss, ticks           uint64
}

func parseProcStat(pid int, raw []byte) (procSample, error) {
	p := procSample{pid: pid}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return p, errors.New("invalid proc stat")
	}
	f := strings.Fields(string(raw[end+1:]))
	if len(f) < 22 {
		return p, errors.New("incomplete proc stat")
	}
	var err error
	if p.parent, err = strconv.Atoi(f[1]); err != nil {
		return p, err
	}
	if p.threads, err = strconv.Atoi(f[17]); err != nil {
		return p, err
	}
	for _, i := range []int{11, 12, 13, 14} {
		v, err := strconv.ParseUint(f[i], 10, 64)
		if err != nil {
			return p, err
		}
		p.ticks += v
	}
	rss, err := strconv.ParseUint(f[21], 10, 64)
	p.rss = rss * uint64(os.Getpagesize())
	return p, err
}

// Read the launcher's subtree in the parent namespace. The private PID
// namespace keeps daemonized descendants parented to its reaper. This is
// sampled supervision: short spikes remain covered only by container limits.
func processTree(root int) ([]procSample, error) {
	dirs, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	all := make(map[int]procSample)
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil || pid <= 0 {
			continue
		}
		raw, err := os.ReadFile("/proc/" + d.Name() + "/stat")
		if err != nil {
			if pid == root && !os.IsNotExist(err) {
				return nil, err
			}
			continue
		}
		p, err := parseProcStat(pid, raw)
		if err != nil {
			return nil, err
		}
		all[pid] = p
	}
	if _, ok := all[root]; !ok {
		return nil, nil // exited between samples
	}
	known := map[int]bool{root: true}
	for changed := true; changed; {
		changed = false
		for id, p := range all {
			if !known[id] && known[p.parent] {
				known[id], changed = true, true
			}
		}
	}
	out := make([]procSample, 0, len(known))
	for id := range known {
		out = append(out, all[id])
	}
	return out, nil
}

func monitorResources(ctx context.Context, root int, cfg Config) error {
	auxv, err := unix.Auxv()
	if err != nil {
		return fmt.Errorf("sentinel resource monitor unavailable: %w", err)
	}
	var clockTicks uint64
	for _, p := range auxv {
		if p[0] == 17 { // AT_CLKTCK, kernel-specific on both ARM64 and AMD64
			clockTicks = uint64(p[1])
		}
	}
	if clockTicks == 0 {
		return errors.New("sentinel CPU clock frequency unavailable")
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	lastSample := time.Now()
	var lastCPU uint64
	credit := 2.0 // bounded startup/burst allowance in CPU seconds
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			ps, err := processTree(root)
			if err != nil {
				return fmt.Errorf("sentinel resource monitor failed: %w", err)
			}
			if len(ps) == 0 {
				return nil
			}
			var memory, cpu uint64
			threads := 0
			for _, p := range ps {
				memory += p.rss
				cpu += p.ticks
				threads += p.threads
			}
			if memory > uint64(cfg.MemoryMiB)<<20 {
				return errors.New("sentinel supervised RSS limit exceeded")
			}
			if len(ps) > cfg.MaxProcesses || threads > cfg.MaxProcesses {
				return errors.New("sentinel supervised process/thread limit exceeded")
			}
			// A small burst accommodates interpreter/module startup. Sources
			// consuming sustained CPU beyond their grant fail visibly rather
			// than silently contending forever with foreground Agent work.
			now := time.Now()
			credit = min(2, credit+now.Sub(lastSample).Seconds()*float64(cfg.CPUMilliCores)/1000)
			if cpu >= lastCPU {
				credit -= float64(cpu-lastCPU) / float64(clockTicks)
			}
			lastCPU, lastSample = cpu, now
			if credit < 0 {
				return errors.New("sentinel supervised CPU budget exceeded")
			}
		}
	}
}
