package web

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

// writeTranscript files one session with a single user message under dir and
// returns its id. Used to stand in for a delegated run's transcript, which a
// child process writes under the session dir's subagents/ subdirectory.
func writeTranscript(t *testing.T, dir, text string) string {
	t.Helper()
	store, err := session.Create(dir, "/wd", "k3")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(llm.UserText(text)); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(filepath.Base(store.Path()), ".jsonl")
}

// A delegated run's transcript is served by the transcript endpoint but never
// listed: it is a byproduct of the parent's task, not a session anyone
// resumes. See session.SubagentDirName.
func TestTranscriptServesSubagentFileHiddenFromList(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("ok")))
	dir := h.mgr.cfg.SessionDir
	id := writeTranscript(t, filepath.Join(dir, session.SubagentDirName), "child task")

	resp := h.do(http.MethodGet, "/api/sessions/"+id+"/transcript", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET transcript: %d", resp.StatusCode)
	}
	var out struct {
		Snapshot struct {
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		} `json:"snapshot"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Snapshot.Messages) != 1 || out.Snapshot.Messages[0].Role != "user" ||
		out.Snapshot.Messages[0].Content[0].Text != "child task" {
		t.Fatalf("snapshot = %+v", out.Snapshot)
	}

	resp = h.do(http.MethodGet, "/api/sessions", "")
	defer resp.Body.Close()
	var list struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	for _, s := range list.Sessions {
		if s.ID == id {
			t.Fatal("a subagent transcript reached the sidebar list")
		}
	}
}

// The same endpoint answers for an ordinary top-level session: the subagents/
// fallback must not shadow the common case.
func TestTranscriptServesTopLevelSession(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("ok")))
	id := writeTranscript(t, h.mgr.cfg.SessionDir, "mine")

	resp := h.do(http.MethodGet, "/api/sessions/"+id+"/transcript", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET transcript: %d", resp.StatusCode)
	}
}

func TestTranscriptMissingIsNotFound(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("ok")))
	resp := h.do(http.MethodGet, "/api/sessions/20260101T000000Z-deadbeef/transcript", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET transcript: %d, want 404", resp.StatusCode)
	}
}

// Get and Delete resolve a transcript in either location, because the dock's
// subagent sheet can outlive the run that produced it.
func TestGetAndDeleteFindSubagentTranscript(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("ok")))
	dir := h.mgr.cfg.SessionDir
	id := writeTranscript(t, filepath.Join(dir, session.SubagentDirName), "child task")

	if _, err := h.mgr.Get(id); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := h.mgr.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := h.mgr.Get(id); err != ErrNotFound {
		t.Fatalf("Get after Delete: %v, want ErrNotFound", err)
	}
}
