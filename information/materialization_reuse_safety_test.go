//go:build linux || darwin

package information

// Independent acceptance of public copy/reuse contracts. These fixtures never
// inspect a private cache schema and never use real user workspaces.
import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func safetyAsset(t *testing.T, s *Store, name, body string) Asset {
	t.Helper()
	a, e := s.PutAsset(name, "", strings.NewReader(body), MaxAssetBytes)
	if e != nil {
		t.Fatal(e)
	}
	return a
}
func safetyMaterialize(t *testing.T, s *Store, a Asset, workspace string) string {
	t.Helper()
	p, got, e := s.MaterializeAsset(a.Asset, workspace)
	if e != nil {
		t.Fatal(e)
	}
	if got.Asset != a.Asset || got.Name != a.Name {
		t.Fatalf("wrong source metadata: %+v", got)
	}
	return p
}
func safetyRead(t *testing.T, workspace, p, want string) {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(p)))
	if e != nil || string(b) != want {
		t.Fatalf("workspace copy %q: %q %v", p, b, e)
	}
}

func TestMaterializationReuseSafetyContentNameAndEditedCopies(t *testing.T) {
	s := testStore(t)
	w := t.TempDir()
	a := safetyAsset(t, s, "report.csv", "a,b\n1,2\n")
	p := safetyMaterialize(t, s, a, w)
	if q := safetyMaterialize(t, s, a, w); q != p {
		t.Fatal("same trusted asset allocated a second copy")
	}
	otherName := safetyAsset(t, s, "renamed.csv", "a,b\n1,2\n")
	named := safetyMaterialize(t, s, otherName, w)
	if named == p || filepath.Base(named) != "renamed.csv" {
		t.Fatal("same bytes with a different display filename were conflated")
	}
	changed := safetyAsset(t, s, "report.csv", "a,b\n3,4\n")
	q := safetyMaterialize(t, s, changed, w)
	if q == p {
		t.Fatal("new source bytes reused a prior source version")
	}
	safetyRead(t, w, p, "a,b\n1,2\n")
	safetyRead(t, w, q, "a,b\n3,4\n")
	if e := os.WriteFile(filepath.Join(w, p), []byte("USER EDIT"), 0600); e != nil {
		t.Fatal(e)
	}
	fresh := safetyMaterialize(t, s, a, w)
	if fresh == p {
		t.Fatal("edited workspace copy reused")
	}
	safetyRead(t, w, p, "USER EDIT")
	safetyRead(t, w, fresh, "a,b\n1,2\n")
	if again := safetyMaterialize(t, s, a, w); again != fresh {
		t.Fatal("clean replacement should become stable reusable copy")
	}
}

func TestMaterializationReuseSafetyTrustedWorkspaceAndScopeIsolation(t *testing.T) {
	s := testStore(t)
	a := safetyAsset(t, s, "report.txt", "same source bytes")
	one, two := t.TempDir(), t.TempDir()
	p := safetyMaterialize(t, s, a, one)
	q := safetyMaterialize(t, s, a, two)
	if p == q {
		t.Fatal("different workspaces shared the same cached relative identity")
	}
	safetyRead(t, one, p, "same source bytes")
	safetyRead(t, two, q, "same source bytes")
	foreign := testStore(t)
	b := safetyAsset(t, foreign, "report.txt", "same source bytes")
	x := safetyMaterialize(t, foreign, b, one)
	if x == p {
		t.Fatal("different source owner scope borrowed another scope's copy")
	}
	if safetyMaterialize(t, s, a, one) != p {
		t.Fatal("foreign scope changed original mapping")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if e := os.Symlink(one, alias); e != nil {
		t.Fatal(e)
	}
	if _, _, e := s.MaterializeAsset(a.Asset, alias); e == nil {
		t.Fatal("symlink workspace was accepted as a trusted identity")
	}
}

func TestMaterializationReuseSafetyNoPartialPublicationAndRollback(t *testing.T) {
	s := testStore(t)
	w := t.TempDir()
	good := safetyAsset(t, s, "good.txt", "original good")
	bad := safetyAsset(t, s, "bad.txt", "original bad")
	if e := os.WriteFile(filepath.Join(s.Root(), "assets", bad.SHA256, "data"), []byte("corrupt bad!"), 0600); e != nil {
		t.Fatal(e)
	}
	batch, e := s.BeginMaterialization(w)
	if e != nil {
		t.Fatal(e)
	}
	defer batch.Rollback()
	pending, e := batch.MaterializeAsset(good.Asset)
	if e != nil {
		t.Fatal(e)
	}
	if !pending.Created {
		t.Fatal("new source unexpectedly borrowed")
	}
	if _, e = os.Lstat(filepath.Join(w, pending.Path)); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("new materialization exposed its public path before commit", e)
	}
	if _, e = batch.MaterializeAsset(bad.Asset); e == nil {
		t.Fatal("corrupt second asset was accepted")
	}
	batch.Rollback()
	if _, e = os.Lstat(filepath.Join(w, pending.Path)); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("failed transaction published a new copy", e)
	}
	published := safetyMaterialize(t, s, good, w)
	borrow, e := s.BeginMaterialization(w)
	if e != nil {
		t.Fatal(e)
	}
	defer borrow.Rollback()
	got, e := borrow.MaterializeAsset(good.Asset)
	if e != nil || got.Created || got.Path != published {
		t.Fatal("expected previously published borrowed copy", got, e)
	}
	if _, e = borrow.MaterializeAsset(bad.Asset); e == nil {
		t.Fatal("corrupt later part was accepted")
	}
	borrow.Rollback()
	safetyRead(t, w, published, "original good")
	if safetyMaterialize(t, s, good, w) != published {
		t.Fatal("failure removed or invalidated another request's public copy")
	}
}

func TestMaterializationReuseSafetyCandidateSymlinkHardlinkAndTruncation(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			s := testStore(t)
			w := t.TempDir()
			body := "original bytes"
			a := safetyAsset(t, s, "reference.txt", body)
			p := safetyMaterialize(t, s, a, w)
			target := filepath.Join(t.TempDir(), "outside.txt")
			if e := os.WriteFile(target, []byte(body), 0600); e != nil {
				t.Fatal(e)
			}
			if e := os.Remove(filepath.Join(w, p)); e != nil {
				t.Fatal(e)
			}
			var e error
			switch kind {
			case "symlink":
				e = os.Symlink(target, filepath.Join(w, p))
			case "hardlink":
				e = os.Link(target, filepath.Join(w, p))
			default:
				e = os.WriteFile(filepath.Join(w, p), []byte("short"), 0600)
			}
			if e != nil {
				t.Fatal(e)
			}
			q, _, e := s.MaterializeAsset(a.Asset, w)
			if e == nil {
				if q == p {
					t.Fatal("unsafe or edited candidate reused")
				}
				safetyRead(t, w, q, body)
				st, _ := os.Lstat(filepath.Join(w, q))
				outside, _ := os.Stat(target)
				if st == nil || !st.Mode().IsRegular() || os.SameFile(st, outside) {
					t.Fatal("replacement still aliases outside inode")
				}
			}
			if b, e := os.ReadFile(target); e != nil || string(b) != body {
				t.Fatal("source/candidate attack changed unrelated target", e)
			}
		})
	}
}

func TestMaterializationReuseSafetyBorrowedCopyRevalidatedAtCommit(t *testing.T) {
	s := testStore(t)
	w := t.TempDir()
	a := safetyAsset(t, s, "report.txt", "original")
	p := safetyMaterialize(t, s, a, w)
	batch, e := s.BeginMaterialization(w)
	if e != nil {
		t.Fatal(e)
	}
	defer batch.Rollback()
	m, e := batch.MaterializeAsset(a.Asset)
	if e != nil || m.Created || m.Path != p {
		t.Fatal(m, e)
	}
	if e := os.WriteFile(filepath.Join(w, p), []byte("late user edit"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = batch.Commit(); e == nil {
		t.Fatal("commit claimed a borrowed copy that changed after validation")
	}
	batch.Rollback()
	safetyRead(t, w, p, "late user edit")
}

func TestMaterializationReuseSafetyConcurrentStoreHandlesPublishOneCopy(t *testing.T) {
	s := testStore(t)
	other, e := Open(s.Root(), s.ScopeID())
	if e != nil {
		t.Fatal(e)
	}
	w := t.TempDir()
	body := strings.Repeat("concurrent source\n", 4096)
	a := safetyAsset(t, s, "bulk.txt", body)
	const count = 24
	start := make(chan struct{})
	paths := make(chan string, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			store := s
			if i%2 == 0 {
				store = other
			}
			p, _, e := store.MaterializeAsset(a.Asset, w)
			if e != nil {
				errs <- e
				return
			}
			data, e := os.ReadFile(filepath.Join(w, p))
			if e != nil {
				errs <- e
				return
			}
			if string(data) != body {
				errs <- errors.New("returned partially written or wrong bytes")
				return
			}
			paths <- p
		}(i)
	}
	close(start)
	wg.Wait()
	close(paths)
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	distinct := map[string]bool{}
	n := 0
	for p := range paths {
		distinct[p] = true
		n++
	}
	if n != count || len(distinct) != 1 {
		t.Fatalf("concurrent reuse produced %d successful responses and %d copies", n, len(distinct))
	}
}
