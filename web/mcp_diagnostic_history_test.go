package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/session"
)

func diagnosticHistoryFixture() ([]session.Record, *mcpobserve.Call) {
	call := &mcpobserve.Call{Direction: "outbound", Status: "error", SessionID: "synthetic-session", ToolCallID: "legacy-tool-call", WireTool: "mcp_fixture_lookup", StartedAt: 110}
	use := llm.Block{Type: llm.BlockToolUse, ID: call.ToolCallID, Name: call.WireTool, Input: json.RawMessage(`{"value":"private-input"}`)}
	result := llm.Block{Type: llm.BlockToolResult, ToolUseID: call.ToolCallID, IsError: true, Text: "business_error: Dataset does not exist for private-input; token=private-token"}
	rows := []session.Record{{ID: "meta", Type: "meta", Time: 100, Meta: &session.Meta{Cwd: "/synthetic", Model: "synthetic"}}, {ID: "assistant", ParentID: "meta", Type: "message", Time: 120, Message: &llm.Message{Role: "assistant", Content: []llm.Block{use}}}, {ID: "result", ParentID: "assistant", Type: "message", Time: 130, Message: &llm.Message{Role: "user", Content: []llm.Block{result}}}}
	return rows, call
}
func diagnosticHistoryBytes(rows []session.Record) []byte {
	var out []byte
	for _, r := range rows {
		raw, _ := json.Marshal(r)
		out = append(out, raw...)
		out = append(out, '\n')
	}
	return out
}
func TestMCPDiagnosticHistoryExactRawBranchAndCitation(t *testing.T) {
	rows, call := diagnosticHistoryFixture()
	raw := diagnosticHistoryBytes(rows)
	validMeta := func(m *session.Meta) bool { return m != nil && m.Model == "synthetic" }
	d := historicalMCPDiagnostic(raw, call, validMeta)
	if d.Code != "business_error" || !strings.Contains(d.Summary, "Dataset does not exist") || strings.Contains(d.Summary, "private-") {
		t.Fatalf("precise safe historical failure missing: %+v", d)
	}
	body := rows[2].Message.Content[0].Text
	sum := sha256.Sum256([]byte(body))
	source, _ := json.Marshal(map[string]string{"label": "S17", "type": "mcp_tool", "call_id": call.ToolCallID, "hash": hex.EncodeToString(sum[:])[:12]})
	rows[2].Message.Content[0].Text = "[cite:S17 | " + call.WireTool + " | fixture]\n" + body
	rows[2].Message.Content[0].Source = source
	if got := historicalMCPDiagnostic(diagnosticHistoryBytes(rows), call, validMeta); got != d {
		t.Fatalf("host citation header corrupted failure: %+v vs %+v", got, d)
	}
	// A current fork is irrelevant when the exact unique result is on an older
	// committed branch. Lookup is anchored to that result's own record ID.
	rows = append(rows, session.Record{ID: "new-branch", ParentID: "meta", Type: "fork", Time: 150})
	if got := historicalMCPDiagnostic(diagnosticHistoryBytes(rows), call, validMeta); got != d {
		t.Fatalf("unique historical branch lost: %+v", got)
	}
}
func TestMCPDiagnosticHistoryRejectsUnreliableEvidence(t *testing.T) {
	for _, name := range []string{"wrong-wire", "duplicate-use-other-branch", "duplicate-result", "success-result", "wrong-role", "missing-result", "corrupt-record", "uncommitted", "invalid-input", "large-input", "bad-citation", "too-old-result", "bad-meta"} {
		t.Run(name, func(t *testing.T) {
			rows, call := diagnosticHistoryFixture()
			validMeta := func(*session.Meta) bool { return true }
			var extra []byte
			switch name {
			case "wrong-wire":
				call.WireTool = "mcp_other_lookup"
			case "duplicate-use-other-branch":
				duplicate := *rows[1].Message
				rows = append(rows, session.Record{ID: "duplicate", ParentID: "meta", Type: "message", Time: 160, Message: &duplicate})
			case "duplicate-result":
				duplicate := *rows[2].Message
				rows = append(rows, session.Record{ID: "duplicate", ParentID: "assistant", Type: "message", Time: 160, Message: &duplicate})
			case "success-result":
				rows[2].Message.Content[0].IsError = false
			case "wrong-role":
				rows[2].Message.Role = "assistant"
			case "missing-result":
				rows = rows[:2]
			case "corrupt-record":
				extra = []byte("{unusable JSON\n")
			case "uncommitted":
				rows[1].BatchID = "pending"
				rows[1].BatchIndex = 0
				rows[1].BatchSize = 3
				rows[2].BatchID = "pending"
				rows[2].BatchIndex = 1
				rows[2].BatchSize = 3
			case "invalid-input":
				rows[1].Message.Content[0].Input = nil
			case "large-input":
				rows[1].Message.Content[0].Input, _ = json.Marshal(strings.Repeat("x", mcpDiagnosticHistoryMaxInput+1))
			case "bad-citation":
				rows[2].Message.Content[0].Text = "[cite:S17 | " + call.WireTool + " | fixture]\n" + rows[2].Message.Content[0].Text
				rows[2].Message.Content[0].Source = json.RawMessage(`{"label":"S17","type":"mcp_tool","call_id":"legacy-tool-call","hash":"wrong"}`)
			case "too-old-result":
				rows[2].Time = call.StartedAt - 1
			case "bad-meta":
				validMeta = func(*session.Meta) bool { return false }
			}
			raw := append(diagnosticHistoryBytes(rows), extra...)
			if got := historicalMCPDiagnostic(raw, call, validMeta); got != (mcpobserve.Diagnostic{}) {
				t.Fatalf("unreliable history got a cause: %+v", got)
			}
		})
	}
}
func TestMCPDiagnosticHistoryBoundedStableFileReader(t *testing.T) {
	dir := t.TempDir()
	server := &Server{mgr: &Manager{cfg: Config{SessionDir: dir}}}
	path := filepath.Join(dir, "synthetic.jsonl")
	if err := os.WriteFile(path, []byte("small synthetic file"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := server.mcpDiagnosticTranscript(context.Background(), "synthetic"); string(got) != "small synthetic file" {
		t.Fatalf("actual file missing: %q", got)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(mcpDiagnosticHistoryMaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := server.mcpDiagnosticTranscript(context.Background(), "synthetic"); got != nil {
		t.Fatal("oversize transcript read")
	}
	os.Remove(path)
	target := filepath.Join(t.TempDir(), "foreign.jsonl")
	os.WriteFile(target, []byte("private foreign fixture"), 0600)
	if err = os.Symlink(target, path); err != nil {
		t.Skip("symlinks unsupported")
	}
	if got := server.mcpDiagnosticTranscript(context.Background(), "synthetic"); got != nil {
		t.Fatal("symlink transcript followed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := server.mcpDiagnosticTranscript(ctx, "missing"); got != nil {
		t.Fatal("cancelled/missing transcript enriched")
	}
}
