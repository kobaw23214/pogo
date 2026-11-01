package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
)

func TestSelectedCredentialIsLiteralAndScoped(t *testing.T) {
	file := filepath.Join(t.TempDir(), "private.env")
	contents := "UNRELATED=do-not-load\nexport MODEL_TEST_KEY='literal-$(never-execute)-value'\n"
	if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	value, err := selectedCredential(file, "MODEL_TEST_KEY")
	if err != nil || value != "literal-$(never-execute)-value" {
		t.Fatalf("literal selected credential was not preserved: %v", err)
	}
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = selectedCredential(file, "MODEL_TEST_KEY"); err == nil {
		t.Fatal("accepted a non-private credential file")
	}
}

func TestSelectedCredentialRefusesDuplicateWithoutEchoingValue(t *testing.T) {
	file := filepath.Join(t.TempDir(), "private.env")
	if err := os.WriteFile(file, []byte("MODEL_TEST_KEY=secret-one\nMODEL_TEST_KEY=secret-two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := selectedCredential(file, "MODEL_TEST_KEY")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("duplicate credential should be rejected without its values")
	}
}

func TestFreshWorkspaceCannotOverwriteExistingEdits(t *testing.T) {
	fixtures, data := t.TempDir(), filepath.Join(t.TempDir(), "run")
	for _, name := range []string{"pogo-office-word.docx", "pogo-office-excel.xlsx"} {
		if err := os.WriteFile(filepath.Join(fixtures, name), []byte("artificial fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := `{"synthetic":true,"files":{"word":{"name":"pogo-office-word.docx"},"excel":{"name":"pogo-office-excel.xlsx"}}}`
	if err := os.WriteFile(filepath.Join(fixtures, "manifest.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	workspace, err := copyFixtures(options{data: data, fixtures: fixtures})
	if err != nil {
		t.Fatal(err)
	}
	word := filepath.Join(workspace, "pogo-office-word.docx")
	if err = os.WriteFile(word, []byte("unsaved test export"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = copyFixtures(options{data: data, fixtures: fixtures}); err == nil {
		t.Fatal("a prior workspace was reused")
	}
	after, _ := os.ReadFile(word)
	if string(after) != "unsaved test export" {
		t.Fatal("existing edits were overwritten")
	}
}

func TestUserPromptHashInputSkipsToolResults(t *testing.T) {
	history := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockText, Text: "用户保存要求"}}}, {Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: "tool_001", Name: "docs_office"}}}, {Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockToolResult, Text: "not a new user prompt"}}}}
	if got := lastUserText(history); got != "用户保存要求" {
		t.Fatalf("last user body = %q", got)
	}
}

func TestProvenanceContainsOnlyExplicitMetadata(t *testing.T) {
	directory := t.TempDir()
	recorder := &callRecorder{provenancePath: filepath.Join(directory, "model-provenance.json"), model: "configured-model", provider: "configured-provider", outputCap: 8192, calls: []map[string]any{{"responseSha256": strings.Repeat("a", 64), "toolUseIds": []string{"actual-tool-id"}, "completed": true}}}
	if err := recorder.writeProvenance(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(recorder.provenancePath)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if value["kind"] != "real-provider-streams" || value["synthetic"] != false || value["syntheticDocuments"] != true {
		t.Fatal("provenance confused model and document identities")
	}
	for _, name := range []string{"apiKey", "token", "headers", "baseURL", "config", "response"} {
		if _, ok := value[name]; ok {
			t.Fatalf("provenance exposes %s", name)
		}
	}
	info, _ := os.Stat(recorder.provenancePath)
	if info.Mode().Perm() != 0600 {
		t.Fatal("provenance permissions are not private")
	}
}
