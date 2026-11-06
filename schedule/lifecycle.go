package schedule

// Job lifecycle operations shared by the CLI (-schedule) and the web API
// (/api/schedule/*): the CLI used to own them in package main, which no other
// front end could import. They live here so both surfaces agree on the rules
// that matter — a failed OS registration rolls the job back out, a remove
// keeps the run history, enable/disable only flips the flag.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// JobNameShape is what a user-chosen job name may be: it lands in a launchd
// label, a systemd unit name and file names, so shell-and-plist-safe only.
var JobNameShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)

// AddJob saves job and installs its OS trigger, rolling the save back when
// registration fails: a job in jobs.json with no OS trigger is a schedule
// that silently never runs.
func AddJob(st *Store, be Backend, job Job) error {
	if job.AgentID != "" || job.Timezone != "" {
		be = Internal()
	}
	if err := st.Add(job); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.Abs(exe)
	}
	if err != nil {
		if rollback := st.removeUnregistered(job); rollback != nil {
			return errors.Join(err, rollback)
		}
		return err
	}
	if err := be.Register(job, exe); err != nil {
		if rollback := st.removeUnregistered(job); rollback != nil {
			return fmt.Errorf("OS registration failed; current job retained: %w", errors.Join(err, rollback))
		}
		return fmt.Errorf("OS registration failed (job not saved): %w", err)
	}
	return nil
}

// RemoveJob deletes id from the store, unregistering its OS trigger first
// when a backend is available. be may be nil (the OS scheduler unreachable):
// the job is removed anyway and warn says the trigger may be orphaned — a
// stale trigger fires a process that finds no job and exits, which is
// harmless, while a job we refuse to delete is not. Run history and logs are
// kept (see Store.Remove).
func RemoveJob(st *Store, be Backend, id string) (warn string, err error) {
	job, err := st.Find(id)
	if err != nil {
		return "", err
	}
	if be == nil {
		warn = "cannot reach the OS scheduler; the trigger may be orphaned"
	} else if uerr := be.Unregister(job); uerr != nil {
		warn = fmt.Sprintf("unregistering %s: %v", job.ID, uerr)
	}
	if err := st.Remove(id); err != nil {
		return "", err
	}
	return warn, nil
}

// SetEnabled flips the job's own flag. The OS trigger stays registered either
// way: a disabled job's trigger exits immediately, enable never needs to
// re-collect credentials, and a running job can never bootout itself.
func SetEnabled(st *Store, id string, enabled bool) error {
	return st.Update(id, func(j *Job) { j.Enabled = enabled })
}

// NextRun is when the job fires next, or nil when that cannot be said: the
// job is disabled, the expression does not parse, an every-job has never run
// (the OS counts from registration, which the store does not record), or an
// at-job's time has passed.
const MaxEverySeconds = int64((1<<63 - 1) / time.Second)

func ValidEverySeconds(seconds int64) bool { return seconds >= 1 && seconds <= MaxEverySeconds }

func NextRun(j Job, now time.Time) *time.Time {
	if !j.Enabled || j.WorkflowID != "" {
		return nil
	}
	switch j.Kind {
	case KindCron:
		s, err := Parse(j.Cron)
		if err != nil {
			return nil
		}
		loc, err := Location(j.Timezone)
		if err != nil {
			return nil
		}
		if next := s.Next(now.In(loc)); !next.IsZero() {
			return &next
		}
	case KindEvery:
		if !ValidEverySeconds(j.EverySec) {
			return nil
		}
		anchor := j.LastRunAt
		if j.LastQueuedAt.After(anchor) {
			anchor = j.LastQueuedAt
		}
		if j.ScheduleFrom != nil && j.ScheduleFrom.After(anchor) {
			anchor = *j.ScheduleFrom
		}
		if !anchor.IsZero() {
			next := anchor.Add(time.Duration(j.EverySec) * time.Second)
			return &next
		}
	case KindAt:
		if at, err := time.Parse(time.RFC3339, j.At); err == nil && at.After(now) {
			return &at
		}
	}
	return nil
}

// ParseAt accepts RFC3339 and the two local-time shorthand forms.
func ParseAt(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q: want RFC3339 or 2006-01-02T15:04 (local time)", s)
}

// TailLines returns the last n lines of path, reading at most the trailing
// 256 KB so a huge log is not slurped whole.
func TailLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const window = 256 << 10
	start := int64(0)
	if st.Size() > window {
		start = st.Size() - window
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if start > 0 {
		// The first partial line of the window is dropped.
		if i := strings.IndexByte(string(data), '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		// An empty (or all-newline) file has no last lines, and returning
		// [""] would read as one.
		return nil, nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// Location keeps legacy jobs on the host timezone. New web jobs persist an
// explicit IANA zone so cron wall time is independent of the Docker host.
func Location(zone string) (*time.Location, error) {
	if zone == "" {
		return time.Local, nil
	}
	if zone == "Local" {
		return nil, fmt.Errorf("timezone must name an IANA zone, not Local")
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, fmt.Errorf("invalid IANA timezone %q", zone)
	}
	return loc, nil
}
func ParseAtZone(s, zone string) (time.Time, error) {
	loc, err := Location(zone)
	if err != nil {
		return time.Time{}, err
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			if t.Format(layout) != s {
				return time.Time{}, fmt.Errorf("local time does not exist in timezone %s", zone)
			}
			wall, _ := time.Parse(layout, s)
			matches := map[int64]bool{}
			for _, hours := range []int{-48, -24, -12, -6, -3, -1, 0, 1, 3, 6, 12, 24, 48} {
				_, offset := t.Add(time.Duration(hours) * time.Hour).Zone()
				candidate := wall.Add(-time.Duration(offset) * time.Second)
				if candidate.In(loc).Format(layout) == s {
					matches[candidate.Unix()] = true
				}
			}
			if len(matches) > 1 {
				return time.Time{}, fmt.Errorf("local time is ambiguous in timezone %s; specify an explicit RFC3339 offset", zone)
			}
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("want RFC3339 or 2006-01-02T15:04")
}
