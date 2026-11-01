package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/internal/testfs"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skills"
)

func TestRuntimeSkillDocumentEndpointKeepsPrivatePayloadsPrivate(t *testing.T) {
	archive := selectionFixtureZIP(t)
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, file := range zr.File {
		if err := zw.Copy(file); err != nil {
			t.Fatal(err)
		}
	}
	w, err := zw.Create("references/workflow.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "# Bundled workflow\n包内资料"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	service, err := skillruntime.NewManagementService(testfs.SealedTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := service.Import(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	h, _ := newStudioHarness(t, []llm.Response{textTurn("unused")}, func(c *Config) { c.SkillRuntime = service })
	endpoint := "/api/manage/skills/" + skillruntime.PackageSkillID(installed.Ref) + "/content"
	var document map[string]any
	runtimeSkillResponse(t, h.do("GET", endpoint+"?path=references/workflow.md", ""), 200, &document)
	if !strings.Contains(document["content"].(string), "Bundled workflow") || document["path"] != "" {
		t.Fatalf("runtime document or opaque location: %+v", document)
	}
	// Administrators may inspect source metadata from the original ZIP, while
	// binary payloads and paths outside that original package remain unavailable.
	for relative, want := range map[string]int{"payload/rt.zip": 415, "contracts/scan.json": 200, "pogo-skill.json": 200, "../install.json": 400, "references/../contracts/scan.json": 400} {
		if got := studioStatus(t, h, "GET", endpoint+"?path="+url.QueryEscape(relative), ""); got != want {
			t.Errorf("package source %s returned %d, want %d", relative, got, want)
		}
	}
	var rootFiles skills.FileList
	runtimeSkillResponse(t, h.do("GET", strings.TrimSuffix(endpoint, "/content")+"/files", ""), 200, &rootFiles)
	for _, entry := range rootFiles.Entries {
		if strings.Contains(entry.Path, service.Root) || entry.Name == "install.json" || entry.Name == "runtime" || entry.Name == "data" {
			t.Fatalf("private installed state leaked: %+v", entry)
		}
	}
	var payloadFiles skills.FileList
	runtimeSkillResponse(t, h.do("GET", strings.TrimSuffix(endpoint, "/content")+"/files?path=payload", ""), 200, &payloadFiles)
	if len(payloadFiles.Entries) != 1 || payloadFiles.Entries[0].Path != "payload/rt.zip" || payloadFiles.Entries[0].Previewable || payloadFiles.Entries[0].SizeBytes == 0 || payloadFiles.Entries[0].Reason == "" {
		t.Fatalf("binary payload metadata: %+v", payloadFiles)
	}
	var contractFiles skills.FileList
	runtimeSkillResponse(t, h.do("GET", strings.TrimSuffix(endpoint, "/content")+"/files?path=contracts", ""), 200, &contractFiles)
	if len(contractFiles.Entries) != 1 || !contractFiles.Entries[0].Previewable {
		t.Fatalf("original contract text not inspectable: %+v", contractFiles)
	}
	if _, _, err := service.SkillDocument(skillruntime.PackageSkillID(installed.Ref), "contracts/scan.json"); err == nil {
		t.Fatal("administrator source preview widened Agent metadata access")
	}
}

func TestSkillDocumentEndpointsAndSelection(t *testing.T) {
	dir := t.TempDir()
	a := writeTestSkill(t, dir, "alpha", "alpha docs", "# Alpha")
	b := writeTestSkill(t, dir, "beta", "beta docs", "# Beta")
	for _, sk := range []skills.Skill{a, b} {
		if err := os.MkdirAll(filepath.Join(sk.Dir, "references/sql-reuse"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sk.Dir, "references/sql-reuse/workflow.md"), []byte("# "+sk.Name+" workflow\n完整说明"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sk.Dir, "references/file.pdf"), []byte("%PDF-1.7\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	h, _ := newStudioHarness(t, []llm.Response{textTurn("unused")}, func(c *Config) { c.Skills = []skills.Skill{a, b} })
	agent := postStudioAgent(t, h, studioAgentCapsBody("Docs", "ready", map[string]any{"skills": []string{skillIDOf(a)}}), http.StatusCreated)
	sid := startStudioConversation(t, h, agent.ID)
	paths := []string{"/api/manage/skills/" + skillIDOf(a) + "/content", "/api/skills/content?name=alpha&session_id=" + sid}
	for _, endpoint := range paths {
		separator := "?"
		if strings.Contains(endpoint, "?") {
			separator = "&"
		}
		resp := h.do(http.MethodGet, endpoint+separator+"path="+url.QueryEscape("references/sql-reuse/workflow.md"), "")
		var document struct {
			Content, RelativePath, Language, MediaType string
			Truncated                                  bool
		}
		var data map[string]json.RawMessage
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s: %d %s", endpoint, resp.StatusCode, body)
		}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		json.Unmarshal(data["content"], &document.Content)
		json.Unmarshal(data["relative_path"], &document.RelativePath)
		json.Unmarshal(data["language"], &document.Language)
		json.Unmarshal(data["media_type"], &document.MediaType)
		if !strings.Contains(document.Content, "alpha workflow") || document.RelativePath != "references/sql-reuse/workflow.md" || document.Language != "markdown" || document.MediaType != "text/markdown" {
			t.Fatalf("unexpected document: %+v", document)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("document response may be cached across selections")
		}
		for relative, status := range map[string]int{"../beta/SKILL.md": 400, "%2e%2e/beta/SKILL.md": 400, "/etc/passwd": 400, "references/missing.md": 404, "references/file.pdf": 415} {
			if got := studioStatus(t, h, http.MethodGet, endpoint+separator+"path="+url.QueryEscape(relative), ""); got != status {
				t.Errorf("%s %s: %d, want %d", endpoint, relative, got, status)
			}
		}
	}
	if got := studioStatus(t, h, http.MethodGet, "/api/skills/content?name=beta&session_id="+sid+"&path=references/sql-reuse/workflow.md", ""); got != 404 {
		t.Fatalf("outside-selection document returned %d", got)
	}
	if got := studioStatus(t, h, http.MethodGet, "/api/skills/content?name=alpha&session_id="+sid+"ff&path=references/sql-reuse/workflow.md", ""); got != 404 {
		t.Fatalf("unknown session document returned %d", got)
	}
}

func TestSkillDocumentPreservesSessionOwnerAndAdminBoundaries(t *testing.T) {
	sk := writeTestSkill(t, t.TempDir(), "owner-docs", "document ownership", "# Main")
	if err := os.MkdirAll(filepath.Join(sk.Dir, "references"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sk.Dir, "references/flow.md"), []byte("# Owner reference"), 0644); err != nil {
		t.Fatal(err)
	}
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")), func(c *Config) { c.Skills = []skills.Skill{sk} })
	sid := h.createAs(aliceToken)
	for _, check := range []struct {
		token, endpoint string
		status          int
	}{
		{aliceToken, "/api/skills/content?name=owner-docs&session_id=" + sid + "&path=references/flow.md", 200},
		{bobToken, "/api/skills/content?name=owner-docs&session_id=" + sid + "&path=references/flow.md", 404},
		{aliceToken, "/api/manage/skills/" + skillIDOf(sk) + "/content?path=references/flow.md", 403},
		{"", "/api/manage/skills/" + skillIDOf(sk) + "/content?path=references/flow.md", 401},
	} {
		resp := h.doAs(check.token, "GET", check.endpoint, "")
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != check.status {
			t.Fatalf("%s: %d, want %d (%s)", check.endpoint, resp.StatusCode, check.status, body)
		}
	}
}
