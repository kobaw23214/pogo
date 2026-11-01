package mcpobserve

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func assertDiagnosticSafety(t *testing.T, d Diagnostic, forbidden ...string) {
	t.Helper()
	if !utf8.ValidString(d.Summary) || len([]rune(d.Summary)) > 512 {
		t.Fatalf("diagnostic summary exceeded its Unicode bound: %d runes", len([]rune(d.Summary)))
	}
	for _, r := range d.Summary {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			t.Fatalf("diagnostic is not a single safe line: %q", d.Summary)
		}
	}
	if len(d.Code) > 64 {
		t.Fatalf("diagnostic code exceeded its bound: %d bytes", len(d.Code))
	}
	for _, r := range d.Code {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("_.:-", r)) {
			t.Fatalf("diagnostic code is not an ASCII identifier: %q", d.Code)
		}
	}
	for _, value := range forbidden {
		if value != "" && (strings.Contains(d.Summary, value) || strings.Contains(d.Code, value)) {
			t.Fatalf("synthetic sensitive canary leaked into diagnostic: %q", value)
		}
	}
}

func TestDiagnosticSafetyFinalSanitizerRedactsSecretsAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		forbidden  []string
	}{
		{"credentials", "access denied password=PasswordCanary892 token=TokenCanary337 Authorization: Bearer BearerCanary410", []string{"PasswordCanary892", "TokenCanary337", "BearerCanary410"}},
		{"URL", "connection refused https://user:URLPasswordCanary@fixture.invalid/path?token=QueryCanary334&password=QueryPasswordCanary#FragmentCanary", []string{"URLPasswordCanary", "QueryCanary334", "QueryPasswordCanary"}},
		{"SQL", "database rejected SELECT * FROM accounts WHERE password='SQLLiteralCanary773' AND token='SQLTokenCanary556'", []string{"SQLLiteralCanary773", "SQLTokenCanary556"}},
		{"multiline-unicode", strings.Repeat("错误原因", 300) + "\n\r\t\x00\u2028\u2029 tail", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := SafeDiagnostic(Diagnostic{Summary: tc.text, Code: "BAD CODE\n令牌 " + strings.Repeat("C", 100)})
			assertDiagnosticSafety(t, d, tc.forbidden...)
			if d.Summary == "" {
				t.Fatal("safe generic failure reason was lost completely")
			}
			if again := SafeDiagnostic(d); !reflect.DeepEqual(again, d) {
				t.Fatalf("final sanitization is not idempotent: %+v then %+v", d, again)
			}
		})
	}
}

func TestDiagnosticSafetyRPCAndArgumentEchoDoNotPersistPayload(t *testing.T) {
	args := json.RawMessage(`{"password":"ArgPasswordCanary992","token":"ArgTokenCanary442","database":"ArgDatabaseCanary552","nested":{"user":"NestedScalarCanary331"}}`)
	rpc := &jsonrpc.Error{
		Code:    -32001,
		Message: "permission denied ArgDatabaseCanary552 NestedScalarCanary331 password=ArgPasswordCanary992 token=ArgTokenCanary442",
		Data:    json.RawMessage(`{"message":"RPCDataCanary772","rows":["RPCRowCanary554"]}`),
	}
	d := DiagnoseError(fmt.Errorf("transport wrapper: %w", rpc), args)
	assertDiagnosticSafety(t, d, "ArgPasswordCanary992", "ArgTokenCanary442", "ArgDatabaseCanary552", "NestedScalarCanary331", "RPCDataCanary772", "RPCRowCanary554")
	if !strings.Contains(strings.ToLower(d.Summary), "permission denied") || !strings.Contains(d.Code, "32001") {
		t.Fatalf("useful RPC reason or numeric code lost: %+v", d)
	}
	if got := DiagnoseError(nil, args); got != (Diagnostic{}) {
		t.Fatalf("nil error generated a diagnostic from arguments: %+v", got)
	}
	// Malformed arguments cannot force whole-payload serialization or a panic.
	assertDiagnosticSafety(t, DiagnoseError(errors.New("permission denied token=MalformedTokenCanary"), json.RawMessage(`{"password":`)), "MalformedTokenCanary")
	assertDiagnosticSafety(t, DiagnoseError(errors.New("permission denied TrailingArgCanary"), json.RawMessage(`{} {"password":"TrailingArgCanary"}`)), "TrailingArgCanary")
}

func TestDiagnosticSafetyResultAllowlistExcludesNonReasonPayload(t *testing.T) {
	args := json.RawMessage(`{"password":"ResultPasswordCanary","url":"https://fixture.invalid/?token=ArgURLCanary"}`)
	for _, tc := range []struct {
		name string
		r    *mcp.CallToolResult
	}{
		{"structured", &mcp.CallToolResult{IsError: true, StructuredContent: map[string]any{
			"message": "permission denied password=ResultPasswordCanary", "code": "DENIED",
			"rows": []any{"RowPayloadCanary"}, "output": "OutputPayloadCanary", "debug": map[string]any{"message": "NestedPayloadCanary"},
		}}},
		{"JSON-text", &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: `{"reason":"permission denied token=ResultTokenCanary","rows":["RowPayloadCanary"],"output":"OutputPayloadCanary","debug":{"message":"NestedPayloadCanary"}}`}}}},
		{"detail", &mcp.CallToolResult{IsError: true, StructuredContent: map[string]any{
			"detail": "permission denied at https://fixture.invalid/path?token=ResultURLCanary", "response_body": "ResponseBodyCanary",
		}}},
		{"code-echo", &mcp.CallToolResult{IsError: true, StructuredContent: map[string]any{
			"message": "permission denied", "code": "ERR_ResultPasswordCanary",
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := DiagnoseResult(tc.r, args)
			assertDiagnosticSafety(t, d, "ResultPasswordCanary", "ResultTokenCanary", "ResultURLCanary", "ArgURLCanary", "RowPayloadCanary", "OutputPayloadCanary", "NestedPayloadCanary", "ResponseBodyCanary")
			if !strings.Contains(strings.ToLower(d.Summary), "permission denied") {
				t.Fatalf("safe allowlisted error cause was not retained: %+v", d)
			}
		})
	}
	for _, result := range []*mcp.CallToolResult{
		nil,
		{Content: []mcp.Content{&mcp.TextContent{Text: "SuccessPayloadCanary"}}, StructuredContent: map[string]any{"message": "SuccessMessageCanary"}},
	} {
		if d := DiagnoseResult(result, args); d != (Diagnostic{}) {
			t.Fatalf("successful/absent tool result was captured as diagnostics: %+v", d)
		}
	}
	d := DiagnoseResult(&mcp.CallToolResult{IsError: true, StructuredContent: map[string]any{"rows": []string{"OnlyRowsCanary"}}, Content: []mcp.Content{
		&mcp.ImageContent{Data: []byte("ImagePayloadCanary"), MIMEType: "image/png"},
	}}, nil)
	assertDiagnosticSafety(t, d, "OnlyRowsCanary", "ImagePayloadCanary", "SW1hZ2VQYXlsb2FkQ2FuYXJ5")
}

func TestDiagnosticSafetySpanAndDirectStoreBoundary(t *testing.T) {
	sqlStore, _ := newSQLStore(t, StoreOptions{})
	memStore, err := NewStore(StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(memStore.Close)
	for _, s := range []*Store{memStore, sqlStore} {
		t.Run(s.storageKind(), func(t *testing.T) {
			ctx := WithMetadata(WithRecorder(context.Background(), s), Metadata{OwnerID: "owner", AgentID: "agent"})
			_, span := Start(ctx, Call{Direction: "outbound", Tool: "select"})
			span.EndDiagnostic("error", "tool_error", Diagnostic{Summary: "permission denied password=SpanPasswordCanary", Code: "ERR_ACCESS"})
			span.EndDiagnostic("error", "tool_error", Diagnostic{Summary: "second completion must not replace first", Code: "SECOND"})
			s.Record(Call{ID: "direct-write-" + s.storageKind(), Direction: "outbound", Tool: "raw", Status: "error", StartedAt: time.Now().UnixMilli(),
				ErrorSummary: "database failed token=StoreTokenCanary https://fixture.invalid/?password=StoreQueryCanary WHERE secret='StoreSQLCanary'",
				ErrorCode:    "unsafe\nCODE " + strings.Repeat("Z", 100)})
			flush(t, s)
			page, err := s.Query(context.Background(), Filter{})
			if err != nil || len(page.Calls) != 2 {
				t.Fatalf("completed diagnostic duplicated/lost rows: %+v, %v", page, err)
			}
			for _, c := range page.Calls {
				assertDiagnosticSafety(t, Diagnostic{Summary: c.ErrorSummary, Code: c.ErrorCode}, "SpanPasswordCanary", "StoreTokenCanary", "StoreQueryCanary", "StoreSQLCanary")
				if c.Tool == "select" && (c.ErrorCode != "ERR_ACCESS" || !strings.Contains(c.ErrorSummary, "permission denied") || c.ErrorKind != "tool_error") {
					t.Fatalf("first diagnostic completion was not retained: %+v", c)
				}
			}
		})
	}
}

func TestDiagnosticSafetyPreviousSchemaMigrationAndScopedReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prediagnostic.db")
	open := func() *sql.DB {
		t.Helper()
		db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	db := open()
	_, err := db.Exec(`CREATE TABLE mcpobserve_calls (
		id TEXT PRIMARY KEY, trace_id TEXT NOT NULL DEFAULT '', parent_id TEXT NOT NULL DEFAULT '',
		direction TEXT NOT NULL DEFAULT '', server TEXT NOT NULL DEFAULT '', tool TEXT NOT NULL DEFAULT '',
		wire_tool TEXT NOT NULL DEFAULT '', transport TEXT NOT NULL DEFAULT '', client TEXT NOT NULL DEFAULT '',
		mcp_session_id TEXT NOT NULL DEFAULT '', request_id TEXT NOT NULL DEFAULT '', session_id TEXT NOT NULL DEFAULT '',
		run_id TEXT NOT NULL DEFAULT '', task_id TEXT NOT NULL DEFAULT '', owner_id TEXT NOT NULL DEFAULT '',
		tool_call_id TEXT NOT NULL DEFAULT '', started_at INTEGER NOT NULL, finished_at INTEGER NOT NULL DEFAULT 0,
		duration_ms REAL NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT '', error_kind TEXT NOT NULL DEFAULT '',
		span_id TEXT NOT NULL DEFAULT '', parent_span_id TEXT NOT NULL DEFAULT '', trace_flags TEXT NOT NULL DEFAULT '',
		client_id TEXT NOT NULL DEFAULT '', entrypoint TEXT NOT NULL DEFAULT '', recorder_instance_id TEXT NOT NULL DEFAULT '',
		agent_id TEXT NOT NULL DEFAULT '', agent_name TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	_, err = db.Exec(`INSERT INTO mcpobserve_calls (id, direction, tool, owner_id, agent_id, agent_name, session_id, started_at, status, error_kind)
		VALUES ('legacydiagnostic', 'outbound', 'legacy', 'owner-a', 'agent-a', '旧名称', 'legacy-session', ?, 'error', 'tool_error')`, now)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for reopen := range 2 {
		db = open()
		s, err := NewStore(StoreOptions{DB: db})
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		legacy, err := s.Detail(context.Background(), "legacydiagnostic")
		if err != nil || legacy.ErrorSummary != "" || legacy.ErrorCode != "" || legacy.ErrorKind != "tool_error" || legacy.AgentName != "旧名称" || legacy.Status != "error" {
			t.Fatalf("reopen %d changed legacy outcome or invented a reason: %+v, %v", reopen, legacy, err)
		}
		if reopen == 0 {
			for i, c := range []Call{
				{OwnerID: "owner-a", AgentID: "agent-a", SessionID: "session-one", ErrorSummary: "Own diagnostic one"},
				{OwnerID: "owner-a", AgentID: "agent-a", SessionID: "session-two", ErrorSummary: "Own diagnostic two"},
				{OwnerID: "owner-b", AgentID: "agent-a", ErrorSummary: "ForeignOwnerDiagnosticCanary"},
				{OwnerID: "owner-a", AgentID: "agent-b", ErrorSummary: "ForeignAgentDiagnosticCanary"},
				{OwnerID: "", AgentID: "agent-a", ErrorSummary: "UnownedDiagnosticCanary"},
			} {
				c.ID, c.Tool, c.Direction, c.Status, c.StartedAt, c.ErrorCode = fmt.Sprintf("newdiagnostic%d", i), "diagnostic", "outbound", "error", now+int64(i+1), "ERR_ACCESS"
				s.Record(c)
			}
			flush(t, s)
		}
		f := Filter{OwnerID: "owner-a", ExactOwner: true, AgentID: "agent-a", Limit: 1}
		calls := collectAll(t, s, f)
		if len(calls) != 3 {
			t.Fatalf("reopen %d lost old/new scoped diagnostic history: %+v", reopen, calls)
		}
		raw, _ := json.Marshal(calls)
		for _, secret := range []string{"ForeignOwnerDiagnosticCanary", "ForeignAgentDiagnosticCanary", "UnownedDiagnosticCanary"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("scoped page leaked foreign diagnostic %s", secret)
			}
		}
		for i := 2; i < 5; i++ {
			if got, err := s.DetailScoped(context.Background(), fmt.Sprintf("newdiagnostic%d", i), f); got != nil || !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("scoped detail disclosed another owner/Agent's diagnostic: %+v, %v", got, err)
			}
		}
		own, err := s.DetailScoped(context.Background(), "newdiagnostic0", f)
		if err != nil || own.ErrorSummary != "Own diagnostic one" || own.ErrorCode != "ERR_ACCESS" {
			t.Fatalf("reopen %d lost saved diagnostic fields: %+v, %v", reopen, own, err)
		}
		unowned := collectAll(t, s, Filter{ExactOwner: true, AgentID: "agent-a"})
		if len(unowned) != 1 || unowned[0].ErrorSummary != "UnownedDiagnosticCanary" {
			t.Fatalf("empty owner scope became a diagnostic wildcard: %+v", unowned)
		}
		s.Close()
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
