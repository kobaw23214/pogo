// The live title overlay must derive the same line a later disk read will:
// a send that was only files names the sidebar row for the whole window
// before the transcript append — the describe step can hold that window
// open for tens of seconds.
package web

import (
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

func TestFirstUserTitleFallsBackToAttachments(t *testing.T) {
	h := NewHub()
	h.Seed([]session.Timed{
		{Message: llm.Message{Role: llm.RoleUser, Content: []llm.Block{
			{Type: llm.BlockFile, FilePath: "uploads/bench/a.csv", FileName: "a.csv"},
			{Type: llm.BlockFile, FilePath: "uploads/bench/b.csv", FileName: "b.csv"},
		}}},
	})
	if got := h.FirstUserTitle(); got != "bench" {
		t.Errorf("title = %q, want the folder root", got)
	}

	// A second send's text does not take over: the first send named it,
	// matching what summarise derives from disk.
	h2 := NewHub()
	h2.Seed([]session.Timed{
		{Message: llm.Message{Role: llm.RoleUser, Content: []llm.Block{
			{Type: llm.BlockImage, ImageRef: "uploads/shot.png", ImageName: "shot.png"},
		}}},
		{Message: llm.UserText("这是什么")},
	})
	if got := h2.FirstUserTitle(); got != "shot.png" {
		t.Errorf("title = %q, want the first send's name to stand", got)
	}

	h3 := NewHub()
	if got := h3.FirstUserTitle(); got != "" {
		t.Errorf("empty hub has no title: %q", got)
	}
}

func TestOrderedFirstUserTitleMatchesPersistedDerivation(t *testing.T) {
	observation := "<page-element source=\"页面\" url=\"https://fixture.invalid/\">\nelement: button &quot;导出&quot;\n</page-element>"
	m := llm.Message{Role: llm.RoleUser, InputLayout: "inline", Content: []llm.Block{{Type: llm.BlockText, Text: "点击"}, {Type: llm.BlockText, Text: observation, InputElement: true}, {Type: llm.BlockText, Text: "再读取"}, {Type: llm.BlockFile, FilePath: "uploads/规则.xlsx", FileName: "规则.xlsx"}}}
	live := NewHub()
	live.Publish(Event{Type: EvUserMessage, Content: m.Content, InputLayout: m.InputLayout})
	reopened := NewHub()
	reopened.Seed([]session.Timed{{Message: m}})
	want := session.UserInputTitle(m)
	if live.FirstUserTitle() != want || reopened.FirstUserTitle() != want {
		t.Fatalf("live=%q reopened=%q want=%q", live.FirstUserTitle(), reopened.FirstUserTitle(), want)
	}
	legacy := NewHub()
	m.InputLayout = ""
	legacy.Seed([]session.Timed{{Message: m}})
	if legacy.FirstUserTitle() != m.Text() {
		t.Fatal("legacy title was changed")
	}
}
