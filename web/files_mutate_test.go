package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/identity"
)

func fileMutationJSON(t *testing.T, root, path, name string, recursive bool) string {
	t.Helper()
	info, err := os.Lstat(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(fileMutationRequest{Path: path, Name: name, Recursive: recursive, MtimeMS: info.ModTime().UnixMilli(), Size: info.Size()})
	return string(data)
}

func TestFileMutationRenameAndRecursiveDelete(t *testing.T) {
	h := filesHarness(t)
	root := h.mgr.Cwd()
	writeFixture(t, root, "reports/nested/中文.txt", "content")
	resp := h.do(http.MethodPost, "/api/files/rename", fileMutationJSON(t, root, "reports", "评审", false))
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("rename directory: %d %s", resp.StatusCode, data)
	}
	if data, err := os.ReadFile(filepath.Join(root, "评审/nested/中文.txt")); err != nil || string(data) != "content" {
		t.Fatalf("moved file %q %v", data, err)
	}
	resp = h.do(http.MethodDelete, "/api/files", fileMutationJSON(t, root, "评审", "", false))
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("missing recursive consent = %d", resp.StatusCode)
	}
	resp = h.do(http.MethodDelete, "/api/files", fileMutationJSON(t, root, "评审", "", true))
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("delete directory = %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, "评审")); !os.IsNotExist(err) {
		t.Fatalf("directory survived: %v", err)
	}
}

func TestFileMutationRejectsRootEscapesNamesLinksAndStaleSelection(t *testing.T) {
	h := filesHarness(t)
	root := h.mgr.Cwd()
	outside := t.TempDir()
	writeFixture(t, root, "note.txt", "mine")
	writeFixture(t, root, "other.txt", "other")
	writeFixture(t, root, "actual/child.txt", "actual")
	writeFixture(t, outside, "secret.txt", "outside")
	if err := os.Symlink(filepath.Join(root, "actual"), filepath.Join(root, "link-in")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link-out")); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path, name string
		want       int
	}{
		{".", "root", 400}, {"../outside", "x", 400}, {outside, "x", 400}, {"note.txt", "../bad", 400}, {"note.txt", "a/b", 400}, {"note.txt", "a\\b", 400}, {"note.txt", "..", 400},
		{"link-in", "x", 400}, {"link-in/child.txt", "x", 400}, {"link-out", "x", 400}, {"link-out/secret.txt", "x", 400}, {"note.txt", "other.txt", 409},
	} {
		data, _ := json.Marshal(fileMutationRequest{Path: test.path, Name: test.name, MtimeMS: 1, Size: 4})
		resp := h.do(http.MethodPost, "/api/files/rename", string(data))
		resp.Body.Close()
		if resp.StatusCode != test.want {
			t.Errorf("%s -> %s = %d, want %d", test.path, test.name, resp.StatusCode, test.want)
		}
	}
	// A correct current selection still cannot replace an existing file.
	resp := h.do(http.MethodPost, "/api/files/rename", fileMutationJSON(t, root, "note.txt", "other.txt", false))
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("collision=%d", resp.StatusCode)
	}
	for path, want := range map[string]string{"note.txt": "mine", "other.txt": "other", "actual/child.txt": "actual"} {
		got, _ := os.ReadFile(filepath.Join(root, path))
		if string(got) != want {
			t.Errorf("%s changed", path)
		}
	}
	stale := fileMutationJSON(t, root, "note.txt", "renamed.txt", false)
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("new contents"), 0600); err != nil {
		t.Fatal(err)
	}
	resp = h.do(http.MethodPost, "/api/files/rename", stale)
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("stale selection=%d", resp.StatusCode)
	}
	resp = h.do(http.MethodDelete, "/api/files", fileMutationJSON(t, root, "link-in", "", true))
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("delete symlink=%d", resp.StatusCode)
	}
	secret, _ := os.ReadFile(filepath.Join(outside, "secret.txt"))
	if string(secret) != "outside" {
		t.Fatal("outside target changed")
	}
}

func TestFileMutationRecursiveDeleteDoesNotFollowChildSymlink(t *testing.T) {
	h := filesHarness(t)
	root := h.mgr.Cwd()
	outside := t.TempDir()
	writeFixture(t, root, "folder/note.txt", "mine")
	writeFixture(t, outside, "secret.txt", "safe")
	if err := os.Symlink(outside, filepath.Join(root, "folder", "link")); err != nil {
		t.Fatal(err)
	}
	resp := h.do(http.MethodDelete, "/api/files", fileMutationJSON(t, root, "folder", "", true))
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("delete=%d", resp.StatusCode)
	}
	if data, err := os.ReadFile(filepath.Join(outside, "secret.txt")); err != nil || string(data) != "safe" {
		t.Fatalf("outside file changed: %q %v", data, err)
	}
}

func TestFileMutationBlocksOfficeOccupancyIncludingDescendants(t *testing.T) {
	h := filesHarness(t)
	root := h.mgr.Cwd()
	writeFixture(t, root, "reports/review.xlsx", "preserved")
	server, err := NewServer(h.mgr, ServerOptions{Token: h.token})
	if err != nil {
		t.Fatal(err)
	}
	docPath := filepath.Join(root, "reports/review.xlsx")
	d := &officeDocumentSession{id: "orphan", absolute: docPath, dirtyEpoch: 9, persistedEpoch: 3, restarted: true}
	server.office = &officeService{sessions: map[string]*officeDocumentSession{"orphan": d}, active: map[string]string{docPath: "orphan"}}
	srv := httptest.NewServer(server.Handler())
	defer srv.Close()
	do := func(method, path, body string) int {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.token)
		req.Header.Set("Content-Type", "application/json")
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, test := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/files/rename", fileMutationJSON(t, root, "reports/review.xlsx", "new.xlsx", false)},
		{http.MethodPost, "/api/files/rename", fileMutationJSON(t, root, "reports", "new-reports", false)},
		{http.MethodDelete, "/api/files", fileMutationJSON(t, root, "reports", "", true)},
	} {
		if status := do(test.method, test.path, test.body); status != 409 {
			t.Fatalf("occupied operation=%d", status)
		}
	}
	if d.dirtyEpoch != 9 || d.persistedEpoch != 3 || d.closed {
		t.Fatal("mutation cleared unknown Office state")
	}
	if err := os.Link(docPath, filepath.Join(root, "linked-review.xlsx")); err != nil {
		t.Fatal(err)
	}
	if status := do(http.MethodDelete, "/api/files", fileMutationJSON(t, root, "linked-review.xlsx", "", false)); status != 409 {
		t.Fatalf("occupied inode alias=%d", status)
	}
	if info, err := os.Stat(filepath.Join(root, "REPORTS")); err == nil && info.IsDir() {
		if status := do(http.MethodDelete, "/api/files", fileMutationJSON(t, root, "REPORTS", "", true)); status != 409 {
			t.Fatalf("case alias directory=%d", status)
		}
	}
	server.office.mu.Lock()
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
	server.office.mu.Unlock()
	if status := do(http.MethodDelete, "/api/files", fileMutationJSON(t, root, "reports", "", true)); status != 409 {
		t.Fatalf("closed but unconfirmed dirty editor=%d", status)
	}
	server.office.mu.Lock()
	delete(server.office.active, docPath)
	server.office.batchLeases = map[string]string{docPath: "batch"}
	server.office.mu.Unlock()
	if status := do(http.MethodDelete, "/api/files", fileMutationJSON(t, root, "reports", "", true)); status != 409 {
		t.Fatalf("batch operation=%d", status)
	}
	server.office.mu.Lock()
	delete(server.office.batchLeases, docPath)
	server.office.mu.Unlock()
	if status := do(http.MethodPost, "/api/files/rename", fileMutationJSON(t, root, "reports", "new-reports", false)); status != 200 {
		t.Fatalf("closed operation=%d", status)
	}
}

func TestMultiuserFileMutationCannotReachAnotherAccount(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("ok")))
	alice, bob := h.alice(t), h.bob(t)
	root := h.mgr.UserRoot(identity.LocalScope("alice"))
	writeFixture(t, root, "private.txt", "mine")
	data := fileMutationJSON(t, root, "private.txt", "renamed.txt", false)
	resp := h.doAs(bob, http.MethodPost, "/api/files/rename", data)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("other account relative path=%d", resp.StatusCode)
	}
	var body fileMutationRequest
	_ = json.Unmarshal([]byte(data), &body)
	body.Path = filepath.Join(root, "private.txt")
	encoded, _ := json.Marshal(body)
	resp = h.doAs(bob, http.MethodPost, "/api/files/rename", string(encoded))
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("other account absolute path=%d", resp.StatusCode)
	}
	resp = h.doAs(alice, http.MethodPost, "/api/files/rename", data)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("owner rename=%d", resp.StatusCode)
	}
}

func TestFileMutationDoesNotManagePrivateLibraryAssetsOrTheirAncestor(t *testing.T) {
	h := filesHarness(t, func(cfg *Config) {
		cfg.SessionDir = filepath.Join(cfg.Cwd, "internal", "sessions")
	})
	root := h.mgr.Cwd()
	writeFixture(t, root, "internal/sessions/information/assets/source.txt", "private")
	for _, path := range []string{"internal", "internal/sessions/information/assets/source.txt"} {
		resp := h.do(http.MethodDelete, "/api/files", fileMutationJSON(t, root, path, "", true))
		resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Fatalf("private target %s=%d", path, resp.StatusCode)
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "internal/sessions/information/assets/source.txt")); err != nil || string(data) != "private" {
		t.Fatalf("private asset changed %q %v", data, err)
	}
}

func TestFileMutationRejectsPrivateStorageCaseAliases(t *testing.T) {
	h := filesHarness(t, func(cfg *Config) {
		cfg.SessionDir = filepath.Join(cfg.Cwd, ".localagent", "sessions")
	})
	root := h.mgr.Cwd()
	writeFixture(t, root, ".localagent/sessions/information/assets/source.txt", "private")
	canonical, err := os.Stat(filepath.Join(root, ".localagent"))
	if err != nil {
		t.Fatal(err)
	}
	alias, err := os.Stat(filepath.Join(root, ".LOCALAGENT"))
	if err != nil || !os.SameFile(canonical, alias) {
		t.Skip("temporary volume is case-sensitive; canonical storage guards run in the platform-independent test")
	}
	for _, path := range []string{".LOCALAGENT", ".LOCALAGENT/SESSIONS", ".LOCALAGENT/SESSIONS/INFORMATION/assets/SOURCE.txt"} {
		for _, method := range []string{http.MethodDelete, http.MethodPost} {
			endpoint := "/api/files"
			if method == http.MethodPost {
				endpoint = "/api/files/rename"
			}
			resp := h.do(method, endpoint, fileMutationJSON(t, root, path, "renamed-private", true))
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 403 {
				t.Fatalf("private case alias %s %s=%d %s", method, path, resp.StatusCode, data)
			}
			if !strings.Contains(string(data), "PRIVATE_STORAGE_PROTECTED") {
				t.Fatalf("private case alias returned wrong protection: %s", data)
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, ".localagent/sessions/information/assets/source.txt")); err != nil || string(data) != "private" {
		t.Fatalf("private asset changed %q %v", data, err)
	}
	// The identity guard must not classify a distinct similarly named folder
	// as private merely because it shares a case-folded prefix.
	writeFixture(t, root, ".localagent-public/note.txt", "user")
	resp := h.do(http.MethodPost, "/api/files/rename", fileMutationJSON(t, root, ".localagent-public/note.txt", "renamed.txt", false))
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("independent public folder rename=%d", resp.StatusCode)
	}
}

func TestFileMutationPrivateStorageIdentityGuardBothDirections(t *testing.T) {
	workspace := t.TempDir()
	// This direct identity check has no HTTP or background-runtime dependency.
	// Its private-root variants must not mutate a live Manager's configuration.
	server := &Server{mgr: &Manager{cfg: Config{SessionDir: filepath.Join(workspace, "private-parent", "sessions")}}}
	writeFixture(t, workspace, "private-parent/sessions/information/asset.txt", "private")
	writeFixture(t, workspace, "public/note.txt", "public")
	root, err := os.OpenRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, test := range []struct {
		path    string
		blocked bool
	}{{"private-parent", true}, {"private-parent/sessions", true}, {"private-parent/sessions/information/asset.txt", true}, {"public/note.txt", false}} {
		info, err := root.Lstat(test.path)
		if err != nil {
			t.Fatal(err)
		}
		err = server.panelPrivateStorageIdentityAllowed(root, test.path, info)
		if test.blocked {
			var typed *officeError
			if !errors.As(err, &typed) || typed.status != 403 || typed.code != "PRIVATE_STORAGE_PROTECTED" {
				t.Fatalf("identity guard %s=%v", test.path, err)
			}
		} else if err != nil {
			t.Fatalf("public identity guard=%v", err)
		}
	}
	// A configured private descendant that has not yet been initialized still
	// protects its existing ancestor from removal through another spelling.
	server.mgr.cfg.SessionDir = filepath.Join(workspace, "private-parent", "not-created", "sessions")
	info, err := root.Lstat("private-parent")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.panelPrivateStorageIdentityAllowed(root, "private-parent", info); err == nil {
		t.Fatal("future private root lost ancestor identity protection")
	}
}
