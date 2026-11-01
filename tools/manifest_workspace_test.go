package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A relative manifest directory is resolved at load time. The script must stay
// anchored to its skill after the registry switches to a session workspace,
// while its input path must resolve relative to that session.
func TestSynthRelativeManifestDirAcrossWorkspaces(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	src := writeManifest(t, "relative-skill", `{"tools": [{
		"name": "docs.read-relative", "description": "Read a workspace file",
		"inputSchema": {"type": "object", "properties": {
			"file": {"type": "string"}
		}, "required": ["file"]},
		"x-pogo-exec": {
			"argv": ["sh", "$SKILL_DIR/read-file.sh"], "map": {"file": "append"}
		}
	}]}`)
	script := filepath.Join(src.Dir, "read-file.sh")
	if err := os.WriteFile(script, []byte("set -e\nIFS= read -r value < \"$1\"\nprintf '%s' \"$value\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	processCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeDir, err := filepath.Rel(processCwd, src.Dir)
	if err != nil {
		t.Fatal(err)
	}
	src.Dir = relativeDir
	loaded, _, diags := LoadSynthTools([]SynthSource{src})
	if len(diags) != 0 || len(loaded) != 1 {
		t.Fatalf("load relative manifest: %d tools, diagnostics %+v", len(loaded), diags)
	}

	const inputPath = "uploads/中文 文档.txt"
	args, err := json.Marshal(map[string]string{"file": inputPath})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"workspace A", "workspace B"} {
		cwd := t.TempDir()
		if err := os.Mkdir(filepath.Join(cwd, "uploads"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cwd, inputPath), []byte(want+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		registry := New(Options{Cwd: cwd, Synth: loaded, SpillDir: t.TempDir()})
		tool, ok := registry.Get(loaded[0].Name())
		if !ok {
			t.Fatal("manifest tool was not registered")
		}
		result, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("execute from %s: %v", want, err)
		}
		if result.Text != want {
			t.Errorf("read %q, want %q", result.Text, want)
		}
		argv := result.Details.(SynthDetails).Argv
		if argv[1] != script || !filepath.IsAbs(argv[1]) {
			t.Errorf("script path = %q, want absolute %q", argv[1], script)
		}
		if argv[2] != inputPath {
			t.Errorf("workspace input = %q, want unchanged %q", argv[2], inputPath)
		}
	}
}
