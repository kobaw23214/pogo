package tools

import (
	"encoding/json"
	"errors"
	"fmt"
)

// CommandUnavailableDetails is a host observation, not an exit status from the
// requested command. Keeping execution and retryability explicit prevents a
// capability failure from being treated as a broken script or retried via a
// different executor that would bypass the same required boundary.
type CommandUnavailableDetails struct {
	Code               string `json:"code"`
	Phase              string `json:"phase"`
	RequestExecuted    bool   `json:"request_executed"`
	Retryable          bool   `json:"retryable"`
	Mode               string `json:"mode,omitempty"`
	RequiredProtection string `json:"required_protection"`
	Reason             string `json:"reason"`
	Recovery           string `json:"recovery"`
}

type sandboxUnavailableError struct {
	mode, reason, requiredProtection, recovery string
}

func (e *sandboxUnavailableError) Error() string {
	return fmt.Sprintf("command was not executed: required OS sandbox unavailable (%s=%s; %s). %s",
		EnvSandbox, e.mode, e.reason, e.recovery)
}

func (e *sandboxUnavailableError) diagnostic() CommandUnavailableDetails {
	return CommandUnavailableDetails{
		Code: "SANDBOX_UNAVAILABLE", Phase: "pre_execution", RequestExecuted: false, Retryable: false,
		Mode: e.mode, RequiredProtection: e.requiredProtection, Reason: e.reason, Recovery: e.recovery,
	}
}

func commandUnavailableResult(cause error) (Result, error) {
	var unavailable *sandboxUnavailableError
	var details CommandUnavailableDetails
	switch {
	case errors.As(cause, &unavailable):
		details = unavailable.diagnostic()
	case errors.Is(cause, ErrScopedCommandUnavailable):
		details = CommandUnavailableDetails{
			Code: "SANDBOX_UNAVAILABLE", Phase: "pre_execution", RequestExecuted: false, Retryable: false,
			RequiredProtection: "session_isolation", Reason: ErrScopedCommandUnavailable.Error(),
			Recovery: "Ask the platform administrator to provide a Linux runtime with working bubblewrap and private PID/proc namespaces. Do not repeat the command or use another shell or subagent to bypass session isolation.",
		}
	default:
		return Result{}, cause
	}
	data, _ := json.Marshal(details)
	text := string(data)
	return Result{Text: text, Details: details, Payload: &ResultPayload{Text: text, Structured: data, Format: "json"}}, cause
}
