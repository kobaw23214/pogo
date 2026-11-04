package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/catalog"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpclient"
	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/memory"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

// subPaneIf loans the browser pane to this session's children, or withholds
// the loan when the definition's tool selection removed the browser tool.
func subPaneIf(enabled bool, g *BrowserGate) tools.BrowserLink {
	if !enabled {
		return nil
	}
	return subPane{g}
}

// Timeouts that replace "the client hung up" as the way a run ends.
//
// The CLI can rely on a person being present; a server cannot. Since a run
// deliberately survives its originating connection, these are the only things
// standing between a stuck loop and a process that never lets go of it.
const (
	DefaultRunTimeout  = 30 * time.Minute
	DefaultIdleTimeout = 15 * time.Minute
)

var (
	// ErrRunActive is the 409 case: one run at a time per session.
	ErrRunActive = errors.New("a run is already in progress for this session")
	ErrNotFound  = errors.New("session not found")
)

type Config struct {
	// Catalog is the authoritative session-ownership registry (catalog/). Nil
	// keeps the classic single-user mode: no owner anywhere, every session
	// visible to the shared token. Non-nil switches the server to per-user
	// isolation — sessions are created with an owner scope, listed and reached
	// only through it, and each user's workspace and memory live under their own
	// root inside the server tree.
	Catalog *catalog.Store
	// Cwd is the server root, fixed at startup. A session's own working
	// directory is the root or one of its subdirectories (see Create): the path
	// guard is per-session, but it never bites anywhere outside this tree.
	// In multi-user mode each owner's sessions default to their own root under
	// <Cwd>/users/, which is still inside this tree.
	Cwd        string
	SessionDir string
	Model      string
	Retries    int
	MaxTurns   int
	// Native browser worker. Empty URL leaves browser operations unavailable.
	BrowserURL   string
	BrowserToken string
	browser      *nativeBrowserConfig
	// BrowserDesktopOwner binds a desktop worker to exactly one local account.
	// Empty preserves the shared headless-worker deployment contract.
	BrowserDesktopOwner string
	browserDesktopOwner *identity.Scope

	// Skills are already discovered by the caller. Loading them here would mean
	// re-scanning per session for no benefit: Cwd is fixed for the whole server,
	// so the set cannot differ between sessions.
	Skills []skills.Skill

	// SkillOptions are the options Skills was loaded with. The set is fixed no
	// longer — the skills sheet can upload a bundle at runtime — and a reload
	// needs the same locations and flags the boot scan used, or the uploaded
	// skill would install into a directory the next scan disagrees with.
	SkillOptions skills.Options

	// ReadRoots are extra directories the read-only tools may reach outside the
	// Cwd tree — tool installs, data mounts — from -read-root / POGO_READ_ROOTS.
	// The skill roots (below) exist so a bundle's instructions stay readable; these
	// exist so the things the instructions point *at* are. Same grant, never
	// writable, and fixed for the whole server like Skills.
	ReadRoots []string
	// Memory is the agent's note store, already resolved by the caller for the same
	// reason Skills is: Cwd is fixed for the whole server, so re-resolving per session
	// could only produce a view that disagrees with the one a session already recorded.
	//
	// Nil is valid and means -no-memory: Store's methods all tolerate a nil receiver,
	// so there is no branch here.
	Memory *memory.Store
	// MemoryAuto enables post-run memory distillation for new interactive sessions.
	MemoryAuto bool

	GateTimeout time.Duration
	RunTimeout  time.Duration
	IdleTimeout time.Duration

	// ContextEdit is the raw -context-edit spec, resolved per session against that
	// session's model. Kept as the spec rather than a resolved config because "auto"
	// means a fraction of the model's window and the browser can switch models mid-session,
	// so a value resolved once at startup would be wrong after the first switch.
	// Empty means "auto".
	ContextEdit string

	// Compact is the raw -compact spec, kept and resolved for exactly the
	// ContextEdit reasons. Empty means "off" — compaction stays on the /compact
	// button unless the flag armed it.
	Compact string

	// Citations stamps every tool result with a source label and asks the model
	// to cite them (agent.CitationSection). Same zero-value convention as
	// other optional features — off unless requested, so a test's Manager
	// produces answers with no [cite:...] headers nobody scripted.
	Citations bool

	// Followups turns on generated follow-up suggestion chips: after a run ends,
	// a model call predicts what the user asks next and publishes it as chips.
	// Same zero-value convention as Citations — off unless the flag asked for
	// it, so a test's Manager never makes a call nobody scripted.
	Followups bool

	// SpillSweepInterval turns on the spill directory's automatic sweeper
	// (web/spillsweep.go): one pass at startup, then one per interval.
	// Same zero-value convention again — off unless the caller asked, because
	// this one *deletes files*, and a test's Manager must never remove
	// anything nobody planted. serveWeb sets DefaultSpillSweepInterval; the
	// manual form (-spill-prune, POST /api/prefs/prune-spill) needs no timer.
	SpillSweepInterval time.Duration

	// ScheduleTickInterval turns on the in-process schedule timer
	// (web/scheduler.go) and is how often it re-reads jobs.json between
	// deadlines — the cadence at which a job added by the CLI or the panel
	// becomes visible to it. The timer stands down by itself when an OS
	// backend owns the triggers, so on a laptop it is inert overlap
	// protection rather than a second clock; it earns its keep in a
	// container, where it is the only clock there is. Zero keeps the
	// goroutine off (tests); serveWeb sets DefaultScheduleTickInterval.
	ScheduleTickInterval time.Duration

	// NewClient overrides how an LLM client is built. It exists so tests can
	// drive a whole run without a network, which is the only way to verify the
	// things that matter here: that a run outlives its connection and that an
	// unanswered approval turns into a tool result instead of a dead loop.
	NewClient func(config.Resolved, func(llm.RetryInfo)) llm.Client

	// MCPClientStore is the mcp.json path — the external MCP servers this
	// agent connects *to* (mcpclient/), the mirror image of ServerOptions.MCP's
	// server surface. Empty leaves the client pool off entirely (tests);
	// serveWeb sets mcpclient.Path(), which is also what the CLI resolves, so
	// both fronts edit and read one file.
	MCPClientStore string
	// SkillRuntime enables the isolated declaration-based prototype. Nil disables it.
	SkillRuntime *skillruntime.Service
}

// Manager is the session registry.
//
// agent.Agent is not safe for concurrent use — its message slice is appended by
// the run goroutine and SetClient writes a field unsynchronised — so each session
// owns one Agent, one tool registry (the path guard is per-registry) and at most
// one run. Concurrency across sessions is unrestricted; within a session it is
// one.
type Manager struct {
	cfg                      Config
	sentinelMarkers          sync.Map // immutable first-record navigation roles
	backgroundTaskMarkers    sync.Map // immutable first-record schedule execution roles
	backgroundTaskLegacy     sync.Map // confirmed historical roles; bindings are rechecked on read
	sentinelWakeSessions     sync.Map // immutable first user-input host wake marker
	sentinelParents          sync.Map // legacy native receipt projection, keyed by source file fingerprint
	browserDownloadPrefsPath string
	studio                   *studioStore
	workflows                *workflowStore
	workflowCallbackStop     func()
	// Bound by the authenticated Web host; model arguments never supply a
	// principal, credential, or arbitrary workflow owner.
	workflowAuthoringHost *Server
	agentWorkflowHost     *Server
	// Installed once by a configured Web Office host before requests start.
	// The factory binds the live editor tool to trusted conversation identity;
	// unconfigured and non-docs conversations have no Office tool.
	officeForSession    func(id, cwd string, owner *identity.Scope, hub *Hub) tools.OfficeLink
	officeBeforeDelete  func(id string) error
	officeRevokeSession func(id string)
	officeSessionActive func(id string) bool
	// Internal failure-injection seam for deletion. Production always uses the
	// tools package's scoped remover; no request or configuration can override it.
	spillScopeCleanup func(base, scope string) error
	// File publication protection is independent of whether an Agent selected
	// the docs skill. Omitting an editor tool must not make managed originals
	// writable through a different Agent's command tools.
	officeCommandProtection func(cwd string, owner *identity.Scope) (string, error)
	officeRegisterDocument  func(cwd string, owner *identity.Scope, absolute string) error
	// wfruns owns workflow run state: the in-memory active set, the per-run
	// hubs, and recovery of the wf-runs directory. Built in NewManager right
	// after the workflows store it reads definitions from.
	wfruns *wfRunManager

	// Derived from cfg.Skills once, because every session needs the same three
	// projections of it and rebuilding them per session is pure waste.
	skillRoots   []string
	skillSection string
	skillNames   []string
	// skillPathFlags are the -skill arguments children are spawned with: each
	// loaded bundle's directory (its SKILL.md path for a bare file). Derived
	// once like the other projections; a child re-loads the bundles itself.
	skillPathFlags []string
	// uiSection states what the answer surface can render (see
	// uiCapabilitiesSection). Built once like the skill projections: the UI's
	// capabilities are fixed for the life of the server.
	uiSection string
	// readRoots is skillRoots plus cfg.ReadRoots — everything the read-only four
	// may reach outside the session's own directory.
	readRoots []string

	// Manifest tools declared by the skill bundles (tools.json), loaded once
	// with the skills they travel with. Three projections for the same reason
	// as the skill ones: every session needs the same set, and a set that
	// changed between sessions would change what a resumed transcript's tool
	// calls mean.
	synthTools   []tools.Tool
	synthCatalog []tools.SynthInfo
	// synthReload is the post-reload hook; see OnSynthReload.
	synthReload func()
	// runtimeSkillChange is the packaged-runtime-Skill counterpart of
	// synthReload; see OnRuntimeSkillChange.
	runtimeSkillChange func()
	synthReadOnly      []string // wire names of read-only declared tools, for the gate
	// skillSnapshot is the observation view of the same skill set: trusted
	// identities plus their read targets, rebuilt with the other projections
	// so a session can never mix an old prompt with new attribution.
	skillSnapshot []skillSnapshotEntry

	// journal is the workspace-wide file pre-image store: one per workspace
	// root, shared by every session in that root so the workspace-changes view
	// sees all of them. Single-user mode has exactly one (the server root);
	// multi-user mode lazily creates one per owner root — see workspaceFor.
	journal *tools.DirJournal

	// shadow is the per-workspace checkpoint store for rewinding files. Nil
	// when git (or the dir) is unavailable: rewind then degrades to
	// conversation-only, which is what it was before checkpoints existed.
	// Per-owner instances in multi-user mode, same as the journal.
	shadow *ShadowRepo

	// mcpPool owns the connections to the external MCP servers mcp.json names
	// (mcpclient.Pool). Nil when MCPClientStore is empty — the feature is off,
	// and sessions project nothing. Sessions snapshot it at creation; the pool
	// outlives them and dies with Close.
	mcpPool *mcpclient.Pool

	// Per-account MCP credentials (users.json "mcp_tokens"): an account with
	// overrides gets sessions backed by its own pool, so the bearer token the
	// connection authenticates with is the account's own — the shared pool's
	// credential is never spent on someone else's run. mcpTokenFor enumerates
	// the live overrides (keyed by the owner's identity.Scope); SetMCPUserTokens
	// arms it once the auth store exists. Accounts without overrides, and
	// owner-less sessions, keep using mcpPool unchanged.
	mcpOwnerMu    sync.Mutex
	mcpBase       mcpclient.Config
	mcpTokenFor   func() map[identity.Scope]map[string]string
	mcpOwnerPools map[string]*mcpclient.Pool

	// mcpRecorder is the MCP call observability sink (mcpobserve), installed by
	// NewServer once the store behind it exists. Nil until then, and nil again
	// if the store is absent: observation is additive, never a reason a run or
	// an MCP request fails. Read per run/request rather than captured, so a
	// late install still arms sessions created earlier.
	mcpRecorder   atomic.Pointer[mcpRecorderHolder]
	skillRecorder atomic.Pointer[skillRecorderHolder]

	// Subagent settings, resolved once from the environment in NewManager so that a
	// bad value is a startup error and every session of this server agrees. Reading
	// them per session would let a variable changed under a long-lived server give
	// two sessions different delegation concurrency.
	subagentConcurrency int

	// maxTurnsOverride is the settings dialog's live turn cap (prefs.json
	// max_turns): 0 means cfg.MaxTurns — the -max-turns flag — decides. Set
	// by SetMaxTurnsOverride, which also pushes the value into every cached
	// session's agent, so an already-open conversation's next run obeys it.
	maxTurnsOverride atomic.Int64

	// memoryAuto is the settings dialog's live memory-distillation switch
	// (prefs.json memory_auto). Unlike the turn cap it needs no per-session
	// push: sessions read it through MemoryAutoEnabled at a run's end, the
	// moment a distillation would start, so a toggle applies to the next run
	// of an already-open conversation without a reload.
	memoryAuto atomic.Bool

	mu       sync.Mutex
	sessions map[string]*Session
	// Loads are reserved per conversation, so cold HTTP requests share one
	// construction while unrelated conversations remain independent. Eviction
	// keeps a reservation until the old instance has finished closing: a new
	// instance must not acquire an Office role the old one is about to revoke.
	sessionLoads map[string]*sessionLoad
	closed       bool
	closeDone    chan struct{}
	deleting     map[string]bool

	// sweepMu guards lastSweep, the most recent automatic spill-sweep outcome
	// (web/spillsweep.go). Its own lock rather than mu: the session lock is
	// held across run orchestration and a cleaner must not queue behind it.
	sweepMu   sync.Mutex
	lastSweep *SpillSweep

	// schedRunning tracks schedule-job runs started through the API's run-now
	// (web/scheduleapi.go), so GET jobs can mark a job in flight and a second
	// click answers 409. Its own lock, same reason as sweepMu.
	schedMu            sync.Mutex
	schedRunning       map[string]time.Time
	schedSessions      map[string]string
	scheduleOwnerValid func(*identity.Scope) bool

	// visionClients and visionCache back the vision helper (web/vision.go):
	// one client per configured vision_model, and answers memoised by
	// cwd+ref+question so a re-asked question is free. visionMu guards both.
	visionMu      sync.Mutex
	visionClients map[string]llm.Client
	visionCache   map[string]string

	// workspaces caches the per-root journal+checkpoint pairs and memories of
	// multi-user mode. The server-root pair lives in journal/shadow above and
	// cfg.Memory; these are the lazily created per-owner copies, keyed by the
	// owner's workspace root. wsMu guards the map only.
	wsMu       sync.Mutex
	workspaces map[string]*ownerWorkspace

	stop           chan struct{}
	wg             sync.WaitGroup
	scheduleCtx    context.Context
	scheduleCancel context.CancelFunc
	sentinels      *sentinelRuntime
	taskRuns       *taskRunRuntime
	shellScheduler *tools.ShellScheduler
}

type sessionLoad struct {
	done     chan struct{}
	session  *Session
	err      error
	retiring bool
}

// ownerWorkspace is one user's slice of the shared server state: the workspace
// root their sessions run in, and the journal, checkpoint repo and memory that
// are scoped to it instead of to the server root.
type ownerWorkspace struct {
	root    string
	journal *tools.DirJournal
	shadow  *ShadowRepo
	memory  *memory.Store
}

func NewManager(cfg Config) (*Manager, error) {
	var browserErr error
	cfg.browser, browserErr = parseNativeBrowserConfig(cfg.BrowserURL, cfg.BrowserToken)
	if browserErr != nil {
		return nil, browserErr
	}
	if err := cfg.bindBrowserDesktopOwner(); err != nil {
		return nil, err
	}
	if cfg.Cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		cfg.Cwd = wd
	}
	abs, err := filepath.Abs(cfg.Cwd)
	if err != nil {
		return nil, err
	}
	cfg.Cwd = abs
	if cfg.SkillRuntime != nil && !privateRuntimeSkillStore(cfg.SkillRuntime.Root, cfg.Cwd) {
		return nil, errors.New("Skill store must be outside the Agent workspace")
	}
	if cfg.SessionDir == "" {
		cfg.SessionDir = session.DefaultDir()
	}
	if cfg.GateTimeout <= 0 {
		cfg.GateTimeout = DefaultGateTimeout
	}
	if cfg.RunTimeout <= 0 {
		cfg.RunTimeout = DefaultRunTimeout
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	// Fail at startup rather than on the first prompt: an unusable model or a
	// missing key should not look like a broken UI. The same reasoning covers a
	// timeout configuration that cannot hold — see CheckApprovalBudget for why that
	// one is otherwise invisible.
	_, err = tools.IsolationFromEnv()
	if err != nil {
		return nil, err
	}
	concurrency, err := tools.ConcurrencyFromEnv()
	if err != nil {
		return nil, err
	}
	if concurrency <= 0 {
		concurrency = tools.DefaultSubagentConcurrency
	}
	// Checked against the configured concurrency, not the constant: raising the
	// number of children raises the approval time they can hold between them, and
	// that is precisely the inequality this function exists to state.
	if err := CheckApprovalBudget(cfg.RunTimeout, cfg.GateTimeout, concurrency); err != nil {
		return nil, err
	}
	if _, err := config.Resolve(cfg.Model); err != nil {
		return nil, err
	}
	studio, err := openStudioStore(cfg.SessionDir)
	if err != nil {
		return nil, fmt.Errorf("agent studio: %w", err)
	}
	workflows, err := openWorkflowStore(cfg.SessionDir)
	if err != nil {
		return nil, fmt.Errorf("workflows: %w", err)
	}

	m := &Manager{
		studio:              studio,
		workflows:           workflows,
		cfg:                 cfg,
		subagentConcurrency: concurrency,
		journal:             tools.NewDirJournal(filepath.Join(cfg.SessionDir, "journal", journalDirKey(cfg.Cwd)), cfg.Cwd),
		sessions:            make(map[string]*Session),
		schedRunning:        make(map[string]time.Time),
		workspaces:          make(map[string]*ownerWorkspace),
		shellScheduler:      tools.NewShellScheduler(tools.ShellSchedulerConfig{}),
		stop:                make(chan struct{}),
	}
	m.SetMemoryAuto(cfg.MemoryAuto)
	// Skill/MCP references on a definition validate against this manager's
	// live catalogs: a selection can only name what exists right now.
	studio.refs = m.validateStudioRefs
	studio.bindSkillTools = m.normalizeStudioSkillTools
	workflows.refs = m.validateWorkflowRefs
	m.wfruns = newWFRunManager(filepath.Join(cfg.SessionDir, "wf-runs"), m)
	m.taskRuns = newTaskRunRuntime(m)
	if _, err := m.taskRuns.database(true); err != nil {
		return nil, fmt.Errorf("workflow control store: %w", err)
	}
	m.wfruns.recover()
	if err := m.importWorkflowHistory(m.taskRuns.store); err != nil {
		_ = m.taskRuns.store.Close()
		_ = m.taskRuns.lease.Close()
		return nil, fmt.Errorf("workflow history: %w", err)
	}
	if err := m.reconcileWorkflowSegments(m.taskRuns.store); err != nil {
		_ = m.taskRuns.store.Close()
		_ = m.taskRuns.lease.Close()
		return nil, fmt.Errorf("workflow recovery: %w", err)
	}
	m.rebuildSkillViews()
	// Complete reset before starting any goroutines. Constructor failures must
	// also release the newly acquired persistent dispatcher lease.
	if err := m.finishStudioReset(); err != nil {
		_ = m.taskRuns.store.Close()
		_ = m.taskRuns.lease.Close()
		return nil, fmt.Errorf("studio format reset: %w", err)
	}
	// Runs caught idle by the crash continue once the skill views exist; the
	// human-wait timeout sweep runs beside them.
	m.wfruns.resumePending()
	m.wfruns.wg.Add(1)
	go m.wfruns.runSweep()

	// The shadow repo sits next to the journal. Failure (no git, an
	// unwritable dir) turns checkpointing off, not the server: rewind still
	// works, just without file restoration.
	if shadow, err := NewShadowRepo(filepath.Join(cfg.SessionDir, "checkpoints", journalDirKey(cfg.Cwd)), cfg.Cwd); err != nil {
		fmt.Fprintf(os.Stderr, "checkpointing unavailable: %v\n", err)
	} else {
		m.shadow = shadow
	}
	// The MCP client pool connects in the background from construction on:
	// connects never block NewManager (Reconcile is asynchronous by design),
	// and a session created in the meantime simply projects the servers that
	// are already up. A file that exists but will not parse is a log line,
	// not a startup failure — prefs' LoadError call: a broken config is
	// exactly when the user needs the editor (/api/mcp) that fixes it.
	if cfg.MCPClientStore != "" {
		m.mcpPool = mcpclient.NewPool(nil)
		mcpCfg, err := mcpclient.Load(cfg.MCPClientStore)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mcp client config: %v\n", err)
		}
		m.mcpPool.Reconcile(context.Background(), mcpCfg)
		m.mcpBase = mcpCfg
		m.mcpOwnerPools = map[string]*mcpclient.Pool{}
	}
	m.sentinels = newSentinelRuntime(m)
	m.wg.Add(1)
	go m.taskRuns.loop()
	m.wg.Add(1)
	go m.sentinels.loop()
	m.scheduleCtx, m.scheduleCancel = context.WithCancel(context.Background())
	m.wg.Add(1)
	go m.reap()
	if cfg.SpillSweepInterval > 0 {
		m.wg.Add(1)
		go m.sweepSpill()
	}
	if cfg.ScheduleTickInterval > 0 {
		m.wg.Add(1)
		go m.runSchedule()
	}
	return m, nil
}

// rebuildSkillViews recomputes every projection of the loaded skill set:
// prompt section, readable roots, manifest tools, child -skill flags. Called
// once at construction and again by SetSkills after an upload, so the two
// paths cannot drift. Callers hold mu (construction is single-threaded).
func (m *Manager) rebuildSkillViews() {
	list := agentConversationSkills(m.cfg.Skills)
	m.skillRoots = skills.Roots(list)
	m.skillSection = skills.PromptSection(list)
	m.skillNames = skills.Names(list)
	m.uiSection = uiCapabilitiesSection(list)

	// The -skill flags a subagent child is spawned with. Dir first (a bundle
	// re-scans whole), Path as the fallback for a bare SKILL.md file.
	m.skillPathFlags = nil
	for _, s := range list {
		if p := s.Dir; p != "" {
			m.skillPathFlags = append(m.skillPathFlags, p)
		} else if p := s.Path; p != "" {
			m.skillPathFlags = append(m.skillPathFlags, p)
		}
	}

	// The union of skill roots and the operator's read roots, computed once
	// here rather than per session: the components cannot differ.
	m.readRoots = append(append([]string{}, m.skillRoots...), m.cfg.ReadRoots...)

	// The skill roots also join POGO_READ_ROOTS itself, not just this process's
	// in-memory slice: the environment is the inheritance channel a subagent
	// child rebuilds its own roots from, and a child's skills tell it to read
	// files under the skill directory. Union, so anything the operator baked
	// into the variable survives — and so a reload after an upload keeps the
	// operator's entries while adding the new roots.
	if len(m.skillRoots) > 0 {
		seen := make(map[string]bool)
		var merged []string
		for _, r := range append(append([]string{}, m.skillRoots...), filepath.SplitList(os.Getenv(tools.EnvReadRoots))...) {
			r = strings.TrimSpace(r)
			if r == "" || seen[r] {
				continue
			}
			seen[r] = true
			merged = append(merged, r)
		}
		os.Setenv(tools.EnvReadRoots, strings.Join(merged, string(os.PathListSeparator)))
	}

	// Manifest tools come from the same bundles as the skills and inherit their
	// trust boundary exactly. Failures skip the entry and say so, the skills
	// rule — a bundle is third-party-shaped input and must not be able to stop
	// the server.
	// The observation snapshot is built in the same pass: the identity a
	// manifest tool carries and the identity a SKILL.md read resolves to are
	// one computation, so they cannot disagree.
	m.skillSnapshot = nil
	sources := make([]tools.SynthSource, 0, len(list))
	for _, s := range list {
		id := skillIdentityOf(s)
		if id.ID != "" {
			m.skillSnapshot = append(m.skillSnapshot, skillSnapshotEntry{
				identity: id, path: canonicalSkillPath(s.Path), hidden: s.DisableModelInvocation,
			})
		}
		if s.Dir != "" {
			sources = append(sources, tools.SynthSource{Skill: s.Name, Dir: s.Dir, Identity: id})
		}
	}
	var synthDiags []tools.SynthDiag
	m.synthTools, m.synthCatalog, synthDiags = tools.LoadSynthTools(sources)
	for _, d := range synthDiags {
		if d.Path == "" {
			fmt.Fprintf(os.Stderr, "manifest tools: %s\n", d.Message)
			continue
		}
		fmt.Fprintf(os.Stderr, "manifest tools: %s (%s)\n", d.Message, d.Path)
	}
	m.synthReadOnly = nil
	for _, info := range m.synthCatalog {
		if info.ReadOnly {
			m.synthReadOnly = append(m.synthReadOnly, info.WireName)
		}
	}
}

// skillViews is a consistent snapshot of the skill-derived projections. The
// fields were immutable for the Manager's whole life until uploads could
// replace them at runtime; sessions still capture their view at creation, so
// a swap mid-Session-build cannot tear it.
type skillViews struct {
	section      string
	uiSection    string
	pathFlags    []string
	readRoots    []string
	synthTools   []tools.Tool
	synthCatalog []tools.SynthInfo
	synthRO      []string
	snapshot     []skillSnapshotEntry
}

func (m *Manager) skillViews() skillViews {
	m.mu.Lock()
	defer m.mu.Unlock()
	return skillViews{
		section:      m.skillSection,
		uiSection:    m.uiSection,
		pathFlags:    append([]string(nil), m.skillPathFlags...),
		readRoots:    append([]string(nil), m.readRoots...),
		synthTools:   append([]tools.Tool(nil), m.synthTools...),
		synthCatalog: append([]tools.SynthInfo(nil), m.synthCatalog...),
		synthRO:      append([]string(nil), m.synthReadOnly...),
		snapshot:     append([]skillSnapshotEntry(nil), m.skillSnapshot...),
	}
}

// SetSkills replaces the loaded skill set (after an upload installed a new
// bundle) and recomputes every projection derived from it. Sessions already
// in memory keep the prompt and tools they were built with — a system prompt
// that changed mid-conversation would invalidate the cached prefix on every
// turn — while sessions created or loaded afterwards see the new set.
func (m *Manager) SetSkills(list []skills.Skill) {
	m.mu.Lock()
	m.cfg.Skills = list
	m.rebuildSkillViews()
	// Captured under the lock, invoked after it: the MCP layer's resync calls
	// SynthTools/SynthCatalog right back in here, and calling those with m.mu
	// held would deadlock on the first skill upload.
	hook := m.synthReload
	m.mu.Unlock()
	if hook != nil {
		hook()
	}
}

// journalDirKey keeps one journal per workspace under the session dir. The
// hash is of the path only because "same cwd" is the identity that matters.
func journalDirKey(cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return hex.EncodeToString(sum[:])[:16]
}

func (m *Manager) Cwd() string { return m.cfg.Cwd }

// Journal is the workspace file pre-image store, for the /api/workspace
// handlers. Every session's registry writes through it.
func (m *Manager) Journal() *tools.DirJournal { return m.journal }

// Skills are the skills in effect for every session on this server. Guarded:
// an upload replaces the set at runtime, so the immutable-at-boot assumption
// no longer holds.
func (m *Manager) Skills() []skills.Skill {
	m.mu.Lock()
	defer m.mu.Unlock()
	return agentConversationSkills(m.cfg.Skills)
}

// loadedSkills includes retired compatibility entries for validating saved
// definitions. Runtime callers use Skills, whose projection excludes them.
func (m *Manager) loadedSkills() []skills.Skill {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]skills.Skill(nil), m.cfg.Skills...)
}

// SkillOptions are the options the boot scan used, for the reload an upload
// triggers: the same locations and flags, or the installed bundle would land
// where the next scan disagrees with.
func (m *Manager) SkillOptions() skills.Options {
	return m.cfg.SkillOptions
}

// SkillNames are the loaded skills' names, recorded into each new session's
// metadata. Snapshotted under the lock like the other projections.
func (m *Manager) SkillNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.skillNames...)
}

// SynthCatalog is the manifest tool catalog: every declared tool, registered
// or not, with its MCP-shaped fields. What /api/tools serves — and the shape
// the MCP layer's tools/list answers with, which is the reason the catalog
// keeps the full set rather than only the registered half. Guarded for the
// same reason Skills is.
func (m *Manager) SynthCatalog() []tools.SynthInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]tools.SynthInfo(nil), m.synthCatalog...)
	if m.officeForSession != nil {
		for _, name := range []string{"office", "docs"} {
			if slices.Contains(m.skillNames, name) {
				info := tools.OfficeCatalog()
				info.Skill = name
				out = append(out, info)
				break
			}
		}
	}
	return out
}

// SynthTools returns the synthesized tool instances behind the catalog's
// registered half — what the MCP layer forwards direct calls to, so a manifest
// tool runs the same Execute the model's own calls run. The catalog stays the
// metadata source (names, schemas, annotations); this is the executable pair,
// kept apart because the instance list has no reason to grow a JSON shape.
// Direct MCP calls have no conversation registry to bind these tools: their
// relative paths belong to the Manager workspace. Return fresh bindings so a
// session or another export cannot change that execution directory or spill path.
func (m *Manager) SynthTools() []tools.Tool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]tools.Tool, 0, len(m.synthTools))
	spillDir := tools.DefaultSpillDir()
	for _, t := range m.synthTools {
		if st, ok := t.(*tools.SynthTool); ok {
			t = st.WithWorkspace(m.cfg.Cwd, spillDir)
		}
		out = append(out, t)
	}
	return out
}

// MCPCatalogs is the pool's per-server catalog projection for the skills
// panel: the requesting owner's pool (the shared one when the account carries
// no overrides), narrowed to the conversation's server selection. A nil pool
// answers nil — the feature is off, and the panel shows no MCP section.
func (m *Manager) MCPCatalogs(owner *identity.Scope, only map[string]bool) []mcpclient.ServerCatalog {
	pool := m.poolFor(owner)
	if pool == nil {
		return nil
	}
	return pool.Catalogs(only)
}

// OnSynthReload registers a callback fired after a skill reload has rebuilt
// the manifest tools (SetSkills — today only the upload endpoint drives it).
// The MCP layer uses it to re-register its passthrough tools so an uploaded
// bundle appears on tools/list without a restart. One slot, last setter wins:
// there is one MCP layer per server, and a test that builds a second server
// around the same manager wants the live one to own it.
func (m *Manager) OnSynthReload(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.synthReload = fn
}

// OnRuntimeSkillChange registers a callback fired after the packaged runtime
// Skill catalog changes — an install, a disable (revoke) or a delete. The
// MCP layer uses it to re-register the global endpoint's runtime tools so a
// package change reaches tools/list without a restart. One slot, last setter
// wins, for the same reason as OnSynthReload.
func (m *Manager) OnRuntimeSkillChange(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runtimeSkillChange = fn
}

// fireRuntimeSkillChange invokes the hook with the manager lock released —
// the MCP layer's resync calls right back into the runtime service, and
// holding m.mu across it would deadlock the same way SetSkills' hook would.
func (m *Manager) fireRuntimeSkillChange() {
	m.mu.Lock()
	hook := m.runtimeSkillChange
	m.mu.Unlock()
	if hook != nil {
		hook()
	}
}

// mcpRecorderHolder boxes the recorder so a nil store (observation off) and a
// set one are both representable in the atomic pointer.
type mcpRecorderHolder struct{ recorder mcpobserve.Recorder }

// SetMCPRecorder installs the sink every subsequent MCP call observation goes
// to. Passing nil turns observation back off. Safe to call while runs and MCP
// requests are in flight: readers load the pointer per use.
func (m *Manager) SetMCPRecorder(r mcpobserve.Recorder) {
	if r == nil {
		m.mcpRecorder.Store(nil)
		return
	}
	m.mcpRecorder.Store(&mcpRecorderHolder{recorder: r})
}

// MCPRecorder is the currently installed sink, or nil when observation is off.
// Callers must treat a nil answer as "record nothing", not as an error.
func (m *Manager) MCPRecorder() mcpobserve.Recorder {
	if h := m.mcpRecorder.Load(); h != nil {
		return h.recorder
	}
	return nil
}

// mcpProjection is the session-facing view of the client pool: the tools a
// session registers, and the wire names its policy pre-approves. Creation
// seeds it immediately; each new run refreshes it from its owner's pool.
func (m *Manager) mcpProjection(synth []tools.Tool, owner *identity.Scope) (proj []tools.Tool, trusted []string) {
	return m.mcpProjectionFor(synth, owner, nil)
}

// mcpProjectionFor is mcpProjection narrowed to a selected server set: a
// Studio conversation with an explicit mcp_servers list projects exactly
// those servers' tools and trust, at creation and at every run refresh. A
// nil set is the whole pool (ordinary sessions and legacy definitions).
func (m *Manager) mcpProjectionFor(synth []tools.Tool, owner *identity.Scope, only map[string]bool) (proj []tools.Tool, trusted []string) {
	pool := m.poolFor(owner)
	if pool == nil {
		return nil, nil
	}
	all, trustedAll, _ := pool.SnapshotServers(only)
	return dedupeMCP(all, trustedAll, synth)
}

// SetMCPUserTokens arms per-account MCP credentials. NewServer calls it once
// the users.json store exists; the pools reconcile immediately so an account's
// first session finds its tools connected, mirroring the shared pool's startup
// behaviour. A nil function disarms the feature (nothing to override).
func (m *Manager) SetMCPUserTokens(all func() map[identity.Scope]map[string]string) {
	if all == nil {
		return
	}
	m.mcpOwnerMu.Lock()
	defer m.mcpOwnerMu.Unlock()
	m.mcpTokenFor = all
	m.reconcileOwnerPoolsLocked(context.Background())
}

// ReconcileMCP applies a new base mcp.json to the shared pool and to every
// owner pool, so a saved edit reaches accounts with their own credentials in
// the same breath as everyone else.
func (m *Manager) ReconcileMCP(ctx context.Context, cfg mcpclient.Config) {
	if m.mcpPool != nil {
		m.mcpPool.Reconcile(ctx, cfg)
	}
	m.mcpOwnerMu.Lock()
	defer m.mcpOwnerMu.Unlock()
	m.mcpBase = cfg
	if m.mcpOwnerPools == nil {
		m.mcpOwnerPools = map[string]*mcpclient.Pool{}
	}
	m.reconcileOwnerPoolsLocked(ctx)
}

// poolFor picks the pool a session owned by owner projects: its own when the
// account carries MCP token overrides, the shared one otherwise. Creation is
// lazy but idempotent - SetMCPUserTokens has usually reconciled it already,
// and an account added after boot still gets its pool on first session.
func (m *Manager) poolFor(owner *identity.Scope) *mcpclient.Pool {
	if m.mcpPool == nil {
		return nil
	}
	if owner == nil {
		return m.mcpPool
	}
	m.mcpOwnerMu.Lock()
	defer m.mcpOwnerMu.Unlock()
	if m.mcpTokenFor == nil {
		return m.mcpPool
	}
	tokens, ok := m.mcpTokenFor()[*owner]
	if !ok || len(tokens) == 0 {
		return m.mcpPool
	}
	if pool, ok := m.mcpOwnerPools[owner.SubjectID]; ok {
		return pool
	}
	pool := mcpclient.NewPool(nil)
	if m.mcpOwnerPools == nil {
		m.mcpOwnerPools = map[string]*mcpclient.Pool{}
	}
	m.mcpOwnerPools[owner.SubjectID] = pool
	pool.Reconcile(context.Background(), ownerMCPConfig(m.mcpBase, tokens))
	return pool
}

// reconcileOwnerPoolsLocked aligns the owner-pool set with the live override
// map: new accounts get pools, changed tokens reconnect (the fingerprint
// covers BearerToken), accounts that lost their overrides lose theirs.
// Caller holds mcpOwnerMu.
func (m *Manager) reconcileOwnerPoolsLocked(ctx context.Context) {
	if m.mcpTokenFor == nil {
		return
	}
	live := make(map[string]bool)
	for scope, tokens := range m.mcpTokenFor() {
		if len(tokens) == 0 {
			continue
		}
		live[scope.SubjectID] = true
		pool, ok := m.mcpOwnerPools[scope.SubjectID]
		if !ok {
			pool = mcpclient.NewPool(nil)
			m.mcpOwnerPools[scope.SubjectID] = pool
		}
		pool.Reconcile(ctx, ownerMCPConfig(m.mcpBase, tokens))
	}
	for id, pool := range m.mcpOwnerPools {
		if !live[id] {
			pool.Close()
			delete(m.mcpOwnerPools, id)
		}
	}
}

// ownerMCPConfig clones the base mcp.json for one account: servers the account
// names in mcp_tokens get its bearer token (bearer_token_env cleared - two
// spellings of one credential, pick one, per ServerConfig's own rule); every
// other entry is shared verbatim, pointer included, so an untouched server
// stays one config object in memory.
func ownerMCPConfig(base mcpclient.Config, tokens map[string]string) mcpclient.Config {
	out := mcpclient.Config{
		Servers: make(map[string]*mcpclient.ServerConfig, len(base.Servers)),
		Server:  base.Server,
	}
	for name, cfg := range base.Servers {
		token, overridden := tokens[name]
		if !overridden || cfg == nil {
			out.Servers[name] = cfg
			continue
		}
		clone := *cfg
		clone.BearerToken = token
		clone.BearerTokenEnv = ""
		clone.Args = append([]string(nil), cfg.Args...)
		clone.Env = maps.Clone(cfg.Env)
		clone.Headers = maps.Clone(cfg.Headers)
		clone.IncludeTools = append([]string(nil), cfg.IncludeTools...)
		clone.ExcludeTools = append([]string(nil), cfg.ExcludeTools...)
		out.Servers[name] = &clone
	}
	return out
}

// ownerMCPStatus reports the per-account pools for the admin status view.
// Names are prefixed with the owner's short subject id: same server, different
// credential, and the prefix is what makes that visible instead of a mystery
// duplicate.
func (m *Manager) ownerMCPStatus() []mcpclient.Status {
	m.mcpOwnerMu.Lock()
	defer m.mcpOwnerMu.Unlock()
	out := make([]mcpclient.Status, 0, len(m.mcpOwnerPools))
	for subject, pool := range m.mcpOwnerPools {
		_, _, statuses := pool.Snapshot()
		prefix := "owner:" + subjectShort(subject) + ":"
		for i := range statuses {
			statuses[i].Name = prefix + statuses[i].Name
			out = append(out, statuses[i])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// subjectShort renders a subject id for status lines - enough digits to tell
// accounts apart, never the whole hash.
func subjectShort(subject string) string {
	if len(subject) > 8 {
		return subject[:8]
	}
	return subject
}

// dedupeMCP resolves wire-name collisions with the session's manifest tools
// in the manifest's favour. Registering both is not an option: the
// registry's later write wins the name while its order keeps both entries —
// a doubling the model sees — and a trusted MCP wire name would then
// pre-approve the *manifest* tool standing under it. Skipping the MCP entry
// is the only answer that cannot lie. The built-ins need no check: every
// MCP wire name carries the mcp_ prefix (tools.WireNameFor) and none of
// theirs does.
func dedupeMCP(all []tools.Tool, trustedAll []string, synth []tools.Tool) (proj []tools.Tool, trusted []string) {
	if len(all) == 0 {
		return nil, nil
	}
	taken := make(map[string]bool, len(synth))
	for _, t := range synth {
		taken[t.Name()] = true
	}
	var dropped map[string]bool
	for _, t := range all {
		if taken[t.Name()] {
			if dropped == nil {
				dropped = map[string]bool{}
			}
			dropped[t.Name()] = true
			fmt.Fprintf(os.Stderr, "mcp client: tool %q collides with a manifest tool's wire name; skipped\n", t.Name())
			continue
		}
		proj = append(proj, t)
	}
	for _, name := range trustedAll {
		if !dropped[name] {
			trusted = append(trusted, name)
		}
	}
	return proj, trusted
}

// Close cancels every run and releases every session.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		done := m.closeDone
		m.mu.Unlock()
		if done != nil {
			<-done
		}
		return
	}
	m.closed = true
	m.closeDone = make(chan struct{})
	defer close(m.closeDone)
	list := make([]*Session, 0, len(m.sessions))
	for id, s := range m.sessions {
		list = append(list, s)
		delete(m.sessions, id)
	}
	loads := make(map[string]*sessionLoad, len(m.sessionLoads))
	for id, load := range m.sessionLoads {
		loads[id] = load
	}
	stopCallbacks := m.workflowCallbackStop
	m.mu.Unlock()

	close(m.stop)
	// Drain callback HTTP and leases while their shared persistent database
	// is still open. Any remaining events stay queued for the next process.
	if stopCallbacks != nil {
		stopCallbacks()
	}
	if m.sentinels != nil {
		m.sentinels.cancel()
	}
	if m.taskRuns != nil {
		m.taskRuns.stop()
	}
	if m.scheduleCancel != nil {
		m.scheduleCancel()
	}
	for _, s := range list {
		s.Close()
	}
	// A constructor that observes closed disposes its unpublished resources.
	// Drain it before shared pools disappear underneath that cleanup.
	for id, load := range loads {
		<-load.done
		// No constructor can publish once closed. Dispose any Office role
		// allocated by that discarded constructor after it has drained.
		if m.officeRevokeSession != nil {
			m.officeRevokeSession(id)
		}
	}
	if m.wfruns != nil {
		m.wfruns.close()
	}
	m.wg.Wait()
	// The pool goes last: the sessions above have drained their runs, so no
	// MCP tool call can still be in flight to have its transport vanish
	// underneath it.
	if m.mcpPool != nil {
		m.mcpPool.Close()
	}
	// Owner pools go last for the same reason: their sessions are drained
	// with everyone else's above.
	m.mcpOwnerMu.Lock()
	ownerPools := make([]*mcpclient.Pool, 0, len(m.mcpOwnerPools))
	for id, pool := range m.mcpOwnerPools {
		ownerPools = append(ownerPools, pool)
		delete(m.mcpOwnerPools, id)
	}
	m.mcpOwnerMu.Unlock()
	for _, pool := range ownerPools {
		pool.Close()
	}
}

// List summarises the sessions on disk.
func (m *Manager) List() ([]session.Info, error) {
	infos, err := session.List(m.cfg.SessionDir)
	if err != nil {
		return nil, err
	}
	return m.ordinarySessionInfos(infos)
}

// Search finds sessions whose recorded conversation matches q, best match
// first. Served by the session index; without one it falls back inside
// session.Search to a scan, so the endpoint keeps working everywhere.
func (m *Manager) Search(q string, limit int) ([]session.Hit, error) {
	infos, err := session.List(m.cfg.SessionDir)
	if err != nil {
		return nil, err
	}
	workers, err := m.backgroundWorkerSet(infos)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	hits, err := session.Search(m.cfg.SessionDir, q, limit+len(workers))
	if err != nil {
		return nil, err
	}
	out := make([]session.Hit, 0, min(limit, len(hits)))
	for _, hit := range hits {
		if workers[hit.ID] == "" {
			out = append(out, hit)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

// BusyIDs reports which sessions have a run in flight right now. Only a loaded
// session can be running — eviction requires idleness (see evictIdle) — so the
// in-memory map is the whole answer, and the sidebar's working indicators
// never have to touch disk.
func (m *Manager) BusyIDs() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]bool, len(m.sessions))
	for id, s := range m.sessions {
		if s.Active() {
			out[id] = true
		}
	}
	return out
}

// LiveTitles overlays the sidebar's derived title for the window between a
// first prompt hitting the fold and the transcript append that would let the
// disk read derive it — with an image attachment that window is the whole
// describe step. Only a loaded session can be in that window, so the in-memory
// map is the whole answer. A renamed session already has its title on disk, so
// the overlay only ever fills a gap, never overrides.
func (m *Manager) LiveTitles() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.sessions))
	for id, s := range m.sessions {
		if text := s.hub.FirstUserTitle(); text != "" {
			out[id] = session.Title(text)
		}
	}
	return out
}

// UpdateInfo applies sidebar edits (pin, rename) to a session. They are
// appended as a meta record, not patched over the creation record: the
// transcript stays append-only, and the next List merges the latest values.
// A rename also retitles the catalog row, so the ownership registry and the
// transcript never disagree about what a session is called.
func (m *Manager) UpdateInfo(id string, title *string, pinned *bool) error {
	sess, err := m.Get(id)
	if err != nil {
		return err
	}
	if err := sess.SetInfo(title, pinned); err != nil {
		return err
	}
	if title != nil && m.cfg.Catalog != nil {
		if rec, rerr := m.cfg.Catalog.OwnerOf(context.Background(), id); rerr == nil {
			_ = m.cfg.Catalog.Rename(context.Background(), rec.Scope, id, *title)
		}
	}
	return nil
}

// Create starts a new session in the classic single-user arrangement: the
// workspace is resolved against the server root and no ownership row is
// written.
func (m *Manager) Create(model, workspace string) (*Session, error) {
	return m.CreateAs(model, workspace, nil)
}

// CreateAs starts a new session. workspace is a directory relative to the
// session's base root — the server root for the single-user server, the
// owner's own root in multi-user mode ("" or "." picks the base itself); it
// becomes the session's working directory, which the path guard then confines
// the file tools to. A non-nil owner also writes the catalog row that makes
// the session visible to that owner alone, and uses the collision-resistant
// session id the shared directory requires.
func (m *Manager) CreateAs(model, workspace string, owner *identity.Scope) (*Session, error) {
	if owner != nil && !owner.Valid() {
		return nil, errors.New("session owner scope is invalid")
	}
	if model == "" {
		model = m.cfg.Model
	}
	cfg, err := config.Resolve(model)
	if err != nil {
		return nil, err
	}
	base := m.cfg.Cwd
	createStore := session.Create
	if owner != nil {
		base = m.UserRoot(*owner)
		createStore = session.CreateMultiOwner
	}
	cwd, err := m.resolveWorkspaceUnder(base, workspace)
	if err != nil {
		return nil, err
	}
	store, err := createStore(m.cfg.SessionDir, cwd, cfg.Model, m.SkillNames()...)
	if err != nil {
		return nil, err
	}
	s := m.newSession(store, cfg, nil, cwd, llm.Usage{}, llm.Usage{}, owner)

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		s.closeUnpublished()
		return nil, errors.New("server is shutting down")
	}
	m.sessions[s.ID] = s
	m.mu.Unlock()

	// The catalog row goes in after the session object exists but before the
	// caller sees it: a crash before this point leaves an unowned file the
	// sidebar never lists, which is the safe direction to fail in. State
	// creating→active so the sidebar only ever lists usable sessions.
	if m.cfg.Catalog != nil && owner != nil {
		ctx := context.Background()
		if err := m.cfg.Catalog.Create(ctx, catalog.Session{ID: s.ID, Scope: *owner, State: "creating"}); err != nil {
			m.mu.Lock()
			delete(m.sessions, s.ID)
			m.mu.Unlock()
			s.Close()
			return nil, err
		}
		if err := m.cfg.Catalog.Activate(ctx, *owner, s.ID); err != nil {
			m.mu.Lock()
			delete(m.sessions, s.ID)
			m.mu.Unlock()
			s.Close()
			return nil, err
		}
	}
	return s, nil
}

// Isolation reports whether this server runs with per-user session ownership.
func (m *Manager) Isolation() bool { return m.cfg.Catalog != nil }

// claimImported records ownership for a transcript an importer just wrote
// (WriteImported names the file itself). Returns false when the catalog row
// could not land, in which case the file stays unowned — invisible in
// multi-user mode, the safe direction — and the caller reports it.
func (m *Manager) claimImported(scope identity.Scope, id, title string) bool {
	if m.cfg.Catalog == nil {
		return true
	}
	ctx := context.Background()
	if err := m.cfg.Catalog.Create(ctx, catalog.Session{ID: id, Scope: scope, Title: title, State: "creating"}); err != nil {
		return false
	}
	return m.cfg.Catalog.Activate(ctx, scope, id) == nil
}

// UserRoot is the directory an owner's sessions, files and memory live under.
// It is created on first use; failure to create it is the caller's error to
// report, since a session cannot exist without its workspace. The key is the
// subject id alone: it already hashes app, issuer and tenant in, so two
// owners never share a root, and truncating the display form keeps paths
// readable without weakening the key (the full id stays in the catalog).
func (m *Manager) UserRoot(scope identity.Scope) string {
	key := scope.SubjectID
	if len(key) > 16 {
		key = key[:16]
	}
	root := filepath.Join(m.cfg.Cwd, "users", key)
	if err := os.MkdirAll(root, 0o755); err != nil {
		// Unwritable: report through the path itself. CreateAs will fail on
		// the workspace resolution that follows, with the real error.
		return root
	}
	return root
}

// UserRootRel is the same root as a slash path relative to the server cwd —
// the form sessions' workspace fields and the files panel carry on the wire.
// The bool is false on the empty-key edge, so callers can skip the strip.
func (m *Manager) UserRootRel(scope identity.Scope) (string, bool) {
	key := scope.SubjectID
	if len(key) > 16 {
		key = key[:16]
	}
	if key == "" {
		return "", false
	}
	return "users/" + key, true
}

// OwnedIDs lists the session ids a scope owns, straight from the authoritative
// catalog. The sidebar filters the directory listing against this set; it is
// the ownership answer, not a performance cache.
func (m *Manager) OwnedIDs(scope identity.Scope) map[string]bool {
	out := map[string]bool{}
	if m.cfg.Catalog == nil {
		return nil
	}
	recs, err := m.cfg.Catalog.List(context.Background(), scope, "", 200)
	if err != nil {
		return out
	}
	for _, rec := range recs {
		out[rec.ID] = true
	}
	return out
}

// workspaceFor returns the per-root workspace pair, creating it on first use.
// The server-root pair is the always-present zero value of this lookup and is
// held in the Manager's own fields; per-owner roots get their own journal,
// checkpoint repo and memory, so two users' pre-images, checkpoints and notes
// never share a namespace even though they share a process.
func (m *Manager) workspaceFor(root string) *ownerWorkspace {
	if root == m.cfg.Cwd {
		return &ownerWorkspace{
			root: root, journal: m.journal, shadow: m.shadow,
			memory: m.cfg.Memory,
		}
	}
	m.wsMu.Lock()
	defer m.wsMu.Unlock()
	if ws, ok := m.workspaces[root]; ok {
		return ws
	}
	ws := &ownerWorkspace{root: root}
	ws.journal = tools.NewDirJournal(filepath.Join(m.cfg.SessionDir, "journal", journalDirKey(root)), root)
	if shadow, err := NewShadowRepo(filepath.Join(m.cfg.SessionDir, "checkpoints", journalDirKey(root)), root); err == nil {
		ws.shadow = shadow
	} else {
		fmt.Fprintf(os.Stderr, "checkpointing unavailable under %s: %v\n", root, err)
	}
	// Per-owner memory is project-shaped memory rooted at the owner's
	// workspace: the same feature, the same directory convention, one owner
	// per directory instead of one per host. Load's diagnostics are dropped
	// here rather than printed per session — a directory that cannot be
	// created reports itself through the workspace failure above.
	ws.memory, _ = memory.Load(memory.Options{Project: true, Cwd: root})
	m.workspaces[root] = ws
	return ws
}

// JournalFor returns the file journal that owns a root's pre-images: the
// server root's shared one, or the per-owner one in multi-user mode.
func (m *Manager) JournalFor(root string) *tools.DirJournal {
	return m.workspaceFor(root).journal
}

// ShadowFor returns the checkpoint repo for a root, nil when unavailable.
func (m *Manager) ShadowFor(root string) *ShadowRepo {
	return m.workspaceFor(root).shadow
}

// resolveWorkspaceUnder is resolveWorkspace against an arbitrary base, which
// is how an owner's sessions resolve their workspace: relative to their own
// root, confined inside it, so one user cannot point a session at another's
// directory by naming it.
func (m *Manager) resolveWorkspaceUnder(base, rel string) (string, error) {
	if rel == "" || rel == "." {
		return base, nil
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("workspace must be relative to the workspace root: %q", rel)
	}
	// Join cleans the result, so the prefix check below is what keeps
	// "a/../../elsewhere" from escaping.
	abs := filepath.Join(base, rel)
	if !insideRoot(base, abs) {
		return "", fmt.Errorf("workspace escapes the workspace root: %q", rel)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("workspace %q: %w", rel, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("workspace is not a directory: %q", rel)
	}
	return abs, nil
}

// sessionCwd validates the working directory a transcript was created with. A
// hand-edited session file could point anywhere on disk, and honouring it
// would move the path guard outside the server root — so anything suspicious
// quietly falls back to the root.
func (m *Manager) sessionCwd(meta *session.Meta) string {
	if meta == nil || meta.Cwd == "" {
		return m.cfg.Cwd
	}
	abs, err := filepath.Abs(meta.Cwd)
	if err != nil || !insideRoot(m.cfg.Cwd, abs) {
		return m.cfg.Cwd
	}
	return abs
}

// insideRoot reports whether abs is the root itself or lives underneath it.
func insideRoot(root, abs string) bool {
	return abs == root || strings.HasPrefix(abs, root+string(filepath.Separator))
}

// citeSection is the citation contract for a new session's system prompt,
// appended last of the sections: it constrains how the final answer is written,
// so it sits closest to the conversation. Empty when citations are off;
// SystemPrompt drops empty sections.
func (m *Manager) citeSection() string {
	if !m.cfg.Citations {
		return ""
	}
	return agent.CitationSection()
}

// Get returns a live session, loading it from disk when it is not in memory.
func (m *Manager) Get(id string) (*Session, error) {
	if !validID(id) {
		return nil, ErrNotFound
	}
	for {
		m.mu.Lock()
		if m.closed || m.deleting[id] {
			m.mu.Unlock()
			return nil, ErrNotFound
		}
		if s, ok := m.sessions[id]; ok {
			s.touch()
			m.mu.Unlock()
			return s, nil
		}
		if load := m.sessionLoads[id]; load != nil {
			m.mu.Unlock()
			<-load.done
			if load.retiring {
				continue
			}
			return load.session, load.err
		}
		if m.sessionLoads == nil {
			m.sessionLoads = make(map[string]*sessionLoad)
		}
		load := &sessionLoad{done: make(chan struct{})}
		m.sessionLoads[id] = load
		m.mu.Unlock()
		return m.loadReservedSession(id, load)
	}
}

func (m *Manager) loadReservedSession(id string, load *sessionLoad) (*Session, error) {
	s, err := m.loadSession(id)
	m.mu.Lock()
	if err == nil {
		switch {
		case m.closed:
			err = errors.New("server is shutting down")
		case m.deleting[id]:
			err = ErrNotFound
		default:
			if _, statErr := os.Stat(m.transcriptPath(id)); statErr != nil {
				err = ErrNotFound
			} else if existing := m.sessions[id]; existing != nil {
				// Defensive for a separately published instance. Discarding a
				// temporary constructor never revokes or persists the winner.
				m.mu.Unlock()
				s.closeUnpublished()
				s = existing
				m.mu.Lock()
			} else {
				m.sessions[id] = s
			}
		}
	}
	m.mu.Unlock()
	if err == nil && s != nil && s.studioID != "" {
		if _, syncErr := s.SyncStudioToolPolicy(); syncErr != nil {
			s.hub.Publish(Event{Type: EvError, Error: "工具配置尚未同步：" + syncErr.Error()})
		}
	}
	if err != nil && s != nil {
		s.closeUnpublished()
		s = nil
	}
	m.mu.Lock()
	load.session, load.err = s, err
	delete(m.sessionLoads, id)
	close(load.done)
	m.mu.Unlock()
	if err == nil && s != nil {
		// Recovery may wake the Agent. Publish the fully constructed session and
		// release the load reservation before any completion can start a run.
		go s.restoreShellJobs()
	}
	return s, err
}

// loadSession constructs one private instance. Only loadReservedSession may
// publish it; it performs no registry mutation while doing disk/model setup.
func (m *Manager) loadSession(id string) (*Session, error) {

	path := m.transcriptPath(id)
	if _, err := os.Stat(path); err != nil {
		return nil, ErrNotFound
	}
	store, err := session.Open(path)
	if err != nil {
		return nil, err
	}
	studioBinding, studioBound := m.studio.binding(id)
	if meta := store.Meta(); meta != nil && meta.Runtime != "" {
		if !studioBound || meta.Runtime != studioBinding.runtime() {
			return nil, errors.New("conversation runtime binding is missing or unsupported")
		}
	} else if studioBound {
		return nil, errors.New("conversation runtime marker is missing")
	}
	// A recovered transcript that says nothing is indistinguishable from a healthy
	// one. The browser has no place to show this yet, so it goes to the server
	// console, which is where an operator would look for it.
	for _, d := range store.Diagnostics() {
		fmt.Fprintf(os.Stderr, "session %s: %s\n", id, d)
	}
	// The catalog row — not the file's presence — answers who this session
	// belongs to, and therefore whose memory and workspace roots to build it
	// against. No row means the classic unowned session (single-user server,
	// or a CLI-written transcript); the HTTP layer has already refused to name
	// such an id in multi-user mode, so reaching here with one is an internal
	// caller, which gets exactly the single-user treatment.
	var owner *identity.Scope
	if m.cfg.Catalog != nil {
		if rec, err := m.cfg.Catalog.OwnerOf(context.Background(), id); err == nil {
			scope := rec.Scope
			owner = &scope
		}
	}
	if studioBound && ((owner == nil) != (studioBinding.Owner == nil) || (owner != nil && *owner != *studioBinding.Owner)) {
		return nil, errors.New("Agent conversation ownership does not match its binding")
	}
	model := m.cfg.Model
	if meta := store.Meta(); meta != nil && meta.Model != "" {
		if _, err := config.Resolve(meta.Model); err == nil {
			model = meta.Model
		}
	}
	if studioBound {
		// A conversation's last in-allowlist switch outlives restarts: it is
		// part of the binding, not of the immutable definition snapshot.
		model = studioBinding.currentModel()
	}
	cfg, err := config.Resolve(model)
	if err != nil {
		return nil, err
	}
	usage, delegated := store.UsageTotals()
	cwd := m.sessionCwd(store.Meta())
	if owner != nil {
		// An owned session's working directory stays inside its owner's root
		// even when the transcript claims otherwise (a hand-edited or imported
		// file): the path guard must never leave the owner's tree, which is
		// the one thing the single-user clamp above cannot promise.
		userRoot := m.UserRoot(*owner)
		if !insideRoot(userRoot, cwd) {
			cwd = userRoot
		}
	}
	return m.newSession(store, cfg, store.TimedMessages(""), cwd, usage, delegated, owner), nil
}

// transcriptPath locates a session file: user sessions sit at the top of the
// session dir, delegated runs one level down (see session.SubagentDirName).
// The subagents/ copy is only consulted when the top level misses, so an id
// can never shadow a user session.
func (m *Manager) transcriptPath(id string) string {
	top := filepath.Join(m.cfg.SessionDir, id+".jsonl")
	if _, err := os.Stat(top); err == nil {
		return top
	}
	return filepath.Join(m.cfg.SessionDir, session.SubagentDirName, id+".jsonl")
}

// Delete removes a session file. An in-progress run blocks it: deleting the
// transcript of something still writing to it loses data for no reason.
func (m *Manager) Delete(id string) error {
	if !validID(id) {
		return ErrNotFound
	}
	m.mu.Lock()
	if m.closed || m.deleting[id] {
		m.mu.Unlock()
		return ErrNotFound
	}
	if m.deleting == nil {
		m.deleting = make(map[string]bool)
	}
	// Reserve deletion before waiting for a private constructor (or an
	// eviction's close) to drain. A constructor must not publish after the
	// Office preflight has revoked the conversation's previous generation.
	m.deleting[id] = true
	if load := m.sessionLoads[id]; load != nil {
		m.mu.Unlock()
		<-load.done
		m.mu.Lock()
		if m.closed {
			delete(m.deleting, id)
			m.mu.Unlock()
			return ErrNotFound
		}
	}
	s, live := m.sessions[id]
	if live {
		s.mu.Lock()
		if s.active != nil || s.compacting {
			s.mu.Unlock()
			delete(m.deleting, id)
			m.mu.Unlock()
			return ErrRunActive
		}
		s.closing = true
		s.mu.Unlock()
	}
	resultOwner, err := m.sessionResultOwnerForDelete(id, s)
	if err != nil {
		if live {
			s.mu.Lock()
			s.closing = false
			s.mu.Unlock()
		}
		delete(m.deleting, id)
		m.mu.Unlock()
		return err
	}
	// The transcript cannot be deleted while its live Office canvas still has
	// unsaved or unknown work. Hold the manager reservation during the host
	// preflight so another request cannot reload the conversation between it
	// and the deletion marker. The Office hook never calls back into Manager.
	if m.officeBeforeDelete != nil {
		if err := m.officeBeforeDelete(id); err != nil {
			if live {
				s.mu.Lock()
				s.closing = false
				s.mu.Unlock()
			}
			delete(m.deleting, id)
			m.mu.Unlock()
			return err
		}
	}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.deleting, id)
		if m.sessions[id] == s {
			delete(m.sessions, id)
		}
		m.mu.Unlock()
	}()

	if live {
		s.Close()
	}
	// Close drains foreground work, background child processes and completion
	// hooks before their result registry can be removed. Cleanup is explicit,
	// so retained/active-read TTL protection does not preserve a deleted scope.
	// On failure the transcript and ownership row remain intact for retry;
	// files already removed during a partial cleanup cannot be restored.
	removeSpill := tools.RemoveResultScopeSpill
	if m.spillScopeCleanup != nil {
		removeSpill = m.spillScopeCleanup
	}
	if err := removeSpill(tools.DefaultSpillDir(), sessionResultScope(id, resultOwner)); err != nil {
		return fmt.Errorf("Spill 清理失败，会话记录尚未删除，可重试: %w", err)
	}
	if err := os.RemoveAll(m.shellJobStoreDir(id, resultOwner)); err != nil {
		return fmt.Errorf("后台命令记录清理失败，会话记录尚未删除，可重试: %w", err)
	}
	// Ownership ends before the file does: a tombstoned id stays taken even
	// if the file removal below fails, and a crash between the two leaves an
	// unreachable file rather than an ownerless visible one.
	if m.cfg.Catalog != nil {
		if terr := m.cfg.Catalog.Tombstone(context.Background(), *resultOwner, id); terr != nil {
			return terr
		}
	}
	tp := m.transcriptPath(id)
	// Read what the cleanup needs before the transcript goes: the session's
	// workspace and the uploads its messages reference.
	refs, cwd := sessionUploadRefs(tp)
	if err := os.Remove(tp); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	_ = os.Remove(browserIconArchivePath(tp))
	_ = os.Remove(inboxPath(tp))
	m.gcSessionUploads(id, cwd, refs)
	return nil
}

// newSession wires a transcript store into a live Session. cwd is the
// session's working directory — the server root or one of its subdirectories —
// and it is what the tool registry's path guard confines file tools to. The
// journal stays workspace-wide: its paths are root-relative, so writes from
// any workspace subdirectory still land in the same workspace view.
//
// owner is the session's owning scope in multi-user mode (nil = the classic
// unowned session). It selects the per-owner workspace pair — journal,
// checkpoint repo, memory — that everything below resolves against, which is
// the whole of the isolation story: same tools, same runtime, one owner's
// roots.
//
// usage/delegated are the totals recovered from the transcript's stats
// SetMaxTurnsOverride applies the settings dialog's turn cap. n <= 0 explicitly
// disables it. The stored value covers sessions created later; the loop below
// pushes the choice into every cached session's agent too, so the next run of an
// already-open conversation obeys the save without a reload.
func (m *Manager) SetMaxTurnsOverride(n int) {
	if n < 0 {
		n = 0
	}
	m.maxTurnsOverride.Store(int64(n))
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		s.mu.Lock()
		a := s.agent
		s.mu.Unlock()
		if a != nil {
			m.applyMaxTurns(a)
		}
	}
}

// SetMemoryAuto applies the settings dialog's memory-distillation switch.
// Nothing is pushed into sessions: maybeDistillMemory reads the live value
// through MemoryAutoEnabled at a run's end, so the toggle takes effect on the
// next run of even an already-open conversation.
func (m *Manager) SetMemoryAuto(on bool) {
	m.memoryAuto.Store(on)
}

// MemoryAutoEnabled reports whether completed runs distil into the Agent's
// memory directory right now.
func (m *Manager) MemoryAutoEnabled() bool {
	return m.memoryAuto.Load()
}

// effectiveMaxTurns is the cap a newly created session starts with. Zero means unlimited.
func (m *Manager) effectiveMaxTurns() int {
	return int(m.maxTurnsOverride.Load())
}

func (m *Manager) applyMaxTurns(a *agent.Agent) {
	if n := m.effectiveMaxTurns(); n > 0 {
		a.SetMaxTurns(n)
	} else {
		a.SetUnlimitedTurns()
	}
}

// records (zero for a fresh session). They seed three places that must agree:
// the agent's counters, the hub's snapshot, and the recorded baseline that
// keeps the next finish() from re-recording history as a delta.
func (m *Manager) newSession(store *session.Store, cfg config.Resolved, history []session.Timed, cwd string, usage, delegated llm.Usage, owner *identity.Scope) *Session {
	id := strings.TrimSuffix(filepath.Base(store.Path()), ".jsonl")
	binding, studioBound := m.studio.binding(id)
	if studioBound {
		binding = binding.effectiveSkillBinding()
	}
	if studioBound && binding.runtime() == studioRuntime {
		return m.newStudioSession(store, cfg, history, usage, delegated, owner, binding.Agent)
	}
	if studioBound {
		cfg = applyStudioEffort(cfg)
	}
	root := m.cfg.Cwd
	if owner != nil {
		root = m.UserRoot(*owner)
	}
	ws := m.workspaceFor(root)
	// One consistent view of the skill-derived projections: an upload can swap
	// them while this build runs, and a session torn between old prompt section
	// and new tool set is a session neither load explains.
	v := m.skillViews()
	// A Studio definition may pin a capability subset: its Skills selection
	// narrows every skill-derived surface at once, its MCP selection narrows
	// the pool projection below. Legacy definitions (nil selections) keep
	// every other surface but stop paying manifest-tool schema tokens — the
	// editor is where an explicit selection is made. Ordinary sessions (no
	// binding at all: embed bridge, MCP submit, legacy global schedules) have no selection
	// mechanism of their own, so the global set stays their registration.
	var mcpOnly map[string]bool
	invocationSkills := m.Skills()
	explicitSkills := false
	// The definition's native-tool selection travels the same pointer semantics
	// as its Skills: nil is the legacy full set. browserTool is the arming
	// path's half of the decision — the registry's half is Enabled below.
	toolSelection := m.studioNativeSelection(binding.effectiveNativeAgent())
	browserTool := m.browserAllowed(owner)
	if studioBound {
		subset, only := m.studioSelection(binding.Agent)
		if subset != nil {
			v = m.filterSkillViews(v, subset)
			invocationSkills = subset
			explicitSkills = true
		}
		mcpOnly = only
		if !explicitSkills {
			v = m.unregisterSynthTools(v)
		}
		if toolSelection != nil && !slices.Contains(*toolSelection, "browser") {
			browserTool = false
		}
		if authored, err := m.authoredSkills(owner, binding.Agent.ID); err == nil && len(authored) > 0 {
			invocationSkills = appendAuthoredSkills(invocationSkills, authored)
			v = m.filterSkillViews(v, invocationSkills)
		}
	}
	if !m.browserAllowed(owner) {
		deviceSkills := slices.DeleteFunc(slices.Clone(invocationSkills), func(sk skills.Skill) bool { return sk.BrowserTask })
		v = m.filterSkillViews(v, deviceSkills)
	}
	hub := NewHub()
	hub.Seed(history)
	hub.SetCompactions(store.Compactions(""))
	modelHistory := store.PromptTimedMessages("")
	hub.SetRunInfo(cfg.Model, cfg.Provider, cfg.ContextWindow)
	hub.SetUsage(usage)

	policy := NewPolicy()
	// Declared read-only tools skip review like the read-only built-ins do.
	// Marked per session on a policy computed from the snapshot above, because
	// the annotation travels with the manifest and the manifest is fixed for
	// the session's life once captured here.
	policy.MarkReadOnly(v.synthRO...)
	boundRuntimeSkill, runtimeSkillTools, runtimeSkillPrompt := m.runtimeSkillTools(binding, id)
	if boundRuntimeSkill != nil {
		invocationSkills = append(append([]skills.Skill(nil), invocationSkills...), boundRuntimeSkill.Skills()...)
	}
	for _, t := range runtimeSkillTools {
		if ro, ok := t.(tools.ReadOnlyCapable); ok && ro.IsReadOnly() {
			policy.MarkReadOnly(t.Name())
		}
	}
	// Copy the shared view's slice before composing this session's tools.
	sessionTools := append(append([]tools.Tool(nil), v.synthTools...), runtimeSkillTools...)
	if m.officeForSession != nil && (!studioBound || explicitSkills) {
		for _, sk := range invocationSkills {
			if sk.Name == "office" || sk.Name == "docs" {
				if link := m.officeForSession(id, cwd, owner, hub); link != nil {
					sessionTools = append(sessionTools, &tools.Office{Link: link})
				}
				break
			}
		}
	}
	// Seed the initial view without network I/O. New runs refresh this owner's
	// pool and replace only MCP entries. Explicit server trust is separate from
	// remembered session approvals so removal or redefinition can revoke those.
	mcpTools, mcpTrusted := m.mcpProjectionFor(sessionTools, owner, mcpOnly)
	policy.ReplaceMCP(mcpTools, mcpTrusted)
	gate := NewWebGate(hub, policy, m.cfg.GateTimeout)
	interactive := false
	if meta := store.Meta(); meta != nil && meta.BackgroundTask == nil && meta.Sentinel == nil && meta.AgentWorkflow == nil {
		interactive = true
	}
	s := &Session{
		ID:           strings.TrimSuffix(filepath.Base(store.Path()), ".jsonl"),
		mgr:          m,
		hub:          hub,
		policy:       policy,
		gate:         gate,
		browser:      NewBrowserGate(gate),
		store:        store,
		cfg:          cfg,
		cwd:          cwd,
		root:         root,
		owner:        owner,
		shadow:       ws.shadow,
		persisted:    len(modelHistory),
		recorded:     session.Recorded{Usage: usage, Delegated: delegated},
		lastUsed:     time.Now(),
		mcpReserved:  sessionTools,
		runtimeSkill: boundRuntimeSkill,
		// The session's skill snapshot for observation: captured with the
		// same views lock as the prompt section and tool set, so a reload
		// can never re-attribute this session's reads or executions.
		skillSnapshot: append([]skillSnapshotEntry(nil), v.snapshot...),
		browserTool:   browserTool,
	}
	if interactive {
		s.userInput = NewWebUserInputWithStore(hub, DefaultUserInputTimeout, store.Path(), s.deliverUserInputAnswer)
	}
	if m.cfg.browser != nil && m.browserAllowed(owner) {
		s.nativeBrowser = newNativeBrowser(m.cfg.browser)
		s.nativeBrowser.bindSessionProfile(s.ID, store.Path(), owner)
		s.nativeBrowser.bindDownloads(s, m.browserDownloadPolicy)
		s.nativeBrowser.uploadRoot = s.cwd
		icons := newBrowserIconArchive(store.Path(), owner)
		s.nativeBrowser.rememberIcons = icons.remember
		s.nativeBrowser.storeScreenshot = s.storeBrowserScreenshot
		s.browser.executor = s.nativeBrowser
		// Bind host metadata independently of the initial selection so a later
		// native-tool grant reuses this same Agent-scoped browser channel.
		{
			bookmarkOwner := owner
			if owner != nil {
				copy := *owner
				bookmarkOwner = &copy
			}
			bookmarkAgentID := ""
			if studioBound {
				bookmarkAgentID = binding.Agent.ID
			}
			s.browser.bookmarks = func(ctx context.Context, query string, limit, offset int) (tools.BrowserReply, error) {
				if err := ctx.Err(); err != nil {
					return tools.BrowserReply{}, err
				}
				return m.browserBookmarkReply(bookmarkOwner, query, limit, offset, bookmarkAgentID)
			}
		}
	}
	if studioBound {
		s.studioID, s.studioName = binding.Agent.ID, binding.Agent.Name
		s.studioWeb = true
		// The Agent's own memory layer: one directory per definition under the
		// owner's root, shared by every session of this Agent. Background
		// executions (schedule, sentinel, workflow) share the directory but do
		// not distill into it — their transcripts are task output, not
		// conclusions a person steered.
		if validAgentDirName(binding.Agent.ID) {
			s.memoryDir = memory.ProjectDir(filepath.Join(root, "agents", binding.Agent.ID))
		}
		// Eligibility for distillation is fixed here (identity), while the
		// on/off switch is the Manager's live setting read at each run's end.
		s.memoryDistill = s.memoryDir != "" &&
			s.store.Meta().BackgroundTask == nil && s.store.Meta().Sentinel == nil && s.store.Meta().AgentWorkflow == nil
		// The capability contract of the pinned definition revision: the
		// model allowlist SetModel checks against, the skills /skill:name may
		// expand, and the server set every MCP refresh re-narrows to. Model and
		// Skill/native policies may move at idle boundaries; MCP selections and
		// Agent instructions retain their original definition snapshot.
		s.studioModels = binding.allowedModels()
		s.invocationSkills = invocationSkills
		s.mcpOnly = mcpOnly
	}
	// A managed transform uses this session's existing command permission. The
	// Office tool alone cannot grant a definition that withheld bash a shell.
	commandProtectionManifest := ""
	if m.officeCommandProtection != nil {
		manifest, protectionErr := m.officeCommandProtection(cwd, owner)
		commandProtectionManifest = manifest
		if protectionErr != nil {
			if commandProtectionManifest == "" {
				commandProtectionManifest = filepath.Join(m.cfg.SessionDir, "office-command-protection", ".unavailable-"+officeHash([]byte(s.ID))+".json")
			}
			hub.Publish(Event{Type: EvError, Error: "文档修改保护暂未就绪；命令不会绕过文档事务。请检查文档状态后重试。"})
		}
	}
	for _, tool := range sessionTools {
		if officeTool, ok := tool.(*tools.Office); ok {
			if link, ok := officeTool.Link.(*officeLink); ok {
				link.commandSession = s
				link.commandEnabled = toolSelection == nil || slices.Contains(*toolSelection, "bash")
			}
		}
	}
	// Attachments first: the tool registration below (Options.Images) resolves
	// against the images still present in model context.
	s.seedImages(modelHistory)
	s.seedManagedOfficeAttachments()
	// The background runner's context is the session's own: independent of any
	// run, cancelled when the session closes or is evicted. The hooks are the
	// session's methods, which is what puts a backgrounded child's progress on
	// the same hub every subscriber is already watching.
	bgCtx, bgCancel := context.WithCancel(context.Background())
	s.bgCancel = bgCancel
	// The same lifetime parents follow-up generation: a run's context dies with
	// the run, and this work only starts as the run ends.
	s.sessCtx = bgCtx
	s.followupsOn = m.cfg.Followups
	s.bg = tools.NewBgRunner(bgCtx, tools.BgHooks{
		OnStart:    s.onBgStart,
		OnFrame:    s.onBgFrame,
		OnComplete: s.onBgComplete,
	})
	s.initShellJobs(bgCtx)
	editCfg, _ := agent.ParseContextEdit(m.cfg.ContextEdit, cfg.ContextWindow)
	// Same read of the spec for the same reason: a bad one was already rejected
	// at startup, so an error here can only mean an unknown window, which
	// ParseCompactSpec answers with "off" — compaction stays manual.
	compactCfg, _ := agent.ParseCompactSpec(m.cfg.Compact, cfg.ContextWindow)
	// The Agent layer lists ahead of the owner-wide one: the nearer scope is the
	// more relevant record. Loaded fresh at each build — LoadAgent is a small
	// directory walk, and a cached listing would hide notes a sibling session
	// wrote after this Manager first saw the Agent.
	memStore := ws.memory
	if s.memoryDir != "" {
		if agentStore, _ := memory.LoadAgent(filepath.Join(root, "agents", s.studioID)); agentStore != nil {
			memStore = agentStore.Combine(ws.memory)
		}
	}
	memorySection, citationSection := memStore.PromptSection(), m.citeSection()
	// Labeled as it is assembled, so the context meter can itemize the fixed
	// cost (agent.OverheadBreakdown). The joined parts are the prompt itself.
	s.buildSkillPrompt = func(view skillViews, runtimePrompt string) (string, []agent.PromptPart) {
		parts := agent.SystemPromptParts(cwd,
			agent.PromptPart{Key: agent.PromptPartUI, Text: view.uiSection},
			agent.PromptPart{Key: agent.PromptPartSkills, Text: view.section},
			agent.PromptPart{Key: agent.PromptPartMemory, Text: memorySection},
			agent.PromptPart{Key: agent.PromptPartCitations, Text: citationSection})
		if studioBound {
			parts = append(parts, agent.PromptPart{Key: agent.PromptPartAgent,
				Text: "\n\n# Agent configuration\nYou are " + binding.Agent.Name + ".\n" + binding.Agent.Instructions})
		}
		if runtimePrompt != "" {
			parts = append(parts, agent.PromptPart{Key: agent.PromptPartRuntime, Text: runtimePrompt})
		}
		return agent.JoinPromptParts(parts), parts
	}
	s.skillProtectionManifest = commandProtectionManifest
	systemPrompt, systemPromptParts := s.buildSkillPrompt(v, runtimeSkillPrompt)
	childOwnerKey := sessionResultOwnerKey(owner)
	childStore, childStoreErr := tools.NewSubagentStore(m.cfg.SessionDir, s.ID, childOwnerKey)
	s.retainedSubagents = childStore
	informationStore, informationErr := m.InformationStore(owner)
	informationTool := &tools.Information{Store: informationStore, Err: informationErr, Cwd: cwd}
	var shortcutBackend tools.ShortcutBackend
	var authorBackend tools.SkillAuthorBackend
	if studioBound {
		if backend := m.shortcutToolBackend(s); backend != nil {
			shortcutBackend = backend
		}
		if backend := m.skillAuthorBackend(s); backend != nil {
			authorBackend = backend
		}
	}
	var informationRoot, informationScope string
	if informationErr == nil && informationStore != nil {
		informationRoot, informationScope = informationStore.Root(), informationStore.ScopeID()
	}
	msgs := make([]llm.Message, 0, len(modelHistory))
	for _, t := range modelHistory {
		msgs = append(msgs, t.Message)
	}
	var legacyResultPaths []string
	if owner == nil {
		// Legacy shared spill files have no owner provenance. Preserve exact
		// historical paths only for the single-user host, never turn a supplied
		// transcript path into a cross-owner read capability.
		legacyResultPaths = tools.LegacyResultPaths(msgs)
	}
	s.agent = agent.New(agent.Config{
		Client: s.client(cfg),
		// The subagent tool is registered here too, not only in the terminal: the
		// browser is where an approval gate exists, so it is the mode where
		// delegating work is most useful and most in need of supervision.
		Registry: tools.New(tools.Options{
			Cwd:                       cwd,
			UserInput:                 s.userInput,
			AsyncUserInput:            s.userInput,
			ShellJobs:                 s.shellJobs,
			ResultScope:               sessionResultScope(s.ID, owner),
			LegacyResultPaths:         legacyResultPaths,
			Information:               informationTool,
			Shortcuts:                 shortcutBackend,
			SkillAuthor:               authorBackend,
			Watches:                   m.watchBackendForConversation(s),
			BackgroundTasks:           m.backgroundBackendForConversation(s),
			AgentWorkflows:            m.agentWorkflowToolBackend(s),
			CommandProtectionManifest: commandProtectionManifest,
			// The pinned definition's native-tool selection; nil for legacy and
			// ordinary sessions, which keeps every built-in exactly as before.
			// The protected core survives any selection — see Options.Enabled.
			Enabled: toolSelection,
			// The browser pane is this session's other executor: the browser
			// tool's operations travel through the gate below to whatever pane
			// the UI has open. Whether an operation may run at all is the
			// gate's armed-task decision, not a registry one — see
			// BrowserGate.Arm.
			Browser:          browserLinkIf(m.browserAllowed(owner), s.browser),
			BrowserImageMode: browserModelImageMode(cfg),
			// The image bridge: analyze_image reaches the vision helper through
			// the manager, and resolves names against this session's
			// attachments. Nil-safe by construction — the tool is only
			// registered when a vision_model exists, so a deployment without
			// one spends no schema tokens on a tool that cannot answer.
			Images: m.imageTool(s),
			// Skill bundles for the prompt-rewrite protection described on
			// Options.ReadRoots, plus the deployment's extra readable roots, one
			// union computed in NewManager.
			ReadRoots: v.readRoots,
			// A browser session is always top level, so it always gets memory, for the
			// same reason it always gets the task list. Children are separate processes
			// and decide in toolOptions, where the depth is known. The roots are the
			// owner's memory plus the Agent's own layer in multi-user mode and the
			// server's single shared one otherwise — the write grant follows the
			// prompt injection, never spans two owners.
			WriteRoots: memStore.Roots(),
			// A browser session is always top level, so it always gets the task list.
			// Children are spawned as separate processes and decide for themselves in
			// toolOptions, where the depth is known.
			Todo: true,
			// Declared tools, after the built-ins and before the coordination
			// tools — see tools.Options.Synth.
			Synth: sessionTools,
			// The read tool's skill-load attribution, from the same snapshot —
			// see tools.Options.SkillReads.
			SkillReads: skillReadTargets(v.snapshot),
			// The external MCP servers' tools, after the declared ones — see
			// tools.Options.MCP. Later runs replace just this part of the set.
			MCP: mcpTools,
			// The journal is workspace-wide; ForSession stamps who touched first.
			Journal: ws.journal.ForSession(s.ID),
			Subagent: m.subagentForAgentWorkflow(s, &tools.Subagent{
				Enabled:                   cloneToolSelection(toolSelection),
				InformationRoot:           informationRoot,
				InformationScope:          informationScope,
				CommandProtectionManifest: commandProtectionManifest,
				Store:                     childStore, StoreError: childStoreErr,
				// MaxTurns is deliberately not inherited: see Subagent.MaxTurns. This
				// session's turn limit bounds this session's run.
				Cwd: cwd, Model: cfg.Model,
				// Both were validated when the server started — NewManager refuses a
				// bad value — so these cannot fail in a way a session could report.
				Concurrency: m.subagentConcurrency,
				// An explore child may be configured onto a different model than this
				// session's. Resolved from cfg rather than the manager's default, so
				// that switching models mid-session moves the subagent with it.
				ExploreModel: config.SubagentModel(cfg.Model),
				ExplorePool:  explorePool(),
				// The session's skills travel to every child; the read-only
				// declared tools are named for its explore note. See the fields.
				SkillPaths:         v.pathFlags,
				ExplicitSkills:     studioBound && binding.Agent.Skills != nil,
				SkillReadRoots:     append([]string(nil), v.readRoots...),
				ReadOnlySkillTools: tools.ReadOnlySynthNames(v.synthTools),
				// The child's calls come back through the same gate the user is
				// already watching, marked with the subagent they came from. Passing
				// the gate rather than a copy of the policy is what makes "a
				// subagent never exceeds its parent" structural.
				Review: subagentReview(s.gate, s.hub, commandProtectionManifest),
				// The same arrangement for the pane: a foreground child drives this
				// session's browser over a pipe instead of having one of its own. It
				// is the same gate object behind the subPane loan, so the armed-task
				// check applies to children transitively — a child spawned outside
				// a browser task gets the same refusal its parent would, and only
				// the main conversation can raise the arm card. A definition that
				// withheld the browser tool loans nothing: the child's registry
				// follows the parent's.
				Browser: subPaneIf(browserTool, s.browser),
				// The browser is where a run can end while delegated work continues,
				// which is exactly what the background runner is for. The terminal
				// gets none: a one-shot process has no "later" to deliver a result
				// in, so its calls stay synchronous.
				Bg: s.bg,
			}),
		}),
		SystemPrompt:      systemPrompt,
		SystemPromptParts: systemPromptParts,
		MaxTurns:          m.effectiveMaxTurns(),
		// One output-limit continuation stays inside this run's existing
		// allowance. It never increases the operator's model output cap.
		Continuation:     agent.ContinuationConfig{MaxAttempts: 1},
		ToolOutputBudget: agent.ToolOutputBudgetConfig{ContextWindow: cfg.ContextWindow, MaxOutputTokens: cfg.MaxTokens},
		// Images resolve per model call, so a mid-session switch between a
		// text-only and a vision model changes what the same history sends —
		// pixels to one, description text to the other — without a rewrite.
		ImageResolver: s.imageResolver,
		Gate:          s.gate,
		// Half the run may go to waiting for approvals; the other half is for the
		// work. Without a cap, a batch of unanswered calls at GateTimeout apiece
		// outlasts RunTimeout and the run's own deadline kills the whole turn.
		ReviewBudget: m.cfg.RunTimeout / 2,
		// C1 RFC: stop detection - default disabled for web UI
		StagnationThreshold: 0,
		TokenBudget:         0,
		CostBudget:          0,
		TimeBudget:          0,
		// The CLI's checkpoint interval (agent.DefaultSoftTurns) is a flag
		// default, not a Config one: pinned to 0 here like the budgets.
		SoftTurns: 0,
		// Resolved per session against this session's model, because "auto" is half
		// that model's window. A bad spec was already rejected at startup, so an error
		// here can only mean the catalogue lost the model; clearing off is the safe
		// reading of that, and it is what ParseContextEdit returns anyway.
		ContextEdit: editCfg,
		// Same resolution, same fallback: an unknown window means compaction
		// stays on the /compact button.
		Compact: compactCfg,
		// Citations and Cwd: the stamp registry is per session, and the session's
		// directory is what shortens source paths to relative ones.
		Citations: m.cfg.Citations,
		Cwd:       cwd,
	})
	m.applyMaxTurns(s.agent)
	s.agent.SetMessages(msgs)
	s.agent.SetUsage(usage, delegated)
	hub.SetOverhead(s.agent.OverheadBreakdown())
	legacyCompaction := false
	if len(msgs) > 0 {
		_, legacyCompaction = agent.CompactionContinuationSummary(msgs[0])
	}
	if len(store.Compactions("")) > 0 || legacyCompaction {
		hub.EstimateContext(newContextPrompt(msgs, s.agent.OverheadTokens()))
	}
	hub.SetClearTrigger(s.agent.ContextEditTrigger())
	if err := s.loadInboxLocked(); err != nil {
		hub.Publish(Event{Type: EvError, Error: "无法读取待处理消息：" + err.Error()})
	}
	if s.userInput != nil {
		go s.userInput.recoverAnswers()
	}
	return s
}

// explorePool adapts the configured subagent pool into the tool's plain shape;
// package tools stays ignorant of the catalogue.
func explorePool() []tools.ExploreTarget {
	pool := config.SubagentPool()
	out := make([]tools.ExploreTarget, 0, len(pool))
	for _, m := range pool {
		out = append(out, tools.ExploreTarget{Provider: m.Provider, Model: m.ID})
	}
	return out
}

// client builds an LLM client whose retry notices reach the browser. A
// rate-limited turn should look like waiting, not like a hang.
func (s *Session) client(cfg config.Resolved) llm.Client {
	onRetry := func(r llm.RetryInfo) {
		s.hub.Publish(Event{
			Type: EvRetry, Attempt: r.Attempt, Max: r.Max,
			DelayMS: r.Delay.Milliseconds(), Reason: r.Reason,
		})
	}
	if s.mgr.cfg.NewClient != nil {
		return s.mgr.cfg.NewClient(cfg, onRetry)
	}
	return llm.New(llm.Options{
		BaseURL:       cfg.BaseURL,
		APIKey:        cfg.APIKey,
		Model:         cfg.Model,
		MaxTokens:     cfg.MaxTokens,
		Timeout:       cfg.Timeout,
		Headers:       cfg.Headers,
		ExtraBody:     cfg.ExtraBody,
		Compatibility: cfg.Compatibility,
		MaxRetries:    s.mgr.cfg.Retries,
		OnRetry:       onRetry,
	})
}

// reap evicts idle sessions and gives up on runs that will never finish.
func (m *Manager) reap() {
	defer m.wg.Done()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.evictIdle(m.cfg.IdleTimeout)
		}
	}
}

// evictAll drops every idle session from memory. It is the eviction path with
// the clock taken out, which is what makes the reload-from-disk behaviour
// testable without waiting out a timeout.
func (m *Manager) evictAll() { m.evictIdle(0) }

func (m *Manager) evictIdle(maxIdle time.Duration) {
	var evicted []*Session
	m.mu.Lock()
	for id, s := range m.sessions {
		if m.deleting[id] || m.sessionLoads[id] != nil {
			continue
		}
		// A human can still be editing after the Agent run has ended. Keep the
		// owning conversation until its Office documents close, including during
		// a skill-view refresh; dropping it would revoke unsaved editor work.
		if m.officeSessionActive != nil && m.officeSessionActive(id) {
			continue
		}
		// Only a session with nothing running and nobody watching is a candidate.
		// Its state is on disk, so the next request rebuilds it transparently. A
		// background subagent still in flight counts as running: its result has
		// nowhere to go once the hub is gone.
		s.mu.Lock()
		if s.active != nil || s.compacting || s.closing || s.hub.Subscribers() > 0 || s.BgActive() {
			s.mu.Unlock()
			continue
		}
		if time.Since(s.lastUsed) < maxIdle {
			s.mu.Unlock()
			continue
		}
		if err := s.persistMessagesLocked(s.agent.Messages()); err != nil {
			s.mu.Unlock()
			continue
		}
		if err := s.recordCostLocked(s.agent.Messages()); err != nil {
			s.mu.Unlock()
			continue
		}
		s.closing = true
		s.mu.Unlock()
		if m.sessionLoads == nil {
			m.sessionLoads = make(map[string]*sessionLoad)
		}
		m.sessionLoads[id] = &sessionLoad{done: make(chan struct{}), retiring: true}
		delete(m.sessions, id)
		evicted = append(evicted, s)
	}
	m.mu.Unlock()

	for _, s := range evicted {
		s.Close()
		m.mu.Lock()
		load := m.sessionLoads[s.ID]
		delete(m.sessionLoads, s.ID)
		close(load.done)
		m.mu.Unlock()
	}
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validID keeps a session id from becoming a path. It is joined with the session
// directory, so anything that could escape it has to be rejected here.
func validID(id string) bool {
	return id != "" && len(id) <= 128 && idPattern.MatchString(id) &&
		!strings.Contains(id, "..") && !strings.ContainsAny(id, `/\`)
}

func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
