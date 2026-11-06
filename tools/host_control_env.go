package tools

import "strings"

// Host control credentials are never model/command inputs, even when an
// administrator adds their names to the ordinary sandbox environment extras.
// The parent Pogo/runner processes retain their own environment unchanged.
func isHostControlSecretEnv(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "POGO_WEB_SERVICE_RUNNER_TOKEN", "POGO_WEB_RUNNER_TOKEN":
		return true
	default:
		return false
	}
}
