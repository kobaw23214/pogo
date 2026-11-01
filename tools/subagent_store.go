package tools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/session"
)

const envSubagentContextLeaseFD = "POGO_SUBAGENT_CONTEXT_LEASE_FD"

var errSubagentContextBusy = errors.New("this subagent is still running or queued; wait for its result before continuing or archiving")

var ErrSubagentNotFound = errors.New("unknown subagent in this parent session")

// SubagentStore retains logical children, not live processes. The host supplies
// the parent and owner; neither transcript paths nor ownership are tool inputs.
// A per-child OS lease covers queued and running work, including the child
// process after a parent crash. Separate stores/processes therefore cannot
// append to the same transcript concurrently.
type SubagentStore struct {
	dir, sessionDir, parent, owner string
}

type SubagentInfo struct {
	AgentID       string `json:"agent_id"`
	Mode          string `json:"mode"`
	Model         string `json:"model"`
	Status        string `json:"status"`
	Session       string `json:"session,omitempty"`
	RunID         string `json:"run_id,omitempty"`
	LastRunStatus string `json:"last_run_status,omitempty"`
	Task          string `json:"task,omitempty"`
	Result        string `json:"result,omitempty"`
	Error         string `json:"error,omitempty"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

// SubagentListDetails is deliberately distinct from SubagentDetails: listing
// retained contexts incurs no delegated usage and is not a new execution.
type SubagentListDetails struct {
	Agents []SubagentInfo `json:"agents"`
}

type subagentRecord struct {
	SubagentInfo
	Version     int    `json:"version"`
	Parent      string `json:"parent_session_id"`
	Owner       string `json:"owner_key"`
	Permissions string `json:"permissions"`
	Cwd         string `json:"cwd"`
}

// NewSubagentStore does not resume work. Busy records whose OS lease no longer
// exists become interrupted on recovery and require an explicit continue.
func NewSubagentStore(sessionDir, parentSessionID, ownerKey string) (*SubagentStore, error) {
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(sessionDir) == "" {
		return nil, errors.New("retained subagents require a parent session and session directory")
	}
	root, err := filepath.Abs(sessionDir)
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256([]byte(ownerKey + "\x00" + parentSessionID))
	dir := filepath.Join(root, session.SubagentDirName, "agents", hex.EncodeToString(key[:]))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create retained subagent registry: %w", err)
	}
	store := &SubagentStore{dir: dir, sessionDir: root, parent: parentSessionID, owner: ownerKey}
	if _, err := store.list(); err != nil {
		return nil, fmt.Errorf("recover retained subagent registry: %w", err)
	}
	return store, nil
}

func newAgentID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "agent_" + hex.EncodeToString(b[:]), nil
}

func validAgentID(id string) bool {
	if len(id) != len("agent_")+32 || !strings.HasPrefix(id, "agent_") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "agent_"))
	return err == nil
}

func (s *SubagentStore) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *SubagentStore) lock(id string) (*os.File, error) {
	if !validAgentID(id) {
		return nil, ErrSubagentNotFound
	}
	return trySubagentContextLease(filepath.Join(s.dir, id+".lock"))
}

func (s *SubagentStore) read(id string) (subagentRecord, error) {
	var r subagentRecord
	if !validAgentID(id) {
		return r, ErrSubagentNotFound
	}
	b, err := os.ReadFile(s.path(id))
	if err != nil {
		return r, ErrSubagentNotFound
	}
	if err := json.Unmarshal(b, &r); err != nil || r.Version != 1 || r.AgentID != id || r.Parent != s.parent || r.Owner != s.owner {
		return r, errors.New("retained subagent ownership or record is invalid")
	}
	if filepath.Dir(r.Session) != filepath.Join(s.sessionDir, session.SubagentDirName) || filepath.Ext(r.Session) != ".jsonl" {
		return r, errors.New("retained subagent transcript is outside its session directory")
	}
	info, err := os.Lstat(r.Session)
	if err != nil || !info.Mode().IsRegular() {
		return r, errors.New("retained subagent transcript must be an existing regular file")
	}
	return r, nil
}

// Lookup is an owner/parent-scoped read. It neither adopts the transcript as a
// top-level session nor starts or resumes its process. An active transcript may
// continue appending; session.Open exposes only complete committed records.
func (s *SubagentStore) Lookup(agentID string) (SubagentInfo, error) {
	if s == nil {
		return SubagentInfo{}, ErrSubagentNotFound
	}
	r, err := s.read(agentID)
	return r.SubagentInfo, err
}

func (s *SubagentStore) write(r subagentRecord) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".agent-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(f.Name(), s.path(r.AgentID)); err != nil {
		return err
	}
	dir, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *SubagentStore) list() ([]SubagentInfo, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := make([]SubagentInfo, 0)
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".json")
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || !validAgentID(id) {
			continue
		}
		lease, lerr := s.lock(id)
		if lerr != nil && !errors.Is(lerr, errSubagentContextBusy) {
			return nil, lerr
		}
		r, err := s.read(id)
		if err == nil && lease != nil && (r.Status == "running" || r.Status == "queued") {
			r.Status, r.LastRunStatus, r.UpdatedAt = "interrupted", "interrupted", time.Now().UnixMilli()
			r.Error = "The previous process ended before reporting completion. Inspect its transcript and partial outputs before continuing."
			err = s.write(r)
		}
		if lease != nil {
			lease.Close()
		}
		if err != nil {
			return nil, err
		}
		out = append(out, r.SubagentInfo)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

func (s *SubagentStore) archive(id string) (SubagentInfo, error) {
	lease, err := s.lock(id)
	if err != nil {
		return SubagentInfo{}, err
	}
	defer lease.Close()
	r, err := s.read(id)
	if err != nil {
		return SubagentInfo{}, err
	}
	r.Status, r.UpdatedAt = "archived", time.Now().UnixMilli()
	return r.SubagentInfo, s.write(r)
}

// Permission changes never silently broaden a retained child. A new delegation
// can adopt changed capabilities; continuation retains the original boundary.
func (s *Subagent) permissionKey(mode string) string {
	v := struct {
		Cwd, Mode, ReadRoots          string
		InformationRoot               string `json:",omitempty"`
		InformationScope              string `json:",omitempty"`
		Skills, SkillRoots, ReadTools []string
		NativeTools                   *[]string `json:",omitempty"`
		Explicit, Browser, Gate       bool
	}{s.Cwd, mode, os.Getenv(EnvReadRoots), s.InformationRoot, s.InformationScope, s.SkillPaths, s.SkillReadRoots, s.ReadOnlySkillTools, s.Enabled, s.ExplicitSkills, s.Browser != nil, s.Review != nil}
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type subagentRun struct {
	store               *SubagentStore
	record              subagentRecord
	lease               *os.File
	provider            string
	resumed             bool
	beforeIn, beforeOut int64
	once                sync.Once
	result              Result
	err                 error
}

func (s *Subagent) prepareRetained(a subagentArgs) (*subagentRun, error) {
	store := s.Store
	var r subagentRecord
	var lease *os.File
	var err error
	provider := ""
	if a.Action == "continue" {
		lease, err = store.lock(a.AgentID)
		if err != nil {
			return nil, err
		}
		r, err = store.read(a.AgentID)
		if err == nil && r.Status == "archived" {
			err = errors.New("this subagent is archived; start a new delegation")
		}
		if err == nil && a.Mode != "" && a.Mode != r.Mode {
			err = errors.New("continuation cannot change a subagent's permission mode")
		}
		if err == nil && (r.Cwd != s.Cwd || r.Permissions != s.permissionKey(r.Mode)) {
			err = errors.New("this subagent's workspace or capabilities have changed; start a new delegation")
		}
		if err != nil {
			lease.Close()
			return nil, err
		}
		// Continuation pins its model rather than asking the pool to choose a
		// different one, but still counts against that provider's in-flight load.
		if r.Mode == ModeExplore {
			for _, target := range s.ExplorePool {
				if target.Model == r.Model {
					provider = target.Provider
					s.exploreMu.Lock()
					if s.exploreInflight == nil {
						s.exploreInflight = make(map[string]int)
					}
					s.exploreInflight[provider]++
					s.exploreMu.Unlock()
					break
				}
			}
		}
	} else {
		id, e := newAgentID()
		if e != nil {
			return nil, e
		}
		lease, err = store.lock(id)
		if err != nil {
			return nil, err
		}
		var model string
		model, provider = s.pickDelegationModel(a.Mode)
		transcript, e := session.CreateMultiOwner(filepath.Join(store.sessionDir, session.SubagentDirName), s.Cwd, model)
		if e != nil {
			lease.Close()
			s.releaseExplore(provider)
			return nil, e
		}
		now := time.Now().UnixMilli()
		r = subagentRecord{Version: 1, Parent: store.parent, Owner: store.owner, Cwd: s.Cwd, Permissions: s.permissionKey(a.Mode), SubagentInfo: SubagentInfo{AgentID: id, Mode: a.Mode, Model: model, Session: transcript.Path(), CreatedAt: now}}
	}
	transcript, err := session.Open(r.Session)
	if err != nil {
		lease.Close()
		s.releaseExplore(provider)
		return nil, fmt.Errorf("open retained subagent transcript: %w", err)
	}
	meta := transcript.Meta()
	if meta == nil || meta.Cwd != r.Cwd || meta.Model != r.Model {
		lease.Close()
		s.releaseExplore(provider)
		return nil, errors.New("retained subagent transcript no longer matches its execution boundary")
	}
	usage, _ := transcript.UsageTotals()
	runID, err := newAgentID()
	if err != nil {
		lease.Close()
		s.releaseExplore(provider)
		return nil, err
	}
	r.RunID, r.Status, r.Task, r.UpdatedAt = "run_"+strings.TrimPrefix(runID, "agent_"), "queued", clipSubagent(a.Task, 500), time.Now().UnixMilli()
	r.Error = ""
	if err := store.write(r); err != nil {
		lease.Close()
		s.releaseExplore(provider)
		return nil, err
	}
	return &subagentRun{store: store, record: r, lease: lease, provider: provider, resumed: a.Action == "continue", beforeIn: usage.Input, beforeOut: usage.Output}, nil
}

func clipSubagent(v string, n int) string {
	r := []rune(v)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return v
}

func (s *Subagent) executeRetained(ctx context.Context, a subagentArgs, onPartial func(Partial)) (Result, error) {
	if a.Action == "" {
		a.Action = "spawn"
	}
	switch a.Action {
	case "list":
		list, err := s.Store.list()
		if err != nil {
			return Result{}, err
		}
		var b strings.Builder
		if len(list) == 0 {
			b.WriteString("No retained subagents in this parent session.")
		}
		for _, r := range list {
			fmt.Fprintf(&b, "%s — %s, %s, model %s; last run %s\nTask: %s\n", r.AgentID, r.Status, r.Mode, r.Model, r.LastRunStatus, r.Task)
			if r.Result != "" {
				fmt.Fprintf(&b, "Result: %s\n", r.Result)
			}
			if r.Error != "" {
				fmt.Fprintf(&b, "Error: %s\n", r.Error)
			}
		}
		return Result{Text: b.String(), Details: SubagentListDetails{Agents: list}}, nil
	case "archive":
		r, err := s.Store.archive(a.AgentID)
		if err != nil {
			return Result{}, err
		}
		return Result{Text: fmt.Sprintf("Subagent %s is archived. Its transcript is retained at %s.", r.AgentID, r.Session), Details: SubagentListDetails{Agents: []SubagentInfo{r}}}, nil
	case "spawn":
		if a.AgentID != "" {
			return Result{}, errors.New("spawn creates a new agent_id; use continue for an existing child")
		}
		if a.Mode != ModeExplore && a.Mode != ModeEdit {
			return Result{}, errors.New("spawn requires mode explore or edit")
		}
	case "continue":
		if a.AgentID == "" {
			return Result{}, errors.New("continue requires agent_id from this parent session")
		}
	default:
		return Result{}, errors.New("unknown subagent action: use spawn, continue, list, or archive")
	}
	if strings.TrimSpace(a.Task) == "" {
		return Result{}, errors.New("task must not be empty")
	}
	maxDepth := s.MaxDepth
	if maxDepth <= 0 {
		maxDepth = DefaultSubagentMaxDepth
	}
	if s.Depth >= maxDepth {
		return Result{}, fmt.Errorf("subagents cannot be nested more than %d level(s) deep", maxDepth)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	exe := s.Exe
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return Result{}, err
		}
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultSubagentTimeout
	}
	r, err := s.prepareRetained(a)
	if err != nil {
		return Result{}, err
	}
	if r.record.Mode == ModeExplore && !a.Wait && s.Bg != nil {
		res, err := s.Bg.dispatchRun(s, exe, r.record.RunID, r.record.Mode, a.Task, timeout, r, ctx)
		if err != nil {
			return r.finish(s, Result{}, err)
		}
		return res, nil
	}
	if err := s.acquire(ctx); err != nil {
		return r.finish(s, Result{}, err)
	}
	defer s.release()
	return s.runRetained(ctx, exe, r, a.Task, timeout, onPartial)
}

func (s *Subagent) runRetained(ctx context.Context, exe string, r *subagentRun, task string, timeout time.Duration, onPartial func(Partial)) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r.record.Status, r.record.UpdatedAt = "running", time.Now().UnixMilli()
	if err := r.store.write(r.record); err != nil {
		return r.finish(s, Result{}, err)
	}
	spec := childSpec{id: r.record.RunID, dir: s.Cwd, readOnly: r.record.Mode == ModeExplore, model: r.record.Model, modelPinned: true, resume: r.record.Session, contextLease: r.lease}
	preamble := s.exploreNote()
	if r.record.Mode == ModeEdit {
		lease, err := acquireEditLease(ctx)
		if err != nil {
			return r.finish(s, Result{}, err)
		}
		defer lease.Close()
		spec.editLease = lease
		preamble = sharedNote()
	}
	if r.resumed {
		preamble += "<continued-task>\nContinue your retained conversation for the new task below. Previous browser elements, coordinates, page versions, leases and login observations are historical: obtain a fresh snapshot before operating a page. Re-check files and partial outputs before repeating a cancelled or interrupted action. Do not replay old tool calls automatically.\n</continued-task>\n\n"
	}
	run, _ := s.spawn(ctx, exe, spec, preamble+task, onPartial)
	// JSON run_end counters are cumulative after -resume. Only the increment
	// belongs to this run. A killed process may have persisted turns but no end
	// event, so use its transcript as a second source of completed usage.
	totalIn, totalOut := run.input, run.output
	if transcript, err := session.Open(r.record.Session); err == nil {
		u, _ := transcript.UsageTotals()
		if u.Input > totalIn {
			totalIn = u.Input
		}
		if u.Output > totalOut {
			totalOut = u.Output
		}
	}
	d := SubagentDetails{ID: r.record.RunID, AgentID: r.record.AgentID, RunID: r.record.RunID, Mode: r.record.Mode, Model: r.record.Model, Session: r.record.Session, Turns: run.turns, InputTok: max(int64(0), totalIn-r.beforeIn), OutTok: max(int64(0), totalOut-r.beforeOut), ExitCode: run.exit}
	if r.record.Mode == ModeEdit {
		d.Isolation, d.Workspace = IsolationShared, s.Cwd
	}
	text, err := run.report(&d, timeout, s.SpillDir)
	if run.session != "" && run.session != r.record.Session {
		err = errors.New("child reported an unexpected transcript; retained context was not rebound")
	}
	return r.finish(s, Result{Text: text, Details: d}, err)
}

func (r *subagentRun) finish(s *Subagent, result Result, runErr error) (Result, error) {
	r.once.Do(func() {
		defer r.lease.Close()
		defer s.releaseExplore(r.provider)
		d, ok := result.Details.(SubagentDetails)
		if !ok {
			d = SubagentDetails{ID: r.record.RunID, AgentID: r.record.AgentID, RunID: r.record.RunID, Mode: r.record.Mode, Model: r.record.Model, Session: r.record.Session}
		}
		r.record.Status, r.record.LastRunStatus = "idle", "succeeded"
		if runErr != nil {
			r.record.LastRunStatus, r.record.Error = "failed", clipSubagent(runErr.Error(), 1500)
			if errors.Is(runErr, context.Canceled) || strings.Contains(runErr.Error(), "was aborted") {
				r.record.LastRunStatus = "cancelled"
			}
			if errors.Is(runErr, context.DeadlineExceeded) || strings.Contains(runErr.Error(), "did not finish within") {
				r.record.LastRunStatus = "timed_out"
			}
		}
		r.record.Result, r.record.UpdatedAt = clipSubagent(result.Text, 2000), time.Now().UnixMilli()
		d.Status = r.record.Status
		if err := r.store.write(r.record); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("save retained child status: %w", err))
			d.Status = "interrupted"
		}
		result.Details = d
		if runErr == nil {
			result.Text += fmt.Sprintf("\n\n[Retained subagent: %s. Context is idle and available for action=continue; this run: %s.]", d.AgentID, d.RunID)
		} else {
			runErr = fmt.Errorf("%w [Retained subagent: %s; inspect its transcript or use action=list before continuing.]", runErr, d.AgentID)
		}
		r.result, r.err = result, runErr
	})
	return r.result, r.err
}
