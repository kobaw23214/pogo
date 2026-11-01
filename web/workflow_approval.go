package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/workflow"
)

var errWorkflowApprovalAssignment = errors.New("审批分配不可用")

func (s *Server) handleWorkflowApprovalSubjects(w http.ResponseWriter, r *http.Request) {
	if _, exists := s.mgr.workflows.get(r.PathValue("wid")); !exists {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	type subject struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		PrincipalID string `json:"principal_id"`
	}
	users := []subject{}
	if s.multiuser() {
		for _, entry := range s.workflowAccessSubjects() {
			if s.workflowApprovalPrincipalActive(entry.PrincipalID) {
				users = append(users, subject{Username: entry.DisplayName, DisplayName: entry.DisplayName, PrincipalID: entry.PrincipalID})
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"subjects": users})
}

func workflowHasApprovalPolicies(def workflow.Definition) bool {
	for _, node := range def.Nodes {
		if node.ApprovalPolicy != nil {
			return true
		}
	}
	return false
}

func (s *Server) workflowApprovalPrincipal(id string) (identity.Principal, bool) {
	if !s.multiuser() {
		if id == identity.SingleUserPrincipal().ID {
			return identity.SingleUserPrincipal(), true
		}
		return identity.Principal{}, false
	}
	s.auth.mu.RLock()
	defer s.auth.mu.RUnlock()
	for _, account := range s.auth.users {
		p := identity.LocalPrincipal(identity.LocalScope(account.Username), account.Username, account.Admin || account.Username == bootstrapUser)
		if p.ID == id {
			return p, true
		}
	}
	return identity.Principal{}, false
}

func (s *Server) workflowApprovalPrincipalActive(id string) bool {
	p, exists := s.workflowApprovalPrincipal(id)
	if !exists {
		return false
	}
	role, err := s.workflowRole(p)
	return err == nil && role != "disabled"
}

func (s *Server) resolveWorkflowApprovals(def workflow.Definition, meta taskqueue.WorkflowRunMeta, business map[string][]string, previous map[string]taskqueue.WorkflowApprovalAssignment) (map[string]taskqueue.WorkflowApprovalAssignment, error) {
	if !workflowHasApprovalPolicies(def) {
		return nil, nil
	}
	result := map[string]taskqueue.WorkflowApprovalAssignment{}
	for _, node := range def.Nodes {
		policy := node.ApprovalPolicy
		if policy == nil {
			continue
		}
		assignment := taskqueue.WorkflowApprovalAssignment{PreventSelfApproval: policy.PreventSelfApproval, Assignment: policy.Assignment}
		if prior, ok := previous[node.ID]; ok {
			// Retries retain the original routing candidates, while excluding
			// their actual initiator and currently unavailable accounts anew.
			assignment.Candidates = slices.Clone(prior.Candidates)
		} else if policy.Assignment == "business" {
			assignment.Candidates = slices.Clone(business[node.ID])
		} else {
			for _, username := range policy.Approvers {
				if s.multiuser() {
					assignment.Candidates = append(assignment.Candidates, identity.LocalPrincipal(identity.LocalScope(username), username, false).ID)
				}
			}
		}
		sort.Strings(assignment.Candidates)
		assignment.Candidates = slices.Compact(assignment.Candidates)
		for _, candidate := range assignment.Candidates {
			if assignment.PreventSelfApproval && (candidate == meta.OwnerID || candidate == meta.InitiatorID) {
				continue
			}
			if s.workflowApprovalPrincipalActive(candidate) {
				assignment.Assignees = append(assignment.Assignees, candidate)
			}
		}
		if len(assignment.Assignees) == 0 {
			return nil, fmt.Errorf("%w: 审批节点 %s 没有可用的非自批审批人，请调整审批配置或业务审批分配", errWorkflowApprovalAssignment, node.ID)
		}
		result[node.ID] = assignment
	}
	return result, nil
}

func (m *Manager) prepareWorkflowApprovalAssignments(def workflow.Definition, meta *taskqueue.WorkflowRunMeta, key string, business map[string][]string, previous map[string]taskqueue.WorkflowApprovalAssignment) error {
	if !workflowHasApprovalPolicies(def) {
		return nil
	}
	db, err := m.taskRuns.database(true)
	if err != nil {
		return err
	}
	if old, err := db.WorkflowAdmissionMeta(meta.OwnerID, meta.WorkflowID, key); err == nil {
		for node, prior := range old.Approvals {
			if prior.Assignment != "business" {
				continue
			}
			candidates := slices.Clone(business[node])
			if origin, ok := previous[node]; ok {
				candidates = slices.Clone(origin.Candidates)
			}
			sort.Strings(candidates)
			candidates = slices.Compact(candidates)
			if !slices.Equal(candidates, prior.Candidates) {
				return fmt.Errorf("%w: 同一运行请求不能更换业务审批人", taskqueue.ErrConflict)
			}
		}
		meta.Approvals = old.Approvals
		return nil
	} else if !errors.Is(err, taskqueue.ErrNotFound) {
		return err
	}
	m.taskRuns.mu.Lock()
	resolve := m.taskRuns.workflowApprovalResolver
	m.taskRuns.mu.Unlock()
	if resolve == nil {
		return errors.New("审批分配尚未就绪，未发起运行")
	}
	assignments, err := resolve(def, *meta, business, previous)
	if err != nil {
		return err
	}
	meta.Approvals = assignments
	return nil
}

func workflowApprovalPolicy(rec workflow.RunRecord, nodeID string) *workflow.ApprovalPolicy {
	for _, node := range rec.Definition.Nodes {
		if node.ID == nodeID {
			return node.ApprovalPolicy
		}
	}
	return nil
}

// Policy approval is separate from owner/operator privileges. A policy node
// needs an explicit approval grant, and the separation rule precedes all grants.
func (s *Server) workflowPolicyApprovalAllowed(r *http.Request, rec workflow.RunRecord, nodeID string) (bool, bool, error) {
	declared := workflowApprovalPolicy(rec, nodeID) != nil || nodeID == "" && workflowHasApprovalPolicies(rec.Definition)
	db, err := s.workflowAccessStore(false)
	if err != nil || db == nil {
		return declared || err != nil, false, err
	}
	meta, err := db.GetWorkflowRun(rec.ID)
	if errors.Is(err, taskqueue.ErrNotFound) {
		return declared, false, nil
	}
	if err != nil {
		return true, false, err
	}
	assignment, ok := meta.Approvals[nodeID]
	if nodeID == "" && len(meta.Approvals) > 0 {
		return true, false, nil
	}
	if !ok {
		return declared, false, nil
	}
	if meta.WorkflowID != rec.WorkflowID {
		return true, false, nil
	}
	principal := s.workflowPrincipal(r)
	if assignment.PreventSelfApproval && (principal.ID == meta.OwnerID || principal.ID == meta.InitiatorID) {
		return true, false, nil
	}
	for _, scope := range []string{nodeID, ""} {
		grants, err := db.RunGrant(rec.ID, principal.ID, scope)
		if err != nil {
			return true, false, err
		}
		if slices.Contains(grants, wfRunApprove) {
			return true, true, nil
		}
	}
	return true, false, nil
}

func (s *Server) authorizeWorkflowApprovalDecision(rec workflow.RunRecord, decision workflow.HumanDecision) error {
	if decision.Timeout {
		return nil
	}
	p, ok := s.workflowApprovalPrincipal(decision.Actor)
	if !ok {
		return errors.New("审批身份无效")
	}
	r, _ := http.NewRequest(http.MethodPost, "/", nil)
	allowed, err := s.workflowRunAllowed(withWorkflowPrincipal(r, p), rec, wfRunApprove, decision.Node)
	if err != nil {
		return err
	}
	if !allowed {
		return errors.New("没有处理该节点审批的权限，发起人不得自批")
	}
	return nil
}

func (s *Server) workflowApprovalGrantAllowed(rec workflow.RunRecord, nodeID, target string) (bool, error) {
	db, err := s.workflowAccessStore(false)
	if err != nil || db == nil {
		return false, err
	}
	meta, err := db.GetWorkflowRun(rec.ID)
	if errors.Is(err, taskqueue.ErrNotFound) {
		return true, nil // Legacy records retain their original participant API.
	}
	if err != nil {
		return false, err
	}
	for id, assignment := range meta.Approvals {
		if nodeID != "" && id != nodeID {
			continue
		}
		if assignment.PreventSelfApproval && (target == meta.OwnerID || target == meta.InitiatorID) {
			return false, nil
		}
	}
	return true, nil
}

func (s *Server) workflowApprovalView(r *http.Request, rec workflow.RunRecord) map[string]workflow.RunApprovalAssignment {
	db, err := s.workflowAccessStore(false)
	if err != nil || db == nil {
		return nil
	}
	meta, err := db.GetWorkflowRun(rec.ID)
	if err != nil || len(meta.Approvals) == 0 {
		return nil
	}
	grants, err := db.ListRunGrants(rec.ID)
	if err != nil {
		return nil
	}
	actor := s.workflowPrincipal(r).ID
	result := map[string]workflow.RunApprovalAssignment{}
	for node, initial := range meta.Approvals {
		v := workflow.RunApprovalAssignment{PreventSelfApproval: initial.PreventSelfApproval, Assignment: initial.Assignment,
			Assignees: []workflow.ApprovalAssignee{}, InitialAssignees: []workflow.ApprovalAssignee{}}
		for _, id := range initial.Assignees {
			p, exists := s.workflowApprovalPrincipal(id)
			name := "历史主体"
			if exists {
				name = p.Name
			}
			v.InitialAssignees = append(v.InitialAssignees, workflow.ApprovalAssignee{PrincipalID: id, DisplayName: name})
		}
		seen := map[string]bool{}
		for _, grant := range grants {
			if grant.NodeID != "" && grant.NodeID != node || !slices.Contains(grant.Actions, wfRunApprove) || seen[grant.PrincipalID] {
				continue
			}
			id := grant.PrincipalID
			if initial.PreventSelfApproval && (id == meta.OwnerID || id == meta.InitiatorID) || !s.workflowApprovalPrincipalActive(id) {
				continue
			}
			p, _ := s.workflowApprovalPrincipal(id)
			v.Assignees = append(v.Assignees, workflow.ApprovalAssignee{PrincipalID: id, DisplayName: p.Name})
			seen[id] = true
		}
		sort.Slice(v.Assignees, func(i, j int) bool { return v.Assignees[i].PrincipalID < v.Assignees[j].PrincipalID })
		if initial.PreventSelfApproval && (actor == meta.OwnerID || actor == meta.InitiatorID) {
			v.BlockedReason = "initiator_cannot_approve"
		} else if len(v.Assignees) == 0 {
			v.BlockedReason = "no_eligible_approver"
		} else if !seen[actor] {
			v.BlockedReason = "not_assigned"
		}
		result[node] = v
	}
	return result
}
