package web

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yosukeno/pogo/internal/testfs"
	"github.com/yosukeno/pogo/skillruntime"
)

func TestRuntimeSkillDeleteProtectsDraftsAndHistoricalBindings(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	service, err := skillruntime.NewManagementService(testfs.SealedTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	h.mgr.cfg.SkillRuntime = service
	installed, err := service.Import(bytes.NewReader(selectionFixtureZIP(t)))
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/manage/skills/" + skillruntime.PackageSkillID(installed.Ref)
	runtimeSkillResponse(t, h.doAs(aliceToken, "DELETE", path, ""), 403, nil)
	runtimeSkillResponse(t, h.doAs(h.token, "DELETE", "/api/manage/skills/invalid", ""), 404, nil)
	selection := skillruntime.Selection{SkillIDs: []string{skillruntime.PackageSkillID(installed.Ref)}}
	body := studioAgentCapsBody("Protected draft", "draft", map[string]any{"skills": selection.SkillIDs, "mcp_servers": []string{}})
	var a studioAgent
	runtimeSkillResponse(t, h.doAs(h.token, "POST", "/api/manage/agents", studioBody(body)), 201, &a)
	runtimeSkillResponse(t, h.doAs(h.token, "DELETE", path, ""), 409, nil)
	// Incomplete drafts can have only selection intent and no generated binding.
	h.mgr.studio.mu.Lock()
	draft := a
	draft.SkillBinding = nil
	h.mgr.studio.data.Agents[a.ID] = draft
	h.mgr.studio.mu.Unlock()
	runtimeSkillResponse(t, h.doAs(h.token, "DELETE", path, ""), 409, nil)
	// A deleted Agent's retained conversation is still a pinned dependency.
	runtimeSkillResponse(t, h.doAs(h.token, "DELETE", "/api/manage/agents/"+a.ID, ""), 204, nil)
	sid := "Skill-delete-history"
	transcript := filepath.Join(h.mgr.cfg.SessionDir, sid+".jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lock, _ := json.Marshal(map[string]any{"packages": []any{installed.Ref}})
	h.mgr.studio.mu.Lock()
	h.mgr.studio.data.Bindings[sid] = studioBinding{Agent: a, SkillLock: lock}
	h.mgr.studio.mu.Unlock()
	runtimeSkillResponse(t, h.doAs(h.token, "DELETE", path, ""), 409, nil)
	if err := os.Remove(transcript); err != nil {
		t.Fatal(err)
	}
	var deleted skillruntime.DeleteResult
	runtimeSkillResponse(t, h.doAs(h.token, "DELETE", path, ""), 200, &deleted)
	if !deleted.Deleted || deleted.CleanupPending || len(service.SelectionCatalog().Skills) > 0 {
		t.Fatalf("delete incomplete: %+v", deleted)
	}
	if _, err := service.Store.Load(installed.Ref); err == nil {
		t.Fatal("deleted files still loadable")
	}
	runtimeSkillResponse(t, h.doAs(h.token, "DELETE", path, ""), 404, nil)
}
