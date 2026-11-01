// Package observability owns the shared runtime that lets the web server
// and short-lived CLI processes record MCP and skill observations into one
// SQLite database without corrupting each other's in-flight rows.
//
// Multi-process safety is lease-based, never PID-based: every process
// registers a random instance id in a heartbeat table, stamps each row it
// writes with that id, and on startup recovers only rows whose recorder is
// closed, stale, or from before leases existed. A CLI starting while the
// web server holds running spans must leave those spans alone, and a killed
// process's spans must still be recoverable by the next opener — both rules
// fall out of comparing recorder_instance_id against live leases.
package observability

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/skillobserve"
)

const (
	leaseTable        = "observability_instances"
	statusRunning     = "running"
	statusInterrupted = "interrupted"
	errKindUnknown    = "unknown_outcome"
)

// Package variables (not constants) so tests can compress the lease clock.
var (
	heartbeatInterval = 10 * time.Second
	staleAfter        = 60 * time.Second
)

// Sink receives every recorded snapshot in addition to the local stores.
// Implementations (the OTLP exporter) must be nonblocking: Record is on the
// tool-call hot path. The runtime never closes a caller-supplied Sink.
type Sink interface {
	RecordMCP(mcpobserve.Call)
	RecordSkill(skillobserve.Event)
}

// Options configures a Runtime. DB is caller-owned and never closed by
// Close; nil selects bounded in-memory stores with no lease bookkeeping.
// DisableMCP/DisableSkill honour POGO_MCP_CALLS=0 / POGO_SKILL_CALLS=0.
type Options struct {
	DB           *sql.DB
	Ring         int
	MaxRows      int
	RetainDays   int
	DisableMCP   bool
	DisableSkill bool
	Sink         Sink
}

// Runtime bundles the two observation stores with this process's lease.
type Runtime struct {
	MCP    *mcpobserve.Store
	Skills *skillobserve.Store

	db         *sql.DB // caller-owned; never closed here
	sink       Sink
	instanceID string

	heartbeatErrors atomic.Int64
	hbStop          chan struct{}
	hbDone          chan struct{}
	hbCtx           context.Context
	hbCancel        context.CancelFunc
	closeOnce       sync.Once
}

// Open creates the runtime. With a DB it registers this process's instance,
// recovers running rows left by dead or legacy recorders, and starts the
// heartbeat. A lease/setup failure is returned: silently degrading would
// make the next opener recover this process's live spans.
func Open(opts Options) (*Runtime, error) {
	r := &Runtime{
		db:         opts.DB,
		sink:       opts.Sink,
		instanceID: mcpobserve.NewID(),
	}
	if !opts.DisableMCP {
		s, err := mcpobserve.NewStore(mcpobserve.StoreOptions{
			DB: opts.DB, Ring: opts.Ring, MaxRows: opts.MaxRows,
			RetainDays: opts.RetainDays, InstanceID: r.instanceID,
			SkipRecovery: true, // lease-aware recovery below replaces the flip
		})
		if err != nil {
			return nil, err
		}
		r.MCP = s
	}
	if !opts.DisableSkill {
		s, err := skillobserve.NewStore(skillobserve.StoreOptions{
			DB: opts.DB, Ring: opts.Ring, MaxRows: opts.MaxRows,
			RetainDays: opts.RetainDays, InstanceID: r.instanceID,
			SkipRecovery: true,
		})
		if err != nil {
			if r.MCP != nil {
				r.MCP.Close()
			}
			return nil, err
		}
		r.Skills = s
	}
	if opts.DB == nil {
		return r, nil // memory mode: no leases, no recovery
	}
	if r.MCP == nil && r.Skills == nil {
		r.db = nil
		return r, nil
	}
	if err := r.initLease(); err != nil {
		r.closeStores()
		return nil, err
	}
	if err := r.recoverForeign(); err != nil {
		r.closeStores()
		return nil, err
	}
	r.hbStop = make(chan struct{})
	r.hbDone = make(chan struct{})
	r.hbCtx, r.hbCancel = context.WithCancel(context.Background())
	go r.heartbeatLoop()
	return r, nil
}

// InstanceID identifies this process's recorder in the lease table and on
// every row it writes.
func (r *Runtime) InstanceID() string { return r.instanceID }

// HeartbeatErrors counts background lease refresh failures. They are
// surfaced, never fatal: a transient database hiccup must not break task
// execution.
func (r *Runtime) HeartbeatErrors() int64 { return r.heartbeatErrors.Load() }

// MCPRecorder fans each snapshot out to the local store and the optional
// sink. Nil when MCP observation is disabled.
func (r *Runtime) MCPRecorder() mcpobserve.Recorder {
	if r == nil || r.MCP == nil {
		return nil
	}
	return mcpFanout{store: r.MCP, sink: r.sink}
}

// SkillRecorder is the skill-side counterpart of MCPRecorder.
func (r *Runtime) SkillRecorder() skillobserve.Recorder {
	if r == nil || r.Skills == nil {
		return nil
	}
	return skillFanout{store: r.Skills, sink: r.sink}
}

// Context installs the metadata and both recorders on ctx, the same shape
// the web runner builds by hand. The caller still sets the trusted
// skillobserve source (caller/entrypoint) itself.
func (r *Runtime) Context(ctx context.Context, md mcpobserve.Metadata) context.Context {
	ctx = mcpobserve.WithMetadata(ctx, md)
	if rec := r.MCPRecorder(); rec != nil {
		ctx = mcpobserve.WithRecorder(ctx, rec)
	}
	if rec := r.SkillRecorder(); rec != nil {
		ctx = skillobserve.WithRecorder(ctx, rec)
	}
	return ctx
}

// Close drains and stops the stores first (joining their writer workers),
// then marks this instance's remaining running rows interrupted — only its
// own — and finally closes the lease. It never closes the caller-owned DB
// or Sink. Idempotent.
func (r *Runtime) Close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		r.closeStores()
		if r.hbStop != nil {
			close(r.hbStop)
			r.hbCancel()
			<-r.hbDone
		}
		if r.db == nil {
			return
		}
		now := time.Now().UnixMilli()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for _, table := range r.tables() {
			if _, err := r.db.ExecContext(ctx, `UPDATE `+table+` SET status = ?, error_kind = ?
				WHERE status = ? AND recorder_instance_id = ?`,
				statusInterrupted, errKindUnknown, statusRunning, r.instanceID); err != nil {
				r.heartbeatErrors.Add(1)
			}
		}
		if _, err := r.db.ExecContext(ctx, `UPDATE `+leaseTable+` SET closed_at = ? WHERE id = ?`,
			now, r.instanceID); err != nil {
			r.heartbeatErrors.Add(1)
		}
	})
}

func (r *Runtime) tables() []string {
	var tables []string
	if r.MCP != nil {
		tables = append(tables, "mcpobserve_calls")
	}
	if r.Skills != nil {
		tables = append(tables, "skillobserve_events")
	}
	return tables
}

func (r *Runtime) closeStores() {
	if r.MCP != nil {
		r.MCP.Close()
	}
	if r.Skills != nil {
		r.Skills.Close()
	}
}

// initLease creates the lease table and registers this process. The id is
// random per process start; the PID is recorded for diagnostics only and is
// never used for liveness.
func (r *Runtime) initLease() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+leaseTable+` (
		id TEXT PRIMARY KEY,
		pid INTEGER NOT NULL DEFAULT 0,
		started_at INTEGER NOT NULL,
		last_heartbeat INTEGER NOT NULL,
		closed_at INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	_, err := r.db.ExecContext(ctx, `INSERT INTO `+leaseTable+`
		(id, pid, started_at, last_heartbeat, closed_at) VALUES (?,?,?,?,0)`,
		r.instanceID, pidOf(), now, now)
	return err
}

// recoverForeign flips running rows whose recorder cannot complete them:
// legacy rows from before leases (empty recorder), rows from instances that
// closed, and rows from instances that stopped heartbeating. Running rows
// stamped by another live instance — the web server while this CLI starts —
// are left strictly alone.
func (r *Runtime) recoverForeign() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.recoverForeignContext(ctx)
}

func (r *Runtime) recoverForeignContext(ctx context.Context) error {
	staleBefore := time.Now().Add(-staleAfter).UnixMilli()
	for _, table := range r.tables() {
		if _, err := r.db.ExecContext(ctx, `UPDATE `+table+` SET status = ?, error_kind = ?
			WHERE status = ? AND recorder_instance_id != ? AND (
				recorder_instance_id = '' OR recorder_instance_id NOT IN (
					SELECT id FROM `+leaseTable+`
					WHERE closed_at = 0 AND last_heartbeat >= ?))`,
			statusInterrupted, errKindUnknown, statusRunning, r.instanceID,
			staleBefore); err != nil {
			return err
		}
	}
	// Keep at most 2,000 inactive leases. Live recorders are never removed;
	// recovered rows no longer need the expired lease for their outcome.
	_, err := r.db.ExecContext(ctx, `DELETE FROM `+leaseTable+`
		WHERE (closed_at > 0 OR last_heartbeat < ?) AND id NOT IN
		(SELECT id FROM `+leaseTable+` ORDER BY last_heartbeat DESC LIMIT 2000)`, staleBefore)
	return err
}

func (r *Runtime) heartbeatLoop() {
	defer close(r.hbDone)
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			ctx, cancel := context.WithTimeout(r.hbCtx, 5*time.Second)
			if _, err := r.db.ExecContext(ctx, `UPDATE `+leaseTable+` SET last_heartbeat = ?
				WHERE id = ?`, time.Now().UnixMilli(), r.instanceID); err != nil {
				r.heartbeatErrors.Add(1)
			} else if err := r.recoverForeignContext(ctx); err != nil {
				r.heartbeatErrors.Add(1)
			}
			cancel()
		case <-r.hbStop:
			return
		}
	}
}

type mcpFanout struct {
	store *mcpobserve.Store
	sink  Sink
}

func (f mcpFanout) Record(c mcpobserve.Call) {
	f.store.Record(c)
	if f.sink != nil {
		f.sink.RecordMCP(c)
	}
}

type skillFanout struct {
	store *skillobserve.Store
	sink  Sink
}

func (f skillFanout) Record(e skillobserve.Event) {
	f.store.Record(e)
	if f.sink != nil {
		f.sink.RecordSkill(e)
	}
}
