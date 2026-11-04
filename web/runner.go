package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/skillobserve"
	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

// Session is one conversation: an Agent, its transcript on disk, its event Hub,
// and its approval gate.
type Session struct {
	ID string
	// studioID pins this conversation to a server-owned Agent definition.
	studioID string
	// Pinned, server-owned identity; read without taking a second Studio lease.
	studioName string
	// studioWeb reuses Pogo's original workbench runtime and policy. Older
	// text-only bindings keep their limited profile when restored.
	studioWeb    bool
	runtimeSkill *skillruntime.BoundSession
	// browserTool records whether the pinned definition's tool selection kept
	// the browser tool (legacy/nil selections keep it). The /skill:name arming
	// path consults it: an Agent without the tool never opens the gate.
	browserTool bool

	mgr           *Manager
	hub           *Hub
	policy        *Policy
	gate          *WebGate
	userInput     *WebUserInput
	browser       *BrowserGate
	nativeBrowser *nativeBrowser

	// mu guards everything an HTTP request and the run goroutine both touch.
	// Note what it does not guard: the Hub and the gate have their own locks and
	// never call back into Session, so a request can publish or answer an
	// approval while a run holds this.
	wakeMu             sync.Mutex
	mu                 sync.Mutex
	agent              *agent.Agent
	store              *session.Store
	cfg                config.Resolved
	modelConfigPending bool // saved settings waiting for an idle boundary; guarded by mu
	persisted          int
	// cwd is the session's working directory, resolved and validated at
	// creation. The file tools' path guard and the terminal's shell both root
	// here.
	cwd string
	// root is the workspace root this session belongs to: the server root, or
	// the owner's root in multi-user mode. The journal, checkpoint repo and
	// memory the session's tools use are the ones keyed to this root.
	root string
	// owner is the session's owning scope in multi-user mode; nil is the
	// classic unowned session of the single-user server.
	owner  *identity.Scope
	shadow *ShadowRepo
	// compactFails is the auto-compaction circuit breaker: consecutive failed
	// automatic compactions, tripping at 3 (the same number the terminal's
	// driver uses, for the same reason — a loop asking twice is a bug). Manual
	// compaction is not behind it; one success resets it. Guarded by mu because
	// Start's pre-run check reads it before a run exists.
	compactFails int
	// term is the lazily spawned shell; nil until the panel first attaches.
	term *Terminal
	// recorded is how much of the agent's running totals is already on disk, so each
	// run records only what it added. Touched only from finish(), under the same
	// conditions that make reading the agent's counters safe.
	recorded      session.Recorded
	active        *activeRun
	compacting    bool
	compactCancel context.CancelFunc
	compactDone   chan struct{}
	closing       bool
	closeDone     chan struct{}
	lastUsed      time.Time
	// Inbox state is session-local and uses the same mutex and transcript owner.
	inbox         []InboxItem
	inboxHashes   map[string]string
	inboxLoaded   bool
	inboxErr      error
	inboxStarting string

	// images are this conversation's attachments, oldest first, in the shape
	// the analyze_image tool resolves names against. Seeded from history on
	// load, appended on each Start that carries images. Under mu.
	images []tools.ImageHandle

	// bg runs the session's backgrounded subagents; bgCancel is its lifetime,
	// cancelled when the session goes away. bgInbox collects completion
	// notifications that arrive while no run is in flight, and bgWake is the
	// timer that opens a run for them; bgOff latches when the session closes so
	// no late completion can start a run nobody is watching.
	bg *tools.BgRunner
	// shellJobs owns commands that outlive the run that dispatched them. Its
	// lifetime is the session's, so Cancel only stops foreground work.
	shellJobs         *tools.ShellJobRunner
	shellNotify       *shellNotificationState
	retainedSubagents *tools.SubagentStore
	bgCancel          context.CancelFunc
	bgInbox           []string
	bgWake            *time.Timer
	bgOff             bool

	// sessCtx is the session's own context: independent of any run (a run's
	// context is cancelled when its consume returns), cancelled when the
	// session goes away. Work a run spawns but must not wait on — follow-up
	// suggestion generation — is parented here. Shares bgCancel's lifetime.
	sessCtx context.Context
	// followupsOn is the server's -followups switch, fixed at creation: when
	// off, a run's end suggests nothing and the client's rule-based matching
	// is the only source of chips.
	followupsOn bool
	// memoryDistill marks the session eligible for memory distillation:
	// an ordinary interactive Agent session. Background runs (schedule,
	// sentinel, workflow) are ineligible — their transcripts are task output,
	// not conclusions a person steered. The on/off switch is not stored here:
	// it is the Manager's live setting (prefs.json memory_auto), read at each
	// run's end so a settings change applies without a reload.
	memoryDistill bool
	// memoryDir is the Agent-level memory directory, <owner root>/agents/<id>/
	// .pogo/memory. Empty for sessions without an Agent binding, and for
	// bindings whose id does not have the generated directory shape.
	memoryDir string
	// Effective Skill snapshot used for MCP name collisions. New MCP catalogs
	// never reload Skills; explicit Agent edits synchronize only while idle.
	mcpReserved []tools.Tool
	// skillSnapshot is this session's captured skill observation view: the
	// trusted identities discovery events are emitted for and the read
	// targets loads are attributed through. Fixed at creation with the rest
	// of the skill views; explicit Agent selection edits replace them together
	// at an idle boundary, while uploads alone leave the current projection intact.
	skillSnapshot []skillSnapshotEntry

	// MCP grants remain fixed at creation. Model and Skill policies synchronize
	// independently between runs under mu; invocationSkills
	// is the skill set /skill:name may expand from;
	// mcpOnly narrows every MCP projection and refresh (nil = the whole
	// pool, which is also what ordinary sessions carry).
	studioModels            []string
	invocationSkills        []skills.Skill
	buildSkillPrompt        func(skillViews, string) (string, []agent.PromptPart)
	skillProtectionManifest string
	mcpOnly                 map[string]bool
}

type activeRun struct {
	id     string
	cancel context.CancelFunc
	done   chan struct{}
	// Internal scheduled-run receipt; published only by closing done.
	persistErr error
	model      string
	observe    func(Event)
	wake       *sentinelWakeBinding
	// Input arriving during catalog/image preparation belongs to this run.
	// Guarded by Session.mu until it is handed to the agent at launch.
	preparing bool
	steering  []llm.Message
}

func (s *Session) Hub() *Hub             { return s.hub }
func (s *Session) Gate() *WebGate        { return s.gate }
func (s *Session) Policy() *Policy       { return s.policy }
func (s *Session) Browser() *BrowserGate { return s.browser }

// browserToolEnabled reports whether this session's registry carries the
// browser tool — false when a pinned definition or device-owner binding
// withholds it. The arming path refuses without it: the grant and
// the tool are one capability.
func (s *Session) browserToolEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.browserTool
}

func (s *Session) Model() config.Resolved {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

func (s *Session) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.Path()
}

// Workspace is the session's working directory relative to the server root,
// slash-separated; "" means the root itself.
func (s *Session) Workspace() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta := s.store.Meta()
	if meta == nil {
		return ""
	}
	rel, err := filepath.Rel(s.mgr.Cwd(), meta.Cwd)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

// SetInfo appends sidebar edits (pin state, custom title) as a meta record.
// Under s.mu for the same reason finish() persists under it: the run goroutine
// may be mid-append, and store.append is not internally locked.
func (s *Session) SetInfo(title *string, pinned *bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return ErrNotFound
	}
	if s.compacting {
		return ErrRunActive
	}
	return s.store.AppendMeta(&session.Meta{Title: title, Pinned: pinned})
}

func (s *Session) Active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active != nil || s.compacting || s.inboxStarting != ""
}

// Terminal returns the session's shell, spawning it on first use and
// respawning after the user exits it. The shell outlives page reloads and
// session switches — it dies only with the session itself.
func (s *Session) Terminal() (*Terminal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return nil, ErrNotFound
	}
	if s.term != nil && s.term.Exited() {
		s.term = nil
	}
	if s.term == nil {
		t, err := startTerminal(s.cwd, 80, 24)
		if err != nil {
			return nil, err
		}
		s.term = t
	}
	return s.term, nil
}

// closeTerminal kills the shell, if one was ever spawned. Called wherever the
// session itself goes away — eviction, deletion, server shutdown — so a
// forgotten session never leaves a shell (and its children) behind.
func (s *Session) closeTerminal() {
	s.mu.Lock()
	t := s.term
	s.term = nil
	s.mu.Unlock()
	if t != nil {
		t.Kill()
	}
}

func (s *Session) touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastUsed = time.Now()
}

func (s *Session) idleSince() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastUsed
}

// compactCircuit is how many consecutive failed auto-compactions disable the
// browser's pre-run check for the rest of the session — the same number, and
// for the same reason, as the terminal driver's breaker in main.go. A failing
// summariser must not turn every submitted prompt into a failed model call.
const compactCircuit = 3

// prepareInputAttachments is outside the run lock, shared by immediate and
// queued/steering ordered input. Never persist bytes in the receipt ledger.
func (s *Session) prepareInputAttachments(attachments []string, parts []MessageInputPart) ([]llm.Block, []llm.Block, map[string][]llm.Block, []tools.ImageHandle, error) {
	var imageBlocks, fileBlocks []llm.Block
	byPath := make(map[string][]llm.Block)
	var handles []tools.ImageHandle
	requests := flatInputParts(parts)
	if parts == nil {
		for _, rel := range attachments {
			requests = append(requests, MessageInputPart{Type: "file", Path: rel})
		}
	}
	for _, part := range requests {
		if part.Type != "file" {
			continue
		}
		rel, key := part.Path, inputAttachmentKey(part)
		if _, exists := byPath[key]; parts != nil && exists {
			continue
		}
		var current []llm.Block
		abs, err := attachAbs(s.cwd, rel)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("%w: 附件 %s: %v", errMessageInputInvalid, rel, err)
		}
		if parts != nil {
			resolved, resolveErr := filepath.EvalSymlinks(abs)
			root, rootErr := filepath.EvalSymlinks(s.cwd)
			if resolveErr != nil || rootErr != nil || !withinRoot(root, resolved) {
				return nil, nil, nil, nil, fmt.Errorf("%w: 附件 %s 不存在或越出工作区", errMessageInputInvalid, rel)
			}
		}
		st, err := os.Stat(abs)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("%w: 附件 %s 不存在（可能已被移动或删除），请重新上传", errMessageInputInvalid, rel)
		}
		if !st.Mode().IsRegular() {
			return nil, nil, nil, nil, fmt.Errorf("%w: 附件 %s 不是普通文件", errMessageInputInvalid, rel)
		}
		name := filepath.Base(rel)
		if runtime := s.runtimeSkillSnapshot(); runtime != nil {
			if !st.Mode().IsRegular() || st.Size() > 32<<20 {
				return nil, nil, nil, nil, fmt.Errorf("Skill 附件 %s 必须是最多 32 MiB 的普通文件", rel)
			}
			f, err := os.Open(abs)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			data, readErr := io.ReadAll(io.LimitReader(f, (32<<20)+1))
			_ = f.Close()
			if readErr != nil {
				return nil, nil, nil, nil, readErr
			}
			upload, err := runtime.Upload(data)
			if err != nil {
				return nil, nil, nil, nil, fmt.Errorf("Skill 附件 %s: %w", rel, err)
			}
			note := llm.Block{Type: llm.BlockText, Text: fmt.Sprintf("Skill attachment %q: fileId=%s, digest=%s. Use this fileId with the bound Skill tools.", name, upload.FileID, upload.Digest)}
			fileBlocks = append(fileBlocks, note)
			current = append(current, note)
		}
		ext := strings.ToLower(filepath.Ext(name))
		if _, ok := attachImageExts[extToMime(ext)]; (ok || isImageExt(ext)) && !part.AsFile {
			block := llm.Block{Type: llm.BlockImage, ImageRef: rel, ImageName: name}
			handles = append(handles, tools.ImageHandle{Ref: rel, Name: name})
			imageBlocks = append(imageBlocks, block)
			byPath[key] = append(current, block)
			continue
		}
		kind := fileKindByExt(ext)
		block := llm.Block{
			Type: llm.BlockFile, FilePath: rel, FileName: name,
			FileSize: st.Size(), FileKind: kind,
		}
		fileBlocks = append(fileBlocks, block)
		byPath[key] = append(current, block)
	}

	return imageBlocks, fileBlocks, byPath, handles, nil
}

// Start kicks off one turn in the background and returns immediately.
//
// The returned run outlives this request by design: POST /messages and GET
// /stream are separate endpoints precisely so that the connection which started
// the work is not the only one that can watch it. Closing the browser mid-run
// leaves the agent running, which is the behaviour a coding agent needs — a
// reload should not abandon a half-finished edit.
//
// attachments are workspace-relative paths from the upload endpoint. They are
// validated before the lock is taken; describing the images is the run
// goroutine's first job, not this request's — a cold vision helper takes tens
// of seconds per image, and holding the POST (and the timeline) for that used
// to read as a frozen conversation.
//
// clientID is the sending client's own placeholder id, echoed back on
// EvUserMessage so an optimistic UI can reconcile its bubble with the settled
// event. It is passed through, never inspected, and never persisted.
func (s *Session) Start(prompt string, attachments []string, clientID string) (string, error) {
	return s.start(prompt, attachments, clientID, mcpobserve.Metadata{})
}

// StartObserved is Start for a run another observed call initiated (the MCP
// submit tool): the run continues the caller's trace instead of opening a new
// one, so the tool calls the model makes inside it correlate back to the
// inbound request that caused them. Only TraceID, ParentID and TaskID are
// taken from observed — SessionID, OwnerID and RunID are authoritative local
// facts this session assigns itself; a remote caller's claims about identity
// are never trusted. observed says nothing about the prompt's handling, and a
// zero Metadata makes StartObserved exactly Start.
func (s *Session) StartObserved(prompt string, attachments []string, clientID string, observed mcpobserve.Metadata) (string, error) {
	return s.start(prompt, attachments, clientID, observed)
}

func (s *Session) start(prompt string, attachments []string, clientID string, observed mcpobserve.Metadata) (string, error) {
	return s.startInput(prompt, attachments, clientID, observed, "")
}

func (s *Session) startInput(prompt string, attachments []string, clientID string, observed mcpobserve.Metadata, inboxID string) (string, error) {
	return s.startOrderedInput(prompt, attachments, nil, "", clientID, observed, inboxID)
}

// StartParts accepts only user data parts; legacy callers keep StartObserved.
func (s *Session) StartParts(prompt string, attachments []string, parts []MessageInputPart, layout, clientID string, observed mcpobserve.Metadata) (string, error) {
	return s.startOrderedInput(prompt, attachments, parts, layout, clientID, observed, "")
}

func (s *Session) startOrderedInput(prompt string, attachments []string, parts []MessageInputPart, layout, clientID string, observed mcpobserve.Metadata, inboxID string, capture ...func(*activeRun)) (string, error) {
	return s.startPreparedInput(prompt, attachments, parts, layout, clientID, observed, inboxID, nil, nil, capture...)
}

func (s *Session) startPreparedInput(prompt string, attachments []string, parts []MessageInputPart, layout, clientID string, observed mcpobserve.Metadata, inboxID string, preparationContext context.Context, prepare func() error, capture ...func(*activeRun)) (string, error) {
	var err error
	prompt, attachments, err = normalizeMessageInput(prompt, attachments, parts, layout)
	if err != nil {
		return "", err
	}
	// Configuration synchronization and activation use Session.mu -> Studio.mu order.
	if _, err := s.syncModelConfig(inboxID); err != nil {
		return "", err
	}
	if s.studioID != "" {
		if _, err := s.syncStudioSkillPolicy(inboxID); err != nil {
			return "", err
		}
		if _, err := s.syncStudioToolPolicy(inboxID); err != nil {
			return "", err
		}
	}
	if s.studioID != "" {
		release, err := s.mgr.studio.permit(s.studioID)
		if err != nil {
			return "", err
		}
		release()
		if !s.studioWeb && len(attachments) > 0 {
			return "", errors.New("此 Agent 暂不支持附件")
		}
	}
	if err := s.validateInputFiles(parts); err != nil {
		return "", fmt.Errorf("%w: %v", errMessageInputInvalid, err)
	}
	skillBlocks, skillObserved, armBrowser, err := s.prepareInputSkills(parts)
	if err != nil {
		return "", err
	}
	if observed.TraceID == "" && skillObserved.TraceID != "" {
		observed = skillObserved
	}
	imageBlocks, fileBlocks, byPath, handles, err := s.prepareInputAttachments(attachments, parts)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return "", ErrNotFound
	}
	if s.active != nil || s.compacting || (s.inboxStarting != "" && s.inboxStarting != inboxID) {
		s.mu.Unlock()
		return "", ErrRunActive
	}
	// A previous write failure may have left a suffix only in memory. Resolve
	// it before opening a new incremental writer, whose count must be a prefix.
	if err := s.persistMessagesLocked(s.agent.Messages()); err != nil {
		s.mu.Unlock()
		return "", fmt.Errorf("failed to persist previous run: %w", err)
	}
	if err := s.recordCostLocked(s.agent.Messages()); err != nil {
		s.mu.Unlock()
		return "", fmt.Errorf("failed to persist previous usage: %w", err)
	}
	// Auto-compaction, the browser twin of the terminal's pre-run check: the
	// same between-runs spot (nothing active — checked above), landing on the
	// same Session.Compact the /compact button uses, so the branch, the events
	// and the persistence are the ones the UI already knows how to render.
	// Run before the run is registered rather than inside it: compaction holds
	// a summarising call, and a run in flight would have to pay for it twice —
	// once in latency, once in the persister's counting. The small race with a
	// concurrent second Start is closed on the far side: Session.Compact
	// refuses when a run has appeared by the time it applies, so the worst case
	// is one wasted summary, never a double application.
	fails := s.compactFails
	due := s.agent.CompactThreshold() > 0 && fails < compactCircuit && s.agent.ShouldCompact()
	s.mu.Unlock()
	if preparationContext != nil && preparationContext.Err() != nil {
		return "", preparationContext.Err()
	}
	if prepare != nil {
		if err := prepare(); err != nil {
			return "", err
		}
	}
	if due {
		// RunTimeout bounds the summarising call the same way it bounds a run:
		// Background alone would make this the one model call in the process
		// with no ceiling.
		if preparationContext == nil {
			preparationContext = context.Background()
		}
		cctx, ccancel := context.WithTimeout(preparationContext, s.mgr.cfg.RunTimeout)
		_, cerr := s.CompactAuto(cctx)
		ccancel()
		s.mu.Lock()
		if cerr != nil {
			s.compactFails++
			fmt.Fprintf(os.Stderr, "session %s: auto-compaction failed (%v)\n", s.ID, cerr)
			if s.compactFails == compactCircuit {
				fmt.Fprintf(os.Stderr, "session %s: auto-compaction disabled after %d consecutive failures; the /compact button still works\n",
					s.ID, compactCircuit)
			}
		} else {
			s.compactFails = 0
		}
		s.mu.Unlock()
	}
	if preparationContext != nil && preparationContext.Err() != nil {
		return "", preparationContext.Err()
	}
	// An administrator may have changed model policy while automatic
	// compaction was running. Apply it before activating the new run.
	if due {
		if _, err := s.syncModelConfig(inboxID); err != nil {
			return "", err
		}
	}
	if due && s.studioID != "" {
		if _, err := s.syncStudioSkillPolicy(inboxID); err != nil {
			return "", err
		}
		if _, err := s.syncStudioToolPolicy(inboxID); err != nil {
			return "", err
		}
	}

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return "", ErrNotFound
	}
	if s.active != nil || s.compacting || (s.inboxStarting != "" && s.inboxStarting != inboxID) {
		s.mu.Unlock()
		return "", ErrRunActive
	}
	runID := randomID(4)
	if isShellNotificationID(inboxID) && !s.shellNotificationPendingLocked(inboxID) {
		s.mu.Unlock()
		return "", errShellNotificationSettled
	}
	if inboxID != "" && !isShellNotificationID(inboxID) {
		i := s.inboxIndexLocked(inboxID)
		if i < 0 || s.inbox[i].Status != "steering" {
			s.mu.Unlock()
			return "", errInboxConflict
		}
		old := s.inbox[i]
		s.inbox[i].RunID = runID
		s.inbox[i].Revision++
		s.inbox[i].UpdatedAt = nowMS()
		if err := s.saveInboxLocked(); err != nil {
			s.inbox[i] = old
			s.mu.Unlock()
			return "", err
		}
		s.publishInboxLocked(s.inbox[i])
	}
	// Recheck availability at the actual activation point. Keep the registry
	// read lease only until active is visible, never while waiting for mu or
	// while compacting/preparing; disable/delete writers then see and cancel it.
	releaseActivation := func() {}
	if s.studioID != "" {
		var err error
		releaseActivation, err = s.mgr.studio.permit(s.studioID)
		if err != nil {
			s.mu.Unlock()
			return "", err
		}
	}
	// The hard timeout is the ceiling that replaces "the client disconnected".
	// It reaches the HTTP request to the model and every bash child process
	// through the same context.
	runParent := preparationContext
	if runParent == nil {
		runParent = context.Background()
	}
	ctx, cancel := context.WithTimeout(runParent, s.mgr.cfg.RunTimeout)
	// Observation wiring rides the run context: a fresh trace per run unless an
	// observed caller (MCP submit) continued one, with the session's own IDs as
	// the authoritative correlation — never the caller's. The recorder is the
	// manager's current one, read at start; nil means observe nothing.
	traceID := observed.TraceID
	if traceID == "" {
		traceID = mcpobserve.NewID()
	}
	runMeta := mcpobserve.Metadata{
		TraceID: traceID, ParentID: observed.ParentID,
		ParentSpanID: observed.ParentSpanID, TraceFlags: observed.TraceFlags,
		TraceState: observed.TraceState, ClientID: observed.ClientID,
		SessionID: s.ID, RunID: runID, TaskID: observed.TaskID,
		AgentID: s.studioID, AgentName: s.studioName,
	}
	if s.owner != nil {
		runMeta.OwnerID = s.owner.SubjectID
	}
	// The run's origin: an observed caller (MCP submit) declares its own,
	// everything else is an ordinary pogo run.
	runMeta.Entrypoint = observed.Entrypoint
	if runMeta.Entrypoint == "" {
		runMeta.Entrypoint = "pogo"
	}
	ctx = mcpobserve.WithMetadata(ctx, runMeta)
	if rec := s.mgr.MCPRecorder(); rec != nil {
		ctx = mcpobserve.WithRecorder(ctx, rec)
	}
	// Skill observation rides the same run context: local usage is the
	// internal caller wherever the run originated. Discovery is recorded
	// after preparation, when this snapshot is handed to the agent loop.
	ctx = skillobserve.WithSource(ctx, "internal", "")
	if rec := s.mgr.SkillRecorder(); rec != nil {
		ctx = skillobserve.WithRecorder(ctx, rec)
	}
	run := &activeRun{id: runID, cancel: cancel, done: make(chan struct{}), preparing: true, model: s.cfg.Model}
	s.active = run
	if len(capture) > 0 && capture[0] != nil {
		capture[0](run)
	}
	// Only a host-dispatched event batch may expose watch_decide. Normal
	// conversations and scheduled executions never inherit that schema.
	if err := s.agent.SetSentinelEvent(run.wake != nil); err != nil {
		s.active = nil
		cancel()
		close(run.done)
		s.reconcileInboxLocked(s.agent.Messages(), "任务准备失败，消息尚未处理。")
		releaseActivation()
		s.mu.Unlock()
		return "", err
	}
	if s.inboxStarting == inboxID {
		s.inboxStarting = ""
	}
	s.lastUsed = time.Now()
	s.images = append(s.images, handles...)
	a, model, provider := s.agent, s.cfg.Model, s.cfg.Provider
	seesImages := s.cfg.SeesImages
	head := s.store.Head()
	releaseActivation()
	s.mu.Unlock()
	if armBrowser {
		s.Browser().Arm()
	}

	// The first message: images before the text (every provider's preference),
	// file notes after the images, and no text block at all for an
	// attachment-only turn. The image alts are deliberately NOT filled yet:
	// the describe step runs in the goroutine, announced per image, and
	// mutates this slice in place before the message is persisted and run.
	blocks := append(append(imageBlocks, fileBlocks...), llm.Block{Type: llm.BlockText, Text: prompt})
	if prompt == "" {
		blocks = append(imageBlocks, fileBlocks...)
	}
	if parts != nil {
		blocks = orderedInputBlocks(parts, byPath, skillBlocks)
	}
	first := llm.Message{Role: llm.RoleUser, Content: blocks, InboxID: inboxID, RunID: runID, InputLayout: layout}
	first.Steering = isShellNotificationID(inboxID)
	if run.wake != nil {
		first.WakeID = run.wake.input.ID
	}

	// The persister writes the run to disk as it happens: the prompt once the
	// describe step has filled its alts (see the goroutine), then each turn as
	// its events pass through consume. A kill mid-run loses only the
	// in-flight turn; finish() reconciles whatever the persister failed on,
	// since persisted counts only landed appends. The append runs under s.mu,
	// the store's locking protocol — see SetInfo.
	persister := agent.NewTurnPersister(func(ms []llm.Message) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := s.store.AppendAll(ms); err != nil {
			return err
		}
		s.persisted += len(ms)
		return nil
	}, func(err error) {
		s.hub.Publish(Event{Type: EvError, Error: "incremental persist failed; the run is still saved when it ends: " + err.Error()})
	})

	// The checkpoint must land before the run's first write: it is the state
	// a later rewind to this prompt restores to, and the transcript head at
	// this moment is its name. Failure degrades to conversation-only rewind
	// for this point — it never blocks the run.
	if s.shadow != nil {
		if err := s.shadow.Checkpoint(head); err != nil {
			fmt.Fprintf(os.Stderr, "session %s: checkpoint failed: %v\n", s.ID, err)
		}
	}

	s.hub.Publish(Event{Type: EvRunStart, RunID: runID, Model: model, Provider: provider})
	// Published rather than merely appended, so a second tab sees the prompt
	// appear as it happens — now literally as it happens, before the describe
	// step. Content gets a copy of the blocks: the goroutine fills the image
	// alts in place, and the event (and the Hub's fold of it) must keep the
	// pre-describe shape. Text stays the prompt alone; ClientID lets the
	// sending client retire the optimistic placeholder it drew before this
	// round-trip.
	s.hub.Publish(Event{Type: EvUserMessage, Text: prompt, Content: append([]llm.Block(nil), blocks...), ClientID: clientID, InboxID: inboxID, RunID: runID, Steering: first.Steering, InputLayout: layout})

	go func() {
		defer close(run.done)
		defer cancel()
		if run.wake != nil {
			defer run.wake.finish()
		}
		// Step one for a text-only conversation model: each image gets the
		// description it will carry for the rest of the session. Announced per
		// image because a cold helper takes tens of seconds — an operation
		// that long owes the user a visible step, not a spinner-less wait.
		// Sequential on purpose: the helper may be one local model, and
		// parallel calls would fight over it. blocks is indexed directly so
		// the alts land in the slice first.Content actually holds.
		if len(imageBlocks) > 0 && !seesImages {
			for i := range blocks {
				if blocks[i].Type != llm.BlockImage {
					continue
				}
				ref, name := blocks[i].ImageRef, blocks[i].ImageName
				s.hub.Publish(Event{Type: EvImageDescribeStart, ImageRef: ref, ImageName: name})
				started := time.Now()
				text, ok := s.mgr.describeImage(ctx, s.cwd, ref)
				end := Event{
					Type: EvImageDescribeEnd, ImageRef: ref, ImageName: name,
					DurationMS: time.Since(started).Milliseconds(),
				}
				if ok {
					blocks[i].ImageAlt = text
					end.Text = text
				} else {
					blocks[i].ImageAlt = "（视觉服务当前不可用，可尝试 analyze_image 工具）"
					end.Error = "视觉服务不可用或未配置"
				}
				s.hub.Publish(end)
			}
		}
		persister.Add(first)
		if !persister.Failed() && !session.IsHarnessNotice(first.Text()) {
			s.rearmShellNotifications()
		}
		if run.wake != nil {
			var admissionErr error
			if persister.Failed() {
				admissionErr = errors.New("哨兵输入保存失败，未启动模型")
			} else {
				admissionErr = run.wake.admitted()
			}
			if admissionErr != nil {
				if !persister.Failed() {
					s.mu.Lock()
					a.SetMessages(append(a.Messages(), first))
					s.mu.Unlock()
				}
				run.persistErr = admissionErr
				s.hub.Publish(Event{Type: EvRunEnd, EndReason: "aborted", Error: admissionErr.Error()})
				s.finish()
				return
			}
		}
		if inboxID != "" {
			if persister.Failed() {
				if isShellNotificationID(inboxID) {
					s.holdShellNotifications()
				}
				s.hub.Publish(Event{Type: EvRunEnd, StopReason: "aborted", EndReason: "aborted", Error: "消息保存失败，未开始执行。"})
				s.holdQueuedInputs("执行未完成，待处理消息需要点击发送。")
				s.finish()
				return
			}
			if err := s.markInboxDelivered(first); err != nil {
				// Add succeeded but the receipt did not. Keep the agent's history
				// aligned with its transcript without making a provider call.
				a.SetMessages(append(a.Messages(), first))
				s.hub.Publish(Event{Type: EvRunEnd, StopReason: "aborted", EndReason: "aborted", Error: "收件状态保存失败，未开始执行。"})
				s.holdQueuedInputs("执行未完成，待处理消息需要点击发送。")
				s.finish()
				return
			}
		}
		// active reserves the session while the bounded network check runs.
		// The model and executor then use this one snapshot for the whole run.
		s.refreshMCP(ctx)
		s.mu.Lock()
		if ctx.Err() == nil {
			s.recordSkillDiscovery(ctx)
		}
		events := a.RunMessageWithPending(ctx, first, run.steering)
		run.preparing, run.steering = false, nil
		s.mu.Unlock()
		completed := s.consume(events, persister, run.observe)
		if !completed || ctx.Err() != nil {
			s.holdQueuedInputs("执行未完成，待处理消息需要点击发送。")
		}
		// The browser task gate closes with the run that opened it: page
		// operations are granted per task, and the next ordinary prompt must
		// find them closed without anyone remembering to close them.
		s.browser.Disarm()
		s.finish()
		if completed && ctx.Err() == nil {
			s.startNextQueuedInput()
			if s.userInput != nil {
				s.userInput.recoverAnswers()
			}
		}
	}()
	return runID, nil
}

// consume forwards loop events to the Hub, feeding the persister first: a
// message enters the transcript at the moment it enters the conversation, not
// at the end of the run. This is the third consumer of the same event stream,
// after the terminal renderer and the tests.
func (s *Session) consume(events <-chan agent.Event, persister *agent.TurnPersister, observe ...func(Event)) bool {
	ended := false
	completed := false
	for e := range events {
		persister.OnEvent(e)
		if e.SteerAck != nil && persister.Failed() {
			if isShellNotificationID(e.Message.InboxID) {
				s.holdShellNotifications()
			}
			e.SteerAck <- errors.New("引导消息保存失败，已停止执行")
			continue
		}
		carryOverhead := false
		var overheadTotal int64
		var overheadParts []agent.OverheadPart
		if e.Kind == agent.EventTurnStart {
			// Deferred MCP declarations and Skill-bound groups can change
			// between model requests, and refreshes at the run boundary update
			// the hub without an event. Keep the context breakdown aligned
			// with the current view, and carry it to connected clients
			// whenever the stream has not delivered this figure yet.
			overheadTotal, overheadParts = s.agent.OverheadBreakdown()
			carryOverhead = s.hub.OverheadUnstreamed(overheadTotal, overheadParts)
		}
		we, ok := fromAgentEvent(e)
		if !ok {
			continue
		}
		if carryOverhead && we.Type == EvTurnStart {
			we.OverheadTokens, we.OverheadBreakdown = &overheadTotal, overheadParts
		}
		if we.Type == EvRunEnd && s.hub.HasCompactions() {
			we.ContextPrompt = newContextPrompt(s.agent.Messages(), s.agent.OverheadTokens())
		}
		if len(observe) > 0 && observe[0] != nil {
			observe[0](we)
		}
		s.hub.Publish(we)
		var deliveryErr error
		if e.Kind == agent.EventSteer && e.Message.InboxID != "" {
			deliveryErr = s.markInboxDelivered(e.Message)
		}
		if e.SteerAck != nil {
			e.SteerAck <- deliveryErr
		}
		switch we.Type {
		case EvTurnStart:
			s.spendAutoTurn()
		case EvRunEnd:
			ended = true
			completed = we.EndReason == string(agent.EndCompleted) && we.Error == "" && !persister.Failed()
			// A completion notification that was steered but never delivered
			// must not die with the run: re-queue it for the idle path, which
			// opens a fresh run for it instead.
			for _, u := range we.Undelivered {
				if strings.HasPrefix(u, bgNotificationMarker) {
					s.queueBgNotification(u)
				}
			}
			// Follow-up suggestions generate off this goroutine: finish() is
			// next and must not wait on a model call. The event, not the return
			// of consume, is the hook so the chips can only ever describe a run
			// that announced its own end.
			s.maybeFollowups(we)
			// Memory distillation rides the same hook for the same reason:
			// its model call must not delay finish() either.
			s.maybeDistillMemory(we)
		}
	}
	if !ended {
		// Belt and braces. The loop always ends with one, but a client that never
		// sees run_end would show a turn in progress forever, and a stuck spinner
		// is a worse failure than a slightly redundant event.
		s.hub.Publish(Event{Type: EvRunEnd, StopReason: "aborted"})
	}
	return completed
}

// spendAutoTurn counts down a turn-limited auto mode and announces the reversion.
// Letting it lapse silently is the failure this exists to prevent: the gate would
// re-arm without anything on screen changing.
func (s *Session) spendAutoTurn() {
	from, state, reverted := s.policy.TurnStarted()
	if !reverted {
		return
	}
	s.hub.Publish(Event{
		Type: EvPolicyReverted, Policy: &state,
		From: string(from), To: state.Mode, Reason: "the auto-mode turn budget ran out",
	})
}

// finish persists whatever the run added and clears the active slot.
//
// Reading the agent's messages here is safe because the event channel is closed,
// which happens after the loop's final append. Doing it mid-run would be a data
// race, which is why persistence is per-run rather than per-turn.
func (s *Session) finish() {
	s.mu.Lock()
	run := s.active
	msgs := s.agent.Messages()
	err := s.persistMessagesLocked(msgs)
	// Recorded here for the same reason the messages are: the channel is closed, so
	// reading the agent's counters is safe on this goroutine and nowhere else.
	if err == nil {
		err = s.recordCostLocked(msgs)
	}
	err = errors.Join(err, s.agent.SetSentinelEvent(false))
	if run != nil {
		run.persistErr = errors.Join(run.persistErr, err)
	}
	s.releaseShellNotificationsLocked()
	s.active = nil
	s.reconcileInboxLocked(msgs, "本次执行已结束，消息尚未处理。")
	s.lastUsed = time.Now()
	s.mu.Unlock()

	if err != nil {
		s.hub.Publish(Event{Type: EvError, Error: "failed to persist session: " + err.Error()})
	}
	_, _ = s.SyncModelConfig()
	if s.studioID != "" {
		_, _ = s.SyncStudioSkillPolicy()
		_, _ = s.SyncStudioToolPolicy()
	}
	s.resumeShellNotifications()
}

func (s *Session) persistMessagesLocked(msgs []llm.Message) error {
	if len(msgs) <= s.persisted {
		return nil
	}
	if err := s.store.AppendAll(msgs[s.persisted:]); err != nil {
		return err
	}
	s.persisted = len(msgs)
	return nil
}

// recordCostLocked writes the tokens spent since the last such record, and must be
// called with s.mu held and no run in flight — it reads the agent's counters, which
// the run goroutine writes without a lock.
//
// Shared by finish and Compact rather than copied into both, because they both move
// s.recorded: a second copy of this would eventually double-count a call or skip
// one, and the symptom would be a cost report that is quietly wrong rather than a
// failure anyone notices. The CLI keeps one such closure for the same reason.
func (s *Session) recordCostLocked(msgs []llm.Message) error {
	nextRecorded := s.recorded
	st, ok := session.UsageDelta(&nextRecorded, s.agent.Usage(), s.agent.Delegated())
	if !ok {
		return nil
	}
	// A snapshot rather than a delta, so it is attached here rather than threaded
	// through UsageDelta; see the matching note in main.go.
	comp := session.Compose(msgs, s.agent.OverheadTokens())
	comp.Measured = s.agent.LastInput()
	comp.Cleared = s.agent.ClearedFromPrompt()
	st.Composition = &comp
	if err := s.store.AppendStats(st); err != nil {
		return err
	}
	s.recorded = nextRecorded
	return nil
}

// errMessageUnknown is rewind's not-found: the timeline id the client sent is
// not a user message this session still has.
var errMessageUnknown = errors.New("message not found on the timeline")

// errFilesUnavailable is rewind's "you asked for file restoration but this
// point has no checkpoint": the snapshot predates the feature, or it failed
// to land when the run started. The conversation can still rewind; the files
// cannot follow.
var errFilesUnavailable = errors.New("no file checkpoint for that point")

// RewindPreview reports what restoring files for a rewind to messageID would
// do, without touching anything. available is false when the point has no
// checkpoint to restore to.
func (s *Session) RewindPreview(messageID string) (changes []FileChange, available bool, err error) {
	if s.shadow == nil {
		return nil, false, nil
	}
	k, ok := s.hub.UserMessageOrdinal(messageID)
	if !ok {
		return nil, false, errMessageUnknown
	}
	s.mu.Lock()
	point, ok := s.store.RewindPoint(k)
	s.mu.Unlock()
	if !ok {
		return nil, false, errMessageUnknown
	}
	changes, available = s.shadow.Preview(point)
	return changes, available, nil
}

// Rewind abandons the branch from a user message onwards: the message itself,
// every turn that answered it, and everything after, all become unreachable
// from the session's head. Nothing is deleted — the transcript is append-only,
// so the abandoned records stay in the file as an unreachable branch — but the
// agent, the usage counters and every client's next snapshot all continue
// from the fork. A run in flight blocks it, the same way it blocks SetModel:
// the loop would go on appending to a branch nobody can see.
//
// mode chooses what the rewind acts on. RewindBoth restores the workspace to the
// checkpoint taken when that message was sent — modified files go back, deleted
// ones return, ones created afterwards are removed — and then forks. The restore
// runs before the fork and under the same lock, so that rewind is all-or-nothing:
// a failed restore leaves the conversation alone, and no run can slip into the
// gap between the two.
//
// RewindFiles is not a weaker RewindBoth, it is a different operation: put the
// files back, leave the conversation where it is. Claude Code's rewind menu and
// Roo's restore dialog both offer it, and the reason is that the two things a
// person wants to undo are not always the same thing — a turn whose reasoning was
// useful but whose edit was wrong is the common case. All-or-nothing still holds
// within each mode; it was never a claim that the two halves cannot be asked for
// separately.
//
// paths narrows a file restore to a subset. Empty means the whole checkpoint.
func (s *Session) Rewind(messageID string, mode RewindMode, paths []string) error {
	switch mode {
	case RewindChat, RewindFiles, RewindBoth:
	default:
		return fmt.Errorf("%w: %q (want chat, files or both)", errRewindMode, mode)
	}
	k, ok := s.hub.UserMessageOrdinal(messageID)
	if !ok {
		return errMessageUnknown
	}

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return ErrNotFound
	}
	if s.active != nil || s.compacting || (s.shellJobs != nil && s.shellJobs.InFlight() > 0) {
		s.mu.Unlock()
		return ErrRunActive
	}
	if err := s.settleShellReceiptsLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	point, ok := s.store.RewindPoint(k)
	if !ok {
		// The hub and the store count by the same rule, so this is a drift
		// bug, not user error — but a failed rewind must change nothing.
		s.mu.Unlock()
		return errMessageUnknown
	}
	if mode == RewindFiles || mode == RewindBoth {
		if s.shadow == nil {
			s.mu.Unlock()
			return errFilesUnavailable
		}
		if err := s.shadow.RestorePaths(point, paths); err != nil {
			s.mu.Unlock()
			if errors.Is(err, errNoCheckpoint) {
				return errFilesUnavailable
			}
			return err
		}
	}
	if mode == RewindFiles {
		// Nothing below applies: the transcript did not move, so the agent's
		// messages, the usage totals and the persistence baselines are all still
		// correct. Touching them here would be the bug this early return exists
		// to avoid — SetMessages on an unchanged history would re-append the
		// whole branch on the next finish().
		s.lastUsed = time.Now()
		s.mu.Unlock()
		return nil
	}
	if err := s.store.Fork(point); err != nil {
		s.mu.Unlock()
		return err
	}
	history := s.store.TimedMessages("")
	promptHistory := s.store.PromptTimedMessages("")
	usage, delegated := s.store.UsageTotals()
	msgs := make([]llm.Message, 0, len(promptHistory))
	for _, t := range promptHistory {
		msgs = append(msgs, t.Message)
	}
	s.agent.SetMessages(msgs)
	s.agent.SetUsage(usage, delegated)
	// The persistence baselines move with the agent: without that the next
	// finish() would append the whole surviving branch as if it were new, and
	// UsageDelta would re-record the surviving turns' tokens as a delta.
	s.persisted = len(msgs)
	s.recorded = session.Recorded{Usage: usage, Delegated: delegated}
	s.lastUsed = time.Now()
	// The tool view followed the surviving branch (a Skill loaded or a tool
	// selected after the rewind point is gone with it), and so does its cost.
	s.hub.SetOverhead(s.agent.OverheadBreakdown())
	s.mu.Unlock()

	// The fork does NOT sweep the uploads only the abandoned branch
	// referenced. Attachments are message-bound, not workspace state: the
	// composer's draft rides back as path chips reusing the same bytes, so a
	// re-send uploads nothing and the rewind itself waits on nothing. The
	// files' cleanup is the session's own delete (gcSessionUploads), the same
	// as every other upload.

	// Reset before publishing, so the event only ever meets rebuilt state.
	s.hub.Reset(history, usage)
	s.hub.SetCompactions(s.store.Compactions(""))
	if s.hub.HasCompactions() {
		s.hub.EstimateContext(newContextPrompt(msgs, s.agent.OverheadTokens()))
	}
	s.hub.Publish(Event{Type: EvRewound})
	return nil
}

// Compact replaces model context with a summary and appends a checkpoint to the
// visible transcript. Unlike Rewind it never forks or hides real messages.
//
// The summarising network call cannot run under s.mu — the snapshot endpoint takes
// that lock and the browser would freeze. A compacting reservation blocks competing
// mutations while snapshots remain readable. The commit hook persists the checkpoint
// before Agent swaps context; failures leave both the transcript and context unchanged.
func (s *Session) Compact(ctx context.Context) (agent.CompactResult, error) {
	return s.compact(ctx, 0, "")
}

func (s *Session) CompactWithInstructions(ctx context.Context, instructions string) (agent.CompactResult, error) {
	return s.compact(ctx, 0, instructions)
}

// CompactAuto is the pre-run check's shape: the same summary of the head plus a
// verbatim tail (agent.CompactKeepRecent), for the same reason the terminal's
// automatic path keeps one — the /compact button is a trade a person chose, a
// threshold is not, and the automatic path errs on keeping more.
func (s *Session) CompactAuto(ctx context.Context) (agent.CompactResult, error) {
	return s.compact(ctx, agent.CompactKeepRecent, "")
}

func (s *Session) compact(ctx context.Context, keep int64, instructions string) (agent.CompactResult, error) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return agent.CompactResult{}, ErrNotFound
	}
	if s.active != nil || s.compacting {
		s.mu.Unlock()
		return agent.CompactResult{}, ErrRunActive
	}
	releaseActivation := func() {}
	if keep > 0 && s.studioID != "" {
		var err error
		releaseActivation, err = s.mgr.studio.permit(s.studioID)
		if err != nil {
			s.mu.Unlock()
			return agent.CompactResult{}, err
		}
	}
	if err := s.persistMessagesLocked(s.agent.Messages()); err != nil {
		releaseActivation()
		s.mu.Unlock()
		return agent.CompactResult{}, err
	}
	if err := s.recordCostLocked(s.agent.Messages()); err != nil {
		releaseActivation()
		s.mu.Unlock()
		return agent.CompactResult{}, err
	}
	if err := s.settleShellReceiptsLocked(); err != nil {
		releaseActivation()
		s.mu.Unlock()
		return agent.CompactResult{}, err
	}
	a := s.agent
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.compacting, s.compactCancel, s.compactDone = true, cancel, done
	releaseActivation()
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		s.compacting, s.compactCancel, s.compactDone = false, nil, nil
		close(done)
		s.mu.Unlock()
		// Manual compaction does not pass through finish(). It is still an idle
		// boundary at which a model update deferred during the summary applies.
		_, _ = s.SyncModelConfig()
		if s.studioID != "" {
			_, _ = s.SyncStudioSkillPolicy()
			_, _ = s.SyncStudioToolPolicy()
		}
	}()
	before := a.Messages()
	beforeTokens := llm.EstimateTokens(before)
	startedAt := time.Now().UnixMilli()
	var compaction session.CompactionView
	res, err := a.CompactWithCommitInstructions(ctx, keep, func(msgs []llm.Message) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closing {
			return ErrNotFound
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		summary := ""
		if len(msgs) > 0 {
			summary, _ = agent.CompactionContinuationSummary(msgs[0])
		}
		var err error
		compaction, err = s.store.AppendCompaction(session.Compaction{
			Summary: summary, Instructions: instructions,
			Before: len(before), BeforeTokens: beforeTokens, AfterTokens: llm.EstimateTokens(msgs),
			StartedAt: startedAt, FinishedAt: time.Now().UnixMilli(), Messages: msgs,
		})
		return err
	}, instructions)
	s.mu.Lock()
	msgs := a.Messages()
	if err != nil {
		// Even a rejected or cancelled summary incurred usage. Keep its cursor
		// retryable if recording fails; the conversation is still unchanged.
		costErr := s.recordCostLocked(msgs)
		s.mu.Unlock()
		return res, errors.Join(err, costErr)
	}
	// The persistence baseline follows model context; new real messages still append
	// to the complete visible transcript after its checkpoint.
	s.persisted = len(msgs)
	// The summarising call is spend that already happened, so it is recorded now
	// rather than left for the next finish(): a session compacted and then closed or
	// evicted would otherwise lose it. The CLI's /compact does the same.
	costErr := s.recordCostLocked(msgs)
	s.lastUsed = time.Now()
	// The tool view follows the compacted context: restored Skills keep their
	// tools, selections whose describe results were summarised are gone.
	s.hub.SetOverhead(a.OverheadBreakdown())
	s.mu.Unlock()

	s.hub.Publish(Event{Type: EvCompacted, Compaction: &compaction, ContextPrompt: newContextPrompt(msgs, a.OverheadTokens())})
	return res, costErr
}

// Steer delivers a message into the run that is in flight. It reports whether
// there was one to deliver.
//
// The publish happens in the loop, not here: the message enters the conversation
// at a turn boundary the loop chooses, and putting it on the timeline any earlier
// would show it in a place the model never saw it.
func (s *Session) Steer(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	s.mu.Lock()
	if s.closing || s.compacting {
		s.mu.Unlock()
		return false
	}
	if s.active != nil && s.active.preparing {
		s.active.steering = append(s.active.steering, llm.UserText(text))
		s.mu.Unlock()
		return true
	}
	a := s.agent
	s.mu.Unlock()
	// The agent's own lock decides, so there is no window between "is a run
	// active" and "accept this message".
	return a.Steer(text)
}

// Cancel stops the current run. It reports whether there was one.
//
// Cancellation goes through the run's context, so it reaches the in-flight HTTP
// request and any bash child process. That is the reason the UI cancels through
// this endpoint instead of aborting its own SSE connection: dropping a socket
// leaves `go test` running.
func (s *Session) Cancel() bool {
	s.mu.Lock()
	run := s.active
	compactCancel := s.compactCancel
	cancelledInput := false
	if run == nil && s.inboxStarting != "" {
		cancelledInput = s.cancelStartingInputLocked()
	}
	s.mu.Unlock()
	if run == nil {
		if compactCancel != nil {
			compactCancel()
			return true
		}
		return cancelledInput
	}
	run.cancel()
	return true
}

// Close ends this session's lifetime exactly once. Marking it first prevents a
// retained HTTP handler or a late task callback from starting more work while
// cancellation and persistence are draining.
func (s *Session) Close() {
	s.close(true)
}

// Constructors that were never published have no ownership of the shared
// transcript or conversation-wide Office identity. Release their private
// resources without impersonating the registered instance's final close.
func (s *Session) closeUnpublished() {
	s.close(false)
}

func (s *Session) close(published bool) {
	s.mu.Lock()
	if s.closeDone != nil {
		done := s.closeDone
		s.mu.Unlock()
		<-done
		return
	}
	done := make(chan struct{})
	s.closeDone, s.closing = done, true
	run, compactCancel, compactDone := s.active, s.compactCancel, s.compactDone
	s.mu.Unlock()
	if published && s.mgr != nil && s.mgr.officeRevokeSession != nil {
		s.mgr.officeRevokeSession(s.ID)
	}
	if run != nil {
		run.cancel()
	}
	if compactCancel != nil {
		compactCancel()
	}
	s.closeBg()
	s.closeShellJobs()
	if s.userInput != nil {
		s.userInput.Close()
	}
	if run != nil {
		<-run.done
	}
	if compactDone != nil {
		<-compactDone
	}
	if published {
		s.mu.Lock()
		err := s.persistMessagesLocked(s.agent.Messages())
		if err == nil {
			err = s.recordCostLocked(s.agent.Messages())
		}
		s.mu.Unlock()
		if err != nil {
			fmt.Fprintf(os.Stderr, "session %s: failed to persist while closing: %v\n", s.ID, err)
		}
	}
	s.closeTerminal()
	if s.nativeBrowser != nil {
		s.nativeBrowser.Close()
	}
	s.hub.Close()
	close(done)
}

func (s *Session) wait() {
	s.mu.Lock()
	run := s.active
	s.mu.Unlock()
	if run != nil {
		<-run.done
	}
}

// SetModel switches models between runs, keeping the transcript. Every provider
// here speaks the same wire format and thinking is not replayed, so there is
// nothing model-specific left to translate.
//
// A Studio conversation may switch too — inside its synchronized model-only
// policy. Outside it the switch is refused, and a
// legacy definition (no models field) has exactly its one default model in
// the set. A successful Studio switch is persisted on the binding so a
// restart resumes it.
//
// It refuses during a run: SetClient writes an Agent field with no
// synchronisation, and the value is read by the loop on every turn.
func (s *Session) SetModel(name string) (config.Resolved, error) {
	if s.studioID != "" {
		if _, err := s.SyncStudioModelPolicy(); err != nil {
			return config.Resolved{}, err
		}
	}
	cfg, err := config.Resolve(name)
	if err != nil {
		return config.Resolved{}, err
	}
	studio := s.studioID != ""
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return config.Resolved{}, ErrNotFound
	}
	if s.active != nil || s.compacting || s.inboxStarting != "" {
		return config.Resolved{}, ErrRunActive
	}
	if studio {
		// Model policy can synchronize between runs; read its allowlist under mu.
		allowed := false
		for _, m := range s.studioModels {
			if m == cfg.Model {
				allowed = true
				break
			}
		}
		if !allowed {
			return config.Resolved{}, fmt.Errorf("模型 %s 不在此 Agent 允许的模型列表中", cfg.Model)
		}
		cfg = applyStudioEffort(cfg)
	}

	// Validate the Agent's idle state without changing its current declaration.
	// Session.mu prevents a run or another model switch from starting between
	// this check, durable binding update and the in-memory commit below.
	if err := s.agent.SetBrowserModelVision(s.cfg.SeesImages); err != nil {
		return config.Resolved{}, err
	}
	if studio {
		// Reject busy/closing sessions before writing, and serialize persistence
		// with the full switch. A failed write changes none of the session views.
		if err := s.mgr.studio.setCurrentModel(s.ID, cfg.Model); err != nil {
			return config.Resolved{}, err
		}
	}
	if err := s.applyModelLocked(cfg); err != nil {
		return config.Resolved{}, err
	}
	return cfg, nil
}

// applyModelLocked changes only model runtime state, leaving tools, skills,
// instructions, approvals and transcript history untouched. Session.mu is held.
func (s *Session) applyModelLocked(cfg config.Resolved) error {
	if err := s.agent.SetDelegationParentModel(cfg.Model); err != nil {
		return err
	}
	if err := s.agent.SetBrowserModelVision(cfg.SeesImages); err != nil {
		return err
	}
	s.agent.SetClient(s.client(cfg))
	s.agent.SetPrice(cfg.Price)
	s.agent.SetToolOutputBudget(agent.ToolOutputBudgetConfig{ContextWindow: cfg.ContextWindow, MaxOutputTokens: cfg.MaxTokens})
	// The clearing threshold follows the model: "auto" is half its window, and the
	// catalogue spans 200K to 1M. A bad spec was rejected at startup, so the error
	// here can only be an unknown window, which ParseContextEdit answers with "off".
	editCfg, _ := agent.ParseContextEdit(s.mgr.cfg.ContextEdit, cfg.ContextWindow)
	s.agent.SetContextEdit(editCfg)
	// The compaction threshold travels with the model for the same reason, with
	// the same unknown-window fallback to manual.
	if compactCfg, err := agent.ParseCompactSpec(s.mgr.cfg.Compact, cfg.ContextWindow); err == nil {
		s.agent.SetCompact(compactCfg)
	}
	// The gauge measures its bands against the trigger, so it has to move with the
	// model too — otherwise switching from a 262K model to a 1M one leaves the browser
	// colouring against a threshold the session no longer uses.
	trigger := editCfg.Trigger
	overhead, overheadParts := s.agent.OverheadBreakdown()
	s.cfg = cfg
	s.modelConfigPending = false
	s.lastUsed = time.Now()

	// Publish under the same lock: concurrent successful switches must reach
	// the Hub in commit order, or its model view can regress to an older choice.
	s.hub.Publish(Event{
		Type: EvModelChanged, Model: cfg.Model, Provider: cfg.Provider,
		ContextWindow: cfg.ContextWindow,
		ClearTrigger:  &trigger, OverheadTokens: &overhead, OverheadBreakdown: overheadParts,
	})
	return nil
}

// SetPolicy changes the approval mode and broadcasts it, so a second tab sees the
// auto-mode banner immediately.
func (s *Session) SetPolicy(mode Mode, turns int) PolicyState {
	state := s.policy.Set(mode, turns)
	s.hub.Publish(Event{Type: EvPolicyChanged, Policy: &state, By: "user"})
	if mode == ModeAuto {
		// Cards already on screen predate the switch and would otherwise wait
		// out the timeout: Decide only gates future calls. Answer them too.
		s.gate.ApproveAll()
	}
	return state
}

// AllowTool and AllowCommand install session-scoped standing grants.
func (s *Session) AllowTool(name string) PolicyState {
	state := s.policy.AllowTool(name)
	s.hub.Publish(Event{Type: EvPolicyChanged, Policy: &state, By: "user"})
	return state
}

func (s *Session) AllowCommand(cmd string) PolicyState {
	state := s.policy.AllowCommand(cmd)
	s.hub.Publish(Event{Type: EvPolicyChanged, Policy: &state, By: "user"})
	return state
}

// Deliberately no Usage accessor here. One existed, took s.mu, and returned
// s.agent.Usage() — but s.mu does not guard the Agent's fields, and the loop
// writes its usage totals from the run goroutine with no lock at all. Nothing
// called it, so -race never saw the race that wiring it up would have created.
//
// The token numbers the UI needs already travel as events: per-turn usage on
// message, the accumulated total on run_end. Anyone adding an endpoint here
// should read agent.Agent.Usage's contract first.
