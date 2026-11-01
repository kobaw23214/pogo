package schedule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/inputref"
)

func TestAgentScheduleDefinitionSealsBindingAndOrderedInput(t *testing.T) {
	owner := identity.LocalScope("alice")
	j := testJob("agent")
	j.AgentID = "ag_1"
	j.Owner = &owner
	j.Timezone = "Asia/Shanghai"
	j.Parts = []inputref.Part{{Type: "text", Text: "one"}, {Type: "file", Path: "information-assets/a/report.txt"}, {Type: "text", Text: "two"}}
	original := DefinitionHash(j)
	for name, mutate := range map[string]func(*Job){
		"owner":    func(j *Job) { other := identity.LocalScope("bob"); j.Owner = &other },
		"agent":    func(j *Job) { j.AgentID = "ag_2" },
		"timezone": func(j *Job) { j.Timezone = "UTC" },
		"order":    func(j *Job) { j.Parts[0], j.Parts[2] = j.Parts[2], j.Parts[0] },
	} {
		t.Run(name, func(t *testing.T) {
			copy := j
			copy.Parts = inputref.Clone(j.Parts)
			mutate(&copy)
			if DefinitionHash(copy) == original {
				t.Fatal("definition change was not sealed")
			}
		})
	}
	out := ChildRunner(context.Background(), j, LogPaths{}, nil)
	if out.ExitCode == 0 || out.Err == "" {
		t.Fatal("Agent job reached ordinary CLI")
	}
	// Historical definitions keep exactly the pre-upgrade encoding/hash.
	old := testJob("old")
	legacy := struct {
		Prompt     string  `json:"prompt"`
		Workdir    string  `json:"workdir"`
		WorkflowID string  `json:"workflow_id,omitempty"`
		Kind       string  `json:"kind"`
		Cron       string  `json:"cron,omitempty"`
		EverySec   int64   `json:"every_sec,omitempty"`
		At         string  `json:"at,omitempty"`
		MaxTurns   int     `json:"max_turns,omitempty"`
		BudgetUSD  float64 `json:"budget_usd,omitempty"`
		Model      string  `json:"model,omitempty"`
		ExpiresAt  string  `json:"expires_at,omitempty"`
	}{old.Prompt, old.Workdir, old.WorkflowID, old.Kind, old.Cron, old.EverySec, old.At, old.MaxTurns, old.BudgetUSD, old.Model, old.ExpiresAt}
	raw, _ := json.Marshal(legacy)
	if got := hashDefinitionTest(raw); got != DefinitionHash(old) {
		t.Fatal("legacy seal changed")
	}
}

func TestAgentScheduleCASPreservesHistoryAndBinding(t *testing.T) {
	st := NewStore(t.TempDir())
	j := testJob("job")
	j.AgentID = "ag_a"
	owner := identity.LocalScope("alice")
	j.Owner = &owner
	j.LastSummary = "historical"
	if err := st.Add(j); err != nil {
		t.Fatal(err)
	}
	original, _ := st.Find(j.ID)
	edited := original
	edited.Title = "new"
	edited.LastSummary = "must not replace history"
	got, err := NewStore(st.Dir()).ReplaceDefinition(j.ID, original.Revision, edited)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 2 || got.LastSummary != "historical" || got.DefHash != DefinitionHash(got) {
		t.Fatalf("bad edited state %+v", got)
	}
	if _, err = st.ReplaceDefinition(j.ID, original.Revision, edited); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS accepted: %v", err)
	}
	edited = got
	edited.AgentID = "ag_other"
	if _, err = st.ReplaceDefinition(j.ID, got.Revision, edited); err == nil {
		t.Fatal("binding reassigned")
	}
}

func TestAgentScheduleIANACronAndOneShotZones(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	j := Job{Enabled: true, Kind: KindCron, Cron: "0 9 * * *", Timezone: "Asia/Shanghai"}
	if n := NextRun(j, now); n == nil || !n.Equal(now.Add(time.Hour)) {
		t.Fatalf("Shanghai9am: %v", n)
	}
	if _, err := Location("Local"); err == nil {
		t.Fatal("machine-dependent Local accepted")
	}
	if _, err := Location("No/SuchZone"); err == nil {
		t.Fatal("invalid zone accepted")
	}
	if _, err := ParseAtZone("2027-03-14T02:30", "America/New_York"); err == nil {
		t.Fatal("DST nonexistent wall time accepted")
	}
	if _, err := ParseAtZone("2026-11-01T01:30", "America/New_York"); err == nil {
		t.Fatal("ambiguous local time accepted without offset")
	}
	at, err := ParseAtZone("2026-10-18T09:00", "Asia/Shanghai")
	if err != nil || !at.Equal(now.Add(time.Hour)) {
		t.Fatalf("at zone: %v %v", at, err)
	}
}

func hashDefinitionTest(raw []byte) string { s := sha256.Sum256(raw); return hex.EncodeToString(s[:]) }

// Advisory-lock regression is exercised on the supported deployment platforms.
var runLockSupported = runtime.GOOS == "linux" || runtime.GOOS == "darwin"

func TestAgentScheduleFailedTriggerEditKeepsPriorDefinition(t *testing.T) {
	st := NewStore(t.TempDir())
	old := testJob("trigger-edit")
	if err := st.Add(old); err != nil {
		t.Fatal(err)
	}
	old, _ = st.Find(old.ID)
	next := old
	next.Prompt = "replacement"
	refusal := errors.New("scheduler refused registration")
	_, err := st.ReplaceDefinition(old.ID, old.Revision, next, DefinitionOptions{Install: func(previous, replacement Job) error {
		if previous.Prompt != old.Prompt || replacement.Prompt != "replacement" {
			t.Fatal("wrong trigger definitions")
		}
		return refusal
	}})
	if !errors.Is(err, refusal) {
		t.Fatal(err)
	}
	retained, err := st.Find(old.ID)
	if err != nil || retained.Prompt != old.Prompt || retained.Revision != old.Revision || retained.DefHash != old.DefHash {
		t.Fatalf("failed registration changed job: %+v %v", retained, err)
	}
}

func TestAgentScheduleTriggerEditReanchorsButContentEditKeepsCadence(t *testing.T) {
	st := NewStore(t.TempDir())
	created := time.Now().Add(-2 * time.Hour)
	originalAnchor := created
	j := testJob("cadence")
	j.Kind = KindEvery
	j.Cron = ""
	j.EverySec = 3600
	j.CreatedAt = created
	j.ScheduleFrom = &originalAnchor
	j.LastRunAt = created.Add(time.Hour)
	if err := st.Add(j); err != nil {
		t.Fatal(err)
	}
	j, _ = st.Find(j.ID)
	edited := j
	now := time.Now()
	edited.ScheduleFrom = &now
	edited.Prompt = "new content"
	unchanged, err := st.ReplaceDefinition(j.ID, j.Revision, edited)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.ScheduleFrom == nil || !unchanged.ScheduleFrom.Equal(originalAnchor) {
		t.Fatal("content edit reset the existing cadence")
	}
	edited = unchanged
	edited.EverySec = 600
	edited.ScheduleFrom = &now
	changed, err := st.ReplaceDefinition(j.ID, unchanged.Revision, edited)
	if err != nil {
		t.Fatal(err)
	}
	if n := NextRun(changed, now); n == nil || !n.Equal(now.Add(600*time.Second)) {
		t.Fatalf("new interval reused historical deadline: %v", n)
	}
}

func TestAgentScheduleDeletedIDsCannotBorrowRetainedHistory(t *testing.T) {
	for _, kind := range []string{"run", "out", "err"} {
		t.Run(kind, func(t *testing.T) {
			st := NewStore(t.TempDir())
			original := testJob("historical")
			if err := st.Add(original); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "run":
				err = st.AppendRun(original.ID, RunRecord{Status: StatusOK, Summary: "private prior result"})
			case "out", "err":
				p := st.OutLogPath(original.ID)
				if kind == "err" {
					p = st.ErrLogPath(original.ID)
				}
				err = os.MkdirAll(filepath.Dir(p), 0700)
				if err == nil {
					err = os.WriteFile(p, []byte("private prior log"), 0600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = st.Remove(original.ID); err != nil {
				t.Fatal(err)
			}
			replacement := testJob(original.ID)
			replacement.AgentID = "ag_other"
			owner := identity.LocalScope("other")
			replacement.Owner = &owner
			if err = st.Add(replacement); err == nil {
				t.Fatal("replacement borrowed prior private history")
			}
			if jobs, _ := st.Load(); len(jobs) != 0 {
				t.Fatal("rejected replacement saved a job")
			}
		})
	}
}

func TestAgentScheduleDeleteReservesIDBeforeLateHistoricalAppend(t *testing.T) {
	st := NewStore(t.TempDir())
	original := testJob("late-history")
	if err := st.Add(original); err != nil {
		t.Fatal(err)
	}
	if err := st.Remove(original.ID); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(st.runsPath(original.ID)); err != nil || len(raw) != 0 {
		t.Fatalf("no empty namespace reservation: %q %v", raw, err)
	}
	replacement := testJob(original.ID)
	replacement.AgentID = "ag_bob"
	owner := identity.LocalScope("bob")
	replacement.Owner = &owner
	if err := st.Add(replacement); err == nil {
		t.Fatal("deleted zero-run job ID reassigned before its late append")
	}
	if err := st.AppendRun(original.ID, RunRecord{Status: StatusOK, Summary: "late prior job result"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Add(replacement); err == nil {
		t.Fatal("late prior history namespace reassigned")
	}
	if jobs, _ := st.Load(); len(jobs) != 0 {
		t.Fatal("replacement definition was saved")
	}
}
func TestAgentScheduleUnregisteredRollbackDoesNotConsumeOrEraseHistory(t *testing.T) {
	st := NewStore(t.TempDir())
	j := testJob("unregistered")
	if err := st.Add(j); err != nil {
		t.Fatal(err)
	}
	if err := st.removeUnregistered(j); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.runsPath(j.ID)); !os.IsNotExist(err) {
		t.Fatal("failed unpublished registration consumed ID")
	}
	if err := st.Add(j); err != nil {
		t.Fatal("clean retry blocked", err)
	}
	if err := st.AppendRun(j.ID, RunRecord{Status: StatusOK, Summary: "real record"}); err != nil {
		t.Fatal(err)
	}
	if err := st.removeUnregistered(j); err == nil {
		t.Fatal("rollback removed an already published job")
	}
	if runs, _ := st.Runs(j.ID, 0); len(runs) != 1 || runs[0].Summary != "real record" {
		t.Fatal("rollback erased actual history")
	}
}

type agentScheduleRegistrationTestBackend struct{ register func(Job, string) error }

func (agentScheduleRegistrationTestBackend) Name() string { return "test" }
func (b agentScheduleRegistrationTestBackend) Register(j Job, p string) error {
	return b.register(j, p)
}
func (agentScheduleRegistrationTestBackend) Unregister(Job) error { return nil }
func TestAgentScheduleFailedRegistrationCannotRollbackAlreadyRunningJob(t *testing.T) {
	if !runLockSupported {
		t.Skip("advisory run locking")
	}
	st := NewStore(t.TempDir())
	j := testJob("registration-run")
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	backend := agentScheduleRegistrationTestBackend{register: func(job Job, _ string) error {
		go func() {
			defer close(done)
			_, _ = st.RunOnce(context.Background(), job.ID, TriggerScheduled, nil, func(context.Context, Job, LogPaths, io.Writer) RunOutcome {
				close(started)
				<-release
				return RunOutcome{Summary: "original execution", EndReason: "completed"}
			})
		}()
		<-started
		return errors.New("registration uncertain after trigger started")
	}}
	err := AddJob(st, backend, j)
	close(release)
	<-done
	if err == nil {
		t.Fatal("failed registration reported success")
	}
	current, findErr := st.Find(j.ID)
	if findErr != nil || current.LastSummary != "original execution" {
		t.Fatalf("running job removed by rollback: %+v %v", current, findErr)
	}
}
