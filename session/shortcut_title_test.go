package session

import (
	"testing"

	"github.com/yosukeno/pogo/llm"
)

func TestShortcutTitleNeverCopiesPrivateBodyWithOrWithoutLayout(t *testing.T) {
	for _, layout := range []string{"", "top", "inline"} {
		message := llm.Message{Role: llm.RoleUser, InputLayout: layout, Content: []llm.Block{{Type: llm.BlockText, Text: "参考 "}, {Type: llm.BlockText, Text: "[资料：数据库]\npassword=synthetic", InputShortcut: &llm.InputShortcutSnapshot{ID: "db", Title: "数据库", Text: "password=synthetic"}}, {Type: llm.BlockText, Text: " 处理"}}}
		if UserInputTitle(message) != "参考 〔数据库〕 处理" {
			t.Fatal("private snapshot body reached the session title")
		}
	}
}
