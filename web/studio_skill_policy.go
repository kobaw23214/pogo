package web

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
	"maps"
	"net/http"
	"slices"
	"strings"
)

// Skill policy updates leave identity, instructions, native tools and MCP fixed.
type studioSkillPolicy struct {
	Skills   *[]string       `json:"skills"`
	Binding  json.RawMessage `json:"binding,omitempty"`
	Lock     json.RawMessage `json:"lock,omitempty"`
	Revision int             `json:"revision"`
}

func (b studioBinding) effectiveSkillBinding() studioBinding {
	if p := b.SkillPolicy; p != nil {
		b.Agent.Skills = p.Skills
		b.Agent.SkillBinding = p.Binding
		b.SkillLock = p.Lock
	}
	return b
}
func (b studioBinding) skillRevision() int {
	if b.SkillPolicy != nil {
		return b.SkillPolicy.Revision
	}
	return b.Agent.Revision
}
func skillPolicyDiffers(b studioBinding, a studioAgent) bool {
	current := b.effectiveSkillBinding().Agent.Skills
	if (current == nil) != (a.Skills == nil) {
		return true
	}
	return current != nil && !slices.Equal(*current, *a.Skills)
}

type studioSkillSync struct {
	Changed         bool `json:"changed"`
	Deferred        bool `json:"deferred"`
	Revision        int  `json:"revision"`
	DesiredRevision int  `json:"desired_revision"`
}

func (s *studioStore) commitSkillPolicy(id string, revision int, p *studioSkillPolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.Bindings[id]
	a, exists := s.data.Agents[b.Agent.ID]
	if !ok || !exists || a.Deleted {
		return ErrNotFound
	}
	if a.Revision != revision {
		return errors.New("Agent 配置已变化，请重试同步 Skill")
	}
	next := s.data
	next.Bindings = maps.Clone(s.data.Bindings)
	b.SkillPolicy = p
	next.Bindings[id] = b
	if err := s.save(next); err != nil {
		return err
	}
	s.data = next
	return nil
}
func (s *Session) SyncStudioSkillPolicy() (studioSkillSync, error) {
	return s.syncStudioSkillPolicy("")
}
func (s *Session) syncStudioSkillPolicy(startingInput string) (studioSkillSync, error) {
	if sealed, err := s.sealedAgentWorkflow(); sealed {
		return studioSkillSync{}, err
	}
	// Global Skill edits precede both manager and session locks. Upload/delete
	// use this same order, so list/eviction cannot close a three-lock cycle.
	s.mgr.studio.runtimeSkillEdits.RLock()
	defer s.mgr.studio.runtimeSkillEdits.RUnlock()
	// Manager snapshots precede Session.mu: list/eviction take Manager.mu first.
	view := s.mgr.skillViews()
	loaded := append([]skills.Skill(nil), s.mgr.Skills()...)
	// Resolve owner-private instruction snapshots before taking Session.mu.
	// Failure is closed: do not replace a currently usable projection with an
	// empty list just because its private store could not be read.
	authored, err := s.mgr.authoredSkills(s.owner, s.studioID)
	if err != nil && s.studioID != "" {
		return studioSkillSync{}, err
	}
	loaded = appendAuthoredSkills(loaded, authored)
	known := make(map[string]skills.Skill, len(loaded))
	for _, sk := range loaded {
		known[skillIDOf(sk)] = sk
	}
	memoryRoots := s.mgr.workspaceFor(s.root).memory.Roots()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.studioID == "" {
		return studioSkillSync{}, ErrNotFound
	}
	b, desired, err := s.mgr.studio.modelPolicyState(s.ID)
	if err != nil {
		return studioSkillSync{}, err
	}
	desired = withAuthoredSelection(desired, authored)
	result := studioSkillSync{Revision: b.skillRevision(), DesiredRevision: desired.Revision}
	if !skillPolicyDiffers(b, desired) && !skillDocumentMetadataDiffers(s.invocationSkills, loaded, desired.Skills) {
		return result, nil
	}
	if s.active != nil || s.compacting || s.BgActive() || (s.inboxStarting != "" && s.inboxStarting != startingInput) {
		result.Deferred = true
		return result, nil
	}
	if s.buildSkillPrompt == nil {
		result.Deferred = true
		return result, nil
	}
	current := b.effectiveSkillBinding()
	reuseRuntime := slices.Equal(runtimeSkillIDs(current.Agent.Skills), runtimeSkillIDs(desired.Skills))
	var lock json.RawMessage
	bindingBytes := desired.SkillBinding
	if reuseRuntime {
		lock = append(json.RawMessage(nil), current.SkillLock...)
		bindingBytes = current.Agent.SkillBinding
	} else if len(desired.SkillBinding) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), s.mgr.cfg.RunTimeout)
		defer cancel()
		if err := s.mgr.prepareRuntimeSkillAgent(ctx, desired); err != nil {
			return result, err
		}
		if err := s.mgr.cfg.SkillRuntime.MaterializeWorkspaceData(desired.SkillBinding, s.cwd); err != nil {
			return result, err
		}
		lock, err = s.mgr.cfg.SkillRuntime.MakeLock(desired.SkillBinding, s.ID)
		if err != nil {
			return result, err
		}
	}
	p := &studioSkillPolicy{Binding: append(json.RawMessage(nil), bindingBytes...), Lock: lock, Revision: desired.Revision}
	if desired.Skills != nil {
		ids := slices.Clone(*desired.Skills)
		p.Skills = &ids
	}
	nextBinding := b
	nextBinding.SkillPolicy = p
	nextBinding = nextBinding.effectiveSkillBinding()
	v := view
	var subset []skills.Skill
	if nextBinding.Agent.Skills != nil {
		subset = []skills.Skill{}
		for _, id := range *nextBinding.Agent.Skills {
			if sk, ok := known[id]; ok {
				subset = append(subset, sk)
			}
		}
	}
	available := loaded
	if subset != nil {
		v = s.mgr.filterSkillViews(v, subset)
		available = subset
	} else {
		v = s.mgr.unregisterSynthTools(v)
		if len(authored) > 0 {
			v = s.mgr.filterSkillViews(v, available)
		}
	}
	if !s.mgr.browserAllowed(s.owner) {
		available = slices.DeleteFunc(slices.Clone(available), func(sk skills.Skill) bool { return sk.BrowserTask })
		v = s.mgr.filterSkillViews(v, available)
	}
	var bound *skillruntime.BoundSession
	var runtimeTools []tools.Tool
	var runtimePrompt string
	if len(p.Binding) > 0 {
		if reuseRuntime && s.runtimeSkill != nil {
			bound, runtimeTools, runtimePrompt = runtimeSkillProjection(s.runtimeSkill)
		} else {
			bound, runtimeTools, runtimePrompt = s.mgr.runtimeSkillTools(nextBinding, s.ID)
		}
		if bound == nil {
			return result, errors.New("Skill 绑定暂不可用，请检查运行环境后重试")
		}
		available = append(append([]skills.Skill(nil), available...), bound.Skills()...)
	}
	projected := append(append([]tools.Tool(nil), v.synthTools...), runtimeTools...)
	// Keep the active Office bridge, including its leases and dirty editor state.
	officeSelected := false
	for _, sk := range available {
		if sk.Name == "office" || sk.Name == "docs" {
			officeSelected = true
			break
		}
	}
	if officeSelected && s.mgr.officeForSession != nil && nextBinding.Agent.Skills != nil {
		var office *tools.Office
		for _, tool := range s.mcpReserved {
			if existing, ok := tool.(*tools.Office); ok {
				office = existing
				break
			}
		}
		if office == nil {
			if link := s.mgr.officeForSession(s.ID, s.cwd, s.owner, s.hub); link != nil {
				office = &tools.Office{Link: link}
				if l, ok := link.(*officeLink); ok {
					l.commandSession = s
					l.commandEnabled = toolSelected(b.effectiveTools(), "bash")
				}
			}
		}
		if office != nil {
			projected = append(projected, office)
		}
	}
	roots := append(append([]string(nil), v.readRoots...), memoryRoots...)
	update := tools.SkillContextUpdate{Previous: s.mcpReserved, Tools: projected, ReadRoots: roots, ChildReadRoots: v.readRoots, SkillReads: skillReadTargets(v.snapshot), SkillPaths: v.pathFlags, ExplicitSkills: nextBinding.Agent.Skills != nil, ReadOnlySkillTools: tools.ReadOnlySynthNames(v.synthTools), Cwd: s.cwd, ProtectionManifest: s.skillProtectionManifest}
	prompt, promptParts := s.buildSkillPrompt(v, runtimePrompt)
	if err := s.agent.ReplaceSkillContext(prompt, update, func() error { return s.mgr.studio.commitSkillPolicy(s.ID, desired.Revision, p) }, promptParts...); err != nil {
		return result, err
	}
	s.policy.replaceSkillReadOnly(s.mcpReserved, projected, v.synthRO)
	s.invocationSkills, s.runtimeSkill, s.mcpReserved = available, bound, projected
	s.skillSnapshot = append([]skillSnapshotEntry(nil), v.snapshot...)
	s.hub.SetOverhead(s.agent.OverheadBreakdown())
	return studioSkillSync{Changed: true, Revision: desired.Revision, DesiredRevision: desired.Revision}, nil
}
func (m *Manager) syncStudioSkills(agentID string) {
	m.mu.Lock()
	var sessions []*Session
	for _, s := range m.sessions {
		if s.studioID == agentID {
			sessions = append(sessions, s)
		}
	}
	m.mu.Unlock()
	for _, s := range sessions {
		_, _ = s.SyncStudioSkillPolicy()
	}
}
func (s *Server) handleStudioSkillSync(w http.ResponseWriter, r *http.Request) {
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
	result, err := sess.SyncStudioSkillPolicy()
	if err != nil {
		status := 400
		if errors.Is(err, ErrNotFound) {
			status = 404
		}
		writeErr(w, status, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (p *Policy) replaceSkillReadOnly(previous, current []tools.Tool, names []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.readOnly = map[string]bool{}
	for _, name := range names {
		p.readOnly[name] = true
	}
	for _, tool := range current {
		if ro, ok := tool.(tools.ReadOnlyCapable); ok && ro.IsReadOnly() {
			p.readOnly[tool.Name()] = true
		}
	}
	for _, tool := range previous {
		delete(p.allowTool, tool.Name())
	}
}

func runtimeSkillIDs(selection *[]string) []string {
	ids := []string{}
	if selection != nil {
		for _, id := range *selection {
			if strings.HasPrefix(id, "rs_") {
				ids = append(ids, id)
			}
		}
	}
	slices.Sort(ids)
	return ids
}

// Instruction bodies are read on demand; their prompt metadata is captured.
// A managed description edit refreshes that metadata at the same idle boundary
// as an explicit selection edit, without changing any Skill identity or grant.
func skillDocumentMetadataDiffers(current, loaded []skills.Skill, selected *[]string) bool {
	type metadata struct {
		name, description string
		hidden, browser   bool
		aliases           string
		revision          string
	}
	old := map[string]metadata{}
	for _, sk := range current {
		if sk.Source != "bundle" {
			old[skillIDOf(sk)] = metadata{sk.Name, sk.Description, sk.DisableModelInvocation, sk.BrowserTask, strings.Join(sk.Aliases, "\x00"), sk.Revision}
		}
	}
	wanted := map[string]bool{}
	if selected != nil {
		for _, id := range *selected {
			wanted[id] = true
		}
	}
	for _, sk := range loaded {
		id := skillIDOf(sk)
		if selected != nil && !wanted[id] {
			continue
		}
		value := metadata{sk.Name, sk.Description, sk.DisableModelInvocation, sk.BrowserTask, strings.Join(sk.Aliases, "\x00"), sk.Revision}
		if prior, ok := old[id]; ok && prior != value || !ok && sk.Source == "authored" {
			return true
		}
		delete(old, id)
	}
	for id := range old {
		if strings.HasPrefix(id, "as_") {
			return true
		}
	}
	return false
}
