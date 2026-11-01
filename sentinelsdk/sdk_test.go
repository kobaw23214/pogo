package sentinelsdk

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/sentinel"
)

func python(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("sentinel SDK tests require Python 3")
	}
	return p
}

func sdkWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), Source(), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func sdkCommand(t *testing.T, code string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, python(t), "-u", "-c", code)
	cmd.Dir = sdkWorkspace(t)
	errOutput := &bytes.Buffer{}
	cmd.Stderr = errOutput
	return cmd, errOutput
}

func writeBoot(t *testing.T, w io.Writer, mode string) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(map[string]any{"v": 1, "kind": "boot", "mode": mode, "workspace": "/authorized/workspace", "state": map[string]any{"n": 0}, "cursor": map[string]any{"seq": 0}}); err != nil {
		t.Fatal(err)
	}
}

func readFrame(t *testing.T, r *bufio.Reader) sentinel.Frame {
	t.Helper()
	raw, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read SDK frame: %v", err)
	}
	var frame sentinel.Frame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode SDK frame: %v: %q", err, raw)
	}
	if err := sentinel.ValidateFrame(frame); err != nil {
		t.Fatalf("SDK frame rejected by host: %v", err)
	}
	return frame
}

func TestEmbeddedSDKIsCopiedAndStartersFailUntilImplemented(t *testing.T) {
	first := Source()
	first[0] ^= 0xff
	if bytes.Equal(first, Source()) {
		t.Fatal("Source exposed mutable embedded bytes")
	}
	if _, err := Starter("unsupported"); err == nil {
		t.Fatal("invalid starter mode accepted")
	}
	for _, mode := range []string{"poll", "stream"} {
		t.Run(mode, func(t *testing.T) {
			body, err := Starter(mode)
			if err != nil {
				t.Fatal(err)
			}
			dir := sdkWorkspace(t)
			path := filepath.Join(dir, "main.py")
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(python(t), path)
			var boot, output, errs bytes.Buffer
			writeBoot(t, &boot, mode)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = &boot, &output, &errs
			if err := cmd.Run(); err == nil || output.Len() != 0 || !strings.Contains(errs.String(), "NotImplementedError") {
				t.Fatalf("starter falsely became ready: err=%v stdout=%q stderr=%q", err, output.String(), errs.String())
			}
		})
	}
}

func TestPythonStreamWaitsForMatchingACKAndKeepsConfirmedCopies(t *testing.T) {
	cmd, errs := sdkCommand(t, `from pogo_watch import Watcher,event
with Watcher(mode='stream') as w:
    assert w.workspace == '/authorized/workspace'
    copy=w.state
    copy['n']=99
    assert w.state == {'n':0}
    w.checkpoint(state={'n':1},cursor={'seq':1})
    assert w.state == {'n':1} and w.cursor == {'seq':1}
    w.emit([event('record:2','important',{'id':2})],state={'n':2},cursor={'seq':2})
    assert w.state == {'n':2} and w.cursor == {'seq':2}
`)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	writeBoot(t, stdin, "stream")
	r := bufio.NewReader(stdout)
	first := readFrame(t, r)
	if len(first.Events) != 0 || string(first.State) != `{"n":1}` {
		t.Fatalf("invalid checkpoint: %+v", first)
	}
	next := make(chan sentinel.Frame, 1)
	go func() {
		raw, err := r.ReadBytes('\n')
		if err == nil {
			var frame sentinel.Frame
			_ = json.Unmarshal(raw, &frame)
			next <- frame
		}
	}()
	select {
	case <-next:
		t.Fatal("SDK advanced to next proposal before host accepted the checkpoint")
	case <-time.After(50 * time.Millisecond):
	}
	if err := json.NewEncoder(stdin).Encode(map[string]any{"v": 1, "kind": "accepted", "id": first.ID}); err != nil {
		t.Fatal(err)
	}
	var second sentinel.Frame
	select {
	case second = <-next:
	case <-time.After(3 * time.Second):
		t.Fatal("SDK did not continue after a valid ACK")
	}
	if err := sentinel.ValidateFrame(second); err != nil || len(second.Events) != 1 || second.ID == first.ID {
		t.Fatalf("second proposal: %+v err=%v", second, err)
	}
	_ = json.NewEncoder(stdin).Encode(map[string]any{"v": 1, "kind": "accepted", "id": second.ID})
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("stream SDK failed: %v stderr=%s", err, errs.String())
	}
}

func TestPythonBadOrClosedACKCannotAdvanceOrContinue(t *testing.T) {
	for _, mode := range []string{"wrong-id", "wrong-version", "malformed", "oversized", "closed"} {
		t.Run(mode, func(t *testing.T) {
			cmd, errs := sdkCommand(t, `import json,sys
from pogo_watch import Watcher,ProtocolError
w=Watcher(mode='stream')
try:
    w.checkpoint(state={'n':1},cursor={'seq':1})
    raise AssertionError('invalid ACK accepted')
except ProtocolError:
    assert w.state == {'n':0} and w.cursor == {'seq':0}
try:
    w.checkpoint(state={'n':2})
    raise AssertionError('poisoned client continued')
except ProtocolError:
    pass
print('CONFIRMED_STATE_UNCHANGED',file=sys.stderr)
`)
			stdin, _ := cmd.StdinPipe()
			stdout, _ := cmd.StdoutPipe()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			writeBoot(t, stdin, "stream")
			r := bufio.NewReader(stdout)
			frame := readFrame(t, r)
			switch mode {
			case "wrong-id":
				_ = json.NewEncoder(stdin).Encode(map[string]any{"v": 1, "kind": "accepted", "id": "other"})
			case "wrong-version":
				_ = json.NewEncoder(stdin).Encode(map[string]any{"v": true, "kind": "accepted", "id": frame.ID})
			case "malformed":
				fmt.Fprintln(stdin, "not-json")
			case "oversized":
				fmt.Fprintln(stdin, strings.Repeat("x", 5000))
			}
			stdin.Close()
			if raw, err := io.ReadAll(r); err != nil || len(raw) != 0 {
				t.Fatalf("poisoned SDK wrote more output: %q %v", raw, err)
			}
			if err := cmd.Wait(); err != nil || !strings.Contains(errs.String(), "CONFIRMED_STATE_UNCHANGED") {
				t.Fatalf("ACK rejection failed: %v stderr=%s", err, errs.String())
			}
		})
	}
}

func TestPythonSDKValidationAndContentIdentity(t *testing.T) {
	cmd, errs := sdkCommand(t, pythonSDKUnitTests)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Python SDK unit tests failed: %v stdout=%s stderr=%s", err, output, errs.String())
	}
	if len(output) != 0 || !strings.Contains(errs.String(), "OK") {
		t.Fatalf("tests wrote non-protocol stdout or failed: stdout=%s stderr=%s", output, errs.String())
	}
}

const pythonSDKUnitTests = `import io,json,math,unittest
from pogo_watch import Watcher,event,ProtocolError,ValidationError,ObservationFailure,CapacityError

def boot(mode='poll',state=None,cursor=None):
    return json.dumps({'v':1,'kind':'boot','mode':mode,'workspace':'/workspace','state':state,'cursor':cursor})+'\n'

def poll(events,state=None,cursor=None):
    output=io.StringIO()
    w=Watcher(mode='poll',stdin=io.StringIO(boot()),stdout=output)
    with w:
        w.emit(events,state=state,cursor=cursor)
    return json.loads(output.getvalue())

class ProtocolTests(unittest.TestCase):
    def test_content_ids_replay_and_event_to_quiet(self):
        ev=event('stable:42','important',{'z':1,'a':2})
        first=poll([ev],{'last':42},{'id':42})
        replay=poll([event('stable:42','important',{'a':2,'z':1})],{'last':42},{'id':42})
        quiet=poll([],{'last':42},{'id':42})
        self.assertEqual(first['id'],replay['id'])
        self.assertNotEqual(first['id'],quiet['id'])
        self.assertEqual(first['events'][0]['key'],'stable:42')
        self.assertNotEqual(first['id'],poll([ev],{'last':43},{'id':42})['id'])
        self.assertNotEqual(first['id'],poll([ev],{'last':42},{'id':43})['id'])

    def test_poll_proposal_is_not_falsely_confirmed(self):
        output=io.StringIO()
        w=Watcher(mode='poll',stdin=io.StringIO(boot(state={'n':0},cursor=1)),stdout=output)
        with w:
            w.checkpoint(state={'n':1},cursor=2)
            self.assertEqual(w.state,{'n':0})
            self.assertEqual(w.cursor,1)

    def test_poll_multiple_and_no_frame(self):
        output=io.StringIO()
        w=Watcher(mode='poll',stdin=io.StringIO(boot()),stdout=output)
        with self.assertRaises(ProtocolError):
            with w:
                w.checkpoint()
                with self.assertRaises(ProtocolError):w.checkpoint()
        self.assertEqual(len(output.getvalue().splitlines()),1)
        for mode in ('poll','stream'):
            with self.assertRaises(ProtocolError):
                with Watcher(mode=mode,stdin=io.StringIO(boot(mode)),stdout=io.StringIO()):pass

    def test_original_exception_is_not_masked(self):
        with self.assertRaisesRegex(RuntimeError,'source failed'):
            with Watcher(mode='poll',stdin=io.StringIO(boot()),stdout=io.StringIO()):
                raise RuntimeError('source failed')

    def test_none_differs_from_omission_and_constructor_is_quiet(self):
        output=io.StringIO()
        with Watcher(mode='poll',stdin=io.StringIO(boot(state={'n':1},cursor='saved')),stdout=output) as w:
            self.assertEqual(output.getvalue(),'')
            w.checkpoint(cursor=None)
        f=json.loads(output.getvalue())
        self.assertEqual(f['state'],{'n':1})
        self.assertIsNone(f['cursor'])
        self.assertNotIn('data',event('x','s'))
        self.assertIn('data',event('x','s',None))

    def assert_invalid(self,events,state=None,cursor=None):
        output=io.StringIO()
        w=Watcher(mode='poll',stdin=io.StringIO(boot()),stdout=output)
        with self.assertRaises(ValidationError):w.emit(events,state=state,cursor=cursor)
        self.assertEqual(output.getvalue(),'')
        w.checkpoint()

    def assert_capacity(self,events,state=None,cursor=None):
        output=io.StringIO()
        w=Watcher(mode='poll',stdin=io.StringIO(boot()),stdout=output)
        for action in (lambda:w.emit(events,state=state,cursor=cursor),lambda:w.checkpoint()):
            with self.assertRaises(ObservationFailure) as failure:action()
            self.assertEqual(failure.exception.reason,'capacity_exceeded')
            self.assertEqual(failure.exception.code,80)
        self.assertEqual(output.getvalue(),'')

    def test_size_limits(self):
        self.assertEqual(len(poll([event(str(i),'s') for i in range(64)])['events']),64)
        self.assert_capacity([event(str(i),'s') for i in range(65)])
        poll([],state='x'*(16384-2),cursor='x'*(16384-2))
        self.assert_capacity([],state='x'*(16384-1))
        self.assert_capacity([],cursor='x'*(16384-1))
        poll([event('x','s','x'*(32768-2))])
        with self.assertRaises(CapacityError):event('x','s','x'*(32768-1))
        self.assert_capacity([event(str(i),'x'*2048) for i in range(32)])
        event('中'*85,'s'*2048)
        with self.assertRaises(CapacityError):event('中'*86,'s')
        with self.assertRaises(CapacityError):event('x','s'*2049)
        # Go's JSON escaping is included in the state limit.
        self.assert_capacity([],state='<'*3000)

    def test_invalid_json_is_rejected_before_output(self):
        for value in (float('nan'),float('inf'),{1:'numeric key'},('\ud800'),{ 'x': object() },(1,2)):
            self.assert_invalid([],state=value)
        cycle=[]
        cycle.append(cycle)
        self.assert_invalid([],state=cycle)
        with self.assertRaises(ValidationError):event('\ud800','summary')
        with self.assertRaises(ValidationError):event('key','\ud800')
        self.assert_invalid([{'key':'k','summary':'s','unexpected':True}])

    def test_boot_validation_and_bounds(self):
        for raw in ('', '{}\n', 'x\n', '{"v":1,"v":1}\n', 'x'*65537+'\n', boot('stream')):
            with self.assertRaises(ProtocolError):
                Watcher(mode='poll',stdin=io.StringIO(raw),stdout=io.StringIO())
        with self.assertRaises(ProtocolError):
            Watcher(stdin=io.StringIO(boot(state='x'*16383)),stdout=io.StringIO())

    def test_binary_streams_and_partial_write_poison(self):
        output=io.BytesIO()
        with Watcher(mode='poll',stdin=io.BytesIO(boot().encode()),stdout=output) as w:
            w.checkpoint()
        self.assertEqual(json.loads(output.getvalue())['events'],[])
        class ShortWriter(io.StringIO):
            def write(self,line):
                super().write(line[:-1])
                return len(line)-1
        output=ShortWriter()
        w=Watcher(mode='poll',stdin=io.StringIO(boot()),stdout=output)
        with self.assertRaises(ProtocolError):w.checkpoint(state={'n':1})
        self.assertIsNone(w.state)
        before=output.getvalue()
        with self.assertRaises(ProtocolError):w.checkpoint()
        self.assertEqual(output.getvalue(),before)

unittest.main(verbosity=2)
`
