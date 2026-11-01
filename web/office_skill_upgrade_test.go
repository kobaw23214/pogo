package web

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

func builtinOfficeForTest(t *testing.T) skills.Skill {
	t.Helper()
	dirs, err := skills.MaterializeEmbedded(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	list, _ := skills.Load(skills.Options{Cwd: t.TempDir(), UserDir: t.TempDir(), Embedded: dirs})
	sk, ok := skills.Find(list, "office")
	if !ok {
		t.Fatal("embedded Office missing")
	}
	return sk
}

func TestOfficeSkillUpgradeKeepsOldAgentSelectionAndInvocation(t *testing.T) {
	sk := builtinOfficeForTest(t)
	old := sk
	old.Name, old.Aliases = "docs", nil
	oldID := skillIDOf(old)
	if oldID == "" || skillIDOf(sk) != oldID || skillIdentityOf(sk).Name != "office" {
		t.Fatalf("old pinned identity lost: old=%s current=%+v", oldID, skillIdentityOf(sk))
	}
	h, capture := newStudioHarness(t, []llm.Response{textTurn("ok"), textTurn("ok")}, func(c *Config) { c.Skills = []skills.Skill{sk} })
	h.mgr.officeForSession = func(string, string, *identity.Scope, *Hub) tools.OfficeLink { return &registrationOfficeLink{} }
	def := postStudioAgent(t, h, studioAgentCapsBody("Existing docs Agent", "ready", map[string]any{"skills": []string{oldID}}), http.StatusCreated)
	sid := startStudioConversation(t, h, def.ID)
	studioAsk(t, h, sid, "/skill:docs 检查当前文档")
	_, _, histories, _ := capture.snapshot()
	if len(histories) != 1 || !strings.Contains(histories[0][0].Text(), `<skill name="office"`) || !strings.Contains(histories[0][0].Text(), "检查当前文档") {
		t.Fatalf("old shortcut did not load upgraded Office instructions: %+v", histories)
	}
	if !slices.Contains(capture.toolSchemas()[0], tools.OfficeToolName) || !slices.Contains(capture.toolSchemas()[0], "docs_xlsx-sheet") {
		t.Fatalf("Office or existing file tools lost: %v", capture.toolSchemas())
	}
	response := h.do("GET", "/api/skills?session_id="+sid, "")
	defer response.Body.Close()
	var out struct {
		Skills []struct {
			Name    string            `json:"name"`
			Aliases []string          `json:"aliases"`
			Tools   []tools.SynthInfo `json:"tools"`
		} `json:"skills"`
	}
	if err := json.NewDecoder(response.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || len(out.Skills) != 1 || out.Skills[0].Name != "office" || !slices.Contains(out.Skills[0].Aliases, "docs") {
		t.Fatalf("visible catalog did not expose exactly one Office choice: %+v", out)
	}
	foundOffice := false
	for _, info := range out.Skills[0].Tools {
		foundOffice = foundOffice || info.WireName == tools.OfficeToolName
	}
	if !foundOffice {
		t.Fatal("Office integrated editor tool missing from Office catalog")
	}
	h.mgr.evictAll()
	studioAsk(t, h, sid, "/skill:office 再次读取")
	if !slices.Contains(capture.toolSchemas()[1], tools.OfficeToolName) {
		t.Fatal("reopened old Agent lost Office registration")
	}
}
