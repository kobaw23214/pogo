package web

import (
	"github.com/yosukeno/pogo/schedule"
	"strings"
)

func scheduleOptionValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

// Creation has new explicit defaults. Omitted PATCH options preserve the saved
// contract, including absent legacy fields, rather than granting new authority.
func (b *scheduleJobBody) normalizeAgentRunOptions(old *schedule.Job) error {
	turns := scheduleOptionValue(b.TurnLimitMode)
	approval := scheduleOptionValue(b.ApprovalMode)
	if old != nil {
		if b.TurnLimitMode == nil {
			turns = old.TurnLimitMode
		}
		if b.ApprovalMode == nil {
			approval = old.ApprovalMode
		}
	} else if b.AgentID != "" {
		if b.TurnLimitMode == nil {
			turns = schedule.TurnLimitUnlimited
			if b.MaxTurns > 0 {
				turns = schedule.TurnLimitLimited
			}
		}
		if b.ApprovalMode == nil {
			approval = string(ModeAuto)
		}
	}
	b.TurnLimitMode, b.ApprovalMode = &turns, &approval
	return schedule.ValidateAgentRunOptions(schedule.Job{AgentID: b.AgentID, MaxTurns: b.MaxTurns, TurnLimitMode: turns, ApprovalMode: approval})
}
