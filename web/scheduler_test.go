package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/schedule"
)

// quoteJSON marshals one string the way the API will parse it — a Windows
// path spliced raw into a JSON literal is an invalid \U escape waiting to
// happen.
func quoteJSON(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(raw)
}

// scheduleEnv points the schedule state at a temp dir and makes this process
// the one that fires: no OS backend anywhere in these tests.
func scheduleEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("POGO_SCHEDULE_DIR", dir)
	t.Setenv(schedule.BackendEnv, "internal")
	return dir
}

// schedulerManager builds a Manager whose timer is on, with schedRunner
// replaced so no child is ever spawned. The runner is a parameter because
// two tests need to observe or gate the launches.
func schedulerManager(t *testing.T, tick time.Duration, runner func(context.Context, schedule.Job, schedule.LogPaths, io.Writer) schedule.RunOutcome) *Manager {
	t.Helper()
	t.Setenv("KIMI_API_KEY", "test-key")
	installTestCatalog(t)
	restore := schedRunner
	schedRunner = runner
	t.Cleanup(func() { schedRunner = restore })
	mgr, err := NewManager(Config{
		Cwd:         t.TempDir(),
		SessionDir:  t.TempDir(),
		Model:       "k3",
		MaxTurns:    5,
		GateTimeout: 5 * time.Second,
		RunTimeout:  time.Minute,
		IdleTimeout: time.Hour,
		// The cadence the test wants, not the deployment default: assertions
		// wait on channels, so the tick only bounds how long a wiring bug
		// takes to notice, never correctness.
		ScheduleTickInterval: tick,
		NewClient: func(c config.Resolved, _ func(llm.RetryInfo)) llm.Client {
			return &scriptedClient{model: c.Model}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	return mgr
}

// recordingRunner is schedRunner for timer tests: every fire announces itself
// on a channel and counts, without spawning anything. The count lives beside
// the channel because consuming an announcement is the point of the channel —
// reading it back for a total would always say zero.
type recordingRunner struct {
	saw   chan string
	fired int32
}

func newRecordingRunner() *recordingRunner {
	return &recordingRunner{saw: make(chan string, 8)}
}

func TestManagerCloseCancelsAndDrainsScheduledWorkers(t *testing.T) {
	dir := scheduleEnv(t)
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	mgr := schedulerManager(t, 10*time.Millisecond, func(ctx context.Context, _ schedule.Job, _ schedule.LogPaths, _ io.Writer) schedule.RunOutcome {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return schedule.RunOutcome{ExitCode: 1, EndReason: "cancelled"}
	})
	st := schedule.NewStore(dir)
	if err := st.Add(schedule.Job{ID: "close-test", Prompt: "offline", Kind: schedule.KindAt, At: time.Now().Add(-time.Minute).Format(time.RFC3339), Enabled: true, CreatedAt: time.Now(), Workdir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled worker did not start")
	}
	closed := make(chan struct{})
	go func() { mgr.Close(); close(closed) }()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("manager close did not cancel scheduled worker")
	}
	select {
	case <-closed:
		t.Fatal("Close returned before worker bookkeeping ended")
	default:
	}
	if _, ok := mgr.startScheduledWork("late"); ok {
		t.Fatal("closed manager accepted scheduled work")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not drain scheduled worker")
	}
	if len(mgr.SchedRunning()) != 0 {
		t.Fatal("closed manager retained running schedule entries")
	}
}

func (r *recordingRunner) run(_ context.Context, job schedule.Job, _ schedule.LogPaths, _ io.Writer) schedule.RunOutcome {
	atomic.AddInt32(&r.fired, 1)
	r.saw <- job.ID
	return schedule.RunOutcome{SessionID: "sess-fake", EndReason: "completed", ExitCode: 0}
}

func (r *recordingRunner) count() int32 { return atomic.LoadInt32(&r.fired) }

// A never-run every-job fires without ever having a LastRunAt: the timer is
// the counter, and it counts from creation. This is the one behaviour
// schedule.NextRun deliberately does not have — "the OS counts from
// registration" — and the whole reason nextFire exists.
func TestNextFireAnchorsEveryJobAtCreation(t *testing.T) {
	now := time.Now()
	fresh := schedule.Job{Kind: schedule.KindEvery, EverySec: 60, Enabled: true, CreatedAt: now}
	if got := nextFire(fresh, now); got == nil || got.Sub(now) != 60*time.Second {
		t.Errorf("never-run every-job next = %v, want one interval from creation", got)
	}
	// Ran once: the anchor is the run, not creation.
	ran := fresh
	ran.LastRunAt = now.Add(-30 * time.Second)
	if got := nextFire(ran, now); got == nil || got.Sub(now) != 30*time.Second {
		t.Errorf("run every-job next = %v, want one interval from the last run", got)
	}
	// An at-job past its time and still armed is due once, not never.
	past := schedule.Job{Kind: schedule.KindAt, At: now.Add(-time.Hour).Format(time.RFC3339), Enabled: true, CreatedAt: now}
	if got := nextFire(past, now); got == nil || !got.Before(now) {
		t.Errorf("overdue at-job next = %v, want the past (one late run)", got)
	}
	// But once it has run — the run failed, say, so RunOnce left it armed —
	// the chance is spent. A past time plus a LastRunAt must not refire;
	// found the hard way, as a quota burner in a live deployment.
	spent := past
	spent.LastRunAt = now.Add(-time.Minute)
	if got := nextFire(spent, now); got != nil {
		t.Errorf("already-run at-job next = %v, want nil", got)
	}
}

// The wiring test: a job in the store fires through the timer, the run
// history records it as a scheduled trigger, and the fired one-shot disarms
// itself — RunOnce's bookkeeping, unchanged.
func TestSchedulerFiresDueJob(t *testing.T) {
	dir := scheduleEnv(t)
	rec := newRecordingRunner()
	schedulerManager(t, 100*time.Millisecond, rec.run)

	st := schedule.NewStore(dir)
	if err := st.Add(schedule.Job{
		ID: "once", Prompt: "hello", Kind: schedule.KindAt,
		At:      time.Now().Add(-time.Minute).Format(time.RFC3339),
		Enabled: true, CreatedAt: time.Now(), Workdir: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case id := <-rec.saw:
		if id != "once" {
			t.Errorf("fired %q, want once", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the timer never fired the due job")
	}

	// The run record says scheduled, and the one-shot disarmed itself; the
	// in-flight mark kept the following passes from double-firing before
	// LastRunAt landed.
	deadline := time.Now().Add(5 * time.Second)
	for {
		runs, err := st.Runs("once", 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) >= 1 && runs[len(runs)-1].Trigger == schedule.TriggerScheduled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run history after fire: %+v, want a scheduled run", runs)
		}
		time.Sleep(50 * time.Millisecond)
	}
	job, err := st.Find("once")
	if err != nil {
		t.Fatal(err)
	}
	if job.Enabled {
		t.Error("fired one-shot is still armed")
	}
	// Give a would-be second pass every chance, then insist on exactly one.
	time.Sleep(300 * time.Millisecond)
	if n := rec.count(); n != 1 {
		t.Errorf("fired %d times, want once (a disarmed job must not refire)", n)
	}
}

// Two jobs due at once both fire, and neither waits for the other: RunOnce
// blocks for a child's lifetime, so the launches must be concurrent. Job a
// holds its run until b has started; serial launches deadlock the test.
func TestSchedulerFiresDueJobsConcurrently(t *testing.T) {
	dir := scheduleEnv(t)
	rec := newRecordingRunner()
	bStarted := make(chan struct{})
	inner := rec.run
	schedulerManager(t, 100*time.Millisecond, func(ctx context.Context, job schedule.Job, logs schedule.LogPaths, mirror io.Writer) schedule.RunOutcome {
		if job.ID == "b" {
			select {
			case bStarted <- struct{}{}:
			default:
			}
		}
		if job.ID == "a" {
			select {
			case <-bStarted:
			case <-time.After(5 * time.Second): // fall through; the assertion below reports it
			}
		}
		return inner(ctx, job, logs, mirror)
	})

	st := schedule.NewStore(dir)
	past := time.Now().Add(-time.Minute).Format(time.RFC3339)
	for _, id := range []string{"a", "b"} {
		if err := st.Add(schedule.Job{
			ID: id, Prompt: "x", Kind: schedule.KindAt, At: past,
			Enabled: true, CreatedAt: time.Now(), Workdir: t.TempDir(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	gotA, gotB := false, false
	for i := 0; i < 40 && !(gotA && gotB); i++ {
		select {
		case id := <-rec.saw:
			gotA = gotA || id == "a"
			gotB = gotB || id == "b"
		case <-time.After(250 * time.Millisecond):
		}
	}
	if !gotA || !gotB {
		t.Fatalf("fired a=%v b=%v: one due job waited on another", gotA, gotB)
	}
	// Let the runs' bookkeeping land before TempDir cleanup races it.
	time.Sleep(300 * time.Millisecond)
}

// An every-job several intervals behind runs once, not N times, and the gap
// leaves a skipped record explaining the missed intervals.
func TestSchedulerCatchUpIsBoundedAndRecorded(t *testing.T) {
	dir := scheduleEnv(t)
	rec := newRecordingRunner()
	schedulerManager(t, 100*time.Millisecond, rec.run)

	// Created 5 minutes ago with a 60s interval: several fires were due
	// while nothing was watching, and the history must say so.
	if err := schedule.NewStore(dir).Add(schedule.Job{
		ID: "poll", Prompt: "x", Kind: schedule.KindEvery, EverySec: 60,
		Enabled: true, CreatedAt: time.Now().Add(-5 * time.Minute), Workdir: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-rec.saw:
	case <-time.After(5 * time.Second):
		t.Fatal("the overdue every-job never fired")
	}

	st := schedule.NewStore(dir)
	deadline := time.Now().Add(5 * time.Second)
	for {
		runs, err := st.Runs("poll", 10)
		if err != nil {
			t.Fatal(err)
		}
		var skipped bool
		for _, r := range runs {
			if r.Status == schedule.StatusSkipped && r.Error != "" {
				skipped = true
			}
		}
		if skipped && len(runs) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runs = %+v, want a missed record plus one real run", runs)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The next fire is a full interval away; no catch-up queue may exist.
	time.Sleep(300 * time.Millisecond)
	if n := rec.count(); n != 1 {
		t.Errorf("fired %d times for one overdue job, want a single catch-up run", n)
	}
}

// The timer stands down when an OS backend owns the triggers: firing anyway
// would double every trigger and litter the history with skips.
func TestInProcessFiringDefersToOSBackend(t *testing.T) {
	scheduleEnv(t)
	t.Setenv(schedule.BackendEnv, "launchd") // an OS backend is present
	if inProcessFiring(schedule.DefaultDir()) {
		t.Error("OS backend present: the timer must stand down")
	}
	t.Setenv(schedule.BackendEnv, "bogus") // a typo is loud, not silently owned
	if inProcessFiring(schedule.DefaultDir()) {
		t.Error("unknown forced backend: the timer must stand down rather than hide the typo")
	}
	t.Setenv(schedule.BackendEnv, "internal")
	if !inProcessFiring(schedule.DefaultDir()) {
		t.Error("forced internal: the timer must own firing")
	}
}

// A Detect failure meaning "no scheduler here" (a container) makes the API
// fall back to the in-process backend: the panel works instead of showing a
// banner, and created jobs carry backend "internal" so the panel can say who
// fires them.
func TestScheduleAPIFallsBackInProcess(t *testing.T) {
	front, dir := scheduleTestServer(t)
	t.Setenv(schedule.BackendEnv, "") // clear the helper's forcing: the seam decides
	restore := schedDetect
	schedDetect = func(string) (schedule.Backend, error) {
		return nil, fmt.Errorf("%w: simulated container", schedule.ErrNoScheduler)
	}
	t.Cleanup(func() { schedDetect = restore })

	code, got := scheduleRequest(t, front, "GET", "/api/schedule/jobs", "")
	if code != 200 || got["backend"] != "internal" {
		t.Errorf("GET without an OS scheduler: %d %v, want 200 with backend internal", code, got)
	}
	code, got = scheduleRequest(t, front, "POST", "/api/schedule/jobs",
		`{"prompt":"x","kind":"every","every_sec":3600,"workdir":`+quoteJSON(dir)+`}`)
	if code != 201 {
		t.Fatalf("POST without an OS scheduler: %d (%v), want 201", code, got)
	}
	if got["backend"] != "internal" {
		t.Errorf("created job's backend = %v, want internal", got["backend"])
	}
}

// ErrNoScheduler is what separates "no scheduler on this machine" from any
// other Detect failure; the sentinel has to survive the wrapping.
func TestErrNoSchedulerSurvivesWrapping(t *testing.T) {
	wrapped := fmt.Errorf("later: %w", fmt.Errorf("%w: inner", schedule.ErrNoScheduler))
	if !errors.Is(wrapped, schedule.ErrNoScheduler) {
		t.Error("sentinel does not survive wrapping")
	}
	if errors.Is(fmt.Errorf("unrelated"), schedule.ErrNoScheduler) {
		t.Error("unrelated error matches the sentinel")
	}
}

// The forced internal backend registers and unregisters as a no-op, and says
// the name the panel shows.
func TestInternalBackendForced(t *testing.T) {
	t.Setenv(schedule.BackendEnv, "internal")
	be, err := schedule.Detect(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if be.Name() != "internal" {
		t.Errorf("name = %q, want internal", be.Name())
	}
	if err := be.Register(schedule.Job{ID: "x"}, os.Args[0]); err != nil {
		t.Errorf("register: %v", err)
	}
	if err := be.Unregister(schedule.Job{ID: "x"}); err != nil {
		t.Errorf("unregister: %v", err)
	}
}
