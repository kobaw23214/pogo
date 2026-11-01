package information

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func reuseAsset(t *testing.T, s *Store, name, text string) Asset {
	t.Helper()
	a, err := s.PutAsset(name, "", strings.NewReader(text), MaxAssetBytes)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func reuseCopy(t *testing.T, s *Store, a Asset, w string) string {
	t.Helper()
	p, _, err := s.MaterializeAsset(a.Asset, w)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func reuseBytes(t *testing.T, w, p, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(w, p))
	if err != nil || string(got) != want {
		t.Fatalf("%s bytes=%q error=%v", p, got, err)
	}
}
func reusePlatform(t *testing.T) {
	t.Helper()
	if !materializationReuseSupported {
		t.Skip("independent-copy fallback platform")
	}
}

func TestInformationMaterializationPristineCopyAndEditedReplacement(t *testing.T) {
	reusePlatform(t)
	s := testStore(t)
	w := t.TempDir()
	a := reuseAsset(t, s, "report.txt", "source")
	first := reuseCopy(t, s, a, w)
	if reuseCopy(t, s, a, w) != first {
		t.Fatal("pristine file duplicated")
	}
	original, _, err := s.AssetRead(a.Asset)
	if err != nil {
		t.Fatal(err)
	}
	oi, _ := original.Stat()
	original.Close()
	ci, _ := os.Stat(filepath.Join(w, first))
	if os.SameFile(oi, ci) {
		t.Fatal("workspace copy linked to immutable original")
	}
	if err = os.WriteFile(filepath.Join(w, first), []byte("edited"), 0600); err != nil {
		t.Fatal(err)
	}
	second := reuseCopy(t, s, a, w)
	if second == first || reuseCopy(t, s, a, w) != second {
		t.Fatal("changed copy reused or pristine replacement duplicated")
	}
	reuseBytes(t, w, first, "edited")
	reuseBytes(t, w, second, "source")
	original, _, err = s.AssetRead(a.Asset)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(original)
	original.Close()
	if string(b) != "source" {
		t.Fatal("source changed")
	}
}
func TestInformationMaterializationScopeWorkspaceNameAndHashIsolation(t *testing.T) {
	reusePlatform(t)
	s := testStore(t)
	w := t.TempDir()
	a := reuseAsset(t, s, "report.txt", "one")
	p := reuseCopy(t, s, a, w)
	for _, asset := range []Asset{reuseAsset(t, s, "other.txt", "one"), reuseAsset(t, s, "report.txt", "two")} {
		if reuseCopy(t, s, asset, w) == p {
			t.Fatal("different name/content reused")
		}
	}
	if reuseCopy(t, s, a, t.TempDir()) == p {
		t.Fatal("workspace crossed")
	}
	other := testStore(t)
	b := reuseAsset(t, other, a.Name, "one")
	if reuseCopy(t, other, b, w) == p {
		t.Fatal("scope crossed")
	}
	canonical, err := filepath.EvalSymlinks(w)
	if err != nil {
		t.Fatal(err)
	}
	if reuseCopy(t, s, a, canonical) != p {
		t.Fatal("canonical alias duplicated")
	}
	reuseBytes(t, w, p, "one")
}
func TestInformationMaterializationLegacyOnlyExplicitTrustedWitness(t *testing.T) {
	reusePlatform(t)
	s := testStore(t)
	w := t.TempDir()
	a := reuseAsset(t, s, "legacy.txt", "bytes")
	legacy := "uploads/information/inf_" + strings.Repeat("a", 32) + "/" + a.Name
	if err := os.MkdirAll(filepath.Dir(filepath.Join(w, legacy)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w, legacy), []byte("bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	batch, err := s.BeginMaterialization(w, "../escape", legacy)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Rollback()
	v, err := batch.MaterializeAsset(a.Asset)
	if err != nil || v.Created || v.Path != legacy {
		t.Fatal(v, err)
	}
	if err = batch.Commit(); err != nil {
		t.Fatal(err)
	}
	if reuseCopy(t, s, a, w) != legacy {
		t.Fatal("trusted witness not registered")
	}
	b := reuseAsset(t, s, "unattributed.txt", "other")
	unknown := "uploads/information/inf_" + strings.Repeat("b", 32) + "/" + b.Name
	if err = os.MkdirAll(filepath.Dir(filepath.Join(w, unknown)), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(w, unknown), []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if reuseCopy(t, s, b, w) == unknown {
		t.Fatal("unattributed file was scanned")
	}
}
func TestInformationMaterializationRejectsSymlinkAndHardlinkCandidates(t *testing.T) {
	reusePlatform(t)
	for _, kind := range []string{"file-symlink", "directory-symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			s := testStore(t)
			w := t.TempDir()
			a := reuseAsset(t, s, "safe.txt", "source")
			p := reuseCopy(t, s, a, w)
			outside := t.TempDir()
			external := filepath.Join(outside, a.Name)
			if err := os.WriteFile(external, []byte("source"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "file-symlink":
				if err := os.Remove(filepath.Join(w, p)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, filepath.Join(w, p)); err != nil {
					t.Skip(err)
				}
			case "directory-symlink":
				dir := filepath.Dir(filepath.Join(w, p))
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, dir); err != nil {
					t.Skip(err)
				}
			case "hardlink":
				if err := os.Remove(external); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(filepath.Join(w, p), external); err != nil {
					t.Skip(err)
				}
			}
			fresh := reuseCopy(t, s, a, w)
			if fresh == p {
				t.Fatal("aliased candidate reused")
			}
			reuseBytes(t, w, fresh, "source")
			data, err := os.ReadFile(external)
			if err != nil || string(data) != "source" {
				t.Fatal("external bytes changed", err)
			}
		})
	}
}
func TestInformationMaterializationRollbackOnlyUnpublishedStages(t *testing.T) {
	reusePlatform(t)
	s := testStore(t)
	w := t.TempDir()
	a := reuseAsset(t, s, "existing.txt", "existing")
	b := reuseAsset(t, s, "new.txt", "new")
	old := reuseCopy(t, s, a, w)
	batch, err := s.BeginMaterialization(w)
	if err != nil {
		t.Fatal(err)
	}
	borrow, err := batch.MaterializeAsset(a.Asset)
	if err != nil || borrow.Created || borrow.Path != old {
		t.Fatal(borrow, err)
	}
	fresh, err := batch.MaterializeAsset(b.Asset)
	if err != nil || !fresh.Created {
		t.Fatal(fresh, err)
	}
	if _, err = os.Stat(filepath.Join(w, fresh.Path)); !os.IsNotExist(err) {
		t.Fatal("copy visible before publication", err)
	}
	batch.Rollback()
	reuseBytes(t, w, old, "existing")
	if found, _ := filepath.Glob(filepath.Join(w, "uploads/information/.pending-*")); len(found) != 0 {
		t.Fatal("pending not cleaned", found)
	}
	batch, err = s.BeginMaterialization(w)
	if err != nil {
		t.Fatal(err)
	}
	first, err := batch.MaterializeAsset(b.Asset)
	if err != nil {
		t.Fatal(err)
	}
	c := reuseAsset(t, s, "collision.txt", "new")
	second, err := batch.MaterializeAsset(c.Asset)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(filepath.Join(w, second.Path))
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "unrelated"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = batch.Commit(); err == nil {
		t.Fatal("existing destination overwritten")
	}
	batch.Rollback()
	reuseBytes(t, w, first.Path, "new")
	reuseBytes(t, w, filepath.Join(filepath.Dir(second.Path), "unrelated"), "keep")
	if reuseCopy(t, s, b, w) != first.Path {
		t.Fatal("already published copy disappeared after rollback")
	}
}
func TestInformationMaterializationRefusesLateEditAndPartialStage(t *testing.T) {
	reusePlatform(t)
	s := testStore(t)
	w := t.TempDir()
	a := reuseAsset(t, s, "a.txt", "source")
	p := reuseCopy(t, s, a, w)
	batch, err := s.BeginMaterialization(w)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = batch.MaterializeAsset(a.Asset); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(w, p), []byte("edited"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = batch.Commit(); err == nil {
		t.Fatal("late edited borrowed path accepted")
	}
	batch.Rollback()
	reuseBytes(t, w, p, "edited")
	batch, err = s.BeginMaterialization(w)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = batch.MaterializeAsset(a.Asset); err != nil {
		t.Fatal(err)
	}
	pending := batch.pending[0]
	if err = os.WriteFile(filepath.Join(w, pending.staging, a.Name), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = batch.Commit(); err == nil {
		t.Fatal("partial stage published")
	}
	batch.Rollback()
	if _, err = os.Stat(filepath.Join(w, pending.destination)); !os.IsNotExist(err) {
		t.Fatal("invalid public copy exists", err)
	}
}
func TestInformationMaterializationConcurrentReferencesOneCopy(t *testing.T) {
	reusePlatform(t)
	s := testStore(t)
	w := t.TempDir()
	a := reuseAsset(t, s, "shared.txt", "source")
	const count = 16
	results := make(chan string, count)
	errs := make(chan error, count)
	var group sync.WaitGroup
	for range count {
		group.Go(func() {
			p, _, err := s.MaterializeAsset(a.Asset, w)
			if err != nil {
				errs <- err
			} else {
				results <- p
			}
		})
	}
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	first := ""
	for p := range results {
		if first != "" && p != first {
			t.Fatal("concurrent pristine copies duplicated")
		}
		first = p
	}
	reuseBytes(t, w, first, "source")
}
func TestInformationMaterializationChildHelper(t *testing.T) {
	if os.Getenv("POGO_MATERIALIZATION_CHILD") != "1" {
		return
	}
	s, err := Open(os.Getenv("POGO_MATERIALIZATION_ROOT"), os.Getenv("POGO_MATERIALIZATION_SCOPE"))
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := s.MaterializeAsset(os.Getenv("POGO_MATERIALIZATION_ASSET"), os.Getenv("POGO_MATERIALIZATION_WORKSPACE"))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("MATERIALIZATION_PATH=" + p)
}
func TestInformationMaterializationSeparateProcessWaitsForCommit(t *testing.T) {
	reusePlatform(t)
	s := testStore(t)
	w := t.TempDir()
	a := reuseAsset(t, s, "process.txt", "source")
	batch, err := s.BeginMaterialization(w)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Rollback()
	first, err := batch.MaterializeAsset(a.Asset)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestInformationMaterializationChildHelper$")
	cmd.Env = append(os.Environ(), "POGO_MATERIALIZATION_CHILD=1", "POGO_MATERIALIZATION_ROOT="+s.Root(), "POGO_MATERIALIZATION_SCOPE="+s.ScopeID(), "POGO_MATERIALIZATION_ASSET="+a.Asset, "POGO_MATERIALIZATION_WORKSPACE="+w)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Fatal("child escaped batch lock", err, out.String())
	case <-time.After(100 * time.Millisecond):
	}
	if err = batch.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err, out.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child stuck after Commit")
	}
	if !strings.Contains(out.String(), "MATERIALIZATION_PATH="+first.Path+"\n") {
		t.Fatal("child did not reuse committed path", out.String())
	}
}
