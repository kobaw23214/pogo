package sentinelsdk

// These tests exercise process/pipe and durable-store boundaries. Source
// cursor expiry is deliberately opaque: only source code can interpret it.
import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/sentinel"
)

type recoveryProcess struct {
	cmd      *exec.Cmd
	in       io.WriteCloser
	out      io.ReadCloser
	reader   *bufio.Reader
	errs     *bytes.Buffer
	waitOnce sync.Once
	done     chan struct{}
	waitErr  error
}

func (p *recoveryProcess) wait() error {
	p.waitOnce.Do(func() {
		go func() {
			p.waitErr = p.cmd.Wait()
			close(p.done)
		}()
	})
	<-p.done
	return p.waitErr
}

func startRecoveryProcess(t *testing.T, code string, state, cursor json.RawMessage) *recoveryProcess {
	t.Helper()
	cmd, errs := sdkCommand(t, code)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &recoveryProcess{cmd: cmd, in: in, out: out, reader: bufio.NewReader(out), errs: errs, done: make(chan struct{})}
	t.Cleanup(func() {
		in.Close()
		out.Close()
		cmd.Process.Kill()
		p.wait()
	})
	if len(state) == 0 {
		state = json.RawMessage("null")
	}
	if len(cursor) == 0 {
		cursor = json.RawMessage("null")
	}
	if err := json.NewEncoder(in).Encode(map[string]any{"v": 1, "kind": "boot", "mode": "stream", "workspace": "/authorized/workspace", "state": state, "cursor": cursor}); err != nil {
		t.Fatal(err)
	}
	return p
}

func newRecoveryStore(t *testing.T) (*sentinel.Store, string, sentinel.Watch) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "events.db")
	db, err := sentinel.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	w, err := db.Create(sentinel.Spec{
		OwnerID: "sdk-test", AgentID: "sdk-agent", Title: "SDK recovery boundary", Task: "Observe generic source records",
		Workspace: root, ScriptDir: root, Command: []string{python(t), "main.py"}, Mode: "stream",
		TimeoutSeconds: 10, MaxTurns: 4, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, path, w
}

func ackRecoveryFrame(t *testing.T, p *recoveryProcess, id string) {
	t.Helper()
	if err := json.NewEncoder(p.in).Encode(map[string]any{"v": 1, "kind": "accepted", "id": id}); err != nil {
		t.Fatal(err)
	}
}

const recoveryEventScript = `import json,sys
from pogo_watch import Watcher,event,ProtocolError
w=Watcher(mode='stream')
before_state,before_cursor=w.state,w.cursor
try:
    w.emit([event('generic:record:1','record observed',{'id':1})],
           state={'last':1},cursor={'record':1})
except ProtocolError:
    assert w.state == before_state and w.cursor == before_cursor
    try:
        w.checkpoint(cursor={'record':999})
        raise AssertionError('unconfirmed client advanced again')
    except ProtocolError:
        pass
    print('ACK_LOST_LOCAL_SNAPSHOT_RETAINED',file=sys.stderr,flush=True)
else:
    assert w.state == {'last':1} and w.cursor == {'record':1}
    print('ACK_CONFIRMED',file=sys.stderr,flush=True)
`

func TestRecoveryCommittedFrameWithLostACKUsesHostBootOnRestart(t *testing.T) {
	db, path, w := newRecoveryStore(t)
	p := startRecoveryProcess(t, recoveryEventScript, nil, nil)
	frame := readFrame(t, p.reader)
	if err := db.Accept(w.Spec.ID, w.Spec.Generation, frame); err != nil {
		t.Fatal(err)
	}
	// Acceptance has already been fsynced. Losing the ACK must not induce the
	// client to assume that acceptance failed or commit a different proposal.
	p.in.Close()
	if raw, err := io.ReadAll(p.reader); err != nil || len(raw) != 0 {
		t.Fatalf("SDK sent a follow-up after unknown acceptance: %q %v", raw, err)
	}
	if err := p.wait(); err != nil || !strings.Contains(p.errs.String(), "ACK_LOST_LOCAL_SNAPSHOT_RETAINED") {
		t.Fatalf("lost ACK mutated local state or was not detected: %v %s", err, p.errs.String())
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sentinel.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	saved, err := reopened.Get(w.Spec.ID)
	if err != nil || saved.Pending != 1 || string(saved.Cursor) != `{"record":1}` {
		t.Fatalf("durable host commit was lost: %+v %v", saved, err)
	}
	p2 := startRecoveryProcess(t, `from pogo_watch import Watcher,event
with Watcher(mode='stream') as w:
    assert w.state == {'last':1} and w.cursor == {'record':1}
    # A source may overlap the last accepted record after losing the ACK.
    # Defaults inherit the host-confirmed snapshots, preserving frame identity.
    w.emit([event('generic:record:1','record observed',{'id':1})])
    # The first post-restart observation is new, not a fresh baseline.
    w.emit([event('generic:record:2','next record',{'id':2})],
           state={'last':2},cursor={'record':2})
    assert w.cursor == {'record':2}
`, saved.State, saved.Cursor)
	replay := readFrame(t, p2.reader)
	if replay.ID != frame.ID {
		t.Fatalf("committed overlap replay did not preserve its frame identity: before=%s replay=%s", frame.ID, replay.ID)
	}
	if err := reopened.Accept(w.Spec.ID, w.Spec.Generation, replay); err != nil {
		t.Fatal(err)
	}
	stillSaved, err := reopened.Get(w.Spec.ID)
	if err != nil || stillSaved.Pending != 1 {
		t.Fatalf("committed overlap replay duplicated an event: %+v %v", stillSaved, err)
	}
	ackRecoveryFrame(t, p2, replay.ID)
	next := readFrame(t, p2.reader)
	if len(next.Events) != 1 || next.Events[0].Key != "generic:record:2" || next.ID == frame.ID {
		t.Fatalf("restart dropped or confused the first new observation: %+v", next)
	}
	if err := reopened.Accept(w.Spec.ID, w.Spec.Generation, next); err != nil {
		t.Fatal(err)
	}
	ackRecoveryFrame(t, p2, next.ID)
	p2.in.Close()
	if err := p2.wait(); err != nil {
		t.Fatalf("restarted client failed: %v %s", err, p2.errs.String())
	}
	saved, err = reopened.Get(w.Spec.ID)
	if err != nil || saved.Pending != 2 || string(saved.Cursor) != `{"record":2}` {
		t.Fatalf("restart did not preserve distinct durable events: %+v %v", saved, err)
	}
}

func TestRecoveryUnacceptedFrameReplaysIdenticallyAfterHostDisconnect(t *testing.T) {
	db, _, w := newRecoveryStore(t)
	p := startRecoveryProcess(t, recoveryEventScript, nil, nil)
	first := readFrame(t, p.reader)
	// Simulate a host disappearing before its durable-acceptance callback.
	p.in.Close()
	if err := p.wait(); err != nil {
		t.Fatalf("first unconfirmed process failed its state assertion: %v %s", err, p.errs.String())
	}
	saved, err := db.Get(w.Spec.ID)
	if err != nil || saved.Pending != 0 || len(saved.Cursor) != 0 {
		t.Fatalf("an unaccepted proposal advanced host state: %+v %v", saved, err)
	}
	p2 := startRecoveryProcess(t, recoveryEventScript, saved.State, saved.Cursor)
	replay := readFrame(t, p2.reader)
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(replay)
	if first.ID != replay.ID || !bytes.Equal(a, b) {
		t.Fatalf("unknown proposal did not replay identically: first=%s replay=%s", a, b)
	}
	if err := db.Accept(w.Spec.ID, w.Spec.Generation, replay); err != nil {
		t.Fatal(err)
	}
	ackRecoveryFrame(t, p2, replay.ID)
	p2.in.Close()
	if err := p2.wait(); err != nil || !strings.Contains(p2.errs.String(), "ACK_CONFIRMED") {
		t.Fatalf("replayed frame could not be confirmed: %v %s", err, p2.errs.String())
	}
	saved, err = db.Get(w.Spec.ID)
	if err != nil || saved.Pending != 1 {
		t.Fatalf("replayed proposal did not create exactly one pending event: %+v %v", saved, err)
	}
}

func TestRecoveryRejectedCheckpointCannotAdvanceCursor(t *testing.T) {
	db, _, w := newRecoveryStore(t)
	initial := sentinel.Frame{V: 1, ID: "initial", Events: []sentinel.InputEvent{}, State: json.RawMessage(`{"last":7}`), Cursor: json.RawMessage(`{"record":7}`)}
	if err := db.Accept(w.Spec.ID, w.Spec.Generation, initial); err != nil {
		t.Fatal(err)
	}
	p := startRecoveryProcess(t, `import sys
from pogo_watch import Watcher,ProtocolError
w=Watcher(mode='stream')
try:
    w.checkpoint(state={'last':8},cursor={'record':8})
    raise AssertionError('rejected checkpoint confirmed')
except ProtocolError:
    assert w.state == {'last':7} and w.cursor == {'record':7}
try:
    w.checkpoint(cursor=None)
    raise AssertionError('rejected client reset cursor')
except ProtocolError:
    pass
print('REJECTED_CHECKPOINT_RETAINED',file=sys.stderr,flush=True)
`, initial.State, initial.Cursor)
	proposal := readFrame(t, p.reader)
	if _, err := db.SetEnabled(w.Spec.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := db.Accept(w.Spec.ID, w.Spec.Generation, proposal); !errors.Is(err, sentinel.ErrStaleGeneration) {
		t.Fatalf("retired collector checkpoint unexpectedly accepted: %v", err)
	}
	// The real host rejects by cancelling/closing control, not by granting an
	// ACK with a rejection field that the SDK could mistake for acceptance.
	p.in.Close()
	if err := p.wait(); err != nil || !strings.Contains(p.errs.String(), "REJECTED_CHECKPOINT_RETAINED") {
		t.Fatalf("checkpoint rejection did not preserve local state: %v %s", err, p.errs.String())
	}
	saved, err := db.Get(w.Spec.ID)
	if err != nil || !bytes.Equal(saved.Cursor, initial.Cursor) || saved.Pending != 0 {
		t.Fatalf("rejected checkpoint changed the host cursor: %+v %v", saved, err)
	}
}

func TestRecoverySourceEOFDoesNotReplaceHostACK(t *testing.T) {
	for _, accept := range []bool{false, true} {
		t.Run(fmt.Sprintf("host_accepts=%v", accept), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			sourceDone := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					sourceDone <- err
					return
				}
				_, err = io.WriteString(conn, "{\"id\":8}\n")
				conn.Close() // End the source before the SDK can receive an ACK.
				sourceDone <- err
			}()
			code := fmt.Sprintf(`import socket,sys,json
from pogo_watch import Watcher,event,ProtocolError
w=Watcher(mode='stream')
host,port=%q.rsplit(':',1)
with socket.create_connection((host,int(port)),timeout=3) as source:
    with source.makefile('r') as records:
        record=json.loads(records.readline())
        assert records.read()==''  # source EOF is already known before emit
try:
    w.emit([event('generic:8','last source record',record)],state={'last':8},cursor={'record':8})
except ProtocolError:
    assert w.cursor == {'record':7}
    print('EOF_WITHOUT_HOST_ACK',file=sys.stderr,flush=True)
else:
    assert w.cursor == {'record':8}
    print('EOF_WITH_HOST_ACK',file=sys.stderr,flush=True)
`, listener.Addr().String())
			p := startRecoveryProcess(t, code, json.RawMessage(`{"last":7}`), json.RawMessage(`{"record":7}`))
			frame := readFrame(t, p.reader)
			if err := <-sourceDone; err != nil {
				t.Fatal(err)
			}
			if accept {
				ackRecoveryFrame(t, p, frame.ID)
			}
			p.in.Close()
			want := "EOF_WITHOUT_HOST_ACK"
			if accept {
				want = "EOF_WITH_HOST_ACK"
			}
			if err := p.wait(); err != nil || !strings.Contains(p.errs.String(), want) {
				t.Fatalf("source EOF was treated as authority to confirm: %v %s", err, p.errs.String())
			}
		})
	}
}

func TestRecoveryFragmentedACKRequiresTheWholeLine(t *testing.T) {
	for _, newline := range []bool{false, true} {
		t.Run(fmt.Sprintf("newline=%v", newline), func(t *testing.T) {
			p := startRecoveryProcess(t, recoveryEventScript, nil, nil)
			frame := readFrame(t, p.reader)
			ack, _ := json.Marshal(map[string]any{"v": 1, "kind": "accepted", "id": frame.ID})
			if _, err := p.in.Write(ack[:len(ack)/2]); err != nil {
				t.Fatal(err)
			}
			wait := make(chan error, 1)
			go func() { wait <- p.wait() }()
			select {
			case err := <-wait:
				t.Fatalf("partial ACK was treated as complete: %v %s", err, p.errs.String())
			case <-time.After(50 * time.Millisecond):
			}
			if _, err := p.in.Write(ack[len(ack)/2:]); err != nil {
				t.Fatal(err)
			}
			if newline {
				io.WriteString(p.in, "\n")
			}
			p.in.Close()
			if err := <-wait; err != nil {
				t.Fatalf("fragment handling failed: %v %s", err, p.errs.String())
			}
			want := "ACK_LOST_LOCAL_SNAPSHOT_RETAINED"
			if newline {
				want = "ACK_CONFIRMED"
			}
			if !strings.Contains(p.errs.String(), want) {
				t.Fatalf("unterminated/full ACK classification incorrect: %s", p.errs.String())
			}
		})
	}
}

func TestRecoveryClosedOutputPipeCannotConfirmOrContinue(t *testing.T) {
	cmd, errs := sdkCommand(t, `import os,sys
from pogo_watch import Watcher,ProtocolError
w=Watcher(mode='stream')
try:
    w.checkpoint(state={'n':1},cursor={'seq':1})
    raise AssertionError('closed output accepted')
except ProtocolError:
    assert w.state == {'n':0} and w.cursor == {'seq':0}
try:
    w.checkpoint()
    raise AssertionError('continued after output failure')
except ProtocolError:
    pass
print('BROKEN_OUTPUT_UNCONFIRMED',file=sys.stderr,flush=True)
os._exit(0) # Avoid Python's interpreter-shutdown flush of the deliberately broken fd.
`)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		in.Close()
		out.Close()
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	// Close the reader before allowing the child to finish booting.
	out.Close()
	writeBoot(t, in, "stream")
	in.Close()
	if err := cmd.Wait(); err != nil || !strings.Contains(errs.String(), "BROKEN_OUTPUT_UNCONFIRMED") {
		t.Fatalf("closed stdout did not poison the client: %v %s", err, errs.String())
	}
}

func TestRecoveryBootCursorIsOpaqueAndValidationFailureIsAtomic(t *testing.T) {
	cmd, errs := sdkCommand(t, recoveryValidationTests)
	output, err := cmd.Output()
	if err != nil || len(output) != 0 || !strings.Contains(errs.String(), "RECOVERY_VALIDATION_OK") {
		t.Fatalf("recovery validation failed: %v stdout=%s stderr=%s", err, output, errs.String())
	}
}

const recoveryValidationTests = `import io,json,sys
from pogo_watch import Watcher,ProtocolError,ValidationError,CapacityError
def boot(state,cursor,mode='poll'):
    return json.dumps({'v':1,'kind':'boot','mode':mode,'workspace':'/workspace','state':state,'cursor':cursor})+'\n'

# An expired source token is not SDK metadata. Do not erase, decode, renew or
# rebaseline it; only source-specific code and the authority can choose recovery.
expired={'token':'expired-test-cursor','expires_at':0,'nested':[3,{'opaque':True}]}
out=io.StringIO()
with Watcher(mode='poll',stdin=io.StringIO(boot({'epoch':4},expired)),stdout=out) as w:
    assert w.cursor==expired and w.state=={'epoch':4}
    changed=w.cursor
    changed['nested'][1]['opaque']=False
    assert w.cursor==expired
    w.checkpoint()
assert json.loads(out.getvalue())['cursor']==expired

# A failed proposal must produce no prefix and cannot erase a restored cursor.
for invalid in ({'bad':float('nan')},{'bad':float('inf')}):
    out=io.StringIO()
    with Watcher(mode='poll',stdin=io.StringIO(boot({'epoch':4},expired)),stdout=out) as w:
        try:w.checkpoint(state=invalid,cursor=None)
        except ValidationError:pass
        else:raise AssertionError('invalid proposed state accepted')
        assert out.getvalue()=='' and w.state=={'epoch':4} and w.cursor==expired
        w.checkpoint()
    assert json.loads(out.getvalue())['cursor']==expired

# SDK 2 distinguishes a known size ceiling from recoverable JSON format errors.
# Even if caught, an oversized proposal cannot quiet-checkpoint past that work.
out=io.StringIO()
try:
    with Watcher(mode='poll',stdin=io.StringIO(boot({'epoch':4},expired)),stdout=out) as w:
        try:w.checkpoint(state='x'*16383,cursor=None)
        except CapacityError:pass
        else:raise AssertionError('over-capacity proposal accepted')
        assert out.getvalue()=='' and w.state=={'epoch':4} and w.cursor==expired
        try:w.checkpoint()
        except CapacityError:pass
        else:raise AssertionError('capacity failure turned into quiet success')
except CapacityError as failure:
    assert failure.code==80
else:raise AssertionError('caught capacity failure did not stop the client')
assert out.getvalue()==''

# A locally buffered full line is not a confirmed submission if flushing fails.
class FlushFailure(io.StringIO):
    def flush(self):raise OSError('simulated flush failure')
class BootOnly(io.StringIO):
    def __init__(self):super().__init__(boot({'epoch':4},expired,'stream'));self.reads=0
    def readline(self,limit=-1):
        self.reads+=1
        if self.reads>1:raise AssertionError('SDK read an ACK after its output flush failed')
        return super().readline(limit)
out=FlushFailure()
w=Watcher(mode='stream',stdin=BootOnly(),stdout=out)
try:w.checkpoint(state={'epoch':5},cursor=None)
except ProtocolError:pass
else:raise AssertionError('flush failure confirmed')
assert w.state=={'epoch':4} and w.cursor==expired
prefix=out.getvalue()
try:w.checkpoint()
except ProtocolError:pass
else:raise AssertionError('flush-failed watcher continued')
assert out.getvalue()==prefix

class SourceCursorExpired(Exception):pass
out=io.StringIO()
try:
    with Watcher(mode='stream',stdin=io.StringIO(boot({'epoch':4},expired,'stream')),stdout=out) as w:
        assert w.cursor==expired
        raise SourceCursorExpired('synthetic source rejected its opaque cursor')
except SourceCursorExpired:pass
else:raise AssertionError('source cursor expiry was swallowed')
assert out.getvalue()==''

for raw in (b'\xff\n', boot(None,'x'*16383).encode(),
            boot(None,None).replace('"v": 1','"v": true').encode(),
            boot(None,None).replace('null','NaN',1).encode()):
    out=io.StringIO()
    try:Watcher(stdin=io.BytesIO(raw),stdout=out)
    except ProtocolError:pass
    else:raise AssertionError('invalid restart boot accepted')
    assert out.getvalue()==''
print('RECOVERY_VALIDATION_OK',file=sys.stderr,flush=True)
`
