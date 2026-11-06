package web

import (
	"errors"
	"maps"
	"net/http"
	"slices"

	"github.com/yosukeno/pogo/tools"
)

// External server grants move at the same idle boundary as native tools and
// Skills. The original Agent snapshot remains an immutable identity record.
// Nil inherits the configured server set; an explicit empty list grants none.
type studioMCPPolicy struct {
	MCPServers *[]string `json:"mcp_servers"`
	Revision   int       `json:"revision"`
}

type studioMCPSync struct {
	Changed         bool     `json:"changed"`
	Deferred        bool     `json:"deferred"`
	Revision        int      `json:"revision"`
	DesiredRevision int      `json:"desired_revision"`
	MCPServers      []string `json:"mcp_servers"`
}

func (b studioBinding) effectiveMCPServers() *[]string {
	if b.MCPPolicy != nil {
		return b.MCPPolicy.MCPServers
	}
	return b.Agent.MCPServers
}

func (b studioBinding) mcpRevision() int {
	if b.MCPPolicy != nil {
		return b.MCPPolicy.Revision
	}
	return b.Agent.Revision
}

func mcpSelectionOnly(selection *[]string) map[string]bool {
	if selection == nil {
		return nil
	}
	only := make(map[string]bool, len(*selection))
	for _, name := range *selection {
		only[name] = true
	}
	return only
}

func mcpSelectionNames(selection *[]string, inherited []string) []string {
	names := slices.Clone(inherited)
	if selection != nil {
		names = slices.Clone(*selection)
	}
	if names == nil {
		names = []string{}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func mcpPolicyDiffers(b studioBinding, desired studioAgent) bool {
	current := b.effectiveMCPServers()
	if (current == nil) != (desired.MCPServers == nil) {
		return true
	}
	return !maps.Equal(mcpSelectionOnly(current), mcpSelectionOnly(desired.MCPServers))
}

func (s *studioStore) commitMCPPolicy(id string, revision int, policy *studioMCPPolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, ok := s.data.Bindings[id]
	desired, exists := s.data.Agents[binding.Agent.ID]
	if !ok || !exists || desired.Deleted || desired.Status != "ready" {
		return ErrNotFound
	}
	if desired.Revision != revision {
		return errors.New("Agent 配置已变化，请重试同步 MCP")
	}
	next := s.data
	next.Bindings = maps.Clone(s.data.Bindings)
	binding.MCPPolicy = policy
	next.Bindings[id] = binding
	if err := s.save(next); err != nil {
		return err
	}
	s.data = next
	return nil
}

func (s *Session) SyncStudioMCPPolicy() (studioMCPSync, error) {
	return s.syncStudioMCPPolicy("")
}

func (s *Session) syncStudioMCPPolicy(startingInput string) (studioMCPSync, error) {
	if sealed, err := s.sealedAgentWorkflow(); sealed {
		return studioMCPSync{}, err
	}
	// Obtain account-owned pool/configuration before Session.mu. Saving a
	// selection uses its current catalog and never waits on remote tools/list.
	pool := s.mgr.poolFor(s.owner)
	inherited := []string{}
	for name, config := range s.mgr.mcpServerConfigs() {
		if config.IsEnabled() {
			inherited = append(inherited, name)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.studioID == "" {
		return studioMCPSync{}, ErrNotFound
	}
	binding, desired, err := s.mgr.studio.modelPolicyState(s.ID)
	if err != nil {
		return studioMCPSync{}, err
	}
	result := studioMCPSync{Revision: binding.mcpRevision(), DesiredRevision: desired.Revision, MCPServers: mcpSelectionNames(binding.effectiveMCPServers(), inherited)}
	if !s.studioWeb || !mcpPolicyDiffers(binding, desired) {
		return result, nil
	}
	if s.active != nil || s.compacting || s.BgActive() || (s.inboxStarting != "" && s.inboxStarting != startingInput) {
		result.Deferred = true
		return result, nil
	}
	policy := &studioMCPPolicy{MCPServers: cloneToolSelection(desired.MCPServers), Revision: desired.Revision}
	only := mcpSelectionOnly(policy.MCPServers)
	var projected []tools.Tool
	var trusted []string
	if pool != nil {
		all, grants, _ := pool.SnapshotServers(only)
		projected, trusted = dedupeMCP(all, grants, s.mcpReserved)
	}
	if err := s.agent.ReplaceMCPWithCommit(projected, func() error { return s.mgr.studio.commitMCPPolicy(s.ID, desired.Revision, policy) }); err != nil {
		return result, err
	}
	s.mcpOnly = only
	s.policy.ReplaceMCP(projected, trusted)
	s.hub.SetOverhead(s.agent.OverheadBreakdown())
	result = studioMCPSync{Changed: true, Revision: desired.Revision, DesiredRevision: desired.Revision, MCPServers: mcpSelectionNames(policy.MCPServers, inherited)}
	s.hub.Publish(Event{Type: EvCapabilitiesChanged})
	return result, nil
}

func (m *Manager) syncStudioMCPs(agentID string) {
	m.mu.Lock()
	var sessions []*Session
	for _, s := range m.sessions {
		if s.studioID == agentID {
			sessions = append(sessions, s)
		}
	}
	m.mu.Unlock()
	for _, s := range sessions {
		result, err := s.SyncStudioMCPPolicy()
		if err != nil {
			s.hub.Publish(Event{Type: EvError, Error: "MCP 配置尚未同步：" + err.Error()})
		} else if result.Deferred {
			s.hub.Publish(Event{Type: EvCapabilitiesChanged})
		}
	}
}

func (s *Server) handleStudioMCPSync(w http.ResponseWriter, r *http.Request) {
	sid, aid := r.PathValue("sid"), r.PathValue("aid")
	binding, ok := s.mgr.studio.binding(sid)
	if !ok || binding.Agent.ID != aid || !s.ownsSession(r, sid) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	if s.rejectBackgroundTaskWrite(w, sid) {
		return
	}
	sess, err := s.mgr.Get(sid)
	if err != nil {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	result, err := sess.SyncStudioMCPPolicy()
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrNotFound) {
			status = http.StatusNotFound
		}
		writeErr(w, status, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}
