// Package skillauthor stores host-scoped, instruction-only Agent-authored Skills.
// These bundles never register executables or grant additional capabilities.
package skillauthor

import (
	"errors"
	"time"
)

const (
	MaxFiles      = 32
	MaxFileBytes  = 256 << 10
	MaxSkillBytes = 1 << 20
	MaxSkills     = 128
	MaxVersions   = 64
	MaxRequests   = 4096
	MaxScopeBytes = 32 << 20
)

var (
	ErrInvalid         = errors.New("invalid instruction Skill")
	ErrNotFound        = errors.New("authored Skill or revision does not exist")
	ErrConflict        = errors.New("Skill changed; read its latest revision before saving")
	ErrNameConflict    = errors.New("Skill name already exists in this Agent")
	ErrRequestConflict = errors.New("request_id was already used with different content")
	ErrLimit           = errors.New("authored Skill storage limit exceeded")
)

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type SaveRequest struct {
	RequestID        string `json:"request_id"`
	SkillID          string `json:"skill_id,omitempty"`
	ExpectedRevision string `json:"expected_revision,omitempty"`
	Files            []File `json:"files"`
	Activate         *bool  `json:"activate,omitempty"`
}

type Snapshot struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Revision    string    `json:"revision"`
	Version     int       `json:"version"`
	Enabled     bool      `json:"enabled"`
	Files       []File    `json:"files"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type SaveResult struct {
	Skill     Snapshot `json:"skill"`
	RequestID string   `json:"request_id"`
	Reused    bool     `json:"reused"`
}
