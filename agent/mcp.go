package agent

import (
	"fmt"

	"github.com/yosukeno/pogo/tools"
)

// ReplaceMCP replaces only the external tools and rebuilds their model
// declarations from the very same registry. Like SetClient, the owning driver
// must serialize this with RunMessage and compaction; an active loop refuses
// the change. History, usage, citations and stateful built-ins are preserved.
//
// The layered-discovery selection set resets with the snapshot: names chosen
// against the old definitions are never implicitly retained against new ones.
// The view itself is rebuilt by rebuildMCPView, which also re-estimates the
// fixed cost and invalidates the provider measurement whenever the visible
// definitions change — even when the byte total happens to stay identical.
func (a *Agent) ReplaceMCP(projected []tools.Tool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return fmt.Errorf("cannot replace MCP tools during an active run")
	}
	a.registry = a.registry.WithMCP(projected)
	a.resetMCPSelection()
	return nil
}
