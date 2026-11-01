package web

// Workflow runs: start a workflow definition, walk its nodes, and make the
// whole thing observable. run.json under wf-runs/<id>/ is the state authority
// — rewritten atomically on every transition — and each active run owns a
// bare Hub (no session) whose stream is the live view of the same
// transitions. A run whose node waits on a person (waiting_human) suspends:
// the goroutine exits, the record stays working, and a resume (or the
// timeout sweep) rebuilds a runner from the file. A process restart fails
// only runs that had real execution in flight; suspended runs wake intact.
//
// Node execution follows the chain.go evaluateRun pattern: one fresh agent
// per node, a tool registry narrowed to the node/agent definitions, the run's
// workspace as cwd, no subagent registration. Orchestrator nodes are the one
// exception: their agents delegate through dispatch tools, never through the
// subagent tool — every dispatch is a fresh node-style agent of its own.
import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/tools"
	"github.com/yosukeno/pogo/workflow"
)

const wfRunRecordFile = "run.json"

// Public diagnostics identify the failure without disclosing filesystem paths
// or raw storage errors. The underlying cause stays in server diagnostics.
const wfPersistenceFailure = "workflow_state_write_failed"

// wfRun is one active run's live state. record is only ever replaced (by the
// run goroutine's sink), never mutated in place, so a read under mu is a
// consistent snapshot. done closes when the goroutine hands the record back —
// settled or suspended.
type wfRun struct {
	id     string
	wid    string
	owner  *identity.Scope
	hub    *Hub
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.RWMutex
	record workflow.RunRecord
}

type wfRunManager struct {
	dir string
	m   *Manager
	// Serialize registration/cancellation, including the pre-drive window.
	controlMu sync.Mutex

	mu   sync.Mutex
	runs map[string]*wfRun
	// blocked holds the last durable snapshots of runs stopped by a write
	// failure. They cannot be resumed/swept in this process. A restart uses
	// run.json recovery, which never replays a durable running node.
	blocked    map[string]workflow.RunRecord
	persist    func(string, workflow.RunRecord) error
	callbackMu sync.RWMutex
	callback   func(workflow.RunRecord) error
	closed     bool
	wg         sync.WaitGroup
	// pending holds runs found working but idle at boot (no node running, no
	// node waiting on a person — the crash caught the scheduler between
	// transitions). They resume once the manager is fully built.
	pending []workflow.RunRecord
}

func newWFRunManager(dir string, m *Manager) *wfRunManager {
	return &wfRunManager{dir: dir, m: m, runs: map[string]*wfRun{}, blocked: map[string]workflow.RunRecord{}}
}

func (rm *wfRunManager) writeRecord(path string, rec workflow.RunRecord) error {
	var err error
	if rm.persist != nil {
		err = rm.persist(path, rec)
	} else {
		err = writeWFRecord(path, rec)
	}
	if err != nil {
		return err
	}
	rm.callbackMu.RLock()
	record := rm.callback
	rm.callbackMu.RUnlock()
	if record != nil {
		return record(rec)
	}
	return nil
}

func (rm *wfRunManager) runDir(id string) string { return filepath.Join(rm.dir, id) }

func (rm *wfRunManager) recordPath(id string) string {
	return filepath.Join(rm.runDir(id), wfRunRecordFile)
}

// recover settles what the last process left behind, three ways. A run with a
// node still marked running had real execution in flight when the process
// died: it fails with "process restarted" (running nodes failed, waiting
// skipped, human waits cancelled). A run waiting only on people is suspended
// and stays that way — resume answers it after the restart. A run with
// neither was caught between scheduler transitions: its record is consistent,
// so it queues to continue. Unreadable records are left alone: recovery has
// no business deleting state it cannot parse.
func (rm *wfRunManager) recover() {
	entries, err := os.ReadDir(rm.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !validID(e.Name()) {
			continue
		}
		path := rm.recordPath(e.Name())
		rec, err := readWFRecord(path)
		if err != nil || rec.Status != workflow.RunWorking {
			continue
		}
		inFlight, waitingHuman := false, false
		for _, st := range rec.Nodes {
			switch st.Status {
			case workflow.NodeRunning:
				inFlight = true
			case workflow.NodeWaitingHuman:
				waitingHuman = true
			}
		}
		switch {
		case inFlight:
			lastDurable := rec
			lastDurable.Nodes = make(map[string]workflow.NodeState, len(rec.Nodes))
			for id, state := range rec.Nodes {
				lastDurable.Nodes[id] = state
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			for id, st := range rec.Nodes {
				switch st.Status {
				case workflow.NodeRunning:
					st.Status, st.Error, st.EndedAt = workflow.NodeFailed, "process restarted", now
				case workflow.NodeWaiting:
					st.Status, st.EndedAt = workflow.NodeSkipped, now
				case workflow.NodeWaitingHuman:
					st.Status, st.Error, st.EndedAt = workflow.NodeCancelled, "process restarted", now
				}
				rec.Nodes[id] = st
			}
			rec.Status, rec.Error, rec.EndedAt = workflow.RunFailed, "process restarted", now
			if err := rm.writeRecord(path, rec); err != nil {
				lastDurable.PersistenceError = wfPersistenceFailure
				fmt.Fprintf(os.Stderr, "workflow run %s recovery: %v\n", e.Name(), err)
				rm.blocked[rec.ID] = lastDurable
			}
		case waitingHuman:
			// Suspended: the record already says everything; a resume wakes it.
		default:
			rm.pending = append(rm.pending, rec)
		}
	}
}

// resumePending continues the runs recover() found idle, once the manager's
// skill views exist (NewManager calls it after rebuildSkillViews — executors
// built earlier would see empty catalogs).
func (rm *wfRunManager) resumePending() {
	for _, rec := range rm.pending {
		if err := rm.m.enqueueWorkflowRecovery(rec); err != nil {
			fmt.Fprintf(os.Stderr, "workflow run %s resume: %v\n", rec.ID, err)
		}
	}
	rm.pending = nil
}

// start freezes the given definition — the published snapshot, in the
// publish model — creates the run directory, and launches the runner on its
// own goroutine. The run outlives the request that started it, like a session
// run outliving its connection.
func (rm *wfRunManager) start(def workflow.Definition, owner *identity.Scope) (*wfRun, error) {
	def.Normalize()
	if err := def.Validate(); err != nil {
		return nil, fmt.Errorf("无效的工作流定义：%w", err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	id := "run_" + hex.EncodeToString(nonce[:])
	workspace := filepath.Join(rm.runDir(id), "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		return nil, err
	}
	record := workflow.RunRecord{
		ID: id, WorkflowID: def.ID, Revision: def.Revision, Definition: def,
		Status: workflow.RunWorking, Owner: owner, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := rm.writeRecord(rm.recordPath(id), record); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &wfRun{id: id, wid: def.ID, owner: owner, hub: NewHub(), ctx: ctx, cancel: cancel, done: make(chan struct{}), record: record}
	if err := rm.drive(run, &workflow.Runner{
		Record:    record,
		Executor:  rm.executor(run),
		Sink:      &wfRunSink{rm: rm, run: run},
		Workspace: workspace,
	}); err != nil {
		cancel()
		return nil, err
	}
	return run, nil
}

// drive registers a run and launches its runner goroutine — the shared heart
// of start, resume and recovery. The run outlives the request that woke it,
// like a session run outliving its connection.
func (rm *wfRunManager) drive(run *wfRun, runner *workflow.Runner) error {
	rm.mu.Lock()
	if rm.closed {
		rm.mu.Unlock()
		return errors.New("server is shutting down")
	}
	if rec, blocked := rm.blocked[run.id]; blocked {
		rm.mu.Unlock()
		return fmt.Errorf("%w: %s", errWFRunPersistence, rec.PersistenceError)
	}
	if _, active := rm.runs[run.id]; active {
		rm.mu.Unlock()
		return errWFRunActive
	}
	rm.runs[run.id] = run
	rm.wg.Add(1)
	rm.mu.Unlock()
	go func() {
		defer rm.wg.Done()
		last := runner.Run(run.ctx)
		if err := runner.Err(); err != nil {
			last.PersistenceError = wfPersistenceFailure
			run.mu.Lock()
			run.record = last
			run.mu.Unlock()
			rm.mu.Lock()
			rm.blocked[run.id] = last
			rm.mu.Unlock()
			// A diagnostic, not a successful terminal transition. The last
			// durable Status/Nodes remain visible beside this explicit error.
			run.hub.Publish(Event{Type: EvError, RunID: run.id, WorkflowID: run.wid, Error: wfPersistenceFailure, WorkflowRun: &last})
		}
		// The last transition has been fanned out by the sink; closing the hub
		// ends the live streams, and the record on disk is the state from here
		// — settled, or suspended on a person.
		run.hub.Close()
		rm.mu.Lock()
		delete(rm.runs, run.id)
		rm.mu.Unlock()
		close(run.done)
	}()
	return nil
}

// resumeRecord rebuilds a runner from an on-disk record, applying the human
// decision (nil for a plain continuation). It is the one path every wake-up
// takes: the resume API, the timeout sweep, and boot-time recovery.
func (rm *wfRunManager) resumeRecord(rec workflow.RunRecord, decision *workflow.HumanDecision) (*wfRun, error) {
	workspace := filepath.Join(rm.runDir(rec.ID), "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &wfRun{id: rec.ID, wid: rec.WorkflowID, owner: rec.Owner, hub: NewHub(), ctx: ctx, cancel: cancel, done: make(chan struct{}), record: rec}
	if err := rm.drive(run, &workflow.Runner{
		Record:    rec,
		Executor:  rm.executor(run),
		Sink:      &wfRunSink{rm: rm, run: run},
		Workspace: workspace,
		Resume:    decision,
	}); err != nil {
		cancel()
		return nil, err
	}
	return run, nil
}

// errWFRunActive says the run has a live runner right now: a resume, cancel
// or second wake must wait for it to quiesce.
var errWFRunActive = errors.New("run 正在执行中，请稍后")
var errWFRunPersistence = errors.New("工作流状态保存失败，已停止后续调度；请修复存储后恢复")

// errWFRunNotSuspended / errWFNodeNotWaiting guard the resume preconditions.
var (
	errWFRunNotSuspended = errors.New("run 已结束，无法继续")
	errWFNodeNotWaiting  = errors.New("节点不在等待人工状态")
)

// waitUnmapped waits out a run's drain window: the record shows suspended the
// moment the sink persists it, a beat before the runner goroutine unmaps
// itself. A genuinely executing run never leaves the map inside the budget
// and the caller still answers errWFRunActive.
func (rm *wfRunManager) waitUnmapped(id string, budget time.Duration) {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		rm.mu.Lock()
		_, active := rm.runs[id]
		rm.mu.Unlock()
		if !active {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// resume applies a human decision to one waiting_human node of a suspended
// run and wakes the run. Validation (the node waits, the action is one of its
// buttons, the form answers hold) happens before the runner is touched.
func (rm *wfRunManager) resume(rec workflow.RunRecord, nid string, decision workflow.HumanDecision) (*wfRun, error) {
	if rec.Status != workflow.RunWorking {
		return nil, errWFRunNotSuspended
	}
	st, ok := rec.Nodes[nid]
	if !ok || st.Status != workflow.NodeWaitingHuman || st.Human == nil {
		return nil, errWFNodeNotWaiting
	}
	node := rec.Definition.Node(nid)
	if !decision.Timeout {
		if err := validateHumanDecision(node, decision); err != nil {
			return nil, err
		}
	}
	run, err := rm.resumeRecord(rec, &decision)
	if !errors.Is(err, errWFRunActive) {
		return run, err
	}
	// The record says suspended, so a mapped runner can only be draining its
	// own suspend — wait it out and re-read what it left, in case another
	// decision landed first.
	rm.waitUnmapped(rec.ID, 2*time.Second)
	fresh, rerr := readWFRecord(rm.recordPath(rec.ID))
	if rerr != nil {
		return nil, rerr
	}
	st, ok = fresh.Nodes[nid]
	if !ok || st.Status != workflow.NodeWaitingHuman || st.Human == nil {
		return nil, errWFNodeNotWaiting
	}
	return rm.resumeRecord(fresh, &decision)
}

// validateHumanDecision checks a resume payload against the node's presented
// contract: a known action, required fields answered, select values
// legitimate, no smuggled keys.
func validateHumanDecision(node workflow.Node, d workflow.HumanDecision) error {
	actions := workflow.EffectiveActions(node)
	ok := false
	for _, a := range actions {
		if d.Action == a {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Errorf("无效的操作：%s", d.Action)
	}
	if node.Kind != workflow.KindHuman {
		return nil
	}
	known := map[string]workflow.FieldSpec{}
	for _, f := range node.Fields {
		known[f.Key] = f
		if !f.Required {
			continue
		}
		if strings.TrimSpace(d.Values[f.Key]) == "" {
			return fmt.Errorf("必填字段未填写：%s", f.Key)
		}
	}
	for k, v := range d.Values {
		f, ok := known[k]
		if !ok {
			return fmt.Errorf("未知的字段：%s", k)
		}
		if len([]rune(v)) > 2000 {
			return fmt.Errorf("字段 %s 的内容超过长度限制", k)
		}
		if f.Type == "select" {
			legit := false
			for _, o := range f.Options {
				if v == o {
					legit = true
					break
				}
			}
			if !legit {
				return fmt.Errorf("字段 %s 的取值不在选项内", k)
			}
		}
	}
	return nil
}

// close cancels every active run and waits for the goroutines to settle their
// records. Hubs go with the goroutines; the files keep the final truth.
func (rm *wfRunManager) close() {
	rm.mu.Lock()
	rm.closed = true
	active := make([]*wfRun, 0, len(rm.runs))
	for _, run := range rm.runs {
		active = append(active, run)
	}
	rm.mu.Unlock()
	for _, run := range active {
		run.cancel()
	}
	rm.wg.Wait()
}

// snapshot returns the freshest record for a run: the in-memory copy while it
// is active, the settled file afterwards.
func (rm *wfRunManager) snapshot(id string) (workflow.RunRecord, bool) {
	rm.mu.Lock()
	run, ok := rm.runs[id]
	blocked, quarantined := rm.blocked[id]
	rm.mu.Unlock()
	if ok {
		run.mu.RLock()
		defer run.mu.RUnlock()
		return run.record, true
	}
	if quarantined {
		return blocked, true
	}
	if !validID(id) {
		return workflow.RunRecord{}, false
	}
	rec, err := readWFRecord(rm.recordPath(id))
	if err != nil {
		if rm.m != nil && rm.m.taskRuns != nil {
			if db, derr := rm.m.taskRuns.database(false); derr == nil && db != nil {
				if meta, merr := db.GetWorkflowRun(id); merr == nil {
					if errors.Is(err, os.ErrNotExist) {
						virtual, verr := workflowMetaRecord(meta)
						if verr == nil && !meta.CancelRequested {
							if executions, err := db.ListWorkflowExecutions(id); err == nil && len(executions) > 0 {
								latest := executions[0]
								for _, execution := range executions {
									if execution.CreatedAt > latest.CreatedAt || execution.CreatedAt == latest.CreatedAt && execution.FinishedAt > latest.FinishedAt {
										latest = execution
									}
								}
								switch latest.Status {
								case taskqueue.Failed, taskqueue.Interrupted, taskqueue.Cancelled:
									virtual.Status, virtual.Error = workflow.RunFailed, latest.Error
									if latest.Status == taskqueue.Cancelled {
										virtual.Status = workflow.RunCancelled
									}
									if latest.Status == taskqueue.Interrupted {
										virtual.Status = "interrupted"
									}
									if latest.FinishedAt > 0 {
										virtual.EndedAt = time.UnixMilli(latest.FinishedAt).UTC().Format(time.RFC3339Nano)
									}
								}
							}
						}
						return virtual, verr == nil
					}
				}
			}
		}
		return workflow.RunRecord{}, false
	}
	return rec, true
}

// list returns the runs of one workflow, newest first, with active runs
// overlaid from memory so a poll never reads a stale file.
func (rm *wfRunManager) list(wid string) []workflow.RunRecord {
	byID := map[string]workflow.RunRecord{}
	if rm.m != nil && rm.m.taskRuns != nil {
		if db, err := rm.m.taskRuns.database(false); err == nil && db != nil {
			if metas, err := db.ListWorkflowRuns(taskqueue.WorkflowRunFilter{WorkflowID: wid}); err == nil {
				for _, meta := range metas {
					if rec, ok := rm.snapshot(meta.ID); ok {
						byID[rec.ID] = rec
					}
				}
			}
		}
	}
	entries, err := os.ReadDir(rm.dir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() || !validID(e.Name()) {
				continue
			}
			rec, err := readWFRecord(rm.recordPath(e.Name()))
			if err == nil && rec.WorkflowID == wid {
				byID[rec.ID] = rec
			}
		}
	}
	rm.mu.Lock()
	for id, rec := range rm.blocked {
		if rec.WorkflowID == wid {
			byID[id] = rec
		}
	}
	for _, run := range rm.runs {
		if run.wid == wid {
			run.mu.RLock()
			byID[run.id] = run.record
			run.mu.RUnlock()
		}
	}
	rm.mu.Unlock()
	out := make([]workflow.RunRecord, 0, len(byID))
	for _, rec := range byID {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out
}

// cancel reports whether the run exists and whether a cancellation was sent.
// A settled run answers (true, false), mirroring the MCP task cancel shape. A
// suspended run has no goroutine to signal, so its open nodes are settled
// cancelled in place — the record is the authority either way.
func (rm *wfRunManager) cancel(id string) (exists, sent bool) {
	rm.mu.Lock()
	run, ok := rm.runs[id]
	rm.mu.Unlock()
	if ok {
		run.cancel()
		// Draining suspend or live execution: give the runner the window to
		// settle the record itself before settling it in place below.
		rm.waitUnmapped(id, 2*time.Second)
	}
	// Only a genuinely suspended run may be rewritten in place. A slow
	// executor can still be draining, and its live map is not ours to mutate.
	// Holding this lock also excludes a simultaneous resume while we persist.
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if _, active := rm.runs[id]; active {
		return true, true
	}
	if _, blocked := rm.blocked[id]; blocked {
		return true, false
	}
	if !validID(id) {
		return false, false
	}
	rec, err := readWFRecord(rm.recordPath(id))
	if err != nil {
		return false, false
	}
	if workflow.IsTerminal(rec.Status) {
		// Terminal on arrival, or settled by the wait: only a cancelled
		// record means a cancellation actually landed.
		return true, rec.Status == workflow.RunCancelled
	}
	if rec.PersistenceError != "" {
		return true, false
	}
	lastDurable := rec
	rec.Nodes = make(map[string]workflow.NodeState, len(lastDurable.Nodes))
	for nid, state := range lastDurable.Nodes {
		rec.Nodes[nid] = state
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for nid, st := range rec.Nodes {
		switch st.Status {
		case workflow.NodeWaiting, workflow.NodeWaitingHuman:
			st.Status, st.EndedAt = workflow.NodeCancelled, now
			rec.Nodes[nid] = st
		}
	}
	rec.Status, rec.Error, rec.EndedAt = workflow.RunCancelled, "cancelled", now
	if err := rm.writeRecord(rm.recordPath(id), rec); err != nil {
		// The failed write changes neither the old file nor a live record.
		lastDurable.PersistenceError = wfPersistenceFailure
		fmt.Fprintf(os.Stderr, "workflow run %s cancellation: %v\n", id, err)
		rm.blocked[id] = lastDurable
		return true, false
	}
	return true, true
}

// runSweep is the human-wait timeout enforcer: once a minute it settles
// expired waits by their on_timeout contract. Started by NewManager; stops
// with the manager's stop channel.
func (rm *wfRunManager) runSweep() {
	defer rm.wg.Done()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-rm.m.stop:
			return
		case <-t.C:
			rm.sweepOnce()
		}
	}
}

// sweepOnce handles expired human waits in suspended runs, one per run per
// pass — a wake may continue the run for minutes, and the next tick handles
// whatever waits again. Active runs are skipped: their waits can only lapse
// once they quiesce, and the sweep meets them there.
func (rm *wfRunManager) sweepOnce() {
	entries, err := os.ReadDir(rm.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !validID(e.Name()) {
			continue
		}
		rm.mu.Lock()
		_, active := rm.runs[e.Name()]
		_, blocked := rm.blocked[e.Name()]
		rm.mu.Unlock()
		if active || blocked {
			continue
		}
		rec, err := readWFRecord(rm.recordPath(e.Name()))
		if err != nil || rec.Status != workflow.RunWorking {
			continue
		}
		nid := expiredHumanWait(rec)
		if nid == "" {
			continue
		}
		if err := rm.m.enqueueWorkflowDecision(rec, workflow.HumanDecision{Node: nid, Timeout: true, Actor: "service:workflow-timeout"}); err != nil && !errors.Is(err, taskqueue.ErrConflict) {
			fmt.Fprintf(os.Stderr, "workflow run %s human timeout: %v\n", e.Name(), err)
		}
	}
}

// expiredHumanWait names the first node (definition order, deterministic)
// whose human wait has lapsed, or "" when none has.
func expiredHumanWait(rec workflow.RunRecord) string {
	now := time.Now().UTC()
	for _, n := range rec.Definition.Nodes {
		st, ok := rec.Nodes[n.ID]
		if !ok || st.Status != workflow.NodeWaitingHuman || st.Human == nil || st.Human.Deadline == "" {
			continue
		}
		if deadline, err := time.Parse(time.RFC3339Nano, st.Human.Deadline); err == nil && now.After(deadline) {
			return n.ID
		}
	}
	return ""
}

// cancelWorkflow stops every active run of one workflow — the definition went
// away or back to draft, so its runs must not finish against it.
func (rm *wfRunManager) cancelWorkflow(wid string) {
	for _, rec := range rm.list(wid) {
		if workflow.IsTerminal(rec.Status) || rec.Status == "interrupted" {
			continue
		}
		if _, err := rm.m.cancelWorkflowRun(rec.ID); err != nil {
			fmt.Fprintf(os.Stderr, "workflow run %s cancellation: %v\n", rec.ID, err)
		}
	}
}

// Legacy ownership fallback. Unowned history is operator-only; the action
// policy additionally handles explicit participants and catalog ownership.
func wfRunVisible(rec workflow.RunRecord, scope *identity.Scope, admin bool) bool {
	if admin {
		return true
	}
	return rec.Owner != nil && scope != nil && *scope == *rec.Owner
}

// wfRunSink is the runner's EventSink: every transition replaces the live
// record, rewrites run.json atomically, and fans out the matching web event.
// The file write comes first so a crash leaves the record at worst one event
// behind the stream, never ahead of it.
type wfRunSink struct {
	rm  *wfRunManager
	run *wfRun
}

func (s *wfRunSink) Publish(e workflow.Event) error {
	// run.json first, then the live record the API reads, then the stream:
	// a reader of either must never see a state the authority does not hold.
	// The Runner publishes from one goroutine, so the order is preserved.
	if err := s.rm.writeRecord(s.rm.recordPath(s.run.id), e.Record); err != nil {
		// Surface the failure immediately, even when an already-started tool
		// takes time to acknowledge cancellation. Durable node states stay put.
		s.run.mu.Lock()
		last := s.run.record
		last.PersistenceError = wfPersistenceFailure
		fmt.Fprintf(os.Stderr, "workflow run %s record (%s %s): %v\n", s.run.id, e.Kind, e.Node, err)
		s.run.record = last
		s.run.mu.Unlock()
		s.rm.mu.Lock()
		s.rm.blocked[s.run.id] = last
		s.rm.mu.Unlock()
		s.run.hub.Publish(Event{Type: EvError, RunID: s.run.id, WorkflowID: s.run.wid, Error: last.PersistenceError, WorkflowRun: &last})
		return err
	}
	s.run.mu.Lock()
	s.run.record = e.Record
	s.run.mu.Unlock()
	rec := e.Record
	out := Event{RunID: s.run.id, WorkflowID: s.run.wid, WorkflowRun: &rec}
	switch e.Kind {
	case workflow.EventRunStart:
		out.Type = EvWorkflowRunStart
	case workflow.EventNodeStart:
		out.Type = EvWorkflowNodeStart
		out.NodeID, out.NodeStatus = e.Node, e.State.Status
	case workflow.EventNodeEnd:
		out.Type = EvWorkflowNodeEnd
		out.NodeID, out.NodeStatus = e.Node, e.State.Status
		out.Artifact, out.Error = e.State.Artifact, e.State.Error
	case workflow.EventRunEnd:
		out.Type = EvWorkflowRunEnd
		out.Error = e.Record.Error
	default:
		return nil
	}
	s.run.hub.Publish(out)
	return nil
}

// executor builds the runner's NodeExecutor. One fresh agent per node, wired
// the way newSession wires a Studio conversation but cut down: capability
// narrowing from the agent definition, the run workspace as cwd, no session
// store, no gate, no subagent — a node never delegates.
func (rm *wfRunManager) executor(run *wfRun) workflow.NodeExecutor {
	m := rm.m
	return func(ctx context.Context, in workflow.NodeInput) (workflow.NodeResult, error) {
		a, ok := m.studio.get(in.Node.Agent)
		if !ok || a.Status != "ready" {
			return workflow.NodeResult{}, fmt.Errorf("agent %s is unavailable", in.Node.Agent)
		}
		model := a.Model
		if in.Node.Model != "" {
			model = in.Node.Model
		}
		cfg, err := config.Resolve(model)
		if err != nil {
			return workflow.NodeResult{}, err
		}
		cfg = applyStudioEffort(cfg)
		v := m.skillViews()
		subset, mcpOnly := m.studioSelection(a)
		if subset != nil {
			v = m.filterSkillViews(v, subset)
		} else {
			v = m.unregisterSynthTools(v)
		}
		mcpTools, trusted := m.mcpProjectionFor(v.synthTools, run.owner, mcpOnly)
		system := agent.SystemPrompt(in.Workspace, v.uiSection, v.section)
		system += "\n\n# Agent configuration\nYou are " + a.Name + ".\n" + a.Instructions
		maxTurns := 15
		if in.Node.MaxTurns > 0 {
			maxTurns = in.Node.MaxTurns
		}
		synthTools := v.synthTools
		var coll *dispatchCollector
		if in.Node.Orchestrate != nil {
			coll = newDispatchCollector(in.Node.Orchestrate)
			synthTools = append(append([]tools.Tool(nil), synthTools...), rm.dispatchTools(run, in, coll)...)
		}
		registry := tools.New(tools.Options{
			Cwd:         in.Workspace,
			ResultScope: "workflow:" + run.id + ":" + in.Node.ID,
			Enabled:     narrowWorkflowTools(m.studioNativeSelection(a), in.Node.Tools),
			ReadRoots:   v.readRoots,
			WriteRoots:  []string{in.Workspace},
			Todo:        true,
			Synth:       synthTools,
			MCP:         mcpTools,
		})
		if err := workflowTrialNodeExecutionError(run, a, in.Node, registry, v.synthTools, mcpTools, trusted); err != nil {
			return workflow.NodeResult{}, err
		}
		ag := agent.New(agent.Config{
			Client:           rm.client(cfg),
			Registry:         registry,
			SystemPrompt:     system,
			MaxTurns:         maxTurns,
			ToolOutputBudget: agent.ToolOutputBudgetConfig{ContextWindow: cfg.ContextWindow, MaxOutputTokens: cfg.MaxTokens},
		})
		var summary string
		var end *agent.Event
		for e := range ag.Run(ctx, workflowNodePrompt(in)) {
			if e.Kind == agent.EventMessage {
				summary = e.Message.Text()
			}
			if e.Kind == agent.EventAgentEnd {
				final := e
				end = &final
			}
			rm.forwardFrame(run, in.Node.ID, e)
		}
		res := workflow.NodeResult{Summary: clipRunes(summary, 2000), Usage: ag.Usage(), Dispatches: coll.records()}
		if err := workflowAgentError(ctx, end); err != nil {
			return res, err
		}
		if in.Node.Expect != nil && in.Node.Expect.Path != "" {
			p := filepath.Join(in.Workspace, filepath.FromSlash(in.Node.Expect.Path))
			if st, err := os.Stat(p); err != nil || st.IsDir() {
				return workflow.NodeResult{Dispatches: coll.records()}, fmt.Errorf("expected artifact %s was not produced", in.Node.Expect.Path)
			}
			res.Artifact = in.Node.Expect.Path
		}
		return res, nil
	}
}

// dispatchCollector is one orchestrator node's delegation state: the budget
// left, the concurrency slots, and the settled records every return path
// reports. Candidates share the run workspace, so parallel dispatches
// (max_parallel > 1) must give their expected outputs distinct paths.
type dispatchCollector struct {
	mu     sync.Mutex
	log    []workflow.DispatchRecord
	budget int
	sem    chan struct{}
}

func newDispatchCollector(o *workflow.Orchestrate) *dispatchCollector {
	budget, parallel := o.MaxDispatches, o.MaxParallel
	if budget <= 0 {
		budget = 5
	}
	if parallel <= 0 {
		parallel = 1
	}
	return &dispatchCollector{budget: budget, sem: make(chan struct{}, parallel)}
}

// takeBudget spends one dispatch slot, or reports the budget exhausted.
func (c *dispatchCollector) takeBudget() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.budget <= 0 {
		return false
	}
	c.budget--
	return true
}

func (c *dispatchCollector) add(rec workflow.DispatchRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log = append(c.log, rec)
}

func (c *dispatchCollector) records() []workflow.DispatchRecord {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]workflow.DispatchRecord(nil), c.log...)
}

// dispatchTools builds one dispatch_to_<agent> tool per candidate. The hint
// is the tool's description — the only thing the orchestrating model reads
// when choosing.
func (rm *wfRunManager) dispatchTools(run *wfRun, in workflow.NodeInput, coll *dispatchCollector) []tools.Tool {
	out := make([]tools.Tool, 0, len(in.Node.Orchestrate.Candidates))
	for _, cand := range in.Node.Orchestrate.Candidates {
		out = append(out, &dispatchTool{rm: rm, run: run, parent: in, cand: cand, coll: coll})
	}
	return out
}

// dispatchTool is one candidate's dispatch handle. Its execution body reuses
// the node execution path: a fresh agent on the candidate's own narrowed
// capabilities, the run workspace as cwd, the node's upstream artifact
// manifest in the prompt.
type dispatchTool struct {
	rm     *wfRunManager
	run    *wfRun
	parent workflow.NodeInput
	cand   workflow.Candidate
	coll   *dispatchCollector
}

func (t *dispatchTool) Name() string { return "dispatch_to_" + t.cand.Agent }

func (t *dispatchTool) Description() string {
	if t.cand.Hint != "" {
		return t.cand.Hint
	}
	return "Dispatch a subtask to agent " + t.cand.Agent + " and return its result."
}

func (t *dispatchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string", "description": "The subtask brief, at most 2000 characters."},
		},
		"required": []string{"task"},
	}
}

func (t *dispatchTool) ExecutionMode() tools.ExecutionMode { return tools.Parallel }

func (t *dispatchTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	if err := ctx.Err(); err != nil {
		return tools.Result{}, err
	}
	var body struct {
		Task string `json:"task"`
	}
	if err := json.Unmarshal(args, &body); err != nil {
		return tools.Result{}, err
	}
	task := strings.TrimSpace(body.Task)
	if task == "" {
		return tools.Result{}, errors.New("task is required")
	}
	if len([]rune(task)) > 2000 {
		return tools.Result{}, errors.New("task exceeds 2000 characters")
	}
	if !t.coll.takeBudget() {
		return tools.Result{}, errors.New("dispatch budget for this node is exhausted")
	}
	select {
	case t.coll.sem <- struct{}{}:
		defer func() { <-t.coll.sem }()
	case <-ctx.Done():
		return tools.Result{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return tools.Result{}, err
	}
	return t.runDispatch(ctx, task)
}

// runDispatch drives one delegation to completion and reports it in the
// shape the run record keeps: summary, artifact and usage as the tool result.
func (t *dispatchTool) runDispatch(ctx context.Context, task string) (tools.Result, error) {
	rec := workflow.DispatchRecord{
		Candidate: t.cand.Agent, Task: clipRunes(task, 1000),
		Status: workflow.NodeRunning, StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	t.publish(EvWorkflowDispatchStart, rec)
	res, err := t.execute(ctx, task)
	rec.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
	rec.Summary, rec.Usage = res.Summary, res.Usage
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			rec.Status = workflow.NodeCancelled
		} else {
			rec.Status = workflow.NodeFailed
		}
		rec.Error = err.Error()
		t.coll.add(rec)
		t.publish(EvWorkflowDispatchEnd, rec)
		return tools.Result{}, err
	}
	rec.Status = workflow.NodeSuccess
	rec.Summary, rec.Artifact, rec.Usage = res.Summary, res.Artifact, res.Usage
	t.coll.add(rec)
	t.publish(EvWorkflowDispatchEnd, rec)
	payload, err := json.Marshal(map[string]any{"summary": res.Summary, "artifact": res.Artifact, "usage": res.Usage})
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Text: string(payload), Details: rec}, nil
}

// execute is the candidate's agent run — the node executor's body at
// dispatch scale: same narrowing, same workspace, no todo list, no further
// dispatch tools (a candidate never re-delegates).
func (t *dispatchTool) execute(ctx context.Context, task string) (workflow.NodeResult, error) {
	m := t.rm.m
	a, ok := m.studio.get(t.cand.Agent)
	if !ok || a.Status != "ready" {
		return workflow.NodeResult{}, fmt.Errorf("candidate agent %s is unavailable", t.cand.Agent)
	}
	cfg, err := config.Resolve(a.Model)
	if err != nil {
		return workflow.NodeResult{}, err
	}
	cfg = applyStudioEffort(cfg)
	v := m.skillViews()
	subset, mcpOnly := m.studioSelection(a)
	if subset != nil {
		v = m.filterSkillViews(v, subset)
	} else {
		v = m.unregisterSynthTools(v)
	}
	mcpTools, trusted := m.mcpProjectionFor(v.synthTools, t.run.owner, mcpOnly)
	system := agent.SystemPrompt(t.parent.Workspace, v.uiSection, v.section)
	system += "\n\n# Agent configuration\nYou are " + a.Name + ".\n" + a.Instructions
	registry := tools.New(tools.Options{
		Cwd:         t.parent.Workspace,
		ResultScope: "workflow:" + t.run.id + ":" + t.parent.Node.ID + ":dispatch:" + t.cand.Agent,
		Enabled:     m.studioNativeSelection(a),
		ReadRoots:   v.readRoots,
		WriteRoots:  []string{t.parent.Workspace},
		Synth:       v.synthTools,
		MCP:         mcpTools,
	})
	if err := workflowTrialNodeExecutionError(t.run, a, workflow.Node{Agent: t.cand.Agent}, registry, v.synthTools, mcpTools, trusted); err != nil {
		return workflow.NodeResult{}, err
	}
	ag := agent.New(agent.Config{
		Client:           t.rm.client(cfg),
		Registry:         registry,
		SystemPrompt:     system,
		MaxTurns:         15,
		ToolOutputBudget: agent.ToolOutputBudgetConfig{ContextWindow: cfg.ContextWindow, MaxOutputTokens: cfg.MaxTokens},
	})
	var prompt strings.Builder
	prompt.WriteString(task)
	if len(t.parent.Artifacts) > 0 {
		prompt.WriteString("\n\nArtifacts from upstream nodes are in the workspace:\n")
		for _, art := range t.parent.Artifacts {
			fmt.Fprintf(&prompt, "- %s (node %s)\n", art.Path, art.Node)
		}
	}
	var summary string
	var end *agent.Event
	for e := range ag.Run(ctx, prompt.String()) {
		if e.Kind == agent.EventMessage {
			summary = e.Message.Text()
		}
		if e.Kind == agent.EventAgentEnd {
			final := e
			end = &final
		}
	}
	res := workflow.NodeResult{Summary: clipRunes(summary, 2000), Usage: ag.Usage()}
	return res, workflowAgentError(ctx, end)
}

// Only a clean completed event means the agent finished. Existing artifacts
// or an earlier text message cannot turn a transport error/budget stop into
// success. Unknown/missing terminal reasons fail closed.
func workflowAgentError(ctx context.Context, end *agent.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if end == nil {
		return errors.New("agent closed without a terminal event")
	}
	if end.EndReason == agent.EndAborted {
		if end.Err != nil {
			return fmt.Errorf("agent ended (%s): %v: %w", end.EndReason, end.Err, context.Canceled)
		}
		return fmt.Errorf("agent ended (%s): %w", end.EndReason, context.Canceled)
	}
	if end.Err != nil {
		return fmt.Errorf("agent ended (%s): %w", end.EndReason, end.Err)
	}
	if end.EndReason == agent.EndCompleted {
		return nil
	}
	return fmt.Errorf("agent did not complete: %s", end.EndReason)
}

// publish fans a dispatch transition out to the run's hub; a copy goes with
// each event so later mutation never reaches a subscriber.
func (t *dispatchTool) publish(typ EventType, rec workflow.DispatchRecord) {
	copied := rec
	t.run.hub.Publish(Event{Type: typ, RunID: t.run.id, WorkflowID: t.run.wid, NodeID: t.parent.Node.ID, Dispatch: &copied})
}

// forwardFrame translates one loop event into the web frame and relays it as
// a workflow node frame, tagged with the node so the observer can place it.
// Events the wire contract does not carry (agent_start, tool_results) stay
// home — the node's own start/end events already say as much.
func (rm *wfRunManager) forwardFrame(run *wfRun, nodeID string, e agent.Event) {
	frame, ok := fromAgentEvent(e)
	if !ok {
		return
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return
	}
	run.hub.Publish(Event{Type: EvWorkflowNodeFrame, RunID: run.id, WorkflowID: run.wid, NodeID: nodeID, Frame: raw})
}

// client builds the node's LLM client, honoring the manager's test override
// exactly like the session path does.
func (rm *wfRunManager) client(cfg config.Resolved) llm.Client {
	if rm.m.cfg.NewClient != nil {
		return rm.m.cfg.NewClient(cfg, nil)
	}
	return llm.New(llm.Options{
		BaseURL:       cfg.BaseURL,
		APIKey:        cfg.APIKey,
		Model:         cfg.Model,
		MaxTokens:     cfg.MaxTokens,
		Headers:       cfg.Headers,
		ExtraBody:     cfg.ExtraBody,
		Compatibility: cfg.Compatibility,
		MaxRetries:    rm.m.cfg.Retries,
	})
}

// narrowWorkflowTools resolves a node's native-tool selection against its
// agent's: the node may only narrow, never widen. Both absent is the legacy
// full set; an empty intersection is the protected core alone, fail closed.
func narrowWorkflowTools(agentTools, nodeTools *[]string) *[]string {
	if nodeTools == nil {
		return agentTools
	}
	if agentTools == nil {
		return nodeTools
	}
	allowed := map[string]bool{}
	for _, name := range *agentTools {
		allowed[name] = true
	}
	out := make([]string, 0, len(*nodeTools))
	for _, name := range *nodeTools {
		if allowed[name] {
			out = append(out, name)
		}
	}
	return &out
}

// workflowNodePrompt is the node's brief: its task, the upstream artifact
// manifest, and the deliverable contract. The persona lives in the system
// prompt; this is the one user message the node's agent ever sees.
func workflowNodePrompt(in workflow.NodeInput) string {
	var b strings.Builder
	b.WriteString(in.Node.Task)
	if len(in.Artifacts) > 0 {
		b.WriteString("\n\nArtifacts from upstream nodes are in the workspace:\n")
		for _, art := range in.Artifacts {
			fmt.Fprintf(&b, "- %s (node %s)\n", art.Path, art.Node)
		}
	}
	if in.Node.Expect != nil && in.Node.Expect.Path != "" {
		fmt.Fprintf(&b, "\nWrite your deliverable to %s in the workspace.", in.Node.Expect.Path)
	}
	return b.String()
}

func readWFRecord(path string) (workflow.RunRecord, error) {
	var rec workflow.RunRecord
	f, err := os.Open(path)
	if err != nil {
		return rec, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (32<<20)+1))
	if err != nil {
		return rec, err
	}
	if len(raw) > 32<<20 {
		return rec, errors.New("workflow run record exceeds size limit")
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return rec, fmt.Errorf("workflow run record: %w", err)
	}
	return rec, nil
}

// writeWFRecord is the stores' atomic-write pattern for one run record:
// temp file, 0600, sync, rename.
func writeWFRecord(path string, rec workflow.RunRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".wfrun-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// --- run API ---------------------------------------------------------------

func (s *Server) handleWorkflowRunStart(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Inputs    map[string]string `json:"inputs,omitempty"`
		VersionID string            `json:"version_id,omitempty"`
	}
	if err := decodeWorkflowRunRequest(r, &request); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	run, err := s.workflowPublishedRun(r, r.PathValue("wid"), request.Inputs, r.Header.Get("Idempotency-Key"), request.VersionID)
	if err != nil {
		code := http.StatusBadRequest
		var typed *workflowAuthoringError
		if errors.As(err, &typed) {
			code = typed.Status
		}
		writeErr(w, code, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": run.ID})
}

// scheduleJobRunner is the schedule runner's dispatch: a job that names a
// workflow runs it in-process through wfruns (the only place the workflow
// runner lives); anything else keeps spawning the prompt child.
func (m *Manager) scheduleJobRunner(ctx context.Context, job schedule.Job, logs schedule.LogPaths, mirror io.Writer) schedule.RunOutcome {
	if job.AgentID != "" {
		if job.WorkflowID != "" {
			return schedule.RunOutcome{Err: "ambiguous Agent/workflow binding", ExitCode: 1}
		}
		return m.runAgentSchedule(ctx, job, logs, mirror)
	}
	if job.WorkflowID != "" {
		return m.runWorkflowJob(ctx, job)
	}
	return schedRunner(ctx, job, logs, mirror)
}

// runWorkflowJob fires one scheduled workflow run and waits it out. The run
// is the published snapshot's; a suspension on a person ends the job's part
// cleanly — the human resumes through the UI — while a failed or cancelled
// run is the job's error.
func (m *Manager) runWorkflowJob(ctx context.Context, job schedule.Job) schedule.RunOutcome {
	def, err := m.workflows.publishedDefinition(job.WorkflowID)
	if err != nil {
		return schedule.RunOutcome{Err: err.Error(), ExitCode: 1}
	}
	return m.runWorkflowJobQueued(ctx, job, def)
}

// legacyWorkflowJobOutcome is shared by direct scheduler callers and queued
// schedule segments. The segment has already released all model resources.
func legacyWorkflowJobOutcome(out taskqueue.Outcome) schedule.RunOutcome {
	result := schedule.RunOutcome{SessionID: out.SessionID, EndReason: out.EndReason, Summary: out.Summary}
	if out.Status == taskqueue.Failed || out.Status == taskqueue.Interrupted || out.Status == taskqueue.Cancelled {
		result.Err, result.ExitCode = out.Error, 1
		if result.Err == "" {
			result.Err = out.Status
		}
	}
	return result
}

func (s *Server) workflowRunFor(w http.ResponseWriter, r *http.Request, actions ...string) (workflow.RunRecord, bool) {
	rec, ok := s.mgr.wfruns.snapshot(r.PathValue("rid"))
	if !ok || rec.WorkflowID != r.PathValue("wid") {
		writeErr(w, 404, ErrNotFound)
		return rec, false
	}
	action := wfRunRead
	if len(actions) > 0 {
		action = actions[0]
	}
	allowed, err := s.workflowRunAllowed(r, rec, action, r.PathValue("nid"))
	if err != nil {
		workflowAccessError(w, err)
		return rec, false
	}
	if !allowed {
		writeErr(w, 404, ErrNotFound)
		return rec, false
	}
	return rec, true
}

func (s *Server) handleWorkflowRunList(w http.ResponseWriter, r *http.Request) {
	out := []workflow.RunRecord{}
	for _, rec := range s.mgr.wfruns.list(r.PathValue("wid")) {
		allowed, err := s.workflowRunAllowed(r, rec, wfRunRead, "")
		if err != nil {
			workflowAccessError(w, err)
			return
		}
		if allowed {
			out = append(out, s.projectWorkflowRun(r, rec))
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		offset, oerr := strconv.Atoi("0" + r.URL.Query().Get("offset"))
		if err != nil || oerr != nil || limit < 1 || limit > 200 || offset < 0 {
			writeErr(w, http.StatusBadRequest, errors.New("运行列表分页参数无效"))
			return
		}
		if offset >= len(out) {
			out = []workflow.RunRecord{}
		} else {
			out = out[offset:]
			if len(out) > limit {
				out = out[:limit]
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"runs": out})
}

func (s *Server) handleWorkflowRunGet(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.workflowRunFor(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, s.projectWorkflowRun(r, rec))
}

func (s *Server) handleWorkflowRunCancel(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.workflowRunFor(w, r, wfRunCancel); !ok {
		return
	}
	sent, err := s.mgr.cancelWorkflowRun(r.PathValue("rid"))
	if err != nil {
		workflowAccessError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"cancelled": sent})
}

// handleWorkflowRunResume wakes a suspended run with a person's decision for
// one waiting node. The record answers as accepted; the decision itself lands
// on the event stream and the next poll.
func (s *Server) handleWorkflowRunResume(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.workflowRunFor(w, r, wfRunApprove)
	if !ok {
		return
	}
	var body struct {
		Action string            `json:"action"`
		Fields map[string]string `json:"fields"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	nid := r.PathValue("nid")
	decision := workflow.HumanDecision{Node: nid, Action: strings.TrimSpace(body.Action), Values: body.Fields, Actor: s.workflowPrincipal(r).ID}
	err := s.mgr.enqueueWorkflowDecision(rec, decision)
	if err != nil {
		status := 400
		if errors.Is(err, errWFRunPersistence) {
			status = http.StatusServiceUnavailable
		}
		if errors.Is(err, errWFRunNotSuspended) || errors.Is(err, errWFNodeNotWaiting) || errors.Is(err, errWFRunActive) || errors.Is(err, taskqueue.ErrConflict) {
			status = 409
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, 200, s.projectWorkflowRun(r, rec))
}

func (s *Server) handleWorkflowRunArtifact(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.workflowRunFor(w, r, wfArtifactRead)
	if !ok {
		return
	}
	state, ok := rec.Nodes[r.PathValue("nid")]
	if !ok || state.Artifact == "" || !workflow.ValidArtifactPath(state.Artifact) {
		writeErr(w, 404, ErrNotFound)
		return
	}
	workspace := filepath.Join(s.mgr.wfruns.runDir(rec.ID), "workspace")
	root, err := os.OpenRoot(workspace)
	if err != nil {
		writeErr(w, 404, ErrNotFound)
		return
	}
	defer root.Close()
	file, err := root.Open(filepath.FromSlash(state.Artifact))
	if err != nil {
		writeErr(w, 404, ErrNotFound)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeErr(w, 404, ErrNotFound)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name()}))
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

// handleWorkflowRunStream is handleStream for a run's hub, with one change:
// the snapshot frame is the run record (the state authority), not a session
// snapshot. A settled run has no hub left — its stream is the final record
// and a close.
func (s *Server) handleWorkflowRunStream(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.workflowRunFor(w, r)
	if !ok {
		return
	}
	project := func(e Event) bool {
		allowed, err := s.workflowRunAllowed(r, rec, wfRunRead, "")
		if err != nil || !allowed {
			return false
		}
		if nodes := s.workflowReadableNodes(r, rec); nodes != nil {
			if e.WorkflowRun == nil && e.NodeID != "" && !nodes[e.NodeID] {
				return true
			}
			if e.NodeID == "" || !nodes[e.NodeID] {
				e.Artifact = ""
				if e.Error != "" {
					e.Error = "工作流执行异常，请由运行负责人核查"
				}
			}
		}
		if e.WorkflowRun != nil {
			copy := s.projectWorkflowRun(r, *e.WorkflowRun)
			e.WorkflowRun = &copy
		}
		return writeEvent(w, e)
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	from := int64(-1)
	if v := r.URL.Query().Get("from"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, errors.New("from must be a non-negative integer"))
			return
		}
		from = n
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")

	rid := r.PathValue("rid")
	s.mgr.wfruns.mu.Lock()
	run := s.mgr.wfruns.runs[rid]
	s.mgr.wfruns.mu.Unlock()
	headersSent := false
	if run == nil && rec.Status == "queued" {
		// Subscribe before publishing the first live frame. The initial GET
		// already exposes queued; if admission takes longer, return a queued
		// snapshot and let the existing polling fallback reconnect.
		queueWait := time.NewTimer(2 * time.Second)
		defer queueWait.Stop()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for run == nil && rec.Status == "queued" {
			select {
			case <-r.Context().Done():
				return
			case <-queueWait.C:
				w.WriteHeader(http.StatusOK)
				project(Event{Type: EvWorkflowRunStart, TS: nowMS(), RunID: rid, WorkflowID: rec.WorkflowID, WorkflowRun: &rec})
				flusher.Flush()
				return
			case <-tick.C:
				if allowed, err := s.workflowRunAllowed(r, rec, wfRunRead, ""); err != nil || !allowed {
					return
				}
				if current, ok := s.mgr.wfruns.snapshot(rid); ok {
					rec = current
				}
				s.mgr.wfruns.mu.Lock()
				run = s.mgr.wfruns.runs[rid]
				s.mgr.wfruns.mu.Unlock()
			}
		}
	}
	if run == nil {
		// Settled: the record is the whole answer. Suspended reads the same
		// way, but the frame must not call the run ended — it says where the
		// run waits, in the start shape.
		frame := Event{Type: EvWorkflowRunEnd, TS: nowMS(), RunID: rid, WorkflowID: rec.WorkflowID, Error: rec.Error, WorkflowRun: &rec}
		if !workflow.IsTerminal(rec.Status) {
			frame.Type = EvWorkflowRunStart
		}
		if rec.PersistenceError != "" {
			frame.Type, frame.Error = EvError, rec.PersistenceError
		}
		if !headersSent {
			w.WriteHeader(http.StatusOK)
		}
		flusher.Flush()
		project(frame)
		flusher.Flush()
		return
	}

	backlog, ch, cancel := run.hub.Subscribe(from)
	defer cancel()

	if !headersSent {
		w.WriteHeader(http.StatusOK)
	}
	flusher.Flush()

	// A snapshot in the backlog is the hub's session-shaped answer; the run
	// record replaces it, stamped with the same sequence so a reconnect with
	// ?from= lines up exactly.
	for _, e := range backlog {
		if e.Type == EvSnapshot {
			run.mu.RLock()
			current := run.record
			run.mu.RUnlock()
			if !project(Event{Seq: e.Seq, Type: EvWorkflowRunStart, TS: nowMS(), RunID: rid, WorkflowID: rec.WorkflowID, WorkflowRun: &current}) {
				return
			}
			continue
		}
		if !project(e) {
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
			if allowed, err := s.workflowRunAllowed(r, rec, wfRunRead, ""); err != nil || !allowed {
				return
			}
			if _, err := fmt.Fprint(w, ":\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case e, open := <-ch:
			if !open {
				return
			}
			if !project(e) {
				return
			}
			flusher.Flush()
		}
	}
}
