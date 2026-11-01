package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func shellFixtureRunner(t *testing.T, dir string) *ShellJobRunner {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("managed shell task storage requires Unix process-safe leases")
	}
	r, err := NewShellJobRunner(context.Background(), dir, ShellJobHooks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}

func shellFixtureRecord(t *testing.T, r *ShellJobRunner, status, output string) ShellJobRecord {
	t.Helper()
	record := ShellJobRecord{TaskID: "shell_00112233445566778899aabbccddeeff", Command: "this fixture must never execute", Status: status, StartedAt: time.Now().UnixMilli(), Revision: 1, Background: true}
	f, err := r.root.OpenFile(record.TaskID+".log", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(output); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	disk := shellJobDisk{Version: 1, ShellJobRecord: record}
	if err := r.save(disk); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	close(done)
	r.jobs[record.TaskID] = &shellJob{disk: disk, done: done}
	return record
}

func TestShellRecoveryRetainsOutputAndAcknowledgesExactNotification(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "jobs")
	r := shellFixtureRunner(t, dir)
	record := shellFixtureRecord(t, r, "running", "已有输出：你好\n")
	r.Close()
	recovered := shellFixtureRunner(t, dir)
	got, err := recovered.Get(record.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "interrupted" || got.Revision != 2 || got.ExitCode != nil || got.OutputBytes != int64(len("已有输出：你好\n")) {
		t.Fatalf("unexpected recovery: %+v", got)
	}
	if !strings.Contains(got.Error, "has not been rerun") {
		t.Fatalf("missing unknown-outcome recovery notice: %q", got.Error)
	}
	if len(recovered.PendingNotifications()) != 1 {
		t.Fatal("recovered completion must remain pending")
	}
	if err := recovered.AckNotification(got.TaskID, 1); !errors.Is(err, ErrShellJobConflict) {
		t.Fatalf("stale acknowledgement: %v", err)
	}
	if err := recovered.AckNotification(got.TaskID, got.Revision); err != nil {
		t.Fatal(err)
	}
	recovered.Close()
	final := shellFixtureRunner(t, dir)
	if len(final.PendingNotifications()) != 0 {
		t.Fatal("acknowledgement did not survive reopening")
	}
}

func TestShellOutputPreservesUnicodeWithOneBytePageAndRejectsForeignIDs(t *testing.T) {
	r := shellFixtureRunner(t, filepath.Join(t.TempDir(), "a"))
	record := shellFixtureRecord(t, r, "completed", "你好abc")
	first, err := r.Output(record.TaskID, 0, 1)
	if err != nil || first.Output != "你" || first.NextOffset != 3 {
		t.Fatalf("first page: %+v, %v", first, err)
	}
	second, err := r.Output(record.TaskID, first.NextOffset, 1)
	if err != nil || second.Output != "好" || second.NextOffset != 6 {
		t.Fatalf("second page: %+v, %v", second, err)
	}
	if _, err := r.Output(record.TaskID, 1, 8); err == nil {
		t.Fatal("must reject an offset inside a character")
	}
	other := shellFixtureRunner(t, filepath.Join(t.TempDir(), "b"))
	if _, err := other.Output(record.TaskID, 0, 8); !errors.Is(err, ErrShellJobNotFound) {
		t.Fatalf("foreign task: %v", err)
	}
}

func TestShellRawTailKeepsUTF8SplitAcrossWrites(t *testing.T) {
	r := shellFixtureRunner(t, filepath.Join(t.TempDir(), "jobs"))
	id := "shell_00112233445566778899aabbccddeeff"
	f, err := r.root.OpenFile(id+".log", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	job := &shellJob{disk: shellJobDisk{Version: 1, ShellJobRecord: ShellJobRecord{TaskID: id, Status: "running", Revision: 1}}, log: f, cancel: func() {}}
	writer := &shellJobWriter{runner: r, job: job}
	bytes := []byte("你")
	if _, err := writer.Write(bytes[:1]); err != nil {
		t.Fatal(err)
	}
	if job.disk.OutputTail != "" {
		t.Fatalf("partial character escaped into preview: %q", job.disk.OutputTail)
	}
	if _, err := writer.Write(bytes[1:]); err != nil {
		t.Fatal(err)
	}
	if job.disk.OutputTail != "你" || job.disk.OutputBytes != 3 {
		t.Fatalf("split character was damaged: %+v", job.disk.ShellJobRecord)
	}
}

func TestShellHelpersFollowBashSelectionAndRunningReceiptHasNoExitCode(t *testing.T) {
	jobs := &ShellJobRunner{}
	selection := []string{"bash"}
	registry := New(Options{Cwd: t.TempDir(), Enabled: &selection, ShellJobs: jobs})
	for _, name := range []string{ShellTasksName, ShellOutputName, ShellStopName} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("missing Bash helper %s", name)
		}
	}
	selection = []string{ShellTasksName}
	next, apply, err := registry.WithNativeSelection(&selection, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply()
	for _, name := range []string{"bash", ShellTasksName, ShellOutputName, ShellStopName} {
		if _, ok := next.Get(name); ok {
			t.Fatalf("helper selection granted %s", name)
		}
	}
	for _, schema := range []map[string]any{(&Bash{}).InputSchema(), (&Bash{}).Schema()} {
		props := schema["properties"].(map[string]any)
		if _, ok := props["run_in_background"]; ok {
			t.Fatal("foreground-only Bash advertises background support")
		}
	}
	data, err := json.Marshal(BashDetails{TaskID: "shell_fixture", Background: true, Status: "running"})
	if err != nil || strings.Contains(string(data), "exit_code") {
		t.Fatalf("running receipt fabricated an exit: %s, %v", data, err)
	}
	data, err = json.Marshal(BashDetails{ExitCode: 0})
	if err != nil || !strings.Contains(string(data), `"exit_code":0`) {
		t.Fatalf("completed zero exit missing: %s, %v", data, err)
	}
}

// The release file makes this deterministic: completion cannot win the yield
// race, and the marker detects an implementation that restarts on handoff.
func TestBashYieldRunsOnceAndDetachesOriginalStream(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash process fixture requires a Unix shell")
	}
	completed := make(chan ShellJobRecord, 1)
	runner, err := NewShellJobRunner(context.Background(), filepath.Join(t.TempDir(), "jobs"), ShellJobHooks{OnComplete: func(record ShellJobRecord) { completed <- record }})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	cwd := t.TempDir()
	bash := &Bash{Cwd: cwd, SpillDir: t.TempDir(), Timeout: 2 * time.Second, Jobs: runner}
	var returned atomic.Bool
	var lateCallbacks atomic.Int64
	result, err := bash.ExecuteStreaming(context.Background(), json.RawMessage(`{"command":"printf once >> marker; printf 'before\\n'; while [ ! -f release ]; do sleep 0.01; done; printf 'after\\n'","yield_after":0.001}`), func(Partial) {
		if returned.Load() {
			lateCallbacks.Add(1)
		}
	})
	returned.Store(true)
	if err != nil {
		t.Fatal(err)
	}
	details, ok := result.Details.(BashDetails)
	if !ok || !details.Background || details.TaskID == "" {
		t.Fatalf("expected a handoff receipt, got %+v", result)
	}
	if err := os.WriteFile(filepath.Join(cwd, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case record := <-completed:
		if record.Status != "completed" {
			t.Fatalf("unexpected command result: %+v", record)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("background completion was not delivered")
	}
	marker, err := os.ReadFile(filepath.Join(cwd, "marker"))
	if err != nil || string(marker) != "once" {
		t.Fatalf("command was lost or repeated: %q, %v", marker, err)
	}
	if lateCallbacks.Load() != 0 {
		t.Fatal("original streaming callback was used after the tool returned")
	}
	output, err := runner.Output(details.TaskID, 0, 1024)
	if err != nil || output.Output != "before\nafter\n" {
		t.Fatalf("full task log was lost: %+v, %v", output, err)
	}
}

func TestShellStopContextTimeoutKeepsState(t *testing.T) {
	r := shellFixtureRunner(t, filepath.Join(t.TempDir(), "jobs"))
	id := "shell_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	job := &shellJob{disk: shellJobDisk{Version: 1, ShellJobRecord: ShellJobRecord{TaskID: id, Status: "queued", Revision: 1, Background: true}}, cancel: func() {}, done: make(chan struct{})}
	r.jobs[id] = job
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := r.StopContext(ctx, id, "stop"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop timeout = %v, want context deadline", err)
	}
	record, err := r.Get(id)
	if err != nil || record.Status != "queued" {
		t.Fatalf("timeout fabricated a terminal state: %+v, %v", record, err)
	}
	close(job.done)
}

func TestShellCloseContextTimeoutReleasesStorageAfterWorkers(t *testing.T) {
	r := shellFixtureRunner(t, filepath.Join(t.TempDir(), "jobs"))
	r.wg.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := r.CloseContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close timeout = %v, want context deadline", err)
	}
	r.wg.Done()
	select {
	case <-r.closeDone:
	case <-time.After(time.Second):
		t.Fatal("storage was not released after the worker settled")
	}
}

func writeShellRetentionTask(t *testing.T, r *ShellJobRunner, id, status string, ended int64, pending bool, bytes int64) {
	t.Helper()
	f, err := r.root.OpenFile(id+".log", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("log"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	disk := shellJobDisk{Version: 1, ShellJobRecord: ShellJobRecord{TaskID: id, Status: status, EndedAt: ended, OutputBytes: bytes, Revision: 1, Background: true}, NotificationPending: pending}
	if err := r.save(disk); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	close(done)
	r.jobs[id] = &shellJob{disk: disk, done: done}
}

func TestShellRetentionPrunesOnlyAcknowledged(t *testing.T) {
	r, err := NewShellJobRunner(context.Background(), filepath.Join(t.TempDir(), "jobs"), ShellJobHooks{}, ShellJobOptions{Retention: ShellRetentionPolicy{MaxTasks: 1, MaxAge: time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	now := time.Now()
	old := now.Add(-time.Hour).UnixMilli()
	writeShellRetentionTask(t, r, "shell_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "completed", old, false, 100)
	writeShellRetentionTask(t, r, "shell_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "completed", old, true, 200)
	writeShellRetentionTask(t, r, "shell_cccccccccccccccccccccccccccccccc", "running", 0, false, 1000)
	writeShellRetentionTask(t, r, "shell_dddddddddddddddddddddddddddddddd", "completed", now.UnixMilli(), false, 200)
	if err := r.prune(now); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get("shell_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); !errors.Is(err, ErrShellJobNotFound) {
		t.Fatalf("expired acknowledged task was retained: %v", err)
	}
	for _, id := range []string{"shell_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "shell_cccccccccccccccccccccccccccccccc", "shell_dddddddddddddddddddddddddddddddd"} {
		if _, err := r.Get(id); err != nil {
			t.Fatalf("protected task %s was pruned: %v", id, err)
		}
	}
	if got := len(r.PendingNotifications()); got != 1 {
		t.Fatalf("pending notification count = %d, want 1", got)
	}
}

func TestShellRetentionRespectsDiskBudget(t *testing.T) {
	r, err := NewShellJobRunner(context.Background(), filepath.Join(t.TempDir(), "jobs"), ShellJobHooks{}, ShellJobOptions{Retention: ShellRetentionPolicy{MaxBytes: 1024}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	now := time.Now().UnixMilli()
	writeShellRetentionTask(t, r, "shell_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "completed", now-2, false, 1000)
	writeShellRetentionTask(t, r, "shell_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "completed", now-1, false, 1000)
	if err := r.prune(time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := len(r.List(false)); got != 1 {
		t.Fatalf("records after disk-budget prune = %d, want 1", got)
	}
	if _, err := r.Get("shell_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); err != nil {
		t.Fatalf("newest record was not retained: %v", err)
	}
}

func TestShellOutputBatchesRefreshes(t *testing.T) {
	r := shellFixtureRunner(t, filepath.Join(t.TempDir(), "jobs"))
	id := "shell_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	f, err := r.root.OpenFile(id+".log", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	job := &shellJob{disk: shellJobDisk{Version: 1, ShellJobRecord: ShellJobRecord{TaskID: id, Status: "running", Revision: 1, Background: true}}, log: f, cancel: func() {}}
	writer := &shellJobWriter{runner: r, job: job}
	if _, err := writer.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if job.savedBytes != 1 {
		t.Fatalf("first output was not flushed: %d", job.savedBytes)
	}
	if _, err := writer.Write([]byte("b")); err != nil {
		t.Fatal(err)
	}
	if job.savedBytes != 1 {
		t.Fatalf("small output caused another immediate sync: %d", job.savedBytes)
	}
	job.lastUpdate = time.Now().Add(-2 * time.Second)
	if _, err := writer.Write([]byte("c")); err != nil {
		t.Fatal(err)
	}
	if job.savedBytes != 3 {
		t.Fatalf("periodic flush did not persist output: %d", job.savedBytes)
	}
	if _, err := writer.Write(make([]byte, 256<<10)); err != nil {
		t.Fatal(err)
	}
	if job.savedBytes != 3+256<<10 || job.disk.OutputBytes != 3+256<<10 {
		t.Fatalf("large flush lost bytes: saved=%d total=%d", job.savedBytes, job.disk.OutputBytes)
	}
}
