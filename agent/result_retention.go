package agent

import (
	"context"
	"sort"
	"time"
)

// Protect the current working set before inference, including host checkpoint
// bindings whose original calls were compacted away. IDs grant no storage scope.
// A lease is temporary protection, not permanent retention or source freshness.
func (a *Agent) refreshResultReferenceLeases(ctx context.Context) {
	if a.registry == nil || a.sources == nil || ctx.Err() != nil || time.Now().Before(a.resultLeaseRefresh) {
		return
	}
	const maximum = 1024
	ids := make([]string, 0, maximum)
	seen := make(map[string]bool)
	add := func(id string) {
		if len(ids) < maximum && sourceResultID(id) && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	// Recent action results take precedence over older checkpoint bookkeeping.
	for i := len(a.messages) - 1; i >= 0 && len(ids) < maximum; i-- {
		for _, block := range a.messages[i].Content {
			add(block.ResultID)
		}
	}
	snapshot := a.sources.sourceSnapshot()
	remaining := make([]string, 0, len(snapshot.bindings))
	for id := range snapshot.bindings {
		remaining = append(remaining, id)
	}
	sort.Strings(remaining)
	for _, id := range remaining {
		add(id)
	}
	if len(ids) == 0 {
		return
	}
	// Missing records remain explicit recovery gaps when read. Lease failures
	// do not turn a source observation into a replayable action or fail a turn.
	err := a.registry.ProtectResultReferences(ctx, ids)
	if ctx.Err() == nil {
		delay := 10 * time.Minute
		if err != nil {
			// A temporary lock/I/O failure must not suppress another attempt
			// for the full success interval. Missing IDs remain recovery gaps.
			delay = time.Minute
		}
		a.resultLeaseRefresh = time.Now().Add(delay)
	}
}
