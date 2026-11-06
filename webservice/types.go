// Package webservice owns managed website identity and lifecycle independently
// of model runs and in-memory conversations. Runtime and gateway adapters are
// host-owned; tool arguments never contain owner identities or upstream URLs.
package webservice

import (
	"context"
	"encoding/json"
	"time"
)

type Scope struct {
	Owner     string `json:"owner"`
	Session   string `json:"session"`
	Workspace string `json:"-"`
}

type Spec struct {
	Name         string   `json:"name,omitempty"`
	DisplayName  string   `json:"display_name,omitempty"`
	ProjectRoot  string   `json:"project_root,omitempty"`
	Profile      string   `json:"profile,omitempty"`
	StartArgv    []string `json:"start_argv,omitempty"`
	BuildArgv    []string `json:"build_argv,omitempty"`
	HealthPath   string   `json:"health_path,omitempty"`
	LeaseSeconds int      `json:"lease_seconds,omitempty"`
}

type Request struct {
	Action      string `json:"action"`
	OperationID string `json:"operation_id,omitempty"`
	ServiceID   string `json:"service_id,omitempty"`
	Spec
	Replacement *Spec  `json:"replacement_spec,omitempty"`
	GrantID     string `json:"grant_id,omitempty"`
	Generation  int    `json:"generation,omitempty"`
	Cursor      string `json:"cursor,omitempty"`
	LimitBytes  int    `json:"limit_bytes,omitempty"`
}

type Error struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Phase     string         `json:"phase,omitempty"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string        { return e.Code + ": " + e.Message }
func Fail(code, message string) error { return &Error{Code: code, Message: message} }

type Instance struct {
	ID        string `json:"id"`
	NetworkID string `json:"network_id"`
	Upstream  string `json:"upstream"`
}

type Record struct {
	SchemaVersion  int           `json:"schema_version"`
	ServiceID      string        `json:"service_id"`
	Owner          string        `json:"owner"`
	Session        string        `json:"session"`
	Installation   string        `json:"installation"`
	Spec           Spec          `json:"spec"`
	DisplayName    string        `json:"display_name,omitempty"`
	Workspace      string        `json:"workspace,omitempty"`
	LeaseID        string        `json:"lease_id,omitempty"`
	Runtime        RuntimeConfig `json:"runtime,omitempty"`
	SourceDigest   string        `json:"source_digest,omitempty"`
	SpecDigest     string        `json:"spec_digest"`
	Generation     int           `json:"generation"`
	Revision       int64         `json:"revision"`
	State          string        `json:"state"`
	Phase          string        `json:"phase,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
	LeaseExpiresAt time.Time     `json:"lease_expires_at"`
	Instance       Instance      `json:"instance"`
	LastError      *Error        `json:"last_error,omitempty"`
	LastHealth     *Health       `json:"last_health_check,omitempty"`
	TerminalTarget string        `json:"terminal_target,omitempty"`
	OperationID    string        `json:"operation_id,omitempty"`
	ControlID      string        `json:"control_id,omitempty"`
	Storage        *StorageUsage `json:"storage,omitempty"`
	StorageWarning *Error        `json:"storage_warning,omitempty"`
}

type Launch struct {
	Record  Record `json:"record"`
	Archive []byte `json:"archive"`
}

type RuntimeStatus struct {
	Running        bool          `json:"running"`
	Exists         bool          `json:"exists"`
	ExitCode       int           `json:"exit_code"`
	Instance       Instance      `json:"instance"`
	Storage        *StorageUsage `json:"storage,omitempty"`
	LastError      *Error        `json:"last_error,omitempty"`
	StorageWarning *Error        `json:"storage_warning,omitempty"`
}

// StorageUsage is measured by the trusted runner. Limits are soft monitoring
// thresholds; an application cannot increase them or report its own usage.
type StorageUsage struct {
	RuntimeBytes   int64     `json:"runtime_bytes"`
	DataBytes      int64     `json:"data_bytes"`
	OwnerDataBytes int64     `json:"owner_data_bytes"`
	CheckedAt      time.Time `json:"checked_at"`
}

type LogPage struct {
	Generation int    `json:"generation"`
	FromCursor string `json:"from_cursor"`
	NextCursor string `json:"next_cursor"`
	Text       string `json:"text"`
	Truncated  bool   `json:"truncated"`
	EOF        bool   `json:"eof"`
}

type Health struct {
	Generation     int       `json:"generation"`
	CheckedAt      time.Time `json:"checked_at"`
	RelativePath   string    `json:"relative_path"`
	UpstreamStatus int       `json:"upstream_status"`
	GatewayStatus  int       `json:"gateway_status"`
	Result         string    `json:"result"`
}

type Grant struct {
	ServiceID  string  `json:"service_id"`
	Generation int     `json:"generation"`
	GrantID    string  `json:"grant_id"`
	PreviewURL string  `json:"preview_url"`
	BrowserURL *string `json:"browser_url"`
	ServiceURL string  `json:"service_url,omitempty"`
	// Gateway-owned browser origins. These are administrator configuration,
	// never inferred from the requesting page or accepted from tool arguments.
	CanonicalOrigin      string    `json:"canonical_origin,omitempty"`
	AllowedParentOrigins []string  `json:"allowed_parent_origins,omitempty"`
	ExpiresAt            time.Time `json:"grant_expires_at"`
	LeaseExpiresAt       time.Time `json:"lease_expires_at"`
	Limitations          []string  `json:"limitations"`
}

// Runner implementations must validate persisted identity before every effect.
// Stop confirms absence; Renew changes an independently enforced runtime lease.
type Runner interface {
	Preflight(context.Context) error
	Start(context.Context, Launch) (Instance, error)
	Inspect(context.Context, Record) (RuntimeStatus, error)
	Stop(context.Context, Record) error
	Renew(context.Context, Record) error
	Logs(context.Context, Record, string, int) (LogPage, error)
	DeleteData(context.Context, Record) error
}

type Gateway interface {
	Preflight(context.Context) error
	Apply(context.Context, Record) error
	Revoke(context.Context, Record) error
	Probe(context.Context, Record) (Health, error)
	Open(context.Context, Record) (Grant, error)
	CloseGrant(context.Context, Record, string) error
}

// Backend is always pre-bound to a trusted Scope by the Web host.
type Backend interface {
	Execute(context.Context, Request) (json.RawMessage, error)
}
