// Package mcpclient connects pogo to the external MCP servers the user
// configures, and projects their tools into the tools registry.
//
// The configuration file is mcp.json — one entry per server in "servers",
// each either a command to run (stdio) or a URL to reach (streamable HTTP,
// or legacy SSE), plus an optional "server" section describing this machine
// as an MCP server for others (ServeConfig). It is command-execution configuration: a typo here is not a cosmetic
// setting quietly doing nothing, it is a command the user believes is or is
// not running. So the decoder is strict (unknown fields are errors, like
// providers.json and unlike prefs.json), and a file that exists but cannot
// be parsed is an error rather than a silent fall-back to "no servers" —
// the same load-failure split prefs.go documents: missing is normal,
// corrupt is never silent.
//
// The file holds no credentials. A bearer token is named by the environment
// variable that holds it (bearer_token_env), and env/header values may
// reference variables ($VAR) which are expanded at connect time — the file
// itself stays check-in-safe.
package mcpclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// EnvFile relocates the MCP configuration file. Same shape as
// POGO_PREFS_FILE and friends: a deployment decides where state lives.
const EnvFile = "POGO_MCP_FILE"

// Default and clamp bounds for the two timeouts. Startup covers connect plus
// the first tools/list — a stdio server may need seconds to boot, but two
// minutes of waiting on a dead command is a hang, not patience. Tool calls
// default to bash's two minutes and may stretch to an hour, matching
// MaxSynthTimeout's reasoning: a config value is written once and read
// forever, so its ceiling deserves a ceiling.
const (
	defaultStartupTimeoutMs = 10000
	minStartupTimeoutMs     = 1000
	maxStartupTimeoutMs     = 120000
	defaultToolTimeoutMs    = 120000
	minToolTimeoutMs        = 5000
	maxToolTimeoutMs        = 3600000
)

var (
	// serverNameRE bounds the name because it becomes part of every wire
	// tool name ("mcp_<server>_<tool>") and provider function names are
	// narrow — see tools.MaxWireNameLength.
	serverNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	// envNameRE is the conventional shape of an environment variable name,
	// copied from config/file.go's envName. bearer_token_env holds a *name*;
	// a value that is not a name is almost always a pasted token, which must
	// fail loudly instead of being sent as the literal word someone typed.
	envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// toolPatternRE is one include/exclude entry: a plain MCP tool name, or
	// one trailing "*" for a prefix wildcard ("mal.*"). No other glob
	// syntax — a pattern language nobody audits is where filters quietly
	// stop matching.
	toolPatternRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+(\*)?$`)
)

// Config is the whole mcp.json file: the servers this machine connects to
// ("servers"), and — optionally — this machine as a server itself ("server").
type Config struct {
	Servers map[string]*ServerConfig `json:"servers,omitempty"`
	// Server is the server-side half: whether other agents may reach this one
	// over MCP, and on what terms. Nil means the section is absent, which is
	// the same as disabled — a new network-facing surface is a grant, not a
	// default (web/mcp.go's MCPOptions comment).
	Server *ServeConfig `json:"server,omitempty"`
}

// ServerConfig describes one external MCP server: how to reach it, and what
// of it to expose.
type ServerConfig struct {
	// Enabled is a pointer so the default can be true — an entry someone
	// wrote is an entry they mean to use — while an explicit false keeps a
	// server in the file without connecting to it.
	Enabled *bool `json:"enabled,omitempty"`
	// Command runs a stdio server. Mutually exclusive with URL.
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// Env is added to the child server's environment, on top of a sanitized
	// inheritance — see pool.go's buildCommand for the exact rule. Values
	// may reference variables ($VAR, ${VAR}), expanded from the *unsanitized*
	// environment: declaring a value here is informed consent to hand it to
	// the child.
	Env map[string]string `json:"env,omitempty"`
	Cwd string            `json:"cwd,omitempty"`
	// URL reaches a remote server. http(s) only — the transports are HTTP.
	URL string `json:"url,omitempty"`
	// Transport is "streamable" (the default) or "sse" (the deprecated
	// 2024-11-05 transport, for servers that never upgraded).
	Transport string            `json:"transport,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	// BearerTokenEnv is the *name* of the environment variable holding the
	// token, never the token itself — same rule as providers.json's key_env,
	// for the same reason.
	BearerTokenEnv string `json:"bearer_token_env,omitempty"`
	// BearerToken is the token itself, inlined in mcp.json. Mutually
	// exclusive with BearerTokenEnv: two spellings of one credential, pick
	// one.
	//
	// This is a deliberate divergence from providers.json's "credentials
	// never touch disk" rule, and the reason it is tolerable here: the
	// Headers field already lets a config write a literal `Authorization`
	// header, so the possibility existed the day headers did — a first-class
	// field only makes it honest, and gives Validate somewhere to catch the
	// "$MY_TOKEN pasted as a value" mistake. The file is 0600 either way,
	// but 0600 is still disk: prefer bearer_token_env when the deployment
	// allows it.
	BearerToken string `json:"bearer_token,omitempty"`
	// StartupTimeoutMs bounds connect plus the first tools/list. Values
	// outside 1s..120s are clamped, not rejected: the file still says what
	// the operator asked for, and the clamp is the harness declining to hang
	// (or to flap) past a bound it can defend.
	StartupTimeoutMs int `json:"startup_timeout_ms,omitempty"`
	// ToolTimeoutMs bounds one tool call. Default 120s, clamped to
	// 5s..3600s — see the clamp comment above.
	ToolTimeoutMs int `json:"tool_timeout_ms,omitempty"`
	// IncludeTools limits the exposed tools; empty means all. Entries are
	// plain names or prefix wildcards ("mal.*").
	IncludeTools []string `json:"include_tools,omitempty"`
	// ExcludeTools wins over IncludeTools wherever both match: exclusion is
	// the safety direction, so it is the one that cannot be overridden by an
	// overly broad include.
	ExcludeTools []string `json:"exclude_tools,omitempty"`
	// Trust marks the server's tools as pre-approved: their wire names land
	// on the session's trust list, the same grant a manifest tool's
	// readOnlyHint earns. It is a per-server statement by the operator,
	// which is the only party who can make it — the server's own
	// annotations are untrusted hints, per the MCP spec.
	Trust bool `json:"trust,omitempty"`
	// Required makes startup fail (Pool.WaitReady) when the first connection
	// attempt does not succeed. For a server the workflow cannot run without;
	// everything else degrades to "reconnecting" instead of blocking the CLI.
	Required bool `json:"required,omitempty"`
}

// IsEnabled reports whether the pool should maintain a connection. Unset
// means enabled — see the field comment.
func (c *ServerConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// StartupTimeout is the configured startup budget with the default and clamp
// applied — the single funnel, so no caller can forget the bounds.
func (c *ServerConfig) StartupTimeout() time.Duration {
	return clampMs(c.StartupTimeoutMs, defaultStartupTimeoutMs, minStartupTimeoutMs, maxStartupTimeoutMs)
}

// ToolTimeout is the configured per-call budget, defaulted and clamped like
// StartupTimeout.
func (c *ServerConfig) ToolTimeout() time.Duration {
	return clampMs(c.ToolTimeoutMs, defaultToolTimeoutMs, minToolTimeoutMs, maxToolTimeoutMs)
}

func clampMs(v, def, min, max int) time.Duration {
	if v == 0 {
		v = def
	}
	if v < min {
		v = min
	}
	if v > max {
		v = max
	}
	return time.Duration(v) * time.Millisecond
}

// Allows reports whether a tool of the given server-side name passes the
// include/exclude filter. Exclusion is checked first because it is the
// override — see ExcludeTools.
func (c *ServerConfig) Allows(tool string) bool {
	for _, p := range c.ExcludeTools {
		if matchPattern(p, tool) {
			return false
		}
	}
	if len(c.IncludeTools) == 0 {
		return true
	}
	for _, p := range c.IncludeTools {
		if matchPattern(p, tool) {
			return true
		}
	}
	return false
}

func matchPattern(pattern, name string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, pattern[:len(pattern)-1])
	}
	return pattern == name
}

// --- server section (this machine as an MCP server) --------------------------

// Defaults and clamp ceilings for the three demand caps. The defaults are the
// values web/mcp.go has always fallen back to; the ceilings exist for the
// same reason the client-side timeout clamps do — a config value is written
// once and read forever, so its ceiling deserves a ceiling.
const (
	defaultServeMaxConcurrent = 8
	maxServeMaxConcurrent     = 64
	defaultServeSubmitRPM     = 60
	maxServeSubmitRPM         = 600
	defaultServeMaxWaits      = 32
	maxServeMaxWaits          = 256
)

// ServeConfig is the "server" section of mcp.json: the terms on which other
// agents may drive this one over MCP. The Raw fields are the file's words —
// 0/empty means *unset*, which is what lets the env → file → static tiering
// in the web layer tell "absent" apart from "zero"; the same-named methods
// resolve them (default plus clamp) and are the only reading the web layer
// applies when the section is the one in effect.
type ServeConfig struct {
	// Enabled turns the /mcp and /sse endpoints on. The zero value is off,
	// deliberately: a file without a server section leaves the machine
	// exactly as unreachable as one without the file.
	Enabled bool `json:"enabled,omitempty"`
	// Token is the credential MCP requests must bear, written directly.
	// Mutually exclusive with TokenEnv. Empty (with TokenEnv also empty) is
	// an explicit statement, not an oversight: the endpoints require no
	// authentication — clients get in whether or not they send a Bearer.
	// Plaintext in a 0600 file, the same trade ServerConfig.BearerToken
	// makes — and 0600 is still disk: prefer TokenEnv when the deployment
	// allows it.
	Token string `json:"token,omitempty"`
	// TokenEnv names the environment variable holding the token, never the
	// token itself — same rule as bearer_token_env, for the same reason.
	TokenEnv string `json:"token_env,omitempty"`
	// PolicyRaw is the approval mode MCP-submitted runs start under:
	// "strict", "standard" (the default) or "auto".
	PolicyRaw string `json:"policy,omitempty"`
	// IncludeTools is the whitelist of tool names this server exposes:
	// empty means everything (the historical behavior), non-empty means
	// tools/list shows only matches and tools/call refuses the rest.
	// Entries are plain names or one trailing-"*" prefix wildcard ("mal.*"),
	// the exact syntax the client side's IncludeTools uses — one pattern
	// language for both directions. There is deliberately no exclude: the
	// whitelist is the safety direction, and one list is auditable in a way
	// two interacting ones are not.
	IncludeTools []string `json:"include_tools,omitempty"`
	// The three caps, defaulted and clamped by the methods below.
	MaxConcurrentRaw int `json:"max_concurrent,omitempty"`
	SubmitRPMRaw     int `json:"submit_rpm,omitempty"`
	MaxWaitsRaw      int `json:"max_waits,omitempty"`
}

// Policy is the configured approval mode, "standard" when unset.
func (s *ServeConfig) Policy() string {
	if s.PolicyRaw == "" {
		return "standard"
	}
	return s.PolicyRaw
}

// Includes reports whether a tool of the given name passes the whitelist.
// An empty list means everything passes — absence is "no filter", never
// "expose nothing". The pattern semantics are the client side's
// matchPattern, shared verbatim.
func (s *ServeConfig) Includes(tool string) bool {
	for _, p := range s.IncludeTools {
		if matchPattern(p, tool) {
			return true
		}
	}
	return len(s.IncludeTools) == 0
}

// MaxConcurrent is the working-task ceiling, defaulted and clamped.
func (s *ServeConfig) MaxConcurrent() int {
	return clampInt(s.MaxConcurrentRaw, defaultServeMaxConcurrent, 1, maxServeMaxConcurrent)
}

// SubmitRPM is the per-minute submission budget, defaulted and clamped.
func (s *ServeConfig) SubmitRPM() int {
	return clampInt(s.SubmitRPMRaw, defaultServeSubmitRPM, 1, maxServeSubmitRPM)
}

// MaxWaits is the concurrent-wait ceiling, defaulted and clamped.
func (s *ServeConfig) MaxWaits() int {
	return clampInt(s.MaxWaitsRaw, defaultServeMaxWaits, 1, maxServeMaxWaits)
}

// ResolvedToken is the section's credential: Token verbatim, or the variable
// TokenEnv names read through getenv (os.Getenv in production, a stub in
// tests — the pool's buildCommand pattern). "" with nil error means the
// section configures no credential, which is a *statement* — the endpoints
// require no authentication — not an oversight. A TokenEnv whose variable is
// missing is an error instead: "authentication is configured but resolves to
// nothing" must not quietly read as "no authentication", because an operator
// who believes the endpoints are gated while they stand open is the worst
// direction to be wrong in. The gate that consumes this fails closed.
func (s *ServeConfig) ResolvedToken(getenv func(string) string) (string, error) {
	if s.Token != "" {
		return s.Token, nil
	}
	if s.TokenEnv != "" {
		t := getenv(s.TokenEnv)
		if t == "" {
			return "", fmt.Errorf("token_env names $%s, which is not set in the environment", s.TokenEnv)
		}
		return t, nil
	}
	return "", nil
}

func clampInt(v, def, min, max int) int {
	if v == 0 {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// validate rejects the server-section shapes that would misfire or lie. The
// caps are not checked here: they are clamped by their accessors, the
// client-side timeout precedent — the file says what the operator asked for,
// and the clamp is the harness declining to honor a value past its bound.
func (s *ServeConfig) validate() error {
	if s.Token != "" && s.TokenEnv != "" {
		return fmt.Errorf(`server: "token" and "token_env" are two spellings of one credential — set exactly one, not both`)
	}
	if s.Token != "" {
		// As with token_env, the failing value is not echoed: it is meant to
		// be a credential.
		if strings.TrimSpace(s.Token) == "" {
			return fmt.Errorf(`server: "token" is all whitespace — that is not a token`)
		}
		if strings.HasPrefix(s.Token, "$") {
			// The file does no expansion in this field, so a "$NAME" here
			// would be handed to clients as the literal credential — almost
			// certainly a variable reference the author expected resolved.
			return fmt.Errorf(`server: "token" starts with '$' — that looks like a variable reference pasted as a value; use token_env for a variable name`)
		}
	}
	if s.TokenEnv != "" && !envNameRE.MatchString(s.TokenEnv) {
		// Deliberately not echoing the value: the likely reason it fails is
		// that the token itself was pasted in.
		return fmt.Errorf(`server: "token_env" must be the name of an environment variable holding the token, not the token itself`)
	}
	switch s.PolicyRaw {
	case "", "strict", "standard", "auto":
	default:
		return fmt.Errorf(`server: "policy" must be "strict", "standard" or "auto", got %q`, s.PolicyRaw)
	}
	for _, p := range s.IncludeTools {
		if !toolPatternRE.MatchString(p) {
			return fmt.Errorf(`server: "include_tools" entry %q is invalid: letters, digits, '_', '-', '.', and at most one trailing '*'`, p)
		}
	}
	return nil
}

// Path resolves where mcp.json lives: $POGO_MCP_FILE, else ~/.pogo/mcp.json
// beside providers.json and prefs.json.
func Path() string {
	if p := strings.TrimSpace(os.Getenv(EnvFile)); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".pogo", "mcp.json")
	}
	return filepath.Join(os.TempDir(), "pogo", "mcp.json")
}

// Validate rejects the configurations that would misfire or lie. Names are
// visited in sorted order so the same file always earns the same error.
func (c Config) Validate() error {
	names := make([]string, 0, len(c.Servers))
	for name := range c.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := validateServer(name, c.Servers[name]); err != nil {
			return err
		}
	}
	if c.Server != nil {
		if err := c.Server.validate(); err != nil {
			return err
		}
	}
	return nil
}

func validateServer(name string, s *ServerConfig) error {
	if !serverNameRE.MatchString(name) {
		return fmt.Errorf("server name %q is invalid: 1-32 characters, letters, digits, '_' and '-' only (it becomes part of every tool's wire name)", name)
	}
	if s == nil {
		return fmt.Errorf("server %q: entry is null", name)
	}
	hasCmd := strings.TrimSpace(s.Command) != ""
	hasURL := strings.TrimSpace(s.URL) != ""
	if hasCmd == hasURL {
		return fmt.Errorf("server %q: exactly one of command (stdio) or url (http) must be set", name)
	}
	if hasCmd {
		// The remaining fields are URL-side; setting them here means the
		// author pictured an HTTP server while configuring a process, which
		// is a wrong-place error worth naming, not a field worth ignoring.
		if s.Transport != "" || len(s.Headers) > 0 || s.BearerTokenEnv != "" || s.BearerToken != "" {
			return fmt.Errorf("server %q: transport, headers, bearer_token_env and bearer_token only apply to url servers", name)
		}
		return validateToolFilters(name, s)
	}
	if len(s.Args) > 0 || len(s.Env) > 0 || s.Cwd != "" {
		return fmt.Errorf("server %q: args, env and cwd only apply to command servers", name)
	}
	u, err := url.Parse(s.URL)
	if err != nil {
		return fmt.Errorf("server %q: url %q is not a URL: %v", name, s.URL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("server %q: url %q must start with http:// or https:// — those are the transports the SDK speaks", name, s.URL)
	}
	if u.Host == "" {
		return fmt.Errorf("server %q: url %q names no host", name, s.URL)
	}
	switch s.Transport {
	case "", "streamable", "sse":
	default:
		return fmt.Errorf("server %q: transport must be \"streamable\" or \"sse\", got %q", name, s.Transport)
	}
	if s.BearerTokenEnv != "" && !envNameRE.MatchString(s.BearerTokenEnv) {
		// Deliberately not echoing the value: the likely reason it fails is
		// that the token itself was pasted in.
		return fmt.Errorf("server %q: bearer_token_env must be the name of an environment variable holding the token, not the token itself", name)
	}
	if s.BearerToken != "" && s.BearerTokenEnv != "" {
		return fmt.Errorf("server %q: bearer_token and bearer_token_env are two spellings of one credential — set exactly one, not both", name)
	}
	if s.BearerToken != "" {
		// As with bearer_token_env, the failing value is not echoed: it is
		// meant to be a credential.
		if strings.TrimSpace(s.BearerToken) == "" {
			return fmt.Errorf("server %q: bearer_token is all whitespace — that is not a token", name)
		}
		if strings.HasPrefix(s.BearerToken, "$") {
			return fmt.Errorf("server %q: bearer_token starts with '$' — that looks like a variable reference pasted as a value; use bearer_token_env for a variable name", name)
		}
	}
	return validateToolFilters(name, s)
}

func validateToolFilters(name string, s *ServerConfig) error {
	for _, list := range []struct {
		field string
		ps    []string
	}{{"include_tools", s.IncludeTools}, {"exclude_tools", s.ExcludeTools}} {
		for _, p := range list.ps {
			if !toolPatternRE.MatchString(p) {
				return fmt.Errorf("server %q: %s entry %q is invalid: letters, digits, '_', '-', '.', and at most one trailing '*'", name, list.field, p)
			}
		}
	}
	return nil
}

// Load reads and validates the configuration at path. A missing file is an
// empty Config with no error — no external servers is the normal state of a
// fresh machine. A file that exists but cannot be parsed or fails Validate
// is an error: see the package comment for why corrupt must not be silent.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(data))
	// Strict, like providers.json and unlike prefs.json: this file names
	// commands to run and hosts to call, so an unrecognised key is a
	// misspelling the user believes is in effect — the config/file.go
	// stance, applied to the file that executes things.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Save validates, then writes 0600 temp+rename with the in-place fallback —
// the same pattern and the same reasons as prefs.Save, whose comment tells
// the whole story: the file may sit on a mounted volume, and the odd
// single-file bind mount cannot be renamed over.
func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if tmp, err := os.CreateTemp(filepath.Dir(path), "mcp.json.tmp-*"); err == nil {
		tmpName := tmp.Name()
		_ = tmp.Chmod(0o600)
		_, werr := tmp.Write(data)
		cerr := tmp.Close()
		if werr == nil && cerr == nil {
			if rerr := os.Rename(tmpName, path); rerr == nil {
				return nil
			}
			os.Remove(tmpName)
		} else {
			os.Remove(tmpName)
		}
		// Fall through to the in-place write: the bind-mount case.
	}

	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := out.Write(data); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
