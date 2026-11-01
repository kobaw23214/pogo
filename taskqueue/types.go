// Package taskqueue stores durable, source-independent task admissions and
// execution outcomes. Authorization and the single controller lease belong to
// the host; transactions protect concurrent and repeated operations here.
package taskqueue

import (
	"encoding/json"
	"errors"
)

const (
	Queued      = "queued"
	Running     = "running"
	Completed   = "completed"
	Failed      = "failed"
	Cancelled   = "cancelled"
	Interrupted = "interrupted"

	MaxPayloadBytes = 1 << 20
)

var (
	ErrNotFound = errors.New("taskqueue: not found")
	ErrConflict = errors.New("taskqueue: conflicting content or execution revision")
	ErrBusy     = errors.New("taskqueue: execution capacity unavailable")
)

// Request is the immutable admission envelope. Payload is opaque JSON: its
// original bytes are preserved, not re-encoded or interpreted by the queue.
// DueAt is Unix milliseconds; zero means immediately eligible.
type Request struct {
	TaskID         string `json:"task_id"`
	Source         string `json:"source"`
	SourceID       string `json:"source_id"`
	Trigger        string `json:"trigger"`
	TriggerID      string `json:"trigger_id"`
	DefinitionHash string `json:"definition_hash"`
	OwnerID        string `json:"owner_id"`
	AgentID        string `json:"agent_id,omitempty"`
	WorkflowID     string `json:"workflow_id,omitempty"`
	// WorkflowRunID binds an execution segment to its logical workflow run.
	// Empty preserves the legacy schedule/sentinel envelope byte for byte.
	WorkflowRunID string          `json:"workflow_run_id,omitempty"`
	Title         string          `json:"title"`
	RetryOf       string          `json:"retry_of,omitempty"`
	DueAt         int64           `json:"due_at"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

type Run struct {
	Request
	ID              string `json:"id"`
	Status          string `json:"status"`
	Revision        int64  `json:"revision"`
	Attempt         int    `json:"attempt"`
	CancelRequested bool   `json:"cancel_requested"`
	CreatedAt       int64  `json:"created_at"`
	StartedAt       int64  `json:"started_at,omitempty"`
	FinishedAt      int64  `json:"finished_at,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	Summary         string `json:"summary,omitempty"`
	Error           string `json:"error,omitempty"`
	EndReason       string `json:"end_reason,omitempty"`
}

type Outcome struct {
	Status    string `json:"status"`
	SessionID string `json:"session_id,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Error     string `json:"error,omitempty"`
	EndReason string `json:"end_reason,omitempty"`
}

type Filter struct {
	OwnerID    string
	AgentID    string
	TaskID     string
	ActiveOnly bool // queued or running, including cancellation requests not yet completed
	Limit      int  // zero returns all matching rows for the trusted host
}
