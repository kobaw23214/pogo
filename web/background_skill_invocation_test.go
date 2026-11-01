package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

func backgroundBuiltinForInvocationTest(t *testing.T) skills.Skill {
	t.Helper()
	dirs, err := skills.MaterializeEmbedded(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	loaded, _ := skills.Load(skills.Options{Cwd: t.TempDir(), UserDir: t.TempDir(), Embedded: dirs})
	sk, ok := skills.Find(loaded, "background-tasks")
	if !ok || sk.Source != "embedded" {
		t.Fatal("real embedded background-tasks Skill is unavailable")
	}
	return sk
}

func TestBackgroundSkillInvocationKeepsNativePermissionsAndSessionIdentity(t *testing.T) {
	for _, grantBackground := range []bool{false, true} {
		name := "tool-withheld"
		if grantBackground {
			name = "tool-granted"
		}
		t.Run(name, func(t *testing.T) {
			sk := backgroundBuiltinForInvocationTest(t)
			selectedTools := []string{"read"}
			if grantBackground {
				selectedTools = append(selectedTools, tools.BackgroundTaskName)
			}
			h, captured := newStudioHarness(t, []llm.Response{textTurn("检查完毕，未创建任务"), textTurn("已说明能力边界，未创建任务")}, func(c *Config) {
				c.Skills = []skills.Skill{sk}
			})
			agentBody := map[string]any{
				"skills": []string{skillIDOf(sk)}, "tools": selectedTools, "mcp_servers": []string{},
			}
			wantCreate := http.StatusBadRequest
			if grantBackground {
				agentBody["skill_tool_bindings"] = []string{"background-tasks"}
				wantCreate = http.StatusCreated
			}
			agent := postStudioAgent(t, h, studioAgentCapsBody("Background Skill", "ready", agentBody), wantCreate)
			if !grantBackground {
				return
			}
			sid := startStudioConversation(t, h, agent.ID)
			s := h.session(sid)
			// The first ordinary request initializes the default-model watermark.
			// Seed that unrelated bookkeeping before measuring Skill effects.
			if _, err := s.SyncModelConfig(); err != nil {
				t.Fatal(err)
			}
			s.SetPolicy(ModeStrict, 0)
			policyBefore := s.Policy().State()
			cwdBefore, ownerBefore := s.cwd, sessionResultOwnerKey(s.owner)
			hubBefore, browserBefore, loopBefore := s.hub, s.browser, s.agent
			bindingBefore, _ := h.mgr.studio.binding(sid)
			definitionBefore, _ := h.mgr.studio.get(agent.ID)
			nativeBefore := nativeCatalogHTTP(t, h, sid)
			historyBefore, err := os.ReadFile(s.Path())
			if err != nil {
				t.Fatal(err)
			}

			var catalog struct {
				Skills []capabilitySkill `json:"skills"`
			}
			if err := decodeJSONBody(h.do(http.MethodGet, "/api/manage/capabilities", ""), &catalog); err != nil {
				t.Fatal(err)
			}
			matches := 0
			for _, row := range catalog.Skills {
				if row.ID != skillIDOf(sk) {
					continue
				}
				matches++
				if row.Name != sk.Name || !row.Builtin || row.ToolCount != 0 || !slices.Contains(row.Aliases, "后台任务") || !slices.Contains(row.Aliases, "哨兵") {
					t.Fatalf("builtin catalog lost canonical identity, aliases or instruction-only scope: %+v", row)
				}
			}
			if matches != 1 {
				t.Fatalf("canonical background Skill catalog rows=%d", matches)
			}

			studioAsk(t, h, sid, "/skill:后台任务 说明怎样在后台执行，不要创建任务")
			stream := h.stream(sid, "")
			defer stream.close()
			stream.next(t)
			parts := []MessageInputPart{{Type: "text", Text: "先按 "}, {Type: "skill", Name: "哨兵"}, {Type: "text", Text: " 说明缺少哪些工具，不要创建任务。"}}
			body, _ := json.Marshal(map[string]any{"parts": parts, "input_layout": "inline"})
			h.post("/api/sessions/"+sid+"/messages", string(body), http.StatusAccepted)
			user := stream.wait(t, EvUserMessage)
			stream.wait(t, EvRunEnd)
			waitFor(t, 5*time.Second, func() bool { return !s.Active() })
			if len(user.Content) != 3 || user.Content[0].Text != "先按 " || user.Content[1].InputSkill != sk.Name || !strings.Contains(user.Content[1].Text, `<skill name="background-tasks"`) || user.Content[2].Text != " 说明缺少哪些工具，不要创建任务。" || user.InputLayout != "inline" {
				t.Fatalf("localized mixed Skill input lost order or canonical identity: %+v", user)
			}
			_, _, histories, _ := captured.snapshot()
			if len(histories) != 2 || !strings.Contains(studioMessageText(histories[0][0]), `<skill name="background-tasks"`) {
				t.Fatal("explicit slash input did not load the embedded Skill into model input")
			}
			modelInput := histories[1][len(histories[1])-1]
			if !reflect.DeepEqual(modelInput.Content, user.Content) {
				t.Fatal("persisted/displayed mixed Skill input differs from model order")
			}
			for _, names := range captured.toolSchemas() {
				if slices.Contains(names, tools.BackgroundTaskName) != grantBackground {
					t.Fatalf("Skill changed independently configured background permission: %v", names)
				}
				for _, withheld := range []string{tools.WatchName, tools.WatchTestName, "write", "edit", "bash"} {
					if slices.Contains(names, withheld) {
						t.Fatalf("instruction Skill granted withheld native tool %q", withheld)
					}
				}
			}
			if !slices.Equal(nativeBefore, nativeCatalogHTTP(t, h, sid)) {
				t.Fatal("explicit Skill invocation changed native tool selection")
			}
			bindingAfter, _ := h.mgr.studio.binding(sid)
			definitionAfter, _ := h.mgr.studio.get(agent.ID)
			if !reflect.DeepEqual(bindingBefore, bindingAfter) || !reflect.DeepEqual(definitionBefore, definitionAfter) {
				t.Fatal("explicit Skill invocation changed the Agent definition or original session binding")
			}
			if h.session(sid) != s || s.cwd != cwdBefore || sessionResultOwnerKey(s.owner) != ownerBefore || s.Policy().State() != policyBefore || s.hub != hubBefore || s.browser != browserBefore || s.agent != loopBefore {
				t.Fatal("Skill invocation replaced session state or changed its owner/workspace/approval policy")
			}
			historyAfter, err := os.ReadFile(s.Path())
			if err != nil || !bytes.HasPrefix(historyAfter, historyBefore) {
				t.Fatal("Skill invocation rewrote existing transcript records", err)
			}
		})
	}
}

func TestBackgroundSkillExplicitInputCannotSelectAnUnboundSkill(t *testing.T) {
	sk := backgroundBuiltinForInvocationTest(t)
	h, captured := newStudioHarness(t, []llm.Response{textTurn("must not run")}, func(c *Config) { c.Skills = []skills.Skill{sk} })
	a := postStudioAgent(t, h, studioAgentCapsBody("No background Skill", "ready", map[string]any{
		"skills": []string{}, "tools": []string{"read", tools.BackgroundTaskName}, "mcp_servers": []string{},
	}), http.StatusCreated)
	sid := startStudioConversation(t, h, a.ID)
	before, _ := os.ReadFile(h.session(sid).Path())
	body, _ := json.Marshal(map[string]any{"parts": []MessageInputPart{{Type: "skill", Name: "后台任务"}}, "input_layout": "inline"})
	h.post("/api/sessions/"+sid+"/messages", string(body), http.StatusBadRequest)
	after, _ := os.ReadFile(h.session(sid).Path())
	_, _, histories, _ := captured.snapshot()
	if len(histories) != 0 || !bytes.Equal(before, after) || len(sessionSkillNamesHTTP(t, h, sid)) != 0 {
		t.Fatal("explicit Skill card expanded the Agent's selected catalog or started a model run")
	}
}
