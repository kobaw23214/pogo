package web

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/sentinelexec"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

func newSentinelHarness(t *testing.T, turns []llm.Response) (*harness, *studioCapture) {
	t.Helper()
	return newStudioHarness(t, turns, func(c *Config) {
		c.Skills = bindingTestSkills(t)
		var err error
		c.Cwd, err = filepath.EvalSymlinks(c.Cwd)
		if err != nil {
			t.Fatal(err)
		}
		c.SessionDir, err = filepath.EvalSymlinks(c.SessionDir)
		if err != nil {
			t.Fatal(err)
		}
	})
}

func sentinelTestSession(t *testing.T, h *harness) *Session {
	t.Helper()
	loaded := h.mgr.Skills()
	if !slices.ContainsFunc(loaded, func(sk skills.Skill) bool { return sk.Name == "background-tasks" && sk.Source == "embedded" }) {
		loaded = bindingTestSkills(t)
		h.mgr.SetSkills(loaded)
	}
	a := postStudioAgent(t, h, studioAgentCapsBody("Generic sentinel", "ready", map[string]any{
		"skills":       []string{bindingSkillID(t, loaded, "background-tasks")},
		"instructions": "Carry out the user's standing instruction.",
	}), 201)
	return h.session(startStudioConversation(t, h, a.ID))
}
func sentinelTestBundle(t *testing.T, s *Session) string {
	t.Helper()
	dir := filepath.Join(s.cwd, "sentinels", "arbitrary-source")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("# original immutable script\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "helper.py"), []byte("VALUE = 'original dependency'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return "sentinels/arbitrary-source/main.py"
}
func sentinelTestCreate(t *testing.T, h *harness, s *Session) sentinel.Watch {
	t.Helper()
	script := sentinelTestBundle(t, s)
	b := h.mgr.sentinelBackend(s)
	r, err := (&tools.Watch{Backend: b}).Execute(context.Background(), json.RawMessage(studioBody(map[string]any{"action": "create", "title": "Any protocol watcher", "task": "Investigate meaningful changes in a custom source.", "mode": "poll", "script_path": script, "args": []string{"--mode", "custom"}})))
	if err != nil {
		t.Fatal(err)
	}
	var w sentinel.Watch
	if err = json.Unmarshal([]byte(r.Text), &w); err != nil {
		t.Fatal(err)
	}
	return w
}
func sentinelTestWake(id string) sentinelWakeInput {
	return sentinelWakeInput{ID: id, WatchID: "test_watch", BatchID: "test_batch", Task: "Investigate the custom source and save a report.", MaxTurns: 5, Events: []sentinel.Event{{ID: "event_1", Key: "custom:42:v2", Summary: "An arbitrary external state changed", Data: json.RawMessage(`{"protocol":"custom","version":2}`)}}}
}
func sentinelWaitRun(t *testing.T, b *sentinelWakeBinding) {
	t.Helper()
	select {
	case <-b.run.done:
	case <-time.After(5 * time.Second):
		b.run.cancel()
		t.Fatal("sentinel run did not complete")
	}
}
func sentinelModelCalls(cap *studioCapture) int {
	systems, _, _, _ := cap.snapshot()
	return len(systems)
}

func TestSentinelCreateSealsGenericBundleAndIsPaused(t *testing.T) {
	h, cap := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	w := sentinelTestCreate(t, h, s)
	if w.Spec.Enabled || w.Spec.Mode != "poll" || w.Spec.Workspace != s.cwd || w.Spec.AgentID != s.studioID || w.Spec.OwnerID != sessionResultOwnerKey(s.owner) {
		t.Fatalf("invalid trusted binding: %+v", w.Spec)
	}
	if w.Spec.ScriptDir == filepath.Dir(filepath.Join(s.cwd, "sentinels/arbitrary-source/main.py")) || !reflect.DeepEqual(w.Spec.Command[len(w.Spec.Command)-2:], []string{"--mode", "custom"}) {
		t.Fatalf("unsealed script or arguments: %+v", w.Spec)
	}
	saved := filepath.Join(w.Spec.ScriptDir, "main.py")
	data, err := os.ReadFile(saved)
	if err != nil || !strings.Contains(string(data), "original") {
		t.Fatalf("sealed entry=%q %v", data, err)
	}
	if _, err = os.Stat(filepath.Join(w.Spec.ScriptDir, "helper.py")); err != nil {
		t.Fatal("script dependency was not sealed", err)
	}
	if err = os.WriteFile(filepath.Join(s.cwd, "sentinels/arbitrary-source/main.py"), []byte("modified source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = verifySentinelBundle(w.Spec.ScriptDir); err != nil {
		t.Fatalf("editing original changed saved version: %v", err)
	}
	if data, err = os.ReadFile(saved); err != nil || !strings.Contains(string(data), "original") {
		t.Fatalf("source edit leaked: %q %v", data, err)
	}
	if err = os.Chmod(saved, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(saved, []byte("tampered registration"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = verifySentinelBundle(w.Spec.ScriptDir); err == nil {
		t.Fatal("modified saved bundle was accepted")
	}
	if sentinelModelCalls(cap) != 0 {
		t.Fatal("paused registration called a model")
	}
}
func TestSentinelBindingsDoNotExposeOtherOwnersOrAgents(t *testing.T) {
	h, _ := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	w := sentinelTestCreate(t, h, s)
	other := sentinelTestSession(t, h)
	b := h.mgr.sentinelBackend(other)
	if _, err := b.Watch(context.Background(), json.RawMessage(studioBody(map[string]any{"action": "status", "watch_id": w.Spec.ID}))); !errors.Is(err, sentinel.ErrNotFound) {
		t.Fatalf("other Agent read private watch: %v", err)
	}
	db, err := h.mgr.sentinels.database(true)
	if err != nil {
		t.Fatal(err)
	}
	foreign := w.Spec
	foreign.ID = ""
	foreign.OwnerID = "different-owner"
	foreign.Owner = json.RawMessage(`{"provider":"local","subject_id":"different-owner"}`)
	made, err := db.Create(foreign)
	if err != nil {
		t.Fatal(err)
	}
	own := h.mgr.sentinelBackend(s)
	if _, err = own.Watch(context.Background(), json.RawMessage(studioBody(map[string]any{"action": "status", "watch_id": made.Spec.ID}))); !errors.Is(err, sentinel.ErrNotFound) {
		t.Fatalf("other owner watch exposed: %v", err)
	}
	// A captured backend cannot survive a changed host identity.
	s.mu.Lock()
	scope := identity.LocalScope("another-user")
	s.owner = &scope
	s.mu.Unlock()
	if _, err = own.Watch(context.Background(), json.RawMessage(`{"action":"list"}`)); err == nil {
		t.Fatal("old backend survived identity rebinding")
	}
	s.mu.Lock()
	s.owner = nil
	s.mu.Unlock()
}
func TestSentinelDryRunDoesNotCommitCursorQueueOrWakeAgent(t *testing.T) {
	h, cap := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	w := sentinelTestCreate(t, h, s)
	calls := 0
	h.mgr.sentinels.runner = func(ctx context.Context, cfg sentinelexec.Config, accept func(sentinel.Frame) error) error {
		calls++
		if cfg.ScriptDir != w.Spec.ScriptDir || cfg.Workspace != s.cwd || cfg.Mode != "poll" || cfg.Timeout > 30*time.Second {
			t.Errorf("wrong dry-run scope: %+v", cfg)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("dry run has no deadline")
		}
		return accept(sentinel.Frame{V: 1, ID: "test_frame", Events: []sentinel.InputEvent{{Key: "custom:42", Summary: "Test observation"}}, State: json.RawMessage(`{"progress":42}`), Cursor: json.RawMessage(`{"cursor":"new"}`)})
	}
	result, err := h.mgr.backgroundTaskBackend(s).Task(context.Background(), json.RawMessage(studioBody(map[string]any{"action": "check", "source": "sentinel", "task_id": w.Spec.ID, "timeout_seconds": 2})))
	if err != nil {
		t.Fatal(err)
	}
	view, ok := result.Result.(map[string]any)
	if !ok || view["test_only"] != true || view["agent_woken"] != false || view["cursor_committed"] != false || calls != 1 {
		t.Fatalf("bad dry-run receipt: %+v", result.Result)
	}
	db, _ := h.mgr.sentinels.database(true)
	after, err := db.Get(w.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.State) != 0 || len(after.Cursor) != 0 || after.Pending != 0 || after.Spec.Enabled {
		t.Fatalf("dry run changed formal state: %+v", after)
	}
	if sentinelModelCalls(cap) != 0 {
		t.Fatal("dry run called model")
	}
}
func TestSentinelWakeRequiresBoundDecisionAndRetainsContext(t *testing.T) {
	h, cap := newSentinelHarness(t, []llm.Response{toolTurn(tools.WatchDecideName, `{"decision":"report","summary":"first investigation saved"}`), textTurn("First report complete"), toolTurn(tools.WatchDecideName, `{"decision":"report","summary":"second investigation saved"}`), textTurn("Second report complete")})
	s := sentinelTestSession(t, h)
	if slices.Contains(s.agent.NativeToolNames(), tools.WatchDecideName) {
		t.Fatal("ordinary session exposed event decision tool")
	}
	if _, err := h.mgr.sentinelBackend(s).Decide(context.Background(), json.RawMessage(`{"decision":"ignore"}`)); err == nil {
		t.Fatal("ordinary conversation decided arbitrary sentinel events")
	}
	firstInput := sentinelTestWake("wake_first")
	b, err := s.startSentinelWake(firstInput)
	if err != nil {
		t.Fatal(err)
	}
	sentinelWaitRun(t, b)
	receipt, err := s.readWakeReceipt(firstInput.ID)
	if err != nil || receipt.Status != "completed" || receipt.Decision != "report" || receipt.Summary != "first investigation saved" {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	if _, err = s.startSentinelWake(firstInput); err == nil {
		t.Fatal("duplicate wake executed again")
	}
	changed := firstInput
	changed.Task = "different goal"
	if _, err = s.startSentinelWake(changed); err == nil {
		t.Fatal("same wake ID accepted different content")
	}
	secondInput := sentinelTestWake("wake_second")
	secondInput.Events[0].Key = "custom:43:v1"
	b, err = s.startSentinelWake(secondInput)
	if err != nil {
		t.Fatal(err)
	}
	sentinelWaitRun(t, b)
	_, _, histories, _ := cap.snapshot()
	if len(histories) != 4 {
		t.Fatalf("unexpected model calls=%d", len(histories))
	}
	found := false
	for _, m := range histories[2] {
		if strings.Contains(studioMessageText(m), "First report complete") {
			found = true
		}
	}
	if !found {
		t.Fatal("next wake lost previous investigation context")
	}
	if slices.Contains(s.agent.NativeToolNames(), tools.WatchDecideName) {
		t.Fatal("completed wake retained event decision tool")
	}
	if schemas := cap.toolSchemas(); !slices.Contains(schemas[0], tools.WatchDecideName) {
		t.Fatalf("decision tool missing: %v", schemas[0])
	}
}
func TestSentinelTranscriptAdmissionFailurePreventsModelCall(t *testing.T) {
	h, cap := newSentinelHarness(t, []llm.Response{textTurn("must not run")})
	s := sentinelTestSession(t, h)
	// Opening a directory as the append target fails on every supported host,
	// without relying on chmod semantics or the test user's privilege level.
	transcript := s.Path()
	if err := os.Remove(transcript); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(transcript, 0700); err != nil {
		t.Fatal(err)
	}
	b, err := s.startSentinelWake(sentinelTestWake("wake_persist_fail"))
	if err == nil {
		sentinelWaitRun(t, b)
	}
	if sentinelModelCalls(cap) != 0 {
		t.Fatal("model ran before failed input admission")
	}
	if b != nil && b.run.persistErr == nil {
		t.Fatal("admission failure was not retained")
	}
}
func TestSentinelMissingDecisionRetainsInterruptedReceipt(t *testing.T) {
	h, _ := newSentinelHarness(t, []llm.Response{textTurn("I looked at the observation")})
	s := sentinelTestSession(t, h)
	input := sentinelTestWake("wake_no_decision")
	b, err := s.startSentinelWake(input)
	if err != nil {
		t.Fatal(err)
	}
	sentinelWaitRun(t, b)
	receipt, err := s.readWakeReceipt(input.ID)
	if err != nil || receipt.Status != "interrupted" || receipt.Decision != "" {
		t.Fatalf("missing decision looked settled: %+v %v", receipt, err)
	}
	if _, err = s.startSentinelWake(input); err == nil {
		t.Fatal("uncertain run automatically replayed")
	}
}
func TestSentinelColdManagerRejectsAlreadyCompletedWake(t *testing.T) {
	h, cap := newSentinelHarness(t, []llm.Response{toolTurn(tools.WatchDecideName, `{"decision":"ignore","summary":"No action needed"}`), textTurn("done")})
	s := sentinelTestSession(t, h)
	input := sentinelTestWake("wake_before_restart")
	b, err := s.startSentinelWake(input)
	if err != nil {
		t.Fatal(err)
	}
	sentinelWaitRun(t, b)
	cfg := h.mgr.cfg
	sid := s.ID
	h.mgr.Close()
	restarted, err := NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	cold, err := restarted.Get(sid)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := cold.readWakeReceipt(input.ID)
	if err != nil || receipt.Status != "completed" {
		t.Fatalf("cold receipt=%+v %v", receipt, err)
	}
	before := sentinelModelCalls(cap)
	if _, err = cold.startSentinelWake(input); err == nil {
		t.Fatal("cold restart replayed completed work")
	}
	if sentinelModelCalls(cap) != before {
		t.Fatal("cold replay called model")
	}
}

func TestSentinelRepeatedDeferredDecisionKeepsOriginalDeadline(t *testing.T) {
	release, started := make(chan struct{}), make(chan struct{})
	releaseModel := func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}
	defer releaseModel()
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			close(started)
			<-release
		}
		return textTurn("deferred"), nil
	}, func(c *Config) {
		var err error
		c.Cwd, err = filepath.EvalSymlinks(c.Cwd)
		if err != nil {
			t.Fatal(err)
		}
		c.SessionDir, err = filepath.EvalSymlinks(c.SessionDir)
		if err != nil {
			t.Fatal(err)
		}
	})
	s := sentinelTestSession(t, h)
	input := sentinelTestWake("wake_deferred")
	bind, err := s.startSentinelWake(input)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("model did not start")
	}
	tool := &tools.WatchDecide{Backend: h.mgr.sentinelBackend(s)}
	raw := json.RawMessage(`{"decision":"defer","summary":"waiting","defer_seconds":60}`)
	if _, err = tool.Execute(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	before, err := s.readWakeReceipt(input.ID)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err = tool.Execute(context.Background(), raw); err != nil {
		t.Fatalf("identical decision retry failed: %v", err)
	}
	after, err := s.readWakeReceipt(input.ID)
	if err != nil || after.DeferUntil != before.DeferUntil {
		t.Fatalf("retry moved deferred deadline: before=%+v after=%+v err=%v", before, after, err)
	}
	if _, err = tool.Execute(context.Background(), json.RawMessage(`{"decision":"defer","summary":"waiting","defer_seconds":120}`)); err == nil {
		t.Fatal("changed decision silently reused original request")
	}
	releaseModel()
	sentinelWaitRun(t, bind)
}

func TestSentinelBusyConversationKeepsEventsUntilLaterDispatch(t *testing.T) {
	release, started := make(chan struct{}), make(chan struct{})
	releaseModel := func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}
	defer releaseModel()
	h := newHarness(t, func(n int) (llm.Response, error) {
		switch n {
		case 1:
			close(started)
			<-release
			return textTurn("foreground complete"), nil
		case 2:
			return toolTurn(tools.WatchDecideName, `{"decision":"report","summary":"queued observation investigated"}`), nil
		default:
			return textTurn("sentinel complete"), nil
		}
	}, func(c *Config) {
		var err error
		c.Cwd, err = filepath.EvalSymlinks(c.Cwd)
		if err != nil {
			t.Fatal(err)
		}
		c.SessionDir, err = filepath.EvalSymlinks(c.SessionDir)
		if err != nil {
			t.Fatal(err)
		}
	})
	s := sentinelTestSession(t, h)
	w := sentinelTestCreate(t, h, s)
	r := h.mgr.sentinels
	r.runner = func(ctx context.Context, _ sentinelexec.Config, _ func(sentinel.Frame) error) error {
		<-ctx.Done()
		return ctx.Err()
	}
	r.mu.Lock()
	r.runs[w.Spec.ID] = true
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.runs, w.Spec.ID); r.mu.Unlock() }()
	db, err := r.database(true)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.UpdateSession(w.Spec.ID, s.ID); err != nil {
		t.Fatal(err)
	}
	w, err = db.SetEnabled(w.Spec.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.SetEnabled(w.Spec.ID, false)
	if err = db.Accept(w.Spec.ID, w.Spec.Generation, sentinel.Frame{V: 1, ID: "busy-frame", Events: []sentinel.InputEvent{{Key: "busy:event", Summary: "arrived while foreground work is active"}}, Cursor: json.RawMessage(`{"version":1}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Start("foreground question", nil, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("foreground did not start")
	}
	s.mu.Lock()
	foreground := s.active
	s.mu.Unlock()
	w, _ = db.Get(w.Spec.ID)
	r.dispatch(db, w)
	batches, err := db.ListBatches(w.Spec.ID, 10)
	if err != nil || len(batches) != 1 || batches[0].Status != "queued" {
		t.Fatalf("busy event not retained: %+v %v", batches, err)
	}
	after, err := db.Get(w.Spec.ID)
	if err != nil || after.Pending != 1 {
		t.Fatalf("busy pending=%+v err=%v", after, err)
	}
	if h.calls() != 1 {
		t.Fatalf("busy sentinel interrupted foreground: %d calls", h.calls())
	}
	releaseModel()
	select {
	case <-foreground.done:
	case <-time.After(3 * time.Second):
		t.Fatal("foreground did not complete")
	}
	r.dispatch(db, after)
	// Admission is now asynchronous through the shared durable dispatcher.
	// Wait for the same frozen source batch instead of calling its executor.
	sentinelResilienceAwait(t, "queued busy event to finish after the foreground run", func() bool {
		rows, err := db.ListBatches(w.Spec.ID, 10)
		return err == nil && len(rows) == 1 && rows[0].Status == "completed"
	})
	batches, err = db.ListBatches(w.Spec.ID, 10)
	if err != nil || len(batches) != 1 || batches[0].Status != "completed" || batches[0].Decision != "report" {
		t.Fatalf("later dispatch did not settle existing batch: %+v %v", batches, err)
	}
	after, err = db.Get(w.Spec.ID)
	if err != nil || after.Pending != 0 {
		t.Fatalf("processed event remains pending: %+v %v", after, err)
	}
	if h.calls() != 3 {
		t.Fatalf("unexpected duplicate or skipped execution: %d calls", h.calls())
	}
}

func TestSentinelPauseRejectsLateCollectorFrame(t *testing.T) {
	h, cap := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	w := sentinelTestCreate(t, h, s)
	r := h.mgr.sentinels
	db, err := r.database(true)
	if err != nil {
		t.Fatal(err)
	}
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	r.runner = func(_ context.Context, _ sentinelexec.Config, accept func(sentinel.Frame) error) error {
		close(entered)
		<-release
		err := accept(sentinel.Frame{V: 1, ID: "late-frame", Events: []sentinel.InputEvent{{Key: "late:event", Summary: "must not arrive after pause"}}, Cursor: json.RawMessage(`{"version":99}`)})
		returned <- err
		return err
	}
	w, err = db.SetEnabled(w.Spec.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(r.ctx)
	defer cancel()
	worker := &sentinelWorker{generation: w.Spec.Generation, cancel: cancel, done: make(chan struct{})}
	r.mu.Lock()
	r.workers[w.Spec.ID] = worker
	r.mu.Unlock()
	go func() { defer close(worker.done); r.collect(ctx, db, w) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("collector did not start")
	}
	if _, err = (&tools.Watch{Backend: h.mgr.sentinelBackend(s)}).Execute(context.Background(), json.RawMessage(studioBody(map[string]any{"action": "pause", "watch_id": w.Spec.ID}))); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err = <-returned:
		if err == nil {
			t.Fatal("late frame accepted after pause")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("late collector did not return")
	}
	<-worker.done
	after, err := db.Get(w.Spec.ID)
	if err != nil || after.Pending != 0 || len(after.Cursor) != 0 || after.Spec.Enabled || after.Spec.Generation == w.Spec.Generation {
		t.Fatalf("late frame mutated paused watch: %+v %v", after, err)
	}
	if sentinelModelCalls(cap) != 0 {
		t.Fatal("paused collector woke Agent")
	}
}
