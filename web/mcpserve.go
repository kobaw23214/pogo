package web

// The MCP server side's live configuration: how the file's "server" section
// (mcp.json, mcpclient.ServeConfig), the POGO_MCP* environment and the static
// MCPOptions baseline combine into what the endpoints do *right now*.
//
// One precedence shape, applied per request so edits land without a restart:
//
//	env  >  file (mcp.json "server", hot-swapped by PUT /api/mcp)  >  static (ServerOptions.MCP)  >  default
//
//   - enabled: POGO_MCP=1 forces on; else the file's enabled; else the
//     static baseline's presence; else off. A file-driven deployment with
//     no server section is as unreachable as one with no file — a new
//     network-facing surface is a grant, not a default.
//   - token: POGO_MCP_TOKEN, then the file's token/token_env, then the
//     static token. Empty is an explicit statement, not an oversight: the
//     endpoints require no authentication — clients are admitted with or
//     without a Bearer. The legacy exception is the no-store path (static
//     options only), where empty keeps its historic meaning of "the web
//     token gates": that fallback lives in server.go's gate, which is the
//     code that knows whether a store exists.
//   - policy: POGO_MCP_POLICY, then the file's policy, then the static one,
//     then standard.
//   - include_tools: file only, deliberately — the tool whitelist is a
//     deployment's editorial choice, and two more tiers of it would only be
//     harder to audit. Hot like the rest of the file section.
//   - caps (max_concurrent/submit_rpm/max_waits): the same tiers, resolved
//     ONCE at construction — a limiter's size is not a dial to turn under
//     in-flight callers. A file edit that disagrees with the resolved
//     values sets restart_required in the state below; enabled/token/policy
//     edits never do.

import (
	"os"
	"strconv"
	"strings"

	"github.com/yosukeno/pogo/mcpclient"
)

// ServeState is GET /api/mcp's server_state, and serveWeb's startup line:
// the effective server-side configuration after the tiers above resolve.
type ServeState struct {
	Enabled bool `json:"enabled"`
	// TokenRequired is true when requests must bear a credential — including
	// the misconfigured case (a token_env whose variable is missing), which
	// fails closed and therefore also "requires" one.
	TokenRequired bool   `json:"token_required"`
	Policy        string `json:"policy"`
	// Source is where the enabled/token words come from: "env", "file", or
	// "default" (nothing configured anywhere — the static baseline included,
	// which is code, not configuration).
	Source string `json:"source"`
	// RestartRequired flags drift the hot path cannot close: a cap value in
	// the file differs from the one the process resolved at startup.
	// Enabled/token/policy edits never set this.
	RestartRequired bool     `json:"restart_required"`
	Endpoints       []string `json:"endpoints"`
	// IncludeTools is the tool whitelist in effect — the file section's own
	// words (it has no other tier). Nil means unfiltered: tools/list shows
	// everything.
	IncludeTools []string `json:"include_tools,omitempty"`
}

// setFileConfig hot-swaps the file's server section after PUT /api/mcp saved
// a new mcp.json. Enabled, token, policy and include_tools follow on the
// next request; the caps do not — see the layer struct comment.
func (l *mcpLayer) setFileConfig(sc *mcpclient.ServeConfig) {
	l.file.Store(sc)
}

// effectiveEnabled reports whether the MCP endpoints currently answer.
func (l *mcpLayer) fileConfig() *mcpclient.ServeConfig {
	if l.parent != nil {
		return l.parent.fileConfig()
	}
	return l.file.Load()
}

func (l *mcpLayer) effectiveEnabled() bool {
	if os.Getenv("POGO_MCP") == "1" {
		return true
	}
	if sc := l.fileConfig(); sc != nil {
		return sc.Enabled
	}
	return l.staticOn
}

// effectiveToken resolves the credential MCP requests must currently bear.
// Empty means no authentication (see the file comment — including the legacy
// fallback this function deliberately does not perform). A token_env that
// names a missing variable is an error, not an empty token: "the operator
// believes authentication is on while it resolves to nothing" is the
// dangerous direction, so the gate fails closed (503 + a loud log line —
// see server.go).
func (l *mcpLayer) effectiveToken() (string, error) {
	if t := strings.TrimSpace(os.Getenv("POGO_MCP_TOKEN")); t != "" {
		return t, nil
	}
	if sc := l.fileConfig(); sc != nil {
		if tok, err := sc.ResolvedToken(os.Getenv); tok != "" || err != nil {
			return tok, err
		}
	}
	if l.static.Token != "" {
		return l.static.Token, nil
	}
	return "", nil
}

// effectivePolicy is the approval mode fresh MCP submissions currently start
// under. ParseMode failures fall through to the next tier rather than
// erroring: a bad env value is already a startup error (serveWeb checks it),
// and a bad file value cannot be saved.
func (l *mcpLayer) effectivePolicy() Mode {
	if m, ok := ParseMode(strings.TrimSpace(os.Getenv("POGO_MCP_POLICY"))); ok {
		return m
	}
	if sc := l.fileConfig(); sc != nil && sc.PolicyRaw != "" {
		if m, ok := ParseMode(sc.PolicyRaw); ok {
			return m
		}
	}
	if m, ok := ParseMode(l.static.Policy); ok {
		return m
	}
	return ModeStandard
}

// restartRequired reports whether a cap value present in the file disagrees
// with the process-resolved one — the only drift the hot path cannot close.
// Caps the file does not mention do not count: their resolved value is not
// the file's business.
func (l *mcpLayer) restartRequired() bool {
	sc := l.fileConfig()
	if sc == nil {
		return false
	}
	return (sc.MaxConcurrentRaw > 0 && sc.MaxConcurrent() != l.maxConcurrent) ||
		(sc.SubmitRPMRaw > 0 && sc.SubmitRPM() != l.submits.max) ||
		(sc.MaxWaitsRaw > 0 && sc.MaxWaits() != cap(l.waitSlots))
}

// serveState assembles the effective view, one function so GET /api/mcp and
// serveWeb's startup line can never describe different configurations.
func (l *mcpLayer) serveState() ServeState {
	tok, terr := l.effectiveToken()
	st := ServeState{
		Enabled:         l.effectiveEnabled(),
		TokenRequired:   tok != "" || terr != nil,
		Policy:          string(l.effectivePolicy()),
		Source:          l.serveSource(),
		RestartRequired: l.restartRequired(),
		Endpoints:       []string{"/mcp", "/sse"},
	}
	if sc := l.fileConfig(); sc != nil {
		st.IncludeTools = sc.IncludeTools
	}
	return st
}

// toolAllowed reports whether the whitelist currently exposes a tool. Read
// per request (the middleware in mcp.go holds no copy), so a PUT edits the
// exposed surface without a restart. Absent section or empty list means
// unfiltered — never "expose nothing".
func (l *mcpLayer) toolAllowed(name string) bool {
	sc := l.fileConfig()
	if sc == nil {
		return true
	}
	return sc.Includes(name)
}

// serveSource is the state view's source field: env when it speaks, file when
// a server section exists, default otherwise.
func (l *mcpLayer) serveSource() string {
	if os.Getenv("POGO_MCP") == "1" || strings.TrimSpace(os.Getenv("POGO_MCP_TOKEN")) != "" {
		return "env"
	}
	if l.fileConfig() != nil {
		return "file"
	}
	return "default"
}

// resolveCaps fixes the three demand caps at construction, env > file >
// static > default. The file's clamped accessors are used for its tier, so a
// file value past the ceiling takes effect clamped — and restartRequired's
// comparison against the same accessors stays consistent.
func resolveCaps(opts MCPOptions, sc *mcpclient.ServeConfig) (maxConcurrent int, submits *submitLimiter, waitSlots chan struct{}) {
	maxConcurrent = capFromEnv("POGO_MCP_MAX_CONCURRENT")
	if maxConcurrent <= 0 && sc != nil && sc.MaxConcurrentRaw > 0 {
		maxConcurrent = sc.MaxConcurrent()
	}
	if maxConcurrent <= 0 {
		maxConcurrent = opts.MaxConcurrent
	}
	if maxConcurrent <= 0 {
		maxConcurrent = 8
	}

	rpm := capFromEnv("POGO_MCP_SUBMIT_RPM")
	if rpm <= 0 && sc != nil && sc.SubmitRPMRaw > 0 {
		rpm = sc.SubmitRPM()
	}
	if rpm <= 0 {
		rpm = opts.SubmitRPM
	}
	if rpm <= 0 {
		rpm = 60
	}

	waits := capFromEnv("POGO_MCP_MAX_WAITS")
	if waits <= 0 && sc != nil && sc.MaxWaitsRaw > 0 {
		waits = sc.MaxWaits()
	}
	if waits <= 0 {
		waits = opts.MaxWaits
	}
	if waits <= 0 {
		waits = 32
	}
	return maxConcurrent, newSubmitLimiter(rpm), make(chan struct{}, waits)
}

// capFromEnv reads one cap from the environment; <= 0 means "env does not
// speak" — serveWeb's historic parsing, kept for the same three variables.
func capFromEnv(name string) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return 0
}

// MCPServeState is the effective MCP server-side configuration, for
// serveWeb's startup line. The zero value (Enabled false) is the answer when
// the layer is absent.
func (s *Server) MCPServeState() ServeState {
	if s.mcp == nil {
		return ServeState{}
	}
	return s.mcp.serveState()
}
