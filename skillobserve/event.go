// Package skillobserve records observable skill operations, never an inferred
// end-to-end skill workflow. Discovery, instruction reads, and owned manifest
// tool execution have separate stages and are not interchangeable successes.
package skillobserve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/yosukeno/pogo/mcpobserve"
)

type Identity struct{ ID, Name, Source string }

// Identify is called on the trusted loader's path, not an arbitrary tool
// argument. The canonical absolute path contributes to identity but is never
// retained. Bind and retain this identity with the catalog/tool snapshot, so a
// later file deletion does not force identity to be resolved again.
func Identify(name, source, path string) Identity {
	if path == "" || name == "" {
		return Identity{}
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	path = filepath.ToSlash(filepath.Clean(path))
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	h := sha256.Sum256([]byte("pogo-skill\x00" + path + "\x00" + name))
	switch source {
	// "bundle" is a packaged runtime Skill: its identifying path is the
	// skill-runtime:// package location, not a filesystem directory.
	case "user", "project", "path", "embedded", "bundle":
	default:
		source = "unknown"
	}
	return Identity{ID: hex.EncodeToString(h[:16]), Name: boundedText(name), Source: source}
}

type Event struct {
	ID           string  `json:"id"`
	TraceID      string  `json:"trace_id"`
	ParentID     string  `json:"parent_id,omitempty"`
	SpanID       string  `json:"span_id"`
	ParentSpanID string  `json:"parent_span_id,omitempty"`
	TraceFlags   string  `json:"trace_flags"`
	ClientID     string  `json:"client_id,omitempty"`
	SkillID      string  `json:"skill_id"`
	SkillName    string  `json:"skill_name"`
	SkillSource  string  `json:"skill_source"`
	Stage        string  `json:"stage"`
	Caller       string  `json:"caller"`
	Entrypoint   string  `json:"entrypoint"`
	Mechanism    string  `json:"mechanism"`
	Tool         string  `json:"tool,omitempty"`
	WireTool     string  `json:"wire_tool,omitempty"`
	SessionID    string  `json:"session_id,omitempty"`
	RunID        string  `json:"run_id,omitempty"`
	TaskID       string  `json:"task_id,omitempty"`
	OwnerID      string  `json:"owner_id,omitempty"`
	ToolCallID   string  `json:"tool_call_id,omitempty"`
	StartedAt    int64   `json:"started_at"`
	FinishedAt   int64   `json:"finished_at,omitempty"`
	DurationMs   float64 `json:"duration_ms"`
	Status       string  `json:"status"`
	ErrorKind    string  `json:"error_kind,omitempty"`
}

type Recorder interface{ Record(Event) }

var ErrInvalidFilter = errors.New("invalid skill observation filter")

type contextKey uint8

const (
	recorderKey contextKey = iota
	sourceKey
)

type recorderValue struct{ r Recorder }
type sourceValue struct{ caller, entrypoint string }

func WithRecorder(ctx context.Context, r Recorder) context.Context {
	return context.WithValue(ctx, recorderKey, recorderValue{r})
}

// WithSource describes a trusted execution entry, not a model argument. An
// empty entrypoint preserves the run's origin carried in shared metadata.
func WithSource(ctx context.Context, caller, entrypoint string) context.Context {
	return context.WithValue(ctx, sourceKey, sourceValue{caller, entrypoint})
}

func SourceFrom(ctx context.Context) (caller, entrypoint string) {
	s, _ := ctx.Value(sourceKey).(sourceValue)
	caller, entrypoint = s.caller, s.entrypoint
	if entrypoint == "" {
		entrypoint = mcpobserve.MetadataFrom(ctx).Entrypoint
	}
	if caller != "internal" && caller != "external" {
		caller = "unknown"
	}
	if entrypoint != "pogo" && entrypoint != "mcp" && entrypoint != "cli" {
		entrypoint = "unknown"
	}
	return
}

// normTraceFlags mirrors mcpobserve: keep a valid flags byte, default to 01.
func normTraceFlags(flags string) string {
	if len(flags) != 2 {
		return "01"
	}
	for i := 0; i < 2; i++ {
		c := flags[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "01"
		}
	}
	return flags
}

type Span struct {
	mu       sync.Mutex
	event    Event
	started  time.Time
	recorder Recorder
	ended    bool
}

func Start(ctx context.Context, identity Identity, stage, mechanism, tool, wireTool string) (context.Context, *Span) {
	m := mcpobserve.MetadataFrom(ctx)
	if m.TraceID == "" {
		m.TraceID = mcpobserve.NewID()
	}
	caller, entrypoint := SourceFrom(ctx)
	started := time.Now()
	id := mcpobserve.NewID()
	parentSpan := m.ParentSpanID
	if parentSpan == "" {
		parentSpan = mcpobserve.SpanIDFor(m.ParentID)
	}
	e := Event{ID: id, TraceID: m.TraceID, ParentID: m.ParentID,
		SpanID: mcpobserve.SpanIDFor(id), ParentSpanID: parentSpan,
		TraceFlags: normTraceFlags(m.TraceFlags), ClientID: m.ClientID,
		SkillID: identity.ID, SkillName: identity.Name, SkillSource: identity.Source,
		Stage: stage, Caller: caller, Entrypoint: entrypoint, Mechanism: mechanism,
		Tool: tool, WireTool: wireTool, SessionID: m.SessionID, RunID: m.RunID,
		TaskID: m.TaskID, OwnerID: m.OwnerID, ToolCallID: m.ToolCallID,
		StartedAt: started.UnixMilli(), Status: "running"}
	e = bounded(e)
	r, _ := ctx.Value(recorderKey).(recorderValue)
	span := &Span{event: e, started: started, recorder: r.r}
	// An unresolved identity is not evidence of skill usage.
	if identity.ID == "" {
		span.recorder = nil
		return ctx, span
	}
	if span.recorder != nil {
		span.recorder.Record(e)
	}
	m.ParentID = e.ID
	m.ParentSpanID = e.SpanID
	m.TraceFlags = e.TraceFlags
	return mcpobserve.WithMetadata(ctx, m), span
}

func (s *Span) Correlate(m mcpobserve.Metadata) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	if m.SessionID != "" {
		s.event.SessionID = m.SessionID
	}
	if m.RunID != "" {
		s.event.RunID = m.RunID
	}
	if m.TaskID != "" {
		s.event.TaskID = m.TaskID
	}
	if m.OwnerID != "" {
		s.event.OwnerID = m.OwnerID
	}
	if m.ToolCallID != "" {
		s.event.ToolCallID = m.ToolCallID
	}
	s.event = bounded(s.event)
}

func (s *Span) End(status, errorKind string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.ended = true
	s.event.FinishedAt = time.Now().UnixMilli()
	s.event.DurationMs = float64(time.Since(s.started).Microseconds()) / 1000
	switch status {
	case "ok", "error", "cancelled", "timeout", "interrupted":
		s.event.Status = status
	default:
		s.event.Status = "interrupted"
		errorKind = "unknown_outcome"
	}
	if s.event.Status == "ok" {
		errorKind = ""
	}
	switch errorKind {
	case "", "read_error", "validation_error", "execution_error", "tool_error", "timeout", "cancelled", "rejected", "unknown_outcome", "configuration_error", "permission_error", "internal_error":
	default:
		errorKind = "unknown_error"
	}
	s.event.ErrorKind = errorKind
	if s.recorder != nil {
		s.recorder.Record(s.event)
	}
}

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

func bounded(e Event) Event {
	for _, p := range []*string{&e.ID, &e.TraceID, &e.ParentID, &e.SkillID, &e.SkillName, &e.SkillSource, &e.Stage, &e.Caller, &e.Entrypoint, &e.Mechanism, &e.Tool, &e.WireTool, &e.SessionID, &e.RunID, &e.TaskID, &e.OwnerID, &e.ToolCallID} {
		*p = boundedText(*p)
	}
	for _, p := range []*string{&e.SpanID, &e.ParentSpanID, &e.TraceFlags, &e.ClientID} {
		*p = boundedText(*p)
	}
	return e
}
