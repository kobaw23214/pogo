package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemovePackageVersionConfinesAndUnseals(t *testing.T) {
	store, pkg, _ := installDemo(t)
	ref := PackageRef{ID: pkg.Manifest.ID, Version: pkg.Manifest.Version, ManifestDigest: pkg.ManifestDigest, PackageDigest: pkg.PackageDigest}
	for _, id := range []string{"..", "../outside", `..\outside`, "C:"} {
		bad := ref
		bad.ID = id
		if err := store.Remove(bad); err == nil {
			t.Fatalf("unsafe removal accepted: %s", id)
		}
	}
	wrong := ref
	wrong.PackageDigest = "sha256:" + strings.Repeat("0", 64)
	if err := store.Remove(wrong); err == nil {
		t.Fatal("wrong digest accepted")
	}
	if _, err := store.Load(ref); err != nil {
		t.Fatal("failed delete changed installation", err)
	}
	other := filepath.Join(store.root, "packages", ref.ID, "2.0.0")
	if err := os.MkdirAll(other, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "keep"), []byte("other version"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(ref); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.versionDir(ref.ID, ref.Version)); !os.IsNotExist(err) {
		t.Fatalf("deleted tree remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "keep")); err != nil {
		t.Fatal("other version removed", err)
	}
	if err := store.Remove(ref); err != nil {
		t.Fatal("retry is not idempotent", err)
	}
}
