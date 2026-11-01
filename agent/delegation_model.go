package agent

import (
	"fmt"

	"github.com/yosukeno/pogo/tools"
)

// SetDelegationParentModel changes defaults for future children only. Retained
// child records and already-dispatched background tasks keep their own model.
func (a *Agent) SetDelegationParentModel(model string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return fmt.Errorf("cannot change delegation model during an active run")
	}
	tool, ok := a.registry.Get("subagent")
	if !ok {
		return nil
	}
	if child, ok := tool.(*tools.Subagent); ok {
		child.SetParentModel(model)
	}
	return nil
}
