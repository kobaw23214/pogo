package sentinelsdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"
)

func TestPythonBoundedHistoryFailsClosedWithoutEvictingProgress(t *testing.T) {
	cmd, errs := sdkCommand(t, pythonHistoryTests)
	output, err := cmd.Output()
	if err != nil || len(output) != 0 || !strings.Contains(errs.String(), "OK") {
		t.Fatalf("bounded history tests: err=%v stdout=%q stderr=%s", err, output, errs.String())
	}
}

func TestPythonObservationFailureUsesReservedExitWithoutSourceText(t *testing.T) {
	for code, reason := range map[int]string{80: "capacity_exceeded", 81: "incomplete_observation", 82: "source_unavailable", 83: "history_invalid"} {
		t.Run(reason, func(t *testing.T) {
			cmd, errs := sdkCommand(t, "from pogo_watch import Watcher\nwith Watcher(mode='poll') as w:\n    w.fail('"+reason+"')\n")
			var boot bytes.Buffer
			writeBoot(t, &boot, "poll")
			cmd.Stdin = &boot
			out, err := cmd.Output()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != code || len(out) != 0 || errs.Len() != 0 {
				t.Fatalf("failure did not use safe fixed code: err=%v output=%q stderr=%q", err, out, errs.String())
			}
			diagnostic, ok := ExitDiagnostic(code)
			if !ok || !strings.HasPrefix(diagnostic, reason+":") {
				t.Fatalf("host and helper disagree: code=%d diagnostic=%q", code, diagnostic)
			}
		})
	}
	for _, code := range []int{-1, 0, 1, 7, 78, 79, 84, 127, 137} {
		if text, ok := ExitDiagnostic(code); ok || text != "" {
			t.Fatalf("ordinary/interrupt exit mapped to a source declaration: code=%d text=%q", code, text)
		}
	}
}

func TestPythonCapacityFailureAfterProposalStillExitsNonzero(t *testing.T) {
	cmd, errs := sdkCommand(t, `from pogo_watch import Watcher,ObservationFailure
with Watcher(mode='poll') as w:
    w.checkpoint(state={'proposed':True},cursor=999)
    try:
        w.fail('capacity_exceeded')
    except BaseException:
        pass
    try:
        w.checkpoint(cursor=1000)
    except ObservationFailure:
        pass
`)
	var boot bytes.Buffer
	writeBoot(t, &boot, "poll")
	cmd.Stdin = &boot
	out, err := cmd.Output()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 80 || errs.Len() != 0 {
		t.Fatalf("capacity failure was swallowed: err=%v stderr=%q", err, errs.String())
	}
	var proposed map[string]any
	if err := json.Unmarshal(out, &proposed); err != nil || proposed["cursor"] != float64(999) {
		t.Fatalf("expected just the pre-failure proposal, not a success: out=%q err=%v", out, err)
	}
}

func TestPythonStreamHistoryCapacityKeepsLastACKedProgress(t *testing.T) {
	db, _, watch := newRecoveryStore(t)
	p := startRecoveryProcess(t, `from pogo_watch import Watcher,ObservationFailure
with Watcher(mode='stream') as w:
    h=w.history(w.state,max_entries=1,max_bytes=200)
    h.remember('source:accepted')
    w.checkpoint(state=h.snapshot(),cursor={'position':7})
    try:
        h.remember('source:not-consumed')
    except BaseException:
        assert w.state == {'v':1,'keys':['source:accepted']}
        assert w.cursor == {'position':7}
    try:
        w.checkpoint(cursor={'position':8})
    except ObservationFailure:
        pass
`, nil, nil)
	frame := readFrame(t, p.reader)
	if err := db.Accept(watch.Spec.ID, watch.Spec.Generation, frame); err != nil {
		t.Fatal(err)
	}
	ackRecoveryFrame(t, p, frame.ID)
	if raw, err := io.ReadAll(p.reader); err != nil || len(raw) != 0 {
		t.Fatalf("failed history produced another proposal: output=%q err=%v", raw, err)
	}
	var exit *exec.ExitError
	if err := p.wait(); !errors.As(err, &exit) || exit.ExitCode() != 80 {
		t.Fatalf("caught capacity failure did not stop stream: err=%v stderr=%s", err, p.errs.String())
	}
	saved, err := db.Get(watch.Spec.ID)
	if err != nil || saved.Pending != 0 || string(saved.Cursor) != `{"position":7}` || string(saved.State) != `{"keys":["source:accepted"],"v":1}` {
		t.Fatalf("capacity failure discarded accepted progress or consumed another record: watch=%+v err=%v", saved, err)
	}
}

func TestPythonSDKSizeCeilingsUseCapacityExitBeforeProgress(t *testing.T) {
	cases := map[string]string{
		"state":                   `w.checkpoint(state='x'*17000)`,
		"cursor":                  `w.checkpoint(cursor='x'*17000)`,
		"state-go-escaped":        `w.checkpoint(state='<'*3000)`,
		"cursor-go-escaped":       `w.checkpoint(cursor='&'*3000)`,
		"history-enclosing-state": `h=w.history(None); h.remember('source:one'); w.checkpoint(state={'history':h.snapshot(),'extra':'x'*17000},cursor='not-consumed')`,
		"event-count":             `w.emit([{'key':str(i),'summary':'s'} for i in range(65)])`,
		"frame":                   `w.emit([{'key':str(i),'summary':'x'*2048} for i in range(32)])`,
		"raw-data":                `w.emit([{'key':'id','summary':'s','data':'x'*32767}])`,
		"raw-key":                 `w.emit([{'key':'中'*86,'summary':'s'}])`,
		"raw-summary":             `w.emit([{'key':'id','summary':'x'*2049}])`,
		"constructor-data":        `event('id','s','x'*32767)`,
		"constructor-key":         `event('中'*86,'s')`,
		"constructor-summary":     `event('id','s'*2049)`,
		"data-go-escaped":         `event('id','s','<'*6000)`,
	}
	for name, operation := range cases {
		t.Run(name, func(t *testing.T) {
			code := fmt.Sprintf("from pogo_watch import Watcher,event\nwith Watcher(mode='poll') as w:\n    %s\n", operation)
			cmd, errs := sdkCommand(t, code)
			var boot bytes.Buffer
			writeBoot(t, &boot, "poll")
			cmd.Stdin = &boot
			out, err := cmd.Output()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 80 || len(out) != 0 || errs.Len() != 0 {
				t.Fatalf("known size ceiling lost safe capacity classification: err=%v output=%q stderr=%q", err, out, errs.String())
			}
		})
	}
}

func TestPythonStatelessEventCapacityExits80WithoutWith(t *testing.T) {
	for name, operation := range map[string]string{
		"data":    `event('id','s','x'*32767)`,
		"key":     `event('中'*86,'s')`,
		"summary": `event('id','s'*2049)`,
	} {
		t.Run(name, func(t *testing.T) {
			cmd, errs := sdkCommand(t, "from pogo_watch import Watcher,event\nw=Watcher(mode='poll')\n"+operation+"\n")
			var boot bytes.Buffer
			writeBoot(t, &boot, "poll")
			cmd.Stdin = &boot
			out, err := cmd.Output()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 80 || len(out) != 0 || errs.Len() != 0 {
				t.Fatalf("unhandled constructor capacity lost its fixed exit: err=%v output=%q stderr=%q", err, out, errs.String())
			}
		})
	}
}

const pythonHistoryTests = `import io,json,unittest
from pogo_watch import Watcher,ObservationFailure,CapacityError,ValidationError,event

def client(state=None,cursor='saved'):
    output=io.StringIO()
    boot=json.dumps({'v':1,'kind':'boot','mode':'poll','workspace':'/workspace','state':state,'cursor':cursor})+'\n'
    return Watcher(mode='poll',stdin=io.StringIO(boot),stdout=output),output

class HistoryTests(unittest.TestCase):
    def assert_poisoned(self,w,output,reason,code):
        for action in (lambda:w.checkpoint(cursor=None),lambda:w.__exit__(None,None,None)):
            with self.assertRaises(ObservationFailure) as stopped:action()
            self.assertEqual(stopped.exception.reason,reason)
            self.assertEqual(stopped.exception.code,code)
        self.assertEqual(output.getvalue(),'')

    def test_restore_proposal_and_replay_do_not_mutate_confirmed_state(self):
        saved={'history':{'v':1,'keys':['source:old']},'other':{'position':7}}
        w,output=client(saved)
        with w:
            h=w.history(w.state['history'],max_entries=2,max_bytes=200)
            self.assertTrue(h.seen('source:old'))
            self.assertFalse(h.remember('source:old'))
            self.assertTrue(h.remember('source:new'))
            snapshot=h.snapshot();snapshot['keys'].clear()
            self.assertEqual(h.snapshot()['keys'],['source:old','source:new'])
            self.assertEqual(w.state,saved)
            w.emit([event('source:new','observed')],state={'history':h.snapshot(),'other':saved['other']},cursor='next')
            self.assertEqual(w.state,saved)
            self.assertEqual(w.cursor,'saved')
        frame=json.loads(output.getvalue())
        self.assertEqual(frame['state']['history']['keys'],['source:old','source:new'])
        self.assertEqual(frame['cursor'],'next')

    def test_full_history_is_not_evicted_and_caught_failure_cannot_quiet(self):
        saved={'v':1,'keys':['already-reported']}
        w,output=client(saved)
        h=w.history(w.state,max_entries=1,max_bytes=200)
        with self.assertRaises(ObservationFailure) as stopped:h.remember('new-version')
        self.assertEqual(stopped.exception.reason,'capacity_exceeded')
        self.assertEqual(h.snapshot(),saved)
        self.assertEqual(w.state,saved)
        self.assertEqual(w.cursor,'saved')
        self.assert_poisoned(w,output,'capacity_exceeded',80)

    def test_host_escaped_bytes_not_unescaped_size_limit_history(self):
        for key in ('id:'+'&'*20,'id:'+'<'*20,'id:'+'>'*20,'id:'+'\u2028'*20,'id:'+'\u2029'*20):
            w,output=client()
            h=w.history(None,max_entries=2,max_bytes=100)
            with self.assertRaises(ObservationFailure):h.remember(key)
            self.assertEqual(h.snapshot(),{'v':1,'keys':[]})
            self.assert_poisoned(w,output,'capacity_exceeded',80)

    def test_restored_capacity_never_discards_older_identities(self):
        saved={'v':1,'keys':['first','second']}
        w,output=client(saved)
        with self.assertRaises(ObservationFailure):w.history(w.state,max_entries=1,max_bytes=200)
        self.assertEqual(w.state,saved)
        self.assert_poisoned(w,output,'capacity_exceeded',80)

    def test_incompatible_history_never_becomes_fresh_baseline(self):
        for saved in ({'legacy':['one']},{'v':2,'keys':['one']},{'v':True,'keys':[]},['one'],{'v':1,'keys':['one','one']},{'v':1,'keys':[{'source':'one'}]}):
            w,output=client(saved)
            with self.assertRaises(ObservationFailure):w.history(w.state)
            self.assertEqual(w.state,saved)
            self.assert_poisoned(w,output,'history_invalid',83)

    def test_source_failure_cannot_be_swallowed_as_empty_success(self):
        for reason,code in (('incomplete_observation',81),('source_unavailable',82)):
            w,output=client({'position':7},7)
            with self.assertRaises(ObservationFailure):w.fail(reason)
            self.assertEqual(w.state,{'position':7})
            self.assertEqual(w.cursor,7)
            self.assert_poisoned(w,output,reason,code)

    def test_unknown_failure_code_cannot_echo_source_secrets_or_resume(self):
        w,output=client()
        with self.assertRaises(ObservationFailure) as stopped:w.fail('SECRET_SOURCE_DIAGNOSTIC')
        self.assertEqual(stopped.exception.reason,'incomplete_observation')
        self.assertNotIn('SECRET',str(stopped.exception))
        self.assert_poisoned(w,output,'incomplete_observation',81)

    def test_new_history_is_explicit_and_bounds_are_never_silently_adjusted(self):
        w,output=client()
        h=w.history(None,max_entries=1,max_bytes=200)
        self.assertEqual(h.snapshot(),{'v':1,'keys':[]})
        for entries,limit in ((0,200),(True,200),(1,16385),(1,0)):
            w,output=client()
            with self.assertRaises(ObservationFailure):w.history(None,max_entries=entries,max_bytes=limit)
            self.assert_poisoned(w,output,'capacity_exceeded',80)

    def test_all_size_failures_poison_even_after_baseexception_is_caught(self):
        operations=(
            lambda w:w.checkpoint(state='x'*17000),
            lambda w:w.checkpoint(cursor='x'*17000),
            lambda w:w.checkpoint(state='<'*3000),
            lambda w:w.checkpoint(cursor='&'*3000),
            lambda w:w.checkpoint(state={'history':w.history(None).snapshot(),'extra':'x'*17000}),
            lambda w:w.emit([{'key':str(i),'summary':'s'} for i in range(65)]),
            lambda w:w.emit([{'key':str(i),'summary':'x'*2048} for i in range(32)]),
            lambda w:w.emit([{'key':'id','summary':'s','data':'x'*32767}]),
            lambda w:event('中'*86,'s'),
            lambda w:event('id','s'*2049),
            lambda w:event('id','s','<'*6000),
        )
        for operation in operations:
            saved={'last_complete':7}
            w,output=client(saved,7)
            with self.assertRaises(CapacityError) as stopped:
                with w:
                    try:operation(w)
                    except BaseException:pass
                    self.assertEqual(w.state,saved)
                    self.assertEqual(w.cursor,7)
                    with self.assertRaises(CapacityError):w.checkpoint(cursor=None)
            self.assertEqual(stopped.exception.code,80)
            self.assertEqual(output.getvalue(),'')

    def test_json_format_validation_remains_distinct_and_recoverable(self):
        w,output=client({'position':7},7)
        with w:
            for bad in ({'key':'id','summary':'s','data':float('nan')},
                        {'key':'','summary':'s'}, {'key':'id','summary':None},
                        {'key':'id','summary':'s','unknown':True}):
                with self.assertRaises(ValidationError):w.emit([bad])
            with self.assertRaises(ValidationError):event('id','s',float('inf'))
            with self.assertRaises(ValidationError):w.emit({})
            w.checkpoint()
        frame=json.loads(output.getvalue())
        self.assertEqual(frame['state'],{'position':7})
        self.assertEqual(frame['cursor'],7)
        self.assertFalse(issubclass(CapacityError,Exception))

    def test_failed_inner_client_does_not_poison_outer_context(self):
        outer,outer_output=client()
        inner,inner_output=client()
        with outer:
            with self.assertRaises(CapacityError):
                with inner:
                    try:event('id','s'*2049)
                    except BaseException:pass
            outer.checkpoint(state={'outer':'complete'})
        self.assertEqual(inner_output.getvalue(),'')
        self.assertEqual(json.loads(outer_output.getvalue())['state'],{'outer':'complete'})
        next_client,next_output=client()
        with next_client:next_client.checkpoint()
        self.assertEqual(json.loads(next_output.getvalue())['events'],[])

    def test_emit_binds_capacity_to_its_own_client(self):
        outer,outer_output=client()
        other,other_output=client()
        with outer:
            with self.assertRaises(CapacityError):
                other.emit([{'key':'id','summary':'s','data':'x'*32767}])
            outer.checkpoint()
        self.assertEqual(other_output.getvalue(),'')
        self.assertEqual(json.loads(outer_output.getvalue())['events'],[])

unittest.main(verbosity=1)
`
