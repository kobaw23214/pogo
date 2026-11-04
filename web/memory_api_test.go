package web

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/memory"
)

func TestMemoryAPIReadEditDeleteWithCredentialGuard(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("done")))
	s := h.session(h.createSession())
	s.memoryDir = filepath.Join(realTempDir(t), "agent-memory")
	if _, err := memory.SaveNote(s.memoryDir, memory.Note{Path: "preferences.md", Type: memory.NoteFeedback, Content: "Use pnpm.", SourceSession: s.ID, Origin: "auto-distill"}, true); err != nil {
		t.Fatal(err)
	}
	response := h.do(http.MethodGet, "/api/sessions/"+s.ID+"/memory", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", response.StatusCode)
	}
	var catalog struct {
		Scope string        `json:"scope"`
		Notes []memory.Note `json:"notes"`
	}
	if err := json.NewDecoder(response.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if catalog.Scope != "agent" || len(catalog.Notes) != 1 || catalog.Notes[0].Type != memory.NoteFeedback {
		t.Fatalf("catalog = %+v", catalog)
	}

	response = h.do(http.MethodPut, "/api/sessions/"+s.ID+"/memory", `{"path":"preferences.md","type":"user","content":"Use npm and always run tests."}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("save status = %d", response.StatusCode)
	}
	response.Body.Close()
	note, err := memory.ReadNote(s.memoryDir, "preferences.md")
	if err != nil {
		t.Fatal(err)
	}
	if note.Type != memory.NoteUser || note.Origin != "manual" || note.SourceSession != s.ID || note.Content != "Use npm and always run tests.\n" {
		t.Fatalf("edited note = %+v", note)
	}
	response = h.do(http.MethodPut, "/api/sessions/"+s.ID+"/memory", `{"path":"secret.md","type":"project","content":"api_key = \"abcdefghijklmnop\""}`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("credential status = %d", response.StatusCode)
	}
	response.Body.Close()

	response = h.do(http.MethodDelete, "/api/sessions/"+s.ID+"/memory?path=preferences.md", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d", response.StatusCode)
	}
	response.Body.Close()
	if _, err := memory.ReadNote(s.memoryDir, "preferences.md"); err == nil {
		t.Fatal("deleted note is still readable")
	}
	if !strings.Contains(s.memoryDir, "agent-memory") {
		t.Fatalf("unexpected test root %q", s.memoryDir)
	}
}
