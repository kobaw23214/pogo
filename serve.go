package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/yosukeno/pogo/catalog"
	"github.com/yosukeno/pogo/mcpclient"
	"github.com/yosukeno/pogo/memory"
	"github.com/yosukeno/pogo/observability"
	"github.com/yosukeno/pogo/prefs"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
	"github.com/yosukeno/pogo/web"
	"github.com/yosukeno/pogo/webservice"
)

type webOptions struct {
	listen              string
	dev                 string
	model               string
	cwd                 string
	maxTurns            int
	retries             int
	stagnationThreshold int
	tokenBudget         int64
	costBudget          float64
	timeBudget          time.Duration
	gateTimeout         time.Duration
	// catalog is the per-user session ownership registry, opened in serveWeb
	// when an identity source arms isolation and handed to the Manager.
	catalog *catalog.Store
	// panels are the raw -web-panel values ("name=url"), parsed in serveWeb.
	panels []string
	// compact is the raw -compact spec, resolved per session by the Manager the
	// same way contextEdit is: the threshold is a function of the session's model.
	compact string
	// contextEdit is the raw -context-edit spec, resolved per session by the Manager:
	// "auto" is a fraction of the model's window, and the browser can switch models
	// mid-session.
	contextEdit string
	// citations is the positive form of -no-citations: whether each session
	// stamps tool results with source labels and asks the model to cite them.
	citations bool
	// followups is -followups: whether a run's end triggers generated follow-up
	// suggestion chips. On by default; off leaves rule-based matching as the
	// only source.
	followups bool
	skills    skills.Options
	memory    memory.Options
}

// serveWeb runs the HTTP/SSE server.
//
// Two defaults here are security decisions rather than conveniences. The bind
// address is loopback, and a token is always required — there is no way to turn
// it off. pogo's bash tool has no path restriction (read, write and edit are
// confined by the path guard; bash is a shell), so an open instance is a remote
// shell. A generated token printed on startup, like Jupyter does, costs nothing
// and removes the failure mode where someone ships the unauthenticated version.
func serveWeb(o webOptions) error {
	cwd := o.cwd
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return err
		}
	}

	token := strings.TrimSpace(os.Getenv("POGO_WEB_TOKEN"))
	generated := false
	if token == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		token, generated = hex.EncodeToString(b), true
	}

	// The password-login users file is the one identity source that arms
	// per-user isolation: with it the server switches from the shared-token
	// single-user arrangement to per-user session spaces and closes the shared
	// token channel.
	usersFile := os.Getenv("POGO_USERS_FILE")
	if usersFile != "" {
		// The ownership catalog sits beside the transcripts it governs and is
		// the authority for who owns which session id. A failure to open it is
		// a startup failure, never a silent fall-back to shared sessions.
		sessionDir := session.DefaultDir()
		cat, err := catalog.Open(filepath.Join(sessionDir, "catalog.db"))
		if err != nil {
			return fmt.Errorf("session ownership catalog: %w", err)
		}
		defer cat.Close()
		o.catalog = cat
		fmt.Printf("pogo web  identity: per-user session isolation is ON; the shared web token channel is closed\n")
	}

	// Discovered once for the whole server: Cwd is fixed, so every session sees
	// the same skills, and re-scanning per session could only produce a set that
	// disagrees with the one already written into a session's metadata.
	o.skills.Cwd = cwd
	// The built-in skills ship in the binary; web mode is where they apply (the
	// terminal does not render diagrams or charts), so they are materialized
	// here rather than in the shared flag handling. Skipped under -no-skills,
	// like the user directory — a default, not an explicit request.
	if !o.skills.Disabled {
		materialized := false
		for i, dir := range embeddedSkillCandidates() {
			dirs, err := skills.MaterializeEmbedded(dir)
			if err != nil {
				continue
			}
			o.skills.Embedded = dirs
			materialized = true
			// Said only on the fallback: the preferred location failing is
			// normal for an unprivileged user, and a warning that fires on
			// every startup teaches people to stop reading stderr.
			if i > 0 {
				fmt.Fprintf(os.Stderr, "embedded skills: %s not writable, materialized under %s\n",
					embeddedSkillCandidates()[0], dir)
			}
			break
		}
		if !materialized {
			fmt.Fprintln(os.Stderr, "embedded skills unavailable")
		}
	}
	skillList, skillDiags := skills.Load(o.skills)
	reportSkillDiagnostics(skillDiags)

	// Resolved once for the whole server, like skills and for the same reason.
	o.memory.Cwd = cwd
	mem, memDiags := memory.Load(o.memory)
	for _, d := range memDiags {
		fmt.Fprintln(os.Stderr, d)
	}

	// Sweep sessions that were created but never used: the web UI writes the
	// file at creation, so every abandoned "new session" would otherwise pile
	// up in the sidebar. Best-effort — a failure here must not block startup.
	sessionDir := session.DefaultDir()
	cleaned, _ := cleanEmptySessionsForServe(sessionDir)

	var runtimeSkills *skillruntime.Service
	if runtimeSkillRoot := strings.TrimSpace(os.Getenv("POGO_SKILL_RUNTIME_STORE")); runtimeSkillRoot != "" {
		service, runtimeSkillErr := skillruntime.NewManagementService(runtimeSkillRoot)
		if runtimeSkillErr != nil {
			return fmt.Errorf("runtimeSkill store: %w", runtimeSkillErr)
		}
		switch mode := strings.TrimSpace(os.Getenv("POGO_SKILL_RUNTIME_EXECUTOR")); mode {
		case "", "local":
			executor, err := skillruntime.NewLocalExecutor(context.Background(), runtimeSkillRoot)
			if err != nil {
				// Keep management reachable, with an explicit blocking issue. A
				// failed namespace probe never enables a raw subprocess fallback.
				service.SetExecutionError(err)
				fmt.Fprintf(os.Stderr, "runtimeSkill execution unavailable: %v\n", err)
			} else {
				service.Executor = executor
			}
		case "disabled":
			service.SetExecutionError(&skillruntime.ExecutionError{Code: "EXECUTOR_DISABLED", Message: "Skill 执行已由 POGO_SKILL_RUNTIME_EXECUTOR=disabled 停用"})
		default:
			return fmt.Errorf("POGO_SKILL_RUNTIME_EXECUTOR: unsupported mode %q; use local or disabled", mode)
		}
		runtimeSkills = service
	}
	var managedHost atomic.Pointer[web.Manager]
	managedServices, managedPrivateDirs, managedCleanup, err := managedWebServicesFromEnv(func(record webservice.Record) {
		if host := managedHost.Load(); host != nil {
			host.ManagedWebServiceChanged(record)
		}
	})
	if err != nil {
		return err
	}
	defer managedCleanup()
	mgr, err := web.NewManager(web.Config{
		WebServices:           managedServices,
		WebServicePrivateDirs: managedPrivateDirs,
		BrowserURL:            os.Getenv("POGO_BROWSER_URL"),
		BrowserToken:          os.Getenv("POGO_BROWSER_TOKEN"),
		BrowserDesktopOwner:   os.Getenv("POGO_BROWSER_DESKTOP_OWNER"),
		SkillRuntime:          runtimeSkills,
		Cwd:                   cwd,
		SessionDir:            sessionDir,
		Catalog:               o.catalog,
		Model:                 o.model,
		Retries:               o.retries,
		MaxTurns:              o.maxTurns,
		GateTimeout:           o.gateTimeout,
		ContextEdit:           o.contextEdit,
		Compact:               o.compact,
		Citations:             o.citations,
		Followups:             o.followups,
		Skills:                skillList,
		// The options the boot scan used, for the runtime reload a skill
		// upload triggers: same locations, same flags.
		SkillOptions: o.skills,
		Memory:       mem,
		// The web server is the one long-lived process pogo has, so it owns
		// the spill directory's timed sweep (the CLI's is the -spill-prune
		// command, same philosophy as -checkpoints-prune).
		SpillSweepInterval: web.DefaultSpillSweepInterval,
		// And the schedule timer — inert wherever an OS scheduler owns the
		// triggers, the only clock in a container. See web/scheduler.go.
		ScheduleTickInterval: web.DefaultScheduleTickInterval,
		// Already merged from -read-root and the environment by the caller, so
		// this is the same list a subagent child will inherit.
		ReadRoots: readRootsEnv(),
		// The MCP client pool (the mirror of the MCP server surface below):
		// mcpclient.Path() honours POGO_MCP_FILE, so the web server, the CLI
		// and the /api/mcp editor all read and write one mcp.json.
		MCPClientStore: mcpclient.Path(),
	})
	if err != nil {
		return err
	}
	managedHost.Store(mgr)
	defer mgr.Close()

	opts := web.ServerOptions{
		Token:    token,
		DevProxy: o.dev,
		Logger:   log.New(os.Stderr, "pogo: ", 0),
	}
	certificate, key := os.Getenv("POGO_WEB_TLS_CERT"), os.Getenv("POGO_WEB_TLS_KEY")
	if certificate != "" || key != "" {
		if certificate == "" || key == "" {
			return fmt.Errorf("POGO_WEB_TLS_CERT and POGO_WEB_TLS_KEY must be configured together")
		}
		pair, err := tls.LoadX509KeyPair(certificate, key)
		if err != nil {
			return fmt.Errorf("web TLS certificate could not be loaded: %w", err)
		}
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}
	}
	for _, raw := range o.panels {
		name, u, ok := strings.Cut(raw, "=")
		if !ok {
			return fmt.Errorf("-web-panel %q: want name=url", raw)
		}
		opts.Panels = append(opts.Panels, web.Panel{Name: name, URL: u})
	}
	// Runtime-registered panels (POST /api/panels, the add-shortcut dialog's
	// proxy option) persist beside the sessions: same state root, same
	// lifetime. POGO_PANELS_FILE moves it elsewhere (e.g. onto a volume).
	opts.PanelStore = os.Getenv("POGO_PANELS_FILE")
	if opts.PanelStore == "" {
		opts.PanelStore = filepath.Join(filepath.Dir(sessionDir), "panels.json")
	}
	// The panel proxy fetches server-side: inside a container the browser
	// user's "localhost" is the container itself. Container deployments set
	// this to host.docker.internal so loopback URLs keep meaning "the host".
	opts.PanelLoopbackMap = os.Getenv("POGO_PANEL_LOOPBACK_MAP")
	// Off by default. Registration is the grant that makes an arbitrary page
	// same-origin with the token-holding UI; when it can happen as a side
	// effect of a navigate, "go to evil.example" is a one-line takeover of
	// every /api route. On means trusting every navigation target with the
	// keys to the UI, which is why it is an explicit env, not a flag default.
	opts.PanelAutoRegister = os.Getenv("POGO_PANEL_AUTO_REGISTER") == "1"
	// The starter-card editor's persistence, same state root as the panels.
	// POGO_STARTERS_FILE moves it onto a volume (this deployment points it at
	// /data, so edits survive container rebuilds).
	opts.StartersStore = os.Getenv("POGO_STARTERS_FILE")
	if opts.StartersStore == "" {
		opts.StartersStore = filepath.Join(filepath.Dir(sessionDir), "starters.json")
	}
	// Shared-answer snapshots, same state root again: a link someone was sent
	// must survive the server that made it. POGO_SHARES_FILE moves it onto a
	// volume for the same reason as the starters.
	opts.SharesStore = os.Getenv("POGO_SHARES_FILE")
	if opts.SharesStore == "" {
		opts.SharesStore = filepath.Join(filepath.Dir(sessionDir), "shares.json")
	}
	// The settings dialog's storage section edits prefs.json (spill retention).
	// prefs.Path() already honours POGO_PREFS_FILE, so the server and the CLI's
	// -spill-prune read and write the same file either way.
	opts.PrefsStore = prefs.Path()
	// The MCP client editor's file, same arrangement: one mcp.json shared with
	// the CLI and the Manager's pool (wired above).
	opts.MCPClientStore = mcpclient.Path()
	// The MCP server side (web/mcp.go) is configured by mcp.json's "server"
	// section with the POGO_MCP* environment on top — env wins, both are read
	// live by the layer (web/mcpserve.go), so this process no longer freezes
	// any of it into ServerOptions. The one check kept here is the policy
	// enum: a typoed POGO_MCP_POLICY should shout at startup, while someone
	// is still reading, rather than fall to the default per request.
	if policy := strings.TrimSpace(os.Getenv("POGO_MCP_POLICY")); policy != "" {
		if _, ok := web.ParseMode(policy); !ok {
			return fmt.Errorf("POGO_MCP_POLICY: want strict, standard or auto, got %q", policy)
		}
	}
	// The MCP request log (web/mcplog.go): a per-process decision, so it is
	// wired here rather than living in mcp.json's live server section. The
	// database sits beside the session store (both are agent state, both ride
	// the same named volume in the container deployment).
	mcpLog := &web.MCPLogOptions{}
	if v, err := strconv.Atoi(os.Getenv("POGO_MCP_LOG_RING")); err == nil && v > 0 {
		mcpLog.Ring = v
	}
	if v, err := strconv.Atoi(os.Getenv("POGO_MCP_LOG_RETAIN_DAYS")); err == nil && v >= 0 {
		mcpLog.RetainDays = v
	}
	if v, err := strconv.Atoi(os.Getenv("POGO_MCP_LOG_MAX_ROWS")); err == nil && v > 0 {
		mcpLog.MaxRows = v
	}
	mcpLog.PayloadOff = os.Getenv("POGO_MCP_LOG_PAYLOAD") == "0"
	mcpLog.CallsOff = os.Getenv("POGO_MCP_CALLS") == "0"
	mcpLog.SkillCallsOff = os.Getenv("POGO_SKILL_CALLS") == "0"
	mcpLog.DBPath = strings.TrimSpace(os.Getenv("POGO_MCP_LOG_DB"))
	if mcpLog.DBPath == "" {
		mcpLog.DBPath = filepath.Join(filepath.Dir(sessionDir), "mcplog.db")
	}
	opts.MCPLog = mcpLog
	if exporter, exportErr := observability.OTLPOptionsFromEnv(os.Getenv); exportErr != nil {
		opts.OTLPConfigError = true
		if opts.Logger != nil {
			opts.Logger.Print("observability: invalid OTLP environment configuration; export disabled")
		}
	} else if exporter.Endpoint != "" {
		opts.OTLP = &exporter
	}
	// Password login (web/auth.go): setting POGO_USERS_FILE enables it — the
	// login page replaces the token gate, and each account gets its own
	// session space. Empty keeps the classic token-only mode.
	opts.UsersFile = usersFile
	if o.dev != "" {
		// The vite dev server serves the page, so its origin has to be accepted.
		opts.AllowedOrigins = []string{strings.TrimSuffix(o.dev, "/")}
	}
	srv, err := web.NewServer(mgr, opts)
	if err != nil {
		return err
	}

	fmt.Printf("pogo web  model=%s  cwd=%s\n", o.model, mgr.Cwd())
	if cleaned > 0 {
		fmt.Printf("  cleaned %d empty session(s)\n", cleaned)
	}
	if len(skillList) > 0 {
		fmt.Printf("  %d skill(s): %s\n", len(skillList),
			strings.Join(skills.Names(skillList), ", "))
	}
	if roots := readRootsEnv(); len(roots) > 0 {
		fmt.Printf("  read roots: %s\n", strings.Join(roots, ", "))
	}
	for _, p := range opts.Panels {
		fmt.Printf("  panel %q → %s\n", p.Name, p.URL)
	}
	if st := srv.MCPServeState(); st.Enabled {
		fmt.Printf("  mcp: /mcp (streamable http) + /sse (legacy), policy %s, source %s\n",
			st.Policy, st.Source)
	}
	if usersFile != "" {
		// No token URL in multi-user mode: the token channel is closed, and a
		// link that cannot work is a support ticket waiting to happen. The
		// browser flow is the password login.
		fmt.Printf("  http://%s/manage\n", displayAddr(o.listen))
	} else {
		fmt.Printf("  http://%s/manage?token=%s\n", displayAddr(o.listen), token)
		if generated {
			fmt.Println("  token generated for this run; set POGO_WEB_TOKEN to pin it")
		}
	}
	// A sandbox override persisted via the settings dialog is restored before
	// the first probe report, so the report already speaks in the effective
	// mode. The prefs file is also created up front when missing: the sandbox
	// deny-binds it once it exists (tools/sandbox.go homeDenyFiles), and a
	// settings file a sandboxed command can *create* is the same hole as one
	// it can rewrite, just a restart later.
	if p, err := prefs.Load(prefs.Path()); err == nil {
		if p.SandboxMode != "" {
			if serr := tools.SetSandboxOverride(p.SandboxMode); serr == nil {
				fmt.Printf("pogo: sandbox mode override %q restored from %s\n", p.SandboxMode, prefs.Path())
			}
		}
		if _, statErr := os.Stat(prefs.Path()); os.IsNotExist(statErr) {
			_ = prefs.Save(prefs.Path(), p) // best-effort: nothing depends on it
		}
	}
	// Probed here rather than lazily on the first command, so a deployment that
	// asked for containment and is not getting it says so while someone is still
	// reading the startup output. The mode is also what decides the wording of
	// the exposure warning below, which used to state flatly that bash is not
	// sandboxed — true when it was written, and the kind of stale reassurance
	// that is worse than no message.
	sandbox := tools.InitSandbox()
	if !loopback(o.listen) {
		confined := "and is not sandboxed"
		if sandbox.Active() {
			confined = "inside a sandbox (workspace, HOME and TMPDIR writable, no network)"
		}
		fmt.Printf("  ! %s is not loopback. The bash tool runs commands with your privileges\n"+
			"    %s — only do this on a network you control.\n",
			o.listen, confined)
	}
	if o.dev != "" {
		fmt.Printf("  proxying non-API routes to %s\n", o.dev)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return srv.Serve(ctx, o.listen)
}

// embeddedSkillCandidates lists where the binary's built-in skills may be
// materialized, in preference order.
//
// The deployment-shared /opt/skills comes first: /opt is FHS's home for
// add-on packages, and a shared location is the one every user of the host
// can read. Root can pre-populate it once and every later process reuses it
// read-only — MaterializeEmbedded skips writes whose bytes already match, so
// an unprivileged user succeeds against a directory they could not have
// written. Not anywhere under ~/.pogo/skills: that directory is the user's,
// and a built-in appearing in it would look like a skill the user wrote.
//
// The per-user cache is the fallback for the common case that /opt is not
// writable and nobody pre-populated it. POGO_EMBEDDED_SKILLS_DIR overrides the
// head of the list, same deal as the other POGO_* location overrides.
func embeddedSkillCandidates() []string {
	var out []string
	if v := os.Getenv("POGO_EMBEDDED_SKILLS_DIR"); v != "" {
		out = append(out, v)
	}
	out = append(out, "/opt/skills")
	if base, err := os.UserCacheDir(); err == nil && base != "" {
		return append(out, filepath.Join(base, "pogo", "embedded-skills"))
	}
	return append(out, filepath.Join(os.TempDir(), "pogo-embedded-skills"))
}

// loopback reports whether an address is reachable only from this machine. A
// hostname that does not parse as an IP is treated as remote: guessing in the
// permissive direction is the wrong way to be wrong here.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "", "localhost":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// displayAddr turns a wildcard bind into something clickable.
func displayAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return "127.0.0.1:" + port
	}
	return addr
}

// A controlled release verifies every pre-existing transcript after startup.
// Preserve even empty sessions while its maintenance marker is present; normal
// startup housekeeping must not silently change that recorded data boundary.
func cleanEmptySessionsForServe(dir string) (int, error) {
	_, err := os.Stat(filepath.Join(filepath.Dir(dir), "release-maintenance.json"))
	if !os.IsNotExist(err) {
		return 0, err
	}
	return session.CleanEmpty(dir)
}
