package web

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/yosukeno/pogo/schedule"
)

// The scheduled jobs' in-process trigger.
//
// The CLI philosophy hands the clock to the OS scheduler because a terminal
// process is short-lived (see schedule/backend.go). The web server is the one
// long-lived process pogo has — the same fact that puts the spill sweep in
// the Manager — so on a machine with no OS scheduler (a container: no systemd
// user manager, no crontab) it is also the only honest place for the clock to
// live. This file is that clock: it watches jobs.json and fires the jobs
// itself, through the same Store.RunOnce the OS backends' -schedule-exec
// calls, so locks, run history, auto-disable and the panel's running flag are
// shared, not duplicated.
//
// The loop is next-deadline shaped rather than a fixed tick: each pass
// computes the earliest fire across enabled jobs and sleeps until min(that,
// one re-read interval). The re-read interval is what makes a job added by
// the CLI or the panel visible to the timer without a file watcher.

// DefaultScheduleTickInterval is how often the timer re-reads jobs.json when
// no fire is due sooner. Short enough that a job created in the panel starts
// counting within moments of the dialog closing; long enough that the read —
// one small JSON file — is noise.
const DefaultScheduleTickInterval = 15 * time.Second

// inProcessFiring reports whether this process is the one that must fire the
// jobs: a Detect failure that means "no scheduler here" (see ErrNoScheduler),
// or a backend that installs nothing itself (internal, noop). An OS backend
// owning the triggers means double firing if this timer ran too — the flock
// would serialise them, but every OS fire would leave a skip record behind,
// so the timer stands down instead. A forced-but-unknown backend value also
// stands down: that is a typo, and firing despite it would hide the mistake
// the API is busy reporting.
func inProcessFiring(dir string) bool {
	be, err := schedDetect(dir)
	if err != nil {
		return errors.Is(err, schedule.ErrNoScheduler)
	}
	switch be.Name() {
	case "internal", "noop":
		return true
	}
	return false
}

// nextFire is when j fires next, or nil when it never does. schedule.NextRun
// does the work; the two gaps it leaves on purpose are closed here, because
// both only exist while this process owns the clock:
//
//   - an every-job that has never run: "the OS counts from registration,
//     which the store does not record" — here *we* are the counter, and the
//     nearest thing to registration time the store keeps is CreatedAt.
//   - an at-job whose time has passed while still enabled: NextRun says nil
//     (it never fires again), but if the server was down at the moment, the
//     honest move is one late run. "Once" is structural: the job's chance is
//     used the moment it has run at all, successfully or not — an erroring
//     one-shot that refires every pass is a quota burner, and the OS backends
//     agree without saying so (their one-shot trigger is consumed whether
//     the run succeeded).
//
// Cron jobs get no special case: their period is not cheaply expressible, so
// a missed cron fire simply runs once on the next pass and re-synchronises.
func nextFire(j schedule.Job, now time.Time) *time.Time {
	if !j.Enabled {
		return nil
	}
	// An expired job never fires here either — RunOnce would refuse it at the
	// door (and disable it), but the timer should not keep waking up for a job
	// whose sunset has passed.
	if j.ExpiresAt != "" {
		if exp, err := time.Parse(time.RFC3339, j.ExpiresAt); err == nil && !exp.After(now) {
			return nil
		}
	}
	reference := now
	if j.Kind == schedule.KindCron {
		reference = j.CreatedAt
		if j.ScheduleFrom != nil && j.ScheduleFrom.After(reference) {
			reference = *j.ScheduleFrom
		}
		if j.LastRunAt.After(reference) {
			reference = j.LastRunAt
		}
		if j.LastQueuedAt.After(reference) {
			reference = j.LastQueuedAt
		}
		if reference.IsZero() {
			reference = now
		}
	}
	if n := schedule.NextRun(j, reference); n != nil {
		return n
	}
	switch j.Kind {
	case schedule.KindEvery:
		if schedule.ValidEverySeconds(j.EverySec) && j.LastRunAt.IsZero() {
			next := j.CreatedAt.Add(time.Duration(j.EverySec) * time.Second)
			return &next
		}
	case schedule.KindAt:
		if !j.LastRunAt.IsZero() || !j.LastQueuedAt.IsZero() {
			return nil // already had its run; a failure is a fact, not a debt
		}
		if at, err := time.Parse(time.RFC3339, j.At); err == nil && !at.After(now) {
			return &at
		}
	}
	return nil
}

// runSchedule is the goroutine: pass-sleep-pass. promptJobsFire mirrors
// inProcessFiring's answer for prompt jobs; workflow jobs always fire here,
// because the workflow runner only exists in this process — they register
// with the internal backend precisely so no OS trigger ever double-fires
// them.
func (m *Manager) runSchedule() {
	defer m.wg.Done()
	dir := schedule.DefaultDir()
	promptJobsFire := inProcessFiring(dir)
	for {
		now := time.Now()
		next := m.fireDuePass(now, promptJobsFire)
		wait := m.cfg.ScheduleTickInterval
		if next != nil {
			if d := time.Until(*next); d < wait {
				wait = d
			}
		}
		// A floor, so a job whose next fire is already past cannot turn the
		// loop into a spin: the pass that just ran it recomputed from now.
		if wait < 250*time.Millisecond {
			wait = 250 * time.Millisecond
		}
		select {
		case <-m.stop:
			return
		case <-time.After(wait):
		}
	}
}

// fireDuePass runs every job whose fire time has arrived and reports the
// earliest future fire, so the goroutine can sleep at the deadline instead of
// polling. Runs launch in their own goroutine — RunOnce blocks for the
// child's lifetime (up to DefaultJobTimeout), and one long job must not hold
// another job's fire hostage. Overlap safety is RunOnce's flock, exactly as
// it is between an OS trigger and a manual run-now.
func (m *Manager) fireDuePass(now time.Time, promptJobsFire bool) *time.Time {
	if active, _ := m.releaseMaintenance(); active {
		return nil
	}
	jobs, err := schedule.NewStore(schedule.DefaultDir()).Load()
	if err != nil {
		// Pause this pass, say it once: the API is reporting the same
		// problem to anyone who opens the panel.
		fmt.Fprintf(os.Stderr, "pogo: schedule timer paused: %v\n", err)
		return nil
	}
	var earliest *time.Time
	for _, j := range jobs {
		if !j.Enabled {
			continue
		}
		if j.AgentID != "" {
			m.mu.Lock()
			ready := m.scheduleOwnerValid != nil
			m.mu.Unlock()
			if !ready {
				continue
			}
		}

		if j.WorkflowID == "" && j.AgentID == "" && j.Timezone == "" && !promptJobsFire {
			continue
		}
		fire := nextFire(j, now)
		if fire == nil {
			continue
		}
		if fire.After(now) {
			if earliest == nil || fire.Before(*earliest) {
				earliest = fire
			}
			continue
		}
		m.fireScheduled(j, *fire, now)
	}
	return earliest
}

// fireScheduled launches one due job: records what was missed where the
// period makes that countable, marks the job in flight for the panel, and
// runs it through the shared bookkeeping.
func (m *Manager) fireScheduled(j schedule.Job, fire, now time.Time) {
	st := schedule.NewStore(schedule.DefaultDir())
	_, created, err := m.taskRuns.enqueueSchedule(j, schedule.TriggerScheduled, fire)
	if err != nil {
		if !errors.Is(err, schedule.ErrRunning) {
			fmt.Fprintln(os.Stderr, "pogo: task admission failed:", err)
		}
		return
	}
	if !created {
		return
	}
	// Catch-up is bounded to one run — the most recent missed fire — and
	// recomputes from now, so an every-job that is N intervals behind runs
	// once, not N times. Where the interval is explicit the gap is countable,
	// and a full missed interval leaves a skipped record behind so the run
	// history explains the silence instead of hiding it.
	if j.Kind == schedule.KindEvery && schedule.ValidEverySeconds(j.EverySec) {
		if missed := int(now.Sub(fire) / (time.Duration(j.EverySec) * time.Second)); missed >= 1 {
			_ = st.AppendRun(j.ID, schedule.RunRecord{
				TS: now, Trigger: schedule.TriggerScheduled, Status: schedule.StatusSkipped,
				Error: fmt.Sprintf("%d interval(s) missed while the server was not running", missed),
			})
		}
	}
	// A run-now in flight owns the job for now; its own RunOnce holds the
	// flock, so stepping aside here avoids a pointless skip record.
}

// startScheduledWork registers before spawning, under the same lock Close uses
// to forbid new work. Both timer and run-now workers share the manager lifetime.
func (m *Manager) startScheduledWork(id string) (context.Context, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || !m.SchedStart(id) {
		return nil, false
	}
	m.wg.Add(1)
	return m.scheduleCtx, true
}

func (m *Manager) finishScheduledWork(id string) {
	m.SchedDone(id)
	m.wg.Done()
}
