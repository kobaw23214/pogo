package web

// Opt-in isolated joint acceptance: real HTTP/SSE/registry and separately built
// UI, deterministic test model, disposable stores, no deployment/network state.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/shortcuts"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

func TestShortcutManagementLocalUI(t *testing.T) {
	contextPath := os.Getenv("POGO_SHORTCUT_MANAGEMENT_UI_CONTEXT")
	if contextPath == "" {
		t.Skip("isolated joint UI acceptance requires explicit POGO_SHORTCUT_MANAGEMENT_UI_CONTEXT")
	}
	build := os.Getenv("POGO_SHORTCUT_MANAGEMENT_UI_BUILD")
	for _, p := range []string{contextPath, build} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || !strings.Contains(filepath.ToSlash(p), "/.localagent/checks/shortcut-management-20261022/") {
			t.Fatal("acceptance paths must stay in the absolute task directory")
		}
	}
	for _, p := range []string{contextPath, contextPath + ".done"} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatal("acceptance context or completion marker already exists")
		}
	}
	if st, err := os.Stat(filepath.Join(build, "index.html")); err != nil || !st.Mode().IsRegular() {
		t.Fatal("fresh private UI build required")
	}
	var h *harness
	var s *Session
	var mixed shortcuts.Entry
	officeSkill := writeTestSkill(t, t.TempDir(), "office", "office", "Isolated Office snapshot fixture, no executable tools.")
	h = newHarness(t, func(n int) (llm.Response, error) {
		if n < 1 || n > 16 {
			return llm.Response{}, errors.New("unexpected additional fixture model call")
		}
		if n%2 == 0 {
			return textTurn("隔离验收操作已完成。"), nil
		}
		view, err := h.mgr.userShortcuts(s.owner, s.studioID)
		if err != nil {
			return llm.Response{}, err
		}
		folderID := ""
		if len(view.Folders) == 1 {
			folderID = view.Folders[0].ID
		}
		mutation := shortcuts.MutationRequest{RequestID: fmt.Sprintf("ui-organize-%02d", n), ExpectedRevision: view.Revision}
		var name string
		var args any
		switch (n - 1) / 2 {
		case 0:
			name, args = tools.ShortcutListName, shortcuts.ListRequest{}
		case 1:
			name, args = tools.ShortcutFolderCreateName, shortcuts.FolderCreateRequest{MutationRequest: mutation, Name: "数据库"}
		case 2:
			name, args = tools.ShortcutMoveName, shortcuts.MoveRequest{MutationRequest: mutation, IDs: []string{mixed.ID}, ParentID: folderID}
		case 3:
			title := "混排流程新版"
			name, args = tools.ShortcutUpdateName, shortcuts.UpdateRequest{MutationRequest: mutation, ID: mixed.ID, Title: &title}
		case 4:
			name, args = tools.ShortcutFolderRenameName, shortcuts.FolderRenameRequest{MutationRequest: mutation, ID: folderID, Name: "数据库操作"}
		case 5:
			name, args = tools.ShortcutMergeName, shortcuts.MergeRequest{MutationRequest: shortcuts.MutationRequest{ExpectedRevision: view.Revision}}
		case 6:
			planID := ""
			for _, result := range s.hub.Snapshot().Results {
				raw, _ := json.Marshal(result.Details)
				var details tools.ShortcutDetails
				_ = json.Unmarshal(raw, &details)
				if details.Action == "merge" && !details.Saved && details.Plan != nil {
					planID = details.Plan.ID
				}
			}
			if planID == "" {
				return llm.Response{}, errors.New("joint fixture did not receive a genuine host merge plan")
			}
			name, args = tools.ShortcutMergeName, shortcuts.MergeRequest{MutationRequest: mutation, Apply: true, PlanID: planID}
		case 7:
			name, args = tools.ShortcutDeleteName, shortcuts.DeleteRequest{MutationRequest: mutation, IDs: []string{folderID}, Recursive: true}
		}
		raw, err := json.Marshal(args)
		if err != nil {
			return llm.Response{}, err
		}
		response := toolTurn(name, string(raw))
		response.Message.Content[0].ID = fmt.Sprintf("ui-organize-call-%02d", n)
		return response, nil
	}, func(c *Config) { c.Skills = []skills.Skill{officeSkill}; c.Followups = false })
	a := postStudioAgent(t, h, studioAgentCapsBody("快捷方式整理联合验收", "ready", map[string]any{"tools": []string{tools.ShortcutCreateName, tools.ShortcutFolderCreateName, tools.ShortcutUpdateName, tools.ShortcutFolderRenameName, tools.ShortcutMoveName, tools.ShortcutDeleteName, tools.ShortcutMergeName}}), http.StatusCreated)
	sid := createStudioConversationBody(t, h, a.ID, "{}").ID
	s = h.session(sid)
	h.setPolicy(sid, ModeAuto, 0)
	b := h.mgr.shortcutToolBackend(s)
	if err := os.MkdirAll(filepath.Join(s.cwd, "uploads"), 0700); err != nil {
		t.Fatal(err)
	}
	originalFile := []byte("ISOLATED FILE SNAPSHOT: unchanged after organization\n")
	if err := os.WriteFile(filepath.Join(s.cwd, "uploads", "evidence.txt"), originalFile, 0600); err != nil {
		t.Fatal(err)
	}
	selection := &inputref.OfficeSnapshot{Path: "uploads/selection.xlsx", Title: "合成选区", Editor: "cell", Sheet: "Data", Range: "B2:C3", Text: "甲\t3\n乙\t5", CanvasVersion: "fixture-v1", CapturedAt: "2026-10-22T00:00:00Z", SourceSession: sid}
	refs := []shortcutToolReference{
		b.issue("browser", "fixture-web", "网页按钮", "", "", "fixture-v1", "message", []inputref.Part{{Type: "element", Text: orderedTestElement}}),
		b.issue("file", "fixture-file", "证据文件", "", "", "fixture-v1", "message", []inputref.Part{{Type: "file", Path: "uploads/evidence.txt", AsFile: true}}),
		b.issue("office", "fixture-selection", "合成选区", "", "", "fixture-v1", "message", []inputref.Part{{Type: "office", Office: selection}}),
		b.issue("skill", "fixture-skill", "office", "", "", "fixture-v1", "skill", []inputref.Part{{Type: "skill", Name: "office"}}),
	}
	for i, ref := range refs {
		frozen, err := b.freezeMessageFiles(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		refs[i] = frozen
		b.refs[frozen.summary.RefID] = frozen
	}
	seed, err := b.Create(context.Background(), shortcuts.CreateRequest{RequestID: "ui-fixture-seed", Graph: shortcuts.Graph{RootID: "root", Nodes: []shortcuts.Node{{LocalID: "root", Title: "混排流程", Parts: []shortcuts.Part{{Kind: "text", Text: "开始|"}, {Kind: "ref", RefID: refs[0].summary.RefID}, {Kind: "ref", RefID: refs[1].summary.RefID}, {Kind: "ref", RefID: refs[2].summary.RefID}, {Kind: "ref", RefID: refs[3].summary.RefID}, {Kind: "node", NodeID: "child"}, {Kind: "text", Text: "|结束"}}}, {LocalID: "child", Title: "嵌套子卡", Parts: []shortcuts.Part{{Kind: "text", Text: "内层原文不可丢"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	mixed = seed.Shortcut
	view, err := h.mgr.userShortcuts(s.owner, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	view.Shortcuts = append(view.Shortcuts, userShortcut{ID: "duplicate-a", Title: "完全重复", Content: "同目录完整相同内容"}, userShortcut{ID: "duplicate-b", Title: "完全重复", Content: "同目录完整相同内容"})
	view, err = h.mgr.replaceUserShortcuts(s.owner, a.ID, view.Revision, view.Shortcuts)
	if err != nil {
		t.Fatal(err)
	}
	initialRevision := view.Revision
	api := h.srv.Config.Handler
	static := http.FileServer(http.Dir(build))
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			api.ServeHTTP(w, r)
			return
		}
		clean := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if st, err := os.Stat(filepath.Join(build, filepath.FromSlash(clean))); err == nil && st.Mode().IsRegular() {
			static.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(build, "index.html"))
	}))
	t.Cleanup(frontend.Close)
	done := contextPath + ".done"
	if err := os.MkdirAll(filepath.Dir(contextPath), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"origin": frontend.URL, "token": h.token, "agentId": a.ID, "sessionId": sid, "donePath": done, "mixedCardId": mixed.ID, "initialRevision": initialRevision, "modelEvidence": "deterministic isolated test model, not a live provider"})
	if err := os.WriteFile(contextPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(contextPath); _ = os.Remove(done) })
	timer := time.NewTimer(360 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatal("isolated shortcut UI fixture timed out")
		case <-tick.C:
			marker, err := os.ReadFile(done)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil || strings.TrimSpace(string(marker)) != "passed" {
				t.Fatal("joint UI acceptance did not pass")
			}
			final, err := h.mgr.userShortcuts(s.owner, a.ID)
			if err != nil || final.Revision != initialRevision+6 || len(final.Folders) != 0 || len(final.Shortcuts) != 1 || final.Shortcuts[0].ID != "duplicate-a" {
				t.Fatalf("final organization state differs: %+v %v", final, err)
			}
			if raw, err := os.ReadFile(filepath.Join(s.cwd, "uploads", "evidence.txt")); err != nil || !reflect.DeepEqual(raw, originalFile) {
				t.Fatal("source file changed during organization")
			}
			foundSnapshot := false
			for _, result := range s.hub.Snapshot().Results {
				raw, _ := json.Marshal(result.Details)
				var details tools.ShortcutDetails
				_ = json.Unmarshal(raw, &details)
				if details.Action == "update" {
					if details.Shortcut == nil || details.Shortcut.ID != mixed.ID || details.Shortcut.Title != "混排流程新版" || !reflect.DeepEqual(details.Shortcut.Parts, mixed.Parts) {
						t.Fatal("metadata update or later deletion changed the frozen mixed snapshot")
					}
					if err = h.mgr.validateShortcutAssets(s.owner, a.ID, []userShortcut{{ID: mixed.ID, Title: mixed.Title, Content: mixed.Content, Parts: mixed.Parts}}); err != nil {
						t.Fatal("deletion removed a referenced private asset", err)
					}
					foundSnapshot = true
				}
			}
			if !foundSnapshot {
				t.Fatal("update snapshot was not preserved")
			}
			fmt.Println("isolated shortcut management UI accepted; deterministic test model; production mutations=0")
			return
		}
	}
}
