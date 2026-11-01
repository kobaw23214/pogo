package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingLink stands in for the parent's real pane.
type recordingLink struct {
	mu    sync.Mutex
	ops   []string
	reply BrowserReply
	err   error
	// block, when non-nil, holds each Execute until it is closed, so a test can
	// keep the lease occupied deterministically.
	block chan struct{}
}

func (l *recordingLink) Execute(_ context.Context, op json.RawMessage) (BrowserReply, error) {
	if l.block != nil {
		<-l.block
	}
	l.mu.Lock()
	l.ops = append(l.ops, string(op))
	l.mu.Unlock()
	return l.reply, l.err
}

func (l *recordingLink) seen() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ops...)
}

// pipePair wires a child-side FDBrowser to a parent-side serveBrowser without
// spawning a process: the protocol is what matters, not exec.
func pipePair(t *testing.T, s *Subagent, subagentID string) (*FDBrowser, func()) {
	t.Helper()
	opR, opW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	repR, repW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if s.browserLease == nil {
		s.browserLease = newBrowserLease()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.serveBrowser(opR, repW, subagentID)
	}()
	child := &FDBrowser{ops: opW, replies: newReader(repR), enc: json.NewEncoder(opW)}
	return child, func() {
		opW.Close()
		<-done
		repW.Close()
		opR.Close()
		repR.Close()
		s.browserLease.release(subagentID)
	}
}

// The whole point of the channel: a child with no browser of its own reaches the
// parent's pane, and the op arrives byte for byte. Anything less and the child's
// refs would not match the page the parent snapshotted.
func TestFDBrowserForwardsOpsToTheParentsPane(t *testing.T) {
	link := &recordingLink{reply: BrowserReply{OK: true, URL: "/panels/lib/", Interactive: 7}}
	s := &Subagent{Browser: link}
	child, cleanup := pipePair(t, s, "sub-1")
	defer cleanup()

	op := json.RawMessage(`{"action":"click","target":"e12"}`)
	reply, err := child.Execute(context.Background(), op)
	if err != nil {
		t.Fatalf("forwarding a page op: %v", err)
	}
	if !reply.OK || reply.URL != "/panels/lib/" || reply.Interactive != 7 {
		t.Fatalf("the parent's reply did not survive the pipe: %+v", reply)
	}
	if got := link.seen(); len(got) != 1 || got[0] != string(op) {
		t.Fatalf("op did not arrive intact: %v", got)
	}
}

// The two failure kinds must stay distinguishable across the pipe. A child told
// "the page refused" for "nobody is listening" would retry forever; a child told
// the reverse would give up on a recoverable page error.
func TestFDBrowserKeepsChannelAndPageFailuresApart(t *testing.T) {
	// Page-level: OK=false rides inside the reply, Execute returns no error.
	link := &recordingLink{reply: BrowserReply{OK: false, Error: "ref e9 not found"}}
	s := &Subagent{Browser: link}
	child, cleanup := pipePair(t, s, "sub-1")
	reply, err := child.Execute(context.Background(), json.RawMessage(`{"action":"click"}`))
	if err != nil {
		t.Fatalf("a page failure must not become a channel error: %v", err)
	}
	if reply.OK || !strings.Contains(reply.Error, "not found") {
		t.Fatalf("page failure lost: %+v", reply)
	}
	cleanup()

	// Channel-level: the parent's own error (not armed, no pane) becomes an error.
	link2 := &recordingLink{err: errors.New("page operations are not enabled for this run")}
	s2 := &Subagent{Browser: link2}
	child2, cleanup2 := pipePair(t, s2, "sub-2")
	defer cleanup2()
	if _, err := child2.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`)); err == nil ||
		!strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("a channel failure must reach the child as an error, got %v", err)
	}
}

// A dead parent must fail closed and stay failed. Retrying a broken pipe once per
// call turns one failure into one per turn for the rest of the run.
func TestFDBrowserFailsClosedAndLatchesWhenTheParentIsGone(t *testing.T) {
	link := &recordingLink{reply: BrowserReply{OK: true}}
	s := &Subagent{Browser: link}
	child, cleanup := pipePair(t, s, "sub-1")
	cleanup() // the parent goes away mid-run

	_, err := child.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`))
	if err == nil {
		t.Fatal("a gone parent must be an error, not a silent success")
	}
	first := err.Error()
	_, err = child.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`))
	if err == nil || err.Error() != first {
		t.Fatalf("the failure should latch with the same explanation, got %v", err)
	}
}

// There is one pane and refs belong to a document, so two children driving it at
// once corrupt each other. The lease is what makes that impossible.
func TestBrowserLeaseAdmitsOneHolderAndReadmitsAfterRelease(t *testing.T) {
	l := newBrowserLease()
	if err := l.acquire(context.Background(), "a"); err != nil {
		t.Fatalf("the first holder should get the pane: %v", err)
	}
	// Idempotent for the holder: every op after the first is already inside.
	if err := l.acquire(context.Background(), "a"); err != nil {
		t.Fatalf("the holder should not have to re-acquire: %v", err)
	}

	// A second child blocks, then succeeds once the first releases.
	got := make(chan error, 1)
	go func() { got <- l.acquire(context.Background(), "b") }()
	select {
	case err := <-got:
		t.Fatalf("b should wait while a holds the pane, got %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	l.release("a")
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("b should get the pane after a released it: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("b never got the pane after release")
	}

	// A release by someone who does not hold it must not hand the pane away.
	l.release("a")
	if l.holder != "b" {
		t.Fatalf("a stale release stole the lease: holder=%q", l.holder)
	}
}

// Cancellation has to win over the queue: a cancelled run should not sit in line
// to then be told no.
func TestBrowserLeaseGivesUpWhenTheRunIsCancelled(t *testing.T) {
	l := newBrowserLease()
	if err := l.acquire(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan error, 1)
	go func() { got <- l.acquire(ctx, "b") }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-got:
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("want a cancellation error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled waiter never returned")
	}
}

// The refusal has to name the fix, because the model's options are real: run the
// browsing children one after another instead of as a parallel batch.
func TestBrowserLeaseContentionExplainsItself(t *testing.T) {
	l := newBrowserLease()
	if err := l.acquire(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	// Squeeze the wait so the test does not sit for 90s: the message is what is
	// under test, not the duration.
	l.mu.Lock()
	l.holder = "a"
	l.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := l.acquire(ctx, "b")
	if err == nil {
		t.Fatal("a contended lease must refuse rather than proceed")
	}
	if !strings.Contains(err.Error(), "cancelled") && !strings.Contains(err.Error(), "one at a time") {
		t.Fatalf("the refusal should explain the constraint: %v", err)
	}
}

// A backgrounded child outlives the run that spawned it, so it must not be able
// to hold the user's own browser. Foreground children have no such conflict.
func TestWireBrowserWithholdsTheChannelFromBackgroundChildren(t *testing.T) {
	bg := NewBgRunner(context.Background(), BgHooks{})
	bg.mu.Lock()
	bg.tasks["bg-1"] = &bgHandle{task: BgTask{ID: "bg-1"}}
	bg.mu.Unlock()

	s := &Subagent{Browser: &recordingLink{}, Bg: bg}
	if !bg.knows("bg-1") {
		t.Fatal("the runner should recognise its own task id")
	}
	if bg.knows("fg-1") {
		t.Fatal("a foreground id must not look backgrounded")
	}

	// A backgrounded spawn attaches nothing: no descriptors, no env var.
	cmd := fakeCmd()
	cleanup, err := s.wireBrowser(cmd, childSpec{id: "bg-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(cmd.ExtraFiles) != 0 || envHas(cmd.Env, EnvBrowserFD) {
		t.Fatalf("a background child must get no browser channel: files=%d env=%v", len(cmd.ExtraFiles), cmd.Env)
	}

	// A foreground one gets the pair, and the env var names the descriptor it
	// actually landed on rather than assuming 3.
	fg := fakeCmd()
	fgCleanup, err := s.wireBrowser(fg, childSpec{id: "fg-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer fgCleanup()
	if len(fg.ExtraFiles) != 2 {
		t.Fatalf("a foreground child needs both descriptors, got %d", len(fg.ExtraFiles))
	}
	if !envHas(fg.Env, EnvBrowserFD+"=3") {
		t.Fatalf("the first channel should start at fd 3: %v", fg.Env)
	}
}

// Two channels means the numbering depends on attach order, so the env var has to
// carry the real number. This pins the arithmetic the child relies on.
func TestBrowserChannelNumbersItselfAfterTheGate(t *testing.T) {
	s := &Subagent{
		Browser: &recordingLink{},
		Review:  func(context.Context, Approval) Decision { return Decision{Allow: true} },
	}
	cmd := fakeCmd()
	closeGate, err := s.wireGate(cmd, "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	defer closeGate()
	closeBrowser, err := s.wireBrowser(cmd, childSpec{id: "sub-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeBrowser()

	if !envHas(cmd.Env, EnvGateFD+"=3") {
		t.Fatalf("the gate is attached first and owns fd 3: %v", cmd.Env)
	}
	if !envHas(cmd.Env, EnvBrowserFD+"=5") {
		t.Fatalf("the browser pair follows the gate's, at fd 5: %v", cmd.Env)
	}
	if len(cmd.ExtraFiles) != 4 {
		t.Fatalf("both channels need two descriptors each, got %d", len(cmd.ExtraFiles))
	}
}

// fakeCmd is an exec.Cmd that is never started: wireGate and wireBrowser only
// mutate ExtraFiles and Env, which is exactly what these tests inspect.
func fakeCmd() *exec.Cmd { return exec.Command("true") }

func envHas(env []string, want string) bool {
	for _, e := range env {
		if e == want || (strings.HasSuffix(want, "=") && strings.HasPrefix(e, want)) {
			return true
		}
		if !strings.Contains(want, "=") && strings.HasPrefix(e, want+"=") {
			return true
		}
	}
	return false
}
