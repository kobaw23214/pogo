package web

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

// The export → import round trip: what renderSessionMarkdown wrote,
// parseSessionMarkdown must read back — roles, prose, thinking, and tool
// call/result pairing. Prose that itself contains Markdown headings and code
// fences is the case that breaks a naive parser, so it is the fixture.
func TestSessionMarkdownRoundTrip(t *testing.T) {
	store, err := session.Create(t.TempDir(), "/wd", "k3")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []llm.Message{
		llm.UserText("帮我看下这段代码 ```go\nfmt.Println(1)\n``` 有没有问题"),
		{
			Role: llm.RoleAssistant,
			Content: []llm.Block{
				{Type: llm.BlockText, Text: "我先跑一下看看输出。"},
				{Type: llm.BlockThinking, Text: "用户想知道输出是什么"},
				{Type: llm.BlockToolUse, ID: "tu-1", Name: "bash", Input: json.RawMessage(`{"command":"go run main.go"}`)},
			},
		},
		{
			Role:    llm.RoleUser,
			Content: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "tu-1", Text: "1\n"}},
		},
		{
			Role: llm.RoleAssistant,
			Content: []llm.Block{
				{Type: llm.BlockText, Text: "输出是 1。补充说明：\n\n## 这是一个回复里的小标题\n\n- 第一项\n- 第二项"},
			},
		},
	}
	if err := store.AppendAll(msgs); err != nil {
		t.Fatal(err)
	}

	doc := renderSessionMarkdown(store, "sid-test", "测试会话", "k3", "/wd", false)
	title, model, cwd, turns, err := parseSessionMarkdown(doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if title != "测试会话" || model != "k3" || cwd != "/wd" {
		t.Errorf("envelope = %q/%q/%q", title, model, cwd)
	}
	if len(turns) != 4 {
		t.Fatalf("turns = %d, want 4", len(turns))
	}

	if turns[0].msg.Role != llm.RoleUser || turns[0].msg.Content[0].Type != llm.BlockText ||
		!strings.Contains(turns[0].msg.Content[0].Text, "fmt.Println(1)") {
		t.Errorf("user turn lost its fenced code: %+v", turns[0].msg.Content)
	}

	assistant := turns[1].msg
	if assistant.Role != llm.RoleAssistant {
		t.Fatalf("turn 1 role = %q", assistant.Role)
	}
	var thinking, call *llm.Block
	for i := range assistant.Content {
		switch assistant.Content[i].Type {
		case llm.BlockThinking:
			thinking = &assistant.Content[i]
		case llm.BlockToolUse:
			call = &assistant.Content[i]
		}
	}
	if thinking == nil || thinking.Text != "用户想知道输出是什么" {
		t.Errorf("thinking block did not come back: %+v", assistant.Content)
	}
	if call == nil || call.Name != "bash" || !json.Valid(call.Input) {
		t.Fatalf("tool call did not come back: %+v", assistant.Content)
	}

	results := turns[2].msg
	if results.Role != llm.RoleUser || len(results.Content) != 1 ||
		results.Content[0].Type != llm.BlockToolResult {
		t.Fatalf("results turn = %+v", results)
	}
	if results.Content[0].ToolUseID != call.ID {
		t.Errorf("result paired with %q, call has %q", results.Content[0].ToolUseID, call.ID)
	}
	if strings.TrimSpace(results.Content[0].Text) != "1" {
		t.Errorf("result text = %q", results.Content[0].Text)
	}

	last := turns[3].msg
	if last.Role != llm.RoleAssistant || !strings.Contains(last.Content[0].Text, "## 这是一个回复里的小标题") {
		t.Errorf("assistant prose lost its inner heading: %+v", last.Content)
	}
}

// A document that never came from export.md must be refused, politely.
func TestParseSessionMarkdownRejectsForeignDocuments(t *testing.T) {
	for name, doc := range map[string]string{
		"empty":       "",
		"no table":    "# 随手记的笔记\n\n一些内容\n",
		"no heading":  "| 项目 | 值 |\n|---|---|\n",
		"table alone": "# 标题\n\n| 项目 | 值 |\n|---|---|\n| 模型 | k3 |\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, turns, err := parseSessionMarkdown(doc)
			if name == "table alone" {
				// Parses, but carries no conversation — the handler refuses it.
				if err != nil || len(turns) != 0 {
					t.Errorf("turns = %d, err = %v", len(turns), err)
				}
				return
			}
			if err == nil {
				t.Errorf("accepted %q as a session export", name)
			}
		})
	}
}

// The whole sidebar loop at HTTP level: a session exported as Markdown and
// imported back through {"markdown", "workspace"} lands as a NEW session in
// the folder the import named, and the original keeps its own cwd.
func TestSessionImportMarkdownHTTP(t *testing.T) {
	h := newHarness(t, idleRespond)
	sid := writeDirectSession(t, h.mgr.sessionDir(), "markdown import me")

	res := h.do(http.MethodGet, "/api/sessions/"+sid+"/export.md", "")
	doc, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(h.mgr.Cwd(), "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"markdown": string(doc), "workspace": "proj"})
	var imp struct {
		Imported   int `json:"imported"`
		SessionIDs []struct {
			ID string `json:"id"`
		} `json:"session_ids"`
	}
	if err := decodeJSONBody(h.do(http.MethodPost, "/api/sessions/import", string(payload)), &imp); err != nil {
		t.Fatal(err)
	}
	if imp.Imported != 1 || len(imp.SessionIDs) != 1 {
		t.Fatalf("import result: %+v", imp)
	}
	if imp.SessionIDs[0].ID == sid {
		t.Fatal("import must write a NEW session, not reuse the id")
	}

	var list struct {
		Sessions []struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		} `json:"sessions"`
	}
	if err := decodeJSONBody(h.do(http.MethodGet, "/api/sessions", ""), &list); err != nil {
		t.Fatal(err)
	}
	found, original := "", false
	for _, s := range list.Sessions {
		if s.ID == imp.SessionIDs[0].ID {
			found = s.Cwd
		}
		if s.ID == sid {
			original = true
		}
	}
	if want := filepath.Join(h.mgr.Cwd(), "proj"); found != want {
		t.Fatalf("imported session cwd = %q, want %q", found, want)
	}
	if !original {
		t.Fatal("the original session survived the import — it must stay listed")
	}

	// A document that is not an export is refused with an explanation.
	bad, _ := json.Marshal(map[string]string{"markdown": "# 随手笔记\n\nhello\n"})
	res = h.do(http.MethodPost, "/api/sessions/import", string(bad))
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("garbage markdown accepted: %d", res.StatusCode)
	}
}
