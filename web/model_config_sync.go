package web

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/yosukeno/pogo/config"
)

type modelConfigSync struct {
	Changed  bool `json:"changed"`
	Deferred bool `json:"deferred"`
}

// SyncModelConfig refreshes the current model's saved configuration without
// changing the chosen model or conversation. Studio allowlists/default changes
// use the same idle boundary, independently of pinned instructions and Skills.
func (s *Session) SyncModelConfig() (modelConfigSync, error) {
	return s.syncModelConfig("")
}

func (s *Session) syncModelConfig(startingInput string) (modelConfigSync, error) {
	if sealed, err := s.sealedAgentWorkflow(); sealed {
		return modelConfigSync{}, err
	}
	if s.studioID != "" {
		result, err := s.syncStudioModelPolicy(startingInput)
		return modelConfigSync{Changed: result.Changed, Deferred: result.Deferred}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return modelConfigSync{}, ErrNotFound
	}
	cfg, err := config.Resolve(s.cfg.Model)
	if err != nil {
		return modelConfigSync{}, err
	}
	changed := !reflect.DeepEqual(cfg, s.cfg)
	if s.active != nil || s.compacting || (s.inboxStarting != "" && s.inboxStarting != startingInput) {
		s.setModelConfigPendingLocked(changed)
		return modelConfigSync{Deferred: changed}, nil
	}
	if !changed {
		s.setModelConfigPendingLocked(false)
		return modelConfigSync{}, nil
	}
	if err := s.applyModelLocked(cfg); err != nil {
		return modelConfigSync{}, err
	}
	return modelConfigSync{Changed: true}, nil
}

// Keep the old effective limit visible while busy. A separate event is needed
// because model_changed is a commit signal, including for unchanged model IDs.
func (s *Session) setModelConfigPendingLocked(pending bool) {
	if s.modelConfigPending == pending {
		return
	}
	s.modelConfigPending = pending
	s.hub.Publish(Event{Type: EvModelConfigPending, ModelConfigPending: pending})
}

// Settings save refreshes every already-loaded conversation, including windows
// other than the one that saved. Dormant chats resolve fresh settings on load.
// Do not hold Manager.mu while taking Session.mu or publishing events.
func (m *Manager) syncModelConfigs() []string {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	failed := 0
	for _, s := range sessions {
		if _, err := s.SyncModelConfig(); err != nil && !errors.Is(err, ErrNotFound) {
			failed++
			s.hub.Publish(Event{Type: EvError, Error: "模型配置尚未同步：" + err.Error()})
		}
	}
	if failed > 0 {
		return []string{fmt.Sprintf("配置已保存，但有 %d 个已有会话尚未同步；请检查所用模型是否仍存在及其连接配置。", failed)}
	}
	return nil
}
