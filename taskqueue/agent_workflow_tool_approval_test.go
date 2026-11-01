package taskqueue

import (
	"bytes"
	af "github.com/yosukeno/pogo/agentworkflow"
	"testing"
)

func TestAgentWorkflowToolApprovalVersionAndHistoryRetention(t *testing.T) {
	s, _ := testStore(t)
	legacy := awFixture(t, s, "alice", "research")
	run, _, _, err := s.AWAdmit(awRunFixture(legacy, "legacy-approval-run"), "legacy-run")
	if err != nil {
		t.Fatal(err)
	}
	before := awVersionRaw(t, s, "agent_workflow_runs", "record", "id", run.ID)
	mode := "auto"
	next, _, err := s.AWEdit("alice", "research", af.EditRequest{Action: "patch", WorkflowID: legacy.ID, ExpectedRevision: legacy.Revision, RequestID: "approval-default", Operations: []af.Operation{{Op: "set_metadata", ToolApprovalMode: &mode}}})
	if err != nil || next.ToolApprovalMode != mode || next.Revision != legacy.Revision+1 {
		t.Fatal("review default not versioned", err)
	}
	enabled, err := s.AWEnable("alice", "research", next.ID, next.Revision, next.DefinitionHash, true)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := s.AWVersion("alice", "research", next.ID, enabled.EnabledVersion)
	if err != nil || frozen.ToolApprovalMode != "auto" {
		t.Fatal("enabled version lost mode", err)
	}
	mode = "strict"
	latest, _, err := s.AWEdit("alice", "research", af.EditRequest{Action: "patch", WorkflowID: next.ID, ExpectedRevision: next.Revision, RequestID: "approval-next", Operations: []af.Operation{{Op: "set_metadata", ToolApprovalMode: &mode}}})
	if err != nil || latest.EnabledVersion != enabled.EnabledVersion {
		t.Fatal("draft mode changed activation", err)
	}
	restored, _, err := s.AWEdit("alice", "research", af.EditRequest{Action: "restore", WorkflowID: latest.ID, ExpectedRevision: latest.Revision, RequestID: "restore-legacy-default", VersionID: legacy.DefinitionHash, TargetRevision: legacy.Revision})
	if err != nil || restored.ToolApprovalMode != "" || restored.EffectiveToolApprovalMode() != "standard" || restored.EnabledVersion != enabled.EnabledVersion {
		t.Fatal("restore changed old mode or active pointer", err)
	}
	if !bytes.Equal(before, awVersionRaw(t, s, "agent_workflow_runs", "record", "id", run.ID)) {
		t.Fatal("changing review mode rewrote retained Run")
	}
	old, _, err := s.AWDefinitionVersion("alice", "research", legacy.ID, legacy.DefinitionHash, legacy.Revision)
	if err != nil || old.ToolApprovalMode != "" || old.DefinitionHash != legacy.DefinitionHash {
		t.Fatal("old definition/hash backfilled", err)
	}
}

func TestAgentWorkflowToolApprovalRetryKeepsOriginalMode(t *testing.T) {
	s, _ := testStore(t)
	d, _, err := s.AWEdit("alice", "research", af.EditRequest{Action: "create", RequestID: "auto-create", Name: "retry policy", ToolApprovalMode: "auto", Inputs: []af.Input{{Key: "topic"}}, Steps: []af.Step{{ID: "task", Kind: "agent", Task: "synthetic task"}}})
	if err != nil {
		t.Fatal(err)
	}
	run, _, _, err := s.AWAdmit(awRunFixture(d, "auto-first"), "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AWCancel("alice", "research", d.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	mode := "strict"
	if _, _, err = s.AWEdit("alice", "research", af.EditRequest{Action: "patch", WorkflowID: d.ID, ExpectedRevision: d.Revision, RequestID: "strict-next", Operations: []af.Operation{{Op: "set_metadata", ToolApprovalMode: &mode}}}); err != nil {
		t.Fatal(err)
	}
	retry := awRunFixture(d, "auto-retry")
	retry.RetryOf = run.ID
	next, _, _, err := s.AWAdmit(retry, "retry")
	if err != nil || next.Definition.ToolApprovalMode != "auto" || next.VersionID != run.VersionID || next.RetryOf != run.ID {
		t.Fatal("retry used latest draft approval instead of frozen snapshot", err)
	}
}
