package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
)

// Run and node status vocabulary. Run statuses align with the platform's task
// lifecycle (mcp.go); node statuses are the CI set the plan fixes, rejected
// and waiting_human included. waiting_human is a suspension, not a terminal:
// the node waits on a person and the run sleeps with it.
const (
	RunWorking   = "working"
	RunCompleted = "completed"
	RunFailed    = "failed"
	RunCancelled = "cancelled"

	NodeWaiting      = "waiting"
	NodeRunning      = "running"
	NodeSuccess      = "success"
	NodeFailed       = "failed"
	NodeSkipped      = "skipped"
	NodeCancelled    = "cancelled"
	NodeRejected     = "rejected"
	NodeWaitingHuman = "waiting_human"
)

// Event kinds an EventSink receives, in lockstep with run.json migrations.
const (
	EventRunStart  = "run_start"
	EventNodeStart = "node_start"
	EventNodeEnd   = "node_end"
	EventRunEnd    = "run_end"
)

// DefaultRunTimeout bounds one whole run when the caller sets none.
const DefaultRunTimeout = 30 * time.Minute

// maxParallelNodes bounds how many node executors run at once.
const maxParallelNodes = 4

// HumanState is the record of a wait on a person: the rendered prompt, the
// form and the buttons as presented (self-contained, so the approval UI never
// joins against the definition), the deadline the sweep enforces, and the
// decision once it arrives.
type HumanState struct {
	Prompt    string            `json:"prompt"`
	Fields    []FieldSpec       `json:"fields,omitempty"`
	Actions   []string          `json:"actions"`
	Deadline  string            `json:"deadline"`
	OnTimeout string            `json:"on_timeout"`
	Action    string            `json:"action,omitempty"`
	Values    map[string]string `json:"values,omitempty"`
	DecidedBy string            `json:"decided_by,omitempty"`
	DecidedAt string            `json:"decided_at,omitempty"`
}

// DispatchRecord is one orchestrator delegation, in the shape the dispatch
// events and the run record share.
type DispatchRecord struct {
	Candidate string    `json:"candidate"`
	Task      string    `json:"task"`
	Status    string    `json:"status"` // running in flight; success | failed | cancelled once settled
	Summary   string    `json:"summary,omitempty"`
	Artifact  string    `json:"artifact,omitempty"`
	Error     string    `json:"error,omitempty"`
	Usage     llm.Usage `json:"usage"`
	StartedAt string    `json:"started_at"`
	EndedAt   string    `json:"ended_at,omitempty"`
}

// NodeState is one node's slice of the run record.
type NodeState struct {
	Status    string    `json:"status"`
	StartedAt string    `json:"started_at,omitempty"`
	EndedAt   string    `json:"ended_at,omitempty"`
	Artifact  string    `json:"artifact,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Error     string    `json:"error,omitempty"`
	Usage     llm.Usage `json:"usage"`
	// Branch is the edge condition that released the node (empty for roots);
	// SkippedBy names the upstream whose outcome closed this node's edges;
	// Inputs records what the node's template references resolved to — what
	// the node actually saw.
	SkippedBy string            `json:"skipped_by,omitempty"`
	Branch    string            `json:"branch,omitempty"`
	Inputs    map[string]string `json:"inputs,omitempty"`
	// Human is set while the node waits on a person and keeps the decision.
	Human *HumanState `json:"human,omitempty"`
	// Dispatches are an orchestrator node's delegations, settled and running
	// alike once the node ends.
	Dispatches []DispatchRecord `json:"dispatches,omitempty"`
}

// RunRecord is the authoritative state of one run, persisted as run.json
// after every transition. Definition is the workflow snapshot the run was
// started from: later edits to the workflow move neither it nor this record.
type RunRecord struct {
	ID         string     `json:"id"`
	WorkflowID string     `json:"workflow_id"`
	Revision   int        `json:"revision"`
	Definition Definition `json:"definition"`
	Status     string     `json:"status"`
	Error      string     `json:"error,omitempty"`
	// PersistenceError is a public in-memory diagnostic code, never a raw
	// storage error or an assertion that a
	// failed transition reached run.json. Status/Nodes remain the last durable
	// snapshot while the host quarantines this run until recovery.
	PersistenceError string               `json:"persistence_error,omitempty"`
	Nodes            map[string]NodeState `json:"nodes"`
	// Owner is the scope a run was started with in multi-user mode (nil in
	// single-user). It rides the record so a settled run's visibility rule
	// survives the process that wrote it.
	Owner            *identity.Scope `json:"owner,omitempty"`
	CreatedAt        string          `json:"created_at"`
	EndedAt          string          `json:"ended_at,omitempty"`
	StateRevision    int64           `json:"state_revision,omitempty"`
	ExecutionID      string          `json:"execution_id,omitempty"`
	AppliedCommandID string          `json:"applied_command_id,omitempty"`
	// Capabilities is a request-specific HTTP projection, never execution authority.
	Capabilities *RunCapabilities `json:"capabilities,omitempty"`
	// Inputs belongs to this Run, not to its published graph. It survives
	// approval pauses and explicit retries without changing the version hash.
	Inputs map[string]string `json:"inputs,omitempty"`
	Origin *RunOrigin        `json:"origin,omitempty"`
	// Draft tests use the ordinary durable run lifecycle while identifying the
	// exact saved draft they exercised. Absent on all historical business runs.
	TestRun        bool   `json:"test_run,omitempty"`
	DraftRevision  int    `json:"draft_revision,omitempty"`
	DefinitionHash string `json:"definition_hash,omitempty"`
	// Approvals is a request-specific view of durable assignments and current
	// grants, never execution authority or a source of account permissions.
	Approvals map[string]RunApprovalAssignment `json:"approvals,omitempty"`
}

type ApprovalAssignee struct {
	PrincipalID string `json:"principal_id"`
	DisplayName string `json:"display_name"`
}

type RunApprovalAssignment struct {
	PreventSelfApproval bool               `json:"prevent_self_approval"`
	Assignment          string             `json:"assignment"`
	Assignees           []ApprovalAssignee `json:"assignees"`
	InitialAssignees    []ApprovalAssignee `json:"initial_assignees,omitempty"`
	BlockedReason       string             `json:"blocked_reason,omitempty"`
}

// RunOrigin is the admission audit trail shown beside the same Run in the
// workbench and in an embedded business page. Old records simply omit it.
type RunOrigin struct {
	Source          string `json:"source"`
	SourceID        string `json:"source_id,omitempty"`
	PublicationID   string `json:"publication_id,omitempty"`
	IntegrationID   string `json:"integration_id,omitempty"`
	ExternalSubject string `json:"external_subject,omitempty"`
	BusinessID      string `json:"business_id,omitempty"`
	VersionID       string `json:"version_id,omitempty"`
	RetryOf         string `json:"retry_of,omitempty"`
}

type RunCapabilities struct {
	CanRead               bool     `json:"can_read"`
	CanCancel             bool     `json:"can_cancel"`
	CanApprove            bool     `json:"can_approve"`
	CanRetry              bool     `json:"can_retry"`
	CanReadArtifacts      bool     `json:"can_read_artifacts"`
	CanManageParticipants bool     `json:"can_manage_participants"`
	CanApproveNodes       []string `json:"can_approve_nodes,omitempty"`
	CanReadArtifactNodes  []string `json:"can_read_artifact_nodes,omitempty"`
}

// ArtifactRef names one file a dependency left in the shared workspace.
type ArtifactRef struct {
	Node string `json:"node"`
	Path string `json:"path"`
}

// NodeInput is everything a node execution gets: its own definition with the
// task's template references already resolved, the artifacts its direct
// dependencies produced, and the workspace it runs in.
type NodeInput struct {
	Node      Node
	Artifacts []ArtifactRef
	Workspace string
}

// NodeResult is what a finished node reports back. Artifact is the
// workspace-relative path of the expected deliverable (empty when the node
// declared none), Summary the node's closing text. Dispatches are an
// orchestrator node's delegation log, whatever the node's outcome.
type NodeResult struct {
	Artifact   string
	Summary    string
	Usage      llm.Usage
	Dispatches []DispatchRecord
}

// NodeExecutor runs one node to completion. A returned error fails the node;
// returning because ctx died is reported as cancellation by the Runner.
type NodeExecutor func(ctx context.Context, in NodeInput) (NodeResult, error)

// Event is one state transition. Record is a snapshot taken at the moment of
// the event, so a sink never has to ask the Runner for state.
type Event struct {
	Kind   string
	Node   string
	State  NodeState
	Record RunRecord
}

// EventSink receives every transition, synchronously, in order — the Runner
// emits from its scheduling goroutine only, so the stream is a single
// timeline even when nodes run in parallel. A sink must durably commit before
// publishing and return any failure; the first error stops all new work.
type EventSink interface {
	Publish(Event) error
}

// Runner schedules a definition on a ready queue. Three rules, FastGPT-style:
// an upstream not yet terminal means wait; an active edge means ready; every
// edge closed means skip and cascade. A when:success edge is a barrier — it
// only counts when every success-edged upstream succeeded — so fan-in is
// static AND. Failure is fail-stop by construction: success edges close
// behind a failed node, running nodes finish untouched, and when:failed /
// when:always edges are the escape hatches.
type Runner struct {
	// Record is the pre-built run record: id, workflow, definition snapshot and
	// creation time filled in by the caller. The Runner owns it from here.
	// A record that already carries settled nodes resumes where it left off.
	Record   RunRecord
	Executor NodeExecutor
	Sink     EventSink
	// Workspace is the run's shared working directory, handed to every node.
	Workspace string
	// Timeout bounds one execution segment; zero means DefaultRunTimeout. A
	// segment that hits it ends failed; a run whose parent context dies ends
	// cancelled. Suspended time waiting on a person never counts: the timer
	// dies with the segment and a resume starts a fresh one.
	Timeout time.Duration
	// Resume, when set, is the human decision that woke this run: applied to
	// the named waiting_human node before scheduling continues.
	Resume        *HumanDecision
	lastPersisted RunRecord
	persistErr    error
	stop          context.CancelFunc
}

// Err reports a failed transition write after Run returns. In that case Run
// returns the last successfully persisted record, not an invented terminal
// state. Hosts must surface the error and must not treat working as suspended.
func (r *Runner) Err() error { return r.persistErr }

// HumanDecision ends one node's wait on a person. Timeout settles by the
// node's on_timeout contract instead of an action.
type HumanDecision struct {
	Node      string
	Action    string
	Values    map[string]string
	Timeout   bool
	CommandID string
	Actor     string
	DecidedAt string
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Run executes the graph until it settles or waits on a person. On a failed
// transition write it stops, drains existing executors, and returns the last
// durable record; callers must inspect Err rather than treating it as a wait.
func (r *Runner) Run(ctx context.Context) RunRecord {
	parent := ctx
	if r.Timeout <= 0 {
		r.Timeout = DefaultRunTimeout
	}
	ctx, stop := context.WithTimeout(ctx, r.Timeout)
	defer stop()
	r.stop, r.persistErr = stop, nil

	// Own the record before mutating it. A resumed run is handed the record a
	// caller may still be serving (the web layer's live copy); writing through
	// its shared maps and Human pointers would race with that reader.
	r.Record = r.recordCopy()
	r.lastPersisted = r.recordCopy()
	if r.Record.Nodes == nil {
		r.Record.Nodes = map[string]NodeState{}
	}
	for _, n := range r.Record.Definition.Nodes {
		if _, ok := r.Record.Nodes[n.ID]; !ok {
			r.Record.Nodes[n.ID] = NodeState{Status: NodeWaiting}
		}
	}
	// A durable node_start means external work may already have happened.
	// Direct callers must not bypass the host's restart recovery and replay it.
	interrupted := false
	for _, state := range r.Record.Nodes {
		interrupted = interrupted || state.Status == NodeRunning
	}
	if interrupted {
		for id, state := range r.Record.Nodes {
			switch state.Status {
			case NodeRunning:
				state.Status, state.Error, state.EndedAt = NodeFailed, "interrupted execution; outcome unknown, not replayed", now()
			case NodeWaiting:
				state.Status, state.EndedAt = NodeSkipped, now()
			case NodeWaitingHuman:
				state.Status, state.EndedAt = NodeCancelled, now()
			}
			r.Record.Nodes[id] = state
		}
		r.Record.Status, r.Record.Error, r.Record.EndedAt = RunFailed, "interrupted execution; outcome unknown, not replayed", now()
		r.emit(EventRunEnd, "", NodeState{})
		return r.Record
	}
	r.Record.Status = RunWorking
	if !r.emit(EventRunStart, "", NodeState{}) {
		return r.Record
	}
	// A resumed run settles its woken node first, so the decision is the
	// first thing after the fresh run_start on the event timeline.
	if r.Resume != nil {
		r.applyDecision(*r.Resume)
		if r.persistErr != nil {
			return r.Record
		}
	}

	if _, err := r.Record.Definition.TopoOrder(); err != nil {
		r.failAll(err)
		return r.Record
	}

	// All scheduler state lives on this goroutine; node workers only run the
	// executor and report back over done, so record mutations never race.
	// Nodes already terminal in the record (a resumed run's history) start
	// settled and never run again.
	type doneMsg struct {
		id  string
		res NodeResult
		err error
	}
	done := make(chan doneMsg, len(r.Record.Definition.Nodes))
	sem := make(chan struct{}, maxParallelNodes)
	settled := map[string]bool{}
	running := map[string]bool{}
	for _, n := range r.Record.Definition.Nodes {
		if settledStatus(r.Record.Nodes[n.ID].Status) {
			settled[n.ID] = true
		}
	}
	settle := func(id string, state NodeState) {
		if r.persistErr != nil {
			return
		}
		settled[id] = true
		r.Record.Nodes[id] = state
		r.emit(EventNodeEnd, id, state)
	}

	// launch starts one ready node, slot permitting. A node that fails
	// template assembly never holds a worker: it starts and fails in place.
	// Reports whether the node moved out of waiting.
	launch := func(n Node, branch string) bool {
		if r.persistErr != nil || ctx.Err() != nil {
			return false
		}
		select {
		case sem <- struct{}{}:
		default:
			return false
		}
		state := NodeState{Status: NodeRunning, StartedAt: now(), Branch: branch}
		task, inputs, err := evalTemplates(n.Task, r.Record.Nodes, r.Workspace, r.Record.Inputs)
		if len(inputs) > 0 {
			state.Inputs = inputs
		}
		r.Record.Nodes[n.ID] = state
		if !r.emit(EventNodeStart, n.ID, state) {
			<-sem
			return false
		}
		if err != nil {
			<-sem
			state.Status, state.Error, state.EndedAt = NodeFailed, err.Error(), now()
			if r.Record.Error == "" {
				r.Record.Error = state.Error
			}
			settle(n.ID, state)
			return true
		}
		node := n
		node.Task = task
		artifacts := make([]ArtifactRef, 0, len(n.After))
		for _, dep := range n.After {
			if st := r.Record.Nodes[dep.Node]; st.Status == NodeSuccess && st.Artifact != "" {
				artifacts = append(artifacts, ArtifactRef{Node: dep.Node, Path: st.Artifact})
			}
		}
		running[n.ID] = true
		go func() {
			res, execErr := r.Executor(ctx, NodeInput{Node: node, Artifacts: artifacts, Workspace: r.Workspace})
			done <- doneMsg{id: n.ID, res: res, err: execErr}
			<-sem
		}()
		return true
	}

	finish := func(msg doneMsg) {
		delete(running, msg.id)
		if r.persistErr != nil {
			return
		}
		state := r.Record.Nodes[msg.id]
		node := r.Record.Definition.node(msg.id)
		state.EndedAt = now()
		state.Summary, state.Usage = msg.res.Summary, msg.res.Usage
		if len(msg.res.Dispatches) > 0 {
			state.Dispatches = append([]DispatchRecord(nil), msg.res.Dispatches...)
		}
		switch {
		case msg.err == nil:
			state.Status = NodeSuccess
			state.Artifact, state.Summary, state.Usage = msg.res.Artifact, msg.res.Summary, msg.res.Usage
			if node.Approval {
				// The agent's output becomes a draft a person must approve:
				// execution ends here, the node waits instead of settling.
				state.Status = NodeWaitingHuman
				state.EndedAt = ""
				state.Human = &HumanState{
					Prompt:    approvalPrompt(node, state),
					Actions:   EffectiveActions(node),
					Deadline:  time.Now().UTC().Add(effectiveTimeout(node)).Format(time.RFC3339Nano),
					OnTimeout: EffectiveOnTimeout(node),
				}
			}
		case ctx.Err() != nil || errors.Is(msg.err, context.Canceled):
			state.Status = NodeCancelled
			state.Error = msg.err.Error()
		default:
			state.Status = NodeFailed
			state.Error = msg.err.Error()
			if r.Record.Error == "" {
				r.Record.Error = state.Error
			}
		}
		if state.Status == NodeWaitingHuman {
			r.Record.Nodes[msg.id] = state
			r.emit(EventNodeEnd, msg.id, state)
			return
		}
		settle(msg.id, state)
	}

	// presentHuman puts a ready human node into its wait: the prompt rendered
	// (a render failure settles the node failed, same contract as task
	// evaluation), the effective form and deadline frozen into the record.
	presentHuman := func(n Node, branch string) {
		if r.persistErr != nil {
			return
		}
		state := NodeState{Status: NodeWaitingHuman, StartedAt: now(), Branch: branch}
		prompt, inputs, err := evalTemplates(n.Prompt, r.Record.Nodes, r.Workspace, r.Record.Inputs)
		if len(inputs) > 0 {
			state.Inputs = inputs
		}
		if err != nil {
			r.Record.Nodes[n.ID] = state
			if !r.emit(EventNodeStart, n.ID, state) {
				return
			}
			state.Status, state.Error, state.EndedAt = NodeFailed, err.Error(), now()
			if r.Record.Error == "" {
				r.Record.Error = state.Error
			}
			settle(n.ID, state)
			return
		}
		// One emit, card complete: an observer must never see waiting_human
		// without the prompt and deadline that explain it.
		state.Human = &HumanState{
			Prompt:    prompt,
			Fields:    append([]FieldSpec(nil), n.Fields...),
			Actions:   EffectiveActions(n),
			Deadline:  time.Now().UTC().Add(effectiveTimeout(n)).Format(time.RFC3339Nano),
			OnTimeout: EffectiveOnTimeout(n),
		}
		r.Record.Nodes[n.ID] = state
		r.emit(EventNodeStart, n.ID, state)
	}

	// schedule walks the definition in order, presenting every ready human
	// node, launching every ready agent node the semaphore admits, and
	// skipping every node whose edges have all closed, until nothing moves.
	schedule := func() {
		for {
			if r.persistErr != nil || ctx.Err() != nil {
				return
			}
			progressed := false
			for _, n := range r.Record.Definition.Nodes {
				if r.persistErr != nil || ctx.Err() != nil {
					return
				}
				if settled[n.ID] || running[n.ID] {
					continue
				}
				if r.Record.Nodes[n.ID].Status == NodeWaitingHuman {
					continue
				}
				waiting := false
				for _, dep := range n.After {
					if !settled[dep.Node] {
						waiting = true
						break
					}
				}
				if waiting {
					continue
				}
				branch, ready := releaseBranch(n, r.Record.Nodes)
				if !ready {
					settle(n.ID, NodeState{Status: NodeSkipped, EndedAt: now(), SkippedBy: skipCulprit(n, r.Record.Nodes)})
					progressed = true
					continue
				}
				if n.Kind == KindHuman {
					presentHuman(n, branch)
					progressed = true
					continue
				}
				if launch(n, branch) {
					progressed = true
				}
				// No slot free: the node stays ready, a completion re-runs
				// this pass. Skips later in the walk still settle.
			}
			if !progressed {
				return
			}
		}
	}

	cancelWaiting := func() {
		for _, n := range r.Record.Definition.Nodes {
			if settled[n.ID] || running[n.ID] {
				continue
			}
			settle(n.ID, NodeState{Status: NodeCancelled, EndedAt: now()})
		}
	}

	for {
		if r.persistErr != nil {
			// Cancellation requests the in-flight executors to stop; drain them
			// without publishing unsaved outcomes or scheduling another node.
			for len(running) > 0 {
				finish(<-done)
			}
			return r.Record
		}
		if ctx.Err() != nil {
			cancelWaiting()
			for len(running) > 0 {
				finish(<-done)
			}
			break
		}
		schedule()
		if r.persistErr != nil {
			continue
		}
		if len(settled) == len(r.Record.Definition.Nodes) {
			break
		}
		if len(running) == 0 {
			// Quiescent. Nodes waiting on a person hold the run open: leave
			// the status working and hand the record back unfinished — a
			// resume (or the timeout sweep) wakes it. With none, every
			// remaining node is unreachable in a valid DAG.
			if r.hasWaitingHuman() {
				return r.Record
			}
			break
		}
		select {
		case msg := <-done:
			finish(msg)
		case <-ctx.Done():
		}
	}

	if r.persistErr != nil {
		return r.Record
	}
	r.Record.EndedAt = now()
	switch {
	case parent.Err() != nil:
		r.Record.Status, r.Record.Error = RunCancelled, "cancelled"
	case ctx.Err() != nil:
		r.Record.Status, r.Record.Error = RunFailed, "run timed out"
	default:
		r.Record.Status = RunCompleted
		for id, st := range r.Record.Nodes {
			if st.Status == NodeFailed {
				r.Record.Status = RunFailed
				if r.Record.Error == "" {
					r.Record.Error = r.Record.Nodes[id].Error
				}
				break
			}
		}
		if r.Record.Status == RunCompleted {
			for _, st := range r.Record.Nodes {
				if st.Status == NodeCancelled {
					r.Record.Status, r.Record.Error = RunCancelled, "cancelled"
					break
				}
			}
		}
	}
	r.emit(EventRunEnd, "", NodeState{})
	return r.Record
}

// edgeActive reports whether one edge's condition matches an upstream's
// terminal outcome. when:always fires on every settled outcome of an actual
// execution — never on skipped or cancelled, or the skip cascade could not
// propagate past it.
func edgeActive(when, status string) bool {
	switch when {
	case WhenSuccess:
		return status == NodeSuccess
	case WhenFailed:
		return status == NodeFailed
	case WhenRejected:
		return status == NodeRejected
	case WhenAlways:
		return status == NodeSuccess || status == NodeFailed || status == NodeRejected
	}
	return false
}

// releaseBranch decides a node whose upstreams are all terminal: the first
// active edge in after order releases it, and that edge's when is the branch
// it took. A success edge only counts when every success-edged upstream
// succeeded — the fan-in barrier. A node with no dependencies is a root and
// always ready.
func releaseBranch(n Node, states map[string]NodeState) (string, bool) {
	if len(n.After) == 0 {
		return "", true
	}
	barrier := true
	for _, dep := range n.After {
		if dep.when() == WhenSuccess && states[dep.Node].Status != NodeSuccess {
			barrier = false
			break
		}
	}
	for _, dep := range n.After {
		if dep.when() == WhenSuccess && !barrier {
			continue
		}
		if edgeActive(dep.when(), states[dep.Node].Status) {
			return dep.when(), true
		}
	}
	return "", false
}

// skipCulprit names the upstream a skip is blamed on: the first success-edged
// upstream that did not succeed (the barrier breaker), else the first
// upstream at all — every edge is closed by definition here, so the first
// one's condition went unmet.
func skipCulprit(n Node, states map[string]NodeState) string {
	for _, dep := range n.After {
		if dep.when() == WhenSuccess && states[dep.Node].Status != NodeSuccess {
			return dep.Node
		}
	}
	if len(n.After) > 0 {
		return n.After[0].Node
	}
	return ""
}

// applyDecision settles a woken node from its wait. approve-class actions
// succeed (a human node's artifact is its decision file), reject settles
// rejected — a neutral outcome that triggers no fail-stop — and a timeout
// follows the node's on_timeout contract.
func (r *Runner) applyDecision(d HumanDecision) {
	state, ok := r.Record.Nodes[d.Node]
	if !ok || state.Status != NodeWaitingHuman || state.Human == nil {
		return // the caller validated; the engine stays defensive
	}
	node := r.Record.Definition.node(d.Node)
	state.EndedAt = now()
	switch {
	case d.Timeout && state.Human.OnTimeout == "fail":
		state.Status = NodeFailed
		state.Error = "human review timed out"
		if r.Record.Error == "" {
			r.Record.Error = state.Error
		}
	case d.Timeout || d.Action == "reject":
		state.Status = NodeRejected
	default:
		state.Status = NodeSuccess
		if node.Kind == KindHuman {
			if err := r.writeHumanInput(node.ID, d); err != nil {
				state.Status, state.Error = NodeFailed, err.Error()
				if r.Record.Error == "" {
					r.Record.Error = state.Error
				}
				break
			}
			state.Artifact = humanArtifactPath(node.ID)
			if state.Summary == "" {
				state.Summary = "human: " + d.Action
			}
		}
	}
	state.Human.Action = d.Action
	if d.Timeout {
		state.Human.Action = "timeout"
	}
	if len(d.Values) > 0 {
		state.Human.Values = d.Values
	}
	state.Human.DecidedBy, state.Human.DecidedAt = d.Actor, d.DecidedAt
	if d.CommandID != "" {
		r.Record.AppliedCommandID = d.CommandID
	}
	r.Record.Nodes[d.Node] = state
	r.emit(EventNodeEnd, d.Node, state)
}

// writeHumanInput persists a human node's decision as its artifact, so
// downstream nodes and the artifact endpoint read it like any other product.
func (r *Runner) writeHumanInput(id string, d HumanDecision) error {
	if r.Workspace == "" {
		return errors.New("run workspace is not set")
	}
	dir := filepath.Join(r.Workspace, "_human")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	payload := map[string]any{
		"node": id, "action": d.Action, "fields": d.Values, "decided_at": now(),
	}
	if d.Actor != "" {
		payload["decided_by"] = d.Actor
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, id+".json"), b, 0o600)
}

func humanArtifactPath(id string) string { return "_human/" + id + ".json" }

// approvalPrompt is the synthesized review card for an approval-gated agent
// node: what it concluded, and where it left the deliverable.
func approvalPrompt(n Node, st NodeState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Node %q finished and asks for approval.", n.ID)
	if st.Summary != "" {
		fmt.Fprintf(&b, "\n\nSummary:\n%s", st.Summary)
	}
	if st.Artifact != "" {
		fmt.Fprintf(&b, "\n\nArtifact: %s", st.Artifact)
	}
	return b.String()
}

func effectiveTimeout(n Node) time.Duration {
	if n.Timeout != "" {
		if d, err := time.ParseDuration(n.Timeout); err == nil && d > 0 {
			return d
		}
	}
	return 72 * time.Hour
}

// hasWaitingHuman reports whether any node currently waits on a person.
func (r *Runner) hasWaitingHuman() bool {
	for _, st := range r.Record.Nodes {
		if st.Status == NodeWaitingHuman {
			return true
		}
	}
	return false
}

// settledStatus reports whether a node status is terminal for scheduling:
// the node never runs again in this run.
func settledStatus(s string) bool {
	switch s {
	case NodeSuccess, NodeFailed, NodeSkipped, NodeCancelled, NodeRejected:
		return true
	}
	return false
}

// failAll ends a run that could not start: every node skipped, the run failed
// with the definition error as the explanation.
func (r *Runner) failAll(err error) {
	for _, n := range r.Record.Definition.Nodes {
		if r.persistErr != nil {
			return
		}
		state := NodeState{Status: NodeSkipped, EndedAt: now()}
		r.Record.Nodes[n.ID] = state
		r.emit(EventNodeEnd, n.ID, state)
	}
	if r.persistErr != nil {
		return
	}
	r.Record.Status = RunFailed
	r.Record.Error = err.Error()
	r.Record.EndedAt = now()
	r.emit(EventRunEnd, "", NodeState{})
}

func (r *Runner) emit(kind, node string, state NodeState) bool {
	if r.persistErr != nil {
		return false
	}
	r.Record.StateRevision++
	event := Event{Kind: kind, Node: node, State: state, Record: r.recordCopy()}
	if r.Sink != nil {
		if err := r.Sink.Publish(event); err != nil {
			r.persistErr = fmt.Errorf("persist workflow %s %s: %w", kind, node, err)
			r.Record = r.lastPersisted
			if r.stop != nil {
				r.stop()
			}
			return false
		}
	}
	r.lastPersisted = event.Record
	return true
}

// recordCopy deep-copies the parts of the record the Runner keeps mutating,
// so a snapshot handed to a sink never changes under the reader.
func (r *Runner) recordCopy() RunRecord {
	out := r.Record
	out.Inputs = maps.Clone(r.Record.Inputs)
	if r.Record.Approvals != nil {
		out.Approvals = make(map[string]RunApprovalAssignment, len(r.Record.Approvals))
		for id, a := range r.Record.Approvals {
			a.Assignees = append([]ApprovalAssignee(nil), a.Assignees...)
			a.InitialAssignees = append([]ApprovalAssignee(nil), a.InitialAssignees...)
			out.Approvals[id] = a
		}
	}
	if r.Record.Origin != nil {
		origin := *r.Record.Origin
		out.Origin = &origin
	}
	out.Definition.Nodes = append([]Node(nil), r.Record.Definition.Nodes...)
	for i := range out.Definition.Nodes {
		if policy := out.Definition.Nodes[i].ApprovalPolicy; policy != nil {
			copy := *policy
			copy.Approvers = append([]string(nil), policy.Approvers...)
			out.Definition.Nodes[i].ApprovalPolicy = &copy
		}
	}
	if r.Record.Nodes == nil {
		out.Nodes = nil
		return out
	}
	nodes := make(map[string]NodeState, len(r.Record.Nodes))
	for id, state := range r.Record.Nodes {
		if state.Inputs != nil {
			inputs := make(map[string]string, len(state.Inputs))
			for k, v := range state.Inputs {
				inputs[k] = v
			}
			state.Inputs = inputs
		}
		if state.Human != nil {
			human := *state.Human
			human.Fields = append([]FieldSpec(nil), state.Human.Fields...)
			human.Actions = append([]string(nil), state.Human.Actions...)
			if state.Human.Values != nil {
				values := make(map[string]string, len(state.Human.Values))
				for k, v := range state.Human.Values {
					values[k] = v
				}
				human.Values = values
			}
			state.Human = &human
		}
		state.Dispatches = append([]DispatchRecord(nil), state.Dispatches...)
		nodes[id] = state
	}
	out.Nodes = nodes
	return out
}

// IsTerminal reports whether a run status is final.
func IsTerminal(status string) bool {
	return status == RunCompleted || status == RunFailed || status == RunCancelled
}
