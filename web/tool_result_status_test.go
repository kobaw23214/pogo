package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/tools"
)

func TestProjectedToolResultErrorRequiresTrustedBrowserMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, tool, details string
		original, want      bool
	}{
		{name: "native failure", tool: "browser", details: `{"action":"navigate","ok":false,"backend":"native"}`, want: true},
		{name: "legacy without backend", tool: "browser", details: `{"action":"batch","ok":false}`, want: true},
		{name: "original error kept", tool: "read", details: `{broken`, original: true, want: true},
		{name: "page error prose is not status", tool: "browser", details: `{"action":"snapshot","ok":true,"error":"failed","text":"browser click failed"}`},
		{name: "other tool", tool: "mcp_custom", details: `{"action":"click","ok":false,"backend":"native"}`},
		{name: "nested business status", tool: "browser", details: `{"action":"devtools","ok":true,"data":{"ok":false}}`},
		{name: "nested only", tool: "browser", details: `{"action":"devtools","data":{"ok":false}}`},
		{name: "string false", tool: "browser", details: `{"action":"click","ok":"false"}`},
		{name: "null false", tool: "browser", details: `{"action":"click","ok":null}`},
		{name: "uppercase is not schema key", tool: "browser", details: `{"action":"click","OK":false}`},
		{name: "unknown action", tool: "browser", details: `{"action":"unknown","ok":false}`},
		{name: "missing action", tool: "browser", details: `{"ok":false}`},
		{name: "bad action type", tool: "browser", details: `{"action":false,"ok":false}`},
		{name: "foreign backend", tool: "browser", details: `{"action":"click","ok":false,"backend":"external"}`},
		{name: "null backend", tool: "browser", details: `{"action":"click","ok":false,"backend":null}`},
		{name: "empty backend", tool: "browser", details: `{"action":"click","ok":false,"backend":""}`},
		{name: "invalid json", tool: "browser", details: `{broken`},
		{name: "trailing json", tool: "browser", details: `{"action":"click","ok":false} {}`},
		{name: "array", tool: "browser", details: `[{"action":"click","ok":false}]`},
		{name: "absent", tool: "browser"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectedToolResultError(tc.tool, tc.original, json.RawMessage(tc.details)); got != tc.want {
				t.Fatalf("projection = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestLegacyBrowserProjectionAgreesInHubAndMarkdownWithoutMutatingHistory(t *testing.T) {
	for _, tc := range []struct {
		tool, details string
		failed        bool
	}{
		{"browser", `{"action":"navigate","ok":false,"backend":"native"}`, true},
		{"browser", `{"action":"wait","ok":false}`, true},
		{"browser", `{"action":"devtools","ok":true,"data":{"ok":false}}`, false},
		{"mcp_custom", `{"action":"click","ok":false}`, false},
	} {
		history := []session.Timed{
			{Time: 1, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: "call", Name: tc.tool}}}},
			{Time: 2, Message: llm.Message{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call", Text: "error text is data", Details: json.RawMessage(tc.details)}}}},
		}
		before, _ := json.Marshal(history)
		h := NewHub()
		h.Seed(history)
		if h.Snapshot().Results["call"].IsError != tc.failed {
			t.Fatalf("historical DTO status differs from executor: %+v", h.Snapshot().Results)
		}
		var md strings.Builder
		renderResultBlock(&md, history[1].Message.Content[0], map[string]string{"call": tc.tool})
		if strings.Contains(md.String(), "结果** · 出错") != tc.failed || !strings.Contains(md.String(), "error text is data") {
			t.Fatalf("Markdown differs from DTO or lost text: %s", md.String())
		}
		after, _ := json.Marshal(history)
		if !bytes.Equal(before, after) || history[1].Message.Content[0].IsError {
			t.Fatal("read-time projection changed original history")
		}
	}
}

func TestLegacyBrowserReadingViewsKeepPortableAndStoredOriginalStatus(t *testing.T) {
	h := newHarness(t, idleRespond)
	store, err := session.Create(h.mgr.sessionDir(), t.TempDir(), "k3")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	registry := tools.New(tools.Options{Cwd: dir, SpillDir: filepath.Join(dir, "spill"), ResultScope: t.Name()})
	prepared := registry.PrepareResult(context.Background(), "browser", tools.Result{
		Text:    "browser navigate failed: page.goto timeout",
		Details: map[string]any{"action": "navigate", "ok": false, "backend": "native"},
	}, nil) // Reproduce the historical bug: executor false, generic error nil.
	if prepared.ResultID == "" {
		t.Fatal("legacy retained result fixture missing")
	}
	details, _ := json.Marshal(prepared.Details)
	if err := store.AppendAll([]llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: "legacy-browser", Name: "browser", Input: json.RawMessage(`{"action":"navigate","url":"https://fixture.invalid/"}`)}}},
		{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "legacy-browser", Text: prepared.Text, Details: details, ResultID: prepared.ResultID}}},
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	sid := strings.TrimSuffix(filepath.Base(store.Path()), ".jsonl")
	var view struct {
		Snapshot Snapshot `json:"snapshot"`
	}
	if err := decodeJSONBody(h.do(http.MethodGet, "/api/sessions/"+sid, ""), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Snapshot.Results["legacy-browser"].IsError {
		t.Fatal("session reading DTO kept the historical false success")
	}
	response := h.do(http.MethodGet, "/api/sessions/"+sid+"/export.md", "")
	defer response.Body.Close()
	var md bytes.Buffer
	if _, err := md.ReadFrom(response.Body); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("Markdown export unavailable: %v", err)
	}
	if !strings.Contains(md.String(), "结果** · 出错（按浏览器执行回执判定）") || !strings.Contains(md.String(), prepared.Text) {
		t.Fatal("reading export lost derived status or rewrote original text")
	}
	var portable exportedSession
	if err := decodeJSONBody(h.do(http.MethodGet, "/api/sessions/"+sid+"/export", ""), &portable); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, raw := range portable.Records {
		var record session.Record
		if json.Unmarshal(raw, &record) != nil || record.Message == nil {
			continue
		}
		for _, block := range record.Message.Content {
			if block.Type == llm.BlockToolResult && block.ToolUseID == "legacy-browser" {
				found = true
				if block.IsError || block.ResultID != prepared.ResultID || block.Text != prepared.Text {
					t.Fatal("portable JSON rewrote the historical result")
				}
			}
		}
	}
	if !found {
		t.Fatal("portable result missing")
	}
	after, err := os.ReadFile(store.Path())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("reading views wrote the original transcript")
	}
	reader, _ := registry.Get(tools.ToolResultReadName)
	input, _ := json.Marshal(map[string]any{"result_id": prepared.ResultID, "view": "text", "limit": 8192})
	retained, err := reader.Execute(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(retained.Details)
	var original map[string]any
	if json.Unmarshal(encoded, &original) != nil || original["original_is_error"] != false {
		t.Fatal("StoredResult original status was rewritten")
	}
}
