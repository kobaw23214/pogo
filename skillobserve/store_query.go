// Query, detail, and aggregate reads for the store. List and stats share one
// filter implementation per backend so the numbers always describe exactly
// the events the list can show. Pagination is a keyset on (started_at, id)
// descending with an opaque, validated cursor.
//
// Stats keeps the three skill stages strictly separate: discoveries, loads,
// and executions are independent counts, and every execution outcome and
// latency metric below is computed from stage='execute' events only. There
// is deliberately no monolithic "skill success" number.
package skillobserve

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
// is a substring search over safe metadata only (identifiers, skill and tool
// names, error kind — never content or paths, which are never stored).
// From/To are UTC Unix milliseconds on started_at. Before is the opaque
// keyset cursor from a previous Page. Limit is bounded by the store.
type Filter struct {
	Stage       string
	Caller      string
	Entrypoint  string
	SkillID     string
	SkillName   string
	SkillSource string
	Mechanism   string
	Tool        string
	Status      string
	SessionID   string
	RunID       string
	TaskID      string
	TraceID     string
	OwnerID     string
	ClientID    string
	Query       string
	Before      string
	From        int64
	To          int64
	Limit       int
}

// Page is one window of the event list. Events is never nil; counters
// surface queue loss instead of hiding it.
type Page struct {
	Events      []Event `json:"events"`
	NextCursor  string  `json:"next_cursor"`
	Storage     string  `json:"storage"`
	Dropped     int64   `json:"dropped"`
	WriteErrors int64   `json:"write_errors"`
}

// SkillStat aggregates one skill bucket. Stage counts are kept apart;
// ExecutionErrors counts stage='execute' events with status error or
// timeout. P95Ms is nil when the skill has no completed execution samples.
type SkillStat struct {
	SkillID         string   `json:"skill_id"`
	SkillName       string   `json:"skill_name"`
	SkillSource     string   `json:"skill_source"`
	Discoveries     int64    `json:"discoveries"`
	Loads           int64    `json:"loads"`
	Executions      int64    `json:"executions"`
	ExecutionErrors int64    `json:"execution_errors"`
	P95Ms           *float64 `json:"p95_ms"`
}

// CallerCounts always carries all three keys, zero included. Caller is
// trusted execution-entry metadata, not an inference from tool names.
type CallerCounts struct {
	Internal int64 `json:"internal"`
	External int64 `json:"external"`
	Unknown  int64 `json:"unknown"`
}

// Stats aggregates every retained event matching the filter, independent of
// Limit/Before. Discoveries/Loads/Executions count all matching events per
// stage. Every Execution* field, ErrorRate, and the latency percentiles are
// computed from stage='execute' events only: with a stage=load filter they
// are intentionally 0/null. ExecutionErrors counts error+timeout;
// ErrorRate divides by ok+error+timeout only, so running, cancelled, and
// interrupted executions never masquerade as successes. Latency samples are
// completed executions (ok/error/timeout/cancelled); rate and latency
// fields are null without samples.
type Stats struct {
	Total                int64        `json:"total"`
	Discoveries          int64        `json:"discoveries"`
	Loads                int64        `json:"loads"`
	Executions           int64        `json:"executions"`
	ExecutionRunning     int64        `json:"execution_running"`
	ExecutionOK          int64        `json:"execution_ok"`
	ExecutionErrors      int64        `json:"execution_errors"`
	ExecutionCancelled   int64        `json:"execution_cancelled"`
	ExecutionTimeouts    int64        `json:"execution_timeouts"`
	ExecutionInterrupted int64        `json:"execution_interrupted"`
	ErrorRate            *float64     `json:"error_rate"`
	P50Ms                *float64     `json:"p50_ms"`
	P95Ms                *float64     `json:"p95_ms"`
	MaxMs                *float64     `json:"max_ms"`
	ByCaller             CallerCounts `json:"by_caller"`
	BySkill              []SkillStat  `json:"by_skill"`
	Storage              string       `json:"storage"`
	Dropped              int64        `json:"dropped"`
	WriteErrors          int64        `json:"write_errors"`
}

// completedExecution reports whether an execute-stage event counts as a
// latency sample.
func completedExecution(status string) bool {
	switch status {
	case "ok", "error", "timeout", "cancelled":
		return true
	}
	return false
}

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

// Query returns one page of events, newest first.
func (s *Store) Query(ctx context.Context, f Filter) (Page, error) {
	p := Page{Events: []Event{}, Storage: s.storageKind(),
		Dropped: s.dropped.Load(), WriteErrors: s.writeErrors.Load()}
	f.Limit = clampLimit(f.Limit)
	var (
		events []Event
		next   string
		err    error
	)
	if s.memory {
		events, next, err = s.memQuery(f)
	} else {
		events, next, err = s.sqlQuery(ctx, f)
	}
	if err != nil {
		return p, err
	}
	p.Events, p.NextCursor = events, next
	return p, nil
}

// Detail returns one event by id, sql.ErrNoRows when unknown.
func (s *Store) Detail(ctx context.Context, id string) (*Event, error) {
	if s.memory {
		s.memMu.RLock()
		defer s.memMu.RUnlock()
		if e, ok := s.mem[id]; ok {
			cp := e
			return &cp, nil
		}
		return nil, sql.ErrNoRows
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+eventColumns+` FROM `+storeTable+` WHERE id = ?`, id)
	e, err := scanEvent(row)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// Stats aggregates all retained events matching the filter; Limit and Before
// are deliberately ignored so UI page size never skews the numbers.
func (s *Store) Stats(ctx context.Context, f Filter) (Stats, error) {
	st := Stats{BySkill: []SkillStat{}, Storage: s.storageKind(),
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

const eventColumns = `id, trace_id, parent_id, skill_id, skill_name,
	skill_source, stage, caller, entrypoint, mechanism, tool, wire_tool,
	session_id, run_id, task_id, owner_id, tool_call_id, started_at,
	finished_at, duration_ms, status, error_kind, span_id, parent_span_id,
	trace_flags, client_id`

type scannable interface{ Scan(dest ...any) error }

func scanEvent(row scannable) (Event, error) {
	var e Event
	err := row.Scan(&e.ID, &e.TraceID, &e.ParentID, &e.SkillID, &e.SkillName,
		&e.SkillSource, &e.Stage, &e.Caller, &e.Entrypoint, &e.Mechanism,
		&e.Tool, &e.WireTool, &e.SessionID, &e.RunID, &e.TaskID, &e.OwnerID,
		&e.ToolCallID, &e.StartedAt, &e.FinishedAt, &e.DurationMs, &e.Status,
		&e.ErrorKind, &e.SpanID, &e.ParentSpanID, &e.TraceFlags, &e.ClientID)
	return e, err
}

// queryFields are the safe metadata columns the substring search covers.
var queryFields = []struct {
	col  string
	read func(*Event) string
}{
	{"id", func(e *Event) string { return e.ID }},
	{"trace_id", func(e *Event) string { return e.TraceID }},
	{"parent_id", func(e *Event) string { return e.ParentID }},
	{"skill_id", func(e *Event) string { return e.SkillID }},
	{"skill_name", func(e *Event) string { return e.SkillName }},
	{"tool", func(e *Event) string { return e.Tool }},
	{"wire_tool", func(e *Event) string { return e.WireTool }},
	{"session_id", func(e *Event) string { return e.SessionID }},
	{"run_id", func(e *Event) string { return e.RunID }},
	{"task_id", func(e *Event) string { return e.TaskID }},
	{"owner_id", func(e *Event) string { return e.OwnerID }},
	{"tool_call_id", func(e *Event) string { return e.ToolCallID }},
	{"error_kind", func(e *Event) string { return e.ErrorKind }},
	{"client_id", func(e *Event) string { return e.ClientID }},
}

var exactFields = []struct {
	col  string
	get  func(*Filter) string
	read func(*Event) string
}{
	{"stage", func(f *Filter) string { return f.Stage }, func(e *Event) string { return e.Stage }},
	{"caller", func(f *Filter) string { return f.Caller }, func(e *Event) string { return e.Caller }},
	{"entrypoint", func(f *Filter) string { return f.Entrypoint }, func(e *Event) string { return e.Entrypoint }},
	{"skill_id", func(f *Filter) string { return f.SkillID }, func(e *Event) string { return e.SkillID }},
	{"skill_name", func(f *Filter) string { return f.SkillName }, func(e *Event) string { return e.SkillName }},
	{"skill_source", func(f *Filter) string { return f.SkillSource }, func(e *Event) string { return e.SkillSource }},
	{"mechanism", func(f *Filter) string { return f.Mechanism }, func(e *Event) string { return e.Mechanism }},
	{"tool", func(f *Filter) string { return f.Tool }, func(e *Event) string { return e.Tool }},
	{"status", func(f *Filter) string { return f.Status }, func(e *Event) string { return e.Status }},
	{"session_id", func(f *Filter) string { return f.SessionID }, func(e *Event) string { return e.SessionID }},
	{"run_id", func(f *Filter) string { return f.RunID }, func(e *Event) string { return e.RunID }},
	{"task_id", func(f *Filter) string { return f.TaskID }, func(e *Event) string { return e.TaskID }},
	{"trace_id", func(f *Filter) string { return f.TraceID }, func(e *Event) string { return e.TraceID }},
	{"owner_id", func(f *Filter) string { return f.OwnerID }, func(e *Event) string { return e.OwnerID }},
	{"client_id", func(f *Filter) string { return f.ClientID }, func(e *Event) string { return e.ClientID }},
}

// matches is the memory-mode predicate; buildWhere encodes the same rules in
// SQL. Both must change together — the parity test pins them.
func (s *Store) matches(e *Event, f *Filter, cutoff int64) bool {
	if cutoff != 0 && e.StartedAt < cutoff {
		return false
	}
	for _, ef := range exactFields {
		if v := ef.get(f); v != "" && ef.read(e) != v {
			return false
		}
	}
	if f.From > 0 && e.StartedAt < f.From {
		return false
	}
	if f.To > 0 && e.StartedAt > f.To {
		return false
	}
	if f.Query != "" {
		q := strings.ToLower(f.Query)
		found := false
		for _, qf := range queryFields {
			if strings.Contains(strings.ToLower(qf.read(e)), q) {
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
		if v := ef.get(f); v != "" {
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

func keysetBefore(e Event, ts int64, id string) bool {
	// Ordering is started_at DESC, id DESC: a row belongs to later pages
	// when it sorts strictly after the cursor position.
	return e.StartedAt < ts || (e.StartedAt == ts && e.ID < id)
}

// --- SQL backend --------------------------------------------------------------

func (s *Store) sqlQuery(ctx context.Context, f Filter) ([]Event, string, error) {
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
	rows, err := s.db.QueryContext(ctx, `SELECT `+eventColumns+` FROM `+storeTable+
		` WHERE `+where+` ORDER BY started_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, "", err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(events) > f.Limit {
		events = events[:f.Limit]
		last := events[len(events)-1]
		next = encodeCursor(last.StartedAt, last.ID)
	}
	return events, next, nil
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
		COALESCE(SUM(stage = 'discovery'), 0),
		COALESCE(SUM(stage = 'load'), 0),
		COALESCE(SUM(stage = 'execute'), 0),
		COALESCE(SUM(caller = 'internal'), 0),
		COALESCE(SUM(caller = 'external'), 0)
		FROM `+storeTable+` WHERE `+where, args...)
	if err := row.Scan(&st.Total, &st.Discoveries, &st.Loads, &st.Executions,
		&st.ByCaller.Internal, &st.ByCaller.External); err != nil {
		return err
	}
	st.ByCaller.Unknown = st.Total - st.ByCaller.Internal - st.ByCaller.External

	// Execution outcomes are stage='execute' only; other stages never leak
	// into success, error, or latency numbers.
	execWhere := where + ` AND stage = 'execute'`
	row = tx.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(status = 'running'), 0),
		COALESCE(SUM(status = 'ok'), 0),
		COALESCE(SUM(status = 'error' OR status = 'timeout'), 0),
		COALESCE(SUM(status = 'cancelled'), 0),
		COALESCE(SUM(status = 'timeout'), 0),
		COALESCE(SUM(status = 'interrupted'), 0)
		FROM `+storeTable+` WHERE `+execWhere, args...)
	if err := row.Scan(&st.ExecutionRunning, &st.ExecutionOK, &st.ExecutionErrors,
		&st.ExecutionCancelled, &st.ExecutionTimeouts, &st.ExecutionInterrupted); err != nil {
		return err
	}
	st.ErrorRate = errorRate(st.ExecutionOK, st.ExecutionErrors)

	latWhere := execWhere + ` AND status IN ('ok','error','timeout','cancelled')`
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

	// Bounded groups: top 20 skills by executions then volume, one rank
	// query per skill for execution p95 instead of loading full history.
	rows, err := tx.QueryContext(ctx, `SELECT skill_id, skill_name, skill_source,
		COALESCE(SUM(stage = 'discovery'), 0),
		COALESCE(SUM(stage = 'load'), 0),
		COALESCE(SUM(stage = 'execute'), 0),
		COALESCE(SUM(stage = 'execute' AND (status = 'error' OR status = 'timeout')), 0),
		COUNT(*)
		FROM `+storeTable+` WHERE `+where+`
		GROUP BY skill_id, skill_name, skill_source
		ORDER BY COALESCE(SUM(stage = 'execute'), 0) DESC, COUNT(*) DESC,
			skill_id, skill_name, skill_source LIMIT 20`, args...)
	if err != nil {
		return err
	}
	type bucket struct {
		ss SkillStat
	}
	var buckets []bucket
	for rows.Next() {
		var b bucket
		var total int64
		if err := rows.Scan(&b.ss.SkillID, &b.ss.SkillName, &b.ss.SkillSource,
			&b.ss.Discoveries, &b.ss.Loads, &b.ss.Executions,
			&b.ss.ExecutionErrors, &total); err != nil {
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
		bWhere := execWhere + ` AND skill_id = ? AND skill_name = ? AND skill_source = ?
			AND status IN ('ok','error','timeout','cancelled')`
		bArgs := append(append([]any{}, args...), b.ss.SkillID, b.ss.SkillName, b.ss.SkillSource)
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
			b.ss.P95Ms = &v
		}
		st.BySkill = append(st.BySkill, b.ss)
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

func (s *Store) memFilter(f *Filter) []Event {
	cutoff := s.retentionCutoff(time.Now())
	out := []Event{}
	for _, id := range s.memOrdr {
		e, ok := s.mem[id]
		if ok && s.matches(&e, f, cutoff) {
			out = append(out, e)
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

func (s *Store) memQuery(f Filter) ([]Event, string, error) {
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
	events := []Event{}
	for _, e := range all {
		if f.Before != "" && !keysetBefore(e, ts, id) {
			continue
		}
		events = append(events, e)
	}
	next := ""
	if len(events) > f.Limit {
		events = events[:f.Limit]
		last := events[len(events)-1]
		next = encodeCursor(last.StartedAt, last.ID)
	}
	return events, next, nil
}

func (s *Store) memStats(f Filter, st *Stats) error {
	s.memMu.Lock()
	s.purgeExpiredLocked(time.Now())
	all := s.memFilter(&f)
	s.memMu.Unlock()

	var durations []float64
	type key struct{ id, name, source string }
	groups := map[key]*SkillStat{}
	groupTotal := map[key]int64{}
	groupDur := map[key][]float64{}
	for i := range all {
		e := &all[i]
		st.Total++
		switch e.Stage {
		case "discovery":
			st.Discoveries++
		case "load":
			st.Loads++
		case "execute":
			st.Executions++
		}
		switch e.Caller {
		case "internal":
			st.ByCaller.Internal++
		case "external":
			st.ByCaller.External++
		default:
			st.ByCaller.Unknown++
		}
		if e.Stage == "execute" {
			switch e.Status {
			case "running":
				st.ExecutionRunning++
			case "ok":
				st.ExecutionOK++
			case "error":
				st.ExecutionErrors++
			case "timeout":
				st.ExecutionErrors++
				st.ExecutionTimeouts++
			case "cancelled":
				st.ExecutionCancelled++
			case "interrupted":
				st.ExecutionInterrupted++
			}
			if completedExecution(e.Status) {
				durations = append(durations, e.DurationMs)
			}
		}
		k := key{e.SkillID, e.SkillName, e.SkillSource}
		g, ok := groups[k]
		if !ok {
			g = &SkillStat{SkillID: e.SkillID, SkillName: e.SkillName, SkillSource: e.SkillSource}
			groups[k] = g
		}
		groupTotal[k]++
		switch e.Stage {
		case "discovery":
			g.Discoveries++
		case "load":
			g.Loads++
		case "execute":
			g.Executions++
			if e.Status == "error" || e.Status == "timeout" {
				g.ExecutionErrors++
			}
			if completedExecution(e.Status) {
				groupDur[k] = append(groupDur[k], e.DurationMs)
			}
		}
	}
	st.ErrorRate = errorRate(st.ExecutionOK, st.ExecutionErrors)
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
		if a.Executions != b.Executions {
			return a.Executions > b.Executions
		}
		if groupTotal[keys[i]] != groupTotal[keys[j]] {
			return groupTotal[keys[i]] > groupTotal[keys[j]]
		}
		if a.SkillID != b.SkillID {
			return a.SkillID < b.SkillID
		}
		if a.SkillName != b.SkillName {
			return a.SkillName < b.SkillName
		}
		return a.SkillSource < b.SkillSource
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
		st.BySkill = append(st.BySkill, *g)
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
