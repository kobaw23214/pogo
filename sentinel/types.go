// Package sentinel stores generic script observations independently of chat
// sessions. It knows nothing about the kind of system a script observes.
package sentinel

import (
	"encoding/json"
	"errors"
)

const (
	MaxFrameBytes    = 64 << 10
	MaxStateBytes    = 16 << 10
	MaxPendingEvents = 1024
	MaxBatchEvents   = 32
	MaxBatchBytes    = 64 << 10
)

var (
	ErrNotFound        = errors.New("sentinel: not found")
	ErrConflict        = errors.New("sentinel: conflicting receipt or state")
	ErrStaleGeneration = errors.New("sentinel: retired collector generation")
	ErrDisabled        = errors.New("sentinel: watch is paused")
	ErrExpired         = errors.New("sentinel: host deadline reached; saved observations and progress are retained")
	ErrQueueFull       = errors.New("sentinel: event queue is full; observation not accepted")
	ErrBatchActive     = errors.New("sentinel: execution is active or requires reconciliation")
)

// Spec contains host-bound authority as well as the saved script declaration.
// Owner, workspace, script directory, identity and generation are never trusted
// directly from script frames or model arguments.
type Spec struct {
	ID              string          `json:"id"`
	OwnerID         string          `json:"owner_id"`
	Owner           json.RawMessage `json:"owner,omitempty"`
	AgentID         string          `json:"agent_id"`
	Workspace       string          `json:"workspace"`
	ScriptDir       string          `json:"script_dir"`
	Command         []string        `json:"command"`
	Title           string          `json:"title"`
	Task            string          `json:"task"`
	Mode            string          `json:"mode"`
	IntervalSeconds int             `json:"interval_seconds"`
	TimeoutSeconds  int             `json:"timeout_seconds"`
	DebounceMS      int             `json:"debounce_ms"`
	MaxTurns        int             `json:"max_turns"`
	Network         bool            `json:"network"`
	Enabled         bool            `json:"enabled"`
	// ExpiresAt is the host-enforced absolute deadline in Unix milliseconds.
	// Zero preserves the lifetime of registrations created before this field.
	ExpiresAt int64 `json:"expires_at,omitempty"`
	// ParentSessionID is the host conversation that registered this watch.
	// It is navigation lineage, not part of the execution definition hash.
	ParentSessionID string `json:"parent_session_id,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	Revision        int64  `json:"revision"`
	Generation      int64  `json:"generation"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

// Expired is inclusive: work cannot begin or commit at the deadline itself.
func (s Spec) Expired(at int64) bool { return s.ExpiresAt > 0 && at >= s.ExpiresAt }

type Watch struct {
	Spec     Spec            `json:"spec"`
	State    json.RawMessage `json:"state,omitempty"`
	Cursor   json.RawMessage `json:"cursor,omitempty"`
	Status   string          `json:"status"`
	Error    string          `json:"error,omitempty"`
	NextAt   int64           `json:"next_at,omitempty"`
	Failures int             `json:"failures"`
	Pending  int             `json:"pending"`
}

// Frame.ID identifies one immutable proposal. Reusing it with different
// contents is rejected. Event keys are stable across collector generations.
type Frame struct {
	V      int             `json:"v"`
	ID     string          `json:"id"`
	Events []InputEvent    `json:"events"`
	Cursor json.RawMessage `json:"cursor,omitempty"`
	State  json.RawMessage `json:"state,omitempty"`
}

type InputEvent struct {
	Key     string          `json:"key"`
	Summary string          `json:"summary"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type Event struct {
	ID      string          `json:"id"`
	WatchID string          `json:"watch_id"`
	Key     string          `json:"key"`
	Summary string          `json:"summary"`
	Data    json.RawMessage `json:"data,omitempty"`
	BatchID string          `json:"batch_id,omitempty"`
}

// A batch's event set is frozen before admission. New observations stay in the
// next batch. ID is also the stable logical wake identity across retries.
type Batch struct {
	Attempt   int     `json:"attempt"`
	ID        string  `json:"id"`
	WatchID   string  `json:"watch_id"`
	SessionID string  `json:"session_id,omitempty"`
	RunID     string  `json:"run_id,omitempty"`
	Status    string  `json:"status"`
	Decision  string  `json:"decision,omitempty"`
	Summary   string  `json:"summary,omitempty"`
	Error     string  `json:"error,omitempty"`
	Events    []Event `json:"events"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
	NotBefore int64   `json:"not_before,omitempty"`
}
