// Query, detail, and aggregate reads for the store. List and stats share one
// filter implementation per backend so the numbers always describe exactly
// the rows the list can show. Pagination is a keyset on (started_at, id)
// descending with an opaque, validated cursor.
package mcpobserve

import (
	"context"
	"database/sql"
	"encoding/base64"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Filter narrows list and stats answers. Exact fields match exactly; Query
// is a substring search over safe metadata only (identifiers, server/tool
// names, client, error kind and redacted diagnostic — never full payloads).
// From/To are UTC Unix milliseconds on started_at. Before is the opaque
// keyset cursor from a previous Page. Limit is bounded by the store.
type Filter struct {
	Direction string
	Server    string
	Tool      string
	Status    string
	SessionID string
	RunID     string
	TaskID    string
	TraceID   string
	OwnerID   string
	AgentID   string
	// ExactOwner enforces even the classic empty-owner scope; never a wildcard.
	ExactOwner bool
	Client     string
	ClientID   string
	Entrypoint string
	Query      string
	Before     string
	From       int64
	To         int64
	Limit      int
}

// Page is one window of the call list. Calls is never nil; counters surface
// queue loss instead of hiding it.
type Page struct {
	Calls       []Call `json:"calls"`
	NextCursor  string `json:"next_cursor"`
	Storage     string `json:"storage"`
	Dropped     int64  `json:"dropped"`
	WriteErrors int64  `json:"write_errors"`
}

// ToolStat aggregates one direction/server/tool bucket. P95Ms is nil when the
// bucket has no completed samples.
type ToolStat struct {
	Direction string   `json:"direction"`
	Server    string   `json:"server"`
	Tool      string   `json:"tool"`
	Total     int64    `json:"total"`
	Errors    int64    `json:"errors"`
	P95Ms     *float64 `json:"p95_ms"`
}

// DirectionCounts always carries both keys, zero included.
type DirectionCounts struct {
	Inbound  int64 `json:"inbound"`
	Outbound int64 `json:"outbound"`
}

// Stats aggregates every retained row matching the filter, independent of
// Limit/Before. Errors counts status error+timeout; ErrorRate divides by
// ok+error+timeout only, so running/cancelled/interrupted never masquerade
// as successes. Rate and latency fields are null without completed samples.
type Stats struct {
	Total       int64           `json:"total"`
	Running     int64           `json:"running"`
	OK          int64           `json:"ok"`
	Errors      int64           `json:"errors"`
	Cancelled   int64           `json:"cancelled"`
	Timeouts    int64           `json:"timeouts"`
	Interrupted int64           `json:"interrupted"`
	ErrorRate   *float64        `json:"error_rate"`
	P50Ms       *float64        `json:"p50_ms"`
	P95Ms       *float64        `json:"p95_ms"`
	MaxMs       *float64        `json:"max_ms"`
	ByDirection DirectionCounts `json:"by_direction"`
	ByTool      []ToolStat      `json:"by_tool"`
	Storage     string          `json:"storage"`
	Dropped     int64           `json:"dropped"`
	WriteErrors int64           `json:"write_errors"`
}

// latencyStatuses are the completed outcomes that count as latency samples.
var latencyStatuses = []string{"ok", "error", "timeout", "cancelled"}

func (s *Store) storageKind() string {
	if s.memory {
		return "memory"
	}
	return "sqlite"
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultQueryLimit
	}
	if limit > maxQueryLimit {
		return maxQueryLimit
	}
	return limit
}

// Query returns one page of calls, newest first.
func (s *Store) Query(ctx context.Context, f Filter) (Page, error) {
	p := Page{Calls: []Call{}, Storage: s.storageKind(),
		Dropped: s.dropped.Load(), WriteErrors: s.writeErrors.Load()}
	f.Limit = clampLimit(f.Limit)
	var (
		calls []Call
		next  string
		err   error
	)
	if s.memory {
		calls, next, err = s.memQuery(f)
	} else {
		calls, next, err = s.sqlQuery(ctx, f)
	}
	if err != nil {
		return p, err
	}
	p.Calls, p.NextCursor = calls, next
	return p, nil
}

// Detail returns one call by id, sql.ErrNoRows when unknown.
func (s *Store) Detail(ctx context.Context, id string) (*Call, error) {
	if s.memory {
		s.memMu.RLock()
		defer s.memMu.RUnlock()
		if c, ok := s.mem[id]; ok {
			cp := c
			return &cp, nil
		}
		return nil, sql.ErrNoRows
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+callColumns+` FROM `+storeTable+` WHERE id = ?`, id)
	c, err := scanCall(row)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// DetailScoped applies the same ownership predicates as list/stats, including
// an explicitly empty owner. A foreign call is indistinguishable from absent.
func (s *Store) DetailScoped(ctx context.Context, id string, f Filter) (*Call, error) {
	c, err := s.Detail(ctx, id)
	if err != nil {
		return nil, err
	}
	if !s.matches(c, &f, s.retentionCutoff(time.Now())) {
		return nil, sql.ErrNoRows
	}
	return c, nil
}

// Stats aggregates all retained rows matching the filter; Limit and Before
// are deliberately ignored so UI page size never skews the numbers.
func (s *Store) Stats(ctx context.Context, f Filter) (Stats, error) {
	st := Stats{ByTool: []ToolStat{}, Storage: s.storageKind(),
		Dropped: s.dropped.Load(), WriteErrors: s.writeErrors.Load()}
	f.Limit, f.Before = 0, ""
	var err error
	if s.memory {
		err = s.memStats(f, &st)
	} else {
		err = s.sqlStats(ctx, f, &st)
	}
	if err != nil {
		return st, err
	}
	return st, nil
}

const callColumns = `id, trace_id, parent_id, direction, server, tool,
	wire_tool, transport, client, mcp_session_id, request_id, session_id,
	run_id, task_id, owner_id, tool_call_id, started_at, finished_at,
	duration_ms, status, error_kind, span_id, parent_span_id, trace_flags,
	client_id, entrypoint, agent_id, agent_name, error_summary, error_code`

type scannable interface{ Scan(dest ...any) error }

func scanCall(row scannable) (Call, error) {
	var c Call
	err := row.Scan(&c.ID, &c.TraceID, &c.ParentID, &c.Direction, &c.Server,
		&c.Tool, &c.WireTool, &c.Transport, &c.Client, &c.MCPSessionID,
		&c.RequestID, &c.SessionID, &c.RunID, &c.TaskID, &c.OwnerID,
		&c.ToolCallID, &c.StartedAt, &c.FinishedAt, &c.DurationMs, &c.Status,
		&c.ErrorKind, &c.SpanID, &c.ParentSpanID, &c.TraceFlags, &c.ClientID,
		&c.Entrypoint, &c.AgentID, &c.AgentName, &c.ErrorSummary, &c.ErrorCode)
	return c, err
}

// queryFields are the safe metadata columns the substring search covers.
var queryFields = []struct {
	col  string
	read func(*Call) string
}{
	{"id", func(c *Call) string { return c.ID }},
	{"trace_id", func(c *Call) string { return c.TraceID }},
	{"parent_id", func(c *Call) string { return c.ParentID }},
	{"server", func(c *Call) string { return c.Server }},
	{"tool", func(c *Call) string { return c.Tool }},
	{"wire_tool", func(c *Call) string { return c.WireTool }},
	{"client", func(c *Call) string { return c.Client }},
	{"mcp_session_id", func(c *Call) string { return c.MCPSessionID }},
	{"request_id", func(c *Call) string { return c.RequestID }},
	{"session_id", func(c *Call) string { return c.SessionID }},
	{"run_id", func(c *Call) string { return c.RunID }},
	{"task_id", func(c *Call) string { return c.TaskID }},
	{"owner_id", func(c *Call) string { return c.OwnerID }},
	{"agent_id", func(c *Call) string { return c.AgentID }},
	{"agent_name", func(c *Call) string { return c.AgentName }},
	{"tool_call_id", func(c *Call) string { return c.ToolCallID }},
	{"error_kind", func(c *Call) string { return c.ErrorKind }},
	{"error_summary", func(c *Call) string { return c.ErrorSummary }},
	{"error_code", func(c *Call) string { return c.ErrorCode }},
	{"client_id", func(c *Call) string { return c.ClientID }},
	{"entrypoint", func(c *Call) string { return c.Entrypoint }},
}

var exactFields = []struct {
	col  string
	get  func(*Filter) string
	read func(*Call) string
}{
	{"direction", func(f *Filter) string { return f.Direction }, func(c *Call) string { return c.Direction }},
	{"server", func(f *Filter) string { return f.Server }, func(c *Call) string { return c.Server }},
	{"tool", func(f *Filter) string { return f.Tool }, func(c *Call) string { return c.Tool }},
	{"status", func(f *Filter) string { return f.Status }, func(c *Call) string { return c.Status }},
	{"session_id", func(f *Filter) string { return f.SessionID }, func(c *Call) string { return c.SessionID }},
	{"run_id", func(f *Filter) string { return f.RunID }, func(c *Call) string { return c.RunID }},
	{"task_id", func(f *Filter) string { return f.TaskID }, func(c *Call) string { return c.TaskID }},
	{"trace_id", func(f *Filter) string { return f.TraceID }, func(c *Call) string { return c.TraceID }},
	{"owner_id", func(f *Filter) string { return f.OwnerID }, func(c *Call) string { return c.OwnerID }},
	{"agent_id", func(f *Filter) string { return f.AgentID }, func(c *Call) string { return c.AgentID }},
	{"client", func(f *Filter) string { return f.Client }, func(c *Call) string { return c.Client }},
	{"client_id", func(f *Filter) string { return f.ClientID }, func(c *Call) string { return c.ClientID }},
	{"entrypoint", func(f *Filter) string { return f.Entrypoint }, func(c *Call) string { return c.Entrypoint }},
}

// matches is the memory-mode predicate; buildWhere encodes the same rules in
// SQL. Both must change together — the parity test pins them.
func (s *Store) matches(c *Call, f *Filter, cutoff int64) bool {
	if cutoff != 0 && c.StartedAt < cutoff {
		return false
	}
	for _, ef := range exactFields {
		if v := ef.get(f); (v != "" || ef.col == "owner_id" && f.ExactOwner) && ef.read(c) != v {
			return false
		}
	}
	if f.From > 0 && c.StartedAt < f.From {
		return false
	}
	if f.To > 0 && c.StartedAt > f.To {
		return false
	}
	if f.Query != "" {
		q := strings.ToLower(f.Query)
		found := false
		for _, qf := range queryFields {
			if strings.Contains(strings.ToLower(qf.read(c)), q) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func likeEscape(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(v)
}

// buildWhere translates the filter into parameterized SQL mirroring matches.
func (s *Store) buildWhere(f *Filter, cutoff int64) (string, []any) {
	var where []string
	var args []any
	if cutoff != 0 {
		where = append(where, "started_at >= ?")
		args = append(args, cutoff)
	}
	for _, ef := range exactFields {
		if v := ef.get(f); v != "" || ef.col == "owner_id" && f.ExactOwner {
			where = append(where, ef.col+" = ?")
			args = append(args, v)
		}
	}
	if f.From > 0 {
		where = append(where, "started_at >= ?")
		args = append(args, f.From)
	}
	if f.To > 0 {
		where = append(where, "started_at <= ?")
		args = append(args, f.To)
	}
	if f.Query != "" {
		// SQLite LIKE is case-insensitive for ASCII, matching the
		// case-folded substring search in matches for metadata values.
		var ors []string
		pat := "%" + likeEscape(f.Query) + "%"
		for _, qf := range queryFields {
			ors = append(ors, qf.col+` LIKE ? ESCAPE '\'`)
			args = append(args, pat)
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}
	if len(where) == 0 {
		return "1=1", args
	}
	return strings.Join(where, " AND "), args
}

// --- keyset cursor ------------------------------------------------------------

func encodeCursor(startedAt int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte("v1:" + strconv.FormatInt(startedAt, 10) + ":" + id))
}

func decodeCursor(s string) (int64, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, "", ErrInvalidCursor
	}
	parts := strings.SplitN(string(raw), ":", 3)
	if len(parts) != 3 || parts[0] != "v1" {
		return 0, "", ErrInvalidCursor
	}
	ts, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || ts <= 0 {
		return 0, "", ErrInvalidCursor
	}
	id := parts[2]
	if id == "" || len(id) > 64 {
		return 0, "", ErrInvalidCursor
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return 0, "", ErrInvalidCursor
		}
	}
	return ts, id, nil
}

func keysetBefore(c Call, ts int64, id string) bool {
	// Ordering is started_at DESC, id DESC: a row belongs to later pages
	// when it sorts strictly after the cursor position.
	return c.StartedAt < ts || (c.StartedAt == ts && c.ID < id)
}

// --- SQL backend --------------------------------------------------------------

func (s *Store) sqlQuery(ctx context.Context, f Filter) ([]Call, string, error) {
	where, args := s.buildWhere(&f, s.retentionCutoff(time.Now()))
	if f.Before != "" {
		ts, id, err := decodeCursor(f.Before)
		if err != nil {
			return nil, "", err
		}
		where += " AND (started_at < ? OR (started_at = ? AND id < ?))"
		args = append(args, ts, ts, id)
	}
	args = append(args, f.Limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT `+callColumns+` FROM `+storeTable+
		` WHERE `+where+` ORDER BY started_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	calls := []Call{}
	for rows.Next() {
		c, err := scanCall(rows)
		if err != nil {
			return nil, "", err
		}
		calls = append(calls, c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(calls) > f.Limit {
		calls = calls[:f.Limit]
		last := calls[len(calls)-1]
		next = encodeCursor(last.StartedAt, last.ID)
	}
	return calls, next, nil
}

func (s *Store) sqlStats(ctx context.Context, f Filter, st *Stats) error {
	// Counts and percentile ranks must observe one SQLite snapshot. Otherwise
	// a concurrent completion or retention sweep can change the sample count
	// between queries, producing contradictory totals or an out-of-range rank.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	where, args := s.buildWhere(&f, s.retentionCutoff(time.Now()))
	row := tx.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(status = 'running'), 0),
		COALESCE(SUM(status = 'ok'), 0),
		COALESCE(SUM(status = 'error' OR status = 'timeout'), 0),
		COALESCE(SUM(status = 'cancelled'), 0),
		COALESCE(SUM(status = 'timeout'), 0),
		COALESCE(SUM(status = 'interrupted'), 0),
		COALESCE(SUM(direction = 'inbound'), 0),
		COALESCE(SUM(direction = 'outbound'), 0)
		FROM `+storeTable+` WHERE `+where, args...)
	if err := row.Scan(&st.Total, &st.Running, &st.OK, &st.Errors, &st.Cancelled,
		&st.Timeouts, &st.Interrupted, &st.ByDirection.Inbound, &st.ByDirection.Outbound); err != nil {
		return err
	}
	st.ErrorRate = errorRate(st.OK, st.Errors)

	latWhere := where + ` AND status IN ('ok','error','timeout','cancelled')`
	var n int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+storeTable+
		` WHERE `+latWhere, args...).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		var max float64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(duration_ms), 0) FROM `+
			storeTable+` WHERE `+latWhere, args...).Scan(&max); err != nil {
			return err
		}
		st.MaxMs = &max
		p50, err := s.sqlPercentile(ctx, tx, latWhere, args, 0.50, n)
		if err != nil {
			return err
		}
		st.P50Ms = &p50
		p95, err := s.sqlPercentile(ctx, tx, latWhere, args, 0.95, n)
		if err != nil {
			return err
		}
		st.P95Ms = &p95
	}

	// Bounded groups: top 20 buckets by volume, one rank query per bucket
	// for p95 instead of loading the full history into RAM.
	rows, err := tx.QueryContext(ctx, `SELECT direction, server, tool, COUNT(*),
		COALESCE(SUM(status = 'error' OR status = 'timeout'), 0)
		FROM `+storeTable+` WHERE `+where+`
		GROUP BY direction, server, tool
		ORDER BY COUNT(*) DESC, direction, server, tool LIMIT 20`, args...)
	if err != nil {
		return err
	}
	type bucket struct {
		ts   ToolStat
		args []any
		n    int64
	}
	var buckets []bucket
	for rows.Next() {
		var b bucket
		if err := rows.Scan(&b.ts.Direction, &b.ts.Server, &b.ts.Tool, &b.ts.Total, &b.ts.Errors); err != nil {
			rows.Close()
			return err
		}
		buckets = append(buckets, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, b := range buckets {
		bWhere := where + ` AND direction = ? AND server = ? AND tool = ?
			AND status IN ('ok','error','timeout','cancelled')`
		bArgs := append(append([]any{}, args...), b.ts.Direction, b.ts.Server, b.ts.Tool)
		var n int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+storeTable+
			` WHERE `+bWhere, bArgs...).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			p95, err := s.sqlPercentile(ctx, tx, bWhere, bArgs, 0.95, n)
			if err != nil {
				return err
			}
			v := p95
			b.ts.P95Ms = &v
		}
		st.ByTool = append(st.ByTool, b.ts)
	}
	return nil
}

// sqlPercentile reads a single nearest-rank value; n is the sample count so
// the query fetches exactly one row regardless of history size.
func (s *Store) sqlPercentile(ctx context.Context, tx *sql.Tx, where string, args []any, p float64, n int64) (float64, error) {
	rank := int64(math.Ceil(p * float64(n)))
	if rank < 1 {
		rank = 1
	}
	var v float64
	err := tx.QueryRowContext(ctx, `SELECT duration_ms FROM `+storeTable+
		` WHERE `+where+` ORDER BY duration_ms LIMIT 1 OFFSET ?`,
		append(append([]any{}, args...), rank-1)...).Scan(&v)
	return v, err
}

// --- memory backend -----------------------------------------------------------

func (s *Store) memFilter(f *Filter) []Call {
	cutoff := s.retentionCutoff(time.Now())
	out := []Call{}
	for _, id := range s.memOrdr {
		c, ok := s.mem[id]
		if ok && s.matches(&c, f, cutoff) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt != out[j].StartedAt {
			return out[i].StartedAt > out[j].StartedAt
		}
		return out[i].ID > out[j].ID
	})
	return out
}

func (s *Store) memQuery(f Filter) ([]Call, string, error) {
	var ts int64
	var id string
	if f.Before != "" {
		var err error
		ts, id, err = decodeCursor(f.Before)
		if err != nil {
			return nil, "", err
		}
	}
	s.memMu.Lock()
	s.purgeExpiredLocked(time.Now())
	all := s.memFilter(&f)
	s.memMu.Unlock()
	calls := []Call{}
	for _, c := range all {
		if f.Before != "" && !keysetBefore(c, ts, id) {
			continue
		}
		calls = append(calls, c)
	}
	next := ""
	if len(calls) > f.Limit {
		calls = calls[:f.Limit]
		last := calls[len(calls)-1]
		next = encodeCursor(last.StartedAt, last.ID)
	}
	return calls, next, nil
}

func (s *Store) memStats(f Filter, st *Stats) error {
	s.memMu.Lock()
	s.purgeExpiredLocked(time.Now())
	all := s.memFilter(&f)
	s.memMu.Unlock()

	var durations []float64
	type key struct{ dir, server, tool string }
	groups := map[key]*ToolStat{}
	groupDur := map[key][]float64{}
	for i := range all {
		c := &all[i]
		st.Total++
		switch c.Status {
		case "running":
			st.Running++
		case "ok":
			st.OK++
		case "error":
			st.Errors++
		case "timeout":
			st.Errors++
			st.Timeouts++
		case "cancelled":
			st.Cancelled++
		case "interrupted":
			st.Interrupted++
		}
		switch c.Direction {
		case "inbound":
			st.ByDirection.Inbound++
		case "outbound":
			st.ByDirection.Outbound++
		}
		completed := c.Status == "ok" || c.Status == "error" ||
			c.Status == "timeout" || c.Status == "cancelled"
		if completed {
			durations = append(durations, c.DurationMs)
		}
		k := key{c.Direction, c.Server, c.Tool}
		g, ok := groups[k]
		if !ok {
			g = &ToolStat{Direction: c.Direction, Server: c.Server, Tool: c.Tool}
			groups[k] = g
		}
		g.Total++
		if c.Status == "error" || c.Status == "timeout" {
			g.Errors++
		}
		if completed {
			groupDur[k] = append(groupDur[k], c.DurationMs)
		}
	}
	st.ErrorRate = errorRate(st.OK, st.Errors)
	if len(durations) > 0 {
		sort.Float64s(durations)
		p50 := percentile(durations, 0.50)
		p95 := percentile(durations, 0.95)
		mx := durations[len(durations)-1]
		st.P50Ms, st.P95Ms, st.MaxMs = &p50, &p95, &mx
	}
	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := groups[keys[i]], groups[keys[j]]
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		if a.Direction != b.Direction {
			return a.Direction < b.Direction
		}
		if a.Server != b.Server {
			return a.Server < b.Server
		}
		return a.Tool < b.Tool
	})
	if len(keys) > 20 {
		keys = keys[:20]
	}
	for _, k := range keys {
		g := groups[k]
		if ds := groupDur[k]; len(ds) > 0 {
			sort.Float64s(ds)
			p95 := percentile(ds, 0.95)
			g.P95Ms = &p95
		}
		st.ByTool = append(st.ByTool, *g)
	}
	return nil
}

// percentile is the nearest-rank method shared by both backends so SQL and
// memory answers agree exactly.
func percentile(sorted []float64, p float64) float64 {
	rank := int(math.Ceil(p * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

func errorRate(ok, errs int64) *float64 {
	denom := ok + errs
	if denom == 0 {
		return nil
	}
	v := float64(errs) / float64(denom)
	return &v
}
