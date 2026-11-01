package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/inputref"
)

func TestShortcutDisplayMetadataNeverReachesProviderOrTokenAccounting(t *testing.T) {
	block := Block{Type: BlockText, Text: "[资料：连接]\nhost=example.invalid", InputShortcut: &InputShortcutSnapshot{ID: "LOCAL_ID_MUST_NOT_LEAK", Title: "连接", Text: "LOCAL_RAW_SNAPSHOT_MUST_NOT_LEAK", Icon: "LOCAL_ICON_MUST_NOT_LEAK", Language: "LOCAL_LANGUAGE_MUST_NOT_LEAK"}}
	message := Message{Role: RoleUser, Content: []Block{{Type: BlockText, Text: "BEFORE "}, block, {Type: BlockText, Text: " AFTER"}}}
	wire, _ := json.Marshal(toWireMessages("", []Message{message}))
	if strings.Contains(string(wire), "input_shortcut") || strings.Contains(string(wire), "LOCAL_") || !strings.Contains(string(wire), "[资料：连接]\\nhost=example.invalid") {
		t.Fatal("display-only shortcut metadata leaked or model body disappeared")
	}
	without := message
	without.Content = append([]Block(nil), message.Content...)
	without.Content[1].InputShortcut = nil
	if EstimateTokens([]Message{message}) != EstimateTokens([]Message{without}) {
		t.Fatal("duplicated display snapshot affects model token accounting")
	}
	raw, _ := json.Marshal(message)
	var restored Message
	if json.Unmarshal(raw, &restored) != nil || restored.Content[1].InputShortcut == nil || restored.Content[1].InputShortcut.Text != block.InputShortcut.Text || restored.Content[1].InputShortcut.Language != block.InputShortcut.Language {
		t.Fatal("exact editable raw snapshot not persisted")
	}
}

func TestNestedShortcutAndOfficeMetadataNotDuplicatedInProviderInput(t *testing.T) {
	message := Message{Role: RoleUser, Content: []Block{
		{Type: BlockText, Text: "[资料：标题]\n", InputShortcut: &InputShortcutSnapshot{ID: "LOCAL_ID", Title: "标题", Text: "LOCAL_FALLBACK", Parts: []inputref.Part{{Type: "text", Text: "LOCAL_DISPLAY_COPY"}}, BlockCount: 1}},
		{Type: BlockText, Text: "ONE_REAL_BODY", InputOffice: &inputref.OfficeSnapshot{Path: "uploads/a.docx", Title: "LOCAL_OFFICE_TITLE", Text: "LOCAL_OFFICE_COPY", Editor: "word", CanvasVersion: "LOCAL_VERSION"}},
	}}
	wire, _ := json.Marshal(toWireMessages("", []Message{message}))
	if strings.Contains(string(wire), "LOCAL_") || strings.Contains(string(wire), "input_office") || strings.Contains(string(wire), "block_count") || strings.Count(string(wire), "ONE_REAL_BODY") != 1 {
		t.Fatal("frozen metadata leaked or children duplicated in provider request")
	}
	plain := message
	plain.Content = append([]Block(nil), message.Content...)
	plain.Content[0].InputShortcut = nil
	plain.Content[1].InputOffice = nil
	if EstimateTokens([]Message{plain}) != EstimateTokens([]Message{message}) {
		t.Fatal("display-only nested metadata affected token usage")
	}
}
