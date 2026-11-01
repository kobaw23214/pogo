// Package mcpobserve records MCP execution metadata and bounded, redacted
// failure diagnostics. It never stores arguments, complete results or raw errors.
package mcpobserve

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

type Call struct {
	ID           string  `json:"id"`
	TraceID      string  `json:"trace_id"`
	ParentID     string  `json:"parent_id,omitempty"`
	SpanID       string  `json:"span_id"`
	ParentSpanID string  `json:"parent_span_id,omitempty"`
	TraceFlags   string  `json:"trace_flags"`
	ClientID     string  `json:"client_id,omitempty"`
	Entrypoint   string  `json:"entrypoint"`
	Direction    string  `json:"direction"`
	Server       string  `json:"server"`
	Tool         string  `json:"tool"`
	WireTool     string  `json:"wire_tool,omitempty"`
	Transport    string  `json:"transport,omitempty"`
	Client       string  `json:"client,omitempty"`
	MCPSessionID string  `json:"mcp_session_id,omitempty"`
	RequestID    string  `json:"request_id,omitempty"`
	SessionID    string  `json:"session_id,omitempty"`
	RunID        string  `json:"run_id,omitempty"`
	TaskID       string  `json:"task_id,omitempty"`
	OwnerID      string  `json:"owner_id,omitempty"`
	AgentID      string  `json:"agent_id,omitempty"`
	AgentName    string  `json:"agent_name,omitempty"`
	ToolCallID   string  `json:"tool_call_id,omitempty"`
	StartedAt    int64   `json:"started_at"`
	FinishedAt   int64   `json:"finished_at,omitempty"`
	DurationMs   float64 `json:"duration_ms"`
	Status       string  `json:"status"`
	ErrorKind    string  `json:"error_kind,omitempty"`
	ErrorSummary string  `json:"error_summary,omitempty"`
	ErrorCode    string  `json:"error_code,omitempty"`
	// ErrorSource is a detail-only read projection, never a persisted claim.
	ErrorSource string `json:"error_source,omitempty"`
}

// Recorder implementations must return promptly without database/network I/O.
type Recorder interface{ Record(Call) }

type Metadata struct {
	// Agent identity is supplied only by the bound local runtime, never model/tool arguments.
	AgentID, AgentName                                               string
	TraceID, ParentID, SessionID, RunID, TaskID, OwnerID, ToolCallID string
	// ParentSpanID/TraceFlags carry the W3C parent of the next span;
	// TraceState is context only and is NEVER stored in event tables.
	// ClientID is the authenticated local caller identity, distinct from
	// Call.Client (the remote's self-reported name).
	ParentSpanID, TraceFlags, TraceState, ClientID string
	// Entrypoint is the trusted run/request origin, shared with skill telemetry.
	// It is persisted with the event so CLI and Web calls can be filtered.
	Entrypoint string
}

type contextKey uint8

const (
	recorderKey contextKey = iota
	metadataKey
	requestKey
)

var ErrInvalidFilter = errors.New("invalid MCP observation filter")

type RequestInfo struct{ Transport, RequestID string }

func WithRequestInfo(ctx context.Context, info RequestInfo) context.Context {
	return context.WithValue(ctx, requestKey, info)
}

// WithRecorder installs a sink on this execution tree, never process-globally.
func WithRecorder(ctx context.Context, r Recorder) context.Context {
	return context.WithValue(ctx, recorderKey, recorderValue{r})
}

type recorderValue struct{ recorder Recorder }

// WithMetadata replaces metadata. Callers retain fields explicitly when
// continuing a trace, and obtain ownership only from the local session.
func WithMetadata(ctx context.Context, m Metadata) context.Context {
	return context.WithValue(ctx, metadataKey, m)
}

func MetadataFrom(ctx context.Context) Metadata {
	m, _ := ctx.Value(metadataKey).(Metadata)
	return m
}

var fallbackID atomic.Uint64

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	// Correlation IDs are not credentials. Keep them unique if entropy fails.
	return fmt.Sprintf("%016x%016x", uint64(time.Now().UnixNano()), fallbackID.Add(1))
}

type Span struct {
	mu       sync.Mutex
	call     Call
	started  time.Time
	recorder Recorder
	ended    bool
}

func Start(ctx context.Context, call Call) (context.Context, *Span) {
	if info, ok := ctx.Value(requestKey).(RequestInfo); ok && call.Direction == "inbound" {
		if call.Transport == "" {
			call.Transport = info.Transport
		}
		if call.RequestID == "" {
			call.RequestID = info.RequestID
		}
	}
	m := MetadataFrom(ctx)
	// Agent attribution cannot be self-declared by a tool call. Empty means unknown.
	call.AgentID, call.AgentName = m.AgentID, m.AgentName
	call.ID = NewID()
	if m.TraceID == "" {
		m.TraceID = NewID()
	}
	call.TraceID, call.ParentID = m.TraceID, m.ParentID
	call.SpanID = SpanIDFor(call.ID)
	call.ParentSpanID = m.ParentSpanID
	if call.ParentSpanID == "" {
		call.ParentSpanID = SpanIDFor(m.ParentID)
	}
	call.TraceFlags = normTraceFlags(m.TraceFlags)
	if call.ClientID == "" {
		call.ClientID = m.ClientID
	}
	if call.Entrypoint == "" {
		call.Entrypoint = m.Entrypoint
	}
	call.Entrypoint = normEntrypoint(call.Entrypoint)
	if call.SessionID == "" {
		call.SessionID = m.SessionID
	}
	if call.RunID == "" {
		call.RunID = m.RunID
	}
	if call.TaskID == "" {
		call.TaskID = m.TaskID
	}
	if call.OwnerID == "" {
		call.OwnerID = m.OwnerID
	}
	if call.ToolCallID == "" {
		call.ToolCallID = m.ToolCallID
	}
	started := time.Now()
	call.StartedAt, call.FinishedAt, call.DurationMs = started.UnixMilli(), 0, 0
	call.Status, call.ErrorKind, call.ErrorSummary, call.ErrorCode = "running", "", "", ""
	call.ErrorSource = ""
	call = bounded(call)
	rec, _ := ctx.Value(recorderKey).(recorderValue)
	s := &Span{call: call, started: started, recorder: rec.recorder}
	if s.recorder != nil {
		s.recorder.Record(call)
	}
	m = Metadata{TraceID: call.TraceID, ParentID: call.ID, SessionID: call.SessionID,
		RunID: call.RunID, TaskID: call.TaskID, OwnerID: call.OwnerID, ToolCallID: call.ToolCallID,
		ParentSpanID: call.SpanID, TraceFlags: call.TraceFlags, TraceState: m.TraceState,
		ClientID: call.ClientID, Entrypoint: call.Entrypoint, AgentID: call.AgentID, AgentName: call.AgentName}
	return WithMetadata(ctx, m), s
}

// Correlate adds locally resolved IDs, for example after an asynchronous
// submission allocated its session and run. Trace and parent never change.
func (s *Span) Correlate(m Metadata) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	if m.AgentID != "" {
		s.call.AgentID, s.call.AgentName = m.AgentID, m.AgentName
	}
	if m.SessionID != "" {
		s.call.SessionID = m.SessionID
	}
	if m.RunID != "" {
		s.call.RunID = m.RunID
	}
	if m.TaskID != "" {
		s.call.TaskID = m.TaskID
	}
	if m.OwnerID != "" {
		s.call.OwnerID = m.OwnerID
	}
	if m.ToolCallID != "" {
		s.call.ToolCallID = m.ToolCallID
	}
	s.call = bounded(s.call)
}

func (s *Span) End(status, errorKind string) {
	s.EndDiagnostic(status, errorKind, Diagnostic{})
}

// EndDiagnostic records only a sanitized explanation of an observed failure.
// Old callers of End, and historical rows, deliberately have no guessed reason.
func (s *Span) EndDiagnostic(status, errorKind string, diagnostic Diagnostic) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.ended = true
	s.call.FinishedAt = time.Now().UnixMilli()
	s.call.DurationMs = float64(time.Since(s.started).Microseconds()) / 1000
	switch status {
	case "ok", "error", "cancelled", "timeout", "interrupted":
		s.call.Status = status
	default:
		s.call.Status, errorKind = "interrupted", "unknown_outcome"
	}
	s.call.ErrorKind = boundedText(errorKind)
	if s.call.Status != "ok" {
		diagnostic = SafeDiagnostic(diagnostic)
		s.call.ErrorSummary, s.call.ErrorCode = diagnostic.Summary, diagnostic.Code
	}
	if s.recorder != nil {
		s.recorder.Record(s.call)
	}
}

// Bounded identifiers prevent an untrusted client name or tool declaration
// from turning a metadata-only record into an unbounded payload channel.
func boundedText(v string) string {
	v = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, v)
	r := []rune(v)
	if len(r) > 256 {
		return string(r[:256])
	}
	return v
}

func bounded(c Call) Call {
	for _, p := range []*string{&c.TraceID, &c.ParentID, &c.Direction, &c.Server, &c.Tool, &c.WireTool,
		&c.Transport, &c.Client, &c.MCPSessionID, &c.RequestID, &c.SessionID, &c.RunID, &c.TaskID,
		&c.OwnerID, &c.AgentID, &c.AgentName, &c.ToolCallID, &c.SpanID, &c.ParentSpanID, &c.TraceFlags, &c.ClientID, &c.Entrypoint} {
		*p = boundedText(*p)
	}
	return c
}

// normEntrypoint constrains the trusted origin to the known set.
func normEntrypoint(v string) string {
	switch v {
	case "pogo", "mcp", "cli":
		return v
	default:
		return "unknown"
	}
}
