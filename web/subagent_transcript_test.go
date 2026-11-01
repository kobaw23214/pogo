package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

func retainedTranscriptFixture(t *testing.T, m *Manager, parent *Session) (string, string, string) {
	t.Helper()
	store, err := session.Create(filepath.Join(m.cfg.SessionDir, session.SubagentDirName), parent.cwd, "k3")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAll([]llm.Message{llm.UserText("private child task"), {Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "retained child result"}}}}); err != nil {
		t.Fatal(err)
	}
	ownerKey := "single-user"
	if p := parent.owner; p != nil {
		ownerKey = fmt.Sprintf("%q/%q/%q/%q", p.AppID, p.TenantID, p.SubjectID, p.ProjectID)
	}
	key := sha256.Sum256([]byte(ownerKey + "\x00" + parent.ID))
	agentID := "agent_" + strings.Repeat("a", 32)
	record := filepath.Join(m.cfg.SessionDir, session.SubagentDirName, "agents", hex.EncodeToString(key[:]), agentID+".json")
	data, _ := json.Marshal(map[string]any{"version": 1, "agent_id": agentID, "parent_session_id": parent.ID, "owner_key": ownerKey, "session": store.Path(), "status": "running", "mode": "explore", "model": "k3"})
	if err := os.MkdirAll(filepath.Dir(record), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, data, 0600); err != nil {
		t.Fatal(err)
	}
	return agentID, store.Path(), record
}

func TestRetainedSubagentTranscriptUsesParentOwnershipAndNeverStartsChild(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("must not run")))
	parentID := h.createAs(aliceToken)
	siblingID := h.createAs(aliceToken)
	bobID := h.createAs(bobToken)
	parent := h.session(parentID)
	agentID, childPath, recordPath := retainedTranscriptFixture(t, h.mgr, parent)
	beforeTranscript, _ := os.ReadFile(childPath)
	beforeRecord, _ := os.ReadFile(recordPath)
	childID := strings.TrimSuffix(filepath.Base(childPath), ".jsonl")
	for _, tc := range []struct {
		token, parent, agent string
		code                 int
	}{
		{aliceToken, parentID, agentID, 200},
		{bobToken, parentID, agentID, 404},
		{aliceToken, siblingID, agentID, 404},
		{bobToken, bobID, agentID, 404},
		{aliceToken, parentID, "agent_" + strings.Repeat("b", 32), 404},
	} {
		resp := h.doAs(tc.token, http.MethodGet, "/api/sessions/"+tc.parent+"/subagents/"+tc.agent+"/transcript", "")
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.code {
			t.Fatalf("read as %+v returned %d: %s", tc, resp.StatusCode, body)
		}
		if tc.code == 200 {
			var out struct {
				SessionID string   `json:"session_id"`
				Snapshot  Snapshot `json:"snapshot"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			if out.SessionID != childID || len(out.Snapshot.Messages) != 2 || out.Snapshot.Run.Active || out.Snapshot.Messages[1].Content[0].Text != "retained child result" {
				t.Fatalf("bad child transcript: %+v", out)
			}
		} else if bytes.Contains(body, []byte("retained child result")) || bytes.Contains(body, []byte(childPath)) {
			t.Fatal("foreign parent leaked child data")
		}
	}
	// The child is still absent from the root catalog; its old root route must
	// remain denied even to the parent owner.
	resp := h.doAs(aliceToken, http.MethodGet, "/api/sessions/"+childID+"/transcript", "")
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("child became a root session: %d", resp.StatusCode)
	}
	h.mgr.mu.Lock()
	_, adopted := h.mgr.sessions[childID]
	h.mgr.mu.Unlock()
	if adopted || len(h.rec.systems()) != 0 {
		t.Fatal("read-only child transcript started or adopted a runtime")
	}
	afterTranscript, _ := os.ReadFile(childPath)
	afterRecord, _ := os.ReadFile(recordPath)
	if !bytes.Equal(beforeTranscript, afterTranscript) || !bytes.Equal(beforeRecord, afterRecord) {
		t.Fatal("reading changed child state")
	}
}

func TestRetainedSubagentTranscriptRejectsForgedOwnerRecord(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("must not run")))
	parentID := h.createAs(aliceToken)
	agentID, _, recordPath := retainedTranscriptFixture(t, h.mgr, h.session(parentID))
	data, _ := os.ReadFile(recordPath)
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	record["owner_key"] = "forged-owner"
	data, _ = json.Marshal(record)
	if err := os.WriteFile(recordPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	resp := h.doAs(aliceToken, http.MethodGet, "/api/sessions/"+parentID+"/subagents/"+agentID+"/transcript", "")
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("forged owner accepted: %d", resp.StatusCode)
	}
}
