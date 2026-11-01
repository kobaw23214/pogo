// CLI observability wiring: the terminal process records MCP and skill
// observations into the same shared database the web server uses, through
// the observability runtime's instance lease. Nothing here prints to
// stdout (the JSON event stream's contract), changes tool permissions, or
// touches model execution: instrumentation rides the run context and the
// tool layer's existing trusted-injection points only.
//
// Skill identity always comes from the actually loaded skill list
// (skills.Load), projected exactly like the web layer does — a file that
// merely sits inside a skill directory, a bash call after reading a
// SKILL.md, or an external MCP tool with a skill-sounding name are all
// unowned operations and stay unrecorded.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/observability"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/skillobserve"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

// cliSinkHook lets the OTLP exporter integration attach a Sink without
// changing this wiring. Nil (the default) means local recording only.
var cliSinkHook func() observability.Sink

// cliSkillEntry is one loaded skill's observation record, mirroring the web
// layer's skillSnapshotEntry: the trusted identity, the canonical SKILL.md
// path used solely as the read tool's lookup key, and the hidden flag that
// keeps disable-model-invocation skills out of catalog discovery events.
type cliSkillEntry struct {
	identity skillobserve.Identity
	path     string
	hidden   bool
}

// cliObserver carries the shared runtime plus the loaded skill set's
// observation projection for one CLI process.
type cliObserver struct {
	rt        *observability.Runtime
	db        *sql.DB // owned here; the runtime never closes it
	entries   []cliSkillEntry
	inherited mcpobserve.Metadata
	sessionID string // canonical transcript ID, without its directory or extension
	exporter  *observability.OTLPExporter
}

// cliObserveDBPath resolves where observations persist: POGO_MCP_LOG_DB when
// configured, "none" for memory-only, otherwise the same mcplog.db beside
// the session dir the web server writes (serve.go).
func cliObserveDBPath() string {
	v := strings.TrimSpace(os.Getenv("POGO_MCP_LOG_DB"))
	if strings.EqualFold(v, "none") {
		return ""
	}
	if v != "" {
		return v
	}
	return filepath.Join(filepath.Dir(session.DefaultDir()), "mcplog.db")
}

func envInt(raw string, ok func(int) bool) (int, bool) {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || !ok(v) {
		return 0, false
	}
	return v, true
}

// openCLIObservability opens the shared runtime for this CLI process. It
// returns nil — with a bounded stderr diagnostic when initialization failed —
// rather than ever taking the CLI down: observation is additive. Env gates:
// POGO_CLI_CALLS=0 disables CLI instrumentation wholesale; POGO_MCP_CALLS=0
// and POGO_SKILL_CALLS=0 disable their halves.
func openCLIObservability(list []skills.Skill) *cliObserver {
	inherited := mcpobserve.ReadSubagentMetadata()
	if os.Getenv("POGO_CLI_CALLS") == "0" {
		return nil
	}
	disableMCP := os.Getenv("POGO_MCP_CALLS") == "0"
	disableSkill := os.Getenv("POGO_SKILL_CALLS") == "0"
	if disableMCP && disableSkill {
		return nil
	}
	var db *sql.DB
	if path := cliObserveDBPath(); path != "" {
		var err error
		if db, err = observability.OpenDatabase(path); err != nil {
			fmt.Fprintf(diagOut, "pogo: observation store unavailable (%s); using in-memory observation\n",
				boundedDiag(err))
			db = nil // bounded in-memory mode rather than no observation
		}
	}
	opts := observability.Options{
		DB:           db,
		DisableMCP:   disableMCP,
		DisableSkill: disableSkill,
	}
	if v, ok := envInt(os.Getenv("POGO_MCP_LOG_RING"), func(v int) bool { return v > 0 }); ok {
		opts.Ring = v
	}
	if v, ok := envInt(os.Getenv("POGO_MCP_LOG_MAX_ROWS"), func(v int) bool { return v > 0 }); ok {
		opts.MaxRows = v
	}
	// -1 lets the store apply its default retention; an explicit value
	// (0 = keep forever) is honored as-is, same as serve.go.
	opts.RetainDays = -1
	if v, ok := envInt(os.Getenv("POGO_MCP_LOG_RETAIN_DAYS"), func(v int) bool { return v >= 0 }); ok {
		opts.RetainDays = v
	}
	var exporter *observability.OTLPExporter
	if cliSinkHook != nil {
		opts.Sink = cliSinkHook()
	} else {
		exportOptions, err := observability.OTLPOptionsFromEnv(os.Getenv)
		if err == nil {
			exporter, err = observability.NewOTLPExporter(exportOptions)
		}
		if err != nil {
			fmt.Fprintln(diagOut, "pogo: invalid OTLP configuration; export disabled")
		}
		if exporter != nil {
			opts.Sink = exporter
		}
	}
	rt, err := observability.Open(opts)
	if err != nil {
		if exporter != nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = exporter.Shutdown(ctx)
			cancel()
		}
		if db != nil {
			db.Close()
		}
		fmt.Fprintf(diagOut, "pogo: observation disabled (%s)\n",
			boundedDiag(err))
		return nil
	}
	entries := make([]cliSkillEntry, 0, len(list))
	for _, s := range list {
		id := cliSkillIdentity(s)
		entries = append(entries, cliSkillEntry{
			identity: id, path: canonicalCLIPath(s.Path),
			hidden: s.DisableModelInvocation,
		})
	}
	return &cliObserver{rt: rt, db: db, entries: entries, exporter: exporter, inherited: inherited}
}

// Close drains the runtime (marking this process's unfinished spans
// interrupted, never a foreign process's) and then closes the CLI-owned DB
// handle.
func (o *cliObserver) Close() {
	if o == nil {
		return
	}
	o.rt.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = o.exporter.Shutdown(ctx)
	if o.db != nil {
		o.db.Close()
	}
}

func boundedDiag(err error) string {
	msg := err.Error()
	const max = 200
	if len(msg) > max {
		msg = msg[:max] + "…"
	}
	return msg
}

// canonicalCLIPath mirrors the web loader's canonicalisation so the identity
// hash and the read lookup agree with tools.MatchRead.
func canonicalCLIPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// cliSkillIdentity resolves a loaded skill's trusted identity. A skill
// without a SKILL.md path (or without a name) gets none, and none records
// nothing.
func cliSkillIdentity(sk skills.Skill) skillobserve.Identity {
	if sk.Path == "" || sk.Name == "" {
		return skillobserve.Identity{}
	}
	return skillobserve.Identify(sk.Name, sk.Source, canonicalCLIPath(sk.Path))
}

// readTargets projects the loaded skills into the narrow mapping the tool
// registry gets — the only bridge between skills knowledge and the registry.
func (o *cliObserver) readTargets() []tools.SkillReadTarget {
	if len(o.entries) == 0 {
		return nil
	}
	out := make([]tools.SkillReadTarget, 0, len(o.entries))
	for _, e := range o.entries {
		if e.identity.ID == "" || e.path == "" {
			continue
		}
		out = append(out, tools.SkillReadTarget{Identity: e.identity, Path: e.path})
	}
	return out
}

// runContext builds the observation context for one actual run: a fresh
// local run ID, the real session ID, the cli entrypoint, and both recorders.
// traceID continues an explicit skill load's trace when non-empty, so the
// instruction read and the run it produced share one trace; otherwise the
// run starts a fresh trace.
func (o *cliObserver) runContext(parent context.Context, traceID string) (context.Context, string) {
	runID := mcpobserve.NewID()
	if traceID == "" && o.inherited.TraceID == "" {
		traceID = mcpobserve.NewID()
	}
	md := o.inherited
	if md.TraceID == "" || traceID != "" {
		md.TraceID = traceID
	}
	md.SessionID, md.RunID, md.Entrypoint, md.ToolCallID = o.sessionID, runID, "cli", ""
	ctx := o.rt.Context(parent, md)
	return skillobserve.WithSource(ctx, "internal", "cli"), runID
}

// recordDiscovery emits one catalog event per model-visible skill of the
// loaded set, in the run's context, so every event carries the run's
// authoritative session/run/trace. Hidden skills are not model-visible and
// record nothing; a run with no skills records nothing either.
func (o *cliObserver) recordDiscovery(ctx context.Context) {
	for _, e := range o.entries {
		if e.hidden || e.identity.ID == "" {
			continue
		}
		_, span := skillobserve.Start(ctx, e.identity, "discovery", "catalog", "", "")
		span.End("ok", "")
	}
}

// loadSkillExplicit measures the actual instruction read for /skill:name.
// The returned trace id is handed to the run the expansion feeds, so the
// load and that run share one trace without pretending the read spans the
// whole run.
func (o *cliObserver) loadSkillExplicit(sk skills.Skill, extra string) (expanded, traceID string, err error) {
	ctx := mcpobserve.WithMetadata(context.Background(), mcpobserve.Metadata{
		SessionID: o.sessionID, Entrypoint: "cli",
	})
	ctx = skillobserve.WithRecorder(ctx, o.rt.SkillRecorder())
	ctx = skillobserve.WithSource(ctx, "internal", "cli")
	ctx, span := skillobserve.Start(ctx, cliSkillIdentity(sk), "load", "explicit", "", "")
	defer span.End("interrupted", "unknown_outcome")
	expanded, err = skills.Invocation(sk, extra)
	if err != nil {
		span.End("error", "read_error")
	} else {
		span.End("ok", "")
	}
	return expanded, mcpobserve.MetadataFrom(ctx).TraceID, err
}
