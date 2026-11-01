// Package shortcuts compiles ordered shortcut drafts without granting source or
// tool access. Storage and reference authorization belong to the calling host.
package shortcuts

import (
	"context"
	"github.com/yosukeno/pogo/inputref"
)

const (
	MaxDepth = 5
	MaxNodes = 512
	MaxBytes = 256 << 10
)

// Part is one ordered draft atom. Exactly the fields for Kind may be populated.
// A ref is an opaque host-issued handle, never an arbitrary file path or URL.
type Part struct {
	Kind   string `json:"kind"`
	Text   string `json:"text,omitempty"`
	RefID  string `json:"ref_id,omitempty"`
	NodeID string `json:"node_id,omitempty"`
}

type Node struct {
	LocalID  string `json:"local_id"`
	Title    string `json:"title"`
	Icon     string `json:"icon,omitempty"`
	Language string `json:"language,omitempty"`
	Parts    []Part `json:"parts"`
}

// Graph is deliberately flat so its tool schema needs no recursive $ref.
// Repeated node references expand as repeated inline snapshots, not shared writes.
type Graph struct {
	RootID string `json:"root_id"`
	Nodes  []Node `json:"nodes"`
}

// Entry is one durable private shortcut. New child nodes exist only in Parts;
// saving an Entry must not publish each child as an independent shortcut.
type Entry struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Content     string          `json:"content"`
	Icon        string          `json:"icon"`
	Language    string          `json:"language,omitempty"`
	Parts       []inputref.Part `json:"parts,omitempty"`
	InputLayout string          `json:"input_layout,omitempty"`
	ParentID    string          `json:"parent_id,omitempty"`
}

// Reference contains a trusted, already authorized immutable snapshot. File
// atoms must use the host's private shortcut asset namespace before compilation.
// ScopeID is selected by the host and never accepted from model tool arguments.
type Reference struct {
	ScopeID string          `json:"scope_id"`
	Version string          `json:"version,omitempty"`
	Parts   []inputref.Part `json:"parts"`
	SHA256  string          `json:"snapshot_sha256,omitempty"`
}

type Resolver func(context.Context, string) (Reference, error)

type Stats struct {
	Nodes int `json:"nodes"`
	Bytes int `json:"bytes"`
	Depth int `json:"depth"`
}

type Compiled struct {
	Root       Entry                `json:"root"`
	IDs        map[string]string    `json:"ids"`
	Stats      Stats                `json:"stats"`
	References map[string]Reference `json:"reference_snapshots"`
}

type SearchRequest struct {
	Query    string `json:"query,omitempty"`
	Source   string `json:"source,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type ReferenceSummary struct {
	RefID    string `json:"ref_id,omitempty"`
	Source   string `json:"source"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Snippet  string `json:"snippet,omitempty"`
	Version  string `json:"version,omitempty"`
	ID       string `json:"id,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
	Icon     string `json:"icon,omitempty"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

type SearchResult struct {
	Entries    []ReferenceSummary `json:"entries"`
	Total      int                `json:"total"`
	Offset     int                `json:"offset"`
	NextOffset *int               `json:"next_offset,omitempty"`
}

type ReadRequest struct {
	RefID string `json:"ref_id"`
}

type ReadResult struct {
	Reference ReferenceSummary `json:"reference"`
	Parts     []inputref.Part  `json:"parts"`
	// Graph is an optional editable draft representation; its refs remain opaque
	// handles minted by the host. Reading it does not persist or modify anything.
	Graph *Graph `json:"graph,omitempty"`
}

// RequestID provides durable per-owner/Agent idempotency. Same key plus changed
// payload must fail; retries of identical payload return the original Entry.
type CreateRequest struct {
	RequestID string `json:"request_id"`
	ParentID  string `json:"parent_id,omitempty"`
	Graph
}

type CreateResult struct {
	AgentID   string `json:"agent_id,omitempty"`
	Shortcut  Entry  `json:"shortcut"`
	Revision  int64  `json:"revision"`
	RequestID string `json:"request_id"`
	Reused    bool   `json:"reused"`
}
