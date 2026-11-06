package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDelegatedFileAccessUsesLivePrivateGateVerdicts(t *testing.T) {
	account, cwd, sibling, foreign := fileAccessFixture(t)
	var full atomic.Bool
	parent := &Subagent{Cwd: cwd, FileAccess: &FileAccess{Root: account, FullAccess: full.Load}, Review: func(context.Context, Approval) Decision { return Decision{Allow: true} }}
	reqR, reqW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	verR, verW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); parent.serveGate(reqR, verW, "synthetic-child") }()
	g := &FDGate{requests: reqW, verdicts: bufio.NewReader(verR), enc: json.NewEncoder(reqW)}
	g.fileAccess = &FileAccess{Root: canonical(account), FullAccess: g.fullAccess.Load}
	t.Cleanup(func() { reqW.Close(); reqR.Close(); verR.Close(); <-done; verW.Close() })
	registry := New(Options{Cwd: cwd, FileAccess: g.FileAccess(), SpillDir: t.TempDir()})
	reader := find(t, registry, "read")
	args := map[string]any{"path": filepath.Join(sibling, "index.html")}
	for _, enabled := range []bool{false, true, false, true} {
		full.Store(enabled)
		allow, reason := g.Ask("same-child-call", "read", json.RawMessage(`{}`))
		if !allow {
			t.Fatal(reason)
		}
		_, err := callTool(t, reader, args)
		if (err == nil) != enabled {
			t.Fatalf("mode=%v: sibling access err=%v", enabled, err)
		}
		if _, err := callTool(t, reader, map[string]any{"path": filepath.Join(foreign, "index.html")}); err == nil {
			t.Fatal("parent gate granted another account")
		}
	}
	// A disappearing parent must revoke the inherited grant as well as refuse
	// the attempted call; a stale previous allow cannot become a capability.
	verW.Close()
	if allowed, _ := g.Ask("lost-parent", "read", nil); allowed || g.FileAccess().FullAccess() {
		t.Fatal("lost parent left a full access grant")
	}
}

func TestDelegatedFileAccessRejectsRootReplacementAndStripsAmbientGrants(t *testing.T) {
	account, cwd, _, foreign := fileAccessFixture(t)
	g := &FDGate{}
	g.fileAccess = &FileAccess{Root: canonical(account), FullAccess: g.fullAccess.Load}
	g.updateFileAccess(&gateVerdict{Allow: true, FullAccess: true, FileAccessRoot: canonical(account)})
	if !g.FileAccess().FullAccess() {
		t.Fatal("trusted initial root was not granted")
	}
	g.updateFileAccess(&gateVerdict{Allow: true, FullAccess: true, FileAccessRoot: foreign})
	if g.FileAccess().FullAccess() {
		t.Fatal("parent verdict replaced the child root")
	}
	g.updateFileAccess(&gateVerdict{Allow: true})
	if g.FileAccess().FullAccess() {
		t.Fatal("legacy verdict retained a broader grant")
	}
	env := fileAccessChildEnv([]string{envFileAccessRoot + "=/foreign", envFileAccessFull + "=true", "KEEP=value"}, "", false)
	if strings.Join(env, "|") != "KEEP=value" {
		t.Fatalf("ambient grant leaked: %v", env)
	}
	var full atomic.Bool
	full.Store(true)
	guard := &Guard{Workspace: cwd, FileAccess: &FileAccess{Root: account, FullAccess: full.Load}}
	if err := guard.CheckDir(filepath.Dir(cwd), filepath.Dir(cwd)); err != nil {
		t.Fatal(err)
	}
	if err := guard.Check("git status"); err == nil {
		t.Fatal("file grant removed the delegated Git guard")
	}
	full.Store(false)
	if err := guard.CheckDir(filepath.Dir(cwd), filepath.Dir(cwd)); err == nil {
		t.Fatal("delegated workdir retained full access")
	}
}
