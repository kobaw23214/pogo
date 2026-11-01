package agent

import (
	"fmt"
	"math"
)

// SetSentinelEvent is a host-only, between-runs context switch. It neither
// changes saved Agent grants nor supplies a missing decision backend.
func (a *Agent) SetSentinelEvent(enabled bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return fmt.Errorf("cannot change Sentinel event context during an active run")
	}
	next, err := a.registry.WithSentinelEvent(enabled)
	if err != nil {
		return err
	}
	if next == a.registry {
		return nil
	}
	a.registry = next
	a.resetMCPSelection()
	a.measuredThrough = math.MaxInt
	return nil
}
