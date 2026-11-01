// MCP request logging: one record per HTTP request that touches the MCP
// endpoints — every verdict, both transports, both directions.
//
// Why a separate layer when mcpLayer.audit already exists: the audit lines are
// printf noise in docker logs covering tool-layer events only. A protocol-level
// rejection (the 400 a misconfigured remote platform gets) never reaches a
// tool, so it leaves no trace at all — and the SDK's rejection bodies are
// self-explanatory ("Accept must contain both 'application/json' and
// 'text/event-stream'"), so capturing them turns every 400 into a diagnosis.
//
// Design (see MCP-日志方案.md, sources cited there):
//   - Middleware wraps the two transport handlers where the mux mounts them
//     (/mcp → "streamable", /sse → "sse"; the legacy transport POSTs back to
//     its own path, so one wrapper per path covers both directions).
//   - Auth-layer rejections (401/404-disabled/503-misconfig) happen in
//     withToken *before* the mux, so those are recorded via recordGate
//     from that branch — otherwise "any request" would silently mean "any
//     request that passed the gate".
//   - Storage: in-memory ring feeds the UI's live view; SQLite (modernc,
//     already a direct dependency) keeps history for filtering/aggregates.
//     The write path is async and drop-counted: observability must never take
//     down (or slow down) the surface it observes.
//   - Payloads are truncated and secret-redacted by default, in both
//     directions. PayloadOff suppresses the raw text of BOTH the request and
//     the response: an error.message can echo caller-supplied data, so it is
//     not "the server's own words". Classification (verdict/stage/diag_code
//     plus the protocol/tool outcome) is computed in memory before the
//     payload is discarded, so diagnosis survives with payloads off.
//   - Verdict/stage/diag_code are machine codes; the UI maps them to localized
//     text, matching how the rest of the interface stays bilingual.
package web

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/observability"
)

// Verdicts: the coarse "was this normal" answer the list view groups by.
const (
	mcpVerdictOK       = "ok"       // 2xx with no observed protocol/tool error
	mcpVerdictRejected = "rejected" // 4xx — the client's request was wrong
	mcpVerdictError    = "error"    // 5xx or an observed protocol/tool error
)

// Stages: which ring of the funnel the request died in (empty when ok).
const (
	mcpStageAuth    = "auth"    // token gate (withToken)
	mcpStageHeaders = "headers" // Accept / Content-Type / method shape
	mcpStageProto   = "proto"   // protocolVersion negotiation
	mcpStageBody    = "body"    // JSON-RPC parse / validation
	mcpStageSession = "session" // session id handling
	mcpStageHandler = "handler" // past the SDK, our handler errored
)

// Diag codes (machine-stable; the UI's i18n maps them to problem + fix text).
// Each corresponds to a rejection the go-sdk actually emits — the string
// anchors below were verified against go-sdk v1.7.0 mcp/streamable.go.
const (
	mcpDiagAuthFailed           = "auth_failed"               // 401: bad or missing token
	mcpDiagDisabled             = "mcp_disabled"              // 404: file channel says enabled:false
	mcpDiagAuthMisconfigured    = "auth_misconfigured"        // 503: token_env names a missing variable
	mcpDiagAcceptHeaderMissing  = "accept_header_missing"     // 400: Accept lacks both media types
	mcpDiagAcceptHeaderGet      = "accept_header_get_missing" // 400: GET stream without text/event-stream
	mcpDiagProtocolVersion      = "protocol_version"          // 400: unsupported protocolVersion
	mcpDiagContentType          = "content_type"              // 415: Content-Type must be application/json
	mcpDiagMethodNotAllowed     = "method_not_allowed"        // 405
	mcpDiagInvalidJSON          = "invalid_json"              // 400: body did not parse (has line/col)
	mcpDiagMissingSessionHeader = "missing_session_header"    // 400: DELETE without Mcp-Session-Id
	mcpDiagMissingSessionQuery  = "missing_session_query"     // 400: legacy POST-back without sessionid
	mcpDiagGetWithoutSession    = "get_without_session"       // 405: push-stream GET before initialize (our shim)
	mcpDiagBodyTooLarge         = "body_too_large"            // 413
	mcpDiagHandlerError         = "handler_error"             // any 5xx body
	mcpDiagUnknownRejection     = "unknown_rejection"         // 4xx we could not classify
	mcpDiagJSONRPCError         = "jsonrpc_error"             // 2xx transport carrying a JSON-RPC error object
	mcpDiagToolError            = "tool_error"                // 2xx tools/call result with isError:true
)

// Semantic outcomes: what the protocol/tool layer answered, independent of
// the HTTP status. "unknown" is the honest answer when the captured head is
// truncated or holds no response for the request's id — the log never
// guesses a success it cannot see.
const (
	mcpOutcomeOK      = "ok"
	mcpOutcomeError   = "error"
	mcpOutcomeUnknown = "unknown"
)

// Caps. req_head exists to recognise a request, not to replay it; resp_body
// is the first bytes of every response (success included), capped so a long
// SSE stream only costs its opening frames. The read cap is what the
// middleware is willing to buffer to parse JSON-RPC fields out of a live
// request body — beyond it, the entry is still recorded, just probed from the
// first bytes and marked truncated.
const (
	mcpLogReadCap  = 256 << 10
	mcpLogHeadCap  = 2048
	mcpLogRespCap  = 4096
	mcpLogRingDef  = 1000
	mcpLogRowsDef  = 200000
	mcpLogDaysDef  = 30
	mcpLogChanSize = 256
)

// MCPCallLog is one recorded MCP endpoint hit. Field names double as the UI's
// vocabulary; diag_code is translated client-side.
type MCPCallLog struct {
	ID    int64  `json:"id,omitempty"`
	Ts    int64  `json:"ts"` // unix milliseconds
	ReqID string `json:"req_id"`

	Transport  string `json:"transport"` // "streamable" | "sse"
	HTTPMethod string `json:"http_method"`

	JSONRPCMethod   string `json:"jsonrpc_method,omitempty"` // initialize, tools/call…
	Tool            string `json:"tool,omitempty"`           // params.name for tools/call
	SessionID       string `json:"session_id,omitempty"`
	ProtocolVersion string `json:"protocol_version,omitempty"`
	ClientName      string `json:"client_name,omitempty"`

	Status     int     `json:"status"`
	DurationMs float64 `json:"duration_ms"`
	ReqBytes   int     `json:"req_bytes,omitempty"`
	// ReqHead is the redacted, truncated request body (empty when payload
	// logging is off or the request had none).
	ReqHead string `json:"req_head,omitempty"`
	// RespBody is the first bytes of the response body, every status — the
	// "show me what actually came back" field. Streams (SSE responses) stop
	// at the cap after the first frames, so a wait's progress lines show up
	// without buffering a 30-minute stream. RespTruncated marks the cut.
	RespBody      string `json:"resp_body,omitempty"`
	RespTruncated bool   `json:"resp_truncated,omitempty"`
	RemoteAddr    string `json:"remote_addr,omitempty"`

	// Verdict / Stage / DiagCode classify the outcome; see the constants.
	Verdict  string `json:"verdict"`
	Stage    string `json:"stage,omitempty"`
	DiagCode string `json:"diag_code,omitempty"`

	// ProtoOutcome is the JSON-RPC layer's answer ("ok" | "error" |
	// "unknown"), parsed from the captured response head; empty when the
	// request was not a JSON-RPC call expecting a response (notifications,
	// gate rejections, push streams).
	ProtoOutcome string `json:"proto_outcome,omitempty"`
	// ToolOutcome is the tool layer's answer for tools/call only: "ok" |
	// "error" (JSON-RPC error or isError:true) | "unknown" (unparseable or
	// truncated head). Empty for every other method — notifications and the
	// handshake are never counted as tool successes.
	ToolOutcome string `json:"tool_outcome,omitempty"`

	reqTruncated bool   `json:"-"`
	hasID        bool   `json:"-"` // request carried a JSON-RPC id (a call, not a notification)
	reqJSONID    string `json:"-"` // raw id, for matching SSE response frames
}

// diagnose maps (status, captured response body) to the three classification
// fields. The body anchors are prefixes/substrings of go-sdk error strings,
// deliberately matched on stable fragments rather than exact text.
func diagnose(status int, errText string) (verdict, stage, code string) {
	switch {
	case status >= 200 && status < 300:
		return mcpVerdictOK, "", ""
	case status == 401:
		return mcpVerdictRejected, mcpStageAuth, mcpDiagAuthFailed
	case status == 404:
		return mcpVerdictRejected, mcpStageAuth, mcpDiagDisabled
	case status == 503:
		return mcpVerdictError, mcpStageAuth, mcpDiagAuthMisconfigured
	case status >= 500:
		return mcpVerdictError, mcpStageHandler, mcpDiagHandlerError
	}
	// 4xx: classify by the SDK's own explanation when we captured one.
	// Order matters: specific anchors before generic ones (our 405 body also
	// contains "Method Not Allowed").
	switch {
	case strings.Contains(errText, "GET server-push stream requires an initialized session"),
		strings.Contains(errText, "GET requires an Mcp-Session-Id"):
		return mcpVerdictRejected, mcpStageSession, mcpDiagGetWithoutSession
	case strings.Contains(errText, "Accept must contain both"):
		return mcpVerdictRejected, mcpStageHeaders, mcpDiagAcceptHeaderMissing
	case strings.Contains(errText, "Accept must contain 'text/event-stream' for GET"):
		return mcpVerdictRejected, mcpStageHeaders, mcpDiagAcceptHeaderGet
	case strings.Contains(errText, "Unsupported protocol version"):
		return mcpVerdictRejected, mcpStageProto, mcpDiagProtocolVersion
	case strings.Contains(errText, "Content-Type must be"):
		return mcpVerdictRejected, mcpStageHeaders, mcpDiagContentType
	case strings.Contains(errText, "Method Not Allowed"):
		return mcpVerdictRejected, mcpStageHeaders, mcpDiagMethodNotAllowed
	case strings.Contains(errText, "DELETE requires an Mcp-Session-Id"):
		return mcpVerdictRejected, mcpStageSession, mcpDiagMissingSessionHeader
	case strings.Contains(errText, "GET server-push stream requires an initialized session"),
		strings.Contains(errText, "GET requires an Mcp-Session-Id"):
		return mcpVerdictRejected, mcpStageSession, mcpDiagGetWithoutSession
	case strings.Contains(errText, "sessionid must be provided"):
		return mcpVerdictRejected, mcpStageSession, mcpDiagMissingSessionQuery
	case strings.Contains(errText, "request body exceeds"):
		return mcpVerdictRejected, mcpStageBody, mcpDiagBodyTooLarge
	case strings.Contains(errText, "invalid character"),
		strings.Contains(errText, "unexpected end of JSON"),
		strings.Contains(errText, "cannot unmarshal"),
		strings.Contains(errText, "malformed payload"), // the SDK's JSON-RPC parse wrapper
		strings.Contains(errText, "looking for beginning of value"):
		return mcpVerdictRejected, mcpStageBody, mcpDiagInvalidJSON
	}
	return mcpVerdictRejected, mcpStageBody, mcpDiagUnknownRejection
}

// redactLogPayload masks secret-shaped substrings before anything is stored.
// Deliberately over-broad: a false positive costs a mangled log line, a false
// negative leaks a key into a database the browser UI renders. The set is
// finite on purpose — token shapes, Bearer strings, and JSON fields that
// conventionally carry credentials — so the operator can audit exactly what
// is masked.
var mcpLogRedactions = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)("(?:api[_-]?key|access[_-]?token|auth[_-]?token|secret|password|authorization)"\s*:\s*")(?:\\.|[^"\\])*(?:"|$)`), "${1}[redacted]\""},
	{regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`), "[redacted]"},
	{regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{8,}`), "[redacted]"},
	{regexp.MustCompile(`[A-Fa-f0-9]{24,}`), "[redacted]"},         // hex tokens, sha1/md5…
	{regexp.MustCompile(`[A-Za-z0-9_-]{40,}`), "[redacted]"},       // base64url-style keys
	{regexp.MustCompile(`[A-Za-z0-9+/]{40,}={0,2}`), "[redacted]"}, // base64-style keys
}

func redactLogPayload(s string) string {
	for _, r := range mcpLogRedactions {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

func truncateLog(s string, cap int) string {
	if len(s) <= cap {
		return s
	}
	return s[:cap] + "…"
}

func newMCPReqID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// --- store -------------------------------------------------------------------

// MCPLogOptions configures the request log. It sits on ServerOptions — not on
// MCPOptions — because MCP serving itself is configured live from mcp.json's
// server section (mcpserve.go), while log behaviour is a per-process decision
// the operator makes through POGO_MCP_LOG_* before the server ever starts.
// <=0 fields fall back to the defaults; DBPath empty means ring-only (tests,
// or the degraded mode a broken database degrades to).
type MCPLogOptions struct {
	Ring       int
	RetainDays int
	MaxRows    int
	PayloadOff bool
	// CallsOff disables the metadata-only execution monitor independently of
	// the legacy HTTP diagnostic log. The process switch is POGO_MCP_CALLS=0.
	CallsOff bool
	// SkillCallsOff disables skill operation metadata; POGO_SKILL_CALLS=0.
	SkillCallsOff bool
	DBPath        string
}

// mcpLogStore is the ring + async SQLite writer + retention sweeper. A nil
// store (MCP off) is valid: every method is a no-op or empty answer, same
// convention as the nil mcpLayer.
type mcpLogStore struct {
	ring []*MCPCallLog
	next int // next write slot
	mu   sync.RWMutex

	db         *sql.DB
	ch         chan *MCPCallLog
	done       chan struct{}
	dropped    atomic.Int64
	closed     bool // guarded by mu; includes the queue handoff
	writerDone chan struct{}
	sweepDone  chan struct{}
	ioCtx      context.Context
	ioCancel   context.CancelFunc

	retainDays int
	maxRows    int
	payloadOn  bool
	closeOnce  sync.Once
}

func newMCPLogStore(cfg MCPLogOptions, logger *log.Logger) (*mcpLogStore, error) {
	ringSize, retainDays, maxRows := cfg.Ring, cfg.RetainDays, cfg.MaxRows
	if ringSize <= 0 {
		ringSize = mcpLogRingDef
	}
	if maxRows <= 0 {
		maxRows = mcpLogRowsDef
	}
	if retainDays < 0 {
		retainDays = mcpLogDaysDef
	}
	s := &mcpLogStore{
		ring:       make([]*MCPCallLog, ringSize),
		retainDays: retainDays,
		maxRows:    maxRows,
		payloadOn:  !cfg.PayloadOff,
	}
	if cfg.DBPath == "" || strings.EqualFold(cfg.DBPath, "none") {
		return s, nil // ring only (tests, or explicit POGO_MCP_LOG_DB=none)
	}
	db, err := observability.OpenDatabase(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS mcp_calls (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ts INTEGER NOT NULL,
		req_id TEXT NOT NULL,
		transport TEXT NOT NULL,
		http_method TEXT NOT NULL,
		jsonrpc_method TEXT,
		tool TEXT,
		session_id TEXT,
		protocol_version TEXT,
		client_name TEXT,
		status INTEGER NOT NULL,
		duration_ms REAL NOT NULL,
		req_bytes INTEGER NOT NULL,
		req_head TEXT,
		resp_body TEXT,
		remote_addr TEXT,
		verdict TEXT NOT NULL,
		stage TEXT,
		diag_code TEXT,
		proto_outcome TEXT,
		tool_outcome TEXT
	); CREATE INDEX IF NOT EXISTS idx_mcp_calls_ts ON mcp_calls(ts);
	   CREATE INDEX IF NOT EXISTS idx_mcp_calls_verdict ON mcp_calls(verdict);`); err != nil {
		db.Close()
		return nil, err
	}
	// Migrations for databases created before later columns existed
	// (resp_body replaced error_text; the outcome pair landed with the
	// semantic classification): ALTER and ignore the duplicate-column error
	// newer databases raise.
	for _, col := range []string{"resp_body", "proto_outcome", "tool_outcome"} {
		if _, err := db.Exec(`ALTER TABLE mcp_calls ADD COLUMN ` + col + ` TEXT`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, err
		}
	}
	s.db = db
	s.ch = make(chan *MCPCallLog, mcpLogChanSize)
	s.done = make(chan struct{})
	s.writerDone = make(chan struct{})
	s.sweepDone = make(chan struct{})
	s.ioCtx, s.ioCancel = context.WithCancel(context.Background())
	go s.writeLoop(logger)
	go s.sweepLoop(logger)
	return s, nil
}

func (s *mcpLogStore) close() {
	if s == nil || s.done == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.ch)
		close(s.done)
		s.mu.Unlock()
		// An empty queue does not mean the last SQL operation finished.
		// Wait for the writer, then cancel any blocked database work and
		// join both workers before releasing the shared database handle.
		select {
		case <-s.writerDone:
		case <-time.After(2 * time.Second):
		}
		s.ioCancel()
		<-s.writerDone
		<-s.sweepDone
		s.db.Close()
	})
}

// record is the single funnel: ring append + async db handoff.
func (s *mcpLogStore) record(e *MCPCallLog) {
	if s == nil {
		return
	}
	if e.Ts == 0 {
		e.Ts = time.Now().UnixMilli()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.dropped.Add(1)
		return
	}
	s.ring[s.next] = e
	s.next = (s.next + 1) % len(s.ring)
	if s.ch != nil {
		select {
		case s.ch <- e:
		default:
			s.dropped.Add(1) // ring still has it; history loses this one
		}
	}
}

// recordGate logs a rejection from withToken, which runs before the wrapped
// transport handlers and therefore outside their reach.
func (s *mcpLogStore) recordGate(r *http.Request, status int, code string) {
	if s == nil {
		return
	}
	verdict, stage, _ := diagnose(status, "")
	e := &MCPCallLog{
		Ts:         time.Now().UnixMilli(),
		ReqID:      newMCPReqID(),
		Transport:  transportOf(r.URL.Path),
		HTTPMethod: r.Method,
		Status:     status,
		RemoteAddr: r.RemoteAddr,
		Verdict:    verdict,
		Stage:      stage,
		DiagCode:   code,
	}
	s.record(e)
}

func transportOf(path string) string {
	if path == "/sse" {
		return "sse"
	}
	return "streamable"
}

// snapshot returns newest-first ring entries, optionally filtered.
func (s *mcpLogStore) snapshot(limit int, verdict, transport, q string) []*MCPCallLog {
	if s == nil || limit <= 0 {
		return nil
	}
	out := make([]*MCPCallLog, 0, limit)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := 0; i < len(s.ring) && len(out) < limit; i++ {
		idx := (s.next - 1 - i + len(s.ring)*2) % len(s.ring)
		e := s.ring[idx]
		if e == nil {
			break // ring not yet wrapped: first empty slot ends history
		}
		if verdict != "" && verdict != "all" && e.Verdict != verdict {
			continue
		}
		if transport != "" && e.Transport != transport {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.JSONRPCMethod+" "+e.Tool+" "+e.SessionID+" "+e.RespBody+" "+e.ReqHead), strings.ToLower(q)) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// clear empties the ring, and the history table too when scope is "all".
func (s *mcpLogStore) clear(scope string) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	for i := range s.ring {
		s.ring[i] = nil
	}
	s.next = 0
	s.mu.Unlock()
	n := 0
	if s.db != nil && scope == "all" {
		if res, err := s.db.Exec(`DELETE FROM mcp_calls`); err == nil {
			if n64, _ := res.RowsAffected(); n64 > 0 {
				n = int(n64)
			}
		}
	}
	return n
}

// stats aggregates for the header row. Ring answers "now", db answers "ever".
type mcpLogStats struct {
	RingLen     int              `json:"ring_len"`
	Total       int64            `json:"total"`
	ByVerdict   map[string]int64 `json:"by_verdict"`
	ByTransport map[string]int64 `json:"by_transport"`
	Rejected24h int64            `json:"rejected_24h"`
	Dropped     int64            `json:"dropped"`
}

func (s *mcpLogStore) stats() mcpLogStats {
	st := mcpLogStats{ByVerdict: map[string]int64{}, ByTransport: map[string]int64{}}
	if s == nil {
		return st
	}
	st.Dropped = s.dropped.Load()
	s.mu.RLock()
	for _, e := range s.ring {
		if e != nil {
			st.RingLen++
		}
	}
	s.mu.RUnlock()
	if s.db == nil {
		return st
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM mcp_calls`).Scan(&st.Total); err != nil {
		return st
	}
	rows, err := s.db.Query(`SELECT verdict, COUNT(*) FROM mcp_calls GROUP BY verdict`)
	if err == nil {
		for rows.Next() {
			var v string
			var n int64
			if rows.Scan(&v, &n) == nil {
				st.ByVerdict[v] = n
			}
		}
		rows.Close()
	}
	rows, err = s.db.Query(`SELECT transport, COUNT(*) FROM mcp_calls GROUP BY transport`)
	if err == nil {
		for rows.Next() {
			var v string
			var n int64
			if rows.Scan(&v, &n) == nil {
				st.ByTransport[v] = n
			}
		}
		rows.Close()
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM mcp_calls WHERE verdict != 'ok' AND ts > ?`,
		time.Now().Add(-24*time.Hour).UnixMilli()).Scan(&st.Rejected24h)
	return st
}

func (s *mcpLogStore) writeLoop(logger *log.Logger) {
	defer close(s.writerDone)
	for e := range s.ch {
		if s.ioCtx.Err() != nil {
			s.dropped.Add(1)
			continue
		}
		_, err := s.db.ExecContext(s.ioCtx, `INSERT INTO mcp_calls
				(ts, req_id, transport, http_method, jsonrpc_method, tool, session_id,
				 protocol_version, client_name, status, duration_ms, req_bytes,
				 req_head, resp_body, remote_addr, verdict, stage, diag_code,
				 proto_outcome, tool_outcome)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			e.Ts, e.ReqID, e.Transport, e.HTTPMethod, e.JSONRPCMethod, e.Tool, e.SessionID,
			e.ProtocolVersion, e.ClientName, e.Status, e.DurationMs, e.ReqBytes,
			e.ReqHead, e.RespBody, e.RemoteAddr, e.Verdict, e.Stage, e.DiagCode,
			e.ProtoOutcome, e.ToolOutcome)
		if err != nil && logger != nil {
			logger.Printf("mcplog: insert failed: %v", err)
		}
	}
}

// sweepLoop enforces the dual retention (days + rows), journald/lumberjack
// style. No VACUUM: with a row ceiling the freelist is reused by new inserts,
// so the file plateaus instead of growing.
func (s *mcpLogStore) sweepLoop(logger *log.Logger) {
	defer close(s.sweepDone)
	s.sweepOnce(logger)
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			s.sweepOnce(logger)
		}
	}
}

func (s *mcpLogStore) sweepOnce(logger *log.Logger) {
	if s.db == nil {
		return
	}
	if s.retainDays > 0 {
		if _, err := s.db.ExecContext(s.ioCtx, `DELETE FROM mcp_calls WHERE ts < ?`,
			time.Now().Add(-time.Duration(s.retainDays)*24*time.Hour).UnixMilli()); err != nil && logger != nil {
			logger.Printf("mcplog: day sweep failed: %v", err)
		}
	}
	if _, err := s.db.ExecContext(s.ioCtx, `DELETE FROM mcp_calls WHERE id NOT IN
		(SELECT id FROM mcp_calls ORDER BY id DESC LIMIT ?)`, s.maxRows); err != nil && logger != nil {
		logger.Printf("mcplog: row sweep failed: %v", err)
	}
}

// --- middleware ----------------------------------------------------------------

// mcpLogResponseWriter captures the status and the first bytes of the body
// for EVERY response — the panel shows what came back on success too, not
// only on rejection. Buffering stops at the cap (so long SSE streams pay a
// bounded cost and keep streaming untouched); Flush passes through so
// streaming semantics survive the wrapper.
type mcpLogResponseWriter struct {
	http.ResponseWriter
	status int
	buf    *bytes.Buffer
	capped bool
}

func (w *mcpLogResponseWriter) WriteHeader(code int) {
	w.status = code
	if w.buf == nil {
		w.buf = &bytes.Buffer{}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *mcpLogResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.buf == nil {
		w.buf = &bytes.Buffer{}
	}
	if !w.capped {
		if room := mcpLogRespCap - w.buf.Len(); room > 0 {
			if len(p) > room {
				w.buf.Write(p[:room])
				w.capped = true
			} else {
				w.buf.Write(p)
			}
		} else {
			w.capped = true
		}
	}
	return w.ResponseWriter.Write(p)
}

// Flush keeps SSE/streaming chunk delivery working through the wrapper.
func (w *mcpLogResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// samplingBody tees the request body into a bounded sample while passing
// every byte — and every read error — through to the handler untouched. The
// log layer samples; it never caps the request itself, so a tool call larger
// than mcpLogReadCap reaches the server intact. Close delegates to the
// original body.
type samplingBody struct {
	rc     io.ReadCloser
	sample bytes.Buffer
	n      int64
}

func (b *samplingBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		b.n += int64(n)
		if room := mcpLogReadCap - b.sample.Len(); room > 0 {
			m := min(n, room)
			b.sample.Write(p[:m])
		}
	}
	return n, err
}

func (b *samplingBody) Close() error { return b.rc.Close() }

// wrapTransport records every request handled by next. The /sse long-lived GET
// is logged once, when the stream closes — its per-message POST-backs are
// separate requests and each gets its own entry.
func (s *mcpLogStore) wrapTransport(transport string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := newMCPReqID()
		w.Header().Set("X-Mcp-Req-Id", reqID)
		// SDK sessions retain the connection context, so a context request ID
		// would incorrectly reuse the initialize/GET ID for later calls.
		// Streamable HTTP forwards each POST's Header through RequestExtra.
		// Legacy SSE does not: leave its execution request ID unknown.
		r.Header.Set(mcpObservationRequestHeader, reqID) // overwrite client claims
		r = r.WithContext(mcpobserve.WithRequestInfo(r.Context(), mcpobserve.RequestInfo{Transport: transport}))

		e := &MCPCallLog{
			ReqID:      reqID,
			Transport:  transport,
			HTTPMethod: r.Method,
			RemoteAddr: r.RemoteAddr,
		}
		if sid := r.Header.Get("Mcp-Session-Id"); sid != "" {
			e.SessionID = sid
		} else if sid := r.URL.Query().Get("sessionid"); sid != "" {
			e.SessionID = sid
		}

		var sb *samplingBody
		if r.Body != nil && (r.Method == http.MethodPost || r.Method == http.MethodPut) {
			sb = &samplingBody{rc: r.Body}
			r.Body = sb
		}

		rw := &mcpLogResponseWriter{ResponseWriter: w}
		next.ServeHTTP(rw, r)
		if rw.status == 0 {
			rw.status = http.StatusOK
		}
		e.Status = rw.status
		e.DurationMs = float64(time.Since(start).Microseconds()) / 1000.0

		if sb != nil {
			// Only observe bytes the handler consumed. Reading rejected or
			// unread bodies here could block on a slow sender after the
			// handler has already finished. net/http owns body cleanup.
			e.ReqBytes = int(sb.n)
			e.reqTruncated = sb.n > mcpLogReadCap
			probeJSONRPC(e, sb.sample.Bytes())
			if s != nil && s.payloadOn {
				e.ReqHead = truncateLog(redactLogPayload(sb.sample.String()), mcpLogHeadCap)
				if e.reqTruncated {
					e.ReqHead += "\n…(truncated)"
				}
			}
		}
		var respHead string
		if rw.buf != nil && rw.buf.Len() > 0 {
			respHead = rw.buf.String()
			// PayloadOff silences both directions: a response's
			// error.message can echo caller data, so it is stored only when
			// payload logging is on. Classification below works on the
			// in-memory head either way.
			if s != nil && s.payloadOn {
				e.RespBody = truncateLog(redactLogPayload(respHead), mcpLogRespCap)
				e.RespTruncated = rw.capped
			}
		}
		e.Verdict, e.Stage, e.DiagCode = diagnose(e.Status, respHead)
		if e.Status >= 200 && e.Status < 300 {
			e.ProtoOutcome, e.ToolOutcome = classifyResponse(e, rw.Header().Get("Content-Type"), respHead)
			// A 2xx carrying a JSON-RPC error or an isError tool result is
			// not a success; promote it so the list view and the verdict
			// aggregates stop counting it as one.
			switch {
			case e.ProtoOutcome == mcpOutcomeError:
				e.Verdict, e.Stage, e.DiagCode = mcpVerdictError, mcpStageHandler, mcpDiagJSONRPCError
			case e.ToolOutcome == mcpOutcomeError:
				e.Verdict, e.Stage, e.DiagCode = mcpVerdictError, mcpStageHandler, mcpDiagToolError
			}
		}
		s.record(e)
	})
}

// probeJSONRPC pulls the classification fields out of the sampled request
// body. Cheap on purpose: one unmarshal of an already-buffered head into a
// probe struct. A truncated sample fails to parse and simply yields no
// fields — the entry is still recorded, without pretending to know the call.
func probeJSONRPC(e *MCPCallLog, body []byte) {
	if len(body) == 0 {
		return
	}
	var p struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name            string `json:"name"`
			ProtocolVersion string `json:"protocolVersion"`
			ClientInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		} `json:"params"`
	}
	if json.Unmarshal(body, &p) != nil {
		// A truncated sample fails as a whole, but its opening keys are
		// usually intact — recover id/method/tool from the token stream so
		// an oversized call is still recognised for what it is.
		probeJSONRPCStream(e, body)
		return
	}
	e.hasID = len(p.ID) > 0 && string(p.ID) != "null"
	e.reqJSONID = string(p.ID)
	e.JSONRPCMethod = p.Method
	e.Tool = p.Params.Name
	e.ProtocolVersion = p.Params.ProtocolVersion
	e.ClientName = strings.TrimSpace(p.Params.ClientInfo.Name + " " + p.Params.ClientInfo.Version)
}

// probeJSONRPCStream is the truncated-sample fallback for probeJSONRPC: a
// sequential walk that keeps whatever it can extract before the sample runs
// out. Only the depth-1 id/method keys and params.name are recovered —
// partial data beats none, and nothing here is guessed.
func probeJSONRPCStream(e *MCPCallLog, body []byte) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return
	}
	skip := func() bool { // false when the sample ran out mid-value
		var raw json.RawMessage
		return dec.Decode(&raw) == nil
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return
		}
		key, _ := kt.(string)
		switch key {
		case "id":
			var id json.RawMessage
			if err := dec.Decode(&id); err != nil {
				return
			}
			e.hasID = len(id) > 0 && string(id) != "null"
			e.reqJSONID = string(id)
		case "method":
			var m string
			if err := dec.Decode(&m); err != nil {
				return
			}
			e.JSONRPCMethod = m
		case "params":
			t, err := dec.Token()
			if err != nil {
				return
			}
			if t != json.Delim('{') {
				if !skip() {
					return
				}
				continue
			}
			for dec.More() {
				pk, err := dec.Token()
				if err != nil {
					return
				}
				if k, _ := pk.(string); k == "name" && e.Tool == "" {
					var n string
					if err := dec.Decode(&n); err != nil {
						return
					}
					e.Tool = n
				} else if !skip() {
					return // typically the giant arguments value, cut off
				}
			}
			if _, err := dec.Token(); err != nil { // params closing '}'
				return
			}
		default:
			if !skip() {
				return
			}
		}
	}
}

// classifyResponse reads the JSON-RPC/tool outcome out of the captured
// response head, for both content types the streamable transport answers
// with. Only requests (messages with an id) get an outcome; notifications
// and push streams return empty. When the head holds no parseable answer —
// truncated, empty, or the matching response lives on a stream beyond the
// captured frames — the outcome is "unknown", never a guessed success.
func classifyResponse(e *MCPCallLog, contentType, head string) (proto, tool string) {
	if e.JSONRPCMethod == "" || !e.hasID {
		return "", ""
	}
	proto = mcpOutcomeUnknown
	if e.JSONRPCMethod == "tools/call" {
		tool = mcpOutcomeUnknown
	}
	contentType = strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch {
	case contentType == "application/json":
		return classifyJSONRPCPayload(e, []byte(head))
	case contentType == "text/event-stream":
		// SSE joins data lines within a complete, blank-line-delimited
		// event. Do not interpret an incomplete captured event as success.
		var data []string
		scan := bufio.NewScanner(strings.NewReader(head))
		for scan.Scan() {
			line := scan.Text()
			if line == "" {
				p, t := classifyJSONRPCPayload(e, []byte(strings.Join(data, "\n")))
				if p != mcpOutcomeUnknown {
					return p, t
				}
				data = nil
			} else if value, ok := strings.CutPrefix(line, "data:"); ok {
				data = append(data, strings.TrimPrefix(value, " "))
			}
		}
	}
	return proto, tool
}

// classifyJSONRPCPayload classifies one complete JSON-RPC response message.
func classifyJSONRPCPayload(e *MCPCallLog, payload []byte) (proto, tool string) {
	proto = mcpOutcomeUnknown
	if e.JSONRPCMethod == "tools/call" {
		tool = mcpOutcomeUnknown
	}
	var resp struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(payload, &resp); err != nil || resp.JSONRPC != "2.0" || resp.Method != "" || string(resp.ID) != e.reqJSONID {
		return proto, tool
	}
	if resp.Error != nil {
		if e.JSONRPCMethod == "tools/call" {
			return mcpOutcomeError, mcpOutcomeError // the call never produced a result
		}
		return mcpOutcomeError, ""
	}
	if len(resp.Result) == 0 {
		return proto, tool // neither error nor result: not an answer
	}
	if e.JSONRPCMethod != "tools/call" {
		return mcpOutcomeOK, "" // initialize, tools/list…: protocol success, no tool verdict
	}
	var res struct {
		IsError bool `json:"isError"`
	}
	if bytes.Equal(bytes.TrimSpace(resp.Result), []byte("null")) || json.Unmarshal(resp.Result, &res) != nil {
		return mcpOutcomeOK, mcpOutcomeUnknown
	}
	if res.IsError {
		return mcpOutcomeOK, mcpOutcomeError
	}
	return mcpOutcomeOK, mcpOutcomeOK
}

// --- API handlers -------------------------------------------------------------

func (s *Server) handleGetMCPLogs(w http.ResponseWriter, r *http.Request) {
	if s.mcplog == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "mcp logging is off (POGO_MCP not enabled)"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	entries := s.mcplog.snapshot(limit,
		r.URL.Query().Get("verdict"),
		r.URL.Query().Get("transport"),
		r.URL.Query().Get("q"))
	writeJSON(w, http.StatusOK, map[string]any{"logs": entries})
}

func (s *Server) handleClearMCPLogs(w http.ResponseWriter, r *http.Request) {
	if s.mcplog == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "mcp logging is off"})
		return
	}
	scope := r.URL.Query().Get("scope") // "screen" (default) | "all"
	removed := s.mcplog.clear(scope)
	writeJSON(w, http.StatusOK, map[string]any{"scope": scope, "history_removed": removed})
}

func (s *Server) handleMCPLogStats(w http.ResponseWriter, r *http.Request) {
	if s.mcplog == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "mcp logging is off"})
		return
	}
	writeJSON(w, http.StatusOK, s.mcplog.stats())
}

// ensure the sqlite driver is linked even when only this file references it.
var _ = os.Getenv
