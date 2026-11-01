//go:build linux

package sentinelexec

// This opt-in probe exercises the real Linux sandbox, Python SDK and durable
// store. Its generated observations are a test workload, not a source adapter
// or a claim that a real model authored the script.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/sentinelsdk"
)

func TestLinuxSentinelSDKSoak(t *testing.T) {
	if os.Getenv("SENTINEL_SDK_SOAK") != "1" {
		t.Skip("opt-in isolated Linux SDK/store soak; no real model calls")
	}
	seconds := 600
	if raw := os.Getenv("SENTINEL_SOAK_SECONDS"); raw != "" {
		var err error
		seconds, err = strconv.Atoi(raw)
		if err != nil || seconds < 60 || seconds > 900 {
			t.Fatal("soak duration must be 60-900 seconds")
		}
	}
	dir, workspace := t.TempDir(), t.TempDir()
	marker := "pogo-sdk-soak-" + filepath.Base(dir)
	entry := filepath.Join(dir, "main.py")
	code := `import subprocess,sys,time
from pogo_watch import Watcher,event
# This child deliberately outlives its parent unless the sandbox kills the
# complete process tree. The marker is synthetic and used only by this probe.
subprocess.Popen([sys.executable, "-c", "import time;time.sleep(3600)", sys.argv[1]])
with Watcher(mode="stream") as w:
    n = (w.cursor or {}).get("n", 0)
    w.checkpoint(state={"n":n}, cursor={"n":n})
    if n >= 10:
        prior = n // 10 * 10
        w.emit([event("observation:"+str(prior), "synthetic important observation", {"n":prior})], state={"n":n}, cursor={"n":n})
    while True:
        n += 1
        events = [event("observation:"+str(n), "synthetic important observation", {"n":n})] if n % 10 == 0 else []
        w.emit(events, state={"n":n}, cursor={"n":n})
        if n % 25 == 0:
            w.emit(events, state={"n":n}, cursor={"n":n})
        time.sleep(0.1)
`
	if err := os.WriteFile(entry, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sentinelsdk.FileName), sentinelsdk.Source(), 0600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "events.db")
	db, err := sentinel.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	w, err := db.Create(sentinel.Spec{OwnerID: "isolated-soak", AgentID: "synthetic-agent", Workspace: workspace, ScriptDir: dir, Command: []string{"/opt/venv/bin/python3", "-u", entry, marker}, Title: "SDK sandbox soak", Task: "test-only synthetic workload", Mode: "stream", TimeoutSeconds: 30, MaxTurns: 10, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	deadline := started.Add(time.Duration(seconds) * time.Second)
	var metricsMu sync.Mutex
	var peakRSS uint64
	var peakProcesses, samples int
	frames, maxN, restarts, generationsFenced := 0, 0, 0, 0
	for time.Now().Before(deadline) {
		w, err = db.Get(w.Spec.ID)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		target := maxN + 300
		runner := &Runner{monitor: func(ctx context.Context, pid int, cfg Config) error {
			sampleCtx, stop := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				ticker := time.NewTicker(200 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-sampleCtx.Done():
						return
					case <-ticker.C:
						ps, e := processTree(pid)
						if e != nil {
							continue
						}
						var rss uint64
						for _, p := range ps {
							rss += p.rss
						}
						metricsMu.Lock()
						peakRSS = max(peakRSS, rss)
						peakProcesses = max(peakProcesses, len(ps))
						samples++
						metricsMu.Unlock()
					}
				}
			}()
			e := monitorResources(ctx, pid, cfg)
			stop()
			<-done
			return e
		}}
		callbackErr := error(nil)
		err = runner.Run(ctx, Config{Command: w.Spec.Command, ScriptDir: dir, Workspace: workspace, Mode: "stream", State: w.State, Cursor: w.Cursor}, func(f sentinel.Frame) error {
			if e := db.Accept(w.Spec.ID, w.Spec.Generation, f); e != nil {
				callbackErr = e
				return e
			}
			var cursor struct {
				N int `json:"n"`
			}
			if e := json.Unmarshal(f.Cursor, &cursor); e != nil || cursor.N < maxN || cursor.N > maxN+1 {
				callbackErr = fmt.Errorf("progress changed unexpectedly: n=%d previous=%d decode=%v", cursor.N, maxN, e)
				return callbackErr
			}
			frames++
			maxN = cursor.N
			if maxN >= target {
				// Inject host cancellation after durable commit, before this
				// callback returns and the runner can write the matching ACK.
				cancel()
			}
			return nil
		})
		cancel()
		if callbackErr != nil {
			t.Fatal(callbackErr)
		}
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("sandbox runner stopped unexpectedly: %v", err)
		}
		// A child retained outside the killed sandbox would appear with the
		// unique marker in the parent PID namespace. Do not trust root exit.
		soakAssertNoMarkedProcess(t, marker)
		oldGeneration := w.Spec.Generation
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
		db, err = sentinel.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.Recover(); err != nil {
			t.Fatal(err)
		}
		w, err = db.Get(w.Spec.ID)
		if err != nil {
			t.Fatal(err)
		}
		if w.Spec.Generation <= oldGeneration || w.Pending != maxN/10 || string(w.Cursor) != fmt.Sprintf(`{"n":%d}`, maxN) {
			t.Fatalf("restart lost progress, queue or fencing: generation=%d old=%d pending=%d n=%d cursor=%s", w.Spec.Generation, oldGeneration, w.Pending, maxN, w.Cursor)
		}
		stale := sentinel.Frame{V: 1, ID: "retired-generation-probe", Cursor: json.RawMessage(`{"n":999999}`)}
		if e := db.Accept(w.Spec.ID, oldGeneration, stale); !errors.Is(e, sentinel.ErrStaleGeneration) {
			t.Fatalf("retired collector could advance progress: %v", e)
		}
		generationsFenced++
		restarts++
		t.Logf("progress elapsed=%s frames=%d n=%d pending=%d restarts=%d", time.Since(started).Round(time.Second), frames, maxN, w.Pending, restarts)
	}
	seen := map[string]bool{}
	for {
		batch, e := db.ClaimBatch(w.Spec.ID, sentinel.MaxBatchEvents)
		if e != nil {
			t.Fatal(e)
		}
		if batch == nil {
			break
		}
		for _, event := range batch.Events {
			if seen[event.Key] {
				t.Fatalf("replay produced duplicate event: %s", event.Key)
			}
			seen[event.Key] = true
		}
		if e = db.SettleBatch(batch.ID, "ignore", "synthetic soak verification; no model invoked", 0); e != nil {
			t.Fatal(e)
		}
	}
	for n := 10; n <= maxN; n += 10 {
		if !seen[fmt.Sprintf("observation:%d", n)] {
			t.Fatalf("missing durable observation %d", n)
		}
	}
	w, err = db.SetEnabled(w.Spec.ID, false)
	if err != nil || w.Pending != 0 || len(seen) != maxN/10 || restarts < 2 || frames < 100 {
		t.Fatalf("final soak state: pending=%d events=%d n=%d restarts=%d frames=%d err=%v", w.Pending, len(seen), maxN, restarts, frames, err)
	}
	metricsMu.Lock()
	metrics := map[string]any{"duration_seconds": time.Since(started).Seconds(), "frames": frames, "confirmed_source_position": maxN, "unique_events": len(seen), "store_reopens": restarts, "retired_generations_rejected": generationsFenced, "process_samples": samples, "peak_process_tree_rss_bytes": peakRSS, "peak_process_tree_count": peakProcesses, "model_calls": 0, "pending": w.Pending, "status": w.Status, "scope": "controlled Python SDK + real Linux sandbox + persistent store; not autonomous model authoring, full web supervisor or production long-term acceptance"}
	metricsMu.Unlock()
	if out := os.Getenv("SENTINEL_SOAK_OUT"); out != "" {
		data, _ := json.MarshalIndent(metrics, "", "  ")
		if err := os.WriteFile(out, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("soak metrics: %+v", metrics)
}

func soakAssertNoMarkedProcess(t *testing.T, marker string) {
	t.Helper()
	for attempts := 0; attempts < 20; attempts++ {
		entries, err := os.ReadDir("/proc")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range entries {
			if _, err := strconv.Atoi(entry.Name()); err != nil {
				continue
			}
			raw, _ := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
			found = found || strings.Contains(string(raw), marker)
		}
		if !found {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("sandbox cancellation left a marked descendant alive")
}
