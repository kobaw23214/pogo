package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

func TestImportedResultGrantsDiscardLocalIDsAndNestedSourceLinks(t *testing.T) {
	const displayText = "An imported conversation retains its visible answer."
	const id = "Raaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	record := session.Record{Message: &llm.Message{Role: llm.RoleUser, Content: []llm.Block{{
		Type: llm.BlockToolResult, ToolUseID: "local-read", ResultID: id, Text: displayText,
		Details: json.RawMessage(`{"RESULT_ID":"` + id + `","FULL_OUTPUT_PATH":"/old/private.log","view":"text","offset":42}`),
		Source:  json.RawMessage(`{"label":"S3","title":"original page","type":"web_page","locator":"https://example.test/article","FULL_PATH":"/old/raw.log","SAVED_RESULT":{"result_id":"` + id + `","origin":{"label":"S1","full_path":"/old/parent.log"},"query":{"result_id":"` + id + `"}}}`),
	}}}}
	stripImportedResultGrants(&record)
	block := record.Message.Content[0]
	if block.ResultID != "" || block.Text != displayText {
		t.Fatalf("import changed display or kept local ID: %+v", block)
	}
	for label, raw := range map[string]json.RawMessage{"details": block.Details, "source": block.Source} {
		if !json.Valid(raw) || strings.Contains(string(raw), id) || strings.Contains(string(raw), "/old/") {
			t.Errorf("%s retains grants or invalid JSON: %s", label, raw)
		}
	}
	var details map[string]any
	var source map[string]any
	if json.Unmarshal(block.Details, &details) != nil || details["view"] != "text" || details["offset"] != float64(42) {
		t.Fatalf("non-grant details lost: %s", block.Details)
	}
	if json.Unmarshal(block.Source, &source) != nil || source["label"] != "S3" || source["locator"] != "https://example.test/article" {
		t.Fatalf("display source lost: %s", block.Source)
	}
}

func TestImportedCompactionSourceStateIsDiscarded(t *testing.T) {
	const checkpoint = `{"version":1,"seq":7,"sources":[{"label":"S7","full_path":"/old/private.log"}],"bindings":[{"result_id":"Raaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state":"source","source_label":"S7"}],"reserved":["S9"]}`
	for _, withText := range []bool{false, true} {
		t.Run(map[bool]string{false: "metadata-only", true: "continuation"}[withText], func(t *testing.T) {
			message := llm.Message{Role: llm.RoleUser, SourceState: json.RawMessage(checkpoint)}
			if withText {
				message.Content = llm.UserText("Visible continuation and citation [1](#cite:S7).").Content
			}
			record := session.Record{Message: &message}
			before := record.Message.Text()
			stripImportedResultGrants(&record)
			if len(record.Message.SourceState) != 0 || record.Message.Text() != before {
				t.Fatal("import retained a host checkpoint or changed visible continuation")
			}
			raw, err := json.Marshal(record)
			if err != nil || strings.Contains(string(raw), "source_state") || strings.Contains(string(raw), "/old/private.log") {
				t.Fatal("imported JSON retained hidden provenance")
			}
		})
	}
	stripImportedResultGrants(&session.Record{})
}
