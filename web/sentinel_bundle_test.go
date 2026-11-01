package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sealedBundleFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	manifest := sentinelBundle{Files: map[string]string{}}
	for rel, data := range map[string]string{"main.py": "from helpers import read\n", "helpers/read.py": "def read(): return 1\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256([]byte(data))
		manifest.Files[rel] = hex.EncodeToString(h[:])
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".pogo-watch-manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSentinelBundleExactFileSet(t *testing.T) {
	for _, mode := range []string{"unchanged", "extra_module", "extra_nested_module", "modified", "missing", "symlink_file", "symlink_directory", "manifest_symlink"} {
		t.Run(mode, func(t *testing.T) {
			dir := sealedBundleFixture(t)
			var err error
			switch mode {
			case "extra_module":
				err = os.WriteFile(filepath.Join(dir, "os.py"), []byte("raise RuntimeError('shadowed')"), 0600)
			case "extra_nested_module":
				err = os.WriteFile(filepath.Join(dir, "helpers", "__init__.py"), []byte("raise RuntimeError('shadowed')"), 0600)
			case "modified":
				err = os.WriteFile(filepath.Join(dir, "main.py"), []byte("changed"), 0600)
			case "missing":
				err = os.Remove(filepath.Join(dir, "helpers", "read.py"))
			case "symlink_file":
				err = os.Remove(filepath.Join(dir, "main.py"))
				if err == nil {
					err = os.Symlink(filepath.Join(dir, "helpers", "read.py"), filepath.Join(dir, "main.py"))
				}
			case "symlink_directory":
				err = os.Symlink(t.TempDir(), filepath.Join(dir, "extra"))
			case "manifest_symlink":
				err = os.Rename(filepath.Join(dir, ".pogo-watch-manifest.json"), filepath.Join(dir, "manifest-old"))
				if err == nil {
					err = os.Symlink(filepath.Join(dir, "manifest-old"), filepath.Join(dir, ".pogo-watch-manifest.json"))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			err = verifySentinelBundle(dir)
			if mode == "unchanged" && err != nil {
				t.Fatal(err)
			}
			if mode != "unchanged" && err == nil {
				t.Fatalf("accepted %s mutation", mode)
			}
		})
	}
}

func TestSentinelBundleRejectsMalformedManifest(t *testing.T) {
	for _, rel := range []string{"../outside.py", "/outside.py", "helpers/../main.py", ".pogo-watch-manifest.json"} {
		t.Run(strings.ReplaceAll(rel, "/", "_"), func(t *testing.T) {
			dir := sealedBundleFixture(t)
			manifest := sentinelBundle{Files: map[string]string{rel: strings.Repeat("0", 64)}}
			raw, _ := json.Marshal(manifest)
			if err := os.WriteFile(filepath.Join(dir, ".pogo-watch-manifest.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := verifySentinelBundle(dir); err == nil {
				t.Fatal("accepted invalid manifest path")
			}
		})
	}
}

func TestSentinelSealCreatesIndependentVersion(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(workspace, "source")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('version1')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: Config{SessionDir: t.TempDir()}}
	b := &sentinelToolBackend{m: m, workspace: workspace}
	sealed, command, err := b.seal("source/main.py", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(command) != 3 || command[2] != filepath.Join(sealed, "main.py") {
		t.Fatalf("unexpected sealed command: %q", command)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('version2')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifySentinelBundle(sealed); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(command[2])
	if err != nil || !strings.Contains(string(data), "version1") {
		t.Fatalf("sealed version changed: %q %v", data, err)
	}
}

func TestSentinelWorkspaceNeverGrantsPrivateControlState(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private-state")
	public := filepath.Join(root, "agent-data")
	if err := os.MkdirAll(private, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(public, 0700); err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: Config{SessionDir: private}}
	for _, workspace := range []string{root, private, filepath.Join(private, "child")} {
		if err := m.validateSentinelWorkspace(workspace); err == nil {
			t.Fatalf("granted private control state %s", workspace)
		}
	}
	if err := m.validateSentinelWorkspace(public); err != nil {
		t.Fatal(err)
	}
}
