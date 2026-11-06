package schedule

import (
	"errors"
	"fmt"
)

var ErrRetiredWorkflow = errors.New("platform workflow schedules are no longer supported")

const (
	TurnLimitUnlimited = "unlimited"
	TurnLimitLimited   = "limited"
	TurnLimitLegacy    = "legacy"
)

// ValidateAgentRunOptions deliberately leaves absent fields unchanged: already
// saved jobs retain their previous cap and standard approval policy.
func ValidateAgentRunOptions(j Job) error {
	if j.WorkflowID != "" {
		return ErrRetiredWorkflow
	}
	if j.AgentID == "" {
		if j.TurnLimitMode != "" || j.ApprovalMode != "" {
			return fmt.Errorf("Agent run options require an Agent binding")
		}
		return nil
	}
	switch j.TurnLimitMode {
	case "", TurnLimitLegacy:
		if j.MaxTurns < 0 || j.MaxTurns > MaxJobTurns {
			return fmt.Errorf("legacy max_turns must be between 0 and %d", MaxJobTurns)
		}
	case TurnLimitUnlimited:
		if j.MaxTurns != 0 {
			return fmt.Errorf("unlimited turn_limit_mode requires max_turns=0")
		}
	case TurnLimitLimited:
		if j.MaxTurns <= 0 {
			return fmt.Errorf("limited turn_limit_mode requires positive max_turns")
		}
	default:
		return fmt.Errorf("turn_limit_mode must be unlimited, limited or legacy")
	}
	switch j.ApprovalMode {
	case "", "auto", "standard", "strict":
	default:
		return fmt.Errorf("approval_mode must be auto, standard or strict")
	}
	return nil
}
