package web

// This opt-in fixture serves the real Web backend and a separately built UI.
// Its only model is deterministic test data: it never loads deployment state,
// browser/Office sessions, provider credentials, or business MCP connections.
import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/tools"
)

func TestAuthoredSkillLocalUI(t *testing.T) {
	contextPath := os.Getenv("POGO_AUTHOR_UI_QA_CONTEXT")
	if contextPath == "" {
		t.Skip("isolated UI fixture requires explicit POGO_AUTHOR_UI_QA_CONTEXT")
	}
	if !filepath.IsAbs(contextPath) || filepath.Clean(contextPath) != contextPath || !strings.Contains(filepath.ToSlash(contextPath), "/.localagent/") {
		t.Fatal("UI context must be an absolute .localagent path")
	}
	if _, err := os.Lstat(contextPath); !os.IsNotExist(err) {
		t.Fatal("UI context already exists or cannot be inspected")
	}
	build := "/tmp/pogo-skill-authoring-ui-build"
	if st, err := os.Stat(filepath.Join(build, "index.html")); err != nil || !st.Mode().IsRegular() {
		t.Fatal("fresh isolated frontend build is unavailable")
	}
	request := authoredRequest("ui-create-once", "ui-report", "UI ORIGINAL WORKFLOW: prepare the synthetic report, then read the bundled SQL example.")
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, scriptedTurns(toolTurn(tools.SkillSaveName, string(raw)), textTurn("已保存 ui-report。此 Skill 仅进行了结构验证，未进行行为验证。"), textTurn("UI_SKILL_USED: 已读取合成报告流程。")))
	a := postStudioAgent(t, h, studioAgentCapsBody("Skill UI QA Agent", "ready", map[string]any{"skills": []string{}, "tools": []string{tools.SkillSaveName}}), http.StatusCreated)
	other := postStudioAgent(t, h, studioAgentCapsBody("Separate UI QA Agent", "ready", map[string]any{"skills": []string{}, "tools": []string{}}), http.StatusCreated)
	sid := createStudioConversationBody(t, h, a.ID, "{}").ID
	h.setPolicy(sid, ModeAuto, 0)
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
	if _, err := os.Lstat(done); !os.IsNotExist(err) {
		t.Fatal("UI completion marker already exists or cannot be inspected")
	}
	if err := os.MkdirAll(filepath.Dir(contextPath), 0700); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"origin": frontend.URL, "token": h.token, "agentId": a.ID, "sessionId": sid, "otherAgentId": other.ID, "donePath": done, "skillName": "ui-report", "originalMarker": "UI ORIGINAL WORKFLOW", "referenceMarker": "synthetic-only", "modelEvidence": "deterministic local test model, not a live provider"}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(contextPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(contextPath); _ = os.Remove(done) })
	deadline := time.NewTimer(300 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("isolated UI fixture timed out waiting for its completion marker")
		case <-tick.C:
			if marker, err := os.ReadFile(done); err == nil {
				if strings.TrimSpace(string(marker)) != "passed" {
					t.Fatal("UI acceptance reported failure")
				}
				fmt.Println("isolated authored Skill UI fixture completed; test model only")
				return
			} else if !os.IsNotExist(err) {
				t.Fatal("cannot read UI completion marker")
			}
		}
	}
}
