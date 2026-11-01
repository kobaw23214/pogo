package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	pogotools "github.com/yosukeno/pogo/tools"
)

const (
	// CatalogRefreshTTL limits polling across all sessions using a pool. List
	// change notifications invalidate it; unsuccessful attempts also consume it.
	CatalogRefreshTTL = 5 * time.Second
	// CatalogRefreshTimeout bounds the entire pre-run check, including waiting
	// behind another refresh. Different servers are checked concurrently.
	CatalogRefreshTimeout = 3 * time.Second
)

// Refresh checks the current catalogs before a new run. It never calls a tool
// or replays a call. A failing server retains its last valid catalog and records
// the error in Status; the caller can still snapshot the other servers.
func (p *Pool) Refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, CatalogRefreshTimeout)
	defer cancel()
	p.mu.Lock()
	conns := make([]*conn, 0, len(p.conns))
	for _, c := range p.conns {
		conns = append(conns, c)
	}
	p.mu.Unlock()
	var wg sync.WaitGroup
	errs := make(chan error, len(conns))
	for _, c := range conns {
		wg.Go(func() {
			// Only the initial attempt is waited for. Reconnection remains the
			// keeper's job, with its existing backoff and startup budget.
			select {
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			case <-c.first:
			}
			if err := c.refreshCurrent(ctx); err != nil {
				errs <- fmt.Errorf("mcp server %q: %w", c.name, err)
			}
		})
	}
	wg.Wait()
	close(errs)
	var failures []error
	for err := range errs {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (c *conn) refreshCurrent(ctx context.Context) error {
	cs := c.session.Load()
	if cs == nil {
		return nil // no usable transport; the keeper will pull on reconnect
	}
	return c.refresh(ctx, cs, true)
}

func (c *conn) refresh(ctx context.Context, cs *mcp.ClientSession, ifStale bool) error {
	select {
	case c.refreshGate <- struct{}{}:
		defer func() { <-c.refreshGate }()
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
	if c.session.Load() != cs || c.ctx.Err() != nil {
		return fmt.Errorf("connection changed during catalog refresh")
	}
	version := c.listVersion.Load()
	c.mu.Lock()
	if ifStale && time.Since(c.lastAttempt) < CatalogRefreshTTL && version == c.listedVersion {
		c.mu.Unlock()
		return nil
	}
	c.lastAttempt = time.Now()
	// A notification is consumed by the attempt, including a failed one. Further
	// runs must respect the failure TTL instead of hammering an unhealthy server.
	c.listedVersion = version
	previous := c.tools
	previousSession := c.catalogSession
	c.mu.Unlock()

	specs, err := listToolCatalog(ctx, cs)
	if err != nil {
		c.mu.Lock()
		if c.session.Load() == cs {
			c.lastErr = "tool list refresh failed: " + err.Error()
			c.catalogStale = true
		}
		c.mu.Unlock()
		return err
	}
	// The session's instructions freeze into each wrapper built below, so a
	// tool snapshot and the instructions snapshot always describe the same
	// session. Read from the session, not the conn: the initial catalog pull
	// runs before setConnected has recorded the handshake.
	instructions := ""
	if ir := cs.InitializeResult(); ir != nil {
		instructions = ir.Instructions
	}
	old := make(map[string]*pogotools.MCPTool, len(previous))
	if previousSession == cs {
		for _, t := range previous {
			old[t.Name()] = t
		}
	}
	var next []*pogotools.MCPTool
	wireNames := make(map[string]bool)
	for _, spec := range specs {
		if !c.cfg.Allows(spec.Name) {
			continue
		}
		var mt *pogotools.MCPTool
		mt, err = pogotools.NewMCPTool(c.name, spec, c.cfg.ToolTimeout(),
			func(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
				return c.callBound(ctx, cs, mt, name, args)
			})
		if err != nil {
			c.logger.Warn("mcp tool skipped", "server", c.name, "tool", spec.Name, "error", err)
			continue
		}
		// Attach after construction but before the closure-captured variable
		// settles into the catalog: callBound's pointer identity must compare
		// against the very pointer stored in next, which is this clone.
		mt = mt.WithServerInstructions(instructions)
		transport := "streamable"
		if c.cfg.Command != "" {
			transport = "stdio"
		} else if c.cfg.Transport == "sse" {
			transport = "sse"
		}
		mt = mt.WithTransport(transport)
		if wireNames[mt.Name()] {
			c.logger.Warn("mcp tool wire name collision; skipped", "server", c.name, "tool", spec.Name)
			continue
		}
		wireNames[mt.Name()] = true
		if prev := old[mt.Name()]; prev != nil && prev.DefinitionFingerprint() == mt.DefinitionFingerprint() {
			// Preserve bindings and remembered approvals for unchanged entries.
			mt = prev
		}
		next = append(next, mt)
	}
	if len(next) > warnServerTools {
		c.logger.Warn("mcp server exposes many tools", "server", c.name, "tools", len(next), "hint", "narrow include_tools")
	}
	c.mu.Lock()
	if c.session.Load() != cs || c.ctx.Err() != nil || ctx.Err() != nil {
		c.mu.Unlock()
		return fmt.Errorf("connection changed or deadline expired during catalog refresh")
	}
	c.tools, c.catalogSession = next, cs
	c.catalogUpdated, c.catalogStale, c.lastErr = time.Now(), false, ""
	c.mu.Unlock()
	c.auditCatalogDiff(previous, next)
	return nil
}

// listToolCatalog publishes nothing until every page succeeds. Repeated cursors
// and malformed entries are failures, never a successful empty catalog.
func listToolCatalog(ctx context.Context, cs *mcp.ClientSession) ([]*mcp.Tool, error) {
	var specs []*mcp.Tool
	cursors, names := map[string]bool{}, map[string]bool{}
	for cursor := ""; ; {
		if cursors[cursor] {
			return nil, fmt.Errorf("tools/list repeated cursor %q", cursor)
		}
		cursors[cursor] = true
		res, err := cs.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		if res == nil {
			return nil, fmt.Errorf("tools/list returned no result")
		}
		for _, spec := range res.Tools {
			if spec == nil || spec.Name == "" {
				return nil, fmt.Errorf("tools/list returned an unnamed tool")
			}
			if names[spec.Name] {
				return nil, fmt.Errorf("tools/list returned duplicate tool %q", spec.Name)
			}
			names[spec.Name] = true
			specs = append(specs, spec)
		}
		if res.NextCursor == "" {
			return specs, nil
		}
		cursor = res.NextCursor
	}
}

func (c *conn) callBound(ctx context.Context, cs *mcp.ClientSession, binding *pogotools.MCPTool, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	c.mu.Lock()
	current := c.session.Load()
	valid := false
	for _, t := range c.tools {
		if t == binding {
			valid = true
			break
		}
	}
	c.mu.Unlock()
	if current == nil || c.ctx.Err() != nil {
		return nil, fmt.Errorf("mcp server %q is reconnecting or closed; tool call was not sent", c.name)
	}
	if current != cs || !valid {
		return nil, fmt.Errorf("mcp tool %q on server %q changed or was removed, or its connection was replaced; start a new run to use the latest catalog; tool call was not sent", name, c.name)
	}
	// Use the captured session exactly once. A concurrent disconnect may fail
	// this call, but must never redirect it or replay a possible side effect.
	params := &mcp.CallToolParams{Name: name, Arguments: args}
	injectTraceMeta(ctx, params)
	return cs.CallTool(ctx, params)
}
