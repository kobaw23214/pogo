package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EnvBrowserFD tells a child that a browser channel exists and which descriptor
// it starts at.
//
// The whole mechanism is wireGate's, one file over: a child has no browser of its
// own, so it asks the parent to drive one, exactly as it asks the parent to
// approve a call. Reusing the shape rather than inventing a second one is the
// point — the two read as one mechanism and a third would cost nothing.
//
// Unlike EnvGateFD this carries the *actual* descriptor number rather than
// assuming 3. Two channels means the numbering depends on which was attached
// first, and a hardcoded 3 in the second one is a bug that waits for someone to
// reorder two lines in spawn.
const EnvBrowserFD = "POGO_BROWSER_FD"

// Backend metadata travels with the private descriptor grant. It shapes the
// child's schema only; all operations still pass the parent's task gate.
const EnvBrowserBackend = "POGO_BROWSER_BACKEND"

// browserOpMsg and browserReplyMsg are the two lines of the private protocol.
// The reply carries Err separately from the payload because BrowserLink.Execute
// distinguishes a channel failure (no pane, not armed, timeout) from a page-level
// failure that rides inside the reply — and that distinction has to survive the
// pipe or the child would report "the page refused" for "nobody is listening".
type browserOpMsg struct {
	Op json.RawMessage `json:"op"`
}

type browserReplyMsg struct {
	Reply BrowserReply `json:"reply"`
	Err   string       `json:"err,omitempty"`
}

// browserLeaseWait is how long a child waits for the pane before giving up.
//
// There is exactly one pane and its refs are per-document, so two children
// driving it at once corrupt each other: A snapshots, B navigates, A clicks a ref
// that no longer exists. Serializing is therefore correctness, not politeness.
// Waiting rather than refusing outright is what keeps a parallel batch of
// research children working — they queue instead of failing — and the cap is what
// stops a queued child from spending its whole timeout in line.
const browserLeaseWait = 90 * time.Second

// browserLease grants the pane to one child at a time.
//
// Held for the child's whole lifetime rather than per operation, because a
// browsing session is the unit that must not interleave: releasing between ops
// would let another child navigate away between a snapshot and the click that
// used it. Release is tied to process exit, so there is no timeout to tune and no
// way to leak the lease — spawn's deferred cleanup runs after cmd.Wait.
type browserLease struct {
	mu     sync.Mutex
	free   *sync.Cond
	holder string
}

func newBrowserLease() *browserLease {
	l := &browserLease{}
	l.free = sync.NewCond(&l.mu)
	return l
}

// acquire takes the pane for holder, or reports why it could not. Idempotent for
// the current holder: every op after the first is already inside the lease.
func (l *browserLease) acquire(ctx context.Context, holder string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder == holder {
		return nil
	}
	if l.holder == "" {
		l.holder = holder
		return nil
	}
	// Cond has no deadline, so a waker does the waiting and the sleeper is woken
	// either by a release or by the timer. Spurious wakeups are handled by the
	// loop condition, which is the ordinary Cond contract.
	deadline := time.AfterFunc(browserLeaseWait, func() { l.free.Broadcast() })
	defer deadline.Stop()
	stop := context.AfterFunc(ctx, func() { l.free.Broadcast() })
	defer stop()

	until := time.Now().Add(browserLeaseWait)
	for l.holder != "" {
		if ctx.Err() != nil {
			return errors.New("the run was cancelled while waiting for the browser pane")
		}
		if !time.Now().Before(until) {
			return errors.New("another subagent is driving the browser pane and did not finish within " +
				browserLeaseWait.String() + "; there is one pane, so browsing subagents run one at a time — " +
				"delegate them sequentially (wait: true) rather than as a parallel batch")
		}
		l.free.Wait()
	}
	l.holder = holder
	return nil
}

// release hands the pane back. Safe to call for a holder that never acquired.
func (l *browserLease) release(holder string) {
	l.mu.Lock()
	if l.holder == holder {
		l.holder = ""
		l.free.Broadcast()
	}
	l.mu.Unlock()
}

// wireBrowser attaches the browser channel to cmd and starts serving it.
//
// Returns a function to run after the child has been waited for, matching
// wireGate's contract: it closes the parent's ends in the order that lets the
// server goroutine see EOF, and releases the lease.
//
// Withheld from a backgrounded child on purpose. A background delegation outlives
// the run that started it, so it would be holding the user's own browser while
// the user is using it — and the pane is the user's, not the agent's. Foreground
// children have no such conflict: the parent is blocked while they run, so
// nothing else can touch the pane.
func (s *Subagent) wireBrowser(cmd *exec.Cmd, spec childSpec) (func(), error) {
	// Asked of the runner rather than carried on the spec: the runner is what
	// knows which ids are its own, and threading a flag through explore, edit and
	// both isolation paths would put the same fact in five signatures.
	if s.Browser == nil || (s.Bg != nil && s.Bg.knows(spec.id)) {
		return func() {}, nil
	}
	// Child writes ops here, parent reads.
	opR, opW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	// Parent writes replies here, child reads.
	repR, repW, err := os.Pipe()
	if err != nil {
		opR.Close()
		opW.Close()
		return nil, err
	}
	// ExtraFiles[0] is fd 3, so the pair lands wherever the slice already ends.
	base := 3 + len(cmd.ExtraFiles)
	cmd.ExtraFiles = append(cmd.ExtraFiles, opW, repR)
	cmd.Env = append(cmd.Env, EnvBrowserFD+"="+strconv.Itoa(base))
	cmd.Env = append(cmd.Env, EnvBrowserBackend+"="+BrowserBackendOf(s.Browser))

	if s.browserLease == nil {
		s.browserLease = newBrowserLease()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.serveBrowser(opR, repW, spec.id)
	}()

	return func() {
		opW.Close()
		repR.Close()
		opR.Close()
		<-done
		repW.Close()
		s.browserLease.release(spec.id)
	}, nil
}

// serveBrowser answers the child's operations one at a time.
//
// Serial by construction like serveGate, because the child's side is one
// goroutine asking and waiting. The lease is acquired on the first page op
// rather than at spawn: a child that only reads bookmarks/download metadata
// must not hold the pane away from one that browses.
func (s *Subagent) serveBrowser(ops io.Reader, replies io.Writer, subagentID string) {
	sc := bufio.NewScanner(ops)
	// Native protocol parameters allow 64 KiB before JSON transport escaping.
	// Each HTML-sensitive byte may become a six-byte escape in the pipe; retain
	// room for that envelope rather than terminating a valid child request at the
	// ordinary 50 KiB tool-output ceiling.
	sc.Buffer(make([]byte, 0, 8*1024), 6*MaxBrowserDevToolsParamsBytes+4096)
	enc := json.NewEncoder(replies)
	for sc.Scan() {
		var m browserOpMsg
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			// The child is blocked on an answer; silence would hang it until its
			// own timeout, so a malformed op is answered rather than dropped.
			_ = enc.Encode(browserReplyMsg{Err: "the browser operation could not be read"})
			continue
		}
		ctx := context.Background()
		if BrowserMetadataAction(BrowserActionOf(m.Op)) {
			// Host metadata neither drives nor observes a page. Validate the
			// full standalone contract before bypassing the page lease; a child
			// must not turn an arbitrary operation into a metadata read.
			if err := (&Browser{Link: s.Browser}).ValidateArgs(m.Op); err != nil {
				_ = enc.Encode(browserReplyMsg{Err: err.Error()})
				continue
			}
		} else {
			if err := s.browserLease.acquire(ctx, subagentID); err != nil {
				_ = enc.Encode(browserReplyMsg{Err: err.Error()})
				continue
			}
		}
		reply, err := s.Browser.Execute(ctx, m.Op)
		msg := browserReplyMsg{Reply: reply}
		if err != nil {
			msg.Err = err.Error()
		}
		if err := enc.Encode(msg); err != nil {
			return // the child is gone
		}
	}
}

// FDBrowser is the child's side: it drives the parent's pane instead of a browser
// of its own.
//
// Constructed only when EnvBrowserFD is set, which happens only when the parent
// had a pane to delegate to and the child is not backgrounded. Everywhere else it
// is nil and the browser tool is simply not registered, which is how "this
// process cannot drive a page" stays structural rather than becoming a refusal
// the model can argue with.
type FDBrowser struct {
	// serial keeps one op outstanding at a time. One pipe with one reply per
	// request means two concurrent sends would read each other's answers; the
	// browser tool is Sequential anyway, so nothing legitimate queues here.
	serial   sync.Mutex
	ops      *os.File
	replies  *bufio.Reader
	enc      *json.Encoder
	unusable string
	backend  string
}

func (b *FDBrowser) BrowserBackend() string { return b.backend }

// OpenFDBrowser returns the child's browser link, or nil when this process was
// not given one.
func OpenFDBrowser() *FDBrowser {
	base, err := strconv.Atoi(strings.TrimSpace(os.Getenv(EnvBrowserFD)))
	if err != nil || base < 3 {
		return nil
	}
	// Named for what they are, since these names show up in error messages.
	ops := os.NewFile(uintptr(base), "pogo browser ops")
	replies := os.NewFile(uintptr(base+1), "pogo browser replies")
	if ops == nil || replies == nil {
		return nil
	}
	backend := os.Getenv(EnvBrowserBackend)
	if backend != BrowserBackendNative {
		backend = ""
	}
	return &FDBrowser{ops: ops, replies: bufio.NewReader(replies), enc: json.NewEncoder(ops), backend: backend}
}

// Execute implements BrowserLink by forwarding to the parent.
//
// A broken channel is a channel-level error, not a page-level one: the model
// should hear "the browser is no longer reachable" and stop, rather than
// "the page said no" and retry. Once broken it stays broken — a pipe does not
// recover, and retrying it per call would turn one failure into one per turn.
func (b *FDBrowser) Execute(ctx context.Context, op json.RawMessage) (BrowserReply, error) {
	if err := ctx.Err(); err != nil {
		return BrowserReply{}, errors.New("the run was cancelled")
	}
	b.serial.Lock()
	defer b.serial.Unlock()
	if b.unusable != "" {
		return BrowserReply{}, errors.New(b.unusable)
	}
	if err := b.enc.Encode(browserOpMsg{Op: op}); err != nil {
		b.unusable = "the parent agent's browser channel is closed, so page operations are no longer possible"
		return BrowserReply{}, errors.New(b.unusable)
	}
	line, err := b.replies.ReadString('\n')
	if err != nil {
		b.unusable = "the parent agent did not answer the page operation, so page operations are no longer possible"
		return BrowserReply{}, errors.New(b.unusable)
	}
	var m browserReplyMsg
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &m); err != nil {
		return BrowserReply{}, errors.New("the parent agent's answer to the page operation could not be read")
	}
	if m.Err != "" {
		return BrowserReply{}, errors.New(m.Err)
	}
	return m.Reply, nil
}
