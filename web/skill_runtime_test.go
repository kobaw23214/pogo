package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skillruntime"
)

func TestSkillManagementWithoutRuntime(t *testing.T) {
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")})
	resp := h.do("GET", "/api/manage/skills", "")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("disabled Skill route returned %d", resp.StatusCode)
	}
	// Skill 绑定由服务端从所选技能计算,从不接受客户端字段(studioInput.skillBinding
	// 无 json 标签):注入的 skill_binding 必须被忽略而不是生效。
	body := studioAgentBody("Skill", "", "ready")
	body["skill_binding"] = map[string]any{"kind": "AgentBinding"}
	resp2 := h.do("POST", "/api/manage/agents", studioBody(body))
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("agent with injected skill_binding returned %d", resp2.StatusCode)
	}
	var created studioAgent
	if err := json.NewDecoder(resp2.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if len(created.SkillBinding) != 0 {
		t.Fatalf("client-supplied skill_binding leaked into the definition: %s", created.SkillBinding)
	}
	// 而选择运行时技能(rs_)在无运行环境时必须被拒绝。
	body2 := studioAgentBody("Skill2", "", "ready")
	body2["skills"] = []string{"rs_deadbeef"}
	resp3 := h.do("POST", "/api/manage/agents", studioBody(body2))
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusBadRequest {
		t.Fatalf("rs_ selection without runtime returned %d, want 400", resp3.StatusCode)
	}
}

func TestRuntimeSkillRouteBoundary(t *testing.T) {
	base := t.TempDir()
	if privateRuntimeSkillStore(filepath.Join(base, "Skills"), base) {
		t.Fatal("Skill store exposed inside workspace")
	}
	if !privateRuntimeSkillStore(filepath.Join(base, "Skills"), filepath.Join(base, "workspaces")) {
		t.Fatal("separate Skill store rejected")
	}
}

func runtimeSkillResponse(t *testing.T, resp *http.Response, status int, out any) {
	t.Helper()
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("HTTP %d, expected %d: %s", resp.StatusCode, status, data)
	}
	if out != nil {
		if err = json.Unmarshal(data, out); err != nil {
			t.Fatal(err)
		}
	}
}

// This test uses the real Pogo HTTP server, session/agent loop and YARA engine.
// Only the LLM is scripted to select the tool deterministically, without keys.
func TestRuntimeSkillYARAWebIntegration(t *testing.T) {
	archive := os.Getenv("POGO_SKILL_RUNTIME_TEST_ARCHIVE")
	local := os.Getenv("POGO_SKILL_RUNTIME_TEST_LOCAL")
	if archive == "" || local != "1" {
		t.Skip("set real Skill archive and POGO_SKILL_RUNTIME_TEST_LOCAL=1 in the Linux sandbox environment for acceptance")
	}
	service, err := skillruntime.NewService(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	archiveBytes, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	turns := []llm.Response{textTurn("placeholder"), textTurn("YARA analysis complete")}
	h, capture := newStudioHarness(t, turns, func(c *Config) { c.SkillRuntime = service })
	installed, err := service.Import(bytes.NewReader(archiveBytes))
	if err != nil {
		t.Fatal(err)
	}
	body := studioAgentBody("YARA Skill", "Use the bound capability and cite the matched rule.", "ready")
	body["skills"] = []string{skillruntime.PackageSkillID(installed.Ref)}
	var definition studioAgent
	runtimeSkillResponse(t, h.do("POST", "/api/manage/agents", studioBody(body)), 201, &definition)
	conversation := createStudioConversationBody(t, h, definition.ID, "")
	var uploaded skillruntime.UploadedFile
	runtimeSkillResponse(t, h.do("POST", "/api/sessions/"+conversation.ID+"/skill-files", "POGO_SKILL_RUNTIME_SAFE_FIXTURE"), 201, &uploaded)
	sess := h.session(conversation.ID)
	if sess.runtimeSkill == nil {
		t.Fatal("Skill session not constructed")
	}
	descriptors := sess.runtimeSkill.Descriptors()
	if len(descriptors) != 1 {
		t.Fatal(descriptors)
	}
	turns[0] = toolTurn(descriptors[0].Name, studioBody(map[string]string{"fileId": uploaded.FileID}))
	sess.policy.Set(ModeAuto, 0)
	studioAsk(t, h, conversation.ID, "Scan fileId "+uploaded.FileID)
	schemas := capture.toolSchemas()
	if len(schemas) != 2 || !slices.Contains(schemas[0], descriptors[0].Name) {
		t.Fatalf("unexpected registry: %v", schemas)
	}
	_, _, histories, _ := capture.snapshot()
	found := false
	for _, msg := range histories[1] {
		for _, block := range msg.Content {
			if block.Type == llm.BlockToolResult && strings.Contains(block.Text, "PogoPrototypeMarker") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("real YARA result did not reach the Pogo agent loop")
	}
	var bad map[string]any
	runtimeSkillResponse(t, h.do("POST", "/api/sessions/"+conversation.ID+"/skill-calls", studioBody(map[string]any{"capability": descriptors[0].ID, "input": map[string]string{"fileId": "/etc/passwd"}})), 400, &bad)
	other := createStudioConversationBody(t, h, definition.ID, "")
	runtimeSkillResponse(t, h.do("POST", "/api/sessions/"+other.ID+"/skill-calls", studioBody(map[string]any{"capability": descriptors[0].ID, "input": map[string]string{"fileId": uploaded.FileID}})), 400, nil)
	if secondArchive := os.Getenv("POGO_SKILL_RUNTIME_TEST_ARCHIVE_V2"); secondArchive != "" {
		secondBytes, err := os.ReadFile(secondArchive)
		if err != nil {
			t.Fatal(err)
		}
		second, err := service.Import(bytes.NewReader(secondBytes))
		if err != nil {
			t.Fatal(err)
		}
		update := studioAgentBody("YARA Skill", "Use the bound capability.", "ready")
		update["skills"] = []string{skillruntime.PackageSkillID(second.Ref)}
		update["revision"] = definition.Revision
		runtimeSkillResponse(t, h.do("PATCH", "/api/manage/agents/"+definition.ID, studioBody(update)), 200, nil)
		upgraded := createStudioConversationBody(t, h, definition.ID, "")
		var secondFile skillruntime.UploadedFile
		runtimeSkillResponse(t, h.do("POST", "/api/sessions/"+upgraded.ID+"/skill-files", "POGO_SKILL_RUNTIME_SAFE_FIXTURE"), 201, &secondFile)
		var upgradedResult skillruntime.CallResult
		runtimeSkillResponse(t, h.do("POST", "/api/sessions/"+upgraded.ID+"/skill-calls", studioBody(map[string]any{"capability": descriptors[0].ID, "input": map[string]string{"fileId": secondFile.FileID}})), 200, &upgradedResult)
		if !strings.Contains(string(upgradedResult.Data), "PogoUpgradeMarker") {
			t.Fatalf("new session did not use new rules: %s", upgradedResult.Data)
		}
		// A failed import must leave the current catalog and pinned old version usable.
		if _, err := service.Import(strings.NewReader("invalid archive")); err == nil {
			t.Fatal("invalid archive accepted")
		}
	}
	// RawMessage indentation changes in the registry must preserve the exact
	// separately published binding bytes and lock across a server restart.
	configCopy := h.mgr.cfg
	h.mgr.Close()
	reopened, err := skillruntime.NewService(context.Background(), service.Root)
	if err != nil {
		t.Fatal(err)
	}
	configCopy.SkillRuntime = reopened
	restoredManager, err := NewManager(configCopy)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredManager.Close()
	restored, err := restoredManager.Get(conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.runtimeSkill == nil {
		t.Fatal("Skill lock did not survive a real Manager reload")
	}
	result, err := restored.runtimeSkill.Call(context.Background(), descriptors[0].ID, json.RawMessage(studioBody(map[string]string{"fileId": uploaded.FileID})))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Data), "PogoPrototypeMarker") {
		t.Fatal(string(result.Data))
	}
	if err = reopened.Revoke(installed.Ref.PackageDigest); err != nil {
		t.Fatal(err)
	}
	if _, err = restored.runtimeSkill.Call(context.Background(), descriptors[0].ID, json.RawMessage(studioBody(map[string]string{"fileId": uploaded.FileID}))); err == nil {
		t.Fatal("revoked pinned version executed")
	}
	resumed, err := skillruntime.NewService(context.Background(), service.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = resumed.Session(restored.runtimeSkill.Binding, restored.runtimeSkill.Lock, conversation.ID); err == nil {
		t.Fatal("revocation did not survive reload")
	}
}

func TestRuntimeSkillOwnerIsolationIntegration(t *testing.T) {
	archive := os.Getenv("POGO_SKILL_RUNTIME_TEST_ARCHIVE")
	local := os.Getenv("POGO_SKILL_RUNTIME_TEST_LOCAL")
	if archive == "" || local != "1" {
		t.Skip("set real Skill archive and image")
	}
	service, err := skillruntime.NewService(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := service.Import(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	h := newMultiuserHarness(t, scriptedTurns(textTurn("ok")))
	// Configure before any request creates or restores a session.
	h.mgr.cfg.SkillRuntime = service
	body := studioAgentBody("Owned YARA", "Use the selected Skill.", "ready")
	body["skills"] = []string{skillruntime.PackageSkillID(installed.Ref)}
	var definition studioAgent
	runtimeSkillResponse(t, h.doAs(h.token, "POST", "/api/manage/agents", studioBody(body)), 201, &definition)
	var conversation studioConversation
	runtimeSkillResponse(t, h.doAs(aliceToken, "POST", "/api/apps/"+definition.ID+"/sessions", ""), 201, &conversation)
	var uploaded skillruntime.UploadedFile
	runtimeSkillResponse(t, h.doAs(aliceToken, "POST", "/api/sessions/"+conversation.ID+"/skill-files", "private harmless bytes"), 201, &uploaded)
	runtimeSkillResponse(t, h.doAs(bobToken, "GET", "/api/sessions/"+conversation.ID+"/skill-capabilities", ""), 404, nil)
	runtimeSkillResponse(t, h.doAs(bobToken, "POST", "/api/sessions/"+conversation.ID+"/skill-files", "wrong owner"), 404, nil)
	runtimeSkillResponse(t, h.doAs(bobToken, "POST", "/api/skills/upload", "not an admin"), 403, nil)
	runtimeSkillResponse(t, h.doAs(aliceToken, "POST", "/api/sessions/"+conversation.ID+"/skill-calls", "{}"), 403, nil)
	runtimeSkillResponse(t, h.doAs(aliceToken, "POST", "/api/sessions/"+conversation.ID+"/control", `{"action":"set_policy","mode":"standard"}`), 200, nil)
	runtimeSkillResponse(t, h.doAs(aliceToken, "POST", "/api/sessions/"+conversation.ID+"/control", `{"action":"rewind","mode":"files","message_id":"fake"}`), 403, nil)
}
