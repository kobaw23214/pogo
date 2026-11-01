package web

import (
	"io"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/yosukeno/pogo/skills"
)

func TestManageSkillFilesLazyTreeAndAdminBoundary(t *testing.T) {
	sk := writeTestSkill(t, t.TempDir(), "archive-tree", "archive tree", "# Main")
	if err := os.MkdirAll(filepath.Join(sk.Dir, "references/sql"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sk.Dir, "references/sql/flow.md"), []byte("# Flow"), 0644); err != nil {
		t.Fatal(err)
	}
	embedded := writeTestSkill(t, t.TempDir(), "builtin-tree", "built in", "# Builtin")
	embedded.Source = "embedded"
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")), func(c *Config) { c.Skills = []skills.Skill{sk, embedded} })
	endpoint := "/api/manage/skills/" + skillIDOf(sk) + "/files"
	var root skills.FileList
	runtimeSkillResponse(t, h.doAs(h.token, "GET", endpoint, ""), 200, &root)
	if root.RelativePath != "" || len(root.Entries) != 2 {
		t.Fatalf("unexpected root: %+v", root)
	}
	resp := h.doAs(h.token, "GET", endpoint+"?path=references/sql", "")
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("file tree may be cached")
	}
	var nested skills.FileList
	runtimeSkillResponse(t, resp, 200, &nested)
	if nested.RelativePath != "references/sql" || len(nested.Entries) != 1 || nested.Entries[0].Path != "references/sql/flow.md" || !nested.Entries[0].Previewable {
		t.Fatalf("unexpected nested: %+v", nested)
	}
	for _, check := range []struct {
		token, path string
		status      int
	}{
		{aliceToken, endpoint, 403}, {"", endpoint, 401},
		{h.token, "/api/manage/skills/not-installed/files", 404},
		{h.token, "/api/manage/skills/" + skillIDOf(embedded) + "/files", 404},
		{h.token, endpoint + "?path=" + url.QueryEscape("../outside"), 400},
		{h.token, endpoint + "?path=" + url.QueryEscape("%2e%2e/outside"), 400},
		{h.token, endpoint + "?path=SKILL.md", 404},
	} {
		r := h.doAs(check.token, "GET", check.path, "")
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != check.status {
			t.Fatalf("%s: %d, want %d (%s)", check.path, r.StatusCode, check.status, body)
		}
	}
}
