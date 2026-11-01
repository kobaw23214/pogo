package web

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// Stale clients must receive 404 without changing the repository or its old
// commit rules, spending a model call, or clearing the file journal.
func TestRetiredGitEndpointsHaveNoSideEffects(t *testing.T) {
	h := filesHarness(t)
	cwd := h.mgr.Cwd()
	makeRetiredGitFixture(t, cwd)
	sid := h.createSession()
	h.setPolicy(sid, ModeAuto, 0)
	h.mgr.Journal().BeforeChange(filepath.Join(cwd, "tracked.txt"), []byte("baseline\n"), true)
	before := snapshotRetiredGitFixture(t, cwd)
	journalBefore := h.mgr.Journal().List()
	body, err := json.Marshal(map[string]any{
		"sid": sid, "paths": []string{"tracked.txt", "fresh.txt"},
		"message": "must not commit", "content": "must not replace old rules",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []struct{ method, path string }{
		{http.MethodGet, "/api/workspace/git"},
		{http.MethodPost, "/api/workspace/commit"},
		{http.MethodPost, "/api/workspace/commit/message"},
		{http.MethodGet, "/api/workspace/commit/rules"},
		{http.MethodPut, "/api/workspace/commit/rules"},
	} {
		resp := h.do(req.method, req.path, string(body))
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", req.method, req.path, resp.StatusCode)
		}
	}
	assertRetiredGitFixtureUnchanged(t, cwd, before)
	if got := h.mgr.Journal().List(); !reflect.DeepEqual(got, journalBefore) {
		t.Errorf("retired requests changed the file journal: before=%v after=%v", journalBefore, got)
	}
	if n := h.calls(); n != 0 {
		t.Errorf("retired requests called the model %d times", n)
	}
}

// This repository exists only in the test's temporary workspace. Turning off
// ambient configuration keeps local hooks and identities out of the fixture.
func makeRetiredGitFixture(t *testing.T, root string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture git %v: %v: %s", args, err, out)
		}
	}
	writeFixture(t, root, "tracked.txt", "baseline\n")
	writeFixture(t, root, ".pogo/commit-rules.md", "existing rules\n")
	run("init", "-q", "-b", "main")
	run("config", "user.name", "test")
	run("config", "user.email", "test@example.invalid")
	run("add", "tracked.txt", ".pogo/commit-rules.md")
	run("commit", "-q", "-m", "baseline")
	writeFixture(t, root, "tracked.txt", "modified\n")
	writeFixture(t, root, "fresh.txt", "new file\n")
}

func snapshotRetiredGitFixture(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[rel] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func assertRetiredGitFixtureUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	if after := snapshotRetiredGitFixture(t, root); !reflect.DeepEqual(after, before) {
		t.Error("workspace or Git metadata changed after a read or retired API request")
	}
}
