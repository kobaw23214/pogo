package agent

import (
	"fmt"
	"math"

	"github.com/yosukeno/pogo/tools"
)

// ReplaceNativeSelection commits the host policy before changing the live
// registry. The session owner serializes idle/busy and background-child checks.
func (a *Agent) ReplaceNativeSelection(enabled *[]string, childBrowser tools.BrowserLink, persist func() error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return fmt.Errorf("cannot replace tools during an active run")
	}
	next, apply, err := a.registry.WithNativeSelection(enabled, childBrowser)
	if err != nil {
		return err
	}
	if err := persist(); err != nil {
		return err
	}
	apply()
	a.registry = next
	a.resetMCPSelection()
	a.measuredThrough = math.MaxInt
	return nil
}

// NativeToolNames reports registered built-ins, rather than the global catalog.
func (a *Agent) NativeToolNames() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	names := []string{}
	for _, info := range tools.BuiltinRuntimeCatalog() {
		if _, ok := a.registry.Get(info.Name); ok {
			names = append(names, info.Name)
		}
	}
	return names
}
