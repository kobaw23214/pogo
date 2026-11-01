package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/prefs"
	"github.com/yosukeno/pogo/tools"
)

// The regression these tests exist to pin: a cleanup function that was
// written but never called is how a spill directory grows to tens of
// gigabytes. So the wiring is what's tested — an aged file in a temp spill
// dir must actually disappear when the Manager sweeps.

// sweepEnv points the spill directory and the prefs file at temp dirs and
// returns the spill dir. Env rather than config plumbing: that is the
// channel the real process resolves them through (POGO_SPILL_DIR,
// POGO_PREFS_FILE), so this exercises the same lookup.
func sweepEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(tools.SpillDirEnv, dir)
	t.Setenv(prefs.EnvFile, filepath.Join(t.TempDir(), "prefs.json"))
	return dir
}

func sweepManager(t *testing.T, interval time.Duration) *Manager {
	t.Helper()
	t.Setenv("KIMI_API_KEY", "test-key")
	installTestCatalog(t)
	mgr, err := NewManager(Config{
		Cwd:         t.TempDir(),
		SessionDir:  t.TempDir(),
		Model:       "k3",
		MaxTurns:    5,
		GateTimeout: 5 * time.Second,
		RunTimeout:  time.Minute,
		IdleTimeout: time.Hour,
		// Zero in most tests: no goroutine, no deletes nobody planted. The
		// one wiring test below opts in explicitly.
		SpillSweepInterval: interval,
		NewClient: func(c config.Resolved, _ func(llm.RetryInfo)) llm.Client {
			return &scriptedClient{model: c.Model}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	return mgr
}

func agedSpill(t *testing.T, dir, name string, size int, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	data := make([]byte, size)
	for i := range data {
		data[i] = 'x'
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

func writePrefs(t *testing.T, days int) {
	t.Helper()
	if err := prefs.Save(prefs.Path(), prefs.Prefs{SpillRetentionDays: days}); err != nil {
		t.Fatal(err)
	}
}

// One pass, driven by hand: old goes, new stays, and the outcome is recorded
// for GET /api/prefs.
func TestSweepSpillOnceRemovesWhatRetentionSays(t *testing.T) {
	dir := sweepEnv(t)
	writePrefs(t, 7)
	mgr := sweepManager(t, 0)
	now := time.Now()
	old := agedSpill(t, dir, "bash-aaaaaa.log", 100, now.Add(-10*24*time.Hour))
	fresh := agedSpill(t, dir, "bash-bbbbbb.log", 50, now)

	mgr.sweepSpillOnce(now)

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the 10-day-old spill survived a 7-day retention sweep")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a fresh spill was swept: %v", err)
	}
	sw := mgr.LastSpillSweep()
	if sw == nil || sw.Removed != 1 || sw.Bytes != 100 {
		t.Errorf("LastSpillSweep = %+v, want one recorded removal of 100 bytes", sw)
	}
}

// The goroutine half of the wiring: a Manager configured with an interval
// sweeps at startup without anyone calling into it.
func TestManagerStartupRunsTheSweep(t *testing.T) {
	dir := sweepEnv(t)
	writePrefs(t, 7)
	now := time.Now()
	old := agedSpill(t, dir, "bash-aaaaaa.log", 10, now.Add(-30*24*time.Hour))

	sweepManager(t, time.Hour) // interval irrelevant; the startup pass is the point

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(old); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the startup sweep never removed the old spill — the goroutine is not wired")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A prefs file that cannot be read pauses the sweep. Falling back to the
// default retention here is the dangerous direction: a typo'd hand-edit
// would silently delete what the operator asked to keep.
func TestSweepSkipsWhenPrefsAreBroken(t *testing.T) {
	dir := sweepEnv(t)
	if err := os.WriteFile(prefs.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	mgr := sweepManager(t, 0)
	old := agedSpill(t, dir, "bash-aaaaaa.log", 10, time.Now().Add(-30*24*time.Hour))

	mgr.sweepSpillOnce(time.Now())

	if _, err := os.Stat(old); err != nil {
		t.Error("broken prefs did not pause the sweep: the old spill is gone")
	}
	if sw := mgr.LastSpillSweep(); sw != nil {
		t.Errorf("a paused sweep recorded a result: %+v", sw)
	}
}

// retention 0 means "never automatically", not "keep nothing".
func TestSweepSkipsWhenDisabled(t *testing.T) {
	dir := sweepEnv(t)
	writePrefs(t, 0)
	mgr := sweepManager(t, 0)
	old := agedSpill(t, dir, "bash-aaaaaa.log", 10, time.Now().Add(-30*24*time.Hour))

	mgr.sweepSpillOnce(time.Now())

	if _, err := os.Stat(old); err != nil {
		t.Error("spill_retention_days=0 deleted a file; 0 must mean disabled")
	}
	if sw := mgr.LastSpillSweep(); sw != nil {
		t.Errorf("a disabled sweep recorded a result: %+v", sw)
	}
}

func TestSweepPreservesStoredResultsDuringReleaseMaintenance(t *testing.T) {
	for _, state := range []string{"active", "malformed", "unreadable"} {
		t.Run(state, func(t *testing.T) {
			dir := sweepEnv(t)
			writePrefs(t, 7)
			now := time.Now()
			old := now.Add(-30 * 24 * time.Hour)
			body, err := json.Marshal(tools.StoredToolResult{Version: 1, Tool: "maintenance-fixture", Text: "original historical result", TextComplete: true})
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(body)
			id := "R" + hex.EncodeToString(digest[:])
			results := filepath.Join(dir, "scopes", strings.Repeat("b", 64), "results")
			if err := os.MkdirAll(filepath.Join(results, ".metadata"), 0700); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(results, "result-"+id[1:]+".log")
			metadataPath := filepath.Join(results, ".metadata", id+".json")
			metadata, err := json.Marshal(map[string]any{"version": 1, "result_id": id, "created_at": old, "updated_at": old, "lease_until": old.Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			for name, data := range map[string][]byte{log: body, metadataPath: metadata} {
				if err := os.WriteFile(name, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(name, old, old); err != nil {
					t.Fatal(err)
				}
			}

			parent := t.TempDir()
			mgr := &Manager{cfg: Config{SessionDir: filepath.Join(parent, "sessions")}}
			marker := mgr.releaseMaintenancePath()
			removeMarker := func() error { return os.Remove(marker) }
			switch state {
			case "active":
				if err := os.WriteFile(marker, []byte(`{"token":"`+strings.Repeat("a", 64)+`"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(marker, []byte(`{not valid JSON`), 0600); err != nil {
					t.Fatal(err)
				}
			case "unreadable":
				// A non-directory ancestor makes opening the marker fail even
				// when tests run as root and chmod(000) would still be readable.
				blocked := filepath.Join(parent, "blocked")
				if err := os.WriteFile(blocked, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
				mgr.cfg.SessionDir = filepath.Join(blocked, "sessions")
				removeMarker = func() error { return os.Remove(blocked) }
			}
			previous := SpillSweep{Time: now.Add(-time.Minute), Removed: 2, Bytes: 17}
			mgr.lastSweep = &previous
			mgr.sweepSpillOnce(now)
			for name, want := range map[string][]byte{log: body, metadataPath: metadata} {
				got, err := os.ReadFile(name)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("maintenance changed historical spill %s: %v", filepath.Base(name), err)
				}
				info, err := os.Stat(name)
				if err != nil || !info.ModTime().Equal(old) {
					t.Fatalf("maintenance changed historical spill timestamp: %v", err)
				}
			}
			if got := mgr.LastSpillSweep(); got == nil || *got != previous {
				t.Fatalf("maintenance updated the last completed sweep: %+v", got)
			}

			if err := removeMarker(); err != nil {
				t.Fatal(err)
			}
			mgr.sweepSpillOnce(now)
			for _, name := range []string{log, metadataPath} {
				if _, err := os.Stat(name); !os.IsNotExist(err) {
					t.Fatalf("normal retention did not remove expired spill %s: %v", filepath.Base(name), err)
				}
			}
			if got := mgr.LastSpillSweep(); got == nil || !got.Time.Equal(now) || got.Removed != 1 || got.Bytes != int64(len(body)) {
				t.Fatalf("normal retention result changed after maintenance: %+v", got)
			}
		})
	}
}
