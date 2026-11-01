package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSubagentExecutionModeDependsOnValidatedArguments(t *testing.T) {
	s := &Subagent{}
	for _, tc := range []struct {
		raw  string
		want ExecutionMode
	}{
		{`{"mode":"explore","task":"read"}`, Parallel},
		{`{"mode":"explore","task":"read","wait":true}`, Parallel},
		{`{"mode":"edit","task":"write"}`, Sequential},
		{`{"mode":"edit","task":"write","wait":false}`, Sequential},
		{`{"mode":"explore","task":""}`, Sequential},
		{`{"mode":"explore","task":null}`, Sequential},
		{`{"mode":"explore","task":"read","wait":null}`, Sequential},
		{`{"mode":"explore"}`, Sequential},
		{`{"mode":"unknown","task":"read"}`, Sequential},
		{`{"mode":"explore","task":"read","extra":true}`, Sequential},
		{`{"mode":"explore","task":"read","wait":"false"}`, Sequential},
		{`{"Mode":"explore","task":"read"}`, Sequential},
		{`{`, Sequential},
	} {
		if got := s.ExecutionModeForArgs(json.RawMessage(tc.raw)); got != tc.want {
			t.Errorf("ExecutionModeForArgs(%s) = %v, want %v", tc.raw, got, tc.want)
		}
	}
	if s.ExecutionMode() != Sequential {
		t.Fatal("argument-unaware caller must remain sequential")
	}
}

func TestSubagentSchemaExplainsSynchronousEditing(t *testing.T) {
	s := &Subagent{Bg: &BgRunner{}}
	for _, schema := range []map[string]any{s.InputSchema(), s.Schema()} {
		props := schema["properties"].(map[string]any)
		wait := props["wait"].(map[string]any)["description"].(string)
		if !strings.Contains(wait, "Edit always") {
			t.Fatalf("schema permits edit backgrounding: %s", wait)
		}
	}
	for _, obsolete := range []string{"git worktree", "git cherry-pick", "come back as a commit"} {
		if strings.Contains(s.Description(), obsolete) {
			t.Errorf("retired instruction: %s", obsolete)
		}
	}
}

func TestSubagentHistoricalDetailsStillDecode(t *testing.T) {
	var d SubagentDetails
	if err := json.Unmarshal([]byte(`{"id":"sub_old","mode":"edit","ref":"refs/pogo/sub/sub_old","commit":"abc","worktree":"/old/worktree","tampered":true}`), &d); err != nil {
		t.Fatal(err)
	}
	if d.Ref == "" || d.Commit != "abc" || d.Worktree != "/old/worktree" || !d.Tampered {
		t.Fatalf("historical fields lost: %+v", d)
	}
}
