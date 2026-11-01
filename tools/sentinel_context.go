package tools

import "fmt"

// WithSentinelEvent exposes the host-created decision tool only for a genuine
// event execution. The caller must derive enabled from a trusted run binding,
// never from model input or a saved tool-selection string. No other grant or
// tool instance is changed. The backend still validates the actual live batch.
func (r *Registry) WithSentinelEvent(enabled bool) (*Registry, error) {
	decision, available := r.nativeTools[WatchDecideName]
	if enabled && (r.readOnly || !available) {
		return nil, fmt.Errorf("Sentinel event decision host is unavailable")
	}
	current, registered := r.byName[WatchDecideName]
	if r.sentinelEvent == enabled && ((!enabled && !registered) || enabled && current == decision) {
		return r, nil
	}
	base := make([]Tool, 0, len(r.order)+1)
	start := 0
	for i, tool := range r.order {
		if i == r.mcpStart {
			start = len(base)
		}
		if tool.Name() != WatchDecideName {
			base = append(base, tool)
		}
	}
	if r.mcpStart >= len(r.order) {
		start = len(base)
	}
	if enabled {
		base = append(append(append([]Tool(nil), base[:start]...), decision), base[start:]...)
		start++
	}
	next := NewRegistry(base...)
	next.mcpStart, next.readOnly, next.spillDir = start, r.readOnly, r.spillDir
	next.results, next.scopedOutputRoot, next.nativeTools = r.results, r.scopedOutputRoot, r.nativeTools
	next.resultDelivery = CloneResultDeliveryPolicy(r.resultDelivery)
	next.sentinelEvent = enabled
	return next, nil
}
