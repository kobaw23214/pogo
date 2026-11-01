package information

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every process opens the same existing source store, then waits at a barrier
// before its first materialization. Neither the lock namespace nor uploads/
// exists beforehand, so a warm parent cannot hide a directory-creation race.
func TestMaterializationColdStartChild(t *testing.T) {
	if os.Getenv("POGO_REUSE_COLD_CHILD") != "1" {
		return
	}
	s, err := Open(os.Getenv("POGO_REUSE_COLD_ROOT"), os.Getenv("POGO_REUSE_COLD_SCOPE"))
	if err != nil {
		t.Fatal(err)
	}
	control := os.Getenv("POGO_REUSE_COLD_CONTROL")
	if err = os.WriteFile(filepath.Join(control, "ready-"+os.Getenv("POGO_REUSE_COLD_INDEX")), nil, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err = os.Stat(filepath.Join(control, "go")); err == nil {
			break
		}
		if !os.IsNotExist(err) || time.Now().After(deadline) {
			t.Fatal("cold-start barrier failed", err)
		}
		time.Sleep(time.Millisecond)
	}
	p, _, err := s.MaterializeAsset(os.Getenv("POGO_REUSE_COLD_ASSET"), os.Getenv("POGO_REUSE_COLD_WORKSPACE"))
	if err != nil {
		entries, _ := os.ReadDir(filepath.Join(s.Root(), "materializations", "locks"))
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("%v; existing lock names: %v", err, names)
	}
	fmt.Println("COLD_PATH=" + p)
}

func TestMaterializationColdStartIndependentProcessesReuseOneCopy(t *testing.T) {
	reusePlatform(t)
	s := testStore(t)
	w, control := t.TempDir(), t.TempDir()
	a := reuseAsset(t, s, "mac命令.txt", "original source")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const count = 8
	cmds := make([]*exec.Cmd, count)
	outputs := make([]bytes.Buffer, count)
	for i := range count {
		cmd := exec.CommandContext(ctx, exe, "-test.run=^TestMaterializationColdStartChild$")
		cmd.Env = append(os.Environ(), "POGO_REUSE_COLD_CHILD=1", "POGO_REUSE_COLD_ROOT="+s.Root(), "POGO_REUSE_COLD_SCOPE="+s.ScopeID(), "POGO_REUSE_COLD_WORKSPACE="+w, "POGO_REUSE_COLD_ASSET="+a.Asset, "POGO_REUSE_COLD_CONTROL="+control, fmt.Sprintf("POGO_REUSE_COLD_INDEX=%d", i))
		cmd.Stdout, cmd.Stderr = &outputs[i], &outputs[i]
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds[i] = cmd
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		ready, err := filepath.Glob(filepath.Join(control, "ready-*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(ready) == count {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("children did not reach cold-start barrier")
		}
		time.Sleep(time.Millisecond)
	}
	for _, p := range []string{filepath.Join(s.Root(), "materializations"), filepath.Join(w, "uploads")} {
		if _, err = os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("materialization namespace was prewarmed", p, err)
		}
	}
	if err = os.WriteFile(filepath.Join(control, "go"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	first := ""
	for i, cmd := range cmds {
		if err = cmd.Wait(); err != nil {
			t.Fatalf("child %d: %v\n%s", i, err, outputs[i].String())
		}
		p := ""
		for _, line := range strings.Split(outputs[i].String(), "\n") {
			if strings.HasPrefix(line, "COLD_PATH=") {
				p = strings.TrimPrefix(line, "COLD_PATH=")
			}
		}
		if p == "" || first != "" && p != first {
			t.Fatalf("cold children duplicated or lost the copy: %q != %q", p, first)
		}
		first = p
		reuseBytes(t, w, p, "original source")
	}
	entries, err := os.ReadDir(filepath.Join(w, "uploads", "information"))
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		t.Fatal("expected exactly one complete workspace copy", entries, err)
	}
}
