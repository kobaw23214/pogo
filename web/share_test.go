package web

// Shared answers: creation resolves the question's timeline id to the exchange
// it opens, and the view is public by unguessable id. The security postures
// are pinned here rather than left to review: public GET without any
// credential, creation still gated.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

// writeShareableSession builds a transcript with the shape sharing cares
// about: two genuine questions, a multi-turn first answer whose tool-result
// carrier and mid-exchange harness notice would fool a naive walk, and a
// second exchange to prove the span closes where the next question starts.
func writeShareableSession(t *testing.T, dir string) string {
	t.Helper()
	store, err := session.Create(dir, t.TempDir(), "k3")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []llm.Message{
		llm.UserText("summarize the sample library"),
		{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockText, Text: "Let me look."},
			{Type: llm.BlockToolUse, ID: "c1", Name: "mal_stats", Input: nil},
		}},
		{Role: llm.RoleUser, Content: []llm.Block{
			{Type: llm.BlockToolResult, ToolUseID: "c1", Text: "12 families"},
		}},
		// A harness nudge landing mid-exchange: visible, so it counts toward
		// ordinals, but it must not close the span nor enter the snapshot.
		llm.UserText("[pogo] Context window is filling up; wrap up soon."),
		{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockText, Text: "The library holds **12 families**."},
		}},
		llm.UserText("and the second question"),
		{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockText, Text: "Second answer."},
		}},
	}
	if err := store.AppendAll(msgs); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(filepath.Base(store.Path()), ".jsonl")
}

// questionID resolves a question's text to its timeline id through the same
// snapshot the browser builds its timeline from, so the test posts exactly
// what the UI would.
func questionID(t *testing.T, h *harness, sid, text string) string {
	t.Helper()
	var env struct {
		Snapshot Snapshot `json:"snapshot"`
	}
	if err := decodeJSONBody(h.do(http.MethodGet, "/api/sessions/"+sid, ""), &env); err != nil {
		t.Fatal(err)
	}
	for _, m := range env.Snapshot.Messages {
		if m.Role == string(llm.RoleUser) && strings.Contains(m.Content[0].Text, text) {
			return m.ID
		}
	}
	t.Fatalf("question %q not on the timeline", text)
	return ""
}

func TestShareCreateView(t *testing.T) {
	h := newHarness(t, idleRespond)
	sid := writeShareableSession(t, h.mgr.sessionDir())
	qid := questionID(t, h, sid, "summarize the sample library")

	// Create: returns the public path.
	var created struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := decodeJSONBody(h.do(http.MethodPost, "/api/sessions/"+sid+"/share",
		`{"message_id":"`+qid+`"}`), &created); err != nil {
		t.Fatal(err)
	}
	if len(created.ID) != 32 || created.URL != "/share/"+created.ID {
		t.Fatalf("create shape: %+v", created)
	}

	// The snapshot carries the question and every turn's answer text — not the
	// tool work, and not the harness notice that interrupted the exchange.
	var view shareEntry
	if err := decodeJSONBody(h.do(http.MethodGet, "/api/share/"+created.ID, ""), &view); err != nil {
		t.Fatal(err)
	}
	if view.Question != "summarize the sample library" {
		t.Fatalf("question = %q", view.Question)
	}
	want := "Let me look.\n\nThe library holds **12 families**."
	if view.Answer != want {
		t.Fatalf("answer = %q, want %q", view.Answer, want)
	}

	// The timeline projection rides along: question + turns + the notice, with
	// the tool-result carrier folded into the results map — the shape the
	// session view renders from, so the public page can show the work.
	if len(view.Messages) != 4 {
		t.Fatalf("messages = %d, want 4 (question, two turns, the notice)", len(view.Messages))
	}
	for _, b := range view.Messages[0].Content {
		if b.Type != llm.BlockText {
			t.Fatalf("question carried a %s block; attachments stay with the owner", b.Type)
		}
	}
	toolRes, ok := view.Results["c1"]
	if !ok || toolRes.Name != "mal_stats" || toolRes.Text != "12 families" {
		t.Fatalf("results[c1] = %+v, %v", toolRes, ok)
	}

	// Public by design: no credential at all still reads the snapshot — this
	// is the promise the share link makes.
	res := h.get("/api/share/" + created.ID)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("anonymous GET: %d", res.StatusCode)
	}
}

func TestShareCreateSecondExchange(t *testing.T) {
	h := newHarness(t, idleRespond)
	sid := writeShareableSession(t, h.mgr.sessionDir())
	qid := questionID(t, h, sid, "and the second question")

	var created struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := decodeJSONBody(h.do(http.MethodPost, "/api/sessions/"+sid+"/share",
		`{"message_id":"`+qid+`"}`), &created); err != nil {
		t.Fatal(err)
	}
	var view shareEntry
	if err := decodeJSONBody(h.do(http.MethodGet, "/api/share/"+created.ID, ""), &view); err != nil {
		t.Fatal(err)
	}
	if view.Question != "and the second question" || view.Answer != "Second answer." {
		t.Fatalf("second exchange = %q / %q", view.Question, view.Answer)
	}
}

func TestShareCreateRejectsUnknownMessage(t *testing.T) {
	h := newHarness(t, idleRespond)
	sid := writeShareableSession(t, h.mgr.sessionDir())

	res := h.do(http.MethodPost, "/api/sessions/"+sid+"/share", `{"message_id":"u99"}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown message: %d", res.StatusCode)
	}

	// Regression: a transcript record id is not a timeline id. The first share
	// implementation matched record ids, which made every click from the UI a
	// 404 — the two namespaces must stay unresolvable for each other.
	store, err := session.Open(h.mgr.transcriptPath(sid))
	if err != nil {
		t.Fatal(err)
	}
	var recordID string
	for _, rec := range store.BranchRecords("") {
		if rec.Message != nil && rec.Message.Role == llm.RoleAssistant {
			recordID = rec.ID
		}
	}
	res = h.do(http.MethodPost, "/api/sessions/"+sid+"/share", `{"message_id":"`+recordID+`"}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("record id as message id: %d, want 404", res.StatusCode)
	}
}

func TestShareStorePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shares.json")
	s1, err := newShareStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.put(shareEntry{ID: "abc", Answer: "x", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("store not written: %v", err)
	}
	s2, err := newShareStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := s2.get("abc"); !ok || e.Answer != "x" {
		t.Fatalf("reload lost the entry: %+v %v", e, ok)
	}
}
