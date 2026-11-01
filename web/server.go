package web

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/internal/browserrelay"
	"github.com/yosukeno/pogo/mcpclient"
	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/observability"
	"github.com/yosukeno/pogo/prefs"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/skillobserve"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
	"nhooyr.io/websocket"
)

// keepalive interval for SSE comment frames. Without them an idle stream can be
// closed by an intermediary while the agent is simply thinking.
const keepalive = 20 * time.Second

type ServerOptions struct {
	// TLSConfig enables an HTTPS and TURN/TLS listener on the same web port.
	// Nil retains ordinary HTTP and TURN/TCP for local deployments.
	TLSConfig *tls.Config
	// Office is an explicitly configured web editor. Nil reads the POGO_OFFICE
	// environment; an empty environment leaves the feature disabled.
	Office *OfficeOptions
	// Token is required on every request, including the SSE stream. Empty means
	// no authentication, which the caller must only allow for a loopback bind.
	Token string
	// DevProxy points the non-API routes at a vite dev server during development.
	DevProxy string
	// Panels are external web applications shown as dock sheets (-web-panel,
	// or the runtime POST /api/panels the UI's add-shortcut dialog uses).
	// Order is display order.
	Panels []Panel
	// PanelStore persists runtime-registered panels across restarts. Empty
	// disables persistence (tests). Flag panels are never stored — they are
	// config, re-supplied on every start.
	PanelStore string
	// PanelLoopbackMap rewrites loopback hosts in panel URLs to this host
	// (Docker Desktop's host.docker.internal): the proxy fetches server-side,
	// and inside a container the browser user's "localhost" is the container.
	// Empty leaves URLs as typed.
	PanelLoopbackMap string
	// PanelAutoRegister lets the browser pane register a cross-origin panel
	// silently when navigation needs one (POGO_PANEL_AUTO_REGISTER=1). Off by
	// default: registration is the moment an arbitrary page becomes
	// same-origin with the token-holding UI, and a grant like that has to be a
	// human's click, not a side effect of where the agent was told to go.
	PanelAutoRegister bool
	// AllowedOrigins are extra browser origins accepted besides same-origin.
	AllowedOrigins []string
	// UsersFile enables password login when set: the login page replaces the
	// token gate, accounts live in this JSON file (see web/auth.go), and each
	// account gets its own session space — the one switch that turns the
	// server from the shared-token single-user arrangement to per-user
	// isolation and closes the shared token channel.
	UsersFile string
	// StartersStore persists the user-edited starter cards (the empty state's
	// shortcuts). Empty disables editing: the skill-provided starters.json is
	// the whole story, as it was before the editor existed.
	StartersStore string
	// PrefsStore is where the settings dialog's non-provider preferences live
	// (spill retention days; see prefs/). Empty falls back to prefs.Path(),
	// which is also what the CLI's -spill-prune reads — one file, one answer.
	PrefsStore string
	// SharesStore persists shared-answer snapshots (web/share.go). Empty means
	// in-memory shares that do not survive a restart — fine for tests, wrong
	// for a deployment, so serve.go always sets it.
	SharesStore string
	// MCPClientStore is where the MCP *client* configuration lives (mcp.json —
	// the external servers this agent connects to, edited through /api/mcp;
	// see mcpclient/). Empty falls back to mcpclient.Path(), the same default
	// the CLI resolves — one file, one answer, as with PrefsStore.
	MCPClientStore string
	// MCP, when non-nil, serves the MCP endpoints (/mcp Streamable HTTP, /sse
	// the deprecated 2024-11-05 transport) so other agents can drive this one
	// over the network. See mcp.go for the placement decisions. Off means the
	// paths do not exist at all, not that they 401.
	MCP *MCPOptions
	// MCPLog tunes the request log (mcplog.go). Nil means defaults (ring
	// 1000, history on with a state-dir database when serve.go fills it in);
	// it is separate from MCP because serving config now lives in mcp.json's
	// server section while logging is a startup-time operator decision.
	MCPLog *MCPLogOptions
	// OTLP enables bounded metadata-only HTTP/JSON trace export. Nil is off.
	OTLP            *observability.OTLPOptions
	OTLPConfigError bool
	Logger          *log.Logger
}

// Panel is an external web application the browser UI can open as a dock sheet.
// The sheet's iframe loads /panels/<name>/, which this server reverse-proxies
// to URL: same origin, so the Origin check covers it and no CORS is opened.
// Panels arrive from two channels: the -web-panel flag at startup, and the
// token-gated POST /api/panels at runtime (the add-shortcut dialog's proxy
// option) — same trust bar either way.
//
// A panel runs in the page's origin — register only backends you trust with the
// token that page already holds, the same bar -skill clears by rewriting the
// system prompt. Panel content is served without the token for the same reason
// the page and its assets are: it is content, not operations. A backend with
// mutations must do its own auth.
type Panel struct {
	Name      string `json:"name"`                 // shown in the sheet rail; no "/" or "="
	URL       string `json:"url"`                  // absolute http(s) backend
	ProxyMode string `json:"proxy_mode,omitempty"` // empty/passthrough keeps legacy base-path semantics
	SourceURL string `json:"source_url,omitempty"` // original address before container loopback mapping
}

// validatePanel rejects the two shapes that would break routing: a name that
// collides with the URL grammar, and a URL we cannot proxy to.
func validatePanel(p Panel) error {
	if p.ProxyMode != "" && p.ProxyMode != "passthrough" && p.ProxyMode != "compat" {
		return fmt.Errorf("unknown panel proxy mode %q", p.ProxyMode)
	}
	if p.Name == "" || strings.ContainsAny(p.Name, "/= \t") {
		return fmt.Errorf("invalid panel name %q: no spaces, slashes or '='", p.Name)
	}
	u, err := url.Parse(p.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid panel url %q: want absolute http(s)", p.URL)
	}
	if p.SourceURL != "" {
		source, err := url.Parse(p.SourceURL)
		if err != nil || source.Host == "" || (source.Scheme != "http" && source.Scheme != "https") {
			return fmt.Errorf("invalid panel source URL")
		}
	}
	return nil
}

type Server struct {
	htmlPreviewStreamCounts    map[string]int
	htmlPreviewMu              sync.Mutex
	htmlPreviews               map[string]*htmlPreviewCapability
	htmlPreviewClosed          bool
	browserAuthoringMu         sync.Mutex
	browserAuthoring           map[string]*browserAuthoringSession
	browserAuthoringOperations map[string]*browserAuthoringOperation
	browserAuthoringClosed     bool
	browserLiveMu              sync.Mutex
	browserLiveTickets         map[string]nativeBrowserLiveTicket
	browserLiveConnections     map[*nativeBrowserLiveConnection]struct{}
	browserLiveClosed          bool
	browserMediaRelay          *browserrelay.Service
	browserRelayPeers          []netip.AddrPort
	browserRelayResolveMu      sync.Mutex
	browserRelayWorkerHost     string
	browserRelayPortMin        uint16
	browserRelayPortMax        uint16
	agentMCPMu                 sync.Mutex
	agentMCP                   map[string]http.Handler
	mgr                        *Manager
	office                     *officeService
	officeConfigured           bool
	officeConfigError          bool
	opts                       ServerOptions
	mux                        *http.ServeMux
	proxy                      http.Handler
	// panelMu guards panelProxies and userPanels: panels can be registered at
	// runtime (POST /api/panels), so the once-at-startup map is gone.
	panelMu      sync.RWMutex
	panelProxies map[string]*httputil.ReverseProxy
	// userPanels are the runtime-registered ones (see PanelStore); flag panels
	// live in opts.Panels. GET /api/panels serves both, in that order.
	userPanels []Panel
	// auth is the password-login store; a zero store (no UsersFile) means the
	// classic token-only mode. See web/auth.go.
	auth *authStore
	// The MCP layer and its two HTTP handlers, built once in NewServer when
	// opts.MCP is set — or whenever MCPClientStore is set, in which case the
	// layer exists unconditionally and the "server" section of that file
	// (plus the POGO_MCP* environment) decides live whether the endpoints
	// answer (see mcpserve.go). One *mcp.Server backs both transports —
	// sessions are per-transport, the tool surface is not.
	mcp     *mcpLayer
	mcpHTTP http.Handler
	mcpSSE  http.Handler
	// mcplog records every request that touches the MCP endpoints (both
	// transports, every verdict — see mcplog.go). Nil when the MCP layer is
	// off; every method is nil-safe.
	mcplog                *mcpLogStore
	mcpCalls              *mcpobserve.Store
	skillCalls            *skillobserve.Store
	observation           *observability.Runtime
	otlp                  *observability.OTLPExporter
	otlpConfigError       bool
	observationPersistent bool
	closeOnce             sync.Once
	// shares is the shared-answer snapshot store (web/share.go). Never nil:
	// NewServer fills it even without a path, so handlers need no guard.
	shares            *shareStore
	workflowEmbed     *workflowEmbedRuntime
	workflowCallbacks *workflowCallbackRuntime
}

func NewServer(mgr *Manager, opts ServerOptions) (*Server, error) {
	// A users file implies isolation, and isolation without the ownership
	// catalog is a half-built deployment: refuse it here rather than let the
	// first session request crash on a nil store.
	if opts.UsersFile != "" && mgr.cfg.Catalog == nil {
		return nil, errors.New("web: per-user isolation requires the ownership catalog (multi-user misconfiguration)")
	}
	s := &Server{mgr: mgr, opts: opts, mux: http.NewServeMux()}
	mgr.mu.Lock()
	mgr.browserDownloadPrefsPath = s.prefsPath()
	mgr.mu.Unlock()
	// The settings dialog's turn cap survives restarts through prefs.json;
	// apply it before the first session is created so it never flashes the
	// flag default for one run. A broken prefs file keeps the flag value —
	// the cap is a budget, not a safety interlock, and the sweeper's
	// pause-on-error rule is about deleting files, not this.
	if p, err := prefs.Load(s.prefsPath()); err == nil {
		mgr.SetMaxTurnsOverride(p.MaxTurns)
		// The memory-distillation switch lives in the same file; sessions read
		// it live at a run's end, so applying it here only sets the baseline.
		mgr.SetMemoryAuto(p.MemoryAuto)
	}
	ready := false
	defer func() {
		if !ready {
			s.Close()
		}
	}()
	auth, err := newAuthStore(opts.UsersFile)
	if err != nil {
		return nil, err
	}
	s.auth = auth
	s.workflowEmbed, err = newWorkflowEmbedRuntime(mgr.cfg.SessionDir)
	if err != nil {
		return nil, err
	}
	if err := s.initOffice(); err != nil {
		return nil, err
	}
	// users.json accounts may carry their own MCP client credentials
	// ("mcp_tokens"): arm the per-owner pools the moment the store exists.
	if auth.Enabled() {
		mgr.SetMCPUserTokens(auth.allMcpTokens)
	}
	shares, err := newShareStore(opts.SharesStore)
	if err != nil {
		return nil, err
	}
	s.shares = shares
	if err := mgr.finishStudioShareReset(shares); err != nil {
		return nil, fmt.Errorf("remove legacy Skill shares: %w", err)
	}
	if opts.MCP != nil || opts.MCPClientStore != "" {
		// The getServer hook runs per request; handing back the same server
		// keeps the streamable handler in its stateful mode (sessions keyed by
		// Mcp-Session-Id), which is what a driver that polls wants.
		mcpOpts := MCPOptions{}
		if opts.MCP != nil {
			mcpOpts = *opts.MCP
		}
		if mcpOpts.Logger == nil {
			mcpOpts.Logger = opts.Logger
		}
		// The file's server section seeds the layer's dynamic half (PUT
		// /api/mcp hot-swaps it from then on). A file that will not load is a
		// log line and an absent section — the Manager's client pool makes
		// the same call for the same file.
		var fileSC *mcpclient.ServeConfig
		if opts.MCPClientStore != "" {
			if c, err := mcpclient.Load(opts.MCPClientStore); err != nil {
				if opts.Logger != nil {
					opts.Logger.Printf("mcp: cannot load %s: %v", opts.MCPClientStore, err)
				}
			} else {
				fileSC = c.Server
			}
		}
		layer := newMCPLayer(mgr, mcpOpts, opts.MCP != nil, fileSC)
		srv := layer.server()
		s.mcp = layer
		s.mcpHTTP = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
		s.mcpSSE = mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	}
	if err := s.initObservability(); err != nil {
		return nil, err
	}
	if opts.DevProxy != "" {
		u, err := url.Parse(opts.DevProxy)
		if err != nil {
			return nil, fmt.Errorf("invalid -web-dev url: %w", err)
		}
		s.proxy = httputil.NewSingleHostReverseProxy(u)
	}
	s.panelProxies = map[string]*httputil.ReverseProxy{}
	for _, p := range opts.Panels {
		p.URL = mapLoopbackHost(p.URL, opts.PanelLoopbackMap)
		if err := validatePanel(p); err != nil {
			return nil, err
		}
		if _, dup := s.panelProxies[p.Name]; dup {
			return nil, fmt.Errorf("duplicate panel name %q", p.Name)
		}
		s.panelProxies[p.Name] = newPanelProxy(p)
	}
	s.loadPanelStore()
	s.routes()
	s.registerAgentWorkflowRoutes()
	ready = true
	// Arm scheduled Agent work only after Office and per-account MCP setup.
	mgr.mu.Lock()
	mgr.scheduleOwnerValid = func(owner *identity.Scope) bool {
		if !s.multiuser() {
			return owner == nil
		}
		if owner == nil || !owner.Valid() {
			return false
		}
		auth.mu.RLock()
		defer auth.mu.RUnlock()
		for _, rec := range auth.users {
			if identity.LocalScope(rec.Username) == *owner {
				return true
			}
		}
		return false
	}
	mgr.mu.Unlock()
	s.bindWorkflowExecutionAuthorization()
	s.bindWorkflowAuthoringTools()
	s.startWorkflowCallbacks()
	if s.workflowCallbacks != nil {
		mgr.wfruns.callbackMu.Lock()
		mgr.wfruns.callback = s.workflowCallbacks.record
		mgr.wfruns.callbackMu.Unlock()
		mgr.mu.Lock()
		mgr.workflowCallbackStop = s.stopWorkflowCallbacks
		mgr.mu.Unlock()
	}
	return s, nil
}

// newPanelProxy strips the /panels/<name> prefix and forwards to the backend,
// so the app inside can use ordinary absolute-relative links and fetch calls.
func newPanelProxy(p Panel) *httputil.ReverseProxy {
	if p.ProxyMode == "compat" {
		return newCompatiblePanelProxy(p)
	}
	target, _ := url.Parse(p.URL) // validated by validatePanel
	prefix := "/panels/" + p.Name
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetXForwarded()
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, prefix)
			if pr.Out.URL.Path == "" {
				pr.Out.URL.Path = "/"
			}
			pr.SetURL(target) // joins target's base path with Out's path
			pr.Out.Host = target.Host
		},
	}
}

func (s *Server) routes() {
	s.officeRoutes()
	s.nativeBrowserRoutes()
	s.browserBookmarkRoutes()
	s.shortcutRoutes()
	s.informationRoutes()
	s.uiIconRoutes()
	s.studioRoutes()
	s.workflowRoutes()
	s.registerWorkflowEmbedRoutes()
	s.registerWorkflowCallbackRoutes()
	// Auth surface first: status is what the SPA's gate branch reads before it
	// knows anything else about the deployment.
	s.mux.HandleFunc("GET /api/auth/status", s.handleLoginStatus)
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/logout", s.handleLogout)
	// The settings pane's change-password action: current credential (old
	// password or the web token) re-verified, then a new hash written.
	s.mux.HandleFunc("POST /api/auth/password", s.handleChangePassword)
	s.mux.HandleFunc("GET /api/models", s.handleModels)
	// 账号清单（"账户与安全"面板）：管理员专属，与运维配置同一门槛。
	s.mux.HandleFunc("GET /api/accounts", s.admin(s.handleListAccounts))
	// 删号：同一面板的操作列；同样是管理员专属，且服务端拒绝删管理员行。
	s.mux.HandleFunc("DELETE /api/accounts/{username}", s.admin(s.handleDeleteAccount))
	// Session attachments: uploaded into the session's own workspace under
	// uploads/ (web/attachments.go), served back by the files endpoints like
	// any other workspace file. The GET lists them — the session-files
	// popover's initial state, which attachment_uploaded events keep current.
	s.mux.HandleFunc("POST /api/sessions/{sid}/attachments", s.handleSessionAttach)
	s.mux.HandleFunc("GET /api/sessions/{sid}/attachments", s.handleSessionAttachments)
	s.mux.HandleFunc("GET /api/sessions/{sid}/files/resolve", s.handleResolveSessionFileLink)
	s.mux.HandleFunc("POST /api/sessions/{sid}/html-previews", s.handleCreateHTMLPreview)
	s.mux.HandleFunc("DELETE /api/sessions/{sid}/html-previews/{previewID}", s.handleDeleteHTMLPreview)
	s.mux.HandleFunc("GET /api/html-previews/{capability}/{asset...}", s.handleHTMLPreviewAsset)
	s.mux.HandleFunc("OPTIONS /api/html-previews/{capability}/{asset...}", s.handleHTMLPreviewAsset)
	s.mux.HandleFunc("POST /api/sessions/{sid}/starter-assets", s.admin(s.handleStarterAssetsSave))
	s.mux.HandleFunc("POST /api/sessions/{sid}/starter-assets/resolve", s.handleStarterAssetsResolve)
	// The allowlist the composer's door check, the picker's accept attribute
	// and saveAttachment all read — one list, fetched rather than hardcoded,
	// so client and server cannot drift apart.
	s.mux.HandleFunc("GET /api/attachments/policy", s.handleGetAttachmentPolicy)
	s.mux.HandleFunc("PUT /api/attachments/policy", s.admin(s.handlePutAttachmentPolicy))
	s.mux.HandleFunc("GET /api/skills", s.handleSkills)
	s.authoredSkillRoutes()
	s.mux.HandleFunc("GET /api/tools", s.handleTools)
	s.mux.HandleFunc("GET /api/skills/content", s.handleSkillContent)
	s.mux.HandleFunc("POST /api/skills/upload", s.admin(s.handleSkillUpload))
	s.mux.HandleFunc("POST /api/skills/upload/resolve", s.admin(s.handleSkillUploadResolve))
	s.mux.HandleFunc("GET /api/skills/sample", s.handleSkillSample)
	s.mux.HandleFunc("GET /api/config", s.admin(s.handleGetConfig))
	s.mux.HandleFunc("PUT /api/config", s.admin(s.handleSaveConfig))
	s.mux.HandleFunc("POST /api/config/probe", s.admin(s.handleProbeConfig))
	s.mux.HandleFunc("POST /api/config/probe-metadata", s.admin(s.handleProbeMetadata))
	s.mux.HandleFunc("POST /api/config/vision-probe", s.admin(s.handleVisionProbe))
	s.mux.HandleFunc("GET /api/config/vision-probe/questions", s.admin(s.handleVisionProbeQuestions))
	s.mux.HandleFunc("GET /api/prefs", s.handleGetPrefs)
	s.mux.HandleFunc("GET /api/prefs/spill/entries", s.admin(s.handleSpillEntries))
	s.mux.HandleFunc("GET /api/prefs/spill/file", s.admin(s.handleSpillFile))
	s.mux.HandleFunc("PUT /api/prefs", s.admin(s.handleSavePrefs))
	s.mux.HandleFunc("PUT /api/max-turns", s.admin(s.handlePutMaxTurns))
	s.mux.HandleFunc("PUT /api/memory-auto", s.admin(s.handlePutMemoryAuto))
	// The MCP *client* editor (mcp.json — connecting to other servers). Under
	// /api so the token + Origin middleware covers it like every other
	// operation; the bare /mcp route below is the opposite direction, the
	// machine channel for driving *this* agent. Admin-gated in multi-user
	// mode: the file carries credentials and rewrites everyone's tool surface.
	s.mux.HandleFunc("GET /api/mcp", s.admin(s.handleGetMCPClient))
	s.mux.HandleFunc("PUT /api/mcp", s.admin(s.handleSaveMCPClient))
	s.mux.HandleFunc("GET /api/mcp/status", s.admin(s.handleMCPClientStatus))
	s.mux.HandleFunc("POST /api/mcp/probe", s.admin(s.handleMCPClientProbe))
	s.mux.HandleFunc("GET /api/sandbox", s.handleGetSandbox)
	s.mux.HandleFunc("PUT /api/sandbox", s.admin(s.handlePutSandbox))
	s.mux.HandleFunc("POST /api/prefs/prune-spill", s.admin(s.handlePruneSpill))
	s.mux.HandleFunc("GET /api/starters", s.handleStarters)
	s.mux.HandleFunc("PUT /api/starters", s.admin(s.handleSaveStarters))
	s.registerScheduleRoutes()
	s.registerTaskCenterRoutes()
	s.registerSentinelSessionRoutes()
	s.mux.HandleFunc("GET /api/panels", s.handlePanels)
	s.mux.HandleFunc("POST /api/panels", s.admin(s.handlePanelRegister))
	s.mux.HandleFunc("DELETE /api/panels/{name}", s.admin(s.handlePanelDelete))
	// Panel proxy: content, not operations, so like the page itself it is not
	// token-gated (see the Panel doc). Bare /panels/<name> redirects to the
	// trailing slash so relative links inside resolve under the prefix.
	s.mux.Handle("/panels/{name}/", http.HandlerFunc(s.handlePanel))
	s.mux.Handle("/panels/{name}", http.HandlerFunc(s.handlePanelRedirect))
	// "GET /api/sessions/sample" is a literal and so wins over the {sid}
	// wildcard below — Go 1.22 ServeMux rule — keeping the sample download
	// from ever being read as a session id.
	s.mux.HandleFunc("GET /api/sessions/sample", s.handleSessionSample)
	// Like sample above, a literal that wins over the {sid} patterns below.
	s.mux.HandleFunc("GET /api/sessions/activity", s.handleSessionsActivity)
	s.mux.HandleFunc("GET /api/sessions/search", s.handleSearchSessions)
	s.mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	s.mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	s.mux.HandleFunc("POST /api/sessions/import", s.handleSessionImport)
	s.mux.HandleFunc("GET /api/sessions/{sid}", s.handleGetSession)
	s.mux.HandleFunc("GET /api/sessions/{sid}/transcript", s.handleTranscript)
	s.mux.HandleFunc("GET /api/sessions/{sid}/subagents/{agentID}/transcript", s.handleSubagentTranscript)
	s.mux.HandleFunc("GET /api/sessions/{sid}/shell-tasks", s.handleShellTasks)
	s.mux.HandleFunc("GET /api/sessions/{sid}/shell-tasks/{taskID}/output", s.handleShellTaskOutput)
	s.mux.HandleFunc("POST /api/sessions/{sid}/shell-tasks/{taskID}/stop", s.handleShellTaskStop)
	s.mux.HandleFunc("GET /api/sessions/{sid}/export", s.handleSessionExport)
	// The Markdown reading copy of the same session the JSON export backs up.
	// Its own literal path, so the dot never lands inside a {sid} match.
	s.mux.HandleFunc("GET /api/sessions/{sid}/export.md", s.handleSessionExportMarkdown)
	// Shared answers (web/share.go): create is owner-only, view is the one
	// public API path (see withToken). Links live until the shares file says
	// otherwise — there is no revoke endpoint.
	s.mux.HandleFunc("POST /api/sessions/{sid}/share", s.handleShareCreate)
	s.mux.HandleFunc("GET /api/share/{id}", s.handleShareView)
	s.mux.HandleFunc("PATCH /api/sessions/{sid}", s.handleUpdateSession)
	s.mux.HandleFunc("DELETE /api/sessions/{sid}", s.handleDeleteSession)
	s.mux.HandleFunc("POST /api/sessions/{sid}/messages", s.handleMessages)
	s.mux.HandleFunc("GET /api/sessions/{sid}/stream", s.handleStream)
	s.mux.HandleFunc("GET /api/sessions/{sid}/terminal", s.handleTerminal)
	s.mux.HandleFunc("POST /api/sessions/{sid}/control", s.handleControl)
	s.mux.HandleFunc("GET /api/files", s.handleFiles)
	s.mux.HandleFunc("GET /api/files/content", s.handleFileContent)
	s.mux.HandleFunc("PUT /api/files/content", s.handleFileSave)
	s.mux.HandleFunc("POST /api/files/mkdir", s.handleFileMkdir)
	s.mux.HandleFunc("POST /api/files/rename", s.handleFileRename)
	s.mux.HandleFunc("DELETE /api/files", s.handleFileDelete)
	s.mux.HandleFunc("GET /api/files/index", s.handleFileIndex)
	s.mux.HandleFunc("GET /api/files/render", s.handleFileRender)
	s.mux.HandleFunc("GET /api/workspace/changes", s.handleWorkspaceChanges)
	s.mux.HandleFunc("GET /api/workspace/diff", s.handleWorkspaceDiff)
	s.mux.HandleFunc("POST /api/workspace/journal/clear", s.handleWorkspaceJournalClear)
	// The MCP endpoints. Both handlers police their own methods; everything
	// else about access (token, Origin) is the middleware stack below, exactly
	// as for /api — an ungated MCP endpoint would be the same remote shell an
	// ungated /api is, not a softer version of it. The routes exist whenever
	// the layer does; whether they *answer* is the enable gate in withToken
	// (mcpserve.go), so a disabled deployment reads as an ordinary 404.
	if s.mcp != nil {
		// The log middleware sits between the mux and the SDK handlers: every
		// request that reaches the transports gets one record, tagged with its
		// transport. Gate-layer rejections (401/404/503) never make it here —
		// withToken records those itself. The 405 shim sits inside the log
		// wrapper so its answers are visible in the panel too.
		s.mux.Handle("/mcp", s.mcplog.wrapTransport("streamable",
			sessionlessGETCompatibility(s.mcpHTTP)))
		s.mux.Handle("/sse", s.mcplog.wrapTransport("sse", s.mcpSSE))
		s.mux.Handle("/mcp/agents/{aid}", s.mcplog.wrapTransport("streamable", sessionlessGETCompatibility(http.HandlerFunc(s.handleAgentMCP))))
	}
	// The log viewer (mcplog.go). Under /api so the token gate and the
	// Fetch-Metadata write guard apply as to every other operation; admin-gated
	// in multi-user mode because the log is a machine-channel record, not a
	// per-user surface.
	s.mux.HandleFunc("GET /api/mcplog", s.admin(s.handleGetMCPLogs))
	s.mux.HandleFunc("DELETE /api/mcplog", s.admin(s.handleClearMCPLogs))
	s.mux.HandleFunc("GET /api/mcplog/stats", s.admin(s.handleMCPLogStats))
	s.mux.HandleFunc("GET /api/mcp/calls", s.admin(s.handleMCPCalls))
	s.mux.HandleFunc("GET /api/mcp/calls/stats", s.admin(s.handleMCPCallStats))
	s.mux.HandleFunc("GET /api/mcp/calls/{id}", s.admin(s.handleMCPCallDetail))
	s.mux.HandleFunc("GET /api/apps/{aid}/mcp/calls", s.handleAgentMCPCalls)
	s.mux.HandleFunc("GET /api/apps/{aid}/mcp/calls/stats", s.handleAgentMCPStats)
	s.mux.HandleFunc("GET /api/apps/{aid}/mcp/calls/{id}", s.handleAgentMCPDetail)
	s.mux.HandleFunc("GET /api/skills/events", s.admin(s.handleSkillEvents))
	s.mux.HandleFunc("GET /api/skills/events/stats", s.admin(s.handleSkillEventStats))
	s.mux.HandleFunc("GET /api/skills/events/{id}", s.admin(s.handleSkillEventDetail))
	s.mux.HandleFunc("GET /api/observability/status", s.admin(s.handleObservabilityStatus))
	// Unknown API paths, including retired routes, must not serve the SPA shell.
	s.mux.HandleFunc("/api/", http.NotFound)
	s.mux.HandleFunc("/", s.handleUI)
}

// sessionlessGETCompatibility answers a session-less GET on /mcp with 405
// instead of the SDK's 400.
//
// Why: real-world MCP clients exist that open the server→client push stream
// (a GET with Accept: text/event-stream) as their FIRST move, before
// initialize — a remote platform did exactly that and read the 400 as a
// broken endpoint. The spec's own menu for a GET the server will not serve
// is a 405 ("The server MUST either return Content-Type: text/event-stream
// … or else it MUST return HTTP 405 Method Not Allowed"), and clients are
// expected to degrade to POST-only operation, which loses nothing here:
// initialize, tools/list, tools/call and wait's streaming progress all ride
// POST responses. A GET that DOES carry an initialized session passes
// through untouched, so conforming clients see no behaviour change.
func sessionlessGETCompatibility(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.Header.Get("Mcp-Session-Id") == "" {
			w.Header().Set("Allow", "POST, DELETE")
			http.Error(w, "Method Not Allowed: the GET server-push stream requires an initialized session; proceed POST-only (405 is the spec-permitted answer)", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Handler wraps the routes in the two checks that stand between this server and
// a remote shell: a shared token and an Origin check.
//
// Both matter more here than for a typical API. pogo's bash tool has no path
// restriction — unlike read, write and edit, which the path guard confines to the
// working directory — so an unauthenticated instance is remote code execution as
// a service.
func (s *Server) Handler() http.Handler {
	return s.withReleaseMaintenance(s.withOrigin(s.withToken(s.guardStudioSessions(s.mux))))
}

func (s *Server) withToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Embedded credentials authorize only the dedicated publication API.
		if strings.HasPrefix(r.URL.Path, workflowEmbedPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		if token := bearerOrQueryToken(r); strings.HasPrefix(token, "wes_") || strings.HasPrefix(token, "wic_") || strings.HasPrefix(token, "wlc_") {
			// Preserve operator-configured legacy tokens with coincidental
			// prefixes, but never let an unrecognized embed bearer fall back
			// to a login cookie or the unauthenticated loopback compatibility.
			_, fixedAccount := s.auth.userByToken(token)
			fixedOperator := s.opts.Token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.opts.Token)) == 1
			if !fixedAccount && !fixedOperator {
				http.Error(w, "embedded credentials cannot access this endpoint", http.StatusUnauthorized)
				return
			}
		}
		if isHTMLPreviewAssetRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		// The three auth endpoints speak for themselves: status must be readable
		// to decide which gate to show, and login/logout are the way in and out.
		if r.URL.Path == "/api/auth/status" || r.URL.Path == "/api/login" || r.URL.Path == "/api/logout" {
			next.ServeHTTP(w, r)
			return
		}
		// The public half of shared answers: a GET by unguessable id serves the
		// snapshot and nothing else. Reads only — creating and revoking stay
		// gated, and a non-GET on the same path never matches the routes above.
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/share/") {
			next.ServeHTTP(w, r)
			return
		}
		// The MCP endpoints gate on their own credential when one is configured,
		// the web token otherwise in the legacy no-store arrangement — never on
		// both, or a separate MCP token would be revocable only by also
		// rotating the web one, which is the opposite of what a second token is
		// for. No cookie channel here: a machine peer is a Bearer header (or
		// ?token= on /mcp, which unlike the legacy /sse POST-back keeps its
		// query intact).
		if isMCPPath(r.URL.Path) {
			// The enable gate comes first: a disabled surface answers an
			// ordinary 404, indistinguishable from a path that was never
			// mounted — it leaks neither that the endpoints could exist, nor
			// whether a token would have been accepted.
			if s.mcp != nil && !s.mcp.effectiveEnabled() {
				// The 404 reads as "not mounted" to the caller; the log still
				// records what actually happened, or the operator debugging a
				// silent platform would see nothing at all.
				s.mcplog.recordGate(r, http.StatusNotFound, mcpDiagDisabled)
				http.NotFound(w, r)
				return
			}
			tok := s.opts.Token
			if s.mcp != nil {
				t, terr := s.mcp.effectiveToken()
				if terr != nil {
					// A configured credential that cannot resolve (token_env
					// names a missing variable) fails closed: the operator
					// believes authentication is on. Refusing the service
					// loudly is the safe reading — silently treating it as
					// "no token configured" would open the endpoints while
					// the config says they are gated.
					if s.opts.Logger != nil {
						s.opts.Logger.Printf("mcp: refusing request from %s: %v", r.RemoteAddr, terr)
					}
					http.Error(w, "mcp server authentication is misconfigured", http.StatusServiceUnavailable)
					s.mcplog.recordGate(r, http.StatusServiceUnavailable, mcpDiagAuthMisconfigured)
					return
				}
				tok = t
				// The legacy fallback lives here, in the code that knows
				// whether a store exists: without one (the static-options
				// path), an empty MCP credential keeps its historic meaning
				// of "the web token gates". With a store, empty is the file
				// channel's deliberate "no authentication".
				if tok == "" && s.opts.MCPClientStore == "" {
					tok = s.opts.Token
				}
			}
			if tok != "" && !tokenOK(r, tok) {
				// Failed machine-channel auth is worth a log line with the
				// source address — the web side has a human watching, this one
				// does not.
				if s.opts.Logger != nil {
					s.opts.Logger.Printf("mcp: 401 unauthorized from %s", r.RemoteAddr)
				}
				// WWW-Authenticate names the scheme a client should retry with —
				// the hook an RFC 9728 protected-resource document would hang off
				// if this deployment ever grows OAuth, at the cost of one header.
				w.Header().Set("WWW-Authenticate", `Bearer realm="pogo-mcp"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				s.mcplog.recordGate(r, http.StatusUnauthorized, mcpDiagAuthFailed)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		// Only the API is gated. The page and its bundle are not: a browser
		// cannot attach a header to the script tags it discovers in the HTML it
		// just loaded, so gating them would 401 the app against its own assets.
		// Serving them openly gives nothing away — it is the same code that ships
		// inside the binary — while every operation still needs the token.
		if (s.opts.Token == "" && !isNativeBrowserLivePath(r)) || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		id, ok := s.authenticate(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIdentityKey, &id)))
	})
}

// requestIdentity is the authenticated caller of one API request: the locally
// managed account behind a password-login cookie or a fixed token. The zero
// value (single-user mode) is "the operator, on the token".
type requestIdentity struct {
	identity.Scope
	// Username of the users.json account behind the request; empty in
	// single-user mode, where the shared token is the operator and nobody
	// else. Admin says whether it carries the operator grant: the default
	// admin account or an "admin": true row.
	Username string
	Local    bool
	Admin    bool
}

var requestIdentityKey struct{}

// identityFrom unwinds the middleware's work for the handlers.
func identityFrom(r *http.Request) (*requestIdentity, bool) {
	id, ok := r.Context().Value(requestIdentityKey).(*requestIdentity)
	return id, ok && id != nil
}

// localIdentity is the identity of a users.json account (or the admin the
// shared token binds to). Admin comes from the account row (isAdmin);
// ordinary accounts are exactly that.
func (s *Server) localIdentity(username string) requestIdentity {
	return requestIdentity{
		Scope:    identity.LocalScope(username),
		Username: username,
		Local:    true,
		Admin:    s.auth.isAdmin(username),
	}
}

// multiuser reports whether any identity source is armed. In multi-user mode
// every request resolves to a named user with its own session space, and the
// shared token channel is closed — a credential that admits everyone is a
// credential that defeats the isolation.
func (s *Server) multiuser() bool {
	return s.auth.Enabled()
}

// authenticate resolves the caller. Channels, in order:
//
//  1. Fixed token, from the Bearer header or ?token= (curl, EventSource and
//     plain links all fit). A user's own token (users.json's "token" field)
//     enters that user's space; the deployment's shared POGO_WEB_TOKEN maps
//     to the default admin account in multi-user mode and keeps the classic
//     "the operator themself" meaning in single-user mode. Tokens carry no
//     expiry — rotating one is editing users.json (or the env) and restarting.
//  2. The password-login cookie — same accounts, browser shape.
func (s *Server) authenticate(r *http.Request) (requestIdentity, bool) {
	// A browser WebSocket carries a short-lived, single-use ticket, never the
	// durable account credential in its URL. Failure must not fall through to
	// the token/cookie channels, including when both were supplied.
	if isNativeBrowserLivePath(r) {
		return s.authenticateNativeBrowserLive(r)
	}
	if given := bearerOrQueryToken(r); given != "" {
		if user, ok := s.auth.userByToken(given); ok {
			return s.localIdentity(user), true
		}
		if subtle.ConstantTimeCompare([]byte(given), []byte(s.opts.Token)) == 1 {
			if s.multiuser() {
				// 默认 token = 默认 admin：环境令牌是运维手里的钥匙，绑定到
				// admin 账号的空间与权限，而不是"看得见所有人"。
				return s.localIdentity(bootstrapUser), true
			}
			return requestIdentity{}, true // single-user mode: the classic meaning
		}
	}
	if s.auth.Enabled() {
		if user, ok := s.auth.sessionUser(r, time.Now()); ok {
			return s.localIdentity(user), true
		}
	}
	return requestIdentity{}, false
}

// bearerOrQueryToken is the fixed-token channel's two spellings: a Bearer
// header, or ?token= for the clients that cannot set headers.
func bearerOrQueryToken(r *http.Request) string {
	given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if given == "" {
		given = r.URL.Query().Get("token")
	}
	return given
}

// ownerScope is the ownership scope of the request's user, present only in
// multi-user mode.
func (s *Server) ownerScope(r *http.Request) (identity.Scope, bool) {
	if !s.multiuser() {
		return identity.Scope{}, false
	}
	if id, ok := identityFrom(r); ok {
		return id.Scope, true
	}
	return identity.Scope{}, false
}

// adminRequest reports whether the caller may reach operator surfaces — the
// config, MCP and skills editors, the scheduler. Local accounts are
// operator-managed and hold the grant through their account row.
func (s *Server) adminRequest(r *http.Request) bool {
	if principal, ok := r.Context().Value(workflowPrincipalContextKey{}).(identity.Principal); ok {
		return principal.IsOperator()
	}
	if caller, ok := identityFrom(r); ok && !caller.Local && (caller.Username != "" || caller.Scope != (identity.Scope{}) || caller.Admin) {
		return false
	}
	if !s.multiuser() {
		return true
	}
	id, ok := identityFrom(r)
	if !ok {
		return false
	}
	return id.Admin
}

// admin gates an operator endpoint: single-user mode passes everything (the
// one token holder is the operator), multi-user mode demands the admin grant.
func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.adminRequest(r) {
			http.Error(w, "operator endpoints require the admin grant", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// isMCPPath reports whether a request targets the MCP endpoints. Note the
// legacy /sse transport POSTs back to the same path (the SDK keeps it, swapping
// only the query), so one path covers both directions.
func isMCPPath(p string) bool {
	return p == "/mcp" || p == "/sse" || strings.HasPrefix(p, "/mcp/agents/")
}

// tokenOK is the constant-time credential check shared by the MCP gate. The
// web API's authorized() layers the password-cookie channel on the same
// comparison; this copy exists because that channel must not admit a
// machine peer to an MCP-only token deployment.
func tokenOK(r *http.Request, token string) bool {
	given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if given == "" {
		given = r.URL.Query().Get("token")
	}
	return subtle.ConstantTimeCompare([]byte(given), []byte(token)) == 1
}

// withOrigin rejects cross-site requests instead of opening CORS. A page on
// another origin must not be able to drive this server through a browser that
// already holds the token.
func (s *Server) withOrigin(next http.Handler) http.Handler {
	allowed := make(map[string]bool, len(s.opts.AllowedOrigins))
	for _, o := range s.opts.AllowedOrigins {
		allowed[strings.TrimSuffix(o, "/")] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isHTMLPreviewAssetRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		// Fetch Metadata first, on writes: Sec-Fetch-Site is set by the
		// browser itself and covers the request shapes that carry no Origin
		// header at all. same-origin is the UI; none is a typed navigation or
		// a non-browser client; anything else is another site driving us.
		// Reads are not gated on it — they are already behind the token, and
		// the SSE/panel traffic has legitimate cross-site-adjacent shapes.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			switch site := r.Header.Get("Sec-Fetch-Site"); site {
			case "", "same-origin", "none":
			default:
				http.Error(w, "cross-site write rejected", http.StatusForbidden)
				return
			}
		}
		origin := r.Header.Get("Origin")
		// No Origin means a non-browser client (curl, a test), which the token
		// already covers.
		if origin == "" || allowed[strings.TrimSuffix(origin, "/")] {
			next.ServeHTTP(w, r)
			return
		}
		if u, err := url.Parse(origin); err == nil && u.Host == r.Host {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
	})
}

// Serve runs until ctx is cancelled, then drains: the HTTP server stops taking
// connections and the Manager cancels every run, which reaches each bash child
// process through its context.
func (s *Server) Serve(ctx context.Context, addr string) error {
	defer func() { s.mgr.Close(); s.Close() }()
	srv := &http.Server{
		Addr:    addr,
		Handler: s.Handler(),
		// No write timeout: an SSE stream is a long-lived response by design, and
		// a deadline here would cut a thinking agent off mid-turn.
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	if s.opts.TLSConfig != nil {
		config := s.opts.TLSConfig.Clone()
		config.NextProtos = []string{"http/1.1", "stun.turn"}
		listener = tls.NewListener(listener, config)
	}
	listener, err = s.startBrowserMediaRelay(listener)
	if err != nil {
		return err
	}
	defer s.closeBrowserMediaRelay()
	s.browserLiveMu.Lock()
	relay := s.browserMediaRelay
	s.browserLiveMu.Unlock()
	if relay != nil {
		srv.ConnContext = relay.ConnContext
		srv.Handler = relay.HTTPHandler(srv.Handler)
	}
	go func() { errc <- srv.Serve(listener) }()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	if s.opts.Logger != nil {
		s.opts.Logger.Print("shutting down, cancelling active runs")
	}
	s.closeBrowserLive()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	s.mgr.Close()
	if errors.Is(err, context.DeadlineExceeded) {
		return srv.Close()
	}
	return err
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	type model struct {
		ID            string   `json:"id"`
		Provider      string   `json:"provider"`
		Aliases       []string `json:"aliases,omitempty"`
		ContextWindow int      `json:"context_window"`
		Configured    bool     `json:"configured"`
		// SubagentModel is what a read-only subagent of this model runs. Reported
		// because it is the one catalogue entry that changes what happens without
		// anyone naming it, and because the terminal's -models already shows it: a
		// field visible in one front end and not the other is how the two drift.
		SubagentModel string `json:"subagent_model,omitempty"`
		// KeyEnv names the variable to set. Without it "configured: false" tells the
		// browser something is wrong but not what to do, and unlike the terminal it
		// cannot print a hint of its own.
		KeyEnv string `json:"key_env,omitempty"`
	}
	out := make([]model, 0, len(config.Catalog()))
	for _, m := range config.Catalog() {
		e := model{
			ID: m.ID, Provider: m.Provider, Aliases: m.Aliases,
			ContextWindow: m.ContextWindow, Configured: config.Configured(m.Provider),
			SubagentModel: m.SubagentModel,
		}
		if !e.Configured {
			e.KeyEnv = config.KeyEnv(m.Provider)
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": out, "default": config.DefaultModel()})
}

// handlePanels lists the external panels registered with -web-panel. The UI
// needs it once at boot to know which sheet buttons to show; the backend URL
// is deliberately withheld — the iframe only ever talks to /panels/<name>/,
// so the internal topology stays the server's business.
// handlePanels lists the registered panels: the -web-panel flags plus the
// runtime-registered ones (POST /api/panels). The UI fetches once at boot, so
// a panel added later surfaces on the next reload. The backend URL is still
// deliberately withheld — the iframe only ever talks to /panels/<name>/,
// so the internal topology stays the server's business.
func (s *Server) handlePanels(w http.ResponseWriter, r *http.Request) {
	type panel struct {
		Name      string `json:"name"`
		Path      string `json:"path"`
		Entry     string `json:"entry"`
		ProxyMode string `json:"proxy_mode"`
	}
	s.panelMu.RLock()
	out := make([]panel, 0, len(s.opts.Panels)+len(s.userPanels))
	for _, p := range s.opts.Panels {
		out = append(out, panel{Name: p.Name, Path: "/panels/" + p.Name + "/", Entry: panelEntryPath(p), ProxyMode: panelMode(p)})
	}
	for _, p := range s.userPanels {
		out = append(out, panel{Name: p.Name, Path: "/panels/" + p.Name + "/", Entry: panelEntryPath(p), ProxyMode: panelMode(p)})
	}
	s.panelMu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"panels": out,
		// The pane reads this before deciding a cross-origin navigate may
		// register its panel silently. Off means the agent is told to ask the
		// human to add the shortcut instead.
		"auto_register": s.opts.PanelAutoRegister,
	})
}

func (s *Server) handlePanel(w http.ResponseWriter, r *http.Request) {
	s.panelMu.RLock()
	proxy, ok := s.panelProxies[r.PathValue("name")]
	s.panelMu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	proxy.ServeHTTP(w, r)
}

func (s *Server) handlePanelRedirect(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.panelMu.RLock()
	_, ok := s.panelProxies[name]
	s.panelMu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	target := "/panels/" + name + "/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

// handleSkills lists the skills in effect. The set is server-wide and fixed at
// startup, so this needs no session and never changes for the life of the process.
//
// The location is included because it is already in the model's system prompt:
// withholding it from the browser would hide from the user something the model can
// see, which is the wrong direction for a tool whose whole job is being watchable.
func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	type skill struct {
		Name        string            `json:"name"`
		Aliases     []string          `json:"aliases,omitempty"`
		Description string            `json:"description"`
		Path        string            `json:"path"`
		Dir         string            `json:"dir"`
		Source      string            `json:"source"`
		ManualOnly  bool              `json:"manual_only,omitempty"`
		Tools       []tools.SynthInfo `json:"tools,omitempty"`
	}
	list := s.mgr.Skills()
	catalog := s.mgr.SynthCatalog()
	// ?session_id= narrows the answer to one conversation's bound selection
	// (the dock panel's view): a Studio conversation sees exactly the skills
	// its definition pinned, legacy and ordinary conversations the global
	// set. The owner check is the same one every session endpoint applies.
	if sid := r.URL.Query().Get("session_id"); sid != "" {
		bound, err := s.sessionSkillSet(r, sid)
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		list = bound
		catalog = s.sessionSynthCatalog(bound)
	}
	// The manifest tools a skill declares, grouped for the skills panel: the
	// by-skill view is "what does this bundle bring", the by-tool view is the
	// same catalog ungrouped (/api/tools). One source of truth, two orders.
	bySkill := map[string][]tools.SynthInfo{}
	for _, ti := range catalog {
		bySkill[ti.Skill] = append(bySkill[ti.Skill], ti)
	}
	out := make([]skill, 0, len(list))
	for _, sk := range list {
		if sk.BrowserTask && !s.browserAllowedRequest(r) {
			continue
		}
		out = append(out, skill{
			Name: sk.Name, Aliases: sk.Aliases, Description: sk.Description, Path: sk.Path,
			Dir: sk.Dir, Source: sk.Source, ManualOnly: sk.DisableModelInvocation,
			Tools: bySkill[sk.Name],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": out})
}

// handleTools serves the tool catalog: the manifest-declared half (skills'
// tools.json) beside the live MCP half (the client pool's per-server
// catalogs). Every declared tool is here whether or not it is registered for
// the model, which is the split the endpoint exists to expose: the catalog
// answers "what tools does this deployment have and what do they take" for
// the UI's forms and a future MCP server, while the registry answers "what
// can the model call" and holds only the registered half.
func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	catalog := s.mgr.SynthCatalog()
	builtin := tools.BuiltinCatalog()
	var mcpOnly map[string]bool // nil: the whole pool — the default for every caller
	if sid := r.URL.Query().Get("session_id"); sid != "" {
		bound, err := s.sessionSkillSet(r, sid)
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		catalog = s.sessionSynthCatalog(bound)
		if binding, ok := s.mgr.studio.binding(sid); ok {
			builtin = tools.BuiltinRuntimeCatalog()
			state, _ := s.mgr.effectiveStudioTools(sid, binding)
			builtin = slices.DeleteFunc(builtin, func(info tools.BuiltinInfo) bool {
				return !slices.Contains(state.Tools, info.Name) || (info.Name == "browser" && !s.browserAllowedRequest(r))
			})
		}
		mcpOnly, err = s.sessionMCPOnly(r, sid)
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tools": catalog,
		"mcp":   s.mcpServerEntries(r, mcpOnly),
		// Unscoped requests serve the editor's full catalog. A session-scoped
		// request exposes only its effective registered native capabilities.
		"builtin": builtin,
	})
}

// mcpToolEntry is one MCP tool's display projection: the same field names the
// manifest catalog serves (the UI's ToolCatalogEntry), plus the owning
// server. Title and description are untrusted server prose — display-only,
// same as on the wire declaration.
type mcpToolEntry struct {
	Name        string         `json:"name"`
	WireName    string         `json:"wire_name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
	ReadOnly    bool           `json:"read_only,omitempty"`
	Destructive bool           `json:"destructive,omitempty"`
	Idempotent  bool           `json:"idempotent,omitempty"`
	OpenWorld   bool           `json:"open_world,omitempty"`
	Server      string         `json:"server"`
	Group       string         `json:"group,omitempty"`
}

// mcpServerEntry is one MCP server's panel card: the pool's live status
// beside its tool list (the cached catalog when the server is down, so an
// outage greys the group rather than vanishing it).
type mcpServerEntry struct {
	Name          string         `json:"name"`
	State         string         `json:"state"`
	ServerName    string         `json:"server_name,omitempty"`
	ServerVersion string         `json:"server_version,omitempty"`
	LastError     string         `json:"last_error,omitempty"`
	CatalogStale  bool           `json:"catalog_stale,omitempty"`
	Tools         []mcpToolEntry `json:"tools"`
}

func mcpEntryOf(t *tools.MCPTool) mcpToolEntry {
	desc := t.FullDescription()
	if desc == "" {
		desc = t.Description()
	}
	entry := mcpToolEntry{
		Name:        t.ServerToolName(),
		WireName:    t.Name(),
		Title:       t.DisplayTitle(),
		Description: desc,
		InputSchema: t.InputSchema(),
		ReadOnly:    t.IsReadOnly(),
		Server:      t.ServerName(),
		Group:       t.CatalogGroup(),
	}
	if a := t.Annotations(); a != nil {
		entry.Idempotent = a.IdempotentHint
		if a.DestructiveHint != nil {
			entry.Destructive = *a.DestructiveHint
		}
		if a.OpenWorldHint != nil {
			entry.OpenWorld = *a.OpenWorldHint
		}
	}
	return entry
}

// mcpServerEntries renders the pool projection for this request: the owner's
// pool (shared when the account carries no overrides), narrowed to the
// conversation's server selection. An empty set means an empty array — the
// panel keys its MCP section on length, never on null.
func (s *Server) mcpServerEntries(r *http.Request, only map[string]bool) []mcpServerEntry {
	var owner *identity.Scope
	if scope, ok := s.ownerScope(r); ok {
		owner = &scope
	}
	catalogs := s.mgr.MCPCatalogs(owner, only)
	out := make([]mcpServerEntry, 0, len(catalogs))
	for _, c := range catalogs {
		entry := mcpServerEntry{
			Name:          c.Status.Name,
			State:         c.Status.State,
			ServerName:    c.Status.ServerName,
			ServerVersion: c.Status.ServerVersion,
			LastError:     c.Status.LastError,
			CatalogStale:  c.Status.CatalogStale,
			Tools:         []mcpToolEntry{},
		}
		for _, t := range c.Tools {
			entry.Tools = append(entry.Tools, mcpEntryOf(t))
		}
		out = append(out, entry)
	}
	return out
}

// handleSkillContent previews documents within one loaded Skill. The optional
// path is confined to that Skill's root, after the existing session-selection
// check; it cannot grant access to another Skill or an ordinary host path.
func (s *Server) handleSkillContent(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	list := s.mgr.Skills()
	// ?session_id= confines the viewer to the conversation's selection: a
	// pinned Studio conversation cannot read a SKILL.md it was not granted,
	// and an outside-selection name answers exactly like an unknown one.
	if sid := r.URL.Query().Get("session_id"); sid != "" {
		bound, err := s.sessionSkillSet(r, sid)
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		list = bound
	}
	sk, ok := skills.Find(list, name)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("unknown skill: %s", name))
		return
	}
	writeSkillDocument(w, sk, r.URL.Query().Get("path"))
}

// handleStarters serves the cards an empty conversation offers. The content is
// the skill's, not pogo's: a deployment describes its own openings, and this
// server only checks that each one can actually do something.
//
// Read per request so an edited starters.json shows up on reload — the same
// reason /skill:name re-reads the instructions. The files are small and this is
// fetched once per page.
func (s *Server) effectiveStarters() effectiveStarterSet {
	warn := func(format string, args ...any) {
		if s.opts.Logger != nil {
			s.opts.Logger.Printf(format, args...)
		}
	}
	all, diags := skills.LoadStarters(s.mgr.Skills())
	for _, d := range diags {
		warn("starters: %s: %s", d.Path, d.Message)
	}

	registered := map[string]bool{}
	for _, p := range s.opts.Panels {
		registered[p.Name] = true
	}
	// Runtime-registered panels (POST /api/panels) are just as registered as
	// the flag ones: handlePanels offers both to the card editor's picker, so
	// a card naming one must not be filtered out here.
	s.panelMu.RLock()
	for _, p := range s.userPanels {
		registered[p.Name] = true
	}
	s.panelMu.RUnlock()

	// The user-edited override, once saved, is the whole set: the editor
	// seeds itself with the effective cards, so nothing is lost by switching,
	// and a deployment's live tweaks stop competing with its skill files.
	custom := false
	if user, msgs := s.userStarters(); user != nil {
		for _, m := range msgs {
			warn("starters (user): %s", m)
		}
		all = []skills.Starters{{
			Heading: user.Heading, Send: user.Send,
			Cards: user.Cards, Followups: user.Followups,
		}}
		custom = true
	}

	// One empty state, so the first heading and the first send flag win; cards
	// concatenate in skill order and stop at the layout's limit.
	// A card pointing at a panel this process never registered would be a button
	// that does nothing, which is worse than one card fewer.
	keepable := func(skill string, cards []skills.StarterCard) []skills.StarterCard {
		out := make([]skills.StarterCard, 0, len(cards))
		for _, c := range cards {
			if c.Panel != "" && !registered[c.Panel] {
				warn("starters: %s: card %q names unregistered panel %q; skipped", skill, c.Title, c.Panel)
				continue
			}
			out = append(out, c)
		}
		return out
	}

	out := effectiveStarterSet{Cards: []skills.StarterCard{}, Followups: []skills.FollowupGroup{}, Custom: custom}

	for _, st := range all {
		if out.Heading == "" {
			out.Heading = st.Heading
			out.Send = st.Send
		}
		for _, c := range keepable(st.Skill, st.Cards) {
			if skills.MaxStarterCards > 0 && len(out.Cards) == skills.MaxStarterCards {
				break
			}
			out.Cards = append(out.Cards, c)
		}
		for _, g := range st.Followups {
			// A group whose chips all pointed at missing panels would render an
			// empty row, so it drops out with them.
			if chips := keepable(st.Skill, g.Chips); len(chips) > 0 {
				out.Followups = append(out.Followups, skills.FollowupGroup{When: g.When, Chips: chips})
			}
		}
	}
	out.Revision = starterSetRevision(out)
	return out
}

func (s *Server) handleStarters(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"starters": s.effectiveStarters()})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	list, err := s.mgr.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if list == nil {
		list = []session.Info{}
	}
	// Multi-user mode: the directory holds everyone's transcripts, so the
	// sidebar is the catalog's answer filtered onto the directory's facts.
	owned := s.ownedSet(r)
	if owned != nil {
		kept := list[:0]
		for _, info := range list {
			if owned[info.ID] {
				kept = append(kept, info)
			}
		}
		list = kept
	}
	busy := s.mgr.BusyIDs()
	liveTitles := s.mgr.LiveTitles()
	out := make([]sessionSummary, 0, len(list))
	for _, info := range list {
		// The derived title comes off the transcript's first user message, but
		// that append trails the run — with an image attachment, by the whole
		// describe step. The live fold already has the prompt, so the sidebar
		// need not show 空会话 until the disk catches up. Only fills a gap:
		// anything on disk (derived or renamed) already settled.
		if info.Title == "" {
			info.Title = liveTitles[info.ID]
		}
		out = append(out, sessionSummary{Info: info, Busy: busy[info.ID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions": out,
		"cwd":      s.mgr.Cwd(),
	})
}

// ownedSet is the caller's catalog answer for list-shaped endpoints; nil in
// single-user mode, where nothing is filtered.
func (s *Server) ownedSet(r *http.Request) map[string]bool {
	if scope, ok := s.ownerScope(r); ok {
		return s.mgr.OwnedIDs(scope)
	}
	return nil
}

// sessionSummary is the sidebar's view of a session: the on-disk Info plus the
// one fact only this process knows — whether a run is in flight right now.
type sessionSummary struct {
	session.Info
	Busy bool `json:"busy,omitempty"`
}

// handleSessionsActivity serves the cheap poll behind the sidebar's working
// indicators: just the ids with a run in flight, straight from memory. The
// full list re-reads every transcript off disk, which a poll firing every few
// seconds from every open tab has no business doing.
func (s *Server) handleSessionsActivity(w http.ResponseWriter, r *http.Request) {
	busy := s.mgr.BusyIDs()
	infos := make([]session.Info, 0, len(busy))
	for id := range busy {
		infos = append(infos, session.Info{ID: id})
	}
	workers, err := s.mgr.backgroundWorkerSet(infos)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	owned := s.ownedSet(r)
	ids := make([]string, 0, len(busy))
	for id := range busy {
		if workers[id] != "" {
			continue
		}
		if owned != nil && !owned[id] {
			continue // somebody else's run is not this sidebar's indicator
		}
		ids = append(ids, id)
	}
	// Sorted, so a poll that changes nothing produces the same bytes — diffs
	// and tests stay quiet.
	sort.Strings(ids)
	writeJSON(w, http.StatusOK, map[string]any{"busy": ids})
}

// handleSearchSessions answers "which session was that" across every saved
// transcript — served by the session index (FTS), so it costs a query, not a
// grep of the whole sessions directory.
func (s *Server) handleSearchSessions(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeErr(w, http.StatusBadRequest, errors.New("missing q"))
		return
	}
	limit := 20
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 100 {
		limit = v
	}
	hits, err := s.mgr.Search(q, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if hits == nil {
		hits = []session.Hit{}
	}
	// The index searches the whole directory; the catalog decides whose hits
	// these are allowed to be.
	if owned := s.ownedSet(r); owned != nil {
		kept := hits[:0]
		for _, h := range hits {
			if owned[h.ID] {
				kept = append(kept, h)
			}
		}
		hits = kept
	}
	type result struct {
		session.Hit
		Busy bool `json:"busy,omitempty"`
	}
	busy := s.mgr.BusyIDs()
	out := make([]result, 0, len(hits))
	for _, h := range hits {
		out = append(out, result{Hit: h, Busy: busy[h.ID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": out, "q": q})
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string `json:"model"`
		// Workspace is a directory relative to the session's base root ("" =
		// the root); it becomes the session's working directory. In multi-user
		// mode the base is the caller's own root, not the server's.
		Workspace string `json:"workspace"`
	}
	_ = decodeBody(r, &body)
	var owner *identity.Scope
	if scope, ok := s.ownerScope(r); ok {
		owner = &scope
	}
	sess, err := s.mgr.CreateAs(body.Model, body.Workspace, owner)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cfg := sess.Model()
	writeJSON(w, http.StatusCreated, map[string]any{
		"session_id": sess.ID,
		"path":       sess.Path(),
		"model":      cfg.Model,
		"provider":   cfg.Provider,
		"workspace":  sess.Workspace(),
	})
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	snap := sess.Hub().Snapshot()
	value := map[string]any{
		"session_id": sess.ID,
		"snapshot":   snap,
	}
	if identity, err := s.mgr.sentinelIdentity(sess.ID); err != nil {
		writeErr(w, 500, err)
		return
	} else if identity != nil {
		value["sentinel"] = map[string]any{"watch_id": identity.WatchID, "parent_session_id": identity.ParentSessionID, "agent_id": identity.AgentID}
	}
	if identity, err := s.mgr.scheduleIdentity(sess.ID); err != nil {
		writeErr(w, 500, err)
		return
	} else if identity != nil {
		background := map[string]any{"source": identity.Source, "task_id": identity.TaskID, "source_id": identity.SourceID, "agent_id": identity.AgentID}
		if identity.RunID != "" {
			background["run_id"] = identity.RunID
		}
		value["background_task"] = background
	}
	if marker := s.mgr.agentWorkflowMarker(sess.ID); marker != nil {
		if !s.mgr.agentWorkflowSessionOwned(sess.ID, marker) {
			writeErr(w, 404, ErrNotFound)
			return
		}
		value["agent_workflow"] = marker
	}
	writeJSON(w, http.StatusOK, value)
}

// handleTranscript re-reads the session file from disk on every call.
//
// handleGetSession answers from the in-memory hub, which is frozen the moment
// the file is opened — right for a session this process runs, useless for a
// delegated run's transcript, which the child process keeps appending to. The
// dock's subagent sheet polls this endpoint instead: a fresh Open each time is
// what lets it watch a still-running child, and it never adopts the file into
// the manager, so reading a transcript cannot start anything.
func (s *Server) handleTranscript(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sid")
	if !validID(id) || !s.ownsSession(r, id) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	path := s.mgr.transcriptPath(id)
	if _, err := os.Stat(path); err != nil {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	store, err := session.Open(path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	hub := NewHub()
	hub.Seed(store.TimedMessages(""))
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": id,
		"snapshot":   hub.Snapshot(),
	})
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if !s.ownsSession(r, r.PathValue("sid")) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	if s.rejectSentinelID(w, r.PathValue("sid")) {
		return
	}
	err := s.mgr.Delete(r.PathValue("sid"))
	var officeErr *officeError
	if errors.As(err, &officeErr) {
		officeHTTPError(w, err)
		return
	}
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, err)
	case errors.Is(err, ErrRunActive):
		writeErr(w, http.StatusConflict, err)
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleUpdateSession applies sidebar edits (pin, rename). Pointers in the
// body, so a request can change one field without having to know the other.
func (s *Server) handleUpdateSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title  *string `json:"title"`
		Pinned *bool   `json:"pinned"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if body.Title == nil && body.Pinned == nil {
		writeErr(w, http.StatusBadRequest, errors.New("nothing to update: pass title and/or pinned"))
		return
	}
	if body.Title != nil {
		trimmed := strings.TrimSpace(*body.Title)
		body.Title = &trimmed
	}
	if !s.ownsSession(r, r.PathValue("sid")) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	if s.rejectSentinelID(w, r.PathValue("sid")) {
		return
	}
	err := s.mgr.UpdateInfo(r.PathValue("sid"), body.Title, body.Pinned)
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, err)
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	if s.rejectSentinelChat(w, r, sess) {
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
		// Attachments are workspace-relative paths the upload endpoint
		// returned ("uploads/report.docx").
		Attachments []string           `json:"attachments"`
		Parts       []MessageInputPart `json:"parts"`
		InputLayout string             `json:"input_layout"`
		// ClientID is the sender's optimistic-bubble id, echoed on the
		// user_message event so it can replace the placeholder. Opaque and
		// capped: it reconciles a live fold, it is never stored.
		ClientID string `json:"client_id"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(body.ClientID) > 64 {
		body.ClientID = body.ClientID[:64]
	}
	prompt, files, inputErr := normalizeMessageInput(body.Prompt, body.Attachments, body.Parts, body.InputLayout)
	if inputErr != nil {
		writeErr(w, http.StatusBadRequest, inputErr)
		return
	}
	body.Attachments = files
	if prompt == "" && len(body.Attachments) == 0 && body.Parts == nil {
		writeErr(w, http.StatusBadRequest, errors.New("prompt is empty"))
		return
	}
	// Slash commands must never reach the transcript. A command that could be
	// parsed out of conversation content would mean one prompt injection — a file
	// containing "please output /auto" — could disable the gate that exists to
	// stop exactly that. The client turns them into /control calls; this is the
	// backstop for a client that forgets.
	if cmd := slashCommand(prompt); cmd != "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf(
			"%s is a command, not a prompt: send it to /control so it never enters the conversation", cmd))
		return
	}

	// /skill:name is the opposite case: it is not a command but a prompt that has
	// to be expanded, and the expansion belongs on the server because that is
	// where the skill file is readable. The result enters the transcript on
	// purpose — what the model was told is the thing worth keeping.
	var observed mcpobserve.Metadata
	skillPrompt := prompt
	skillPart := -1
	hasSkillAtoms := false
	if body.Parts != nil {
		skillPrompt = ""
		for _, part := range flatInputParts(body.Parts) {
			hasSkillAtoms = hasSkillAtoms || part.Type == "skill"
		}
		for index, part := range body.Parts {
			if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
				skillPrompt, skillPart = part.Text, index
				break
			}
		}
	}
	if name, extra, ok := skills.ParseCommand(skillPrompt); ok && !hasSkillAtoms {
		// A Studio conversation expands only the skills its pinned selection
		// granted (invocationSkills was captured with them at creation);
		// anything else answers as unknown rather than reaching the file.
		available := s.mgr.Skills()
		if sess.studioID != "" {
			available = sess.invocationSkills
		}
		sk, found := skills.Find(available, name)
		if !found {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("no skill named %q", name))
			return
		}
		if sk.BrowserTask && !s.mgr.browserAllowed(sess.owner) {
			writeBrowserDeviceNotBound(w, http.StatusForbidden)
			return
		}
		// A browser-task skill opens the browser gate for the run this message
		// starts — the one place ordinary input can grant page operations, and
		// therefore the one place it has to. The run's end closes it (see
		// Session.Start), so nothing outlives the task. An Agent whose tool
		// selection withholds browser never arms: the grant rides with the
		// tool, and the registry it would serve does not have the tool.
		if sk.BrowserTask && sess.browserToolEnabled() {
			sess.Browser().Arm()
		}
		expanded, source, err := s.loadSkillExplicit(sess, sk, extra)
		if err != nil {
			// Found but unreadable: the load is real and failed. An unknown
			// name above records nothing — user input is not attribution.
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		prompt = expanded
		if body.Parts != nil {
			body.Parts = append([]MessageInputPart(nil), body.Parts...)
			body.Parts[skillPart].Text = expanded
		}
		// The explicit load opens the trace the run it becomes continues, so
		// the expansion and everything the model then does sit in one chain.
		// The run ID is not allocated until Start below; it stays empty in
		// the load event rather than being invented.
		observed = source
	}

	runID, err := sess.StartParts(prompt, body.Attachments, body.Parts, body.InputLayout, body.ClientID, observed)
	if body.Parts != nil && errors.Is(err, errMessageInputInvalid) {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if errors.Is(err, ErrRunActive) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": runID})
}

var slashCommands = []string{
	"/auto", "/strict", "/standard", "/model", "/usage", "/compact", "/help", "/exit", "/quit",
}

// slashCommand reports the command a prompt is, if it is one. Only an exact
// leading word counts, so a prompt that merely starts with a path is untouched.
func slashCommand(prompt string) string {
	word, _, _ := strings.Cut(prompt, " ")
	word = strings.TrimSpace(word)
	for _, c := range slashCommands {
		if word == c {
			return c
		}
	}
	return ""
}

// handleStream is the subscription. It is only a reader of the Hub: this handler
// dying, for any reason, has no effect on the run.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}

	// No `from` asks for a snapshot, which is what a page load wants: it has no
	// local state, and a snapshot is always correct. `from` is for a stream that
	// broke while the page stayed alive.
	from := int64(-1)
	if v := r.URL.Query().Get("from"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, errors.New("from must be a non-negative integer"))
			return
		}
		from = n
	}

	backlog, ch, cancel := sess.Hub().Subscribe(from, r.URL.Query().Get("epoch"))
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	// Tells nginx and friends not to buffer, which would defeat streaming.
	h.Set("X-Accel-Buffering", "no")
	// Subscribe cuts replay and live delivery under one lock. The browser can
	// fold backlog normally without treating old tool calls as new UI intents.
	// Do not read Hub.Seq here: a live event may already have been published.
	backlogThrough := from
	if len(backlog) > 0 {
		backlogThrough = backlog[len(backlog)-1].Seq
	}
	if backlogThrough < 0 {
		backlogThrough = 0
	}
	h.Set("X-Pogo-Stream-Backlog-Through", strconv.FormatInt(backlogThrough, 10))
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for _, e := range backlog {
		if !writeEvent(w, e) {
			return
		}
	}
	flusher.Flush()

	ping := time.NewTicker(keepalive)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ":\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case e, open := <-ch:
			if !open {
				// Either the session was evicted, or this subscriber fell too far
				// behind and was dropped. Both are recoverable by reconnecting
				// with ?from=<last seq>.
				return
			}
			if !writeEvent(w, e) {
				return
			}
			flusher.Flush()
		}
	}
}

func writeEvent(w http.ResponseWriter, e Event) bool {
	data, err := json.Marshal(e)
	if err != nil {
		// One unserialisable payload must not kill the stream.
		data, err = json.Marshal(Event{
			Seq: e.Seq, Type: EvError, TS: e.TS,
			Error: "event could not be serialised: " + err.Error(),
		})
		if err != nil {
			return false
		}
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Seq, e.Type, data)
	return err == nil
}

type controlRequest struct {
	Action string `json:"action"`

	RequestID string              `json:"request_id"`
	Answers   map[string][]string `json:"answers"`

	GateID   string          `json:"gate_id"`
	Allow    *bool           `json:"allow"`
	Args     json.RawMessage `json:"args"`
	Reason   string          `json:"reason"`
	Remember string          `json:"remember"`

	// Mode is read by two actions, which never co-occur: set_policy's approval
	// mode, and rewind's "chat" | "files" | "both". This struct is a flat bag
	// interpreted per action — Turns and AllowTool below are shared the same way.
	Mode         string `json:"mode"`
	Turns        int    `json:"turns"`
	AllowTool    string `json:"allow_tool"`
	AllowCommand string `json:"allow_command"`

	Model string `json:"model"`

	// Prompt carries a steering message. It goes through /control rather than
	// /messages because it must not start a run: it only joins one already in
	// flight, and the reply tells the client which happened.
	Prompt        string             `json:"prompt"`
	Parts         []MessageInputPart `json:"parts"`
	InputLayout   string             `json:"input_layout"`
	InboxID       string             `json:"id"`
	Revision      int                `json:"revision"`
	ExpectedRunID string             `json:"expected_run_id"`

	// MessageID identifies the timeline message a rewind forks away from.
	MessageID string `json:"message_id"`

	// TaskID identifies a background subagent for subagent_cancel.
	TaskID string `json:"task_id"`

	// Rewind reads Mode above for what to act on. It replaced a files bool,
	// because a bool could not express the third state — restore the work tree and
	// leave the conversation where it is.
	//
	// Paths narrows a file restore to a subset of what the preview listed. Empty
	// means the whole checkpoint. Only meaningful for the two file modes.
	Paths []string `json:"paths"`
}

func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	var req controlRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if s.mgr.agentWorkflowMarker(sess.ID) != nil {
		switch req.Action {
		case "gate_decide", "gate_freeze", "gate_thaw":
		default:
			writeErr(w, 409, errors.New("内部流程执行会话仅允许处理工具审批，请在工作流面板控制运行"))
			return
		}
		if req.Remember != "" {
			writeErr(w, 400, errors.New("流程工具审批仅对本次调用有效"))
			return
		}
	}
	switch req.Action {
	case "inbox_send", "inbox_steer", "inbox_edit", "inbox_withdraw", "steer", "rewind", "compact", "set_model", "set_policy":
		if s.rejectSentinelChat(w, r, sess) {
			return
		}
	}

	switch req.Action {
	case "inbox_send", "inbox_steer", "inbox_edit", "inbox_withdraw":
		var item InboxItem
		var err error
		switch req.Action {
		case "inbox_send":
			item, err = sess.ReceiveParts(req.InboxID, req.Prompt, req.Parts, req.InputLayout, req.Mode, req.ExpectedRunID)
		case "inbox_steer":
			if !validInboxID(req.InboxID) || req.Revision < 1 {
				err = errInboxInvalid
			} else {
				item, err = sess.activateInput(req.InboxID, req.Revision, req.ExpectedRunID, false)
			}
		case "inbox_edit", "inbox_withdraw":
			item, err = sess.EditParts(req.InboxID, req.Prompt, req.Parts, req.InputLayout, req.Revision, req.Action == "inbox_withdraw")
		}
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errInboxConflict) {
				status = http.StatusConflict
			}
			if errors.Is(err, errInboxInvalid) {
				status = http.StatusBadRequest
			}
			if errors.Is(err, ErrNotFound) {
				status = http.StatusNotFound
			}
			writeErr(w, status, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"accepted": true, "item": item})

	case "steer":
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" {
			writeErr(w, http.StatusBadRequest, errors.New("prompt is empty"))
			return
		}
		// A slash command must not become a prompt here either; the same reasoning
		// as in handleMessages applies, and a steering message is the easier of the
		// two to sneak one into.
		if cmd := slashCommand(prompt); cmd != "" {
			writeErr(w, http.StatusBadRequest, fmt.Errorf(
				"%s is a command, not a prompt", cmd))
			return
		}
		// `steered: false` means there was no run to join, which is not an error:
		// the client's next move is to send it as an ordinary message. Answering
		// with a status code would make the normal race look like a failure.
		writeJSON(w, http.StatusOK, map[string]any{"steered": sess.Steer(prompt)})

	case "cancel":
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": sess.Cancel()})

	case "subagent_cancel":
		// Not found rather than a bare false: a task id the registry does not
		// know is either finished or never existed, and the client can tell the
		// two apart from the card's state.
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": sess.CancelBg(req.TaskID)})

	case "rewind":
		if req.MessageID == "" {
			writeErr(w, http.StatusBadRequest, errors.New("message_id is required"))
			return
		}
		// No default mode. A rewind is destructive, and guessing which half of it
		// was meant is the one place a helpful default would be dangerous.
		err := sess.Rewind(req.MessageID, RewindMode(req.Mode), req.Paths)
		switch {
		case errors.Is(err, ErrRunActive):
			writeErr(w, http.StatusConflict, err)
		case errors.Is(err, errMessageUnknown):
			writeErr(w, http.StatusNotFound, err)
		case errors.Is(err, errFilesUnavailable):
			writeErr(w, http.StatusUnprocessableEntity, err)
		case errors.Is(err, errRewindMode):
			writeErr(w, http.StatusBadRequest, err)
		case err != nil:
			writeErr(w, http.StatusInternalServerError, err)
		default:
			writeJSON(w, http.StatusOK, map[string]any{"rewound": true})
		}

	case "rewind_preview":
		if req.MessageID == "" {
			writeErr(w, http.StatusBadRequest, errors.New("message_id is required"))
			return
		}
		changes, available, err := sess.RewindPreview(req.MessageID)
		switch {
		case errors.Is(err, errMessageUnknown):
			writeErr(w, http.StatusNotFound, err)
		case err != nil:
			writeErr(w, http.StatusInternalServerError, err)
		default:
			if changes == nil {
				changes = []FileChange{}
			}
			writeJSON(w, http.StatusOK, map[string]any{"available": available, "changes": changes})
		}

	case "compact":
		res, err := sess.Compact(r.Context())
		switch {
		case errors.Is(err, ErrRunActive):
			writeErr(w, http.StatusConflict, err)
		case errors.Is(err, agent.ErrNothingToCompact), errors.Is(err, agent.ErrNotSmaller):
			// The request was well formed and the answer is "this would not help".
			// 422 rather than 400: nothing about the call was wrong.
			writeErr(w, http.StatusUnprocessableEntity, err)
		case errors.Is(err, agent.ErrEmptySummary), errors.Is(err, agent.ErrRaced):
			writeErr(w, http.StatusConflict, err)
		case err != nil:
			writeErr(w, http.StatusInternalServerError, err)
		default:
			writeJSON(w, http.StatusOK, map[string]any{
				"compacted":     true,
				"before":        res.Before,
				"before_tokens": res.BeforeTokens,
				"after_tokens":  res.AfterTokens,
				"freed_tokens":  res.Freed(),
				"summary":       res.Summary,
			})
		}

	case "set_model":
		cfg, err := sess.SetModel(req.Model)
		if errors.Is(err, ErrRunActive) {
			writeErr(w, http.StatusConflict, err)
			return
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"model": cfg.Model, "provider": cfg.Provider})

	case "set_policy":
		switch {
		case req.AllowTool != "":
			writeJSON(w, http.StatusOK, sess.AllowTool(req.AllowTool))
		case req.AllowCommand != "":
			writeJSON(w, http.StatusOK, sess.AllowCommand(req.AllowCommand))
		default:
			mode, ok := ParseMode(req.Mode)
			if !ok {
				writeErr(w, http.StatusBadRequest, errors.New(`mode must be "strict", "standard" or "auto"`))
				return
			}
			writeJSON(w, http.StatusOK, sess.SetPolicy(mode, req.Turns))
		}

	case "gate_decide":
		if req.Allow == nil {
			writeErr(w, http.StatusBadRequest, errors.New("allow is required"))
			return
		}
		err := sess.Gate().Decide(req.GateID, Verdict{
			Allow: *req.Allow, Args: req.Args, Reason: req.Reason, Remember: req.Remember,
		})
		if err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "user_input_answer", "user_input_cancel":
		if sess.userInput == nil {
			writeErr(w, http.StatusConflict, ErrUserInputUnavailable)
			return
		}
		var duplicate bool
		var err error
		if req.Action == "user_input_answer" {
			duplicate, err = sess.userInput.Answer(req.RequestID, req.Revision, req.Answers)
		} else {
			duplicate, err = sess.userInput.Cancel(req.RequestID, req.Revision)
		}
		switch {
		case errors.Is(err, ErrUserInputNotFound):
			writeErr(w, http.StatusNotFound, err)
		case errors.Is(err, ErrUserInputRevision), errors.Is(err, ErrUserInputResolved):
			writeErr(w, http.StatusConflict, err)
		case err != nil:
			writeErr(w, http.StatusBadRequest, err)
		default:
			writeJSON(w, http.StatusOK, map[string]any{"accepted": true, "duplicate": duplicate})
		}

	case "gate_freeze", "gate_thaw":
		var err error
		if req.Action == "gate_freeze" {
			err = sess.Gate().Freeze(req.GateID)
		} else {
			err = sess.Gate().Thaw(req.GateID)
		}
		if err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "browser_result":
		writeErr(w, http.StatusGone, errors.New("browser_result is retired; browser operations execute through the native runtime"))

	default:
		writeErr(w, http.StatusBadRequest, fmt.Errorf("unknown action %q", req.Action))
	}
}

// handleAPIListing is what gets served when the front end has not been built.
// A short page describing the API beats a blank screen, since the server is
// perfectly usable with curl.
func (s *Server) handleAPIListing(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, apiListingHTML)
}

const apiListingHTML = `<!doctype html>
<meta charset="utf-8">
<title>pogo</title>
<body style="font:14px/1.6 ui-monospace,monospace;max-width:44rem;margin:4rem auto;padding:0 1rem">
<h1>pogo</h1>
<p>The HTTP and SSE layer is running, but the browser UI has not been built into
this binary. Build it with:</p>
<pre>cd web/ui &amp;&amp; npm ci &amp;&amp; npm run build
go build -o pogo .</pre>
<p>Everything below works over curl in the meantime.</p>
<pre>GET    /api/models
GET    /api/sessions
GET    /api/sessions/search?q=...&limit=  full-text search over every transcript (session index)
POST   /api/sessions
GET    /api/sessions/{id}
GET    /api/sessions/{id}/transcript   fresh read off disk; also finds subagents/ transcripts
PATCH  /api/sessions/{id}              {"title":"...","pinned":true}（可只传一个）
DELETE /api/sessions/{id}
POST   /api/sessions/{id}/messages    {"prompt":"..."}
GET    /api/sessions/{id}/stream[?from=N]
POST   /api/sessions/{id}/control     {"action":"cancel"|"subagent_cancel"|"rewind"|"rewind_preview"|"steer"|"set_model"|"set_policy"|"gate_decide"|"gate_freeze"|"gate_thaw"}</pre>
<p>Pass the token as <code>Authorization: Bearer &lt;token&gt;</code>, or <code>?token=</code> for the stream.</p>
</body>
`

// ownsSession is the ownership half of every {sid} route: in multi-user mode
// the catalog must show this id as an active session of the caller's scope
// before anything else — Manager.Get by bare id would otherwise restore
// anybody's transcript. Another user's session answers exactly like a missing
// one (404), which is the property the whole isolation model rests on.
func (s *Server) ownsSession(r *http.Request, id string) bool {
	if isBrowserAuthoringID(id) {
		return false
	}
	if !s.multiuser() {
		return true
	}
	scope, ok := s.ownerScope(r)
	if !ok || !validID(id) {
		return false
	}
	_, err := s.mgr.cfg.Catalog.Get(r.Context(), scope, id)
	return err == nil
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	// Browser-only authoring handles never grant conversation capabilities,
	// even on an otherwise unauthenticated single-user server.
	if isBrowserAuthoringID(r.PathValue("sid")) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return nil, false
	}
	if !s.ownsSession(r, r.PathValue("sid")) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return nil, false
	}
	sess, err := s.mgr.Get(r.PathValue("sid"))
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return nil, false
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return nil, false
	}
	return sess, true
}

// handleTerminal upgrades to a websocket attached to the session's shell. The
// token middleware gates it like every other /api route — an unauthenticated
// shell here would be remote code execution as a service, the same line the
// rest of the server draws.
func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	if s.rejectBackgroundTaskWrite(w, sess.ID) {
		return
	}
	term, err := sess.Terminal()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Same-origin pages only: the Origin middleware has already run, so no
	// extra check is needed on upgrade.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		return // Accept has already answered with the error
	}
	term.Attach(r.Context(), c)
}

// decodeBody reads a JSON body, tolerating an empty one so that actions without
// parameters need no payload.
func decodeBody(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
