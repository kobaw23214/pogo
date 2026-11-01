package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yosukeno/pogo/schedule"
)

// detectBackend is a variable so tests can register jobs without touching
// launchctl/systemctl/crontab.
var detectBackend = schedule.Detect

// scheduleCommand implements -schedule, the management half of the scheduled
// tasks feature. (-schedule-exec, the half the OS calls, dispatches straight
// from main.)
//
// Subcommand parsing uses flag.Args: the top-level flag set stops at the
// first non-flag word, which is exactly where the subcommand starts, so
// `pogo -schedule add --cron ...` reaches here with its flags unparsed and
// `add` gets its own FlagSet.
func scheduleCommand(out io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: pogo -schedule <list|add|remove|enable|disable|run-now|logs> [args]")
	}
	dir := schedule.DefaultDir()
	switch args[0] {
	case "list":
		return scheduleList(out, dir)
	case "add":
		return scheduleAdd(out, dir, args[1:])
	case "remove":
		return scheduleRemove(out, dir, args[1:])
	case "enable", "disable":
		return scheduleEnable(out, dir, args[0] == "enable", args[1:])
	case "run-now":
		return scheduleRunNow(out, dir, args[1:])
	case "logs":
		return scheduleLogs(out, dir, args[1:])
	default:
		return fmt.Errorf("unknown -schedule subcommand %q (want list, add, remove, enable, disable, run-now or logs)", args[0])
	}
}

func scheduleList(out io.Writer, dir string) error {
	jobs, err := schedule.NewStore(dir).Load()
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		fmt.Fprintf(out, "no scheduled jobs (state dir %s)\n", dir)
		return nil
	}
	now := time.Now()
	fmt.Fprintf(out, "%-12s %-24s %-8s %-17s %-8s %-17s\n", "ID", "SCHEDULE", "ENABLED", "LAST RUN", "STATUS", "NEXT RUN")
	for _, j := range jobs {
		last := "-"
		if !j.LastRunAt.IsZero() {
			last = j.LastRunAt.Format("2006-01-02 15:04")
		}
		status := j.LastRunStatus
		if status == "" {
			status = "-"
		}
		fmt.Fprintf(out, "%-12s %-24s %-8v %-17s %-8s %-17s\n",
			j.ID, describeSchedule(j), j.Enabled, last, status, nextRun(j, now))
	}
	fmt.Fprintln(out, dir)
	return nil
}

// describeSchedule is the SCHEDULE column: the expression as written, or the
// interval, or the one-shot time.
func describeSchedule(j schedule.Job) string {
	switch j.Kind {
	case schedule.KindEvery:
		return "every " + (time.Duration(j.EverySec) * time.Second).String()
	case schedule.KindAt:
		return "at " + j.At
	default:
		return j.Cron
	}
}

func nextRun(j schedule.Job, now time.Time) string {
	if next := schedule.NextRun(j, now); next != nil {
		return next.Format("2006-01-02 15:04")
	}
	return "-"
}

func scheduleAdd(out io.Writer, dir string, args []string) error {
	fs := flag.NewFlagSet("schedule add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		cronExpr = fs.String("cron", "", "cron expression or macro (@daily, @hourly, ...)")
		every    = fs.Duration("every", 0, "fixed interval, e.g. 30m")
		at       = fs.String("at", "", "one-shot time: RFC3339 or 2006-01-02T15:04 (local)")
		prompt   = fs.String("prompt", "", "the prompt the job runs (required)")
		workdir  = fs.String("dir", "", "working directory for the run (default: current)")
		name     = fs.String("name", "", "job id (default: derived from the job)")
		maxTurns = fs.Int("max-turns", 0, "turn cap for the run (0 = default)")
		budget   = fs.Float64("budget", 0, "cost budget for the run (0 = none)")
		model    = fs.String("model", "", "model for the run (default: configured default)")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	kinds := 0
	for _, set := range []bool{*cronExpr != "", *every != 0, *at != ""} {
		if set {
			kinds++
		}
	}
	if kinds != 1 {
		return fmt.Errorf("exactly one of --cron, --every or --at is required")
	}
	if strings.TrimSpace(*prompt) == "" {
		return fmt.Errorf("--prompt is required")
	}

	job := schedule.Job{
		Prompt:    *prompt,
		MaxTurns:  *maxTurns,
		BudgetUSD: *budget,
		Model:     *model,
		Enabled:   true,
		CreatedAt: time.Now(),
	}
	if *workdir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		*workdir = cwd
	}
	abs, err := filepath.Abs(*workdir)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("--dir %q is not a directory", *workdir)
	}
	job.Workdir = abs

	now := time.Now()
	var next time.Time
	switch {
	case *cronExpr != "":
		s, err := schedule.Parse(*cronExpr)
		if err != nil {
			return fmt.Errorf("--cron: %w", err)
		}
		next = s.Next(now)
		if next.IsZero() {
			return fmt.Errorf("--cron %q never fires (nothing in the next five years)", *cronExpr)
		}
		job.Kind, job.Cron = schedule.KindCron, *cronExpr
		// Warn about tight loops, don't refuse: two consecutive firings under
		// five minutes apart is the smell of `*/2 * * * *` spending money
		// unattended, but a legitimate use is not ours to veto.
		if again := s.Next(next); !again.IsZero() && again.Sub(next) < 5*time.Minute {
			fmt.Fprintf(out, "warning: this schedule fires less than 5 minutes apart (%s → %s)\n",
				next.Format("15:04"), again.Format("15:04"))
		}
	case *every != 0:
		if *every < time.Second {
			return fmt.Errorf("--every must be at least 1s")
		}
		job.Kind, job.EverySec = schedule.KindEvery, int64(every.Seconds())
		next = now.Add(*every)
		if *every < 5*time.Minute {
			fmt.Fprintln(out, "warning: an interval under 5 minutes is a tight loop for an unattended agent")
		}
	case *at != "":
		atTime, err := schedule.ParseAt(*at)
		if err != nil {
			return fmt.Errorf("--at: %w", err)
		}
		if !atTime.After(now) {
			return fmt.Errorf("--at %s is in the past", atTime.Format("2006-01-02 15:04"))
		}
		job.Kind, job.At = schedule.KindAt, atTime.Format(time.RFC3339)
		next = atTime
	}

	if *name != "" {
		if !schedule.JobNameShape.MatchString(*name) {
			return fmt.Errorf("--name %q: use 1-40 of [A-Za-z0-9._-], starting with a letter or digit", *name)
		}
		job.ID = *name
	} else {
		job.ID = schedule.NewID(job.Prompt, describeSchedule(job), now.String())
	}

	backend, err := detectBackend(dir)
	if err != nil {
		return err
	}
	if err := schedule.AddJob(schedule.NewStore(dir), backend, job); err != nil {
		return err
	}

	fmt.Fprintf(out, "added %s  (backend: %s)\n  schedule: %s\n  next run: %s\n  workdir:  %s\n",
		job.ID, backend.Name(), describeSchedule(job), next.Format("2006-01-02 15:04"), job.Workdir)
	if keys := len(collectKeyEnvNames()); keys > 0 {
		fmt.Fprintf(out, "note: %d API key(s) referenced by key_env in providers.json (%s) were copied into the OS unit file (mode 0600), because the scheduler runs without your shell environment\n",
			keys, strings.Join(collectKeyEnvNames(), ", "))
	}
	return nil
}

func scheduleRemove(out io.Writer, dir string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: pogo -schedule remove <id>")
	}
	backend, berr := detectBackend(dir)
	if berr != nil {
		backend = nil
	}
	warn, err := schedule.RemoveJob(schedule.NewStore(dir), backend, args[0])
	if err != nil {
		return err
	}
	if berr != nil {
		fmt.Fprintf(out, "warning: cannot reach the OS scheduler (%v); the trigger may be orphaned\n", berr)
	} else if warn != "" {
		// Removed from jobs.json anyway: a stale trigger fires a process that
		// finds no job and exits, which is harmless; a job we refuse to
		// delete is not.
		fmt.Fprintf(out, "warning: %s\n", warn)
	}
	fmt.Fprintf(out, "removed %s (run history and logs kept under %s)\n", args[0], dir)
	return nil
}

func scheduleEnable(out io.Writer, dir string, enable bool, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: pogo -schedule %s <id>", map[bool]string{true: "enable", false: "disable"}[enable])
	}
	if err := schedule.SetEnabled(schedule.NewStore(dir), args[0], enable); err != nil {
		return err
	}
	word := "disabled"
	if enable {
		word = "enabled"
	}
	fmt.Fprintf(out, "%s %s\n", word, args[0])
	// The OS trigger stays registered either way: enable/disable flips the
	// job's own flag, and a disabled job's trigger exits immediately. Doing
	// it this way keeps `enable` from needing to re-collect credentials, and
	// keeps a running job from ever bootout-ing itself (see RunOnce).
	if enable {
		fmt.Fprintln(out, "the OS trigger stays installed; the job will run next time it fires")
	} else {
		fmt.Fprintln(out, "the OS trigger stays installed; if it fires while disabled, the job exits immediately")
	}
	return nil
}

func scheduleRunNow(out io.Writer, dir string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: pogo -schedule run-now <id>")
	}
	res, err := schedule.NewStore(dir).RunOnce(context.Background(), args[0], schedule.TriggerManual,
		out, schedule.ChildRunner)
	if err != nil {
		return err
	}
	status := res.Status
	if res.SessionID != "" {
		fmt.Fprintf(out, "session %s  status %s  %.1fs\n", res.SessionID, status, res.Duration.Seconds())
	} else {
		fmt.Fprintf(out, "status %s  %.1fs\n", status, res.Duration.Seconds())
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("job %s exited with code %d", args[0], res.ExitCode)
	}
	return nil
}

func scheduleLogs(out io.Writer, dir string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: pogo -schedule logs <id>")
	}
	store := schedule.NewStore(dir)
	if _, err := store.Find(args[0]); err != nil {
		return err
	}
	runs, err := store.Runs(args[0], 5)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Fprintln(out, "no runs yet")
	} else {
		fmt.Fprintf(out, "%-17s %-10s %-8s %-9s %s\n", "TIME", "TRIGGER", "STATUS", "SECONDS", "DETAIL")
		for _, r := range runs {
			detail := r.EndReason
			if r.Error != "" {
				detail = strings.TrimSpace(strings.Join([]string{detail, r.Error}, " "))
			}
			fmt.Fprintf(out, "%-17s %-10s %-8s %-9.1f %s\n",
				r.TS.Format("2006-01-02 15:04"), r.Trigger, r.Status, r.DurationSec, detail)
		}
	}
	lines, err := schedule.TailLines(store.OutLogPath(args[0]), 50)
	if err == nil && len(lines) > 0 {
		fmt.Fprintf(out, "\n--- tail of %s ---\n", store.OutLogPath(args[0]))
		for _, l := range lines {
			fmt.Fprintln(out, l)
		}
	}
	return nil
}

// collectKeyEnvNames lists the key_env names providers.json refers to, for
// the add-time disclosure. Values stay in the schedule package; this only
// names them.
func collectKeyEnvNames() []string {
	return schedule.KeyEnvNames()
}
