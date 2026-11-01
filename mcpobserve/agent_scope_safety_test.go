package mcpobserve

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// Agent association is an authorization fact supplied by the local execution
// context. A remote MCP Call must not manufacture it, including for an
// otherwise unassociated invocation.
func TestAgentScopeTrustedContextControlsSpanIdentity(t *testing.T) {
	for _, tc := range []struct {
		name string
		md   Metadata
	}{
		{"trusted", Metadata{AgentID: "agent-trusted", AgentName: "可信助手"}},
		{"unknown", Metadata{}},
		{"known-without-name", Metadata{AgentID: "agent-trusted"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{}
			ctx := WithMetadata(WithRecorder(context.Background(), sink), tc.md)
			childCtx, parent := Start(ctx, Call{
				Direction: "inbound", Tool: "submit", AgentID: "forged-parent", AgentName: "forged name",
			})
			_, child := Start(childCtx, Call{
				Direction: "outbound", Tool: "query", AgentID: "forged-child", AgentName: "forged child name",
			})
			child.End("ok", "")
			parent.End("ok", "")
			if len(sink.calls) != 4 {
				t.Fatalf("got %d snapshots, want both starts and both completions", len(sink.calls))
			}
			for _, call := range sink.calls {
				if call.AgentID != tc.md.AgentID || call.AgentName != tc.md.AgentName {
					t.Fatalf("untrusted agent identity survived trusted context: got %q/%q, want %q/%q",
						call.AgentID, call.AgentName, tc.md.AgentID, tc.md.AgentName)
				}
			}
			if got := MetadataFrom(childCtx); got.AgentID != tc.md.AgentID || got.AgentName != tc.md.AgentName {
				t.Fatalf("child context lost trusted identity: %+v", got)
			}
			if sink.calls[1].ParentID != sink.calls[0].ID || sink.calls[1].TraceID != sink.calls[0].TraceID {
				t.Fatal("agent association broke the existing parent/trace relationship")
			}
		})
	}
}

func TestAgentScopeCorrelateUsesKnownTrustedIdentityAndBoundsIt(t *testing.T) {
	sink := &recordingSink{}
	ctx := WithMetadata(WithRecorder(context.Background(), sink), Metadata{
		AgentID: "old-agent", AgentName: "old name", SessionID: "session-original",
	})
	_, span := Start(ctx, Call{Direction: "outbound", Tool: "query"})
	span.Correlate(Metadata{AgentName: "name without trusted id"})
	span.Correlate(Metadata{})
	span.End("ok", "")
	if got := sink.calls[1]; got.AgentID != "old-agent" || got.AgentName != "old name" {
		t.Fatalf("empty/nameless correlation changed agent identity: %+v", got)
	}

	_, resolved := Start(ctx, Call{Direction: "outbound", Tool: "resolve"})
	resolved.Correlate(Metadata{AgentID: "new-agent", AgentName: "新名称"})
	resolved.Correlate(Metadata{AgentID: "known-without-name"})
	resolved.End("ok", "")
	if got := sink.calls[3]; got.AgentID != "known-without-name" || got.AgentName != "" || got.SessionID != "session-original" {
		t.Fatalf("trusted correlation retained stale agent name or changed session: %+v", got)
	}

	long := "\n" + strings.Repeat("中", 300) + "\x00"
	longCtx := WithMetadata(WithRecorder(context.Background(), sink), Metadata{AgentID: long, AgentName: long})
	_, boundedSpan := Start(longCtx, Call{Direction: "outbound", Tool: "bounded"})
	boundedSpan.Correlate(Metadata{AgentID: long, AgentName: long})
	boundedSpan.End("ok", "")
	for _, call := range sink.calls[4:] {
		for _, value := range []string{call.AgentID, call.AgentName} {
			if len([]rune(value)) != 256 || strings.ContainsAny(value, "\n\x00") {
				t.Fatalf("agent identity bypassed metadata bounds: rune length %d", len([]rune(value)))
			}
		}
	}
}

func TestAgentScopeCorrelationPersistsOneCompletedRow(t *testing.T) {
	sqlStore, _ := newSQLStore(t, StoreOptions{})
	memStore, err := NewStore(StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(memStore.Close)
	for _, s := range []*Store{memStore, sqlStore} {
		ctx := WithMetadata(WithRecorder(context.Background(), s), Metadata{OwnerID: "owner-a"})
		_, span := Start(ctx, Call{Direction: "inbound", Tool: "submit", AgentID: "forged", AgentName: "forged"})
		flush(t, s)
		before, err := s.Query(context.Background(), Filter{OwnerID: "owner-a", ExactOwner: true})
		if err != nil || len(before.Calls) != 1 || before.Calls[0].AgentID != "" || before.Calls[0].AgentName != "" {
			t.Fatalf("%s unresolved start acquired self-declared identity: %+v, %v", s.storageKind(), before, err)
		}
		span.Correlate(Metadata{AgentID: "resolved-agent", AgentName: "已解析助手", SessionID: "resolved-session"})
		span.End("ok", "")
		flush(t, s)
		after, err := s.Query(context.Background(), Filter{AgentID: "resolved-agent", OwnerID: "owner-a", ExactOwner: true})
		if err != nil || len(after.Calls) != 1 || after.Calls[0].ID != before.Calls[0].ID ||
			after.Calls[0].AgentName != "已解析助手" || after.Calls[0].SessionID != "resolved-session" || after.Calls[0].Status != "ok" {
			t.Fatalf("%s completion failed to update trusted identity on the original row: %+v, %v", s.storageKind(), after, err)
		}
		stats, err := s.Stats(context.Background(), Filter{})
		if err != nil || stats.Total != 1 || stats.Running != 0 || stats.OK != 1 {
			t.Fatalf("%s correlation duplicated a call or left a stale running row: %+v, %v", s.storageKind(), stats, err)
		}
	}
}

// Use an actual file and close/reopen every DB handle: this proves additive
// migration of the shipped schema, rather than exercising a newly created
// current table or assigning history to an Agent by a coincident session ID.
func TestAgentScopeLegacyFileMigrationRetainsUnknownHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-observe.db")
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
		client_id TEXT NOT NULL DEFAULT '', entrypoint TEXT NOT NULL DEFAULT '', recorder_instance_id TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	const legacyID = "legacy0001"
	when := time.Now().UnixMilli()
	_, err = db.Exec(`INSERT INTO mcpobserve_calls
		(id, trace_id, direction, server, tool, session_id, owner_id, started_at, finished_at, duration_ms, status)
		VALUES (?, 'historical-trace', 'outbound', 'database', 'select', 'shared-session', 'owner-a', ?, ?, 7, 'ok')`,
		legacyID, when, when+7)
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
		old, err := s.Detail(context.Background(), legacyID)
		if err != nil {
			t.Fatal(err)
		}
		if old.AgentID != "" || old.AgentName != "" || old.Tool != "select" || old.OwnerID != "owner-a" ||
			old.SessionID != "shared-session" || old.Status != "ok" || old.DurationMs != 7 {
			t.Fatalf("reopen %d migrated history incorrectly: %+v", reopen, old)
		}
		if reopen == 0 {
			s.Record(Call{ID: "current0001", Direction: "outbound", Tool: "select", SessionID: "shared-session",
				OwnerID: "owner-a", AgentID: "agent-a", AgentName: "当前助手", StartedAt: when + 10, Status: "ok"})
			flush(t, s)
		}
		page, err := s.Query(context.Background(), Filter{AgentID: "agent-a", OwnerID: "owner-a", ExactOwner: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Calls) != 1 || page.Calls[0].ID != "current0001" || page.Calls[0].AgentName != "当前助手" {
			t.Fatalf("reopen %d inferred agent ownership from historical session or lost new row: %+v", reopen, page.Calls)
		}
		if _, err := s.DetailScoped(context.Background(), legacyID, Filter{AgentID: "agent-a", OwnerID: "owner-a", ExactOwner: true}); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("legacy detail became associated by session, got %v", err)
		}
		all, err := s.Query(context.Background(), Filter{OwnerID: "owner-a", ExactOwner: true})
		if err != nil || len(all.Calls) != 2 {
			t.Fatalf("reopen %d failed to retain both known and unknown history: %+v, %v", reopen, all, err)
		}
		s.Close()
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentScopeQueryStatsPaginationAndDetailMatchBothBackends(t *testing.T) {
	now := time.Now().UnixMilli()
	row := func(n int, agent, owner, session, status string, duration float64) Call {
		return Call{ID: fmt.Sprintf("scope%02d", n), TraceID: "trace-shared", Direction: "outbound",
			Server: "database", Tool: "select", SessionID: session, OwnerID: owner, AgentID: agent,
			AgentName: "助手 " + agent, StartedAt: now + int64(n/2), Status: status, DurationMs: duration}
	}
	calls := []Call{
		row(1, "agent-a", "owner-a", "session-one", "ok", 10),
		row(2, "agent-a", "owner-a", "session-two", "error", 30),
		row(3, "agent-a", "owner-b", "session-one", "timeout", 50),
		row(4, "agent-b", "owner-a", "session-one", "ok", 20),
		row(5, "", "owner-a", "session-one", "ok", 5),
		row(6, "agent-a", "", "session-three", "cancelled", 15),
		row(7, "agent-b", "", "session-four", "running", 0),
		row(8, "agent-a", "owner-a", "session-two", "running", 0),
		row(9, "agent-a", "owner-a", "session-two", "timeout", 40),
	}
	calls[4].AgentName = "" // Historical/unassociated rows are explicitly unknown.
	sqlStore, _ := newSQLStore(t, StoreOptions{})
	memStore, err := NewStore(StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(memStore.Close)
	for _, s := range []*Store{memStore, sqlStore} {
		recordAll(t, s, calls)
	}

	cases := []struct {
		name string
		f    Filter
		ids  []string
		st   [7]int64 // total, running, ok, errors, cancelled, timeouts, interrupted
	}{
		{"agent-across-sessions", Filter{AgentID: "agent-a", OwnerID: "owner-a", ExactOwner: true}, []string{"scope09", "scope08", "scope02", "scope01"}, [7]int64{4, 1, 1, 2, 0, 1, 0}},
		{"agent-one-session", Filter{AgentID: "agent-a", OwnerID: "owner-a", ExactOwner: true, SessionID: "session-two"}, []string{"scope09", "scope08", "scope02"}, [7]int64{3, 1, 0, 2, 0, 1, 0}},
		{"unowned-agent", Filter{AgentID: "agent-a", ExactOwner: true}, []string{"scope06"}, [7]int64{1, 0, 0, 0, 1, 0, 0}},
		{"unowned-all-agents", Filter{ExactOwner: true}, []string{"scope07", "scope06"}, [7]int64{2, 1, 0, 0, 1, 0, 0}},
		{"unknown-owner-is-not-wildcard", Filter{AgentID: "agent-a", OwnerID: "missing", ExactOwner: true}, []string{}, [7]int64{}},
		{"management-wildcard-preserved", Filter{AgentID: "agent-a"}, []string{"scope09", "scope08", "scope06", "scope03", "scope02", "scope01"}, [7]int64{6, 1, 1, 3, 1, 2, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stats []Stats
			for _, s := range []*Store{memStore, sqlStore} {
				f := tc.f
				f.Limit = 1
				var ids []string
				seen := map[string]bool{}
				for pageNumber := 0; ; pageNumber++ {
					if pageNumber > len(calls) {
						t.Fatal("scoped pagination failed to terminate")
					}
					page, err := s.Query(context.Background(), f)
					if err != nil {
						t.Fatal(err)
					}
					for _, c := range page.Calls {
						if seen[c.ID] {
							t.Fatalf("duplicate call %s across scoped pages", c.ID)
						}
						seen[c.ID] = true
						ids = append(ids, c.ID)
					}
					if page.NextCursor == "" {
						break
					}
					f.Before = page.NextCursor
				}
				if len(ids) != len(tc.ids) || (len(ids) > 0 && !reflect.DeepEqual(ids, tc.ids)) {
					t.Fatalf("%s scope returned %v, want %v", s.storageKind(), ids, tc.ids)
				}
				// A cursor cannot reduce aggregates to the currently visible page.
				f.Before = encodeCursor(now+100, "scope99")
				st, err := s.Stats(context.Background(), f)
				if err != nil {
					t.Fatal(err)
				}
				got := [7]int64{st.Total, st.Running, st.OK, st.Errors, st.Cancelled, st.Timeouts, st.Interrupted}
				if got != tc.st || st.ByDirection.Outbound != tc.st[0] || st.ByDirection.Inbound != 0 {
					t.Fatalf("%s aggregates escaped the requested scope: %+v, want counts %v", s.storageKind(), st, tc.st)
				}
				if st.Dropped != 0 || st.WriteErrors != 0 {
					t.Fatalf("test lost telemetry: %+v", st)
				}
				st.Storage = ""
				stats = append(stats, st)
				for _, c := range calls {
					got, err := s.DetailScoped(context.Background(), c.ID, tc.f)
					if seen[c.ID] {
						if err != nil || got == nil || got.ID != c.ID || got.AgentID != c.AgentID || got.AgentName != c.AgentName {
							t.Fatalf("%s allowed scoped row lost its identity: %+v, %v", s.storageKind(), got, err)
						}
					} else if !errors.Is(err, sql.ErrNoRows) || got != nil {
						t.Fatalf("%s scoped detail disclosed %s outside the query's scope: %+v, %v", s.storageKind(), c.ID, got, err)
					}
				}
			}
			if !reflect.DeepEqual(stats[0], stats[1]) {
				t.Fatalf("memory/SQLite scoped aggregates diverged: memory %+v, SQLite %+v", stats[0], stats[1])
			}
		})
	}
	for _, s := range []*Store{memStore, sqlStore} {
		all, err := s.Query(context.Background(), Filter{})
		if err != nil || len(all.Calls) != len(calls) {
			t.Fatalf("management query compatibility changed: %d calls, %v", len(all.Calls), err)
		}
		ids := make([]string, 0, len(all.Calls))
		for _, c := range all.Calls {
			ids = append(ids, c.ID)
		}
		sort.Strings(ids)
		if ids[0] != "scope01" || ids[len(ids)-1] != "scope09" {
			t.Fatalf("management history lost rows: %v", ids)
		}
	}
}
