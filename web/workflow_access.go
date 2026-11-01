package web

// Workflow access has one action-based policy for old and new API paths.
// Only the local-account adapter retains the legacy single-user operator
// grant; service and future external principals must be explicitly allowed.
import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/workflow"
)

const (
	wfRead                  = "workflow.read"
	wfEdit                  = "workflow.edit"
	wfPublish               = "workflow.publish"
	wfInvoke                = "workflow.invoke"
	wfDelete                = "workflow.delete"
	wfSchedule              = "workflow.schedule"
	wfManageAccess          = "workflow.manage_access"
	wfRunRead               = "run.read"
	wfRunCancel             = "run.cancel"
	wfRunRetry              = "run.retry"
	wfRunApprove            = "approval.decide"
	wfArtifactRead          = "artifact.read"
	wfRunManageParticipants = "run.manage_participants"
)

type workflowPrincipalContextKey struct{}

// withWorkflowPrincipal is for trusted authentication adapters, never request
// JSON. An invalid explicit principal fails closed instead of falling back to
// the single-user operator compatibility path.
func withWorkflowPrincipal(r *http.Request, principal identity.Principal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), workflowPrincipalContextKey{}, principal))
}

func (s *Server) workflowPrincipal(r *http.Request) identity.Principal {
	if principal, ok := r.Context().Value(workflowPrincipalContextKey{}).(identity.Principal); ok {
		return principal
	}
	if caller, ok := identityFrom(r); ok {
		if caller.Local {
			return identity.LocalPrincipal(caller.Scope, caller.Username, caller.Admin)
		}
		// The existing shared-token authenticator returns exactly the zero
		// identity. Non-local named/scoped identities must not inherit it.
		if !s.multiuser() && caller.Username == "" && caller.Scope == (identity.Scope{}) && !caller.Admin {
			return identity.SingleUserPrincipal()
		}
		return identity.Principal{}
	}
	if !s.multiuser() {
		return identity.SingleUserPrincipal()
	}
	return identity.Principal{}
}

func (s *Server) workflowAccessStore(create bool) (*taskqueue.Store, error) {
	if s.mgr == nil || s.mgr.taskRuns == nil {
		return nil, errors.New("工作流运行管理尚未就绪")
	}
	return s.mgr.taskRuns.database(create)
}

func (s *Server) workflowRole(principal identity.Principal) (string, error) {
	if !principal.Valid() {
		return "disabled", nil
	}
	if principal.IsOperator() {
		return "space_admin", nil
	}
	db, err := s.workflowAccessStore(false)
	if err != nil || db == nil {
		return "", err
	}
	return db.AccessRole(principal.ID)
}

func validWorkflowAction(action string) bool {
	return slices.Contains([]string{wfRead, wfEdit, wfPublish, wfInvoke, wfDelete, wfSchedule, wfManageAccess}, action)
}

func grantIncludes(actions []string, action string) bool {
	if slices.Contains(actions, action) {
		return true
	}
	if action == wfRead {
		return slices.Contains(actions, wfEdit) || slices.Contains(actions, wfPublish) || slices.Contains(actions, wfInvoke)
	}
	return false
}

func (s *Server) workflowAllowed(r *http.Request, wid, action string) (bool, error) {
	principal := s.workflowPrincipal(r)
	if !principal.Valid() || !validWorkflowAction(action) {
		return false, nil
	}
	role, err := s.workflowRole(principal)
	if err != nil || role == "disabled" {
		return false, err
	}
	if role == "space_admin" {
		return true, nil
	}
	if wid != "" {
		db, err := s.workflowAccessStore(false)
		if err != nil {
			return false, err
		}
		if db != nil {
			grants, err := db.WorkflowGrant(wid, principal.ID)
			if err != nil {
				return false, err
			}
			if grants != nil {
				return grantIncludes(grants, action), nil
			}
		}
	}
	if principal.Kind != identity.PrincipalLocal {
		return false, nil
	}
	// Existing local users keep access to the workflow catalog and published
	// invocation. A persisted empty grant can explicitly withdraw that access.
	if action == wfRead || action == wfInvoke {
		return true, nil
	}
	return role == "builder" && action == wfEdit, nil
}

func (s *Server) requireWorkflowAction(action string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		allowed, err := s.workflowAllowed(r, r.PathValue("wid"), action)
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, errors.New("暂时无法确认工作流权限"))
			return
		}
		if !allowed {
			writeErr(w, http.StatusForbidden, errors.New("没有执行此工作流操作的权限"))
			return
		}
		next(w, r)
	}
}

func (s *Server) workflowRunAllowed(r *http.Request, rec workflow.RunRecord, action, nodeID string) (bool, error) {
	principal := s.workflowPrincipal(r)
	if !principal.Valid() || !slices.Contains([]string{wfRunRead, wfRunCancel, wfRunRetry, wfRunApprove, wfArtifactRead, wfRunManageParticipants}, action) {
		return false, nil
	}
	role, err := s.workflowRole(principal)
	if err != nil || role == "disabled" {
		return false, err
	}
	if action == wfRunApprove {
		if handled, allowed, err := s.workflowPolicyApprovalAllowed(r, rec, nodeID); handled || err != nil {
			return allowed, err
		}
	}
	if role == "space_admin" {
		return true, nil
	}
	db, err := s.workflowAccessStore(false)
	if err != nil {
		return false, err
	}
	if db == nil {
		return rec.Owner != nil && principal.Scope != nil && *rec.Owner == *principal.Scope, nil
	}
	meta, err := db.GetWorkflowRun(rec.ID)
	if err != nil && !errors.Is(err, taskqueue.ErrNotFound) {
		return false, err
	}
	if err == nil {
		if meta.WorkflowID != rec.WorkflowID {
			return false, nil
		}
		if meta.OwnerID == principal.ID {
			return true, nil
		}
	} else if rec.Owner != nil && principal.Scope != nil && *rec.Owner == *principal.Scope {
		// Only records predating the catalog use the legacy owner fallback.
		return true, nil
	}
	grants, err := db.RunGrant(rec.ID, principal.ID, "")
	if err != nil {
		return false, err
	}
	if slices.Contains(grants, action) {
		return true, nil
	}
	if nodeID != "" {
		grants, err = db.RunGrant(rec.ID, principal.ID, nodeID)
		if err != nil {
			return false, err
		}
		if slices.Contains(grants, action) {
			return true, nil
		}
	}
	// A node-specific reviewer needs to open its run to handle that task.
	// It does not gain cancel/retry or access to unrelated node artifacts.
	if action == wfRunRead {
		rows, err := db.ListRunGrants(rec.ID)
		if err != nil {
			return false, err
		}
		for _, row := range rows {
			if row.PrincipalID == principal.ID && row.NodeID != "" && (slices.Contains(row.Actions, wfRunRead) || slices.Contains(row.Actions, wfRunApprove)) {
				return true, nil
			}
		}
	}
	return false, nil
}

type WorkflowRunCapabilities = workflow.RunCapabilities

func (s *Server) workflowRunCapabilities(r *http.Request, rec workflow.RunRecord) WorkflowRunCapabilities {
	allowed := func(action, node string) bool { ok, _ := s.workflowRunAllowed(r, rec, action, node); return ok }
	_, definitionAvailable := s.mgr.workflows.get(rec.WorkflowID)
	caps := WorkflowRunCapabilities{
		CanRead:               allowed(wfRunRead, ""),
		CanCancel:             allowed(wfRunCancel, "") && (rec.Status == workflow.RunWorking || rec.Status == "queued"),
		CanRetry:              definitionAvailable && allowed(wfRunRetry, "") && (rec.Status == workflow.RunFailed || rec.Status == workflow.RunCancelled || rec.Status == "interrupted"),
		CanReadArtifacts:      allowed(wfArtifactRead, ""),
		CanManageParticipants: allowed(wfRunManageParticipants, ""),
	}
	for nodeID, state := range rec.Nodes {
		if state.Status == workflow.NodeWaitingHuman && allowed(wfRunApprove, nodeID) {
			caps.CanApproveNodes = append(caps.CanApproveNodes, nodeID)
		}
		if !allowed(wfArtifactRead, "") && state.Artifact != "" && allowed(wfArtifactRead, nodeID) {
			caps.CanReadArtifactNodes = append(caps.CanReadArtifactNodes, nodeID)
		}
	}
	sort.Strings(caps.CanApproveNodes)
	sort.Strings(caps.CanReadArtifactNodes)
	if len(caps.CanReadArtifactNodes) != 0 {
		caps.CanReadArtifacts = true
	}
	caps.CanApprove = len(caps.CanApproveNodes) != 0
	return caps
}

type WorkflowCapabilities struct {
	CanManagePublications bool `json:"can_manage_publications"`
	CanEdit               bool `json:"can_edit"`
	CanPublish            bool `json:"can_publish"`
	CanInvoke             bool `json:"can_invoke"`
	CanDelete             bool `json:"can_delete"`
	CanSchedule           bool `json:"can_schedule"`
	CanManageAccess       bool `json:"can_manage_access"`
}

func (s *Server) workflowCapabilities(r *http.Request, wid string) WorkflowCapabilities {
	allowed := func(action string) bool { ok, _ := s.workflowAllowed(r, wid, action); return ok }
	return WorkflowCapabilities{CanManagePublications: s.canManageWorkflowPublications(r, wid), CanEdit: allowed(wfEdit), CanPublish: allowed(wfPublish), CanInvoke: allowed(wfInvoke), CanDelete: allowed(wfDelete), CanSchedule: allowed(wfSchedule), CanManageAccess: allowed(wfManageAccess)}
}

func workflowAccessError(w http.ResponseWriter, err error) {
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("工作流权限存储暂时不可用"))
	}
}

func normalizeAccessActions(actions []string, valid func(string) bool) ([]string, error) {
	if len(actions) > 16 {
		return nil, errors.New("权限项过多")
	}
	out := make([]string, 0, len(actions))
	for _, action := range actions {
		action = strings.TrimSpace(action)
		if !valid(action) {
			return nil, errors.New("包含不支持的权限")
		}
		if !slices.Contains(out, action) {
			out = append(out, action)
		}
	}
	sort.Strings(out)
	return out, nil
}
