package web

import (
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/taskqueue"
)

type workflowAccessSubject struct {
	PrincipalID string `json:"principal_id"`
	DisplayName string `json:"display_name"`
}

type workflowAccessGrantView struct {
	PrincipalID string   `json:"principal_id"`
	DisplayName string   `json:"display_name"`
	NodeID      string   `json:"node_id,omitempty"`
	Actions     []string `json:"actions"`
}

func (s *Server) registerWorkflowAccessRoutes() {
	s.mux.HandleFunc("GET /api/manage/workflows/{wid}/approval-subjects", s.requireWorkflowAction(wfEdit, s.handleWorkflowApprovalSubjects))
	s.mux.HandleFunc("GET /api/manage/workflow-access/roles", s.handleWorkflowAccessRoles)
	s.mux.HandleFunc("PUT /api/manage/workflow-access/roles", s.handleWorkflowAccessRolePut)
	s.mux.HandleFunc("GET /api/manage/workflows/{wid}/access", s.requireWorkflowAction(wfManageAccess, s.handleWorkflowAccessGet))
	s.mux.HandleFunc("PUT /api/manage/workflows/{wid}/access", s.requireWorkflowAction(wfManageAccess, s.handleWorkflowAccessPut))
	s.mux.HandleFunc("GET /api/workflows/{wid}/runs/{rid}/participants", s.handleWorkflowParticipantsGet)
	s.mux.HandleFunc("PUT /api/workflows/{wid}/runs/{rid}/participants", s.handleWorkflowParticipantsPut)
}

// Account names are only offered to a caller who may manage access. Password
// data, operator flags and credentials never accompany this small picker.
func (s *Server) workflowAccessSubjects() []workflowAccessSubject {
	out := []workflowAccessSubject{}
	if !s.multiuser() {
		return append(out, workflowAccessSubject{PrincipalID: identity.SingleUserPrincipal().ID, DisplayName: "本地操作者"})
	}
	s.auth.mu.RLock()
	for name := range s.auth.users {
		principal := identity.LocalPrincipal(identity.LocalScope(name), name, false)
		out = append(out, workflowAccessSubject{PrincipalID: principal.ID, DisplayName: name})
	}
	s.auth.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })
	return out
}

func (s *Server) workflowAccessSubjectKnown(id string) bool {
	for _, subject := range s.workflowAccessSubjects() {
		if subject.PrincipalID == id {
			return true
		}
	}
	return false
}

func validWorkflowAccessSubjectID(id string) bool {
	return strings.TrimSpace(id) != "" && len(id) <= 512 && strings.IndexFunc(id, unicode.IsControl) < 0
}

func (s *Server) workflowAccessGrantViews(grants []taskqueue.AccessGrant) []workflowAccessGrantView {
	names := map[string]string{}
	for _, subject := range s.workflowAccessSubjects() {
		names[subject.PrincipalID] = subject.DisplayName
	}
	out := make([]workflowAccessGrantView, 0, len(grants))
	for _, grant := range grants {
		name := names[grant.PrincipalID]
		if name == "" {
			name = "历史主体"
		}
		out = append(out, workflowAccessGrantView{PrincipalID: grant.PrincipalID, DisplayName: name, NodeID: grant.NodeID, Actions: grant.Actions})
	}
	return out
}

func (s *Server) workflowAccessAdministrator(w http.ResponseWriter, r *http.Request) bool {
	role, err := s.workflowRole(s.workflowPrincipal(r))
	if err != nil {
		workflowAccessError(w, err)
		return false
	}
	if role != "space_admin" {
		writeErr(w, http.StatusForbidden, errors.New("没有管理空间权限的权限"))
		return false
	}
	return true
}

func (s *Server) handleWorkflowAccessRoles(w http.ResponseWriter, r *http.Request) {
	if !s.workflowAccessAdministrator(w, r) {
		return
	}
	roles := []taskqueue.AccessRoleBinding{}
	db, err := s.workflowAccessStore(false)
	if err != nil {
		workflowAccessError(w, err)
		return
	}
	if db != nil {
		roles, err = db.ListAccessRoles()
		if err != nil {
			workflowAccessError(w, err)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"space_id": identity.DefaultSpace, "roles": roles, "subjects": s.workflowAccessSubjects()})
}

func (s *Server) handleWorkflowAccessRolePut(w http.ResponseWriter, r *http.Request) {
	if !s.workflowAccessAdministrator(w, r) {
		return
	}
	var body struct {
		PrincipalID string `json:"principal_id"`
		Role        string `json:"role"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !validWorkflowAccessSubjectID(body.PrincipalID) || (!s.workflowAccessSubjectKnown(body.PrincipalID) && body.Role != "") || !slices.Contains([]string{"", "space_admin", "builder", "user", "disabled"}, body.Role) {
		writeErr(w, http.StatusBadRequest, errors.New("主体或空间角色无效"))
		return
	}
	// Local operator accounts retain their operator grant. Do not accept an
	// apparently successful disabled role which that grant would override.
	for _, subject := range s.workflowAccessSubjects() {
		if subject.PrincipalID == body.PrincipalID && (body.PrincipalID == "single-user" || s.auth.isAdmin(subject.DisplayName)) && body.Role != "" && body.Role != "space_admin" {
			writeErr(w, http.StatusConflict, errors.New("运维管理员保留空间管理员权限"))
			return
		}
	}
	db, err := s.workflowAccessStore(true)
	if err == nil {
		err = db.SetAccessRole(body.PrincipalID, body.Role)
	}
	if err != nil {
		workflowAccessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"principal_id": body.PrincipalID, "role": body.Role})
}

func (s *Server) handleWorkflowAccessGet(w http.ResponseWriter, r *http.Request) {
	wid := r.PathValue("wid")
	if _, ok := s.mgr.workflows.get(wid); !ok {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	grants := []taskqueue.AccessGrant{}
	db, err := s.workflowAccessStore(false)
	if err != nil {
		workflowAccessError(w, err)
		return
	}
	if db != nil {
		grants, err = db.ListWorkflowGrants(wid)
		if err != nil {
			workflowAccessError(w, err)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"grants": s.workflowAccessGrantViews(grants), "subjects": s.workflowAccessSubjects()})
}

func (s *Server) handleWorkflowAccessPut(w http.ResponseWriter, r *http.Request) {
	wid := r.PathValue("wid")
	if _, ok := s.mgr.workflows.get(wid); !ok {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	var body struct {
		PrincipalID string    `json:"principal_id"`
		Actions     *[]string `json:"actions"`
		Inherit     bool      `json:"inherit"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !validWorkflowAccessSubjectID(body.PrincipalID) || (!body.Inherit && body.Actions == nil) || (!s.workflowAccessSubjectKnown(body.PrincipalID) && !body.Inherit && len(*body.Actions) != 0) {
		writeErr(w, http.StatusBadRequest, errors.New("请选择有效主体与权限"))
		return
	}
	actions := []string{}
	var err error
	if !body.Inherit {
		actions, err = normalizeAccessActions(*body.Actions, validWorkflowAction)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	db, err := s.workflowAccessStore(true)
	if err == nil {
		if body.Inherit {
			err = db.DeleteWorkflowGrant(wid, body.PrincipalID)
		} else {
			err = db.SetWorkflowGrant(wid, body.PrincipalID, actions)
		}
	}
	if err != nil {
		workflowAccessError(w, err)
		return
	}
	s.handleWorkflowAccessGet(w, r)
}

func (s *Server) workflowParticipantRecord(w http.ResponseWriter, r *http.Request) (string, bool) {
	rec, ok := s.mgr.wfruns.snapshot(r.PathValue("rid"))
	if !ok || rec.WorkflowID != r.PathValue("wid") {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return "", false
	}
	allowed, err := s.workflowRunAllowed(r, rec, wfRunManageParticipants, "")
	if err != nil {
		workflowAccessError(w, err)
		return "", false
	}
	if !allowed {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return "", false
	}
	return rec.ID, true
}

func (s *Server) handleWorkflowParticipantsGet(w http.ResponseWriter, r *http.Request) {
	rid, ok := s.workflowParticipantRecord(w, r)
	if !ok {
		return
	}
	grants := []taskqueue.AccessGrant{}
	db, err := s.workflowAccessStore(false)
	if err != nil {
		workflowAccessError(w, err)
		return
	}
	if db != nil {
		grants, err = db.ListRunGrants(rid)
		if err != nil {
			workflowAccessError(w, err)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"participants": s.workflowAccessGrantViews(grants), "subjects": s.workflowAccessSubjects()})
}

func (s *Server) handleWorkflowParticipantsPut(w http.ResponseWriter, r *http.Request) {
	rid, ok := s.workflowParticipantRecord(w, r)
	if !ok {
		return
	}
	var body struct {
		PrincipalID string    `json:"principal_id"`
		NodeID      string    `json:"node_id"`
		Actions     *[]string `json:"actions"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !validWorkflowAccessSubjectID(body.PrincipalID) || body.Actions == nil || (!s.workflowAccessSubjectKnown(body.PrincipalID) && len(*body.Actions) != 0) {
		writeErr(w, http.StatusBadRequest, errors.New("请选择有效参与者与权限"))
		return
	}
	body.NodeID = strings.TrimSpace(body.NodeID)
	if body.NodeID != "" {
		rec, _ := s.mgr.wfruns.snapshot(rid)
		found := false
		for _, node := range rec.Definition.Nodes {
			found = found || node.ID == body.NodeID
		}
		if !found {
			writeErr(w, http.StatusBadRequest, errors.New("节点不存在"))
			return
		}
	}
	actions, err := normalizeAccessActions(*body.Actions, func(action string) bool {
		if body.NodeID != "" {
			return slices.Contains([]string{wfRunRead, wfRunApprove, wfArtifactRead}, action)
		}
		return slices.Contains([]string{wfRunRead, wfRunCancel, wfRunRetry, wfRunApprove, wfArtifactRead}, action)
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if slices.Contains(actions, wfRunApprove) {
		rec, _ := s.mgr.wfruns.snapshot(rid)
		allowed, err := s.workflowApprovalGrantAllowed(rec, body.NodeID, body.PrincipalID)
		if err != nil {
			workflowAccessError(w, err)
			return
		}
		if !allowed {
			writeErr(w, http.StatusBadRequest, errors.New("发起人不能被授权审批自己的运行"))
			return
		}
	}
	db, err := s.workflowAccessStore(true)
	if err == nil {
		if _, lookupErr := db.GetWorkflowRun(rid); errors.Is(lookupErr, taskqueue.ErrNotFound) {
			rec, _ := s.mgr.wfruns.snapshot(rid)
			err = db.ImportWorkflowRun(workflowMeta(rec, "legacy", ""))
		} else if lookupErr != nil {
			err = lookupErr
		}
	}
	if err == nil {
		err = db.SetRunGrant(rid, body.PrincipalID, body.NodeID, actions)
	}
	if err != nil {
		workflowAccessError(w, err)
		return
	}
	s.handleWorkflowParticipantsGet(w, r)
}
