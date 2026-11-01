package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Load through the manifest path used by the manager, without setting Cwd on
// the shared tool. The registry must supply the workspace for each execution.
func workspaceSynthTools(t *testing.T) []Tool {
	t.Helper()
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("no cat on PATH")
	}
	src := writeManifest(t, "docs", `{"tools": [{
		"name": "docs.read-file", "description": "Read a workspace file",
		"inputSchema": {"type": "object", "properties": {
			"file": {"type": "string"}
		}, "required": ["file"]},
		"annotations": {"readOnlyHint": true, "idempotentHint": true},
		"x-pogo-exec": {"argv": ["cat"], "map": {"file": "append"}}
	}]}`)
	loaded, _, diags := LoadSynthTools([]SynthSource{src})
	if len(diags) != 0 || len(loaded) != 1 {
		t.Fatalf("load: %d tools, diagnostics %+v", len(loaded), diags)
	}
	return loaded
}

func writeSynthWorkspaceFile(t *testing.T, cwd, relativePath, payload string) {
	t.Helper()
	path := filepath.Join(cwd, relativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSynthRegistryResolvesSessionRelativePaths(t *testing.T) {
	loaded := workspaceSynthTools(t)
	const relativePath = "uploads/尚硅谷-08-中间件.pdf"
	args, err := json.Marshal(map[string]string{"file": relativePath})
	if err != nil {
		t.Fatal(err)
	}
	var registries []*Registry
	for _, payload := range []string{"session A", "session B"} {
		cwd := t.TempDir()
		writeSynthWorkspaceFile(t, cwd, relativePath, payload)
		registries = append(registries, New(Options{Cwd: cwd, Synth: loaded, SpillDir: t.TempDir()}))
	}
	// Register both sessions first: binding the shared prototype in place would
	// make the first session read the second session's file.
	for i, want := range []string{"session A", "session B"} {
		tool, ok := registries[i].Get(loaded[0].Name())
		if !ok {
			t.Fatal("manifest tool was not registered")
		}
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
		if res.Text != want {
			t.Errorf("session %d read %q, want %q", i, res.Text, want)
		}
	}
	prototype := loaded[0].(*SynthTool)
	if prototype.Cwd != "" || prototype.SpillDir != "" {
		t.Fatalf("registration mutated the shared tool: cwd=%q spill=%q", prototype.Cwd, prototype.SpillDir)
	}
}

func TestSynthRegistryConcurrentWorkspaceSpills(t *testing.T) {
	loaded := workspaceSynthTools(t)
	// Each registry has its own workspace and spill directory. Exercise both
	// concurrent registration and execution against the same loaded manifest.
	for i := range 8 {
		t.Run(fmt.Sprintf("session-%d", i), func(t *testing.T) {
			t.Parallel()
			cwd, spillDir := t.TempDir(), t.TempDir()
			payload := strings.Repeat(fmt.Sprintf("session-%d %s\n", i, strings.Repeat("x", 100)), 600)
			writeSynthWorkspaceFile(t, cwd, "uploads/中文 文档.txt", payload)
			registry := New(Options{Cwd: cwd, Synth: loaded, SpillDir: spillDir, ReadOnly: true})
			tool, ok := registry.Get(loaded[0].Name())
			if !ok {
				t.Fatal("read-only manifest tool was not registered")
			}
			res, err := tool.Execute(context.Background(), json.RawMessage(`{"file":"uploads/中文 文档.txt"}`))
			if err != nil {
				t.Fatal(err)
			}
			details := res.Details.(SynthDetails)
			if !details.Truncated || details.FullOutputPath == "" {
				t.Fatalf("large output was not spilled: %+v", details)
			}
			if !within(canonical(details.FullOutputPath), canonical(spillDir)) {
				t.Fatalf("spill %q escaped session directory %q", details.FullOutputPath, spillDir)
			}
			full, err := os.ReadFile(details.FullOutputPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(full) != payload {
				t.Fatal("spilled output does not match this session's file")
			}
			readTool, _ := registry.Get("read")
			readArgs, err := json.Marshal(map[string]any{"path": details.FullOutputPath, "limit": 1})
			if err != nil {
				t.Fatal(err)
			}
			readResult, err := readTool.Execute(context.Background(), readArgs)
			if err != nil {
				t.Fatalf("session cannot read its spilled output: %v", err)
			}
			if !strings.Contains(readResult.Text, fmt.Sprintf("session-%d", i)) {
				t.Fatal("read returned another session's spilled output")
			}
		})
	}
}
