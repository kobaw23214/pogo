package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/inputref"
)

func TestMCPInputDisplayMetadataNeverReachesProviderOrTokenAccounting(t *testing.T) {
	message := Message{Role: RoleUser, Content: []Block{{Type: BlockText, Text: "ONE_HOST_METHOD_GUIDE", InputMCP: &inputref.MCPReference{Server: "LOCAL_SERVER", Name: "LOCAL_METHOD"}}}}
	wire, err := json.Marshal(toWireMessages("", []Message{message}))
	if err != nil || strings.Contains(string(wire), "LOCAL_") || strings.Contains(string(wire), "input_mcp") || strings.Count(string(wire), "ONE_HOST_METHOD_GUIDE") != 1 {
		t.Fatalf("reference metadata duplicated/leaked into provider input: %s %v", wire, err)
	}
	plain := message
	plain.Content = []Block{{Type: BlockText, Text: message.Content[0].Text}}
	if EstimateTokens([]Message{message}) != EstimateTokens([]Message{plain}) {
		t.Fatal("reference metadata changed provider token accounting")
	}
	raw, err := json.Marshal(message)
	var restored Message
	if err != nil || json.Unmarshal(raw, &restored) != nil || restored.Content[0].InputMCP == nil || *restored.Content[0].InputMCP != *message.Content[0].InputMCP {
		t.Fatal("editable method identity did not persist")
	}
}
