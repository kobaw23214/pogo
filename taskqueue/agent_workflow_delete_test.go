package taskqueue

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	af "github.com/yosukeno/pogo/agentworkflow"
)

func TestAgentWorkflowDeleteRetainsScopedHistoryAndBlocksExecution(t *testing.T) {
	s, _ := testStore(t)
	d := awFixture(t, s, "alice", "research")
	enabled, err := s.AWEnable("alice", "research", d.ID, d.Revision, d.DefinitionHash, true)
	if err != nil {
		t.Fatal(err)
	}
	r, _, _, err := s.AWAdmit(awRunFixture(enabled, "retained-run"), "old-trial")
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.AWCancel("alice", "research", d.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var runBefore, versionBefore []byte
	if err = s.db.QueryRow(`SELECT record FROM agent_workflow_runs WHERE id=?`, r.ID).Scan(&runBefore); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT definition FROM agent_workflow_versions WHERE workflow_id=? AND version_id=?`, d.ID, d.DefinitionHash).Scan(&versionBefore); err != nil {
		t.Fatal(err)
	}
	request := af.EditRequest{Action: "delete", WorkflowID: d.ID, ExpectedRevision: d.Revision, RequestID: "delete-exact"}
	_, _, err = s.AWEdit("bob", "research", request)
	awExpect(t, err, ErrNotFound)
	_, _, err = s.AWEdit("alice", "other-agent", request)
	awExpect(t, err, ErrNotFound)
	stale := request
	stale.ExpectedRevision++
	_, _, err = s.AWEdit("alice", "research", stale)
	awExpect(t, err, ErrConflict)
	deleted, reused, err := s.AWEdit("alice", "research", request)
	if err != nil || reused || !deleted.Deleted || !deleted.Archived || deleted.EnabledVersion != "" || deleted.Revision != d.Revision+1 {
		t.Fatalf("delete %+v reused=%v err=%v", deleted, reused, err)
	}
	replay, reused, err := s.AWEdit("alice", "research", request)
	if err != nil || !reused || !func() bool { a, _ := json.Marshal(replay); b, _ := json.Marshal(deleted); return bytes.Equal(a, b) }() {
		t.Fatalf("delete replay %+v reused=%v err=%v", replay, reused, err)
	}
	normal, err := s.AWList("alice", "research")
	if err != nil || len(normal) != 0 {
		t.Fatalf("deleted listed normally %+v %v", normal, err)
	}
	history, err := s.AWList("alice", "research", true)
	if err != nil || len(history) != 1 || !history[0].Deleted {
		t.Fatalf("deleted history missing %+v %v", history, err)
	}
	foreign, err := s.AWList("bob", "research", true)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("history scope leak %+v %v", foreign, err)
	}
	current, err := s.AWGet("alice", "research", d.ID)
	if err != nil || !current.Deleted {
		t.Fatal("deleted definition not readable", err)
	}
	old, _, err := s.AWDefinitionVersion("alice", "research", d.ID, d.DefinitionHash, d.Revision)
	if err != nil || old.Deleted || old.DefinitionHash != d.DefinitionHash {
		t.Fatal("prior version changed", err)
	}
	oldRun, err := s.AWRun("alice", "research", d.ID, r.ID)
	if err != nil || oldRun.ID != r.ID {
		t.Fatal("prior run missing", err)
	}
	var runAfter, versionAfter []byte
	if err = s.db.QueryRow(`SELECT record FROM agent_workflow_runs WHERE id=?`, r.ID).Scan(&runAfter); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT definition FROM agent_workflow_versions WHERE workflow_id=? AND version_id=?`, d.ID, d.DefinitionHash).Scan(&versionAfter); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(runBefore, runAfter) || !bytes.Equal(versionBefore, versionAfter) {
		t.Fatal("deletion rewrote frozen history")
	}
	_, _, err = s.AWEdit("alice", "research", af.EditRequest{Action: "patch", WorkflowID: d.ID, ExpectedRevision: deleted.Revision, RequestID: "patch-deleted", Operations: []af.Operation{{Op: "set_metadata", Name: func() *string { v := "changed"; return &v }()}}})
	awExpect(t, err, ErrConflict)
	_, _, err = s.AWEdit("alice", "research", af.EditRequest{Action: "restore", WorkflowID: d.ID, ExpectedRevision: deleted.Revision, RequestID: "restore-deleted", VersionID: d.DefinitionHash, TargetRevision: d.Revision})
	awExpect(t, err, ErrConflict)
	_, err = s.AWEnable("alice", "research", d.ID, deleted.Revision, deleted.DefinitionHash, true)
	awExpect(t, err, ErrConflict)
	_, _, _, err = s.AWAdmit(awRunFixture(d, "new-trial"), "new-trial")
	awExpect(t, err, ErrConflict)
	retry := awRunFixture(d, "new-retry")
	retry.RetryOf = r.ID
	_, _, _, err = s.AWAdmit(retry, "new-retry")
	awExpect(t, err, ErrConflict)
	// A delayed pre-delete edit receipt cannot make a deleted workflow appear live.
	_, _, err = s.AWEdit("alice", "research", af.EditRequest{Action: "create", RequestID: "create", Name: d.Name, Inputs: d.Inputs, Steps: d.Steps})
	awExpect(t, err, ErrConflict)
}

func TestAgentWorkflowDeleteRejectsUnfinishedRunsAndCancellationSegments(t *testing.T) {
	for _, status := range []string{af.Queued, af.Running, af.WaitingConfirmation} {
		t.Run(status, func(t *testing.T) {
			s, _ := testStore(t)
			d := awFixture(t, s, "alice", "research")
			r, q, _, err := s.AWAdmit(awRunFixture(d, "busy-run"), "start")
			if err != nil {
				t.Fatal(err)
			}
			if status != af.Queued {
				r, q = awWaiting(t, s, r, q)
				if status == af.Running {
					r.Status = af.Running
					r.CurrentStep = 1
					r.Steps[1].Status = af.Running
					raw, _ := json.Marshal(r)
					if _, err = s.db.Exec(`UPDATE agent_workflow_runs SET record=? WHERE id=?`, raw, r.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, _ := json.Marshal(r)
			request := af.EditRequest{Action: "delete", WorkflowID: d.ID, ExpectedRevision: d.Revision, RequestID: "delete-busy"}
			_, _, err = s.AWEdit("alice", "research", request)
			awExpect(t, err, ErrConflict)
			unchanged, err := s.AWRun("alice", "research", d.ID, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(unchanged)
			if !bytes.Equal(before, after) {
				t.Fatal("delete implicitly changed/cancelled the run")
			}
			if _, err = s.AWCancel("alice", "research", d.ID, r.ID); err != nil {
				t.Fatal(err)
			}
			if status != af.Queued {
				// Waiting-confirmation cancellation is a durable terminal logical Run;
				// the dispatcher is still exiting and must retain its execution boundary.
				_, _, err = s.AWEdit("alice", "research", request)
				awExpect(t, err, ErrConflict)
				if _, err = s.Finish(q.ID, q.Revision, Outcome{Status: Cancelled}); err != nil {
					t.Fatal(err)
				}
				if status == af.Running {
					// A running cancellation keeps the logical Run live until its worker settles.
					got, err := s.AWRun("alice", "research", d.ID, r.ID)
					if err != nil {
						t.Fatal(err)
					}
					got.Status = af.Cancelled
					raw, _ := json.Marshal(got)
					if _, err = s.db.Exec(`UPDATE agent_workflow_runs SET record=? WHERE id=?`, raw, r.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			deleted, _, err := s.AWEdit("alice", "research", request)
			if err != nil || !deleted.Deleted {
				t.Fatal("delete after explicit end", err)
			}
		})
	}
}

func TestAgentWorkflowDeleteAllowsArchivedAndInvalidDraft(t *testing.T) {
	for _, kind := range []string{"archived", "invalid-draft"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := testStore(t)
			d := awFixture(t, s, "alice", "research")
			var err error
			if kind == "archived" {
				d, _, err = s.AWEdit("alice", "research", af.EditRequest{Action: "archive", WorkflowID: d.ID, ExpectedRevision: d.Revision, RequestID: "archive"})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				d.Steps = nil
				d.DefinitionHash = af.Hash(d)
				raw, _ := json.Marshal(d)
				if _, err = s.db.Exec(`UPDATE agent_workflow_definitions SET definition=? WHERE id=?`, raw, d.ID); err != nil {
					t.Fatal(err)
				}
			}
			got, _, err := s.AWEdit("alice", "research", af.EditRequest{Action: "delete", WorkflowID: d.ID, ExpectedRevision: d.Revision, RequestID: "delete"})
			if err != nil || !got.Deleted {
				t.Fatalf("%s cannot delete: %v", kind, err)
			}
		})
	}
}

func TestAgentWorkflowDeleteAndAdmissionSerialize(t *testing.T) {
	for n := 0; n < 12; n++ {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s, _ := testStore(t)
			d := awFixture(t, s, "alice", "research")
			ready := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			var deleteErr, admitErr error
			go func() {
				defer wg.Done()
				<-ready
				_, _, deleteErr = s.AWEdit("alice", "research", af.EditRequest{Action: "delete", WorkflowID: d.ID, ExpectedRevision: d.Revision, RequestID: "delete-race"})
			}()
			go func() {
				defer wg.Done()
				<-ready
				_, _, _, admitErr = s.AWAdmit(awRunFixture(d, "concurrent-run"), "start-race")
			}()
			close(ready)
			wg.Wait()
			if (deleteErr == nil) == (admitErr == nil) {
				t.Fatalf("delete=%v admit=%v", deleteErr, admitErr)
			}
			if deleteErr != nil && !errors.Is(deleteErr, ErrConflict) {
				t.Fatal(deleteErr)
			}
			if admitErr != nil && !errors.Is(admitErr, ErrConflict) {
				t.Fatal(admitErr)
			}
			current, err := s.AWGet("alice", "research", d.ID)
			if err != nil {
				t.Fatal(err)
			}
			runs, err := s.AWRuns("alice", "research", d.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Deleted && len(runs) != 0 || !current.Deleted && len(runs) != 1 {
				t.Fatal("deleted workflow and live admission coexist")
			}
		})
	}
}
