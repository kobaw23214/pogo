package schedule

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testJob(id string) Job {
	return Job{
		ID:      id,
		Prompt:  "summarize the night shift",
		Workdir: "/tmp/work",
		Kind:    KindCron,
		Cron:    "0 9 * * *",
		Enabled: true,
		// A concrete time: a zero time.Time round-trips through JSON with a
		// non-nil location pointer, which would break the == below.
		CreatedAt: time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC),
	}
}

func TestJobsRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())
	j1 := testJob("aaaaaaaa")
	j1.MaxTurns, j1.BudgetUSD, j1.Model = 25, 1.5, "glm-5.2"
	j2 := testJob("bbbbbbbb")
	j2.Kind, j2.EverySec = KindEvery, 1800
	if err := s.Add(j1); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(j2); err != nil {
		t.Fatal(err)
	}

	jobs, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("Load returned %d jobs, want 2", len(jobs))
	}
	// Add seals each job with its definition hash; everything else must come
	// back exactly as written.
	j1.DefHash = DefinitionHash(j1)
	j2.DefHash = DefinitionHash(j2)
	if !reflect.DeepEqual(jobs[0], j1) || !reflect.DeepEqual(jobs[1], j2) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v and %+v", jobs, j1, j2)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	s := NewStore(t.TempDir())
	jobs, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Errorf("Load of a missing jobs.json = %v, want empty", jobs)
	}
}

// A corrupt jobs.json is an error, never an empty list: silently treating
// "I have jobs" as "I have none" is how a schedule quietly stops running.
func TestLoadCorruptFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "jobs.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	if _, err := s.Load(); err == nil {
		t.Fatal("Load of a corrupt jobs.json succeeded, want an error")
	}
}

func TestJobsFileIsPrivate(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Add(testJob("aaaaaaaa")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(s.Dir(), "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("jobs.json perm = %o, want 600 (it holds prompts)", perm)
	}
}

func TestAddRefusesDuplicateID(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Add(testJob("aaaaaaaa")); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(testJob("aaaaaaaa")); err == nil {
		t.Fatal("duplicate id accepted")
	}
}

func TestUpdateAndRemove(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Add(testJob("aaaaaaaa")); err != nil {
		t.Fatal(err)
	}
	if err := s.Update("aaaaaaaa", func(j *Job) { j.Enabled = false; j.ConsecutiveFailures = 3 }); err != nil {
		t.Fatal(err)
	}
	j, err := s.Find("aaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if j.Enabled || j.ConsecutiveFailures != 3 {
		t.Errorf("Update did not land: %+v", j)
	}
	if err := s.Remove("aaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Find("aaaaaaaa"); err == nil {
		t.Fatal("removed job still found")
	}
	if err := s.Remove("aaaaaaaa"); err == nil {
		t.Fatal("removing a missing job succeeded")
	}
}

// The atomic write must not leave temp files behind.
func TestSaveLeavesNoTempFiles(t *testing.T) {
	s := NewStore(t.TempDir())
	for i := 0; i < 3; i++ {
		if err := s.Add(testJob(NewID(string(rune('a' + i))))); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestDirEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(DirEnv, dir)
	if got := DefaultDir(); got != dir {
		t.Errorf("DefaultDir() = %q, want %q from %s", got, dir, DirEnv)
	}
}

func TestRunHistoryAppendAndRead(t *testing.T) {
	s := NewStore(t.TempDir())
	base := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	for i, status := range []string{StatusOK, StatusError, StatusSkipped} {
		r := RunRecord{
			TS: base.Add(time.Duration(i) * time.Hour), Trigger: TriggerScheduled,
			Status: status, DurationSec: 12.5, SessionID: "sess-1", EndReason: "completed",
		}
		if err := s.AppendRun("aaaaaaaa", r); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.Runs("aaaaaaaa", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("Runs returned %d records, want 3", len(runs))
	}
	if runs[1].Status != StatusError {
		t.Errorf("runs[1].Status = %q, want error", runs[1].Status)
	}
	// A limit returns the *newest* records, oldest first.
	runs, err = s.Runs("aaaaaaaa", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].Status != StatusError || runs[1].Status != StatusSkipped {
		t.Errorf("Runs(2) = %+v, want the newest two", runs)
	}

	// Missing history is empty, not an error.
	runs, err = s.Runs("no-such-job", 0)
	if err != nil || len(runs) != 0 {
		t.Errorf("Runs of a missing history = %v, %v; want empty, nil", runs, err)
	}
}

func TestRunHistoryTruncates(t *testing.T) {
	s := NewStore(t.TempDir())
	r := RunRecord{TS: time.Now(), Trigger: TriggerScheduled, Status: StatusOK, DurationSec: 1}
	// ~1.1 KB per record: 2100 of them cross 2 MB while also exceeding the
	// 2000-line keep, which is what the truncation enforces.
	r.Error = strings.Repeat("x", 1024)
	for i := 0; i < 2100; i++ {
		if err := s.AppendRun("aaaaaaaa", r); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.Runs("aaaaaaaa", 0)
	if err != nil {
		t.Fatal(err)
	}
	// 2100 records went in; the cap keeping exactly the newest 2000 is the
	// truncation having happened.
	if len(runs) != keepRunLines {
		t.Errorf("kept %d records, want %d", len(runs), keepRunLines)
	}
}

func TestNewIDIsDeterministicAndDistinct(t *testing.T) {
	a := NewID("prompt", "0 9 * * *", "2026-08-22")
	if len(a) != 8 {
		t.Errorf("NewID = %q, want 8 hex chars", a)
	}
	if b := NewID("prompt", "0 9 * * *", "2026-08-22"); a != b {
		t.Errorf("same seed gave %q and %q", a, b)
	}
	if c := NewID("prompt", "0 9 * * *", "2026-08-23"); a == c {
		t.Errorf("different seeds collided on %q", a)
	}
}
