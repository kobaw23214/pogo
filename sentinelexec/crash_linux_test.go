//go:build linux

package sentinelexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/sentinelsdk"
)

type hostCrashProbe struct {
	DB, WatchID, Ready string
}

func TestLinuxHostCrashFixture(t *testing.T) {
	if os.Getenv("SENTINEL_HOST_CRASH_HELPER") != "1" {
		return
	}
	raw, err := os.ReadFile(os.Args[len(os.Args)-1])
	if err != nil {
		t.Fatal(err)
	}
	var probe hostCrashProbe
	if err = json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	db, err := sentinel.Open(probe.DB)
	if err != nil {
		t.Fatal(err)
	}
	w, err := db.Get(probe.WatchID)
	if err != nil {
		t.Fatal(err)
	}
	err = (&Runner{}).Run(context.Background(), Config{Command: w.Spec.Command, ScriptDir: w.Spec.ScriptDir, Workspace: w.Spec.Workspace, Mode: "stream"}, func(f sentinel.Frame) error {
		if err := db.Accept(w.Spec.ID, w.Spec.Generation, f); err != nil {
			return err
		}
		// Commit is durable, but this callback has not returned, so no ACK
		// can have reached the SDK. The parent kills this host with SIGKILL.
		if err := os.WriteFile(probe.Ready, []byte("committed-before-ACK"), 0600); err != nil {
			return err
		}
		select {}
	})
	t.Fatal("crash probe ended without being killed:", err)
}

func TestLinuxHostSIGKILLPreservesCommitAndRetiresSandbox(t *testing.T) {
	cfg := linuxFixture(t, `import sys,subprocess
from pogo_watch import Watcher,event
subprocess.Popen([sys.executable,"-c","import time;time.sleep(3600)",sys.argv[1]])
with Watcher(mode="stream") as w:
    w.emit([event("record:1","first",{"n":1})],state={"n":1},cursor={"n":1})
    w.emit([event("record:2","second",{"n":2})],state={"n":2},cursor={"n":2})
`, "stream")
	marker := "pogo-host-crash-" + filepath.Base(cfg.ScriptDir)
	cfg.Command = append(cfg.Command, marker)
	if err := os.WriteFile(filepath.Join(cfg.ScriptDir, sentinelsdk.FileName), sentinelsdk.Source(), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dbPath := filepath.Join(root, "events.db")
	db, err := sentinel.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	w, err := db.Create(sentinel.Spec{AgentID: "isolated-crash-probe", Workspace: cfg.Workspace, ScriptDir: cfg.ScriptDir, Command: cfg.Command, Title: "host crash test", Task: "test only", Mode: "stream", TimeoutSeconds: 10, MaxTurns: 5, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	probe := hostCrashProbe{DB: dbPath, WatchID: w.Spec.ID, Ready: filepath.Join(root, "commit.ready")}
	probePath := filepath.Join(root, "probe.json")
	raw, _ := json.Marshal(probe)
	if err = os.WriteFile(probePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestLinuxHostCrashFixture$", "--", probePath)
	cmd.Env = append(os.Environ(), "SENTINEL_HOST_CRASH_HELPER=1")
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ready, _ := os.ReadFile(probe.Ready); string(ready) == "committed-before-ACK" {
			break
		}
		select {
		case e := <-done:
			t.Fatalf("host exited before commit: %v logs=%s", e, logs.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("host never committed a frame before the crash")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("host crash unexpectedly looked like a clean exit")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SIGKILL did not retire the test host")
	}
	soakAssertNoMarkedProcess(t, marker)
	db, err = sentinel.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Recover(); err != nil {
		t.Fatal(err)
	}
	w, err = db.Get(w.Spec.ID)
	if err != nil || w.Pending != 1 || string(w.Cursor) != `{"n":1}` {
		t.Fatalf("SIGKILL lost an already committed observation: cursor=%s pending=%d err=%v", w.Cursor, w.Pending, err)
	}
	cfg.State, cfg.Cursor = w.State, w.Cursor
	stop := errors.New("new observation received after host restart")
	calls := 0
	err = (&Runner{}).Run(context.Background(), cfg, func(f sentinel.Frame) error {
		calls++
		if e := db.Accept(w.Spec.ID, w.Spec.Generation, f); e != nil {
			return e
		}
		if calls == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || calls != 2 {
		t.Fatalf("restarted SDK did not replay then continue: %v calls=%d", err, calls)
	}
	soakAssertNoMarkedProcess(t, marker)
	w, err = db.Get(w.Spec.ID)
	if err != nil || w.Pending != 2 || string(w.Cursor) != `{"n":2}` {
		t.Fatalf("restart duplicated/lost observations: cursor=%s pending=%d err=%v", w.Cursor, w.Pending, err)
	}
}
