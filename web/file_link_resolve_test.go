package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/tools"
)

func fileLinkTestSession(t *testing.T, h *harness, workspace string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(h.mgr.Cwd(), workspace), 0700); err != nil {
		t.Fatal(err)
	}
	response := h.do(http.MethodPost, "/api/sessions", `{"workspace":`+studioBody(workspace)+`}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("create file-link session: %d %s", response.StatusCode, body)
	}
	var value struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil || value.SessionID == "" {
		t.Fatalf("session response: %+v %v", value, err)
	}
	return value.SessionID
}

func fileLinkGet(t *testing.T, h *harness, sid, name string, byName bool) fileLinkResolution {
	t.Helper()
	query := url.Values{"path": {name}}
	if byName {
		query.Set("basename", "1")
	}
	response := h.do(http.MethodGet, "/api/sessions/"+sid+"/files/resolve?"+query.Encode(), "")
	defer response.Body.Close()
	var result fileLinkResolution
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("resolve %q: HTTP%d result=%+v err=%v", name, response.StatusCode, result, err)
	}
	return result
}

func TestSessionFileLinkResolveCorrectsNestedBasenamesForTextAndOffice(t *testing.T) {
	h := filesHarness(t)
	workspace := "agents/example"
	sid := fileLinkTestSession(t, h, workspace)
	for _, name := range []string{"sample-a.txt", "sample-b.txt", "test-data.xlsx", "中文 报告.docx"} {
		rel := "testdir/" + name
		writeFixture(t, h.mgr.Cwd(), workspace+"/"+rel, "fixture data")
		result := fileLinkGet(t, h, sid, name, true)
		want := fileLinkResolution{Status: "unique", Complete: true, Candidates: []fileLinkCandidate{{Path: workspace + "/" + rel, RelativePath: rel, Name: name}}}
		if !reflect.DeepEqual(result, want) {
			t.Fatalf("nested %s: %+v, want %+v", name, result, want)
		}
		// Feed the chosen location into the existing preview API, the same
		// contract used by text previews and the shared Office file opener.
		preview := h.do(http.MethodGet, "/api/files/content?path="+url.QueryEscape(result.Candidates[0].Path), "")
		body, readErr := io.ReadAll(preview.Body)
		preview.Body.Close()
		if readErr != nil || preview.StatusCode != http.StatusOK || !strings.Contains(string(body), "fixture data") {
			t.Fatalf("resolved %s did not open through the existing file API: HTTP%d %v", name, preview.StatusCode, readErr)
		}
		exact := fileLinkGet(t, h, sid, rel, false)
		if exact.Status != "exact" || !exact.Complete || !reflect.DeepEqual(exact.Candidates, want.Candidates) {
			t.Fatalf("explicit %s: %+v", rel, exact)
		}
	}
	if h.calls() != 0 {
		t.Fatal("answer file navigation called the model")
	}
}

func TestSessionFileLinkResolvePreservesExactFilesAndDoesNotGuessExplicitPaths(t *testing.T) {
	h := filesHarness(t)
	sid := fileLinkTestSession(t, h, "agents/one")
	writeFixture(t, h.mgr.Cwd(), "agents/one/testdir/report.xlsx", "nested")
	if got := fileLinkGet(t, h, sid, "report.xlsx", false); got.Status != "not_found" || len(got.Candidates) != 0 || !got.Complete {
		t.Fatalf("explicit missing basename unexpectedly searched: %+v", got)
	}
	if got := fileLinkGet(t, h, sid, "wrong/report.xlsx", false); got.Status != "not_found" || len(got.Candidates) != 0 {
		t.Fatalf("explicit missing directory unexpectedly changed: %+v", got)
	}
	writeFixture(t, h.mgr.Cwd(), "agents/one/report.xlsx", "exact")
	if got := fileLinkGet(t, h, sid, "report.xlsx", true); got.Status != "exact" || len(got.Candidates) != 1 || got.Candidates[0].RelativePath != "report.xlsx" {
		t.Fatalf("exact file did not win over nested match: %+v", got)
	}
	if data, err := os.ReadFile(filepath.Join(h.mgr.Cwd(), "agents/one/report.xlsx")); err != nil || string(data) != "exact" {
		t.Fatal("resolving navigation changed file content", err)
	}
}

func TestSessionFileLinkResolveRequiresAChoiceForDuplicateNamesAndStaysInSession(t *testing.T) {
	h := filesHarness(t)
	sid := fileLinkTestSession(t, h, "agents/one")
	for _, relative := range []string{"agents/one/a/report.txt", "agents/one/b/report.txt", "agents/two/other/report.txt", "outside/report.txt"} {
		writeFixture(t, h.mgr.Cwd(), relative, "content")
	}
	result := fileLinkGet(t, h, sid, "report.txt", true)
	if result.Status != "ambiguous" || !result.Complete || len(result.Candidates) != 2 {
		t.Fatalf("duplicate lookup: %+v", result)
	}
	if result.Candidates[0].RelativePath != "a/report.txt" || result.Candidates[1].RelativePath != "b/report.txt" {
		t.Fatalf("sibling Agent or outer directory leaked: %+v", result.Candidates)
	}
	writeFixture(t, h.mgr.Cwd(), "agents/two/other/only-sibling.txt", "other")
	if got := fileLinkGet(t, h, sid, "only-sibling.txt", true); got.Status != "not_found" || len(got.Candidates) != 0 {
		t.Fatalf("sibling Agent was searched: %+v", got)
	}
}

func TestSessionFileLinkResolveRejectsEscapesAndMalformedQueries(t *testing.T) {
	h := filesHarness(t)
	sid := fileLinkTestSession(t, h, "agents/one")
	for _, value := range []string{"", "/etc/passwd", "../other/file.txt", "folder/../file.txt", "./file.txt", "a//file.txt", "file.txt/", "a\\file.txt", "file.txt\x00", "a\nfile.txt", "C:/file.txt", "a\u202efile.txt"} {
		response := h.do(http.MethodGet, "/api/sessions/"+sid+"/files/resolve?path="+url.QueryEscape(value), "")
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid path %q: HTTP%d", value, response.StatusCode)
		}
	}
	for _, query := range []string{"path=a.txt&path=b.txt", "path=a.txt&basename=1&basename=1", "path=a.txt&basename=true", "path=folder/a.txt&basename=1"} {
		response := h.do(http.MethodGet, "/api/sessions/"+sid+"/files/resolve?"+query, "")
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid query %q: HTTP%d", query, response.StatusCode)
		}
	}
}

func TestSessionFileLinkResolveDoesNotFollowOutsideFileOrDirectorySymlinks(t *testing.T) {
	h := filesHarness(t)
	sid := fileLinkTestSession(t, h, "agents/one")
	cwd := filepath.Join(h.mgr.Cwd(), "agents/one")
	outside := t.TempDir()
	writeFixture(t, outside, "outside.txt", "private")
	if err := os.Symlink(outside, filepath.Join(cwd, "foreign-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cwd, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "outside.txt"), filepath.Join(cwd, "nested/outside.txt")); err != nil {
		t.Fatal(err)
	}
	got := fileLinkGet(t, h, sid, "outside.txt", true)
	if got.Status != "not_found" || len(got.Candidates) != 0 {
		t.Fatalf("outside symlink became a file choice: %+v", got)
	}
	response := h.do(http.MethodGet, "/api/sessions/"+sid+"/files/resolve?path=nested%2Foutside.txt", "")
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("explicit outside symlink: HTTP%d", response.StatusCode)
	}
}

func TestSessionFileLinkResolveKeepsLiteralNamesAndCase(t *testing.T) {
	h := filesHarness(t)
	sid := fileLinkTestSession(t, h, "agents/one")
	for _, name := range []string{"report%20name.txt", "report#L12.txt", "Case.txt", "case.txt"} {
		writeFixture(t, h.mgr.Cwd(), "agents/one/nested/"+name, name)
	}
	for _, name := range []string{"report%20name.txt", "report#L12.txt", "Case.txt"} {
		got := fileLinkGet(t, h, sid, name, true)
		if got.Status != "unique" || len(got.Candidates) != 1 || got.Candidates[0].RelativePath != "nested/"+name {
			t.Fatalf("literal name changed: %s %+v", name, got)
		}
	}
}

func TestSessionFileLinkResolveRejectsAStoredWorkspaceOutsideCallerRoot(t *testing.T) {
	h := filesHarness(t)
	sid := fileLinkTestSession(t, h, "agents/one")
	outside := t.TempDir()
	writeFixture(t, outside, "private.txt", "outside")
	sess := h.session(sid)
	sess.mu.Lock()
	sess.cwd = outside
	sess.mu.Unlock()
	response := h.do(http.MethodGet, "/api/sessions/"+sid+"/files/resolve?path=private.txt&basename=1", "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("stored workspace outside caller root: HTTP%d", response.StatusCode)
	}
}

func TestSessionFileLinkResolveUnreadableTraversalCannotProveUnique(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode-000 directories")
	}
	root, err := tools.Resolve(t.TempDir(), ".")
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "visible/sample.txt", "visible")
	writeFixture(t, root, "hidden/sample.txt", "hidden")
	hidden := filepath.Join(root, "hidden")
	if err := os.Chmod(hidden, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(hidden, 0700) })
	got, err := resolveSessionFileLink(context.Background(), root, root, "sample.txt", true, defaultFileLinkSearchLimits)
	if err != nil || got.Status != "incomplete" || got.Complete || len(got.Candidates) != 1 {
		t.Fatalf("unreadable directory was silently excluded: %+v %v", got, err)
	}
}

func TestSessionFileLinkResolveIncompleteSearchNeverClaimsUniqueOrMissing(t *testing.T) {
	root, err := tools.Resolve(t.TempDir(), ".")
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "a/one.txt", "one")
	writeFixture(t, root, "b/one.txt", "two")
	for _, tc := range []struct {
		name   string
		limits fileLinkSearchLimits
	}{
		{"entry cap", fileLinkSearchLimits{entries: 1, candidates: 50}},
		{"candidate cap", fileLinkSearchLimits{entries: 100, candidates: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveSessionFileLink(context.Background(), root, root, "one.txt", true, tc.limits)
			if err != nil || got.Status != "incomplete" || got.Complete {
				t.Fatalf("partial traversal claimed certainty: %+v %v", got, err)
			}
			if len(got.Candidates) > tc.limits.candidates {
				t.Fatal("candidate limit exceeded")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := resolveSessionFileLink(ctx, root, root, "missing.txt", true, defaultFileLinkSearchLimits)
	if err != nil || got.Status != "incomplete" || got.Complete {
		t.Fatalf("cancelled lookup claimed no match: %+v %v", got, err)
	}
}

func TestSessionFileLinkResolveUsesSessionOwnershipBeforePathInformation(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	alice, bob := h.alice(t), h.bob(t)
	sid := h.createAs(alice)
	sess := h.session(sid)
	sess.mu.Lock()
	cwd := sess.cwd
	sess.mu.Unlock()
	writeFixture(t, cwd, "nested/own.txt", "alice")
	for _, id := range []string{sid, "missing-session"} {
		response := h.doAs(bob, http.MethodGet, "/api/sessions/"+id+"/files/resolve?path=own.txt&basename=1", "")
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound || strings.Contains(string(body), "own.txt") || strings.Contains(string(body), "alice") {
			t.Fatalf("foreign session path leak: %d %s", response.StatusCode, body)
		}
	}
	response := h.doAs(alice, http.MethodGet, "/api/sessions/"+sid+"/files/resolve?path=own.txt&basename=1", "")
	defer response.Body.Close()
	var got fileLinkResolution
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil || response.StatusCode != http.StatusOK || got.Status != "unique" || got.Candidates[0].Path != "nested/own.txt" {
		t.Fatalf("owned file resolution: HTTP%d %+v %v", response.StatusCode, got, err)
	}
}
