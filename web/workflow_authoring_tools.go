package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/tools"
)

// Kept for compatibility with historical host integrations. Platform workflow
// management is no longer an Agent conversation capability.
type workflowToolBackend struct {
	m                  *Manager
	s                  *Session
	owner              *identity.Scope
	agentID, workspace string
}

func (m *Manager) workflowToolBackend(_ *Session) tools.WorkflowBackend {
	return nil
}

func (s *Server) bindWorkflowAuthoringTools() {
	s.mgr.mu.Lock()
	s.mgr.workflowAuthoringHost = s
	s.mgr.mu.Unlock()
}

func (s *Server) unbindWorkflowAuthoringTools() {
	if s.mgr == nil {
		return
	}
	s.mgr.mu.Lock()
	if s.mgr.workflowAuthoringHost == s {
		s.mgr.workflowAuthoringHost = nil
	}
	s.mgr.mu.Unlock()
}

func workflowToolFailure(code, message string, status int) (json.RawMessage, error) {
	raw, err := json.Marshal(map[string]any{"ok": false, "error": map[string]any{"code": code, "message": message, "status": status}})
	return raw, err
}

func (b *workflowToolBackend) WorkflowCall(ctx context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Deny at dispatch as well as discovery: a retained host object, stale
	// transcript, saved grant or administrator-owned Agent cannot bypass this
	// boundary. Human management HTTP handlers keep their existing ACLs.
	return workflowToolFailure("PLATFORM_WORKFLOW_UNAVAILABLE_TO_AGENT", "Agent 对话只能操作当前 Agent 的内部工作流；平台工作流请在管理页面操作", http.StatusForbidden)
}

func (s *Server) workflowToolRequest(ctx context.Context, owner *identity.Scope, action string, raw json.RawMessage) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/manage/workflow-authoring/"+action, strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	if !s.multiuser() {
		if owner != nil {
			return nil, errors.New("unexpected account scope")
		}
		return withWorkflowPrincipal(req, identity.SingleUserPrincipal()), nil
	}
	if owner == nil || !owner.Valid() {
		return nil, errors.New("missing account scope")
	}
	var caller *requestIdentity
	if *owner == identity.LocalScope(bootstrapUser) {
		caller = &requestIdentity{Scope: *owner, Username: bootstrapUser, Local: true, Admin: true}
	}
	s.auth.mu.RLock()
	for _, account := range s.auth.users {
		if scope := identity.LocalScope(account.Username); scope == *owner {
			caller = &requestIdentity{Scope: scope, Username: account.Username, Local: true, Admin: account.Admin || account.Username == bootstrapUser}
			break
		}
	}
	s.auth.mu.RUnlock()
	if caller == nil {
		return nil, errors.New("account removed")
	}
	return req.WithContext(context.WithValue(req.Context(), requestIdentityKey, caller)), nil
}
