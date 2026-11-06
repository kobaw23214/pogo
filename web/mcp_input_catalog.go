package web

import (
	"net/http"
	"slices"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

// mcpMethodsForRequest avoids creating empty conversations merely to complete
// a first-message draft. Once a conversation exists its actual registry is
// authoritative, including deferred schemas and old policy retained while busy.
func (s *Server) mcpMethodsForRequest(r *http.Request) ([]agent.MCPInvocationMethod, error) {
	if sid := r.URL.Query().Get("session_id"); sid != "" {
		if !validID(sid) || !s.ownsSession(r, sid) {
			return nil, ErrNotFound
		}
		if aid := r.URL.Query().Get("agent_id"); aid != "" {
			b, ok := s.mgr.studio.binding(sid)
			if !ok || b.Agent.ID != aid {
				return nil, ErrNotFound
			}
		}
		sess, err := s.mgr.Get(sid)
		if err != nil {
			return nil, ErrNotFound
		}
		sess.refreshMCPInputCatalog()
		return validMCPInvocationMethods(sess.agent.MCPInvocationMethods()), nil
	}
	var owner *identity.Scope
	if scope, ok := s.ownerScope(r); ok {
		owner = &scope
	}
	var definition *studioAgent
	if aid := r.URL.Query().Get("agent_id"); aid != "" {
		a, ok := s.mgr.studio.get(aid)
		if !ok || a.Deleted || a.Status != "ready" {
			return nil, ErrNotFound
		}
		definition = &a
	}
	return s.mgr.draftMCPInvocationMethods(owner, definition), nil
}

// A session can have been created before its MCP connections finished starting.
// Completion reconciles only the already cached catalog at an idle boundary;
// it never performs tools/list or mutates an executing/compacting snapshot.
func (s *Session) refreshMCPInputCatalog() {
	if s.agent == nil || s.studioID != "" && !s.studioWeb {
		return
	}
	pool := s.mgr.poolFor(s.owner)
	if pool == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.active != nil || s.compacting || s.BgActive() || s.inboxStarting != "" {
		return
	}
	all, trusted, _ := pool.SnapshotServers(s.mcpOnly)
	projected, trusted := dedupeMCP(all, trusted, s.mcpReserved)
	wanted := []agent.MCPInvocationMethod{}
	for _, t := range projected {
		if mt, ok := t.(*tools.MCPTool); ok {
			wanted = append(wanted, agent.MCPInvocationMethod{Server: mt.ServerName(), Name: mt.ServerToolName(), WireName: mt.Name(), Description: mt.Description(), Fingerprint: mt.DefinitionFingerprint()})
		}
	}
	if slices.Equal(s.agent.MCPInvocationMethods(), wanted) {
		return // keep already selected executable schemas when nothing changed
	}
	if err := s.agent.ReplaceMCP(projected); err != nil {
		return // a concurrent Agent reservation keeps its original snapshot
	}
	s.policy.ReplaceMCP(projected, trusted)
	s.hub.SetOverhead(s.agent.OverheadBreakdown())
}

// The draft projection is also used when validating a future scheduled input,
// which deliberately has no Agent loop or newly created workspace yet.
func (m *Manager) draftMCPInvocationMethods(owner *identity.Scope, definition *studioAgent) []agent.MCPInvocationMethod {
	var only map[string]bool
	v := m.skillViews()
	if definition != nil {
		subset, selection := m.studioSelection(*definition)
		only = selection
		if subset == nil {
			v = m.unregisterSynthTools(v)
		} else {
			v = m.filterSkillViews(v, subset)
		}
	}
	projected, _ := m.mcpProjectionFor(v.synthTools, owner, only)
	// Build a metadata-only registry projection: it does not instantiate host
	// tools, bind a Skill runtime, write a workspace, or contact an MCP server.
	registry := tools.NewRegistry(v.synthTools...).WithMCP(projected)
	methods := []agent.MCPInvocationMethod{}
	for _, t := range registry.All() {
		if mt, ok := t.(*tools.MCPTool); ok {
			methods = append(methods, agent.MCPInvocationMethod{Server: mt.ServerName(), Name: mt.ServerToolName(), WireName: mt.Name(), Description: mt.Description()})
		}
	}
	return validMCPInvocationMethods(methods)
}

func validMCPInvocationMethods(methods []agent.MCPInvocationMethod) []agent.MCPInvocationMethod {
	out := []agent.MCPInvocationMethod{}
	for _, method := range methods {
		if skills.ValidInputMCPReference(method.Server, method.Name) {
			out = append(out, method)
		}
	}
	return out
}
