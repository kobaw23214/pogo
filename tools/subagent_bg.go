package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/mcpobserve"
)

// maxBackgroundTasks bounds how many delegated tasks one session may have
// outstanding at once, queued ones included.
//
// The running subset is still bounded by Subagent.Concurrency; this cap is on
// the registry itself, so a model that fans out without limit gets a refusal it
// can act on instead of an ever-growing queue the session would spend hours
// working through.
const maxBackgroundTasks = 8

// BgTask is one read-only delegation running in the background. Its ID names
// the task in the registry, approval requests and transcript events.
type BgTask struct {
	ID      string
	AgentID string
	// Captured at dispatch, before queueing. Only one-shot children use these;
	// retained children already have an immutable model in their run record.
	model, provider string
	Mode            string
	Task            string
	// StartedAt is when the task was dispatched, which for a queued task
	// predates its actual start: the queue wait is part of what someone asking
	// about progress wants to know.
	StartedAt time.Time
}

// BgResult is what one finished background task delivers. Text and Err are the
// same two halves the synchronous path hands back: a finished task's Text is
// its answer, a failed one's Err says what happened, and Details carries the
// usage and the transcript path either way.
type BgResult struct {
	Text    string
	Details SubagentDetails
	Err     error
}

// BgHooks are how a session learns about its background tasks. They are called
// from runner goroutines and must not block; a nil hook means "not interested".
//
// OnComplete is the load-bearing one: it fires exactly once per task, on
// success, failure and cancellation alike. Anything else would leave the
// parent waiting for a notification that never comes, which is the failure
// mode background delegation exists to remove.
type BgHooks struct {
	OnStart    func(BgTask)
	OnFrame    func(taskID string, frame json.RawMessage)
	OnComplete func(BgTask, BgResult)
}

// BgRunner owns one session's backgrounded delegations: their processes, their
// progress events, and the delivery of their results.
//
// It exists because the synchronous path holds the parent's whole run hostage
// to a child that may take minutes: the tool call's goroutine sits in
// cmd.Wait, and the loop cannot answer the user until every child in the batch
// comes back. A backgrounded call instead answers immediately with the task id
// — the tool_use/tool_result pairing the API requires is satisfied by the
// dispatch itself — and the real result re-enters the conversation later
// through OnComplete, which the web session turns into a notification message.
type BgRunner struct {
	// ctx is the session's lifetime: tasks are children of it rather than of
	// any one run, which is what lets them outlive the turn that dispatched
	// them. Cancelling it (session close, eviction) stops every task.
	ctx   context.Context
	hooks BgHooks

	mu     sync.Mutex
	tasks  map[string]*bgHandle
	closed bool
	wg     sync.WaitGroup
}

type bgHandle struct {
	task   BgTask
	cancel context.CancelFunc
	// running is false while the task waits for a concurrency slot. The
	// distinction matters to anyone asking how far along it is: queued and
	// stuck look identical from the outside.
	running bool
}

func NewBgRunner(ctx context.Context, hooks BgHooks) *BgRunner {
	return &BgRunner{ctx: ctx, hooks: hooks, tasks: make(map[string]*bgHandle)}
}

// knows reports whether an id is one of this runner's tasks, which is the same
// question as "is this child backgrounded".
//
// Asked by wireBrowser while the child is being spawned, which is inside the
// window where the id is registered: dispatch adds it before run starts and only
// removes it once the child is finished. A foreground child was never added.
func (r *BgRunner) knows(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.tasks[id]
	return ok
}

// dispatch starts the delegation in the background and answers the tool call
// immediately. The answer text is written for the model: the two failure modes
// of a backgrounded call are the model waiting for a result that only ever
// arrives as a message, and the model re-issuing the task because the ack did
// not look like acceptance, so the text rules both out explicitly.
func (r *BgRunner) dispatch(s *Subagent, exe, id, mode, task string, timeout time.Duration, parents ...context.Context) (Result, error) {
	return r.dispatchRun(s, exe, id, mode, task, timeout, nil, parents...)
}

func (r *BgRunner) dispatchRun(s *Subagent, exe, id, mode, task string, timeout time.Duration, retained *subagentRun, parents ...context.Context) (Result, error) {
	if mode != ModeExplore {
		return Result{}, fmt.Errorf("only explore delegations may run in the background")
	}
	r.mu.Lock()
	if r.closed || r.ctx.Err() != nil {
		r.mu.Unlock()
		return Result{}, fmt.Errorf("the background runner is closed")
	}
	if len(r.tasks) >= maxBackgroundTasks {
		r.mu.Unlock()
		return Result{}, fmt.Errorf("there are already %d background subagents outstanding, which "+
			"is the session limit: wait for one to report back (its result arrives as a "+
			"notification message) before delegating more", maxBackgroundTasks)
	}
	parent := r.ctx
	if len(parents) > 0 && parents[0] != nil {
		parent = parents[0]
	}
	// The lifetime remains session-bound, while identity/trace come from the actual dispatch.
	ctx, cancel := context.WithCancel(mcpobserve.WithMetadata(r.ctx, mcpobserve.MetadataFrom(parent)))
	t := BgTask{ID: id, Mode: mode, Task: task, StartedAt: time.Now()}
	if retained != nil {
		t.AgentID = retained.record.AgentID
	} else {
		t.model, t.provider = s.pickDelegationModel(mode)
	}
	r.tasks[id] = &bgHandle{task: t, cancel: cancel}
	r.wg.Add(1)
	r.mu.Unlock()

	// Text partials are dropped on purpose: they exist for a terminal renderer,
	// and the frames carry everything an interface can structure.
	onPartial := func(p Partial) {
		if len(p.Frame) > 0 && r.hooks.OnFrame != nil {
			r.hooks.OnFrame(id, p.Frame)
		}
	}
	if r.hooks.OnStart != nil {
		r.hooks.OnStart(t)
	}

	go r.run(s, ctx, t, exe, timeout, onPartial, retained)
	d := SubagentDetails{ID: id, Mode: mode, Background: true}
	if retained != nil {
		d.AgentID, d.RunID, d.Status, d.Session, d.Model = t.AgentID, id, "queued", retained.record.Session, retained.record.Model
	}
	text := fmt.Sprintf("Subagent %s is now running in the background (mode %q). Its result "+
		"will arrive later as a <task-notification> message — do not sleep, poll, or re-issue "+
		"the task to wait for it. Continue with other work, or tell the user what you delegated. "+
		"The subagent_tasks tool lists what is still outstanding.", id, mode)
	if retained != nil {
		text += fmt.Sprintf(" Retained agent_id: %s. After this run finishes, use action=continue with that agent_id for a related follow-up.", t.AgentID)
	}
	return Result{
		Text:    text,
		Details: d,
	}, nil
}

// run is the task's whole lifecycle: queue for a slot, run the child, deliver
// the outcome. Everything blocking lives here rather than in dispatch so the
// parent's tool call never waits on any of it.
func (r *BgRunner) run(s *Subagent, ctx context.Context, t BgTask, exe string,
	timeout time.Duration, onPartial func(Partial), retained *subagentRun) {
	defer r.wg.Done()
	if retained == nil {
		defer s.releaseExplore(t.provider)
	}

	var res Result
	var err error
	// Queued here rather than at dispatch time: the concurrency limit is about
	// how many children run at once, not about how many the model may delegate,
	// and making the caller wait for the queue would re-introduce the blocking
	// this type exists to remove.
	if aerr := s.acquire(ctx); aerr != nil {
		err = aerr
		if retained != nil {
			res, err = retained.finish(s, Result{}, err)
		}
	} else {
		r.mu.Lock()
		if h, ok := r.tasks[t.ID]; ok {
			h.running = true
		}
		r.mu.Unlock()
		if retained != nil {
			res, err = s.runRetained(ctx, exe, retained, t.Task, timeout, onPartial)
		} else {
			res, err = s.exploreWithModel(ctx, exe, t.ID, t.Task, timeout, onPartial, t.model)
		}
		s.release()
	}

	r.mu.Lock()
	delete(r.tasks, t.ID)
	r.mu.Unlock()
	if r.hooks.OnComplete != nil {
		d, _ := res.Details.(SubagentDetails)
		r.hooks.OnComplete(t, BgResult{Text: res.Text, Details: d, Err: err})
	}
}

// BgStatus is the registry's view of one outstanding task.
type BgStatus struct {
	BgTask
	Running bool
}

// List reports what is still outstanding, oldest first.
func (r *BgRunner) List() []BgStatus {
	r.mu.Lock()
	out := make([]BgStatus, 0, len(r.tasks))
	for _, h := range r.tasks {
		out = append(out, BgStatus{BgTask: h.task, Running: h.running})
	}
	r.mu.Unlock()
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].StartedAt.Before(out[j-1].StartedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// InFlight counts outstanding tasks, queued ones included. The web session's
// eviction guard reads it: a session with children still running is not idle,
// however quiet its run slot is.
func (r *BgRunner) InFlight() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tasks)
}

// Cancel stops one task. The completion hook still fires — with the
// cancellation as the outcome — so a cancel never strands a waiter.
func (r *BgRunner) Cancel(id string) bool {
	r.mu.Lock()
	h, ok := r.tasks[id]
	r.mu.Unlock()
	if !ok {
		return false
	}
	h.cancel()
	return true
}

// Close refuses new dispatches, cancels every task, and waits for the runners
// and their completion hooks. Call outside session locks and never from a hook.
// When it returns the owner may remove transcripts and uploads safely.
func (r *BgRunner) Close() {
	r.mu.Lock()
	r.closed = true
	handles := make([]*bgHandle, 0, len(r.tasks))
	for _, h := range r.tasks {
		handles = append(handles, h)
	}
	r.mu.Unlock()
	for _, h := range handles {
		h.cancel()
	}
	r.wg.Wait()
}

// SubagentTasks lists the session's outstanding background delegations. It
// exists because "how far along is it" is the first question backgrounding
// makes askable, and the model has no other way to answer it: the notification
// channel is push-only, and the progress frames are the interface's, not the
// model's.
type SubagentTasks struct {
	Bg *BgRunner
}

func (*SubagentTasks) Name() string { return "subagent_tasks" }

func (*SubagentTasks) Description() string {
	return "List this session's outstanding background subagents: task id, mode, how long " +
		"each has been going, and whether it is running or still queued for a slot. Use it " +
		"when the user asks how delegated work is progressing. Finished tasks are not " +
		"listed — they report back on their own as notification messages."
}

func (*SubagentTasks) InputSchema() map[string]any {
	// Hand-written rather than object(nil, nil): the helper leaves properties
	// JSON-null, which at least one provider rejects the same way it rejected a
	// null required list.
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (*SubagentTasks) ExecutionMode() ExecutionMode { return Parallel }

func (t *SubagentTasks) Execute(context.Context, json.RawMessage) (Result, error) {
	list := t.Bg.List()
	if len(list) == 0 {
		return Result{Text: "No background subagents are outstanding."}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d background subagent(s) outstanding:\n", len(list))
	for _, st := range list {
		state := "queued"
		if st.Running {
			state = "running"
		}
		fmt.Fprintf(&b, "- %s (%s, %s for %s): %s\n",
			st.ID, st.Mode, state, time.Since(st.StartedAt).Round(time.Second), firstLine(st.Task))
	}
	return Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}
