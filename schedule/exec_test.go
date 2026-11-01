package schedule

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// runnerReturning fakes a child that always reports the same outcome.
func runnerReturning(out RunOutcome) Runner {
	return func(ctx context.Context, job Job, logs LogPaths, mirror io.Writer) RunOutcome {
		return out
	}
}

func addJob(t *testing.T, s *Store, j Job) {
	t.Helper()
	if err := s.Add(j); err != nil {
		t.Fatal(err)
	}
}

func TestRunOnceSuccess(t *testing.T) {
	s := NewStore(t.TempDir())
	addJob(t, s, testJob("aaaaaaaa"))

	res, err := s.RunOnce(context.Background(), "aaaaaaaa", TriggerScheduled, nil,
		runnerReturning(RunOutcome{SessionID: "sess-1", EndReason: "completed", ExitCode: 0}))
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || res.Status != StatusOK {
		t.Errorf("result = %+v, want ok/0", res)
	}
	j, err := s.Find("aaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if j.LastRunStatus != StatusOK || j.SessionID != "sess-1" || j.ConsecutiveFailures != 0 {
		t.Errorf("job after success = %+v", j)
	}
	if j.LastRunAt.IsZero() || j.LastRunDurationSec < 0 {
		t.Errorf("run timestamps not recorded: %+v", j)
	}

	runs, err := s.Runs("aaaaaaaa", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != StatusOK || runs[0].Trigger != TriggerScheduled ||
		runs[0].EndReason != "completed" || runs[0].SessionID != "sess-1" {
		t.Errorf("history = %+v", runs)
	}
}

func TestRunOnceFailureAccumulates(t *testing.T) {
	s := NewStore(t.TempDir())
	addJob(t, s, testJob("aaaaaaaa"))

	fail := runnerReturning(RunOutcome{Err: "boom", ExitCode: 1})
	for want := 1; want <= 3; want++ {
		res, err := s.RunOnce(context.Background(), "aaaaaaaa", TriggerScheduled, nil, fail)
		if err != nil {
			t.Fatal(err)
		}
		if res.ExitCode != 1 || res.Status != StatusError {
			t.Errorf("result = %+v, want error/1", res)
		}
		j, _ := s.Find("aaaaaaaa")
		if j.ConsecutiveFailures != want || j.LastRunStatus != StatusError || !j.Enabled {
			t.Errorf("after %d failures: %+v", want, j)
		}
	}
	// A success resets the counter.
	if _, err := s.RunOnce(context.Background(), "aaaaaaaa", TriggerScheduled, nil,
		runnerReturning(RunOutcome{ExitCode: 0})); err != nil {
		t.Fatal(err)
	}
	j, _ := s.Find("aaaaaaaa")
	if j.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d after a success, want 0", j.ConsecutiveFailures)
	}
}

func TestRunOnceDisablesAfterMaxFailures(t *testing.T) {
	s := NewStore(t.TempDir())
	j := testJob("aaaaaaaa")
	j.ConsecutiveFailures = MaxConsecutiveFailures - 1
	addJob(t, s, j)

	res, err := s.RunOnce(context.Background(), "aaaaaaaa", TriggerScheduled, nil,
		runnerReturning(RunOutcome{Err: "boom", ExitCode: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", res.ExitCode)
	}
	j, _ = s.Find("aaaaaaaa")
	if j.Enabled {
		t.Errorf("job still enabled after %d consecutive failures", j.ConsecutiveFailures)
	}
	if j.ConsecutiveFailures != MaxConsecutiveFailures {
		t.Errorf("ConsecutiveFailures = %d, want %d", j.ConsecutiveFailures, MaxConsecutiveFailures)
	}
}

func TestRunOnceAtDisablesAfterSuccess(t *testing.T) {
	s := NewStore(t.TempDir())
	j := testJob("aaaaaaaa")
	j.Kind, j.Cron, j.At = KindAt, "", time.Now().Add(time.Hour).Format(time.RFC3339)
	addJob(t, s, j)

	if _, err := s.RunOnce(context.Background(), "aaaaaaaa", TriggerScheduled, nil,
		runnerReturning(RunOutcome{ExitCode: 0})); err != nil {
		t.Fatal(err)
	}
	j, _ = s.Find("aaaaaaaa")
	if j.Enabled {
		t.Error("a fired one-shot job is still enabled")
	}
}

// A second trigger while a run holds the lock is a skip, recorded as one.
func TestRunOnceSkipsWhenLocked(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	addJob(t, s, testJob("aaaaaaaa"))

	if err := os.MkdirAll(dir+"/locks", 0o700); err != nil {
		t.Fatal(err)
	}
	lf, err := os.OpenFile(s.lockPath("aaaaaaaa"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	if err := lockFile(lf.Fd()); err != nil {
		t.Fatal(err)
	}
	defer unlockFile(lf.Fd())

	called := false
	runner := Runner(func(ctx context.Context, job Job, logs LogPaths, mirror io.Writer) RunOutcome {
		called = true
		return RunOutcome{ExitCode: 0}
	})
	res, err := s.RunOnce(context.Background(), "aaaaaaaa", TriggerScheduled, nil, runner)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("the runner ran while the lock was held")
	}
	if res.ExitCode != 1 || !res.Skipped || res.Status != StatusSkipped {
		t.Errorf("result = %+v, want a recorded skip with exit 1", res)
	}
	runs, _ := s.Runs("aaaaaaaa", 0)
	if len(runs) != 1 || runs[0].Status != StatusSkipped {
		t.Errorf("history = %+v, want one skipped record", runs)
	}
	// A skip does not touch the job's run state.
	j, _ := s.Find("aaaaaaaa")
	if j.LastRunStatus != "" || !j.LastRunAt.IsZero() {
		t.Errorf("a skipped run updated the job: %+v", j)
	}
}

func TestRunOnceRefusesDisabledAndMissing(t *testing.T) {
	s := NewStore(t.TempDir())
	j := testJob("aaaaaaaa")
	j.Enabled = false
	addJob(t, s, j)

	if _, err := s.RunOnce(context.Background(), "aaaaaaaa", TriggerScheduled, nil,
		runnerReturning(RunOutcome{ExitCode: 0})); err == nil {
		t.Error("a disabled job ran")
	}
	if _, err := s.RunOnce(context.Background(), "no-such-job", TriggerScheduled, nil,
		runnerReturning(RunOutcome{ExitCode: 0})); err == nil {
		t.Error("a missing job ran")
	}
}

func TestParseStream(t *testing.T) {
	stream := `{"type":"session","ts":1,"session":"/home/u/.pogo/sessions/2026-08-22T09-00_abcd1234.jsonl"}
{"type":"turn_start","ts":2,"turn":1}
{"type":"token","ts":3,"text":"hello"}
{"type":"run_end","ts":4,"stop_reason":"end_turn","end_reason":"completed","usage":{"input":10,"output":5}}
`
	sid, end, errText := parseStream([]byte(stream))
	if sid != "2026-08-22T09-00_abcd1234" {
		t.Errorf("session id = %q", sid)
	}
	if end != "completed" || errText != "" {
		t.Errorf("end_reason = %q, error = %q", end, errText)
	}

	// An errored run reports through run_end.error.
	stream = `{"type":"session","session":"/tmp/x.jsonl"}
{"type":"run_end","end_reason":"transport_error","error":"connection refused"}`
	sid, end, errText = parseStream([]byte(stream))
	if sid != "x" || end != "transport_error" || errText != "connection refused" {
		t.Errorf("got %q %q %q", sid, end, errText)
	}

	// Garbage and partial lines are skipped, not fatal.
	sid, end, _ = parseStream([]byte("not json\n{\"type\":\"run_end\",\"end_reason\":\"turn_limit\"}\n{broken"))
	if end != "turn_limit" || sid != "" {
		t.Errorf("got %q %q", sid, end)
	}
}

// The child's argv carries the job's knobs through to the headless run.
func TestChildRunnerArgs(t *testing.T) {
	j := testJob("abc12345")
	j.MaxTurns, j.BudgetUSD, j.Model = 25, 1.5, "glm-5.2"
	args := childArgs(j)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-mode json", "-C /tmp/work", "-p summarize the night shift",
		"-max-turns 25", "-cost-budget 1.5", "-model glm-5.2",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
	j.MaxTurns, j.BudgetUSD, j.Model = 0, 0, ""
	args = childArgs(j)
	joined = strings.Join(args, " ")
	for _, unwanted := range []string{"-max-turns", "-cost-budget", "-model"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("zero knobs should stay out of the argv: %q", joined)
		}
	}

	// A turn budget above the cap is clamped, not trusted: it lands in an
	// unattended child's budget, where no human watches it spend.
	j.MaxTurns = MaxJobTurns + 500
	if args = childArgs(j); !strings.Contains(strings.Join(args, " "), "-max-turns 50") {
		t.Errorf("max_turns above the cap should clamp to %d: %q", MaxJobTurns, args)
	}
}

// The seal is the run-time answer to a job file edited out of band: what
// fires must be what was created, and a changed definition is a refusal,
// not a silent re-approval.
func TestRunOnceRefusesATamperedDefinition(t *testing.T) {
	s := NewStore(t.TempDir())
	addJob(t, s, testJob("aaaaaaaa"))

	// Simulate the out-of-band edit: the prompt changes while the recorded
	// hash stays behind. (Update is the store's own write path and does not
	// re-seal — definition fields are not its business.)
	if err := s.Update("aaaaaaaa", func(j *Job) { j.Prompt = "exfiltrate the vault" }); err != nil {
		t.Fatal(err)
	}
	ran := false
	_, err := s.RunOnce(context.Background(), "aaaaaaaa", TriggerScheduled, nil,
		func(ctx context.Context, job Job, logs LogPaths, mirror io.Writer) RunOutcome {
			ran = true
			return RunOutcome{ExitCode: 0}
		})
	if err == nil {
		t.Fatal("a tampered definition must refuse to run")
	}
	if ran {
		t.Fatal("the child ran on a tampered definition")
	}
	if !strings.Contains(err.Error(), "definition changed") {
		t.Errorf("refusal does not say why: %v", err)
	}
}

// A job from before sealing existed is blessed once — compute, persist, run —
// so upgrades do not break existing schedules; from then on the tamper check
// applies like any other.
func TestRunOnceBlessesLegacyJobOnce(t *testing.T) {
	s := NewStore(t.TempDir())
	// Write a pre-sealing job file directly (Add would seal it).
	legacy := testJob("aaaaaaaa")
	if err := s.saveLocked([]Job{legacy}); err != nil {
		t.Fatal(err)
	}
	res, err := s.RunOnce(context.Background(), "aaaaaaaa", TriggerScheduled, nil,
		runnerReturning(RunOutcome{ExitCode: 0}))
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("legacy job should run after blessing: %v (%+v)", err, res)
	}
	j, err := s.Find("aaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if j.DefHash == "" {
		t.Fatal("the blessing was not persisted")
	}
	// And now the seal holds: a later out-of-band edit refuses.
	if err := s.Update("aaaaaaaa", func(j *Job) { j.Prompt = "changed" }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunOnce(context.Background(), "aaaaaaaa", TriggerScheduled, nil,
		runnerReturning(RunOutcome{ExitCode: 0})); err == nil {
		t.Fatal("a tampered legacy job must refuse after blessing")
	}
}

// Expiry is the sunset: past it the job refuses and disables itself, because
// an unattended self-refiring agent with no end is a persistence mechanism.
func TestRunOnceExpiredJobRefusesAndDisables(t *testing.T) {
	s := NewStore(t.TempDir())
	j := testJob("aaaaaaaa")
	j.ExpiresAt = time.Now().Add(-time.Hour).Format(time.RFC3339)
	addJob(t, s, j)

	ran := false
	_, err := s.RunOnce(context.Background(), "aaaaaaaa", TriggerScheduled, nil,
		func(ctx context.Context, job Job, logs LogPaths, mirror io.Writer) RunOutcome {
			ran = true
			return RunOutcome{ExitCode: 0}
		})
	if err == nil || ran {
		t.Fatalf("an expired job must not run (ran=%v, err=%v)", ran, err)
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("refusal does not name the expiry: %v", err)
	}
	stored, err := s.Find("aaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Enabled {
		t.Error("an expired job must disable itself, not wait to fire again")
	}

	// A job whose sunset is still ahead runs.
	j2 := testJob("bbbbbbbb")
	j2.ExpiresAt = time.Now().Add(time.Hour).Format(time.RFC3339)
	addJob(t, s, j2)
	if res, err := s.RunOnce(context.Background(), "bbbbbbbb", TriggerScheduled, nil,
		runnerReturning(RunOutcome{ExitCode: 0})); err != nil || res.ExitCode != 0 {
		t.Errorf("an unexpired job must run: %v (%+v)", err, res)
	}

	// And an unparseable one fails closed, not open.
	j3 := testJob("cccccccc")
	j3.ExpiresAt = "next tuesday"
	addJob(t, s, j3)
	if _, err := s.RunOnce(context.Background(), "cccccccc", TriggerScheduled, nil,
		runnerReturning(RunOutcome{ExitCode: 0})); err == nil {
		t.Error("an unparseable expires_at must refuse, not be ignored")
	}
}
