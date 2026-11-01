package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/mcpobserve"
	pogotools "github.com/yosukeno/pogo/tools"
)

// The pool owns one conn per configured server. A conn is a keeper goroutine
// that connects, holds the session, and — because the SDK deliberately does
// no session-level reconnection — reconnects on its own: when the session
// dies, the keeper marks the conn disconnected and retries forever on an
// exponential backoff, since the operator may simply be restarting the other
// side. Reconcile aligns the conn set with a new configuration; Snapshot is
// the read side, projecting the connected servers' tools into the harness's
// registry shape.
//
// Two rules keep the concurrency honest:
//
//   - A *mcp.ClientSession is already concurrency-safe (the SDK says so), so
//     the session itself gets no lock — only an atomic pointer, so a
//     reconnect publishes a new catalog and execution bindings. Old bindings
//     fail explicitly instead of calling a new session with an old schema.
//   - Pool.mu is never held while talking to a server. Snapshot copies the
//     conn references, drops the lock, then assembles.

// Pool states, exported for the /api/mcp/status consumers that compare
// against them.
const (
	StateConnecting   = "connecting"
	StateConnected    = "connected"
	StateDisconnected = "disconnected"
)

// Large server catalogs earn an observational warning, never truncation.
// Model-facing exposure is controlled separately by metadata/discovery settings;
// the local registry retains every accepted tool from the selected servers.
const warnServerTools = 20

const (
	keepAliveInterval  = 30 * time.Second
	keepAliveThreshold = 3
	// listChangedDebounce lets a burst of tools/list_changed notifications —
	// a server rewriting its whole catalog sends one per change — settle
	// into a single refetch.
	listChangedDebounce = 500 * time.Millisecond
	backoffCap          = 30 * time.Second
)

// Status is one server's /api/mcp/status projection.
type Status struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Tools int    `json:"tools"`
	// LastError includes catalog failures. A connected session can have a stale
	// catalog: retain the last successful list and expose the failure separately
	// from the transport state. A successful complete pull clears the error.
	LastError        string    `json:"last_error,omitempty"`
	CatalogStale     bool      `json:"catalog_stale,omitempty"`
	CatalogUpdatedAt time.Time `json:"catalog_updated_at,omitempty"`
	ProtocolVersion  string    `json:"protocol_version,omitempty"`
	ServerName       string    `json:"server_name,omitempty"`
	ServerVersion    string    `json:"server_version,omitempty"`
	Instructions     string    `json:"instructions,omitempty"`
	// Since is when the current state began — how long it has been down, or
	// how long it has been up, depending on State.
	Since time.Time `json:"since"`
}

// Pool maintains the configured set of MCP connections.
type Pool struct {
	logger *slog.Logger

	mu     sync.Mutex
	conns  map[string]*conn
	closed bool
}

// NewPool starts an empty pool; Reconcile fills it. A nil logger takes
// slog.Default().
func NewPool(logger *slog.Logger) *Pool {
	if logger == nil {
		logger = slog.Default()
	}
	return &Pool{logger: logger, conns: map[string]*conn{}}
}

// Reconcile aligns the connection set with cfg: servers that appeared are
// connected, servers that disappeared are closed, and a server whose
// configuration changed (by fingerprint) is closed and reconnected. Connects
// run in the background — Reconcile never blocks on a network — and
// WaitReady reports how the first round went.
func (p *Pool) Reconcile(ctx context.Context, cfg Config) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	desired := make(map[string]*ServerConfig, len(cfg.Servers))
	for name, sc := range cfg.Servers {
		if sc.IsEnabled() {
			desired[name] = sc
		}
	}
	for name, c := range p.conns {
		nc, ok := desired[name]
		if ok && fingerprint(nc) == c.fingerprint {
			continue
		}
		why := "removed from configuration"
		if ok {
			why = "configuration changed; reconnecting"
		}
		p.logger.Info("mcp server connection closing", "server", name, "reason", why)
		c.stop()
		delete(p.conns, name)
	}
	names := make([]string, 0, len(desired))
	for name := range desired {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic dial order, like the loader's stable tool order
	for _, name := range names {
		if _, ok := p.conns[name]; ok {
			continue
		}
		c := newConn(ctx, name, desired[name], p.logger)
		p.conns[name] = c
		go c.run()
	}
}

// WaitReady blocks until every currently configured server's first connect
// attempt has finished, then reports the required ones that are not
// connected. For the CLI/startup path: an optional server degrades to
// "reconnecting", a required one is why the session cannot do its job.
func (p *Pool) WaitReady(ctx context.Context) error {
	p.mu.Lock()
	conns := make([]*conn, 0, len(p.conns))
	for _, c := range p.conns {
		conns = append(conns, c)
	}
	p.mu.Unlock()
	for _, c := range conns {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.first:
		}
	}
	var failed []string
	for _, c := range conns {
		if c.cfg.Required && c.status().State != StateConnected {
			failed = append(failed, fmt.Sprintf("%s (%s)", c.name, c.status().LastError))
		}
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		return fmt.Errorf("required MCP servers not connected: %s", strings.Join(failed, ", "))
	}
	return nil
}

// Snapshot projects the connected servers into tools for the registry, the
// wire names of tools on trusted servers, and the per-server status. Every
// accepted tool is retained, in deterministic server-name then catalog order.
func (p *Pool) Snapshot() (toolsOut []pogotools.Tool, trusted []string, status []Status) {
	return p.SnapshotServers(nil)
}

// SnapshotServers is Snapshot narrowed to the named servers: a nil set is
// the whole pool, a non-nil (possibly empty) set drops every connection not
// in it before projecting tools or trust, so a caller with a selection is
// never offered tools or approval trust from a server it did not select.
func (p *Pool) SnapshotServers(only map[string]bool) (toolsOut []pogotools.Tool, trusted []string, status []Status) {
	p.mu.Lock()
	conns := make([]*conn, 0, len(p.conns))
	for name, c := range p.conns {
		if only != nil && !only[name] {
			continue
		}
		conns = append(conns, c)
	}
	p.mu.Unlock()
	sort.Slice(conns, func(i, j int) bool { return conns[i].name < conns[j].name })

	seenNames := make(map[string]bool)
	for _, c := range conns {
		catalog, st := c.snapshot()
		status = append(status, st)
		if st.State != StateConnected {
			// A disconnected server's stale catalog is not offered: the
			// tools would fail on call anyway, and an absent tool is a
			// clearer signal than one that errors — Options.ReadOnly's
			// "withheld rather than refused" reasoning, applied to a server
			// that is not there.
			continue
		}
		for _, t := range catalog {
			// Sanitization can collide even across server/tool boundaries. Keep
			// the first entry in deterministic server order, including its trust.
			if seenNames[t.Name()] {
				p.logger.Warn("mcp wire name collision; tool dropped", "server", c.name, "tool", t.Name())
				continue
			}
			toolsOut = append(toolsOut, t)
			seenNames[t.Name()] = true
			if c.cfg.Trust {
				trusted = append(trusted, t.Name())
			}
		}
	}
	return toolsOut, trusted, status
}

// ServerCatalog pairs one server's status with its tool wrappers — connected
// or not, so an interface can show a down server's cached catalogue greyed
// rather than absent. Display only: execution still goes through Snapshot,
// which withholds a disconnected server's tools.
type ServerCatalog struct {
	Status Status
	Tools  []*pogotools.MCPTool
}

// Catalogs projects every connection (or the `only` subset, same semantics as
// SnapshotServers) into server-name-sorted status/catalog pairs. No cap and
// no collision dropping: this is the presentational whole, not the
// registry-bound selection.
func (p *Pool) Catalogs(only map[string]bool) []ServerCatalog {
	p.mu.Lock()
	conns := make([]*conn, 0, len(p.conns))
	for name, c := range p.conns {
		if only != nil && !only[name] {
			continue
		}
		conns = append(conns, c)
	}
	p.mu.Unlock()
	sort.Slice(conns, func(i, j int) bool { return conns[i].name < conns[j].name })

	out := make([]ServerCatalog, 0, len(conns))
	for _, c := range conns {
		catalog, st := c.snapshot()
		out = append(out, ServerCatalog{Status: st, Tools: catalog})
	}
	return out
}

// Close stops every keeper and session. For process shutdown.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	conns := make([]*conn, 0, len(p.conns))
	for _, c := range p.conns {
		conns = append(conns, c)
	}
	p.conns = map[string]*conn{}
	p.mu.Unlock()
	for _, c := range conns {
		c.stop()
	}
	for _, c := range conns {
		<-c.done
	}
}

// fingerprint detects "the configuration changed" for Reconcile. Struct
// fields marshal in declaration order and map keys sort, so encoding/json is
// already deterministic — the comparison is byte equality, not a hash.
func fingerprint(cfg *ServerConfig) string {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Sprintf("%p", cfg) // unreachable for this shape; never equal across instances
	}
	return string(data)
}

// conn is one server's keeper: context, current session, and the last known
// catalog. Exported state goes through status()/catalog(), both of which
// copy under the conn's mutex.
type conn struct {
	name        string
	cfg         *ServerConfig
	fingerprint string
	logger      *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed when the keeper goroutine exits
	first  chan struct{} // closed when the first connect attempt settles
	once   sync.Once     // guards first

	// session is nil while down. Each tool also captures its originating
	// session and catalog entry, and checks both before dispatch.
	session atomic.Pointer[mcp.ClientSession]
	// listChanged is the debounce channel the ToolListChangedHandler signals
	// (cap 1: a pending refresh subsumes any number of further signals).
	listChanged chan struct{}
	listVersion atomic.Uint64
	// refreshGate serializes initial, notification and pre-run pulls. Acquisition
	// observes the caller's deadline, unlike a mutex held across network I/O.
	refreshGate chan struct{}

	mu              sync.Mutex
	state           string
	lastErr         string
	serverName      string
	serverVersion   string
	protocolVersion string
	instructions    string
	since           time.Time
	tools           []*pogotools.MCPTool
	catalogSession  *mcp.ClientSession
	lastAttempt     time.Time
	catalogUpdated  time.Time
	catalogStale    bool
	listedVersion   uint64
}

func newConn(ctx context.Context, name string, cfg *ServerConfig, logger *slog.Logger) *conn {
	if logger == nil {
		// Probe passes whatever it was handed; the stderr forwarder must not
		// find a nil here.
		logger = slog.Default()
	}
	c := &conn{
		name: name, cfg: cfg, fingerprint: fingerprint(cfg), logger: logger,
		done: make(chan struct{}), first: make(chan struct{}),
		listChanged:  make(chan struct{}, 1),
		refreshGate:  make(chan struct{}, 1),
		catalogStale: true,
		state:        StateConnecting, since: time.Now(),
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
	return c
}

// run is the keeper loop: connect, hold until the session dies, back off,
// repeat — forever, because the operator may be restarting the other side
// and "the server is down" is a state, not a verdict. stop() ends it.
func (c *conn) run() {
	defer close(c.done)
	go c.watchListChanges()
	attempt := 0
	for {
		if attempt > 0 {
			d := backoff(attempt)
			c.logger.Debug("mcp server reconnect scheduled", "server", c.name, "in", d)
			select {
			case <-c.ctx.Done():
				return
			case <-time.After(d):
			}
		}
		cs, err := c.connect()
		if c.ctx.Err() != nil {
			if cs != nil {
				_ = cs.Close()
			}
			return
		}
		if err != nil {
			attempt++
			c.setDisconnected(err)
			c.once.Do(func() { close(c.first) })
			continue
		}
		// A session that completed its handshake and catalog pull resets the
		// backoff: the server was reachable, so its next outage starts the
		// schedule over from 1s rather than inheriting the previous outage's
		// 30s. A connect-and-die-immediately flap still pays the backoff for
		// each failed attempt.
		attempt = 0
		// AfterFunc rather than a watcher goroutine: the conn's context may
		// be cancelled by the caller of Reconcile without a stop(), and the
		// only thing that makes cs.Wait() return is Close().
		unwatch := context.AfterFunc(c.ctx, func() { _ = cs.Close() })
		c.setConnected(cs)
		c.once.Do(func() { close(c.first) })
		waitErr := cs.Wait()
		unwatch()
		// Clear the pointer before anything else: a call that lands between
		// now and the reconnect must fail fast, not write to a dead session.
		c.session.CompareAndSwap(cs, nil)
		if c.ctx.Err() != nil {
			return
		}
		c.setDisconnected(waitErr)
		attempt++ // = 1: the first reconnect already pays the 1s base backoff
	}
}

// connect performs the handshake and the first catalog pull under the
// startup timeout. Both halves are inside the budget: a server that answers
// initialize but hangs tools/list is as unready as one that never answers.
func (c *conn) connect() (*mcp.ClientSession, error) {
	transport, err := c.buildTransport()
	if err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "pogo"}, &mcp.ClientOptions{
		KeepAlive:                 keepAliveInterval,
		KeepAliveFailureThreshold: keepAliveThreshold,
		// The handler runs on the SDK's dispatch and blocks later
		// notifications while it works, so it only signals; the debounce
		// goroutine does the refetch.
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			c.listVersion.Add(1)
			select {
			case c.listChanged <- struct{}{}:
			default:
			}
		},
	})
	sctx, cancel := context.WithTimeout(c.ctx, c.cfg.StartupTimeout())
	defer cancel()
	cs, err := client.Connect(sctx, transport, nil)
	if err != nil {
		return nil, err
	}
	c.session.Store(cs)
	if err := c.refreshCatalog(sctx, cs); err != nil {
		c.session.CompareAndSwap(cs, nil)
		_ = cs.Close()
		return nil, err
	}
	return cs, nil
}

// call is the unbound transport helper. Catalog tools use callBound below.
func (c *conn) call(ctx context.Context, toolName string, args json.RawMessage) (*mcp.CallToolResult, error) {
	cs := c.session.Load()
	if cs == nil {
		return nil, fmt.Errorf("mcp server %q is reconnecting; try again shortly", c.name)
	}
	params := &mcp.CallToolParams{Name: toolName, Arguments: args}
	injectTraceMeta(ctx, params)
	return cs.CallTool(ctx, params)
}

// injectTraceMeta continues the caller's W3C trace on an outbound tools/call
// via params._meta (MCP 2026-07-28), over stdio and HTTP/SSE transports
// alike. The merged map is freshly built: shared/request meta maps are
// copied, never mutated, and without an active parent span _meta is left
// untouched.
func injectTraceMeta(ctx context.Context, params *mcp.CallToolParams) {
	trace := mcpobserve.TraceMeta(ctx)
	if trace == nil {
		return
	}
	merged := make(map[string]any, len(params.GetMeta())+len(trace))
	for k, v := range params.GetMeta() {
		merged[k] = v
	}
	for k, v := range trace {
		merged[k] = v
	}
	params.SetMeta(merged)
}

// refreshCatalog is the initial complete pull. Subsequent pulls go through
// refreshCurrent so simultaneous runs and notifications share one attempt.
func (c *conn) refreshCatalog(ctx context.Context, cs *mcp.ClientSession) error {
	return c.refresh(ctx, cs, false)
}

// auditCatalogDiff logs catalog changes at WARN — all three kinds, additions
// included. This is the rug-pull tripwire: a server that re-describes a tool
// mid-relationship (same name, new instructions for the model, a wider
// schema) is the classic malicious-MCP move, and the log is the only place
// an operator can see it happened. A run keeps its definitions; changed
// execution bindings fail explicitly until the next run takes a new snapshot.
func (c *conn) auditCatalogDiff(prev, next []*pogotools.MCPTool) {
	if len(prev) == 0 {
		// An initial population has no previous definitions to compare.
		return
	}
	shape := func(list []*pogotools.MCPTool) map[string]string {
		m := make(map[string]string, len(list))
		for _, t := range list {
			m[t.Name()] = t.DefinitionFingerprint()
		}
		return m
	}
	oldM, newM := shape(prev), shape(next)
	names := make([]string, 0, len(oldM)+len(newM))
	for n := range oldM {
		names = append(names, n)
	}
	for n := range newM {
		if _, ok := oldM[n]; !ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		o, had := oldM[n]
		w, has := newM[n]
		switch {
		case had && !has:
			c.logger.Warn("mcp tool disappeared from a live server", "server", c.name, "tool", n)
		case !had && has:
			c.logger.Warn("mcp tool appeared on a live server", "server", c.name, "tool", n)
		case o != w:
			c.logger.Warn("mcp tool changed while connected (rug-pull check: review the server's update before trusting the new description or schema)",
				"server", c.name, "tool", n)
		}
	}
}

// watchListChanges debounces tools/list_changed into catalog refetches until
// the conn stops.
func (c *conn) watchListChanges() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.listChanged:
		}
		timer := time.NewTimer(listChangedDebounce)
		select {
		case <-c.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		// Drain whatever arrived during the debounce window; one refetch
		// answers them all.
		for {
			select {
			case <-c.listChanged:
			default:
				goto drained
			}
		}
	drained:
		ctx, cancel := context.WithTimeout(c.ctx, CatalogRefreshTimeout)
		err := c.refreshCurrent(ctx)
		cancel()
		if err != nil {
			c.logger.Warn("mcp tool list refresh failed", "server", c.name, "error", err)
		}
	}
}

func (c *conn) setConnected(cs *mcp.ClientSession) {
	c.mu.Lock()
	c.state = StateConnected
	c.lastErr = ""
	c.since = time.Now()
	if ir := cs.InitializeResult(); ir != nil {
		c.protocolVersion = ir.ProtocolVersion
		c.instructions = ir.Instructions
		if ir.ServerInfo != nil {
			c.serverName = ir.ServerInfo.Name
			c.serverVersion = ir.ServerInfo.Version
		}
	}
	c.mu.Unlock()
	c.logger.Info("mcp server connected",
		"server", c.name, "protocol", c.protocolVersion,
		"implementation", c.serverName+"@"+c.serverVersion, "tools", len(c.catalog()))
}

func (c *conn) setDisconnected(err error) {
	msg := "connection lost"
	if err != nil {
		msg = err.Error()
	}
	c.mu.Lock()
	// A first failed attempt arrives in StateConnecting; everything after is
	// a server that was (or never became) connected. Both read as
	// disconnected to a status consumer — "connecting" only describes a conn
	// whose first attempt is still in flight.
	c.state = StateDisconnected
	c.lastErr = msg
	c.since = time.Now()
	// Keep the last valid list for diagnostics and comparison on recovery.
	// Snapshot withholds it while disconnected; an outage is not an empty list.
	c.catalogStale = true
	c.mu.Unlock()
	c.logger.Warn("mcp server disconnected", "server", c.name, "error", msg)
}

func (c *conn) status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statusLocked()
}

func (c *conn) statusLocked() Status {
	return Status{
		Name: c.name, State: c.state, Tools: len(c.tools),
		LastError: c.lastErr, ProtocolVersion: c.protocolVersion,
		ServerName: c.serverName, ServerVersion: c.serverVersion,
		Instructions: c.instructions, Since: c.since,
		CatalogStale: c.catalogStale, CatalogUpdatedAt: c.catalogUpdated,
	}
}

func (c *conn) snapshot() ([]*pogotools.MCPTool, Status) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*pogotools.MCPTool(nil), c.tools...), c.statusLocked()
}

// catalog copies the current tool slice; callers never hold the mutex.
func (c *conn) catalog() []*pogotools.MCPTool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*pogotools.MCPTool(nil), c.tools...)
}

// stop cancels the keeper and closes the live session, which is what makes
// cs.Wait() return and the run loop notice the cancellation.
func (c *conn) stop() {
	c.cancel()
	if cs := c.session.Load(); cs != nil {
		_ = cs.Close()
	}
}

// backoff is exponential from 1s, doubling per attempt, capped at 30s, with
// full jitter — several servers dying together (a shared host restarting)
// must not reconnect in lockstep. attempt counts from 1.
func backoff(attempt int) time.Duration {
	d := time.Second << min(attempt-1, 5) // 1s, 2s, 4s, … 32s, capped below
	if d > backoffCap {
		d = backoffCap
	}
	return time.Duration(rand.Int64N(int64(d)))
}

// --- transports -------------------------------------------------------------

// buildTransport constructs the SDK transport for the conn's configuration.
func (c *conn) buildTransport() (mcp.Transport, error) {
	cfg := c.cfg
	if cfg.Command != "" {
		cmd := buildCommand(cfg, os.Getenv)
		cmd.Stderr = newStderrLogger(c.logger, cfg.Command)
		// TerminateDuration left at the SDK default (5s of grace after stdin
		// closes, then SIGTERM): a child that ignores its stdin closing gets
		// a bounded farewell.
		return &mcp.CommandTransport{Command: cmd}, nil
	}
	hc, err := c.httpClient()
	if err != nil {
		return nil, err
	}
	if cfg.Transport == "sse" {
		return &mcp.SSEClientTransport{Endpoint: cfg.URL, HTTPClient: hc}, nil
	}
	// MaxRetries disabled: it only governs the SDK's internal SSE-stream
	// resume, and reconnection is the keeper's job — an SDK quietly
	// resurrecting a stream would be a second state machine the pool's
	// status cannot see.
	return &mcp.StreamableClientTransport{Endpoint: cfg.URL, HTTPClient: hc, MaxRetries: -1}, nil
}

// httpClient builds the HTTP client with the configured credential material
// baked into its transport — cmd/mcp-probe's bearerTransport pattern,
// extended with the custom headers. The token comes from bearer_token when
// inlined, else from the variable bearer_token_env names (Validate guarantees
// the two never coexist).
func (c *conn) httpClient() (*http.Client, error) {
	cfg := c.cfg
	token := cfg.BearerToken
	if token == "" && cfg.BearerTokenEnv != "" {
		token = os.Getenv(cfg.BearerTokenEnv)
		if token == "" {
			// A hard failure at connect, not a silent unauthenticated
			// session: the config says the endpoint needs a credential, and
			// connecting without one produces a 401 with none of the context.
			return nil, fmt.Errorf("bearer_token_env names $%s, which is not set in the environment", cfg.BearerTokenEnv)
		}
	}
	headers := make(map[string]string, len(cfg.Headers))
	for k, v := range cfg.Headers {
		// Expanded from the live environment — see the Env field comment
		// for why expansion reads the unsanitized set.
		headers[k] = os.Expand(v, os.Getenv)
	}
	if token == "" && len(headers) == 0 {
		return &http.Client{}, nil
	}
	// The credential's scope is the configured endpoint's origin. Parse
	// failure leaves origin nil, which headerTransport treats as "stamp
	// nothing" rather than "stamp everything".
	var origin *url.URL
	if u, err := url.Parse(cfg.URL); err == nil {
		origin = u
	}
	ht := &headerTransport{token: token, headers: headers, origin: origin}
	return &http.Client{Transport: ht, CheckRedirect: ht.checkRedirect}, nil
}

// headerTransport stamps the credential and custom headers on requests bound
// for the configured origin — and nowhere else. The headers are injected in
// RoundTrip, *below* net/http's redirect logic, so the default cross-domain
// Authorization stripping never sees them; the origin binding here is the
// only thing standing between a redirect to an attacker host and a leaked
// bearer token.
type headerTransport struct {
	token   string
	headers map[string]string
	origin  *url.URL // credential scope: scheme + host + effective port
}

// sameOrigin reports whether u sits inside the credential scope. Scheme is
// part of the comparison, so an https -> http downgrade redirect is "cross
// origin" even when the host matches.
func (t *headerTransport) sameOrigin(u *url.URL) bool {
	if t.origin == nil || u == nil {
		return false
	}
	return strings.EqualFold(u.Scheme, t.origin.Scheme) &&
		strings.EqualFold(u.Hostname(), t.origin.Hostname()) &&
		effectivePort(u) == effectivePort(t.origin)
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	}
	return ""
}

func (t *headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !t.sameOrigin(r.URL) {
		// Defense in depth: checkRedirect already refuses to follow a
		// cross-origin redirect, but a caller-constructed request (or a bug
		// above) must never carry the credential to a stranger either.
		return http.DefaultTransport.RoundTrip(r)
	}
	// Cloned rather than mutated in place: the transport may retry or
	// redirect a request, and the caller's copy is not ours to write.
	r = r.Clone(r.Context())
	if t.token != "" {
		r.Header.Set("Authorization", "Bearer "+t.token)
	}
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// checkRedirect keeps Go's default behaviour (follow, at most 10 times) for
// same-origin redirects and refuses every hop that would leave the
// credential scope: a different host or port, or a scheme downgrade.
// ErrUseLastResponse hands the 3xx back to the caller instead of silently
// following without the credential — a silently-broken request looks like a
// server failure, a visible 3xx is diagnosable.
func (t *headerTransport) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if !t.sameOrigin(req.URL) {
		return http.ErrUseLastResponse
	}
	return nil
}

// --- stdio child process ----------------------------------------------------

// buildCommand is the testable heart of the stdio transport: environment
// sanitization and $VAR expansion live here, where a unit test can pin them
// without spawning a process.
func buildCommand(cfg *ServerConfig, getenv func(string) string) *exec.Cmd {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = childEnv(os.Environ(), cfg.Env, getenv)
	if cfg.Cwd != "" {
		cmd.Dir = cfg.Cwd
	}
	return cmd
}

// childEnv is the child's environment: the inherited set minus anything that
// looks like a credential, plus the config's explicit entries with $VAR
// references expanded from the *unsanitized* environment (getenv).
//
// The sanitization exists because the SDK leaves cmd.Env alone, and exec's
// default is to hand the child everything — including the API keys pogo
// itself runs on. A tool server has no business reading KIMI_API_KEY out of
// its environment; stripping is the default, and the explicit env map is the
// informed-consent channel back (declaring a value in the file is the
// operator opting the child in, Gemini CLI's semantics).
//
// False positives (MONKEY, KEYBOARD_LAYOUT) are the cheap direction: the
// child can still get any variable back through an explicit entry. POGO_ is
// stripped too — those variables steer the harness (sandbox mode, config
// paths), and a child that inherited POGO_CONFIG would read it as its own.
func childEnv(inherit []string, explicit map[string]string, getenv func(string) string) []string {
	out := make([]string, 0, len(inherit)+len(explicit))
	for _, kv := range inherit {
		name, _, _ := strings.Cut(kv, "=")
		if sensitiveEnvName(name) {
			continue
		}
		if _, overridden := explicit[name]; overridden {
			continue
		}
		out = append(out, kv)
	}
	// Sorted for determinism: map iteration order would make two runs of the
	// same config produce different environments, an unnecessary moving part.
	keys := make([]string, 0, len(explicit))
	for k := range explicit {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+os.Expand(explicit[k], getenv))
	}
	return out
}

// sensitiveEnvName reports whether a variable name looks like it carries a
// credential or steers this harness. Case-insensitive: Windows environment
// block is, and a rule that misses "api_token" because it was spelled
// "API_Token" is not a rule.
func sensitiveEnvName(name string) bool {
	up := strings.ToUpper(name)
	if strings.HasPrefix(up, "POGO_") {
		return true
	}
	for _, pat := range []string{"TOKEN", "SECRET", "PASSWORD", "KEY", "AUTH", "CREDENTIAL"} {
		if strings.Contains(up, pat) {
			return true
		}
	}
	return false
}

// stderrLogger is an io.Writer that turns a child server's stderr into slog
// lines. The SDK leaves cmd.Stderr alone, whose default discards it — and a
// stdio server's stderr is the only place its "why I won't start" lives, so
// discarding it is how a dead server becomes an unexplained connect error.
//
// Implemented as a writer rather than a pipe + scanner goroutine so the
// process lifecycle owns the copy: exec spawns its own copier goroutine,
// which ends when the child does, and there is no pipe for us to forget to
// close. A final line with no trailing newline stays buffered — a tolerable
// loss for a diagnostics channel.
type stderrLogger struct {
	logger *slog.Logger
	source string

	mu  sync.Mutex
	buf []byte
}

// maxStderrLine caps one buffered line: a server that never emits a newline
// must not grow this without bound.
const maxStderrLine = 4096

func newStderrLogger(logger *slog.Logger, source string) *stderrLogger {
	return &stderrLogger{logger: logger, source: source}
}

func (w *stderrLogger) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSuffix(string(w.buf[:i]), "\r")
		w.buf = w.buf[i+1:]
		if line != "" {
			w.logger.Debug("mcp server stderr", "server", w.source, "line", line)
		}
	}
	if len(w.buf) > maxStderrLine {
		w.logger.Debug("mcp server stderr (line truncated)", "server", w.source, "line", string(w.buf[:maxStderrLine]))
		w.buf = w.buf[:0]
	}
	return len(p), nil
}
