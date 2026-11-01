package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortableArchivePathRejectsWindowsAliases(t *testing.T) {
	for _, name := range []string{"bin/tool:payload", "pogo-skill.json.", "lib/name ", "data/CON.txt", "NUL", "bin/COM1", "bin/x\x01y"} {
		if _, err := normalizeZipName(name); err == nil {
			t.Errorf("accepted nonportable alias %q", name)
		}
	}
}

func TestDocumentRejectsInvalidUTF8(t *testing.T) {
	if _, err := decodeRawJSON([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
}

func TestReadMetadataResolvesInstalledLayout(t *testing.T) {
	_, pkg, _ := installDemo(t)
	data, err := pkg.ReadMetadata(pkg.Manifest.Skill.Entry)
	if err != nil || len(data) == 0 {
		t.Fatalf("installed metadata unavailable: %v", err)
	}
	if _, err = pkg.ReadMetadata("./../install.json"); err == nil {
		t.Fatal("metadata traversal accepted")
	}
}

func TestLoadRejectsPostInstallSymlink(t *testing.T) {
	store, pkg, _ := installDemo(t)
	dir, err := pkg.RuntimePath("rt")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "lib", "data.txt")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "same-content.txt")
	if err = os.WriteFile(out, content, 0600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(filepath.Dir(path), 0755)
	_ = os.Chmod(path, 0644)
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(out, path); err != nil {
		t.Skipf("host cannot create symlink: %v", err)
	}
	if _, err = store.Load(pkg.ref()); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("same-content external symlink was accepted: %v", err)
	}
}
