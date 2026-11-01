package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/tools"
)

func spillBrowseRequest(t *testing.T, server *httptest.Server, endpoint string, output any) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, server.URL+endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	if output != nil {
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
	}
	return response
}

func TestSpillBrowseDirectoryPagingAndReadOnly(t *testing.T) {
	dir := sweepEnv(t)
	for i := 0; i < 205; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("item-%03d.log", i)), []byte("body"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, ".metadata"), 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	server := prefsTestServer(t)
	seen := map[string]bool{}
	offset := 0
	for pages := 0; ; pages++ {
		if pages > 3 {
			t.Fatal("directory pagination did not terminate")
		}
		var page spillBrowseEntries
		response := spillBrowseRequest(t, server, "/api/prefs/spill/entries?limit=100&offset="+strconv.Itoa(offset), &page)
		if response.StatusCode != http.StatusOK || page.Root != dir || page.Path != "" || !page.Exists || !page.ReadOnly || len(page.Entries) > 100 {
			t.Fatalf("invalid directory page: status %d, %+v", response.StatusCode, page)
		}
		if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("spill inspection was cacheable or sniffable")
		}
		for _, entry := range page.Entries {
			if seen[entry.Name] || entry.Name != entry.Path || entry.ModifiedAt == "" {
				t.Fatalf("duplicate/invalid entry: %+v", entry)
			}
			seen[entry.Name] = true
		}
		if page.Complete {
			if page.NextOffset != nil || page.Truncated {
				t.Fatalf("completed listing advertised continuation: %+v", page)
			}
			break
		}
		if !page.Truncated || page.NextOffset == nil || *page.NextOffset <= offset {
			t.Fatalf("incomplete listing lost its continuation: %+v", page)
		}
		offset = *page.NextOffset
	}
	if len(seen) != 206 || !seen[".metadata"] {
		t.Fatalf("listing omitted ordinary files/metadata: %d entries", len(seen))
	}
	after, err := os.Stat(dir)
	if err != nil || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("browsing mutated the spill directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 206 {
		t.Fatal("browsing created/deleted artifacts or lease sidecars")
	}
}

func TestSpillBrowseFileUTF8ContinuationAndUnsafeContent(t *testing.T) {
	dir := sweepEnv(t)
	body := strings.Repeat("中文🙂文本\n", 30000)
	file := filepath.Join(dir, "unicode.log")
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	age := time.Now().Add(-20 * 24 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(file, age, age); err != nil {
		t.Fatal(err)
	}
	server := prefsTestServer(t)
	var combined strings.Builder
	var offset int64
	for pageNumber := 0; ; pageNumber++ {
		if pageNumber > 20 {
			t.Fatal("UTF-8 file pagination did not terminate")
		}
		var view spillBrowseFile
		response := spillBrowseRequest(t, server, "/api/prefs/spill/file?path=unicode.log&limit=65536&offset="+strconv.FormatInt(offset, 10), &view)
		if response.StatusCode != http.StatusOK || view.Binary || !view.ReadOnly || view.Offset != offset || view.Bytes != len(view.Content) || view.Bytes > spillBrowseFileMax {
			t.Fatalf("invalid UTF-8 chunk: status %d, %+v", response.StatusCode, view)
		}
		combined.WriteString(view.Content)
		if view.Complete {
			if view.NextOffset != nil || view.Truncated {
				t.Fatal("completed file still has a continuation")
			}
			break
		}
		if view.NextOffset == nil || *view.NextOffset != offset+int64(len(view.Content)) || *view.NextOffset <= offset {
			t.Fatal("text continuation is not the delivered UTF-8 byte boundary")
		}
		offset = *view.NextOffset
	}
	if combined.String() != body {
		t.Fatal("continued text was duplicated, omitted or corrupted")
	}
	info, err := os.Stat(file)
	if err != nil || !info.ModTime().Equal(age) {
		t.Fatal("preview changed the artifact mtime/retention")
	}
	for _, tc := range []struct {
		name    string
		content []byte
		binary  bool
	}{
		{"empty.log", nil, false},
		{"binary.log", []byte{0, 1, 255, 7}, true},
		{"attack.html", []byte(`<script>globalThis.spillExecuted = true</script><img src=x onerror=alert(1)>`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, tc.name), tc.content, 0o600); err != nil {
				t.Fatal(err)
			}
			var view spillBrowseFile
			response := spillBrowseRequest(t, server, "/api/prefs/spill/file?path="+url.QueryEscape(tc.name), &view)
			if response.StatusCode != http.StatusOK || !view.Complete || view.Binary != tc.binary || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
				t.Fatalf("unexpected preview: status %d, %+v", response.StatusCode, view)
			}
			if tc.binary && view.Content != "" || !tc.binary && view.Content != string(tc.content) {
				t.Fatal("binary text leaked or textual content was transformed")
			}
		})
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	response := spillBrowseRequest(t, server, "/api/prefs/spill/file?path=unicode.log", nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted file: got %d, want 404", response.StatusCode)
	}
}

func TestSpillBrowseRejectsUnsafePathsAndSymlinks(t *testing.T) {
	dir := sweepEnv(t)
	server := prefsTestServer(t)
	for _, suffix := range []string{"/etc/passwd", "../outside", "a/../b", "a//b", "a/./b", "C:\\Windows\\win.ini", "a\x00b"} {
		for _, endpoint := range []string{"entries", "file"} {
			response := spillBrowseRequest(t, server, "/api/prefs/spill/"+endpoint+"?path="+url.QueryEscape(suffix), nil)
			if response.StatusCode != http.StatusBadRequest {
				t.Errorf("%s accepted path %q: %d", endpoint, suffix, response.StatusCode)
			}
		}
	}
	for _, query := range []string{"root=/tmp", "path=a&path=b", "offset=-1", "offset=100001", "limit=201", "limit=0", "path=" + strings.Repeat("x", 4097)} {
		response := spillBrowseRequest(t, server, "/api/prefs/spill/entries?"+query, nil)
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("invalid directory query %q: %d", query, response.StatusCode)
		}
	}
	for _, query := range []string{"path=a&offset=-1", "path=a&limit=65537", "path=a&limit=1", "path=a&offset=xyz"} {
		response := spillBrowseRequest(t, server, "/api/prefs/spill/file?"+query, nil)
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("invalid file query %q: %d", query, response.StatusCode)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.log"), []byte("OUTSIDE_SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "linked")); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.log"), filepath.Join(dir, "linked-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "normal.log"), []byte("NORMAL"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("normal.log", filepath.Join(dir, "local-link")); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"entries?path=linked", "file?path=linked/secret.log", "file?path=linked-file", "file?path=local-link", "file?path=.", "file?path="} {
		response := spillBrowseRequest(t, server, "/api/prefs/spill/"+endpoint, nil)
		content, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != http.StatusForbidden && response.StatusCode != http.StatusBadRequest || bytes.Contains(content, []byte("OUTSIDE_SECRET")) {
			t.Errorf("unsafe endpoint %q: %d %s", endpoint, response.StatusCode, content)
		}
	}
	var page spillBrowseEntries
	spillBrowseRequest(t, server, "/api/prefs/spill/entries", &page)
	if len(page.Entries) != 1 || page.Entries[0].Name != "normal.log" || page.Skipped != 3 {
		t.Fatalf("symbolic links were not skipped: %+v", page)
	}
}

func TestSpillBrowseMissingRootDoesNotCreateIt(t *testing.T) {
	sweepEnv(t)
	dir := filepath.Join(t.TempDir(), "not-created")
	t.Setenv(tools.SpillDirEnv, dir)
	server := prefsTestServer(t)
	var page spillBrowseEntries
	response := spillBrowseRequest(t, server, "/api/prefs/spill/entries", &page)
	if response.StatusCode != http.StatusOK || page.Exists || !page.Complete || len(page.Entries) != 0 || page.Root != dir {
		t.Fatalf("missing spill root was not an empty read-only view: %+v", page)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatal("browsing created a missing spill directory")
	}
}

func TestSpillBrowseAuthenticationAndAdminBoundary(t *testing.T) {
	sweepEnv(t)
	h := newMultiuserHarness(t, scriptedTurns(textTurn("done")))
	h.armUsers(map[string]userRecord{
		"alice":    {Username: "alice", Token: aliceToken},
		"operator": {Username: "operator", Token: bobToken, Admin: true},
	})
	for _, endpoint := range []string{"entries", "file?path=missing.log"} {
		for _, tc := range []struct {
			token  string
			status int
		}{{"", 401}, {aliceToken, 403}, {bobToken, 200}} {
			response := h.doAs(tc.token, http.MethodGet, "/api/prefs/spill/"+endpoint, "")
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			want := tc.status
			if tc.token == bobToken && strings.HasPrefix(endpoint, "file") {
				want = 404
			}
			if err != nil || response.StatusCode != want {
				t.Fatalf("%q token %q: got %d, want %d: %s", endpoint, tc.token, response.StatusCode, want, body)
			}
		}
	}
}
