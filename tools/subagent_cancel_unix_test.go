//go:build unix

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Mirrors Pogo's signal context and Bash's independent process group. A direct
// SIGKILL of the Pogo group cannot reach this shell; graceful cancellation can.
func TestSubagentCancellableProcessHelper(t *testing.T) {
	if os.Getenv("POGO_TEST_CANCELLABLE_CHILD") != "1" {
		return
	}
	if err := AdoptSubagentEditLease(); err != nil {
		t.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `echo $$ > "$POGO_TEST_BASH_PID"; sleep 30`)
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return KillGroup(cmd) }
	_ = cmd.Run()
	if ctx.Err() == nil {
		t.Fatal("child did not receive cancellation")
	}
	if err := os.WriteFile(os.Getenv("POGO_TEST_CLEANUP_MARKER"), []byte("cleaned"), 0600); err != nil {
		t.Fatal(err)
	}
	fmt.Println(`{"type":"run_end","error":"cancelled"}`)
}

func waitSubagentFixtureFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return b
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child fixture did not create %s", path)
	return nil
}

func TestSubagentCancellationStopsIndependentShellGroupAndReleasesLease(t *testing.T) {
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidfile, marker := filepath.Join(dir, "bash.pid"), filepath.Join(dir, "cleaned")
	t.Setenv("POGO_TEST_CANCELLABLE_CHILD", "1")
	t.Setenv("POGO_TEST_EXECUTABLE", exe)
	t.Setenv("POGO_TEST_BASH_PID", pidfile)
	t.Setenv("POGO_TEST_CLEANUP_MARKER", marker)
	stub := filepath.Join(dir, "pogo-stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexec \"$POGO_TEST_EXECUTABLE\" '-test.run=^TestSubagentCancellableProcessHelper$'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Subagent{Cwd: dir, Exe: stub, Timeout: 8 * time.Second}
	done := make(chan error, 1)
	go func() { _, err := s.Execute(ctx, json.RawMessage(`{"mode":"edit","task":"run command"}`)); done <- err }()
	pid, err := strconv.Atoi(strings.TrimSpace(string(waitSubagentFixtureFile(t, pidfile))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled delegation succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delegation did not stop")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("Pogo child could not clean up independent tool groups")
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatal("independent shell group survived")
	}
	leaseCtx, leaseCancel := context.WithTimeout(context.Background(), time.Second)
	defer leaseCancel()
	lease, err := acquireEditLease(leaseCtx)
	if err != nil {
		t.Fatalf("cancelled child retained lease: %v", err)
	}
	lease.Close()
}

func TestSubagentCancellationForceKillsUnresponsiveGroup(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "started")
	t.Setenv("POGO_TEST_STUB_PID", pidfile)
	stub := filepath.Join(dir, "pogo-stub")
	script := "#!/bin/sh\ntrap '' TERM\necho $$ > \"$POGO_TEST_STUB_PID\"\nwhile :; do sleep 30; done\n"
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Subagent{Cwd: dir, Exe: stub, Timeout: 8 * time.Second}
	done := make(chan error, 1)
	go func() { _, err := s.Execute(ctx, json.RawMessage(`{"mode":"edit","task":"run command"}`)); done <- err }()
	_ = waitSubagentFixtureFile(t, pidfile)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled delegation succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unresponsive group survived cancellation")
	}
	leaseCtx, leaseCancel := context.WithTimeout(context.Background(), time.Second)
	defer leaseCancel()
	lease, err := acquireEditLease(leaseCtx)
	if err != nil {
		t.Fatalf("force-killed child retained lease: %v", err)
	}
	lease.Close()
}
