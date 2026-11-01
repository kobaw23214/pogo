package taskqueue

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"

	af "github.com/yosukeno/pogo/agentworkflow"
)

func TestAgentWorkflowLayoutScopeRevisionAndSnapshotIsolation(t *testing.T) {
	s, _ := testStore(t)
	d := awFixture(t, s, "alice", "research")
	d, err := s.AWEnable("alice", "research", d.ID, d.Revision, d.DefinitionHash, true)
	if err != nil {
		t.Fatal(err)
	}
	run, _, _, err := s.AWAdmit(awRunFixture(d, "layout-run"), "layout-run")
	if err != nil {
		t.Fatal(err)
	}
	var beforeDefinition, beforeVersion, beforeRun []byte
	if err = s.db.QueryRow(`SELECT definition FROM agent_workflow_definitions WHERE id=?`, d.ID).Scan(&beforeDefinition); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT definition FROM agent_workflow_versions WHERE workflow_id=? AND version_id=?`, d.ID, d.EnabledVersion).Scan(&beforeVersion); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT record FROM agent_workflow_runs WHERE id=?`, run.ID).Scan(&beforeRun); err != nil {
		t.Fatal(err)
	}
	initial, err := s.AWLayout("alice", "research", d.ID)
	if err != nil || initial.LayoutRevision != 0 || initial.DefinitionRevision != d.Revision || initial.Positions == nil {
		t.Fatalf("initial %+v %v", initial, err)
	}
	request := af.LayoutSave{RequestID: "move-one", ExpectedDefinitionRevision: d.Revision, Positions: map[string]af.Position{af.LayoutInputNode: {X: 1, Y: 2}, "collect": {X: 200, Y: 40}}, Viewport: &af.Viewport{Zoom: 0.8}}
	got, reused, err := s.AWSaveLayout("alice", "research", d.ID, request)
	if err != nil || reused || got.LayoutRevision != 1 || got.Positions["collect"].X != 200 {
		t.Fatalf("save %+v %v %v", got, reused, err)
	}
	_, err = s.AWLayout("bob", "research", d.ID)
	awExpect(t, err, ErrNotFound)
	_, _, err = s.AWSaveLayout("alice", "other", d.ID, request)
	awExpect(t, err, ErrNotFound)
	replay, reused, err := s.AWSaveLayout("alice", "research", d.ID, request)
	if err != nil || !reused || replay.LayoutRevision != got.LayoutRevision {
		t.Fatal("lost idempotent layout receipt", err)
	}
	request.Positions["collect"] = af.Position{X: 900}
	_, _, err = s.AWSaveLayout("alice", "research", d.ID, request)
	awExpect(t, err, ErrConflict)
	request.RequestID = "another-intent"
	_, _, err = s.AWSaveLayout("alice", "research", d.ID, request)
	awExpect(t, err, ErrConflict)
	var afterDefinition, afterVersion, afterRun []byte
	_ = s.db.QueryRow(`SELECT definition FROM agent_workflow_definitions WHERE id=?`, d.ID).Scan(&afterDefinition)
	_ = s.db.QueryRow(`SELECT definition FROM agent_workflow_versions WHERE workflow_id=? AND version_id=?`, d.ID, d.EnabledVersion).Scan(&afterVersion)
	_ = s.db.QueryRow(`SELECT record FROM agent_workflow_runs WHERE id=?`, run.ID).Scan(&afterRun)
	if !bytes.Equal(beforeDefinition, afterDefinition) || !bytes.Equal(beforeVersion, afterVersion) || !bytes.Equal(beforeRun, afterRun) {
		t.Fatal("layout changed semantic definition, activation or historical run")
	}
}

func TestAgentWorkflowLayoutStaleDefinitionAndInvalidPositions(t *testing.T) {
	s, _ := testStore(t)
	d := awFixture(t, s, "alice", "research")
	request := af.LayoutSave{RequestID: "layout", ExpectedDefinitionRevision: d.Revision, Positions: map[string]af.Position{"collect": {X: 20}}}
	if _, _, err := s.AWSaveLayout("alice", "research", d.ID, request); err != nil {
		t.Fatal(err)
	}
	name := "对话修改后的流程"
	next, _, err := s.AWEdit("alice", "research", af.EditRequest{Action: "patch", WorkflowID: d.ID, ExpectedRevision: d.Revision, RequestID: "edit-name", Operations: []af.Operation{{Op: "set_metadata", Name: &name}}})
	if err != nil {
		t.Fatal(err)
	}
	request.RequestID, request.ExpectedLayoutRevision = "stale-graph", 1
	_, _, err = s.AWSaveLayout("alice", "research", d.ID, request)
	awExpect(t, err, ErrConflict)
	view, err := s.AWLayout("alice", "research", d.ID)
	if err != nil || view.LayoutRevision != 1 || view.DefinitionRevision != next.Revision {
		t.Fatalf("GET must not rewrite revision: %+v %v", view, err)
	}
	request.ExpectedDefinitionRevision = next.Revision
	for _, bad := range []map[string]af.Position{nil, {"foreign": {}}, {"collect": {X: math.NaN()}}, {"collect": {Y: 1_000_001}}, {"../escape": {}}} {
		request.Positions = bad
		_, _, err = s.AWSaveLayout("alice", "research", d.ID, request)
		if !errors.Is(err, af.ErrInvalid) {
			t.Fatalf("bad positions admitted: %v: %v", bad, err)
		}
	}
	request.Positions, request.Viewport = map[string]af.Position{}, &af.Viewport{Zoom: 0}
	_, _, err = s.AWSaveLayout("alice", "research", d.ID, request)
	awExpect(t, err, af.ErrInvalid)
	request.Viewport = nil
	request.ExpectedDefinitionRevision = next.Revision
	if _, _, err = s.AWEdit("alice", "research", af.EditRequest{Action: "archive", WorkflowID: d.ID, ExpectedRevision: next.Revision, RequestID: "archive"}); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.AWSaveLayout("alice", "research", d.ID, request)
	awExpect(t, err, ErrConflict)
}

func TestAgentWorkflowLayoutConcurrentReplayAndDurableProjection(t *testing.T) {
	s, path := testStore(t)
	d, _, err := s.AWEdit("alice", "research", af.EditRequest{Action: "create", RequestID: "new", Name: "画布", Steps: []af.Step{{ID: "first", Kind: "agent", Task: "整理"}, {ID: "review", Kind: "confirm", Prompt: "核对"}}})
	if err != nil {
		t.Fatal(err)
	}
	req := af.LayoutSave{RequestID: "same-layout", ExpectedDefinitionRevision: d.Revision, Positions: map[string]af.Position{"first": {X: 200}, "review": {X: 500}, af.LayoutOutputNode: {X: 800}}}
	var wg sync.WaitGroup
	var fresh atomic.Int32
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, reused, e := s.AWSaveLayout("alice", "research", d.ID, req)
			if e != nil || value.LayoutRevision != 1 {
				t.Errorf("concurrent layout: %+v %v", value, e)
			}
			if !reused && e == nil {
				fresh.Add(1)
			}
		}()
	}
	wg.Wait()
	if fresh.Load() != 1 {
		t.Fatal("duplicate layout revisions", fresh.Load())
	}
	_, _, err = s.AWEdit("alice", "research", af.EditRequest{Action: "patch", WorkflowID: d.ID, ExpectedRevision: d.Revision, RequestID: "remove-node", Operations: []af.Operation{{Op: "remove_step", StepID: "review"}}})
	if err != nil {
		t.Fatal(err)
	}
	var stored []byte
	_ = s.db.QueryRow(`SELECT layout FROM agent_workflow_layouts WHERE workflow_id=?`, d.ID).Scan(&stored)
	var original af.Layout
	_ = json.Unmarshal(stored, &original)
	if _, ok := original.Positions["review"]; !ok {
		t.Fatal("semantic edit rewrote original layout")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	view, err := reopened.AWLayout("alice", "research", d.ID)
	if err != nil || view.LayoutRevision != 1 || view.DefinitionRevision != 2 {
		t.Fatalf("reopen %+v %v", view, err)
	}
	if _, exists := view.Positions["review"]; exists {
		t.Fatal("removed step exposed in current layout projection")
	}
	if view.Positions["first"].X != 200 {
		t.Fatal("saved position lost")
	}
}
