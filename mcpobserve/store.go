// Store persistence for MCP call metadata. The store keeps one row per call
// id: Start's running snapshot and End's completed snapshot upsert the same
// key, so a call is never counted twice.
//
// Recording is nonblocking: Record only touches in-memory state plus a
// bounded handoff channel. A single writer goroutine owns SQLite writes so
// tool latency never includes database I/O. Queue overflow and write
// failures are counted (dropped / write_errors) and surfaced on every Page
// and Stats answer instead of being silently swallowed.
//
// The *sql.DB is shared with and owned by the legacy web mcplog layer; the
// store never closes it. A nil DB selects the explicit, bounded in-memory
// mode, which answers with storage:"memory" and identical filter semantics.
package mcpobserve

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

const (
	storeTable         = "mcpobserve_calls"
	defaultRing        = 1000
	defaultMaxRows     = 200000
	defaultRetainDays  = 30
	defaultQueryLimit  = 100
	maxQueryLimit      = 500
	sweepInterval      = 5 * time.Minute
	closeDrainTimeout  = 5 * time.Second
	statusRunning      = "running"
	statusInterrupted  = "interrupted"
	errKindUnknownDone = "unknown_outcome"
)

// storeQueueSize is a package variable (not a const) so tests can shrink it
// to exercise the drop path deterministically.
var storeQueueSize = 4096

// StoreOptions configures a Store. DB is the shared database handle owned by
// the legacy mcplog layer; nil selects bounded in-memory mode.
//
// InstanceID stamps every written row with the recording process's lease id
// (hidden recorder_instance_id column), so a later opener can tell a live
// foreign writer from a dead one. SkipRecovery suppresses the startup flip
// of all running rows to interrupted; the observability runtime sets both
// and performs lease-aware recovery itself. The zero values preserve the
// legacy single-process behaviour exactly: no stamp, flip everything.
type StoreOptions struct {
	DB           *sql.DB
	Ring         int
	MaxRows      int
	RetainDays   int
	InstanceID   string
	SkipRecovery bool
}

type writeReq struct {
	call *Call
	ack  chan struct{} // Flush marker: closed after all prior writes complete
}

// Store records and serves MCP call metadata.
type Store struct {
	db         *sql.DB
	memory     bool
	retainDays int
	maxRows    int
	instanceID string

	dropped     atomic.Int64
	writeErrors atomic.Int64

	mu         sync.RWMutex // guards closed + channel handoff
	closed     bool
	ch         chan writeReq
	writerDone chan struct{}
	sweepStop  chan struct{}
	sweepDone  chan struct{}
	ioCtx      context.Context
	ioCancel   context.CancelFunc
	closeOnce  sync.Once

	memMu   sync.RWMutex
	mem     map[string]Call
	memOrdr []string // insertion order for bounded eviction
	memCap  int
}

// NewStore opens the store. With a DB it creates its own table (never
// touching the legacy mcp_calls rows), flips rows left running by a previous
// process to interrupted, and starts the async writer and retention sweeper.
// A schema or startup failure is returned, not hidden behind a memory
// fallback; memory mode is only entered when DB is explicitly nil.
func NewStore(opts StoreOptions) (*Store, error) {
	s := &Store{
		db:         opts.DB,
		memory:     opts.DB == nil,
		retainDays: opts.RetainDays,
		maxRows:    opts.MaxRows,
		memCap:     opts.Ring,
		instanceID: opts.InstanceID,
	}
	if s.retainDays < 0 {
		s.retainDays = defaultRetainDays
	}
	if s.maxRows <= 0 {
		s.maxRows = defaultMaxRows
	}
	if s.memCap <= 0 {
		s.memCap = defaultRing
	}
	if s.memory {
		s.mem = make(map[string]Call)
		return s, nil
	}
	s.ioCtx, s.ioCancel = context.WithCancel(context.Background())
	if err := s.initSchema(); err != nil {
		s.ioCancel()
		return nil, err
	}
	if err := s.migrateColumns(); err != nil {
		s.ioCancel()
		return nil, err
	}
	// Rows still running predate this process (the writer goroutine is not
	// started yet, so no new rows can exist): their completion is unknowable.
	// The shared-database runtime skips this global flip and recovers only
	// rows whose recorder lease is dead — another live process's spans are
	// not ours to interrupt.
	if !opts.SkipRecovery {
		if _, err := s.db.Exec(`UPDATE `+storeTable+` SET status = ?, error_kind = ?
			WHERE status = ?`, statusInterrupted, errKindUnknownDone, statusRunning); err != nil {
			s.ioCancel()
			return nil, err
		}
	}
	s.sweepOnce()
	s.ch = make(chan writeReq, storeQueueSize)
	s.writerDone = make(chan struct{})
	s.sweepStop = make(chan struct{})
	s.sweepDone = make(chan struct{})
	go s.writeLoop()
	go s.sweepLoop()
	return s, nil
}

func (s *Store) initSchema() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS ` + storeTable + ` (
		id TEXT PRIMARY KEY,
		trace_id TEXT NOT NULL DEFAULT '',
		parent_id TEXT NOT NULL DEFAULT '',
		direction TEXT NOT NULL DEFAULT '',
		server TEXT NOT NULL DEFAULT '',
		tool TEXT NOT NULL DEFAULT '',
		wire_tool TEXT NOT NULL DEFAULT '',
		transport TEXT NOT NULL DEFAULT '',
		client TEXT NOT NULL DEFAULT '',
		mcp_session_id TEXT NOT NULL DEFAULT '',
		request_id TEXT NOT NULL DEFAULT '',
		session_id TEXT NOT NULL DEFAULT '',
		run_id TEXT NOT NULL DEFAULT '',
		task_id TEXT NOT NULL DEFAULT '',
		owner_id TEXT NOT NULL DEFAULT '',
		tool_call_id TEXT NOT NULL DEFAULT '',
		started_at INTEGER NOT NULL,
		finished_at INTEGER NOT NULL DEFAULT 0,
		duration_ms REAL NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT '',
		error_kind TEXT NOT NULL DEFAULT '',
		span_id TEXT NOT NULL DEFAULT '',
		parent_span_id TEXT NOT NULL DEFAULT '',
		trace_flags TEXT NOT NULL DEFAULT '',
		client_id TEXT NOT NULL DEFAULT '',
		entrypoint TEXT NOT NULL DEFAULT '',
		recorder_instance_id TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_mcpobserve_calls_keyset
		ON ` + storeTable + ` (started_at DESC, id DESC);
	CREATE INDEX IF NOT EXISTS idx_mcpobserve_calls_status
		ON ` + storeTable + ` (status);
	CREATE INDEX IF NOT EXISTS idx_mcpobserve_calls_tool
		ON ` + storeTable + ` (direction, server, tool, started_at DESC, id DESC);
	CREATE INDEX IF NOT EXISTS idx_mcpobserve_calls_trace
		ON ` + storeTable + ` (trace_id, started_at DESC, id DESC);
	CREATE INDEX IF NOT EXISTS idx_mcpobserve_calls_session
		ON ` + storeTable + ` (session_id, started_at DESC, id DESC);`)
	return err
}

// addedColumns are the columns introduced after the first shipped schema.
// recorder_instance_id is hidden: written and used for lease-aware recovery,
// never selected back into a Call.
var addedColumns = []string{
	"span_id", "parent_span_id", "trace_flags", "client_id", "entrypoint",
	"recorder_instance_id", "agent_id", "agent_name",
	"error_summary", "error_code",
}

// migrateColumns additively upgrades a table created by an older build.
// Existing rows keep their data (the new columns default to ”), the id
// primary key is untouched, and the operation is idempotent.
func (s *Store) migrateColumns() error {
	rows, err := s.db.Query(`PRAGMA table_info(` + storeTable + `)`)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, col := range addedColumns {
		if existing[col] {
			continue
		}
		if _, err := s.db.Exec(`ALTER TABLE ` + storeTable +
			` ADD COLUMN ` + col + ` TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_mcpobserve_calls_agent_owner ON ` + storeTable + ` (agent_id, owner_id, started_at DESC, id DESC)`)
	return err
}

// Record ingests one call snapshot. It never blocks on database I/O: the
// handoff channel is bounded and overflow is counted, not waited on.
func (s *Store) Record(c Call) {
	if s == nil || c.ID == "" {
		return
	}
	c.ErrorSource = "" // historical detail provenance is never written back
	// Also protect direct Recorder callers, not only instrumented Span.End.
	if c.Status == "ok" || c.Status == "running" {
		c.ErrorSummary, c.ErrorCode = "", ""
	} else {
		d := SafeDiagnostic(Diagnostic{Summary: c.ErrorSummary, Code: c.ErrorCode})
		c.ErrorSummary, c.ErrorCode = d.Summary, d.Code
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		s.dropped.Add(1)
		return
	}
	if s.memory {
		s.memRecord(c)
		return
	}
	select {
	case s.ch <- writeReq{call: &c}:
	default:
		s.dropped.Add(1) // history loses this snapshot; the counter says so
	}
}

func (s *Store) memRecord(c Call) {
	s.memMu.Lock()
	defer s.memMu.Unlock()
	s.purgeExpiredLocked(time.Now())
	if _, ok := s.mem[c.ID]; !ok {
		s.memOrdr = append(s.memOrdr, c.ID)
	}
	s.mem[c.ID] = c
	for len(s.memOrdr) > s.memCap {
		delete(s.mem, s.memOrdr[0])
		s.memOrdr = s.memOrdr[1:]
	}
}

// purgeExpiredLocked applies the retention window to memory mode so answers
// stay consistent with SQL mode between sweeps.
func (s *Store) purgeExpiredLocked(now time.Time) {
	cutoff := s.retentionCutoff(now)
	if cutoff == 0 {
		return
	}
	kept := s.memOrdr[:0]
	for _, id := range s.memOrdr {
		if c, ok := s.mem[id]; ok && c.StartedAt < cutoff {
			delete(s.mem, id)
			continue
		}
		kept = append(kept, id)
	}
	s.memOrdr = kept
}

func (s *Store) retentionCutoff(now time.Time) int64 {
	if s.retainDays <= 0 {
		return 0
	}
	return now.Add(-time.Duration(s.retainDays) * 24 * time.Hour).UnixMilli()
}

// Flush waits until every snapshot queued before the call has been written.
// Memory mode has nothing to flush.
func (s *Store) Flush(ctx context.Context) error {
	if s == nil || s.memory {
		return nil
	}
	ack := make(chan struct{})
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil // Close already drained the queue
	}
	select {
	case s.ch <- writeReq{ack: ack}:
		s.mu.RUnlock()
	case <-ctx.Done():
		s.mu.RUnlock()
		return ctx.Err()
	}
	select {
	case <-ack:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops intake, drains queued writes with a bounded wait, and halts the
// sweeper. It is idempotent and never closes the shared DB.
func (s *Store) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		if s.ch != nil {
			close(s.ch)
		}
		if s.sweepStop != nil {
			close(s.sweepStop)
		}
		s.mu.Unlock()
		if s.writerDone != nil {
			select {
			case <-s.writerDone:
			case <-time.After(closeDrainTimeout):
			}
			s.ioCancel() // also interrupts a blocked shared-DB connection wait
			<-s.writerDone
			<-s.sweepDone
		}
	})
}

func (s *Store) writeLoop() {
	defer close(s.writerDone)
	for req := range s.ch {
		if req.ack != nil {
			close(req.ack)
			continue
		}
		if s.ioCtx.Err() != nil {
			s.dropped.Add(1)
			continue
		}
		if err := s.upsert(*req.call); err != nil {
			s.writeErrors.Add(1)
		}
	}
}

func (s *Store) upsert(c Call) error {
	_, err := s.db.ExecContext(s.ioCtx, `INSERT INTO `+storeTable+` (
		id, trace_id, parent_id, direction, server, tool, wire_tool, transport,
		client, mcp_session_id, request_id, session_id, run_id, task_id,
		owner_id, tool_call_id, started_at, finished_at, duration_ms, status,
		error_kind, span_id, parent_span_id, trace_flags, client_id, entrypoint,
		recorder_instance_id, agent_id, agent_name, error_summary, error_code
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(id) DO UPDATE SET
		trace_id=excluded.trace_id, parent_id=excluded.parent_id,
		direction=excluded.direction, server=excluded.server, tool=excluded.tool,
		wire_tool=excluded.wire_tool, transport=excluded.transport,
		client=excluded.client, mcp_session_id=excluded.mcp_session_id,
		request_id=excluded.request_id, session_id=excluded.session_id,
		run_id=excluded.run_id, task_id=excluded.task_id, owner_id=excluded.owner_id,
		tool_call_id=excluded.tool_call_id, started_at=excluded.started_at,
		finished_at=excluded.finished_at, duration_ms=excluded.duration_ms,
		status=excluded.status, error_kind=excluded.error_kind,
		span_id=excluded.span_id, parent_span_id=excluded.parent_span_id,
		trace_flags=excluded.trace_flags, client_id=excluded.client_id,
		entrypoint=excluded.entrypoint,
		recorder_instance_id=excluded.recorder_instance_id,
		agent_id=excluded.agent_id, agent_name=excluded.agent_name,
		error_summary=excluded.error_summary, error_code=excluded.error_code`,
		c.ID, c.TraceID, c.ParentID, c.Direction, c.Server, c.Tool, c.WireTool,
		c.Transport, c.Client, c.MCPSessionID, c.RequestID, c.SessionID, c.RunID,
		c.TaskID, c.OwnerID, c.ToolCallID, c.StartedAt, c.FinishedAt,
		c.DurationMs, c.Status, c.ErrorKind, c.SpanID, c.ParentSpanID,
		c.TraceFlags, c.ClientID, c.Entrypoint, s.instanceID, c.AgentID, c.AgentName,
		c.ErrorSummary, c.ErrorCode)
	return err
}

// sweepOnce honors the retention window and row cap on our own table only.
// Sweep failures are counted as write errors rather than dropped silently.
func (s *Store) sweepOnce() {
	if s.memory || s.db == nil {
		return
	}
	if cutoff := s.retentionCutoff(time.Now()); cutoff != 0 {
		if _, err := s.db.ExecContext(s.ioCtx, `DELETE FROM `+storeTable+` WHERE started_at < ?`, cutoff); err != nil {
			s.writeErrors.Add(1)
		}
	}
	if s.maxRows > 0 {
		if _, err := s.db.ExecContext(s.ioCtx, `DELETE FROM `+storeTable+` WHERE id NOT IN
			(SELECT id FROM `+storeTable+` ORDER BY started_at DESC, id DESC LIMIT ?)`,
			s.maxRows); err != nil {
			s.writeErrors.Add(1)
		}
	}
}

func (s *Store) sweepLoop() {
	defer close(s.sweepDone)
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.sweepOnce()
		case <-s.sweepStop:
			return
		}
	}
}

// ErrInvalidCursor marks a malformed pagination cursor so the HTTP layer can
// answer 400 instead of 500.
var ErrInvalidCursor = errors.New("mcpobserve: invalid cursor")
