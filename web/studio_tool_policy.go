package web

import (
	"errors"
	"maps"
	"net/http"
	"slices"

	"github.com/yosukeno/pogo/tools"
)

// Native grants move independently of the original identity/instruction/MCP
// snapshot. Nil retains legacy inheritance; an empty list grants no optional tool.
type studioToolPolicy struct {
	Tools             *[]string `json:"tools"`
	SkillToolBindings []string  `json:"skill_tool_bindings,omitempty"`
	Revision          int       `json:"revision"`
}

type studioToolSync struct {
	Changed         bool     `json:"changed"`
	Deferred        bool     `json:"deferred"`
	Revision        int      `json:"revision"`
	DesiredRevision int      `json:"desired_revision"`
	Tools           []string `json:"tools"`
}

func (b studioBinding) effectiveTools() *[]string {
	if b.ToolPolicy != nil {
		return b.ToolPolicy.Tools
	}
	return b.Agent.Tools
}
func (b studioBinding) toolsRevision() int {
	if b.ToolPolicy != nil {
		return b.ToolPolicy.Revision
	}
	return b.Agent.Revision
}
func cloneToolSelection(v *[]string) *[]string {
	if v == nil {
		return nil
	}
	copy := slices.Clone(*v)
	if copy == nil {
		copy = []string{}
	}
	return &copy
}
func toolSelected(selection *[]string, name string) bool {
	return tools.BuiltinSelected(selection, name)
}
func toolPolicyDiffers(b studioBinding, a studioAgent) bool {
	if !slices.Equal(b.effectiveNativeAgent().SkillToolBindings, a.SkillToolBindings) {
		return true
	}
	// A grouped management tool can retain only part of its actions from an
	// older selection. Comparing the visible name alone would miss revocations.
	if !slices.Equal(tools.ShortcutManageActions(b.effectiveTools()), tools.ShortcutManageActions(a.Tools)) {
		return true
	}
	for _, name := range studioNativePolicyNames() {
		if toolSelected(b.effectiveTools(), name) != toolSelected(a.Tools, name) {
			return true
		}
	}
	return false
}
func (s *studioStore) commitToolPolicy(id string, revision int, p *studioToolPolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.Bindings[id]
	a, exists := s.data.Agents[b.Agent.ID]
	if !ok || !exists || a.Deleted || a.Status != "ready" {
		return ErrNotFound
	}
	if a.Revision != revision {
		return errors.New("Agent 配置已变化，请重试同步工具")
	}
	next := s.data
	next.Bindings = maps.Clone(s.data.Bindings)
	b.ToolPolicy = p
	next.Bindings[id] = b
	if err := s.save(next); err != nil {
		return err
	}
	s.data = next
	return nil
}

func (s *Session) SyncStudioToolPolicy() (studioToolSync, error) { return s.syncStudioToolPolicy("") }
func (s *Session) syncStudioToolPolicy(startingInput string) (studioToolSync, error) {
	if sealed, err := s.sealedAgentWorkflow(); sealed {
		return studioToolSync{}, err
	}
	// Device ownership is a manager fact; read it before Session.mu to retain
	// the common Manager -> Session -> Studio lock order.
	browserAllowed := s.mgr.browserAllowed(s.owner)
	nativeSkills := s.mgr.skillCatalogIDs()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.studioID == "" {
		return studioToolSync{}, ErrNotFound
	}
	b, desired, err := s.mgr.studio.modelPolicyState(s.ID)
	if err != nil {
		return studioToolSync{}, err
	}
	desired.Tools = studioBoundNativeSelection(desired, nativeSkills)
	result := studioToolSync{Revision: b.toolsRevision(), DesiredRevision: desired.Revision, Tools: s.agent.NativeToolNames()}
	// Historical discussion-only conversations keep their explicit no-tools runtime.
	if !s.studioWeb || !toolPolicyDiffers(b, desired) {
		return result, nil
	}
	if s.active != nil || s.compacting || s.BgActive() || (s.inboxStarting != "" && s.inboxStarting != startingInput) {
		result.Deferred = true
		return result, nil
	}
	p := &studioToolPolicy{Tools: cloneToolSelection(desired.Tools), SkillToolBindings: slices.Clone(desired.SkillToolBindings), Revision: desired.Revision}
	if err := s.agent.ReplaceNativeSelection(p.Tools, subPaneIf(browserAllowed, s.browser), func() error { return s.mgr.studio.commitToolPolicy(s.ID, desired.Revision, p) }); err != nil {
		return result, err
	}
	s.policy.replaceNativeGrants(b.effectiveTools(), p.Tools)
	s.browserTool = browserAllowed && toolSelected(p.Tools, "browser")
	if !s.browserTool {
		s.browser.Disarm()
	}
	for _, tool := range s.mcpReserved {
		if office, ok := tool.(*tools.Office); ok {
			if link, ok := office.Link.(*officeLink); ok {
				link.commandEnabled = toolSelected(p.Tools, "bash")
			}
		}
	}
	s.hub.SetOverhead(s.agent.OverheadBreakdown())
	result = studioToolSync{Changed: true, Revision: desired.Revision, DesiredRevision: desired.Revision, Tools: s.agent.NativeToolNames()}
	s.hub.Publish(Event{Type: EvToolConfig, ToolConfig: &result})
	return result, nil
}

func (p *Policy) replaceNativeGrants(previous, current *[]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !slices.Equal(tools.ShortcutManageActions(previous), tools.ShortcutManageActions(current)) {
		delete(p.allowTool, tools.ShortcutManageName)
	}
	for _, name := range studioNativePolicyNames() {
		if !tools.IsProtectedBuiltin(name) && toolSelected(previous, name) != toolSelected(current, name) {
			delete(p.allowTool, name)
		}
	}
	if toolSelected(previous, "bash") != toolSelected(current, "bash") {
		p.allowCmd = map[string]bool{}
		// Shell helpers inherit Bash rather than appearing as selectable tools.
		// Revoking and restoring Bash must not restore an old cancellation grant.
		delete(p.allowTool, tools.ShellStopName)
	}
}
func (m *Manager) syncStudioTools(agentID string) {
	m.mu.Lock()
	var sessions []*Session
	for _, s := range m.sessions {
		if s.studioID == agentID {
			sessions = append(sessions, s)
		}
	}
	m.mu.Unlock()
	for _, s := range sessions {
		result, err := s.SyncStudioToolPolicy()
		if err != nil {
			s.hub.Publish(Event{Type: EvError, Error: "工具配置尚未同步：" + err.Error()})
		} else if result.Deferred {
			s.hub.Publish(Event{Type: EvToolConfig, ToolConfig: &result})
		}
	}
}

// Read effective and desired state beside the real live registry, never during
// its persist/apply interval. Inspecting a dormant chat does not load its history.
func (m *Manager) effectiveStudioTools(sid string, binding studioBinding) (studioToolSync, bool) {
	nativeSkills := m.skillCatalogIDs()
	m.mu.Lock()
	live := m.sessions[sid]
	m.mu.Unlock()
	if live != nil {
		live.mu.Lock()
		defer live.mu.Unlock()
	}
	if current, ok := m.studio.binding(sid); ok {
		binding = current
	}
	result := studioToolSync{Revision: binding.toolsRevision(), DesiredRevision: binding.toolsRevision(), Tools: []string{}}
	web := binding.runtime() == studioWebRuntime
	if live != nil {
		result.Tools = live.agent.NativeToolNames()
	} else if web {
		for _, info := range tools.BuiltinRuntimeCatalog() {
			if !info.ContextOnly && toolSelected(studioBoundNativeSelection(binding.effectiveNativeAgent(), nativeSkills), info.Name) {
				result.Tools = append(result.Tools, info.Name)
			}
		}
	}
	if b, desired, err := m.studio.modelPolicyState(sid); err == nil {
		result.DesiredRevision = desired.Revision
		desired.Tools = studioBoundNativeSelection(desired, nativeSkills)
		result.Deferred = web && toolPolicyDiffers(b, desired)
	}
	return result, web
}
func (s *Server) handleStudioToolSync(w http.ResponseWriter, r *http.Request) {
	sid, aid := r.PathValue("sid"), r.PathValue("aid")
	b, ok := s.mgr.studio.binding(sid)
	if !ok || b.Agent.ID != aid || !s.ownsSession(r, sid) {
		writeErr(w, 404, ErrNotFound)
		return
	}
	if s.rejectBackgroundTaskWrite(w, sid) {
		return
	}
	sess, err := s.mgr.Get(sid)
	if err != nil {
		writeErr(w, 404, ErrNotFound)
		return
	}
	result, err := sess.SyncStudioToolPolicy()
	if err != nil {
		code := 400
		if errors.Is(err, ErrNotFound) {
			code = 404
		}
		writeErr(w, code, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}

// Hidden legacy verbs still participate in revoke/sync and remembered approvals.
func studioNativePolicyNames() []string {
	names := []string{}
	for _, info := range tools.BuiltinCatalog() {
		names = append(names, info.Name)
	}
	for _, group := range tools.SkillBindingNames() {
		names = append(names, tools.SkillBindingLegacyTools(group)...)
	}
	return names
}
