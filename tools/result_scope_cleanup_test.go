package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeCleanupFixture(t *testing.T, file, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveResultScopeSpillOnlyExactScopeAndAllItsArtifacts(t *testing.T) {
	base := t.TempDir()
	scope := "web:user:../arbitrary:/session-a"
	target := resultSpillDir(base, scope)
	other := resultSpillDir(base, scope+"-other")
	for _, relative := range []string{
		"browser-page.log", ".partial-abc", "results/result-record.log",
		"results/.metadata/record.json", "results/nested/lock.data", "arbitrary/nested/file.bin",
	} {
		writeCleanupFixture(t, filepath.Join(target, relative), "DELETE")
	}
	writeCleanupFixture(t, filepath.Join(other, "results/keep.log"), "OTHER_SCOPE")
	writeCleanupFixture(t, filepath.Join(base, "browser-legacy.log"), "LEGACY")
	writeCleanupFixture(t, filepath.Join(base, "results/legacy.log"), "LEGACY_RESULTS")
	if err := RemoveResultScopeSpill(base, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("target still exists: %v", err)
	}
	for file, expected := range map[string]string{
		filepath.Join(other, "results/keep.log"):  "OTHER_SCOPE",
		filepath.Join(base, "browser-legacy.log"): "LEGACY",
		filepath.Join(base, "results/legacy.log"): "LEGACY_RESULTS",
	} {
		data, err := os.ReadFile(file)
		if err != nil || string(data) != expected {
			t.Fatalf("cleanup touched unrelated file %s: %q %v", file, data, err)
		}
	}
	if err := RemoveResultScopeSpill(base, scope); err != nil {
		t.Fatalf("repeated deletion was not idempotent: %v", err)
	}
}

func TestRemoveResultScopeSpillIgnoresRetentionButWaitsForCurrentOperation(t *testing.T) {
	base := t.TempDir()
	scope := "web:owner:session-retained"
	_, store := newSessionResultStorage(base, scope)
	reference, err := store.Save(StoredToolResult{Tool: "fixture", Text: "retained observation", TextComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Retain(context.Background(), reference.ID, 90); err != nil {
		t.Fatal(err)
	}
	root, err := openResultStoreRoot(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	unlock, err := lockResultLifecycle(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	done := make(chan error, 1)
	go func() { done <- RemoveResultScopeSpill(base, scope) }()
	select {
	case err := <-done:
		t.Fatalf("deleted a scope with an operation still locked: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup did not resume after the operation finished")
	}
	if _, err := os.Lstat(resultSpillDir(base, scope)); !os.IsNotExist(err) {
		t.Fatal("explicit delete left leased/retained result files or metadata")
	}
}

func TestRemoveResultScopeSpillMissingPathsAndEmptyIdentity(t *testing.T) {
	base := filepath.Join(t.TempDir(), "missing")
	if err := RemoveResultScopeSpill(base, "scope"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(base); !os.IsNotExist(err) {
		t.Fatal("cleanup created a missing spill root")
	}
	base = t.TempDir()
	if err := RemoveResultScopeSpill(base, "scope"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(base)
	if err != nil || len(entries) != 0 {
		t.Fatal("cleanup created missing scopes or result directories")
	}
	writeCleanupFixture(t, filepath.Join(base, "legacy.log"), "KEEP")
	for _, scope := range []string{"", " ", "\n\t"} {
		if err := RemoveResultScopeSpill(base, scope); err == nil {
			t.Fatal("accepted an empty result identity")
		}
	}
	if _, err := os.Stat(filepath.Join(base, "legacy.log")); err != nil {
		t.Fatal("empty identity touched legacy shared storage")
	}
}

func TestRemoveResultScopeSpillRejectsLinkedRootsAndUnlinksNestedLinks(t *testing.T) {
	base := t.TempDir()
	scope := "web:owner:session-link"
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.log")
	writeCleanupFixture(t, secret, "OUTSIDE")
	linkedBase := filepath.Join(t.TempDir(), "base-link")
	if err := os.Symlink(outside, linkedBase); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if err := RemoveResultScopeSpill(linkedBase, scope); err == nil {
		t.Fatal("accepted a symbolic spill root")
	}
	if err := os.Symlink(outside, filepath.Join(base, "scopes")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveResultScopeSpill(base, scope); err == nil {
		t.Fatal("accepted a symbolic scopes directory")
	}
	if err := os.Remove(filepath.Join(base, "scopes")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "scopes"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := resultSpillDir(base, scope)
	if err := os.Symlink(outside, target); err != nil {
		t.Fatal(err)
	}
	if err := RemoveResultScopeSpill(base, scope); err == nil {
		t.Fatal("accepted a symbolic session directory")
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	writeCleanupFixture(t, filepath.Join(target, "results/.metadata/record.json"), "DELETE")
	if err := os.Symlink(outside, filepath.Join(target, "nested-outside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(target, "results/linked-result.log")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveResultScopeSpill(base, scope); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(secret); err != nil || string(data) != "OUTSIDE" {
		t.Fatalf("nested symlink escaped the target namespace: %q %v", data, err)
	}
}

func TestRemoveResultScopeContentsKeepsPinnedDirectoryAfterReplacement(t *testing.T) {
	base := t.TempDir()
	original := filepath.Join(base, "original")
	writeCleanupFixture(t, filepath.Join(original, "results/delete.log"), "DELETE")
	root, err := os.OpenRoot(original)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	moved := filepath.Join(base, "moved")
	if err := os.Rename(original, moved); err != nil {
		t.Skipf("platform does not permit renaming an open root: %v", err)
	}
	writeCleanupFixture(t, filepath.Join(original, "results/keep.log"), "REPLACEMENT")
	if err := removeResultScopeContents(root); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(moved)
	if err != nil || len(entries) != 0 {
		t.Fatalf("pinned original contents were not removed: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(original, "results/keep.log")); err != nil || string(data) != "REPLACEMENT" {
		t.Fatalf("cleanup reopened a replaced path: %q %v", data, err)
	}
}

func TestRemoveResultScopeSpillRemovesMoreThanOneDirectoryBatch(t *testing.T) {
	base := t.TempDir()
	scope := "web:owner:many-files"
	target := resultSpillDir(base, scope)
	for i := 0; i < 600; i++ {
		writeCleanupFixture(t, filepath.Join(target, fmt.Sprintf("%03d.log", i)), "DELETE")
	}
	if err := RemoveResultScopeSpill(base, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("multi-batch directory was not fully removed: %v", err)
	}
}
