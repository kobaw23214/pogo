package taskqueue

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	af "github.com/yosukeno/pogo/agentworkflow"
)

func awVersionPatchName(t *testing.T, s *Store, d af.Definition, name, request string) af.Definition {
	t.Helper()
	value, _, err := s.AWEdit(d.OwnerID, d.AgentID, af.EditRequest{Action: "patch", WorkflowID: d.ID, ExpectedRevision: d.Revision, RequestID: request, Operations: []af.Operation{{Op: "set_metadata", Name: &name}}})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func awVersionRaw(t *testing.T, s *Store, table, column, where, id string) []byte {
	t.Helper()
	var raw []byte
	if err := s.db.QueryRow(`SELECT `+column+` FROM `+table+` WHERE `+where+`=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAgentWorkflowVersionsProjectSavedHistoryAndRestoreWithoutRewritingRuns(t *testing.T) {
	s, _ := testStore(t)
	d1 := awFixture(t, s, "alice", "research")
	d2 := awVersionPatchName(t, s, d1, "第二次保存", "save-2")
	d2, err := s.AWEnable("alice", "research", d2.ID, d2.Revision, d2.DefinitionHash, true)
	if err != nil {
		t.Fatal(err)
	}
	d3 := awVersionPatchName(t, s, d2, d1.Name, "save-3")
	if d3.DefinitionHash == d1.DefinitionHash {
		t.Fatal("revision must remain part of the existing hash format")
	}
	run, _, _, err := s.AWAdmit(awRunFixture(d3, "retained-run"), "run-before-restore")
	if err != nil {
		t.Fatal(err)
	}
	versionBefore := awVersionRaw(t, s, "agent_workflow_versions", "definition", "version_id", d2.DefinitionHash)
	runBefore := awVersionRaw(t, s, "agent_workflow_runs", "record", "id", run.ID)
	// Older host receipts may wrap the same retained definition. Preserve the
	// original shape and still expose its actual historical snapshot.
	wrapped, _ := json.Marshal(map[string]any{"workflow": d1})
	if _, err = s.db.Exec(`UPDATE agent_workflow_edits SET response=? WHERE request_key=?`, wrapped, "create"); err != nil {
		t.Fatal(err)
	}
	page, err := s.AWVersions("alice", "research", d1.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Versions) != 3 || page.HasMore || page.EnabledVersion == nil || page.EnabledVersion.Revision != 2 {
		t.Fatalf("wrong history %+v", page)
	}
	for i, v := range page.Versions {
		if v.Revision != 3-i || v.IsEnabled != (v.Revision == 2) || v.IsCurrentDraft != (v.Revision == 3) {
			t.Fatalf("incorrect version badges %+v", v)
		}
		if v.StepCount != 3 {
			t.Fatal("history omitted step count", v)
		}
	}
	if !reflect.DeepEqual(page.Versions[0].Sources, []string{"draft", "saved", "run"}) {
		t.Fatal("sources do not describe actual retained records", page.Versions[0].Sources)
	}
	snapshot, summary, err := s.AWDefinitionVersion("alice", "research", d1.ID, d1.DefinitionHash, 1)
	if err != nil || snapshot.Name != d1.Name || snapshot.Revision != 1 || summary.IsCurrentDraft || summary.IsEnabled {
		t.Fatalf("exact preview failed %+v %v", summary, err)
	}
	restore := af.EditRequest{Action: "restore", WorkflowID: d1.ID, ExpectedRevision: 3, VersionID: d1.DefinitionHash, TargetRevision: 1, RequestID: "restore-one"}
	d4, reused, err := s.AWEdit("alice", "research", restore)
	if err != nil || reused || d4.Revision != 4 || d4.Name != d1.Name || d4.CreatedAt != d1.CreatedAt || d4.EnabledVersion != d2.DefinitionHash || d4.DefinitionHash == d1.DefinitionHash {
		t.Fatalf("restore did not create a new inactive draft: %+v %v", d4, err)
	}
	if !bytes.Equal(versionBefore, awVersionRaw(t, s, "agent_workflow_versions", "definition", "version_id", d2.DefinitionHash)) || !bytes.Equal(runBefore, awVersionRaw(t, s, "agent_workflow_runs", "record", "id", run.ID)) {
		t.Fatal("restoring rewrote an activation snapshot or Run")
	}
	d5 := awVersionPatchName(t, s, d4, "另一个较新草稿", "save-5")
	replay, reused, err := s.AWEdit("alice", "research", restore)
	if err != nil || !reused || replay.Revision != 4 {
		t.Fatalf("restore retry was not idempotent: %+v %v", replay, err)
	}
	latest, _ := s.AWGet("alice", "research", d1.ID)
	if latest.Revision != d5.Revision || latest.Name != d5.Name {
		t.Fatal("idempotent retry rewound the latest draft")
	}
	restore.RequestID = "stale-restore"
	_, _, err = s.AWEdit("alice", "research", restore)
	awExpect(t, err, ErrConflict)
	for _, scope := range [][2]string{{"bob", "research"}, {"alice", "other-agent"}} {
		_, err = s.AWVersions(scope[0], scope[1], d1.ID, 0)
		awExpect(t, err, ErrNotFound)
		_, _, err = s.AWDefinitionVersion(scope[0], scope[1], d1.ID, d1.DefinitionHash, 1)
		awExpect(t, err, ErrNotFound)
		restore.ExpectedRevision = d5.Revision
		_, _, err = s.AWEdit(scope[0], scope[1], restore)
		awExpect(t, err, ErrNotFound)
	}
	_, _, err = s.AWDefinitionVersion("alice", "research", d1.ID, d1.DefinitionHash, 2)
	awExpect(t, err, ErrNotFound)
}

func TestAgentWorkflowVersionsIncludeOnlyActuallyRetainedLegacySnapshots(t *testing.T) {
	s, _ := testStore(t)
	d1 := awFixture(t, s, "alice", "research")
	_, _, _, err := s.AWAdmit(awRunFixture(d1, "legacy-run"), "legacy-run")
	if err != nil {
		t.Fatal(err)
	}
	d2 := awVersionPatchName(t, s, d1, "旧的启用版本", "legacy-save")
	d2, err = s.AWEnable("alice", "research", d2.ID, d2.Revision, d2.DefinitionHash, true)
	if err != nil {
		t.Fatal(err)
	}
	d3 := awVersionPatchName(t, s, d2, "当前草稿", "latest-save")
	// Simulate pre-history data with retained Run/activation snapshots but no
	// edit receipts. Reading must neither fabricate missing saves nor backfill.
	if _, err = s.db.Exec(`DELETE FROM agent_workflow_edits WHERE owner_id=? AND agent_id=?`, "alice", "research"); err != nil {
		t.Fatal(err)
	}
	page, err := s.AWVersions("alice", "research", d1.ID, 0)
	if err != nil || len(page.Versions) != 3 {
		t.Fatalf("legacy retained history missing %+v %v", page, err)
	}
	if !reflect.DeepEqual(page.Versions[0].Sources, []string{"draft"}) || !reflect.DeepEqual(page.Versions[1].Sources, []string{"enabled"}) || !reflect.DeepEqual(page.Versions[2].Sources, []string{"run"}) {
		t.Fatal("legacy history was relabelled as saved edits", page.Versions)
	}
	var rows int
	if err = s.db.QueryRow(`SELECT count(*) FROM agent_workflow_edits`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("history read wrote migration rows", err)
	}
	got, _, err := s.AWDefinitionVersion("alice", "research", d1.ID, d1.DefinitionHash, 1)
	if err != nil || got.Revision != 1 {
		t.Fatal("Run-only snapshot cannot be previewed", err)
	}
	_, _, err = s.AWEdit("alice", "research", af.EditRequest{Action: "restore", WorkflowID: d1.ID, ExpectedRevision: d3.Revision, VersionID: d1.DefinitionHash, TargetRevision: 1, RequestID: "legacy-restore"})
	if err != nil {
		t.Fatal("Run-only snapshot cannot be restored", err)
	}
}

func TestAgentWorkflowVersionsPaginateAndKeepOldEnabledIdentity(t *testing.T) {
	s, _ := testStore(t)
	d := awFixture(t, s, "alice", "research")
	_, err := s.AWEnable("alice", "research", d.ID, d.Revision, d.DefinitionHash, true)
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 112; i++ {
		d = awVersionPatchName(t, s, d, fmt.Sprintf("保存 %d", i), fmt.Sprintf("save-%d", i))
	}
	page, err := s.AWVersions("alice", "research", d.ID, 0)
	if err != nil || len(page.Versions) != 100 || !page.HasMore || page.NextBeforeRevision != 13 || page.EnabledVersion == nil || page.EnabledVersion.Revision != 1 {
		t.Fatalf("bad first page %+v %v", page, err)
	}
	older, err := s.AWVersions("alice", "research", d.ID, page.NextBeforeRevision)
	if err != nil || len(older.Versions) != 12 || older.HasMore || older.Versions[0].Revision != 12 || older.Versions[11].Revision != 1 || !older.Versions[11].IsEnabled {
		t.Fatalf("bad older page %+v %v", older, err)
	}
	if _, _, err = s.AWEdit("alice", "research", af.EditRequest{Action: "archive", WorkflowID: d.ID, ExpectedRevision: d.Revision, RequestID: "archive"}); err != nil {
		t.Fatal(err)
	}
	page, err = s.AWVersions("alice", "research", d.ID, 0)
	if err != nil || len(page.Versions) != 100 || !page.HasMore || page.NextBeforeRevision != 13 {
		t.Fatal("archive receipt hid an older page", err)
	}
}

func TestAgentWorkflowRestoreConcurrentRetriesAndLargeDefinitions(t *testing.T) {
	s, _ := testStore(t)
	steps := make([]af.Step, 200)
	for i := range steps {
		steps[i] = af.Step{ID: fmt.Sprintf("step_%03d", i), Kind: "agent", Task: strings.Repeat("x", 8192)}
	}
	d1, _, err := s.AWEdit("alice", "research", af.EditRequest{Action: "create", RequestID: "large", Name: "200节点历史", Steps: steps})
	if err != nil {
		t.Fatal(err)
	}
	d2 := awVersionPatchName(t, s, d1, "较新的大图", "large-next")
	page, err := s.AWVersions("alice", "research", d1.ID, 0)
	if err != nil || len(page.Versions) != 2 || page.Versions[0].StepCount != 200 {
		t.Fatal("large history lost nodes", err)
	}
	raw, _ := json.Marshal(page)
	if len(raw) > 4096 {
		t.Fatal("history summary transported full node bodies", len(raw))
	}
	req := af.EditRequest{Action: "restore", WorkflowID: d1.ID, RequestID: "large-restore", ExpectedRevision: d2.Revision, VersionID: d1.DefinitionHash, TargetRevision: 1}
	var wg sync.WaitGroup
	var fresh atomic.Int32
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, reused, e := s.AWEdit("alice", "research", req)
			if e != nil {
				errs <- e
				return
			}
			if d.Revision != 3 || len(d.Steps) != 200 {
				errs <- fmt.Errorf("unexpected restored revision or node count")
			}
			if !reused {
				fresh.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err = range errs {
		t.Fatal(err)
	}
	if fresh.Load() != 1 {
		t.Fatal("concurrent retry restored more than once", fresh.Load())
	}
}

func TestAgentWorkflowVersionRejectsTamperedRevisionHashPair(t *testing.T) {
	s, _ := testStore(t)
	d1 := awFixture(t, s, "alice", "research")
	d2 := awVersionPatchName(t, s, d1, "版本二", "save-2")
	bad := d1
	bad.Revision = d2.Revision
	raw, _ := json.Marshal(bad)
	if _, err := s.db.Exec(`UPDATE agent_workflow_edits SET response=? WHERE request_key=?`, raw, "save-2"); err != nil {
		t.Fatal(err)
	}
	_, err := s.AWVersions("alice", "research", d1.ID, 0)
	if !errors.Is(err, ErrConflict) {
		t.Fatal("hash from another revision was accepted", err)
	}
	_, _, err = s.AWDefinitionVersion("alice", "research", d1.ID, d1.DefinitionHash, 2)
	awExpect(t, err, ErrConflict)
}

func TestAgentWorkflowRestoreCompetingRevisionsNeverOverwriteANewerDraft(t *testing.T) {
	s, _ := testStore(t)
	d1 := awFixture(t, s, "alice", "research")
	d2 := awVersionPatchName(t, s, d1, "第二版", "second")
	d3 := awVersionPatchName(t, s, d2, "第三版", "third")
	var wg sync.WaitGroup
	var committed, conflicted atomic.Int32
	errs := make(chan error, 2)
	for _, target := range []af.Definition{d1, d2} {
		wg.Add(1)
		go func(target af.Definition) {
			defer wg.Done()
			_, _, err := s.AWEdit("alice", "research", af.EditRequest{Action: "restore", WorkflowID: d1.ID, ExpectedRevision: d3.Revision, TargetRevision: target.Revision, VersionID: target.DefinitionHash, RequestID: fmt.Sprintf("restore-%d", target.Revision)})
			switch {
			case err == nil:
				committed.Add(1)
			case errors.Is(err, ErrConflict):
				conflicted.Add(1)
			default:
				errs <- err
			}
		}(target)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	latest, err := s.AWGet("alice", "research", d1.ID)
	if err != nil || committed.Load() != 1 || conflicted.Load() != 1 || latest.Revision != 4 {
		t.Fatal("competing restore overwrote a newer draft", latest, err)
	}
}

func TestAgentWorkflowVersionsRejectInconsistentStoredScope(t *testing.T) {
	s, _ := testStore(t)
	d := awFixture(t, s, "alice", "research")
	bad := d
	bad.OwnerID = "bob"
	raw, _ := json.Marshal(bad)
	if _, err := s.db.Exec(`UPDATE agent_workflow_definitions SET definition=? WHERE id=?`, raw, d.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.AWVersions("alice", "research", d.ID, 0)
	awExpect(t, err, ErrConflict)
	_, _, err = s.AWDefinitionVersion("alice", "research", d.ID, d.DefinitionHash, 1)
	awExpect(t, err, ErrConflict)
	_, _, err = s.AWEdit("alice", "research", af.EditRequest{Action: "restore", WorkflowID: d.ID, RequestID: "invalid-scope-restore", ExpectedRevision: 1, TargetRevision: 1, VersionID: d.DefinitionHash})
	awExpect(t, err, ErrConflict)
}
