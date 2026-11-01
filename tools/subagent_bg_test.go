package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// bgCapture records the runner's hooks. complete is buffered so the runner
// never blocks on a test that is slow to read.
type bgCapture struct {
	mu       sync.Mutex
	started  []BgTask
	frames   int
	complete chan BgResult
}

func newBgCapture() *bgCapture {
	return &bgCapture{complete: make(chan BgResult, maxBackgroundTasks+1)}
}

func (c *bgCapture) hooks() BgHooks {
	return BgHooks{
		OnStart: func(t BgTask) {
			c.mu.Lock()
			c.started = append(c.started, t)
			c.mu.Unlock()
		},
		OnFrame: func(string, json.RawMessage) {
			c.mu.Lock()
			c.frames++
			c.mu.Unlock()
		},
		OnComplete: func(_ BgTask, r BgResult) { c.complete <- r },
	}
}

func (c *bgCapture) awaitComplete(t *testing.T) BgResult {
	t.Helper()
	select {
	case r := <-c.complete:
		return r
	case <-time.After(15 * time.Second):
		t.Fatal("the background task never reported its completion")
		return BgResult{}
	}
}

func bgStub(t *testing.T) string {
	t.Helper()
	stub := filepath.Join(t.TempDir(), "pogo-stub")
	if err := os.WriteFile(stub, []byte(stubScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return stub
}

// The core promise: the tool call answers at once with a task id, and the real
// result arrives later through the completion hook.
func TestBackgroundDispatchAnswersImmediatelyAndDelivers(t *testing.T) {
	t.Setenv("STUB_MODE", "readonly")
	cap := newBgCapture()
	bg := NewBgRunner(context.Background(), cap.hooks())
	s := &Subagent{Cwd: t.TempDir(), Exe: bgStub(t), Bg: bg}

	res, err := call(t, s, ModeExplore, "where is the entry point")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	d := details(t, res)
	if !d.Background {
		t.Error("the ack is not marked as a background dispatch")
	}
	if d.ID == "" {
		t.Fatal("the ack carries no task id")
	}
	if !strings.Contains(res.Text, d.ID) || !strings.Contains(res.Text, "background") {
		t.Errorf("the ack does not tell the model what happened: %q", res.Text)
	}

	out := cap.awaitComplete(t)
	if out.Err != nil {
		t.Fatalf("the task failed: %v", out.Err)
	}
	if !strings.Contains(out.Text, "the entry point is main.go") {
		t.Errorf("the delivered result is not the child's answer: %q", out.Text)
	}
	if out.Details.InputTok != 90 || out.Details.OutTok != 6 {
		t.Errorf("usage did not come back with the result: %+v", out.Details)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.started) != 1 || cap.started[0].ID != d.ID {
		t.Errorf("OnStart did not fire with the dispatched id: %+v", cap.started)
	}
	if cap.frames == 0 {
		t.Error("no progress frames were forwarded")
	}
	if n := bg.InFlight(); n != 0 {
		t.Errorf("a finished task is still registered: %d", n)
	}
}

// wait:true keeps the old synchronous behaviour even where a runner exists:
// the result comes back as the call's own output and no hook fires.
func TestBackgroundWaitStaysSynchronous(t *testing.T) {
	t.Setenv("STUB_MODE", "readonly")
	cap := newBgCapture()
	bg := NewBgRunner(context.Background(), cap.hooks())
	s := &Subagent{Cwd: t.TempDir(), Exe: bgStub(t), Bg: bg}

	args, _ := json.Marshal(subagentArgs{Task: "where is the entry point", Mode: ModeExplore, Wait: true})
	res, err := s.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("synchronous call: %v", err)
	}
	if !strings.Contains(res.Text, "the entry point is main.go") {
		t.Errorf("wait:true did not return the answer directly: %q", res.Text)
	}
	if details(t, res).Background {
		t.Error("a synchronous call is marked as backgrounded")
	}
	select {
	case <-cap.complete:
		t.Error("a synchronous call reported through the background channel")
	case <-time.After(200 * time.Millisecond):
	}
}

// A cancelled task still completes — with the cancellation as its outcome —
// because the session is told to expect a notification and one must come.
func TestBackgroundCancelReportsInsteadOfVanishing(t *testing.T) {
	t.Setenv("STUB_MODE", "hang")
	t.Setenv("STUB_PIDFILE", filepath.Join(t.TempDir(), "pid"))
	cap := newBgCapture()
	bg := NewBgRunner(context.Background(), cap.hooks())
	defer bg.Close()
	s := &Subagent{Cwd: t.TempDir(), Exe: bgStub(t), Bg: bg}

	res, err := call(t, s, ModeExplore, "look around")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	id := details(t, res).ID
	deadline := time.Now().Add(5 * time.Second)
	for bg.InFlight() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !bg.Cancel(id) {
		t.Fatal("the task was not there to cancel")
	}
	out := cap.awaitComplete(t)
	if out.Err == nil {
		t.Error("a cancelled task reported success")
	}
}

// The registry cap turns an unbounded fan-out into an error the model can act
// on, instead of a queue that grows for the life of the session.
func TestBackgroundCapRefusesOverflow(t *testing.T) {
	t.Setenv("STUB_MODE", "hang")
	t.Setenv("STUB_PIDFILE", filepath.Join(t.TempDir(), "pid"))
	bg := NewBgRunner(context.Background(), BgHooks{})
	defer bg.Close()
	s := &Subagent{Cwd: t.TempDir(), Exe: bgStub(t), Bg: bg}

	for i := 0; i < maxBackgroundTasks; i++ {
		if _, err := call(t, s, ModeExplore, "look around"); err != nil {
			t.Fatalf("dispatch %d within the cap refused: %v", i, err)
		}
	}
	if _, err := call(t, s, ModeExplore, "one too many"); err == nil ||
		!strings.Contains(err.Error(), "session limit") {
		t.Errorf("the overflow dispatch was not a usable refusal: %v", err)
	}
}

// The status tool is how the model answers "how far along is it".
func TestSubagentTasksListsOutstanding(t *testing.T) {
	t.Setenv("STUB_MODE", "hang")
	t.Setenv("STUB_PIDFILE", filepath.Join(t.TempDir(), "pid"))
	bg := NewBgRunner(context.Background(), BgHooks{})
	defer bg.Close()
	s := &Subagent{Cwd: t.TempDir(), Exe: bgStub(t), Bg: bg}

	tool := &SubagentTasks{Bg: bg}
	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "No background subagents") {
		t.Errorf("an empty registry should say so: %q", res.Text)
	}

	dispatch, err := call(t, s, ModeExplore, "find the thing")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	id := details(t, dispatch).ID
	res, err = tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, id) || !strings.Contains(res.Text, "find the thing") {
		t.Errorf("the outstanding task is not listed: %q", res.Text)
	}
}

func TestBackgroundCloseDrainsQueuedTasksAndCompletionHooks(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	bg := NewBgRunner(context.Background(), BgHooks{OnComplete: func(_ BgTask, result BgResult) {
		if result.Err == nil {
			t.Error("closed queued task reported success")
		}
		close(entered)
		<-release
	}})
	s := &Subagent{Concurrency: 1}
	// Occupy the only slot: the test never launches a subprocess.
	if err := s.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.release()
	if _, err := bg.dispatch(s, "never-executed", "queued", ModeExplore, "offline", time.Minute); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { bg.Close(); close(closed) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("close did not cancel queued task")
	}
	select {
	case <-closed:
		t.Fatal("close returned while a completion hook was still running")
	default:
	}
	if _, err := bg.dispatch(s, "never-executed", "late", ModeExplore, "offline", time.Minute); err == nil {
		t.Fatal("dispatch accepted during close")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("close did not finish after callback")
	}
	bg.Close() // idempotent and no wait-group underflow
	if bg.InFlight() != 0 {
		t.Fatal("closed runner retained tasks")
	}
}

func TestBackgroundDispatchRejectsCancelledSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	bg := NewBgRunner(ctx, BgHooks{})
	if _, err := bg.dispatch(&Subagent{}, "never-executed", "late", ModeExplore, "offline", time.Minute); err == nil {
		t.Fatal("cancelled session accepted a task")
	}
	bg.Close()
}
