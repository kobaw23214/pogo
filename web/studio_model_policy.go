package web

import (
	"errors"
	"maps"
	"net/http"
	"reflect"
	"slices"

	"github.com/yosukeno/pogo/config"
)

type studioModelPolicy struct {
	DefaultModel string   `json:"default_model"`
	Models       []string `json:"models"`
	Revision     int      `json:"revision"`
}

func (b studioBinding) allowedModels() []string {
	if b.ModelPolicy != nil {
		return slices.Clone(b.ModelPolicy.Models)
	}
	return b.Agent.allowedModels()
}
func (b studioBinding) defaultModel() string {
	if b.ModelPolicy != nil {
		return b.ModelPolicy.DefaultModel
	}
	return b.Agent.Model
}
func (b studioBinding) modelRevision() int {
	if b.ModelPolicy != nil {
		return b.ModelPolicy.Revision
	}
	return b.Agent.Revision
}

type studioModelSync struct {
	Changed         bool     `json:"changed"`
	Deferred        bool     `json:"deferred"`
	Model           string   `json:"model"`
	Models          []string `json:"models"`
	Revision        int      `json:"revision"`
	DesiredRevision int      `json:"desired_revision"`
}

// policyState reads desired/effective state under one registry lock. It does
// not persist the desired policy while an old-model run is still using it.
func (s *studioStore) modelPolicyState(id string) (studioBinding, studioAgent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.data.Bindings[id]
	if !ok {
		return b, studioAgent{}, ErrNotFound
	}
	a, ok := s.data.Agents[b.Agent.ID]
	if !ok || a.Deleted {
		return b, a, ErrNotFound
	}
	return b, a, nil
}

func modelPolicyDiffers(b studioBinding, a studioAgent) bool {
	return b.defaultModel() != a.Model || !slices.Equal(b.allowedModels(), a.allowedModels())
}

func resolveStudioModelPolicy(b studioBinding, a studioAgent, current string) (config.Resolved, error) {
	target := current
	if b.defaultModel() != a.Model || !slices.Contains(a.allowedModels(), current) {
		target = a.Model
	}
	cfg, err := config.Resolve(target)
	return applyStudioEffort(cfg), err
}

func (s *studioStore) syncModelPolicy(id, current string) (studioBinding, config.Resolved, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.Bindings[id]
	if !ok {
		return b, config.Resolved{}, false, ErrNotFound
	}
	a, ok := s.data.Agents[b.Agent.ID]
	if !ok || a.Deleted {
		return b, config.Resolved{}, false, ErrNotFound
	}
	models := a.allowedModels()
	cfg, err := resolveStudioModelPolicy(b, a, current)
	if err != nil {
		return b, cfg, false, err
	}
	changed := modelPolicyDiffers(b, a) || cfg.Model != current
	if b.ModelPolicy != nil && b.modelRevision() == a.Revision && b.CurrentModel == cfg.Model {
		return b, cfg, changed, nil
	}
	b.ModelPolicy = &studioModelPolicy{DefaultModel: a.Model, Models: models, Revision: a.Revision}
	b.CurrentModel = cfg.Model
	next := s.data
	next.Bindings = maps.Clone(s.data.Bindings)
	next.Bindings[id] = b
	if err := s.save(next); err != nil {
		return studioBinding{}, cfg, false, err
	}
	s.data = next
	return b, cfg, changed, nil
}

func (s *Session) SyncStudioModelPolicy() (studioModelSync, error) {
	return s.syncStudioModelPolicy("")
}

func (s *Session) syncStudioModelPolicy(startingInput string) (studioModelSync, error) {
	if sealed, err := s.sealedAgentWorkflow(); sealed {
		return studioModelSync{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.studioID == "" {
		return studioModelSync{}, ErrNotFound
	}
	b, desired, err := s.mgr.studio.modelPolicyState(s.ID)
	if err != nil {
		return studioModelSync{}, err
	}
	result := studioModelSync{Model: s.cfg.Model, Models: b.allowedModels(), Revision: b.modelRevision(), DesiredRevision: desired.Revision}
	candidate, err := resolveStudioModelPolicy(b, desired, s.cfg.Model)
	if err != nil {
		return result, err
	}
	if s.active != nil || s.compacting || (s.inboxStarting != "" && s.inboxStarting != startingInput) {
		result.Deferred = modelPolicyDiffers(b, desired) || !reflect.DeepEqual(candidate, s.cfg)
		s.setModelConfigPendingLocked(result.Deferred)
		return result, nil
	}
	// Confirm the loop really is idle before writing a policy it must enact.
	if err := s.agent.SetBrowserModelVision(s.cfg.SeesImages); err != nil {
		return result, err
	}
	b, cfg, changed, err := s.mgr.studio.syncModelPolicy(s.ID, s.cfg.Model)
	if err != nil {
		return result, err
	}
	s.studioModels = b.allowedModels()
	if !reflect.DeepEqual(cfg, s.cfg) {
		if err := s.applyModelLocked(cfg); err != nil {
			return result, err
		}
		changed = true
	} else if changed {
		// Other open windows must refresh the picker even when only the allowed
		// model list changed. An unchanged sync emits nothing, avoiding a loop.
		s.modelConfigPending = false
		s.hub.Publish(Event{Type: EvModelChanged, Model: s.cfg.Model, Provider: s.cfg.Provider, ContextWindow: s.cfg.ContextWindow})
	}
	s.setModelConfigPendingLocked(false)
	return studioModelSync{Changed: changed, Model: s.cfg.Model, Models: slices.Clone(s.studioModels), Revision: b.modelRevision(), DesiredRevision: b.modelRevision()}, nil
}

func (m *Manager) syncStudioModels(agentID string) {
	m.mu.Lock()
	var sessions []*Session
	for _, s := range m.sessions {
		if s.studioID == agentID {
			sessions = append(sessions, s)
		}
	}
	m.mu.Unlock()
	for _, s := range sessions {
		_, _ = s.SyncStudioModelPolicy()
	}
}

// A capabilities read must not mix a newly committed policy with an old live
// client during the small persist/apply interval. This is read-only and never
// loads a dormant conversation just to inspect it.
func (m *Manager) effectiveStudioModel(sid string, binding studioBinding) (string, []string, int) {
	m.mu.Lock()
	live := m.sessions[sid]
	m.mu.Unlock()
	if live == nil {
		return binding.currentModel(), binding.allowedModels(), binding.modelRevision()
	}
	live.mu.Lock()
	defer live.mu.Unlock()
	if current, ok := m.studio.binding(sid); ok {
		binding = current
	}
	return live.cfg.Model, binding.allowedModels(), binding.modelRevision()
}

func (s *Server) handleStudioModelSync(w http.ResponseWriter, r *http.Request) {
	sid, aid := r.PathValue("sid"), r.PathValue("aid")
	b, ok := s.mgr.studio.binding(sid)
	if !ok || b.Agent.ID != aid || !s.ownsSession(r, sid) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	if s.rejectBackgroundTaskWrite(w, sid) {
		return
	}
	parent, err := s.mgr.Get(sid)
	if err != nil {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	result, err := parent.SyncStudioModelPolicy()
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrNotFound) {
			status = http.StatusNotFound
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
