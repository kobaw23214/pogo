package web

import (
	"errors"
	"fmt"
)

// Capability dimensions apply independently at the same idle boundaries. A
// failed Skill preparation must not prevent a native or MCP grant revocation.
func (s *Session) syncStudioCapabilities(startingInput string) error {
	if s.studioID == "" {
		return nil
	}
	var failures []error
	if _, err := s.syncStudioSkillPolicy(startingInput); err != nil {
		failures = append(failures, fmt.Errorf("Skill 配置尚未同步：%w", err))
	}
	if _, err := s.syncStudioToolPolicy(startingInput); err != nil {
		failures = append(failures, fmt.Errorf("工具配置尚未同步：%w", err))
	}
	if _, err := s.syncStudioMCPPolicy(startingInput); err != nil {
		failures = append(failures, fmt.Errorf("MCP 配置尚未同步：%w", err))
	}
	return errors.Join(failures...)
}

func (s *Session) syncStudioCapabilitiesAtIdle() {
	if err := s.syncStudioCapabilities(""); err != nil {
		s.hub.Publish(Event{Type: EvError, Error: err.Error()})
	}
}
