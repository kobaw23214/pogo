package core

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Run the compiled test binary as UID 1000 on a native Linux filesystem.
// Root bypasses the directory permissions that caused the production failure.
func TestNonRootInstallLifecycle(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires a non-root Linux user; run with --user 1000:1000")
	}
	t.Logf("installation lifecycle running as uid=%d gid=%d", os.Geteuid(), os.Getegid())
	newStore := func(t *testing.T) (*Store, string) {
		t.Helper()
		dir := t.TempDir()
		t.Cleanup(func() {
			if err := removeInstallStage(dir); err != nil {
				t.Errorf("clean test store: %v", err)
			}
		})
		store, err := OpenStore(filepath.Join(dir, "store"))
		if err != nil {
			t.Fatal(err)
		}
		return store, dir
	}
	assertNoStaging := func(t *testing.T, root string) {
		t.Helper()
		if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if strings.HasPrefix(d.Name(), ".staging-") {
				t.Errorf("install left staging content: %s", path)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	writeArchive := func(t *testing.T, dir string, data []byte) string {
		t.Helper()
		path := filepath.Join(dir, "pkg.zip")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("publish reload and immutable reinstall", func(t *testing.T) {
		store, dir := newStore(t)
		archive, _ := buildDemoOuterZip(t, nil)
		path := writeArchive(t, dir, archive)
		pkg, err := store.InstallZIP(path)
		if err != nil {
			t.Fatalf("non-root installation: %v", err)
		}
		if _, err := store.Load(pkg.ref()); err != nil {
			t.Fatalf("reload installed pkg: %v", err)
		}
		if err := filepath.WalkDir(pkg.Root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Mode().Perm()&0o222 != 0 {
				t.Errorf("installed content remains writable: %s mode=%v", path, info.Mode().Perm())
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pkg.Root, "unexpected"), []byte("x"), 0o600); !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("installed root accepts new files: %v", err)
		}
		again, err := store.InstallZIP(path)
		if err != nil || again.PackageDigest != pkg.PackageDigest {
			t.Fatalf("identical reinstall: %v", err)
		}
		changed, _ := buildDemoOuterZip(t, func(entries []zipEnt) []zipEnt {
			return append(entries, zipEnt{name: "README.md", data: []byte("different"), mode: 0o644})
		})
		if _, err := store.InstallZIP(writeArchive(t, dir, changed)); !errors.Is(err, ErrVersionConflict) {
			t.Fatalf("immutable version collision: %v", err)
		}
		if _, err := store.Load(pkg.ref()); err != nil {
			t.Fatalf("collision damaged existing pkg: %v", err)
		}
		assertNoStaging(t, store.root)
	})

	t.Run("failed publish removes sealed staging", func(t *testing.T) {
		store, dir := newStore(t)
		archive, _ := buildDemoOuterZip(t, nil)
		finalDir := store.versionDir("org.test.demo", "1.0.0")
		if err := os.MkdirAll(finalDir, 0o755); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(finalDir, "existing-content")
		if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := store.InstallZIP(writeArchive(t, dir, archive))
		var renameError *os.LinkError
		if err == nil || !strings.Contains(err.Error(), "publish staged install") || !errors.As(err, &renameError) {
			t.Fatalf("expected wrapped publish failure: %v", err)
		}
		if data, err := os.ReadFile(marker); err != nil || string(data) != "preserve" {
			t.Fatalf("failed publish changed existing content: %v", err)
		}
		assertNoStaging(t, store.root)
	})
}
