package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/schedule"
)

// fakeBackend records registrations instead of touching launchctl/systemctl.
type fakeBackend struct {
	registered   []schedule.Job
	unregistered []schedule.Job
	err          error
}

func (f *fakeBackend) Name() string { return "fake" }
func (f *fakeBackend) Register(j schedule.Job, execPath string) error {
	if f.err != nil {
		return f.err
	}
	f.registered = append(f.registered, j)
	return nil
}
func (f *fakeBackend) Unregister(j schedule.Job) error {
	f.unregistered = append(f.unregistered, j)
	return nil
}

// scheduleCLI isolates the command from the user's real state dir and swaps
// the OS backend for a recording fake.
func scheduleCLI(t *testing.T) (*fakeBackend, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(schedule.DirEnv, dir)
	t.Setenv("POGO_CONFIG", dir+"/no-providers.json")
	fake := &fakeBackend{}
	restore := detectBackend
	detectBackend = func(dir string) (schedule.Backend, error) { return fake, nil }
	t.Cleanup(func() { detectBackend = restore })
	return fake, dir
}

func TestScheduleAddValidation(t *testing.T) {
	scheduleCLI(t)
	var out bytes.Buffer

	// --prompt is required.
	if err := scheduleCommand(&out, []string{"add", "--cron", "0 9 * * *"}); err == nil {
		t.Error("add without --prompt succeeded")
	}
	// Exactly one trigger.
	if err := scheduleCommand(&out, []string{"add", "--prompt", "x"}); err == nil {
		t.Error("add without a trigger succeeded")
	}
	if err := scheduleCommand(&out, []string{"add", "--prompt", "x", "--cron", "0 9 * * *", "--every", "30m"}); err == nil {
		t.Error("add with two triggers succeeded")
	}
	// A bad cron expression is refused at add time, not when the OS chokes.
	if err := scheduleCommand(&out, []string{"add", "--prompt", "x", "--cron", "61 * * * *"}); err == nil {
		t.Error("add with an invalid cron succeeded")
	}
	// A cron that can never fire is refused too.
	if err := scheduleCommand(&out, []string{"add", "--prompt", "x", "--cron", "0 0 30 2 *"}); err == nil {
		t.Error("add with an impossible cron succeeded")
	}
	// An --at in the past is refused.
	if err := scheduleCommand(&out, []string{"add", "--prompt", "x", "--at", "2020-01-01T00:00"}); err == nil {
		t.Error("add with a past --at succeeded")
	}
	// Unknown subcommand.
	if err := scheduleCommand(&out, []string{"frobnicate"}); err == nil {
		t.Error("unknown subcommand succeeded")
	}
}

func TestScheduleAddListRemove(t *testing.T) {
	fake, dir := scheduleCLI(t)
	var out bytes.Buffer

	if err := scheduleCommand(&out, []string{"add", "--cron", "@daily", "--prompt", "morning report",
		"--dir", dir, "--name", "morning"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "added morning") || !strings.Contains(text, "backend: fake") ||
		!strings.Contains(text, "next run:") {
		t.Errorf("add output = %q", text)
	}
	if len(fake.registered) != 1 {
		t.Fatalf("backend saw %d registrations, want 1", len(fake.registered))
	}
	j := fake.registered[0]
	if j.ID != "morning" || j.Kind != schedule.KindCron || j.Cron != "@daily" || !j.Enabled {
		t.Errorf("registered job = %+v", j)
	}

	// The job is listed with its next firing.
	out.Reset()
	if err := scheduleCommand(&out, []string{"list"}); err != nil {
		t.Fatal(err)
	}
	text = out.String()
	if !strings.Contains(text, "morning") || !strings.Contains(text, "@daily") {
		t.Errorf("list output = %q", text)
	}

	// A duplicate id is refused.
	if err := scheduleCommand(&out, []string{"add", "--cron", "@daily", "--prompt", "again",
		"--dir", dir, "--name", "morning"}); err == nil {
		t.Error("duplicate --name accepted")
	}

	// disable/enable flip the flag.
	if err := scheduleCommand(&out, []string{"disable", "morning"}); err != nil {
		t.Fatal(err)
	}
	stored, err := schedule.NewStore(dir).Find("morning")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Enabled {
		t.Error("disable did not land")
	}
	if err := scheduleCommand(&out, []string{"enable", "morning"}); err != nil {
		t.Fatal(err)
	}
	if stored, _ = schedule.NewStore(dir).Find("morning"); !stored.Enabled {
		t.Error("enable did not land")
	}

	// remove unregisters and deletes.
	if err := scheduleCommand(&out, []string{"remove", "morning"}); err != nil {
		t.Fatal(err)
	}
	if len(fake.unregistered) != 1 || fake.unregistered[0].ID != "morning" {
		t.Errorf("unregistered = %+v", fake.unregistered)
	}
	if _, err := schedule.NewStore(dir).Find("morning"); err == nil {
		t.Error("removed job still in the store")
	}
}

func TestScheduleAddEveryAndAt(t *testing.T) {
	fake, dir := scheduleCLI(t)
	var out bytes.Buffer

	if err := scheduleCommand(&out, []string{"add", "--every", "30m", "--prompt", "poll the queue", "--dir", dir}); err != nil {
		t.Fatal(err)
	}
	if got := fake.registered[0]; got.Kind != schedule.KindEvery || got.EverySec != 1800 {
		t.Errorf("every job = %+v", got)
	}

	at := time.Now().Add(2 * time.Hour)
	if err := scheduleCommand(&out, []string{"add", "--at", at.Format("2006-01-02T15:04"), "--prompt", "one shot", "--dir", dir}); err != nil {
		t.Fatal(err)
	}
	got := fake.registered[1]
	if got.Kind != schedule.KindAt {
		t.Fatalf("at job = %+v", got)
	}
	parsed, err := time.Parse(time.RFC3339, got.At)
	if err != nil || parsed.Before(time.Now()) {
		t.Errorf("stored at = %q, %v", got.At, err)
	}
}

// A failed OS registration rolls the job back out of jobs.json: a job with no
// trigger is a schedule that silently never runs.
func TestScheduleAddRollsBackOnRegisterFailure(t *testing.T) {
	fake, dir := scheduleCLI(t)
	fake.err = fmt.Errorf("launchctl said no")
	var out bytes.Buffer

	err := scheduleCommand(&out, []string{"add", "--cron", "@daily", "--prompt", "x", "--dir", dir, "--name", "doomed"})
	if err == nil {
		t.Fatal("add succeeded with a failing backend")
	}
	jobs, lerr := schedule.NewStore(dir).Load()
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(jobs) != 0 {
		t.Errorf("job survived a failed registration: %+v", jobs)
	}
}

func TestScheduleLogsEmpty(t *testing.T) {
	_, dir := scheduleCLI(t)
	var out bytes.Buffer
	if err := scheduleCommand(&out, []string{"add", "--cron", "@daily", "--prompt", "x", "--dir", dir, "--name", "j1"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := scheduleCommand(&out, []string{"logs", "j1"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no runs yet") {
		t.Errorf("logs output = %q", out.String())
	}
	if err := scheduleCommand(&out, []string{"logs", "no-such-job"}); err == nil {
		t.Error("logs of a missing job succeeded")
	}
}
