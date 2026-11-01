package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/internal/testfs"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skills"
)

type editableSkillDocument struct {
	Content   string `json:"content"`
	Revision  string `json:"revision"`
	Editable  bool   `json:"editable"`
	Reason    string `json:"edit_disabled_reason"`
	Truncated bool   `json:"truncated"`
}

func getEditableSkillDocument(t *testing.T, h *harness, id string) editableSkillDocument {
	t.Helper()
	var out editableSkillDocument
	runtimeSkillResponse(t, h.do("GET", "/api/manage/skills/"+id+"/content", ""), 200, &out)
	return out
}
func putEditableSkillDocument(h *harness, id, content, revision string) *http.Response {
	raw, _ := json.Marshal(map[string]string{"content": content, "expected_revision": revision})
	return h.do("PUT", "/api/manage/skills/"+id+"/content", string(raw))
}
func skillEditBody(description, body string) string {
	return "---\nname: editable-check\ndescription: " + description + "\n---\n\n" + body + "\n"
}
func uploadEditableSkillFixture(t *testing.T, h *harness) skills.Skill {
	t.Helper()
	data := webZip(t, []webZipEntry{{"SKILL.md", []byte(skillEditBody("OLD_DESCRIPTION", "OLD_BODY")), 0644}, {"references/guide.md", []byte("UNCHANGED_REFERENCE"), 0644}, {"scripts/helper.py", []byte("# unchanged source; never executed\n"), 0644}})
	runtimeSkillResponse(t, postSkillZip(t, h, data), 201, nil)
	for _, sk := range h.mgr.Skills() {
		if sk.Name == "editable-check" {
			return sk
		}
	}
	t.Fatal("uploaded Skill missing")
	return skills.Skill{}
}

func TestManagedSkillDocumentEditHTTPUploadSaveIdentityAndReload(t *testing.T) {
	root := t.TempDir()
	h, capture := newStudioHarness(t, []llm.Response{textTurn("old"), textTurn("new")}, func(c *Config) { c.SkillOptions = skills.Options{UserDir: root} })
	sk := uploadEditableSkillFixture(t, h)
	id := skillIDOf(sk)
	initial := getEditableSkillDocument(t, h, id)
	if !initial.Editable || initial.Revision != skillDocumentRevision([]byte(initial.Content)) || initial.Reason != "" {
		t.Fatalf("editable metadata=%+v", initial)
	}
	refBefore, _ := os.ReadFile(filepath.Join(sk.Dir, "references/guide.md"))
	scriptBefore, _ := os.ReadFile(filepath.Join(sk.Dir, "scripts/helper.py"))
	a := postStudioAgent(t, h, studioAgentCapsBody("Editable", "ready", map[string]any{"skills": []string{id}, "mcp_servers": []string{}}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	original, _ := h.mgr.studio.binding(sid)
	hub, browser, loop := s.hub, s.browser, s.agent
	studioAsk(t, h, sid, "/skill:editable-check inspect")
	next := skillEditBody("NEW_DESCRIPTION", "NEW_BODY")
	var saved editableSkillDocument
	runtimeSkillResponse(t, putEditableSkillDocument(h, id, next, initial.Revision), 200, &saved)
	if saved.Content != next || saved.Revision != skillDocumentRevision([]byte(next)) || !saved.Editable {
		t.Fatalf("save metadata=%+v", saved)
	}
	if s.hub != hub || s.browser != browser || s.agent != loop {
		t.Fatal("save recreated stateful session")
	}
	current, _ := h.mgr.studio.binding(sid)
	if !reflect.DeepEqual(original.Agent, current.Agent) {
		t.Fatal("document save rewrote Agent definition or Skill identity")
	}
	if names := sessionSkillNamesHTTP(t, h, sid); !slices.Equal(names, []string{"editable-check"}) {
		t.Fatal("selection identity changed")
	}
	studioAsk(t, h, sid, "/skill:editable-check inspect again")
	systems, _, histories, _ := capture.snapshot()
	if !strings.Contains(systems[0], "OLD_DESCRIPTION") || !strings.Contains(systems[1], "NEW_DESCRIPTION") || strings.Contains(systems[1], "OLD_DESCRIPTION") {
		t.Fatal("idle prompt did not refresh description")
	}
	if !strings.Contains(studioMessageText(histories[0][0]), "OLD_BODY") || !strings.Contains(studioMessageText(histories[1][len(histories[1])-1]), "NEW_BODY") {
		t.Fatal("Skill invocation did not read saved body")
	}
	for p, want := range map[string][]byte{"references/guide.md": refBefore, "scripts/helper.py": scriptBefore} {
		got, _ := os.ReadFile(filepath.Join(sk.Dir, p))
		if !bytes.Equal(got, want) {
			t.Fatal("save changed another package file", p)
		}
	}
	loaded, _ := skills.Load(h.mgr.SkillOptions())
	h.mgr.Close()
	h2, _ := newStudioHarness(t, []llm.Response{textTurn("unused")}, func(c *Config) {
		c.SessionDir = h.mgr.cfg.SessionDir
		c.SkillOptions = h.mgr.SkillOptions()
		c.Skills = loaded
	})
	again := getEditableSkillDocument(t, h2, id)
	if again.Content != next || again.Revision != saved.Revision {
		t.Fatal("saved document did not survive reload")
	}
	info, _ := os.Stat(sk.Path)
	runtimeSkillResponse(t, putEditableSkillDocument(h2, id, next, saved.Revision), 200, nil)
	after, _ := os.Stat(sk.Path)
	if !info.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged save rewrote file")
	}
}

func TestManagedSkillDocumentEditHTTPConflictAndConcurrentCAS(t *testing.T) {
	root := t.TempDir()
	h := newHarness(t, idleRespond, func(c *Config) { c.SkillOptions = skills.Options{UserDir: root} })
	sk := uploadEditableSkillFixture(t, h)
	id := skillIDOf(sk)
	initial := getEditableSkillDocument(t, h, id)
	contents := []string{skillEditBody("first", "FIRST"), skillEditBody("second", "SECOND")}
	type answer struct {
		status int
		index  int
	}
	answers := make(chan answer, 2)
	var wg sync.WaitGroup
	for i, content := range contents {
		wg.Add(1)
		go func(i int, content string) {
			defer wg.Done()
			r := putEditableSkillDocument(h, id, content, initial.Revision)
			r.Body.Close()
			answers <- answer{r.StatusCode, i}
		}(i, content)
	}
	wg.Wait()
	close(answers)
	status := map[int]int{}
	winner := -1
	for a := range answers {
		status[a.status]++
		if a.status == 200 {
			winner = a.index
		}
	}
	if status[200] != 1 || status[409] != 1 {
		t.Fatalf("concurrent statuses=%v", status)
	}
	got, _ := os.ReadFile(sk.Path)
	if string(got) != contents[winner] {
		t.Fatal("winner was overwritten")
	}
	runtimeSkillResponse(t, putEditableSkillDocument(h, id, contents[1-winner], initial.Revision), 409, nil)
	files, _ := filepath.Glob(filepath.Join(sk.Dir, ".SKILL.md.edit-*"))
	if len(files) != 0 {
		t.Fatal("temporary edits leaked", files)
	}
}

func TestManagedSkillDocumentEditHTTPValidationKeepsOriginal(t *testing.T) {
	root := t.TempDir()
	h := newHarness(t, idleRespond, func(c *Config) { c.SkillOptions = skills.Options{UserDir: root} })
	sk := uploadEditableSkillFixture(t, h)
	id := skillIDOf(sk)
	initial := getEditableSkillDocument(t, h, id)
	bad := []string{"# Missing frontmatter", strings.Replace(initial.Content, "name: editable-check", "name: renamed", 1), skillEditBody("", "EMPTY"), strings.Replace(initial.Content, "OLD_DESCRIPTION", "\"unterminated", 1), strings.Replace(initial.Content, "description:", "name: editable-check\ndescription:", 1), strings.Replace(initial.Content, "---\n\n", "browser-task: true\n---\n\n", 1), strings.Replace(initial.Content, "---\n\n", "disable-model-invocation: true\n---\n\n", 1), initial.Content + "\x00binary", strings.Replace(initial.Content, "OLD_DESCRIPTION", "[unclosed", 1), strings.Replace(initial.Content, "\n---\n\n", "\nnot-a-key\n---\n\n", 1)}
	for i, content := range bad {
		r := putEditableSkillDocument(h, id, content, initial.Revision)
		r.Body.Close()
		if r.StatusCode != 400 {
			t.Fatalf("invalid edit %d got %d", i, r.StatusCode)
		}
	}
	runtimeSkillResponse(t, putEditableSkillDocument(h, id, initial.Content+strings.Repeat("x", skillDocumentEditLimit), initial.Revision), 413, nil)
	runtimeSkillResponse(t, putEditableSkillDocument(h, id, initial.Content, ""), 400, nil)
	raw := []byte(`{"content":"`)
	raw = append(raw, 0xff)
	raw = append(raw, []byte(`","expected_revision":"`+initial.Revision+`"}`)...)
	runtimeSkillResponse(t, h.do("PUT", "/api/manage/skills/"+id+"/content", string(raw)), 400, nil)
	for _, tail := range []string{"?path=references/guide.md", "?path=../SKILL.md", "?path=%2fSKILL.md"} {
		r := h.do("PUT", "/api/manage/skills/"+id+"/content"+tail, `{"content":"x","expected_revision":"`+initial.Revision+`"}`)
		r.Body.Close()
		if r.StatusCode != 400 {
			t.Fatalf("path=%s status=%d", tail, r.StatusCode)
		}
	}
	current, _ := os.ReadFile(sk.Path)
	if string(current) != initial.Content {
		t.Fatal("invalid edit changed original")
	}
	var reference editableSkillDocument
	runtimeSkillResponse(t, h.do("GET", "/api/manage/skills/"+id+"/content?path=references/guide.md", ""), 200, &reference)
	if reference.Editable || reference.Reason == "" {
		t.Fatal("reference source became editable")
	}
}

func TestManagedSkillDocumentEditHTTPAdminAndImmutableSources(t *testing.T) {
	root := t.TempDir()
	sk := writeTestSkill(t, root, "editable-check", "original", "BODY")
	mounted := writeTestSkill(t, t.TempDir(), "mounted", "mounted", "BODY")
	builtin := builtinOfficeForTest(t)
	service, err := skillruntime.NewManagementService(testfs.SealedTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := service.Import(bytes.NewReader(selectionFixtureZIP(t)))
	if err != nil {
		t.Fatal(err)
	}
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")), func(c *Config) {
		c.SkillOptions = skills.Options{UserDir: root}
		c.Skills = []skills.Skill{sk, mounted, builtin}
		c.SkillRuntime = service
	})
	id := skillIDOf(sk)
	raw, _ := json.Marshal(map[string]string{"content": skillEditBody("new", "BODY"), "expected_revision": "not-used"})
	for _, tc := range []struct {
		token string
		want  int
	}{{aliceToken, 403}, {bobToken, 403}, {"", 401}} {
		r := h.doAs(tc.token, "PUT", "/api/manage/skills/"+id+"/content", string(raw))
		r.Body.Close()
		if r.StatusCode != tc.want {
			t.Fatalf("permission=%d want%d", r.StatusCode, tc.want)
		}
	}
	for _, immutable := range []string{skillIDOf(mounted), skillIDOf(builtin), skillruntime.PackageSkillID(installed.Ref)} {
		r := h.doAs(h.token, "PUT", "/api/manage/skills/"+immutable+"/content", `{"content":"x","expected_revision":"`+strings.Repeat("a", 64)+`"}`)
		r.Body.Close()
		if r.StatusCode != 403 {
			t.Fatalf("immutable=%s status=%d", immutable, r.StatusCode)
		}
		r = h.doAs(h.token, "GET", "/api/manage/skills/"+immutable+"/content", "")
		var doc editableSkillDocument
		if err := decodeJSONBody(r, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Editable || doc.Reason == "" {
			t.Fatal("immutable source has edit affordance")
		}
	}
}

func TestManagedSkillDocumentEditRejectsFileAndDirectorySymlinks(t *testing.T) {
	root := t.TempDir()
	sk := writeTestSkill(t, root, "editable-check", "original", "BODY")
	outside := writeTestSkill(t, t.TempDir(), "outside", "outside", "OUTSIDE")
	original, _ := os.ReadFile(outside.Path)
	h := newHarness(t, idleRespond, func(c *Config) { c.SkillOptions = skills.Options{UserDir: root}; c.Skills = []skills.Skill{sk} })
	id := skillIDOf(sk)
	initial := getEditableSkillDocument(t, h, id)
	if err := os.Remove(sk.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside.Path, sk.Path); err != nil {
		t.Fatal(err)
	}
	runtimeSkillResponse(t, putEditableSkillDocument(h, skillIDOf(sk), skillEditBody("new", "NEW"), initial.Revision), 403, nil)
	if err := os.Remove(sk.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(sk.Dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside.Dir, sk.Dir); err != nil {
		t.Fatal(err)
	}
	runtimeSkillResponse(t, putEditableSkillDocument(h, skillIDOf(sk), skillEditBody("new", "NEW"), initial.Revision), 403, nil)
	got, _ := os.ReadFile(outside.Path)
	if !bytes.Equal(original, got) {
		t.Fatal("symlink target was overwritten")
	}
}

func TestManagedSkillDocumentEditBusyPromptChangesAtIdle(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	root := t.TempDir()
	sk := writeTestSkill(t, root, "editable-check", "OLD_DESCRIPTION", "OLD_BODY")
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			close(entered)
			<-release
		}
		return textTurn("done"), nil
	}, func(c *Config) { c.SkillOptions = skills.Options{UserDir: root}; c.Skills = []skills.Skill{sk} })
	a := postStudioAgent(t, h, studioAgentCapsBody("Busy edit", "ready", map[string]any{"skills": []string{skillIDOf(sk)}}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	initial := getEditableSkillDocument(t, h, skillIDOf(sk))
	stream := h.start(sid, "/skill:editable-check inspect")
	defer stream.close()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	runtimeSkillResponse(t, putEditableSkillDocument(h, skillIDOf(sk), skillEditBody("NEW_DESCRIPTION", "NEW_BODY"), initial.Revision), 200, nil)
	s.mu.Lock()
	old := s.invocationSkills[0].Description
	s.mu.Unlock()
	if old != "OLD_DESCRIPTION" {
		t.Fatal("active run metadata changed in flight")
	}
	sync := syncStudioSkillsHTTP(t, h, a.ID, sid)
	if !sync.Deferred {
		t.Fatal("active edit not deferred")
	}
	once.Do(func() { close(release) })
	stream.wait(t, EvRunEnd)
	s.wait()
	// run_end is streamed before finish completes its idle policy updates;
	// wait() may already observe the cleared active slot. Observe the actual
	// promised outcome without forcing a sync request or racing that boundary.
	deadline := time.Now().Add(3 * time.Second)
	current := ""
	for {
		s.mu.Lock()
		current = s.invocationSkills[0].Description
		s.mu.Unlock()
		if current == "NEW_DESCRIPTION" || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if current != "NEW_DESCRIPTION" {
		t.Fatal("idle boundary did not refresh saved metadata")
	}
}
