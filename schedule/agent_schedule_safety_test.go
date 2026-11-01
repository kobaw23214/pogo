package schedule

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/inputref"
)

func TestAgentScheduleSafetyDSTCronAndExplicitOffsets(t *testing.T) {
	j := Job{Enabled: true, Kind: KindCron, Cron: "30 2 * * *", Timezone: "America/New_York"}
	before := time.Date(2027, 3, 14, 6, 59, 0, 0, time.UTC)
	want := time.Date(2027, 3, 15, 6, 30, 0, 0, time.UTC)
	if got := NextRun(j, before); got == nil || !got.Equal(want) {
		t.Fatalf("DST nonexistent daily wall time must skip to next valid day: %v want %v", got, want)
	}
	j.Cron = "30 1 * * *"
	first := NextRun(j, time.Date(2026, 11, 1, 5, 29, 0, 0, time.UTC))
	if first == nil || !first.Equal(time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)) {
		t.Fatalf("first fallback occurrence %v", first)
	}
	second := NextRun(j, *first)
	if second == nil || !second.After(*first) || second.In(time.UTC).Equal(*first) {
		t.Fatalf("fall-back cron repeated the same instant or stopped: %v", second)
	}
	at, e := ParseAtZone("2026-11-01T01:30:00-05:00", "America/New_York")
	if e != nil || !at.Equal(time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)) {
		t.Fatalf("explicit offset lost its exact instant: %v %v", at, e)
	}
}

func TestAgentScheduleSafetySealDistinguishesNestedDataAndRuntimeState(t *testing.T) {
	owner := identity.LocalScope("alice")
	j := testJob("seal-safety")
	j.AgentID = "ag_safety"
	j.Owner = &owner
	j.Timezone = "Asia/Shanghai"
	j.Parts = []inputref.Part{{Type: "shortcut", ShortcutID: "nested", Title: "Snapshot", Parts: []inputref.Part{{Type: "text", Text: "first"}, {Type: "office", Office: &inputref.OfficeSnapshot{Path: "report.docx", Title: "section", Editor: "word", Text: "frozen section", SourceSession: "s1", Dirty: true}}}}}
	sealed := DefinitionHash(j)
	runtime := j
	runtime.Enabled = false
	runtime.SessionID = "new-runtime-session"
	runtime.LastError = "new runtime error"
	runtime.LastSummary = "new summary"
	runtime.LastRunAt = time.Now()
	runtime.Revision = 30
	if DefinitionHash(runtime) != sealed {
		t.Fatal("run bookkeeping invalidated immutable task definition")
	}
	changed := j
	changed.Parts = inputref.Clone(j.Parts)
	snapshot := *changed.Parts[0].Parts[1].Office
	snapshot.Text = "different selected content"
	changed.Parts[0].Parts[1].Office = &snapshot
	if DefinitionHash(changed) == sealed {
		t.Fatal("nested Office snapshot changed without invalidating definition seal")
	}
}

func TestAgentScheduleSafetyEditHonorsActualRunLease(t *testing.T) {
	if !runLockSupported {
		t.Skip("OS does not support advisory run locking")
	}
	st := NewStore(t.TempDir())
	j := testJob("edit-running")
	j.AgentID = "ag_safety"
	if e := st.Add(j); e != nil {
		t.Fatal(e)
	}
	current, e := st.Find(j.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Join(st.Dir(), "locks"), 0700); e != nil {
		t.Fatal(e)
	}
	lock, e := os.OpenFile(st.lockPath(j.ID), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	if e = lockFile(lock.Fd()); e != nil {
		t.Fatal(e)
	}
	defer unlockFile(lock.Fd())
	next := current
	next.Prompt = "must not replace running definition"
	if _, e = st.ReplaceDefinition(j.ID, current.Revision, next); !errors.Is(e, ErrRunning) {
		t.Fatalf("running definition edit was not refused: %v", e)
	}
	after, e := st.Find(j.ID)
	if e != nil || after.Prompt != current.Prompt || after.DefHash != current.DefHash {
		t.Fatal("rejected edit changed stored definition", e)
	}
}
