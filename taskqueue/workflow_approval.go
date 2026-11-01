package taskqueue

import (
	"database/sql"
	"fmt"
	"slices"
	"sort"
)

// WorkflowApprovalAssignment is the immutable admission-time routing decision.
// Candidates survive retries; effective node grants remain the live authority.
type WorkflowApprovalAssignment struct {
	PreventSelfApproval bool     `json:"prevent_self_approval"`
	Assignment          string   `json:"assignment"`
	Candidates          []string `json:"candidates"`
	Assignees           []string `json:"assignees"`
}

func normalizeWorkflowApprovals(meta WorkflowRunMeta) (map[string]WorkflowApprovalAssignment, error) {
	if len(meta.Approvals) == 0 {
		return nil, nil
	}
	if len(meta.Approvals) > 256 {
		return nil, fmt.Errorf("%w: too many approval assignments", ErrConflict)
	}
	out := make(map[string]WorkflowApprovalAssignment, len(meta.Approvals))
	for node, assignment := range meta.Approvals {
		if !validText(node, 64, true) || (assignment.Assignment != "fixed" && assignment.Assignment != "business") ||
			len(assignment.Candidates) == 0 || len(assignment.Candidates) > 100 || len(assignment.Assignees) == 0 || len(assignment.Assignees) > 100 {
			return nil, fmt.Errorf("%w: invalid approval assignment", ErrConflict)
		}
		assignment.Candidates = slices.Clone(assignment.Candidates)
		assignment.Assignees = slices.Clone(assignment.Assignees)
		for _, principal := range append(slices.Clone(assignment.Candidates), assignment.Assignees...) {
			if !validText(principal, 512, true) {
				return nil, fmt.Errorf("%w: invalid approval principal", ErrConflict)
			}
		}
		for _, principal := range assignment.Assignees {
			if !slices.Contains(assignment.Candidates, principal) || assignment.PreventSelfApproval && (principal == meta.OwnerID || principal == meta.InitiatorID) {
				return nil, fmt.Errorf("%w: invalid self approval assignment", ErrConflict)
			}
		}
		sort.Strings(assignment.Candidates)
		assignment.Candidates = slices.Compact(assignment.Candidates)
		sort.Strings(assignment.Assignees)
		assignment.Assignees = slices.Compact(assignment.Assignees)
		out[node] = assignment
	}
	return out, nil
}

func grantWorkflowApprovers(tx *sql.Tx, meta WorkflowRunMeta) error {
	for node, assignment := range meta.Approvals {
		for _, principal := range assignment.Assignees {
			if _, err := tx.Exec(`INSERT INTO workflow_run_grants(run_id,principal_id,node_id,actions) VALUES(?,?,?,?)`,
				meta.ID, principal, node, []byte(`["approval.decide","artifact.read","run.read"]`)); err != nil {
				return err
			}
		}
	}
	return nil
}

// WorkflowAdmissionMeta is a trusted-host lookup used to retain the original
// routing snapshot after an accepted request loses its HTTP acknowledgement.
// AdmitWorkflow still compares the complete immutable intent before returning it.
func (s *Store) WorkflowAdmissionMeta(owner, workflowID, key string) (WorkflowRunMeta, error) {
	if !validText(owner, 512, true) || !validText(workflowID, 256, true) || !validText(key, 512, true) {
		return WorkflowRunMeta{}, ErrConflict
	}
	var id string
	err := s.db.QueryRow(`SELECT run_id FROM workflow_admissions WHERE owner_id=? AND workflow_id=? AND request_key=?`, owner, workflowID, key).Scan(&id)
	if err == sql.ErrNoRows {
		return WorkflowRunMeta{}, ErrNotFound
	}
	if err != nil {
		return WorkflowRunMeta{}, err
	}
	return s.GetWorkflowRun(id)
}
