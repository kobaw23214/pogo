package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandScopeOutputCaptureAnchorsBeforeDirectorySwap(t *testing.T) {
	parent := t.TempDir()
	private, moved, other := filepath.Join(parent, "private"), filepath.Join(parent, "moved"), filepath.Join(parent, "other")
	for _, dir := range []string{private, other} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	capture, err := newScopedOutputCapture(private, "bash", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer capture.finish()
	if err := os.Rename(private, moved); err != nil {
		t.Skipf("platform cannot swap an open directory: %v", err)
	}
	if err := os.Symlink(other, private); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := capture.Write([]byte(strings.Repeat("safe-output\n", MaxBytes))); err != nil {
		t.Fatal(err)
	}
	kept := capture.finish()
	if !kept.Truncated || kept.Path == "" {
		t.Fatalf("scoped output not retained: %+v", kept)
	}
	entries, err := os.ReadDir(other)
	if err != nil || len(entries) != 0 {
		t.Fatalf("collector escaped through replaced directory: err=%v entries=%v", err, entries)
	}
	entries, err = os.ReadDir(moved)
	if err != nil || len(entries) != 1 {
		t.Fatalf("collector did not use original root: err=%v entries=%v", err, entries)
	}
}
