package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

const studioFormatVersion = 2

// resetFormat starts a new execution registry. It does not translate prior
// capability formats or reopen old conversations with changed permissions.
// Agent identity is retained as a draft so administrators can choose Skills anew.
func (s *studioStore) resetFormat(raw []byte) error {
	var previous struct {
		Agents   map[string]json.RawMessage `json:"agents"`
		Bindings map[string]json.RawMessage `json:"bindings"`
	}
	if err := json.Unmarshal(raw, &previous); err != nil {
		return err
	}
	next := studioData{Version: studioFormatVersion, Agents: map[string]studioAgent{}, Bindings: map[string]studioBinding{}}
	for id, rawAgent := range previous.Agents {
		var a studioAgent
		if err := json.Unmarshal(rawAgent, &a); err != nil {
			return err
		}
		if !validID(id) || id != a.ID || a.Revision < 1 {
			return errors.New("invalid Agent identity during registry reset")
		}
		emptySkills, emptyMCP := []string{}, []string{}
		a.Skills, a.MCPServers = &emptySkills, &emptyMCP
		a.SkillBinding = nil
		a.Status, a.ConversationCount = "draft", 0
		a.Revision++
		a.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		next.Agents[id] = a
	}
	for id := range previous.Bindings {
		if !validID(id) {
			return errors.New("invalid conversation ID during registry reset")
		}
		next.ResetSessions = append(next.ResetSessions, id)
		next.ResetShares = append(next.ResetShares, id)
	}
	sort.Strings(next.ResetSessions)
	sort.Strings(next.ResetShares)
	if err := s.save(next); err != nil {
		return err
	}
	s.data = next
	return nil
}

// Cleanup is journaled before any HTTP handlers or background jobs start.
func (m *Manager) finishStudioReset() error {
	ids := m.studio.data.ResetSessions
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if !validID(id) {
			return errors.New("invalid reset conversation ID")
		}
		for i := 0; i < 2; i++ {
			if err := m.Delete(id); err != nil && !errors.Is(err, ErrNotFound) {
				return fmt.Errorf("delete conversation %s: %w", id, err)
			}
		}
		if m.cfg.SkillRuntime != nil {
			if err := m.cfg.SkillRuntime.PurgeSessionData(id); err != nil {
				return err
			}
		}
	}
	next := m.studio.data
	next.ResetSessions = nil
	if err := m.studio.save(next); err != nil {
		return err
	}
	m.studio.data = next
	return nil
}

func (m *Manager) finishStudioShareReset(shares *shareStore) error {
	m.studio.mu.Lock()
	defer m.studio.mu.Unlock()
	ids := m.studio.data.ResetShares
	if len(ids) == 0 {
		return nil
	}
	if err := shares.removeSessions(ids); err != nil {
		return err
	}
	next := m.studio.data
	next.ResetShares = nil
	if err := m.studio.save(next); err != nil {
		return err
	}
	m.studio.data = next
	return nil
}
