package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/identity"

	_ "modernc.org/sqlite"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	return openAt(t, filepath.Join(t.TempDir(), "catalog", "catalog.db"))
}

func openAt(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func alice() identity.Scope {
	return identity.Scope{AppID: "app", TenantID: "tenant", SubjectID: "alice", ProjectID: "proj"}
}

func bob() identity.Scope {
	return identity.Scope{AppID: "app", TenantID: "tenant", SubjectID: "bob", ProjectID: "proj"}
}

// put inserts an active session with explicit timestamps so ordering and
// round-tripping are deterministic.
func put(t *testing.T, s *Store, scope identity.Scope, id, title string, at int64) {
	t.Helper()
	err := s.Create(context.Background(), Session{
		ID: id, Scope: scope, Title: title,
		State: stateActive, CreatedAt: at, UpdatedAt: at,
	})
	if err != nil {
		t.Fatalf("create %s for %s: %v", id, scope.SubjectID, err)
	}
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("Open(\"\") succeeded, want error")
	}
}

func TestOpenExistingNeverInitializesAuthority(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.db")
	if s, err := OpenExisting(path); err == nil {
		s.Close()
		t.Fatal("missing catalog accepted")
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenExisting(path); err == nil {
		s.Close()
		t.Fatal("empty catalog accepted")
	}
	// Open is reserved for an explicitly new data root.
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	put(t, s, alice(), "owned", "private", 1)
	s.Close()
	s, err = OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Get(context.Background(), alice(), "owned"); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCreatesParentDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "catalog.db")
	s := openAt(t, path)
	put(t, s, alice(), "s1", "title", 1)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("db mode is %v, want 0600", info.Mode().Perm())
		}
	}
}

func TestCreateValidationAndDefaults(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	if err := s.Create(ctx, Session{ID: "s1", Scope: identity.Scope{}}); err == nil {
		t.Error("create with zero scope succeeded, want rejection")
	}
	for _, id := range []string{"", "has space", "slash/id", "dot.id", strings.Repeat("a", 129), "中文"} {
		err := s.Create(ctx, Session{ID: id, Scope: alice(), Title: "t"})
		if err == nil {
			t.Errorf("create with id %q succeeded, want rejection", id)
		}
	}
	for _, state := range []string{stateDeleted, "ACTIVE", "finished"} {
		err := s.Create(ctx, Session{ID: "s1", Scope: alice(), State: state})
		if err == nil {
			t.Errorf("create with state %q succeeded, want rejection", state)
		}
	}

	// Empty state defaults to creating: invisible to reads until Activate.
	if err := s.Create(ctx, Session{ID: "d1", Scope: alice(), Title: "defaults"}); err != nil {
		t.Fatalf("create with empty state: %v", err)
	}
	if _, err := s.Get(ctx, alice(), "d1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get creating session: got %v, want ErrNotFound", err)
	}
	if err := s.Activate(ctx, alice(), "d1"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	rec, err := s.Get(ctx, alice(), "d1")
	if err != nil {
		t.Fatalf("get after activate: %v", err)
	}
	if rec.CreatedAt <= 0 || rec.UpdatedAt <= 0 {
		t.Errorf("zero timestamps not defaulted: created %d updated %d", rec.CreatedAt, rec.UpdatedAt)
	}

	// Explicit timestamps round-trip untouched.
	put(t, s, alice(), "t1", "explicit", 123456)
	rec, err = s.Get(ctx, alice(), "t1")
	if err != nil {
		t.Fatalf("get t1: %v", err)
	}
	if rec.CreatedAt != 123456 || rec.UpdatedAt != 123456 {
		t.Errorf("explicit timestamps became %d/%d, want 123456", rec.CreatedAt, rec.UpdatedAt)
	}
	if rec.State != stateActive || rec.Title != "explicit" || rec.Scope != alice() {
		t.Errorf("round-trip mismatch: %+v", rec)
	}
}

func TestInvalidScopeRejectedEverywhere(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	zero := identity.Scope{}
	if err := s.Create(ctx, Session{ID: "s", Scope: zero}); err == nil {
		t.Error("create accepted zero scope")
	}
	if _, err := s.Get(ctx, zero, "s"); err == nil {
		t.Error("get accepted zero scope")
	}
	if _, err := s.List(ctx, zero, "", 10); err == nil {
		t.Error("list accepted zero scope")
	}
	if err := s.Activate(ctx, zero, "s"); err == nil {
		t.Error("activate accepted zero scope")
	}
	if err := s.Rename(ctx, zero, "s", "t"); err == nil {
		t.Error("rename accepted zero scope")
	}
	if err := s.Tombstone(ctx, zero, "s"); err == nil {
		t.Error("tombstone accepted zero scope")
	}
}

func TestCrossScopeSameIDInvisible(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	put(t, s, alice(), "shared-id", "alice's title", 100)

	// The ID is global: a second scope cannot claim it, live or later.
	if err := s.Create(ctx, Session{ID: "shared-id", Scope: bob(), State: stateActive}); !errors.Is(err, ErrConflict) {
		t.Errorf("second scope claiming the same id: got %v, want ErrConflict", err)
	}

	// Every read and write through bob's scope behaves as if the row did not
	// exist — including the error, which must not hint that it does.
	if _, err := s.Get(ctx, bob(), "shared-id"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-scope get: got %v, want ErrNotFound", err)
	}
	got, err := s.List(ctx, bob(), "alice", 10)
	if err != nil || len(got) != 0 {
		t.Errorf("cross-scope list: got %v (err %v), want empty", got, err)
	}
	if err := s.Rename(ctx, bob(), "shared-id", "hijacked"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-scope rename: got %v, want ErrNotFound", err)
	}
	if err := s.Tombstone(ctx, bob(), "shared-id"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-scope tombstone: got %v, want ErrNotFound", err)
	}
	if err := s.Activate(ctx, bob(), "shared-id"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-scope activate: got %v, want ErrNotFound", err)
	}

	// And alice's row is untouched by all of bob's attempts.
	rec, err := s.Get(ctx, alice(), "shared-id")
	if err != nil {
		t.Fatalf("alice get after bob's attempts: %v", err)
	}
	if rec.Title != "alice's title" {
		t.Errorf("title changed to %q by cross-scope attempt", rec.Title)
	}
}

func TestSubjectIDNotGlobal(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	// Same subject ID mapped through different issuers/apps/projects: the
	// same human, perhaps, but unrelated rows. IDs stay globally unique, so
	// each scope registers its own.
	scopes := []identity.Scope{
		{AppID: "app-a", TenantID: "t", SubjectID: "carol", ProjectID: "p"},
		{AppID: "app-b", TenantID: "t", SubjectID: "carol", ProjectID: "p"},
		{AppID: "app-a", TenantID: "other", SubjectID: "carol", ProjectID: "p"},
		{AppID: "app-a", TenantID: "t", SubjectID: "carol", ProjectID: "other"},
	}
	ids := []string{"carol-appb", "carol-other-tenant", "carol-other-project", "carol-base"}
	for i, sc := range scopes {
		if err := s.Create(ctx, Session{ID: ids[i], Scope: sc, Title: fmt.Sprintf("row-%d", i), State: stateActive}); err != nil {
			t.Fatalf("create for scope %d: %v", i, err)
		}
	}
	for i, sc := range scopes {
		rec, err := s.Get(ctx, sc, ids[i])
		if err != nil {
			t.Fatalf("get for scope %d: %v", i, err)
		}
		if rec.Title != fmt.Sprintf("row-%d", i) || rec.Scope != sc {
			t.Errorf("scope %d sees %+v, want its own row", i, rec)
		}
		list, err := s.List(ctx, sc, "", 10)
		if err != nil || len(list) != 1 || list[0].Title != fmt.Sprintf("row-%d", i) {
			t.Errorf("scope %d list: got %v (err %v), want exactly its own row", i, list, err)
		}
		// Another scope's ID is invisible, not merely unowned.
		for j := range scopes {
			if j == i {
				continue
			}
			if _, err := s.Get(ctx, sc, ids[j]); !errors.Is(err, ErrNotFound) {
				t.Errorf("scope %d get of scope %d's id: got %v, want ErrNotFound", i, j, err)
			}
		}
	}
}

func TestIDNeverReassigned(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	put(t, s, alice(), "g1", "alice's", 1)

	// No second scope while the row is live...
	if err := s.Create(ctx, Session{ID: "g1", Scope: bob(), State: stateActive}); !errors.Is(err, ErrConflict) {
		t.Errorf("live id claimed by second scope: got %v, want ErrConflict", err)
	}
	// ...and none after the owner tombstones it: the row stays, the ID stays
	// taken, ownership is never reassigned.
	if err := s.Tombstone(ctx, alice(), "g1"); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	if err := s.Create(ctx, Session{ID: "g1", Scope: bob(), State: stateActive}); !errors.Is(err, ErrConflict) {
		t.Errorf("tombstoned id claimed by second scope: got %v, want ErrConflict", err)
	}
	if err := s.Create(ctx, Session{ID: "g1", Scope: alice(), State: stateActive}); !errors.Is(err, ErrConflict) {
		t.Errorf("tombstoned id re-registered by owner: got %v, want ErrConflict", err)
	}
}

func TestListWildcardQueryIsLiteral(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	put(t, s, alice(), "p1", "100% done", 1)
	put(t, s, alice(), "p2", "under_score", 2)
	put(t, s, alice(), "p3", "axb", 3)
	put(t, s, alice(), "p4", `back\slash`, 4)
	put(t, s, alice(), "p5", "plain", 5)

	for _, q := range []string{"%", "_", `\`} {
		got, err := s.List(ctx, alice(), q, 50)
		if err != nil {
			t.Fatalf("list %q: %v", q, err)
		}
		var want string
		switch q {
		case "%":
			want = "100% done"
		case "_":
			want = "under_score"
		case `\`:
			want = `back\slash`
		}
		if len(got) != 1 || got[0].Title != want {
			t.Errorf("query %q: got %v, want exactly [%s]", q, titles(got), want)
		}
	}

	// Ordinary substrings still match anywhere in the title.
	got, err := s.List(ctx, alice(), "done", 50)
	if err != nil || len(got) != 1 || got[0].Title != "100% done" {
		t.Errorf("substring query: got %v (err %v), want [100%% done]", got, err)
	}
	// A metacharacter mid-query stays literal too.
	got, err = s.List(ctx, alice(), `% d`, 50)
	if err != nil || len(got) != 1 || got[0].Title != "100% done" {
		t.Errorf("literal mid-query: got %v (err %v), want [100%% done]", got, err)
	}
}

func TestListScopeFilteredBeforeLimit(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	// 60 rows each for alice and bob, comparable timestamps, so any list
	// that filtered late would fill its limit with the wrong owner's rows.
	for i := 0; i < 60; i++ {
		put(t, s, alice(), fmt.Sprintf("s%03d", i), fmt.Sprintf("alice-%03d", i), int64(1000+i))
		put(t, s, bob(), fmt.Sprintf("b%03d", i), fmt.Sprintf("bob-%03d", i), int64(5000+i))
	}
	got, err := s.List(ctx, alice(), "", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("got %d rows, want 10", len(got))
	}
	for _, rec := range got {
		if rec.Scope != alice() {
			t.Fatalf("limit applied before scope filter: %+v in alice's list", rec)
		}
	}
	// Newest first, id ascending as tiebreak.
	if got[0].ID != "s059" || got[9].ID != "s050" {
		t.Errorf("order wrong: got %s..%s, want s059..s050", got[0].ID, got[9].ID)
	}

	// Default limit is 50 even when the owner has more.
	got, _ = s.List(ctx, alice(), "", 0)
	if len(got) != defaultListLimit {
		t.Errorf("default limit: got %d rows, want %d", len(got), defaultListLimit)
	}
}

func TestListLimitClampedToMax(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	for i := 0; i < maxListLimit+50; i++ {
		put(t, s, alice(), fmt.Sprintf("s%04d", i), "t", int64(i))
	}
	got, err := s.List(ctx, alice(), "", 1<<30)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != maxListLimit {
		t.Errorf("limit not clamped: got %d rows, want %d", len(got), maxListLimit)
	}
}

func TestListTiebreakByID(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	put(t, s, alice(), "zz", "same time", 777)
	put(t, s, alice(), "aa", "same time", 777)
	got, err := s.List(ctx, alice(), "", 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("list: %v (err %v)", got, err)
	}
	if got[0].ID != "aa" || got[1].ID != "zz" {
		t.Errorf("tiebreak wrong: got %s then %s, want aa then zz", got[0].ID, got[1].ID)
	}
}

func TestStateLifecycle(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	// creating is unreadable.
	if err := s.Create(ctx, Session{ID: "s1", Scope: alice(), Title: "t"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.Get(ctx, alice(), "s1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get creating: got %v, want ErrNotFound", err)
	}
	if got, _ := s.List(ctx, alice(), "", 10); len(got) != 0 {
		t.Errorf("list shows creating session: %v", got)
	}

	// activate makes it readable; re-activation is idempotent.
	if err := s.Activate(ctx, alice(), "s1"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := s.Activate(ctx, alice(), "s1"); err != nil {
		t.Errorf("second activate: %v, want nil", err)
	}
	if _, err := s.Get(ctx, alice(), "s1"); err != nil {
		t.Fatalf("get active: %v", err)
	}

	// tombstone makes it unreadable, permanently.
	if err := s.Tombstone(ctx, alice(), "s1"); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	if _, err := s.Get(ctx, alice(), "s1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get deleted: got %v, want ErrNotFound", err)
	}
	if err := s.Activate(ctx, alice(), "s1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("activate deleted: got %v, want ErrNotFound", err)
	}
	if err := s.Rename(ctx, alice(), "s1", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rename deleted: got %v, want ErrNotFound", err)
	}
	// Re-tombstoning is idempotent for the owner.
	if err := s.Tombstone(ctx, alice(), "s1"); err != nil {
		t.Errorf("second tombstone: %v, want nil", err)
	}

	// The row persists, so neither the former owner nor anyone else can
	// re-register the ID.
	if err := s.Create(ctx, Session{ID: "s1", Scope: alice(), State: stateActive}); !errors.Is(err, ErrConflict) {
		t.Errorf("recreate after delete: got %v, want ErrConflict", err)
	}
	if err := s.Create(ctx, Session{ID: "s1", Scope: bob(), State: stateActive}); !errors.Is(err, ErrConflict) {
		t.Errorf("recreate under another scope after delete: got %v, want ErrConflict", err)
	}

	// A creating session can also be tombstoned directly.
	if err := s.Create(ctx, Session{ID: "s2", Scope: alice()}); err != nil {
		t.Fatalf("create s2: %v", err)
	}
	if err := s.Tombstone(ctx, alice(), "s2"); err != nil {
		t.Fatalf("tombstone creating: %v", err)
	}
}

func TestRestartPreservesOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	s1 := openAt(t, path)
	ctx := context.Background()
	put(t, s1, alice(), "s1", "alice's", 10)
	put(t, s1, bob(), "s2", "bob's", 20)
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2 := openAt(t, path)
	a, err := s2.Get(ctx, alice(), "s1")
	if err != nil || a.Title != "alice's" {
		t.Errorf("alice after restart: %+v (err %v)", a, err)
	}
	b, err := s2.Get(ctx, bob(), "s2")
	if err != nil || b.Title != "bob's" {
		t.Errorf("bob after restart: %+v (err %v)", b, err)
	}
	if got, _ := s2.List(ctx, bob(), "", 10); len(got) != 1 {
		t.Errorf("bob's list after restart: %v, want only his row", got)
	}
}

func TestConcurrentDuplicateCreateConflicts(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	// Half the racers come through another scope: the ID is global, so
	// whoever loses loses the same way.
	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		scope := alice()
		if i%2 == 1 {
			scope = bob()
		}
		wg.Add(1)
		go func(i int, scope identity.Scope) {
			defer wg.Done()
			errs[i] = s.Create(ctx, Session{ID: "race", Scope: scope, Title: "t", State: stateActive})
		}(i, scope)
	}
	wg.Wait()
	wins, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected create error: %v", err)
		}
	}
	if wins != 1 || conflicts != n-1 {
		t.Fatalf("got %d wins, %d conflicts; want exactly 1 win and %d conflicts", wins, conflicts, n-1)
	}
	// Exactly one scope owns the result; the other sees nothing.
	_, errA := s.Get(ctx, alice(), "race")
	_, errB := s.Get(ctx, bob(), "race")
	if (errA == nil) == (errB == nil) {
		t.Errorf("ownership after race: alice err %v, bob err %v; want exactly one to find it", errA, errB)
	}
}

func TestConcurrentMixedOperations(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		put(t, s, alice(), fmt.Sprintf("s%02d", i), "seed", int64(i))
	}
	var wg sync.WaitGroup
	// Concurrent distinct creates: all must land.
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := s.Create(ctx, Session{ID: fmt.Sprintf("c%02d", i), Scope: alice(), State: stateActive})
			if err != nil {
				t.Errorf("concurrent create c%02d: %v", i, err)
			}
		}(i)
	}
	// Concurrent reads and renames of the same rows while writes land.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.List(ctx, alice(), "", 50); err != nil {
				t.Errorf("concurrent list: %v", err)
			}
			if _, err := s.Get(ctx, alice(), fmt.Sprintf("s%02d", i)); err != nil {
				t.Errorf("concurrent get: %v", err)
			}
			if err := s.Rename(ctx, alice(), fmt.Sprintf("s%02d", i), "renamed"); err != nil {
				t.Errorf("concurrent rename: %v", err)
			}
		}(i)
	}
	wg.Wait()
	got, err := s.List(ctx, alice(), "", maxListLimit)
	if err != nil {
		t.Fatalf("final list: %v", err)
	}
	if len(got) != 40 {
		t.Errorf("after concurrent mix: %d rows, want 40", len(got))
	}
}

func TestConcurrentRenameSameRow(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	put(t, s, alice(), "s1", "original", 1)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			title := fmt.Sprintf("title-%d", i)
			if err := s.Rename(ctx, alice(), "s1", title); err != nil {
				t.Errorf("rename %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	rec, err := s.Get(ctx, alice(), "s1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.HasPrefix(rec.Title, "title-") {
		t.Errorf("final title %q is not one of the renames", rec.Title)
	}
}

func TestUnknownSchemaVersionRejected(t *testing.T) {
	for _, version := range []int{2, 999} {
		path := filepath.Join(t.TempDir(), "catalog.db")
		s := openAt(t, path)
		put(t, s, alice(), "s1", "t", 1)
		if err := s.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
		if err != nil {
			t.Fatalf("raw open: %v", err)
		}
		if _, err := raw.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
			t.Fatalf("stamp version: %v", err)
		}
		raw.Close()

		if _, err := Open(path); err == nil {
			t.Errorf("Open accepted future schema version %d", version)
		}
	}
}

func TestForeignDatabaseRejected(t *testing.T) {
	// An unmarked file holding any user table — not just one named
	// "sessions" — is a database this package did not make.
	path := filepath.Join(t.TempDir(), "catalog.db")
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE someone_elses_data (x INTEGER) STRICT`); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	raw.Close()
	if _, err := Open(path); err == nil {
		t.Error("Open adopted an unmarked database holding tables")
	}
}

func TestApplicationIDMismatchRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	s := openAt(t, path)
	put(t, s, alice(), "s1", "t", 1)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec(`PRAGMA application_id = 1111111111`); err != nil {
		t.Fatalf("stamp application_id: %v", err)
	}
	raw.Close()
	if _, err := Open(path); err == nil {
		t.Error("Open accepted a database with a foreign application_id")
	}
}

func TestPragmasHoldOnConnection(t *testing.T) {
	s := openStore(t)
	var journal string
	var synchronous, foreignKeys, busyTimeout int
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if err := s.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatalf("synchronous: %v", err)
	}
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if !strings.EqualFold(journal, "wal") {
		t.Errorf("journal_mode = %q, want wal", journal)
	}
	if synchronous != 2 {
		t.Errorf("synchronous = %d, want 2 (FULL)", synchronous)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}
	if busyTimeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busyTimeout)
	}
}

func TestSpecialCharacterPath(t *testing.T) {
	// '#', spaces, and non-ASCII must stay part of the path rather than
	// becoming a URI fragment or breaking the DSN. '?' is legal on some
	// platforms but not Windows filenames, so it is not exercised here.
	path := filepath.Join(t.TempDir(), "po#go 猫", "sub dir", "catalog#1.db")
	s := openAt(t, path)
	put(t, s, alice(), "s1", "title", 1)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database not at the exact requested path: %v", err)
	}
	s2 := openAt(t, path)
	if _, err := s2.Get(context.Background(), alice(), "s1"); err != nil {
		t.Fatalf("get after reopen over encoded path: %v", err)
	}
}

func TestOpenNonDatabaseRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	if err := os.WriteFile(path, []byte("definitely not a sqlite database"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := Open(path); err == nil {
		t.Error("Open accepted a file that is not a database")
	}
}

func titles(recs []Session) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Title
	}
	return out
}
