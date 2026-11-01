package skillruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageDeleteReclaimsVersionAndCache(t *testing.T) {
	s := newStubService(t)
	installed, _ := installSkillFixture(t, s)
	if len(s.SelectionCatalog().Skills) == 0 {
		t.Fatal("fixture has no Skills")
	}
	s.active[installed.Ref.PackageDigest] = map[string]context.CancelFunc{"call": func() {}}
	if _, err := s.Delete(installed.Ref.PackageDigest); !errors.Is(err, ErrPackageBusy) {
		t.Fatalf("active call: %v", err)
	}
	delete(s.active, installed.Ref.PackageDigest)
	if err := s.Revoke(installed.Ref.PackageDigest); err != nil {
		t.Fatal(err)
	}
	result, err := s.Delete(installed.Ref.PackageDigest)
	if err != nil || !result.Deleted || result.CleanupPending {
		t.Fatalf("delete: %+v %v", result, err)
	}
	if len(s.Catalog()) != 0 || len(s.SelectionCatalog().Skills) != 0 || len(s.metadata) != 0 {
		t.Fatal("deleted version remains visible or cached")
	}
	if _, err := os.Stat(filepath.Join(s.Root, "packages", "packages", installed.Ref.ID, installed.Ref.Version)); !os.IsNotExist(err) {
		t.Fatalf("files remain: %v", err)
	}
	if _, err := s.Delete(installed.Ref.PackageDigest); !errors.Is(err, ErrPackageNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	reloaded, err := NewManagementService(s.Root)
	if err != nil || len(reloaded.List()) != 0 {
		t.Fatalf("reload: %v", err)
	}
	// Reimport remains an explicit administrator action, not an undo of revoke.
	installedAgain, _ := installSkillFixture(t, reloaded)
	if installedAgain.Revoked || len(reloaded.List()) != 1 {
		t.Fatal("explicit reinstall did not restore package")
	}
}

func TestPackageDeleteJournalRecovery(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-commit", true: "after-commit"}[committed], func(t *testing.T) {
			s := newStubService(t)
			installed, _ := installSkillFixture(t, s)
			journal := filepath.Join(s.Root, ".delete-"+strings.TrimPrefix(installed.Ref.PackageDigest, "sha256:")+".json")
			if err := atomicJSON(journal, installed.Ref); err != nil {
				t.Fatal(err)
			}
			if committed {
				if err := atomicJSON(filepath.Join(s.Root, "catalog.json"), []Installed{}); err != nil {
					t.Fatal(err)
				}
			}
			reloaded, err := NewManagementService(s.Root)
			if err != nil {
				t.Fatal(err)
			}
			_, loadErr := reloaded.Store.Load(installed.Ref)
			if committed && (len(reloaded.List()) != 0 || loadErr == nil) {
				t.Fatal("committed deletion was not recovered")
			}
			if !committed && (len(reloaded.List()) != 1 || loadErr != nil) {
				t.Fatal("uncommitted deletion damaged package")
			}
			if _, err := os.Stat(journal); !os.IsNotExist(err) {
				t.Fatalf("journal remains: %v", err)
			}
		})
	}
}
