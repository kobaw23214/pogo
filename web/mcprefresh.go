package web

import (
	"context"
	"fmt"
	"os"

	"github.com/yosukeno/pogo/tools"
)

// refreshMCP runs after Start reserves the session, before RunMessage starts
// its model loop. No session/manager lock is held during network requests.
func (s *Session) refreshMCP(ctx context.Context) {
	if s.studioID != "" && !s.studioWeb {
		return
	}
	pool := s.mgr.poolFor(s.owner)
	if pool == nil {
		return // MCP is disabled for this manager; leave its registry untouched.
	}
	var projected []tools.Tool
	var trusted []string
	if err := pool.Refresh(ctx); err != nil {
		// The last valid catalog remains available while the transport is
		// connected; Status exposes that it is stale and why. Do not discard
		// the user's message or abort unrelated local work over a list failure.
		fmt.Fprintf(os.Stderr, "session %s: MCP catalog refresh: %v\n", s.ID, err)
	}
	// SnapshotServers applies the pinned Studio selection (nil for ordinary
	// sessions and legacy definitions): a refresh follows the pool's live
	// catalogs but can never widen a conversation past the servers its
	// definition revision selected.
	all, grants, _ := pool.SnapshotServers(s.mcpOnly)
	projected, trusted = dedupeMCP(all, grants, s.mcpReserved)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.agent.ReplaceMCP(projected); err != nil {
		s.hub.Publish(Event{Type: EvError, Error: err.Error()})
		return
	}
	s.policy.ReplaceMCP(projected, trusted)
	s.hub.SetOverhead(s.agent.OverheadBreakdown())
}
