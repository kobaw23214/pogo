//go:build unix

package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A real second process verifies flock, rather than only an in-memory mutex.
func TestEditLeaseProcessHelper(t *testing.T) {
	path := os.Getenv("POGO_TEST_EDIT_LEASE")
	if path == "" {
		return
	}
	if raw := os.Getenv("POGO_TEST_PROBE_LEASE_FD"); raw != "" {
		fd, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatal(err)
		}
		var actual, lease syscall.Stat_t
		if syscall.Fstat(fd, &actual) == nil && syscall.Stat(path, &lease) == nil && actual.Dev == lease.Dev && actual.Ino == lease.Ino {
			t.Fatal("ordinary subprocess inherited the writer lease")
		}
		return
	}
	var f *os.File
	var err error
	if os.Getenv("POGO_TEST_INHERITED_LEASE") == "1" {
		f = os.NewFile(3, "inherited-lease")
		_, err = f.Stat()
	} else {
		f, err = acquireEditLeaseAt(context.Background(), path)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func startEditLeaseHelper(t *testing.T, path string, inherited *os.File) func() {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestEditLeaseProcessHelper$")
	cmd.Env = append(os.Environ(), "POGO_TEST_EDIT_LEASE="+path)
	if inherited != nil {
		cmd.Env = append(cmd.Env, "POGO_TEST_INHERITED_LEASE=1")
		cmd.ExtraFiles = []*os.File{inherited}
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close(); _ = cmd.Process.Kill() })
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(out)
		if scanner.Scan() {
			ready <- scanner.Text()
		} else {
			ready <- ""
		}
	}()
	select {
	case line := <-ready:
		if line != "locked" {
			t.Fatalf("helper: %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lease helper did not start")
	}
	return func() {
		_ = in.Close()
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
}

func assertEditLeaseBlocked(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if f, err := acquireEditLeaseAt(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("lease should remain held, got %v", err)
	}
}

func TestEditLeaseSerializesAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edit.lock")
	finish := startEditLeaseHelper(t, path, nil)
	assertEditLeaseBlocked(t, path)
	finish()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	f, err := acquireEditLeaseAt(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestEditLeaseRemainsHeldByChildAfterParentCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edit.lock")
	f, err := acquireEditLeaseAt(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	finish := startEditLeaseHelper(t, path, f)
	f.Close()
	assertEditLeaseBlocked(t, path)
	finish()
}

func TestEditLeaseAdoptionSealsDescriptorAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edit.lock")
	f, err := acquireEditLeaseAt(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// Dup emulates ExtraFiles: the received descriptor initially lacks CLOEXEC.
	fd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	t.Setenv(EnvSubagentEditLeaseFD, strconv.Itoa(fd))
	if err := AdoptSubagentEditLease(); err != nil {
		t.Fatal(err)
	}
	if _, present := os.LookupEnv(EnvSubagentEditLeaseFD); present {
		t.Fatal("lease marker was forwarded")
	}
	if err := AdoptSubagentEditLease(); err != nil {
		t.Fatal(err)
	}
	assertEditLeaseBlocked(t, path)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestEditLeaseProcessHelper$")
	cmd.Env = append(os.Environ(), "POGO_TEST_EDIT_LEASE="+path, "POGO_TEST_PROBE_LEASE_FD="+strconv.Itoa(fd))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lease leaked to tool: %v\n%s", err, out)
	}
}

func TestEditLeaseRejectsSymlinksAndSharedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edit.lock")
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if f, err := acquireEditLeaseAt(context.Background(), path); err == nil {
		f.Close()
		t.Fatal("accepted symlink")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if f, err := acquireEditLeaseAt(context.Background(), path); err == nil {
		f.Close()
		t.Fatal("accepted shared lease file")
	}
}

func TestSubagentCancelledLeaseWaitDoesNotSpawn(t *testing.T) {
	lease, err := acquireEditLease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "writes")
	s := &Subagent{Cwd: root, Exe: stub}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err = s.Execute(ctx, json.RawMessage(`{"mode":"edit","task":"write a file"}`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected cancelled wait: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "added.txt")); !os.IsNotExist(err) {
		t.Fatalf("cancelled waiter spawned child: %v", err)
	}
}

func TestSubagentWritableLeaseSpansInstances(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "active")
	stub := filepath.Join(root, "pogo-stub")
	t.Setenv("POGO_TEST_ACTIVE_WRITER", active)
	script := `#!/bin/sh
if ! mkdir "$POGO_TEST_ACTIVE_WRITER"; then
  printf '%s\n' '{"type":"run_end","error":"writers overlapped"}'
  exit 1
fi
sleep 0.15
rmdir "$POGO_TEST_ACTIVE_WRITER"
printf '%s\n' '{"type":"token","text":"finished"}' '{"type":"run_end"}'
`
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		s := &Subagent{Cwd: t.TempDir(), Exe: stub}
		go func() {
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := s.Execute(ctx, json.RawMessage(`{"mode":"edit","task":"produce output"}`))
			results <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestSubagentEditIgnoresBackgroundRequest(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "writes")
	cap := newBgCapture()
	bg := NewBgRunner(context.Background(), cap.hooks())
	defer bg.Close()
	s := &Subagent{Cwd: root, Exe: stub, Bg: bg}
	res, err := s.Execute(context.Background(), json.RawMessage(`{"mode":"edit","task":"write output","wait":false}`))
	if err != nil {
		t.Fatal(err)
	}
	d := details(t, res)
	if d.Background || d.Workspace != root || !strings.Contains(res.Text, "I added added.txt") {
		t.Fatalf("edit returned before completion: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "added.txt")); err != nil {
		t.Fatal(err)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.started) != 0 {
		t.Fatal("edit entered background runner")
	}
}

func TestSubagentLeaseDescriptorFollowsGateAndBrowser(t *testing.T) {
	root := t.TempDir()
	stub := filepath.Join(root, "pogo-stub")
	script := `#!/bin/sh
printf '%s\n' "{\"type\":\"token\",\"text\":\"gate=$POGO_GATE_FD browser=$POGO_BROWSER_FD\"}"
test -f /dev/fd/7 || exit 9
printf '%s\n' '{"type":"run_end"}'
`
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	s := &Subagent{Cwd: root, Exe: stub, Browser: &recordingLink{}, Review: func(context.Context, Approval) Decision { return Decision{Allow: true} }}
	res, err := s.Execute(context.Background(), json.RawMessage(`{"mode":"edit","task":"inspect descriptors"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "gate=3 browser=5") {
		t.Fatalf("descriptor numbering changed: %s", res.Text)
	}
}
