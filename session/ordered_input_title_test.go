package session

import (
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
)

const orderedTitleElement = "<page-element source=\"中文页面\" url=\"https://fixture.invalid/\">\nobservation_ref: p7\nelement: button &quot;导出全部&quot;\nposition: 10% top 20% left 100×40\n</page-element>"

func TestOrderedUserInputTitleKeepsAuthoredOrderWithoutObservationMarkup(t *testing.T) {
	m := llm.Message{Role: llm.RoleUser, InputLayout: "inline", Content: []llm.Block{
		{Type: llm.BlockText, Text: "点击"}, {Type: llm.BlockText, Text: orderedTitleElement, InputElement: true},
		{Type: llm.BlockText, Text: "再处理"}, {Type: llm.BlockFile, FilePath: "uploads/folder/规则.xlsx", FileName: "规则.xlsx"},
		{Type: llm.BlockText, Text: "对照"}, {Type: llm.BlockImage, ImageRef: "uploads/图.png", ImageName: "图.png"},
	}}
	original := m.Text()
	if got, want := UserInputTitle(m), "点击〔button \"导出全部\"〕再处理〔规则.xlsx〕对照〔图.png〕"; got != want {
		t.Fatalf("title=%q want=%q", got, want)
	}
	if m.Text() != original || !strings.Contains(m.Text(), "<page-element") {
		t.Fatal("title derivation changed model/history body")
	}
	m.InputLayout = ""
	if got := UserInputTitle(m); got != original {
		t.Fatalf("old message title changed: %q", got)
	}
}

func TestOrderedUserInputTitleNamesElementAndAttachmentOnlyMessages(t *testing.T) {
	for _, tc := range []struct {
		name   string
		blocks []llm.Block
		want   string
	}{
		{"element", []llm.Block{{Type: llm.BlockText, Text: orderedTitleElement, InputElement: true}}, "〔button \"导出全部\"〕"},
		{"element_without_label", []llm.Block{{Type: llm.BlockText, Text: "<page-element source=\"x\" url=\"https://fixture.invalid\">\nobservation_ref: p1\n</page-element>", InputElement: true}}, "〔页面元素〕"},
		{"file", []llm.Block{{Type: llm.BlockFile, FilePath: "uploads/folder/data.csv"}}, "〔data.csv〕"},
		{"image", []llm.Block{{Type: llm.BlockImage, ImageRef: "uploads/folder/图.png"}}, "〔图.png〕"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := llm.Message{Role: llm.RoleUser, InputLayout: "inline", Content: tc.blocks}
			if got := UserInputTitle(m); got != tc.want {
				t.Fatalf("title=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestOrderedUserInputTitleMatchesDiskAndPreservesCustomTitle(t *testing.T) {
	dir := t.TempDir()
	store, err := Create(dir, "/work", "k3", "ordered-title")
	if err != nil {
		t.Fatal(err)
	}
	m := llm.Message{Role: llm.RoleUser, InputLayout: "inline", Content: []llm.Block{{Type: llm.BlockText, Text: "点击"}, {Type: llm.BlockText, Text: orderedTitleElement, InputElement: true}, {Type: llm.BlockText, Text: "再继续"}}}
	if err := store.Append(m); err != nil {
		t.Fatal(err)
	}
	infos, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Title != Title(UserInputTitle(m)) || strings.Contains(infos[0].Title, "page-element") {
		t.Fatalf("disk title leaked observation: %+v", infos)
	}
	custom := "我自己的会话标题"
	if err := store.AppendMeta(&Meta{Title: &custom}); err != nil {
		t.Fatal(err)
	}
	infos, err = List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if infos[0].Title != custom {
		t.Fatalf("custom title overwritten: %q", infos[0].Title)
	}
}
