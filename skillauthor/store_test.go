package skillauthor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testStore(t *testing.T, scope string) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "private"), strings.Repeat(scope, 64))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func files(name, body string) []File {
	return []File{{Path: "SKILL.md", Content: "---\nname: " + name + "\ndescription: A reusable test workflow.\n---\n" + body + "\n"}}
}
func save(t *testing.T, s *Store, in SaveRequest) SaveResult {
	t.Helper()
	v, e := s.Save(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func yes() *bool { v := true; return &v }
func no() *bool  { v := false; return &v }

func TestLazyAndScopeIsolation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	a, e := New(root, strings.Repeat("a", 64))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(root); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("New created storage: %v", e)
	}
	if list, e := a.List(); e != nil || len(list) != 0 {
		t.Fatal(list, e)
	}
	b, e := New(root, strings.Repeat("b", 64))
	if e != nil {
		t.Fatal(e)
	}
	first := save(t, a, SaveRequest{RequestID: "create-a", Files: files("same-name", "Do the task.")})
	second := save(t, b, SaveRequest{RequestID: "create-b", Files: files("same-name", "Other owner task.")})
	if first.Skill.ID == second.Skill.ID || !first.Skill.Enabled {
		t.Fatal("IDs or default activation are wrong")
	}
	if _, e = b.Read(first.Skill.ID, ""); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if list, e := a.List(); e != nil || len(list) != 1 || list[0].ID != first.Skill.ID {
		t.Fatal(list, e)
	}
	st, e := os.Stat(filepath.Join(root, a.filename()))
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatal(st, e)
	}
}
func TestIdempotencyImmutableVersionsCASAndRestart(t *testing.T) {
	s := testStore(t, "a")
	in := SaveRequest{RequestID: "create", Files: files("example", "Do this."), Activate: no()}
	v1 := save(t, s, in)
	if v1.Skill.Enabled || v1.Reused {
		t.Fatal(v1)
	}
	repeat := save(t, s, in)
	if !repeat.Reused || repeat.Skill.ID != v1.Skill.ID || repeat.Skill.Revision != v1.Skill.Revision {
		t.Fatal(repeat)
	}
	changed := in
	changed.Files = files("example", "Different payload.")
	if _, e := s.Save(context.Background(), changed); !errors.Is(e, ErrRequestConflict) {
		t.Fatal(e)
	}
	update := SaveRequest{RequestID: "update", SkillID: v1.Skill.ID, ExpectedRevision: v1.Skill.Revision, Files: files("example", "Do the revised task."), Activate: yes()}
	v2 := save(t, s, update)
	if v2.Skill.Version != 2 || !v2.Skill.Enabled || v2.Skill.Revision == v1.Skill.Revision || !v2.Skill.CreatedAt.Equal(v1.Skill.CreatedAt) {
		t.Fatal(v2)
	}
	if _, e := s.Save(context.Background(), SaveRequest{RequestID: "stale", SkillID: v1.Skill.ID, ExpectedRevision: v1.Skill.Revision, Files: files("example", "Stale.")}); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if _, e := s.Save(context.Background(), SaveRequest{RequestID: "rename", SkillID: v1.Skill.ID, ExpectedRevision: v2.Skill.Revision, Files: files("other-name", "Rename.")}); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if _, e := s.Save(context.Background(), SaveRequest{RequestID: "same-name", Files: files("example", "Another one.")}); !errors.Is(e, ErrNameConflict) {
		t.Fatal(e)
	}
	reopened, e := New(s.root, s.scope)
	if e != nil {
		t.Fatal(e)
	}
	old, e := reopened.Read(v1.Skill.ID, v1.Skill.Revision)
	if e != nil || old.Version != 1 || old.Enabled {
		t.Fatal(old, e)
	}
	latest, e := reopened.Read(v1.Skill.ID, "")
	if e != nil || latest.Version != 2 {
		t.Fatal(latest, e)
	}
	versions, e := reopened.Versions(v1.Skill.ID)
	if e != nil || len(versions) != 2 || versions[0].Revision != v1.Skill.Revision || versions[1].Revision != v2.Skill.Revision {
		t.Fatal(versions, e)
	}
	versions[0].Files[0].Content = "must not mutate persisted history"
	old.Files[0].Content = "mutate returned value"
	old2, e := reopened.Read(v1.Skill.ID, v1.Skill.Revision)
	if e != nil || old2.Files[0].Content != in.Files[0].Content {
		t.Fatal("returned snapshot is mutable", e)
	}
	retry := save(t, reopened, in)
	if !retry.Reused || retry.Skill.Version != 1 {
		t.Fatal("retry must return original receipt, never latest", retry)
	}
}
func TestNoopAndActivationVersions(t *testing.T) {
	s := testStore(t, "a")
	v := save(t, s, SaveRequest{RequestID: "create", Files: files("example", "Workflow.")})
	same := save(t, s, SaveRequest{RequestID: "no-op", SkillID: v.Skill.ID, ExpectedRevision: v.Skill.Revision, Files: v.Skill.Files})
	if !same.Reused || same.Skill.Version != 1 {
		t.Fatal(same)
	}
	off := save(t, s, SaveRequest{RequestID: "disable", SkillID: v.Skill.ID, ExpectedRevision: v.Skill.Revision, Files: v.Skill.Files, Activate: no()})
	if off.Skill.Enabled || off.Skill.Version != 2 || off.Skill.Revision == v.Skill.Revision {
		t.Fatal(off)
	}
	preserve := save(t, s, SaveRequest{RequestID: "nil-preserve", SkillID: v.Skill.ID, ExpectedRevision: off.Skill.Revision, Files: files("example", "Updated draft.")})
	if preserve.Skill.Enabled {
		t.Fatal(preserve)
	}
}
func TestConcurrentSameRequestAndCAS(t *testing.T) {
	s := testStore(t, "a")
	other, e := New(s.root, s.scope)
	if e != nil {
		t.Fatal(e)
	}
	in := SaveRequest{RequestID: "create-once", Files: files("concurrent", "Workflow.")}
	var wg sync.WaitGroup
	results := make(chan SaveResult, 2)
	errs := make(chan error, 2)
	for _, store := range []*Store{s, other} {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			v, e := store.Save(context.Background(), in)
			results <- v
			errs <- e
		}(store)
	}
	wg.Wait()
	for range 2 {
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
	}
	a, b := <-results, <-results
	if a.Skill.ID != b.Skill.ID || a.Reused == b.Reused {
		t.Fatal(a, b)
	}
	for i, store := range []*Store{s, other} {
		wg.Add(1)
		go func(i int, store *Store) {
			defer wg.Done()
			_, e := store.Save(context.Background(), SaveRequest{RequestID: fmt.Sprintf("cas-%d", i), SkillID: a.Skill.ID, ExpectedRevision: a.Skill.Revision, Files: files("concurrent", fmt.Sprintf("Version %d", i))})
			errs <- e
		}(i, store)
	}
	wg.Wait()
	success, conflict := 0, 0
	for range 2 {
		e := <-errs
		if e == nil {
			success++
		} else if errors.Is(e, ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
}
func TestInvalidPathsFlagsAndMetadata(t *testing.T) {
	cases := []struct {
		name   string
		bundle []File
	}{
		{"script", append(files("example", "Instructions."), File{Path: "scripts/run.py", Content: "print('x')"})},
		{"manifest", append(files("example", "Instructions."), File{Path: "tools.json", Content: "{}"})},
		{"escape", append(files("example", "Instructions."), File{Path: "references/../../other.md", Content: "x"})},
		{"absolute", append(files("example", "Instructions."), File{Path: "/SKILL.md", Content: "x"})},
		{"backslash", append(files("example", "Instructions."), File{Path: `references\other.md`, Content: "x"})},
		{"case-duplicate", append(files("example", "Instructions."), File{Path: "references/a.md", Content: "x"}, File{Path: "references/A.md", Content: "x"})},
		{"overlap", append(files("example", "Instructions."), File{Path: "references/a.md", Content: "x"}, File{Path: "references/a.md/other.txt", Content: "x"})},
		{"binary", append(files("example", "Instructions."), File{Path: "references/a.txt", Content: "x\x00y"})},
		{"invalidutf8", append(files("example", "Instructions."), File{Path: "references/a.txt", Content: string([]byte{0xff})})},
		{"hidden", append(files("example", "Instructions."), File{Path: "references/.private.md", Content: "x"})},
	}
	for _, field := range []string{"browser-task: true", "disable-model-invocation: true", "allowed-tools: bash", "tools: []", "name: example", "description: duplicate", "mcp: service", "compatibility: [linux]", "license: &anchor MIT", "license: \"unterminated", "description: {name: value}"} {
		manifest := strings.Replace(files("example", "Instructions.")[0].Content, "---\nInstructions.", field+"\n---\nInstructions.", 1)
		cases = append(cases, struct {
			name   string
			bundle []File
		}{field, []File{{Path: "SKILL.md", Content: manifest}}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, e := ValidateFiles(tc.bundle); !errors.Is(e, ErrInvalid) {
				t.Fatalf("unexpected validation: %v", e)
			}
		})
	}
}
func TestLinksAndSupportedTextResources(t *testing.T) {
	valid := files("example", "Read [reference](references/%E7%A4%BA%E4%BE%8B.md#section), [data][d] and [web](https://example.com).\n[d]: references/data.csv\n`[not a link](missing.txt)`\n```md\n[example](also-missing.md)\n```")
	valid = append(valid, File{Path: "references/示例.md", Content: "[back](../SKILL.md)"}, File{Path: "references/data.csv", Content: "a,b\n1,2"}, File{Path: "references/config.yml", Content: "x: y"}, File{Path: "references/query.sql", Content: "SELECT 1"})
	if _, _, e := ValidateFiles(valid); e != nil {
		t.Fatal(e)
	}
	for _, link := range []string{"missing.md", "../foreign.md", "/etc/passwd", "file:///etc/passwd", "javascript:alert", "//example.com/x", "references/%2e%2e/%2e%2e/foreign.md"} {
		t.Run(link, func(t *testing.T) {
			if _, _, e := ValidateFiles(files("example", "[reference]("+link+")")); !errors.Is(e, ErrInvalid) {
				t.Fatal(e)
			}
		})
	}
	block := []File{{Path: "SKILL.md", Content: "---\nname: example\ndescription: >-\n  A longer reusable\n  description.\nlicense: MIT\ncompatibility: Existing tools only.\n---\nDo this.\n"}}
	if _, _, e := ValidateFiles(block); e != nil {
		t.Fatal(e)
	}
}
func TestBudgetsAndInvalidRequests(t *testing.T) {
	s := testStore(t, "a")
	for _, req := range []SaveRequest{{RequestID: "", Files: files("example", "Do it.")}, {RequestID: "../escape", Files: files("example", "Do it.")}, {RequestID: "new", ExpectedRevision: strings.Repeat("a", 64), Files: files("example", "Do it.")}, {RequestID: "bad-id", SkillID: "../anything", Files: files("example", "Do it.")}} {
		if _, e := s.Save(context.Background(), req); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
	if _, _, e := ValidateFiles(append(files("example", "Do it."), File{Path: "references/large.txt", Content: strings.Repeat("x", MaxFileBytes+1)})); !errors.Is(e, ErrLimit) {
		t.Fatal(e)
	}
	tooMany := files("example", "Do it.")
	for i := 0; i < MaxFiles; i++ {
		tooMany = append(tooMany, File{Path: fmt.Sprintf("references/%d.txt", i), Content: "x"})
	}
	if _, _, e := ValidateFiles(tooMany); !errors.Is(e, ErrLimit) {
		t.Fatal(e)
	}
	tooLarge := files("example", "Do it.")
	for i := 0; i < 4; i++ {
		tooLarge = append(tooLarge, File{Path: fmt.Sprintf("references/%d.txt", i), Content: strings.Repeat("x", MaxFileBytes)})
	}
	if _, _, e := ValidateFiles(tooLarge); !errors.Is(e, ErrLimit) {
		t.Fatal(e)
	}
	v := save(t, s, SaveRequest{RequestID: "create", Files: files("example", "Initial.")})
	for i := 2; i <= MaxVersions; i++ {
		v = save(t, s, SaveRequest{RequestID: fmt.Sprintf("version-%d", i), SkillID: v.Skill.ID, ExpectedRevision: v.Skill.Revision, Files: files("example", fmt.Sprintf("Revision %d", i))})
	}
	if _, e := s.Save(context.Background(), SaveRequest{RequestID: "over-limit", SkillID: v.Skill.ID, ExpectedRevision: v.Skill.Revision, Files: files("example", "One too many.")}); !errors.Is(e, ErrLimit) {
		t.Fatal(e)
	}
}
func TestSymlinkCorruptionAndWriteFailurePreserveStore(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(base, "link")
	if e := os.Symlink(outside, link); e != nil {
		t.Fatal(e)
	}
	if _, e := New(filepath.Join(link, "child"), strings.Repeat("a", 64)); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	s := testStore(t, "a")
	v := save(t, s, SaveRequest{RequestID: "create", Files: files("example", "Original.")})
	file := filepath.Join(s.root, s.filename())
	before, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	r, e := os.OpenRoot(s.root)
	if e != nil {
		t.Fatal(e)
	}
	d, e := s.load(r)
	if e != nil {
		t.Fatal(e)
	}
	r.Close()
	if e = s.commit(context.Background(), r, d); e == nil {
		t.Fatal("closed root accepted commit")
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Fatal("write failure changed previous store")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = s.Save(ctx, SaveRequest{RequestID: "cancelled", SkillID: v.Skill.ID, ExpectedRevision: v.Skill.Revision, Files: files("example", "Never save.")}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	after, _ = os.ReadFile(file)
	if string(before) != string(after) {
		t.Fatal("cancelled save changed store")
	}
	if e = os.Rename(file, file+".original"); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(file+".original", file); e != nil {
		t.Fatal(e)
	}
	if _, e = s.List(); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if e = os.Remove(file); e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(file+".original", file); e != nil {
		t.Fatal(e)
	}
	var decoded map[string]any
	if e = json.Unmarshal(before, &decoded); e != nil {
		t.Fatal(e)
	}
	decoded["scope"] = strings.Repeat("b", 64)
	corrupt, _ := json.Marshal(decoded)
	if e = os.WriteFile(file, corrupt, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Save(context.Background(), SaveRequest{RequestID: "must-not-repair", Files: files("other", "Do this.")}); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	after, _ = os.ReadFile(file)
	if string(after) != string(corrupt) {
		t.Fatal("invalid store overwritten")
	}
}

func TestReceiptAndScopeBudgetsPreserveExistingVersions(t *testing.T) {
	s := testStore(t, "a")
	v := save(t, s, SaveRequest{RequestID: "create", Files: files("example", "Original.")})
	r, err := os.OpenRoot(s.root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	d, err := s.load(r)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(d.Requests); i < MaxRequests; i++ {
		d.Requests[fmt.Sprintf("receipt-%d", i)] = receipt{Payload: strings.Repeat("a", 64), SkillID: v.Skill.ID, Revision: v.Skill.Revision}
	}
	if err = s.commit(context.Background(), r, d); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(context.Background(), SaveRequest{RequestID: "too-many-receipts", SkillID: v.Skill.ID, ExpectedRevision: v.Skill.Revision, Files: files("example", "Cannot save.")}); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	file := filepath.Join(s.root, s.filename())
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	large := emptyData(s.scope)
	large.Skills[v.Skill.ID] = []Snapshot{{Files: []File{{Path: "SKILL.md", Content: strings.Repeat("x", MaxScopeBytes)}}}}
	if err = s.commit(context.Background(), r, large); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	after, err := os.ReadFile(file)
	if err != nil || string(before) != string(after) {
		t.Fatal("scope limit changed existing store", err)
	}
	old, err := s.Read(v.Skill.ID, v.Skill.Revision)
	if err != nil || old.Revision != v.Skill.Revision {
		t.Fatal(old, err)
	}
}
