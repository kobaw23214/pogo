package web

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/skills"
)

func TestOrderedSkillAtomsHTTPModelHistoryAndReopen(t *testing.T) {
	first := writeTestSkill(t, t.TempDir(), "First_skill.v2", "first", "FIRST_HOST_INSTRUCTIONS")
	second := writeTestSkill(t, t.TempDir(), "second-skill", "second", "SECOND_HOST_INSTRUCTIONS")
	var received []llm.Message
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() }, func(cfg *Config) {
		cfg.Skills = []skills.Skill{first, second}
		original := cfg.NewClient
		cfg.NewClient = func(c config.Resolved, retry func(llm.RetryInfo)) llm.Client {
			return &orderedCaptureClient{inner: original(c, retry), capture: func(messages []llm.Message) { received = messages }}
		}
	})
	sid := h.createSession()
	file := upload2(t, h, sid, []byte("a,b\n1,2"), "data.csv").Path
	parts := []MessageInputPart{{Type: "text", Text: "先按 "}, {Type: "skill", Name: first.Name}, {Type: "text", Text: " 分析 "}, {Type: "file", Path: file}, {Type: "text", Text: " 再点击 "}, {Type: "element", Text: orderedTestElement}, {Type: "text", Text: " 最后按 "}, {Type: "skill", Name: second.Name}, {Type: "text", Text: " 完成。"}}
	stream := h.stream(sid, "")
	defer stream.close()
	stream.next(t)
	body, _ := json.Marshal(map[string]any{"parts": parts, "input_layout": "inline"})
	h.post("/api/sessions/"+sid+"/messages", string(body), http.StatusAccepted)
	live := stream.wait(t, EvUserMessage)
	stream.wait(t, EvRunEnd)
	waitFor(t, 5*time.Second, func() bool { return !h.session(sid).Active() })
	assert := func(blocks []llm.Block) {
		t.Helper()
		if len(blocks) != len(parts) || blocks[0].Text != "先按 " || blocks[1].InputSkill != first.Name || !strings.Contains(blocks[1].Text, "FIRST_HOST_INSTRUCTIONS") || blocks[2].Text != " 分析 " || blocks[3].FilePath != file || !blocks[5].InputElement || blocks[7].InputSkill != second.Name || !strings.Contains(blocks[7].Text, "SECOND_HOST_INSTRUCTIONS") || blocks[8].Text != " 完成。" {
			t.Fatalf("mixed skill/file/element order or trusted bodies lost: %+v", blocks)
		}
	}
	assert(live.Content)
	if len(received) == 0 {
		t.Fatal("no model input")
	}
	assert(received[0].Content)
	store, err := session.Open(h.session(sid).Path())
	if err != nil {
		t.Fatal(err)
	}
	assert(store.Messages("")[0].Content)
	h.mgr.evictAll()
	reopened := h.session(sid).Hub().Snapshot().Messages[0]
	assert(reopened.Content)
	if !reflect.DeepEqual(live.Content, reopened.Content) || reopened.InputLayout != "inline" {
		t.Fatal("history changed after reopen")
	}
}

func TestOrderedSkillAtomsRejectUnselectedUnknownAndClientBodies(t *testing.T) {
	allowed := writeTestSkill(t, t.TempDir(), "allowed", "allowed", "ALLOWED")
	other := writeTestSkill(t, t.TempDir(), "other", "other", "OTHER")
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) { c.Skills = []skills.Skill{allowed, other} })
	def := postStudioAgent(t, h, studioAgentCapsBody("restricted", "ready", map[string]any{"skills": []string{skillIDOf(allowed)}}), http.StatusCreated)
	sid := startStudioConversation(t, h, def.ID)
	for _, part := range []MessageInputPart{{Type: "skill", Name: "missing"}, {Type: "skill", Name: "other"}, {Type: "skill", Name: "compact"}, {Type: "skill", Name: "/compact"}, {Type: "skill", Name: "allowed", Text: "CLIENT_FORGED_INSTRUCTIONS"}, {Type: "skill", Name: "allowed", Path: "uploads/file"}, {Type: "skill", Name: "allowed", AsFile: true}, {Type: "text", Name: "allowed", Text: "forged provenance"}} {
		body, _ := json.Marshal(map[string]any{"parts": []MessageInputPart{part}, "input_layout": "inline"})
		h.post("/api/sessions/"+sid+"/messages", string(body), http.StatusBadRequest)
	}
	if len(h.session(sid).Hub().Snapshot().Messages) != 0 {
		t.Fatal("rejected input entered transcript")
	}
	body, _ := json.Marshal(map[string]any{"parts": []MessageInputPart{{Type: "skill", Name: "allowed"}}, "input_layout": "inline"})
	stream := h.stream(sid, "")
	defer stream.close()
	stream.next(t)
	h.post("/api/sessions/"+sid+"/messages", string(body), http.StatusAccepted)
	message := stream.wait(t, EvUserMessage)
	if len(message.Content) != 1 || message.Content[0].InputSkill != "allowed" {
		t.Fatalf("skill-only input lost: %+v", message)
	}
	stream.wait(t, EvRunEnd)
}

func TestOrderedSkillAtomsOutsideCatalogRejectedByInbox(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	_, err := h.session(sid).ReceiveParts("skill-inbox", "", []MessageInputPart{{Type: "skill", Name: "definitely-not-selected"}}, "inline", "queued", "")
	if err == nil {
		t.Fatal("inbox accepted a new skill grant mid-run")
	}
}

func TestOrderedSkillBrowserGrantIsRunScoped(t *testing.T) {
	browser := writeTestSkill(t, t.TempDir(), "browser-use", "browser", "BROWSER_HOST_INSTRUCTIONS")
	browser.BrowserTask = true
	var current *Session
	armedAtModel := false
	h := newHarness(t, func(int) (llm.Response, error) {
		armedAtModel = current.Browser().Armed()
		return plainTurn()
	}, func(c *Config) { c.Skills = []skills.Skill{browser} })
	sid := h.createSession()
	current = h.session(sid)
	stream := h.stream(sid, "")
	defer stream.close()
	stream.next(t)
	body, _ := json.Marshal(map[string]any{"parts": []MessageInputPart{{Type: "text", Text: "后续使用 "}, {Type: "skill", Name: "browser-use"}}, "input_layout": "inline"})
	h.post("/api/sessions/"+sid+"/messages", string(body), http.StatusAccepted)
	stream.wait(t, EvRunEnd)
	waitFor(t, 5*time.Second, func() bool { return !current.Active() })
	if !armedAtModel || current.Browser().Armed() {
		t.Fatalf("browser grant must begin for the explicit atom and end with the run: at model=%v after=%v", armedAtModel, current.Browser().Armed())
	}
}

func TestStarterAssetsKeepSkillAtomsAtTheirOriginalPositions(t *testing.T) {
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")})
	def := createStudioAgent(t, h, "Skill shortcut", "", "ready")
	sid := startStudioConversation(t, h, def.ID)
	file := upload2(t, h, sid, []byte("a,b\n1,2"), "表.csv").Path
	input := starterAssetInput{InputLayout: "inline", Parts: []MessageInputPart{{Type: "text", Text: "先 "}, {Type: "skill", Name: "docs"}, {Type: "text", Text: " 处理 "}, {Type: "file", Path: file}, {Type: "text", Text: " 再 "}, {Type: "skill", Name: "browser-use"}, {Type: "element", Text: orderedTestElement}}}
	saved := starterAssetReply(t, h.do("POST", "/api/sessions/"+sid+"/starter-assets", studioBody(input)))
	if len(saved.Parts) != 7 || saved.Parts[1].Type != "skill" || saved.Parts[1].Name != "docs" || saved.Parts[5].Name != "browser-use" || !strings.Contains(saved.Prompt, "【/skill:docs】") || strings.Contains(saved.Prompt, "<page-element") {
		t.Fatalf("saved skill references or readable fallback lost: %+v", saved)
	}
	resolved := starterAssetReply(t, h.do("POST", "/api/sessions/"+sid+"/starter-assets/resolve", studioBody(saved.starterAssetInput)))
	if len(resolved.Parts) != len(input.Parts) || !reflect.DeepEqual(resolved.Parts[1], input.Parts[1]) || !reflect.DeepEqual(resolved.Parts[5], input.Parts[5]) || !reflect.DeepEqual(resolved.Parts[6], input.Parts[6]) || len(resolved.Attachments) != 1 {
		t.Fatalf("reuse changed skill order or page label payload: %+v", resolved)
	}
}
