package agent

// MCPInvocationMethod is the display projection of a method actually retained
// by this Agent's registry. It includes deferred methods; discovering a schema
// and executing it remain the existing separate, reviewed tool operations.
type MCPInvocationMethod struct {
	Server      string `json:"server"`
	Name        string `json:"name"`
	WireName    string `json:"wire_name"`
	Description string `json:"description"`
	// Internal comparison only; a display client cannot pin/approve a definition.
	Fingerprint string `json:"-"`
}

// MCPInvocationMethods never consults a broader deployment catalog. Registry
// selection, name collisions and read-only filtering have already happened.
func (a *Agent) MCPInvocationMethods() []MCPInvocationMethod {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []MCPInvocationMethod{}
	for _, t := range mcpToolsInOrder(a.registry) {
		out = append(out, MCPInvocationMethod{Server: t.ServerName(), Name: t.ServerToolName(), WireName: t.Name(), Description: t.Description(), Fingerprint: t.DefinitionFingerprint()})
	}
	return out
}
