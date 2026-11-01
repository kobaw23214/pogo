package web

// Manifest-tool registration follows explicit skill selection: an Agent that
// pinned the skill pays its schema tokens, a legacy definition (nil
// selection) and an ordinary session keep every other skill surface but
// register none of the bundle's tools.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
)

func writeSkillFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestManifestToolsRegisterOnlyForExplicitSelection(t *testing.T) {
	userDir := t.TempDir()
	dir := filepath.Join(userDir, "manifest-skill")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillFile(t, filepath.Join(dir, "SKILL.md"), "---\nname: manifest-skill\ndescription: Declares one manifest tool.\n---\nBody.\n")
	writeSkillFile(t, filepath.Join(dir, "tools.json"), `{"tools":[{
		"name": "manifest.echo",
		"description": "Echo for the registration test.",
		"inputSchema": {"type": "object", "properties": {"text": {"type": "string", "minLength": 1}}, "required": ["text"], "additionalProperties": false},
		"annotations": {"readOnlyHint": true},
		"x-pogo-exec": {"argv": ["$SKILL_DIR/scripts/echo"], "map": {"text": "append"}}
	}]}`)

	turns := []llm.Response{textTurn("legacy answer"), textTurn("explicit answer")}
	h, cap := newStudioHarness(t, turns, func(c *Config) {
		c.SkillOptions = skills.Options{UserDir: userDir}
	})
	// The harness skips the serve-path boot scan; load the bundle the way the
	// scan would (SetSkills rebuilds the manifest-tool projections with it).
	list, diags := skills.Load(skills.Options{UserDir: userDir})
	for _, d := range diags {
		t.Errorf("skill diag: %s: %s", d.Path, d.Message)
	}
	h.mgr.SetSkills(list)

	// The catalog carries the declared tool regardless of selection.
	skillID := ""
	for id, sk := range h.mgr.skillCatalogIDs() {
		if sk.Name == "manifest-skill" {
			skillID = id
		}
	}
	if skillID == "" {
		t.Fatal("manifest-skill did not load")
	}
	found := false
	for _, info := range h.mgr.SynthCatalog() {
		if info.WireName == "manifest_echo" {
			found = true
		}
	}
	if !found {
		t.Fatal("declared tool missing from the catalog")
	}

	// Legacy definition: no Skills selection. Every other surface stays; the
	// model-visible registry must not carry the manifest tool.
	legacy := studioAgentBody("Legacy selection", "", "ready")
	var legacyDef studioAgent
	runtimeSkillResponse(t, h.do("POST", "/api/manage/agents", studioBody(legacy)), 201, &legacyDef)
	legacyConversation := createStudioConversationBody(t, h, legacyDef.ID, "")
	studioAsk(t, h, legacyConversation.ID, "hello")
	schemas := cap.toolSchemas()
	legacyNames := schemas[len(schemas)-1]
	for _, name := range legacyNames {
		if name == "manifest_echo" {
			t.Fatal("legacy selection registered the manifest tool")
		}
	}

	// Explicit selection: the tool registers for that conversation alone.
	explicit := studioAgentBody("Explicit selection", "", "ready")
	explicit["skills"] = []string{skillID}
	var explicitDef studioAgent
	runtimeSkillResponse(t, h.do("POST", "/api/manage/agents", studioBody(explicit)), 201, &explicitDef)
	explicitConversation := createStudioConversationBody(t, h, explicitDef.ID, "")
	studioAsk(t, h, explicitConversation.ID, "hello")
	schemas = cap.toolSchemas()
	explicitNames := schemas[len(schemas)-1]
	present := false
	for _, name := range explicitNames {
		if name == "manifest_echo" {
			present = true
		}
	}
	if !present {
		t.Fatalf("explicit selection did not register the manifest tool: %v", explicitNames)
	}
}
