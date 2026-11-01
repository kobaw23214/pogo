package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	pogotools "github.com/yosukeno/pogo/tools"
)

// ProbeResult is the diagnostic answer of one probe: what connecting this
// server entry would produce, or why it would not.
type ProbeResult struct {
	OK bool `json:"ok"`
	// Tools are the server-side names that passed the entry's
	// include/exclude filter — what ListTools answered, not the projected
	// wire names: those embed the servers-map key, and a probe body is one
	// entry that may not have one yet.
	Tools []string `json:"tools,omitempty"`
	// Unregistrable names the tools that pass the filter but would not
	// survive the pool's wrapper validation, with the reason — the same skip
	// refresh.go only logs. Listed so "connects and lists N" stops reading as
	// "registers N": those are two different numbers whenever a declaration
	// is invalid, and the gap is otherwise invisible until the card count
	// disappoints.
	Unregistrable  []ProbeToolIssue `json:"unregistrable,omitempty"`
	ServerName     string           `json:"server_name,omitempty"`
	ServerVersion  string           `json:"server_version,omitempty"`
	ProtocolVersion string          `json:"protocol_version,omitempty"`
	Error          string           `json:"error,omitempty"`
}

// ProbeToolIssue is one tool the server reports but the pool would refuse to
// register, and why.
type ProbeToolIssue struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// probeNoCall stands in for the pool's caller during a probe's validation
// pass: registration is being rehearsed, not invoked.
func probeNoCall(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
	return nil, errors.New("a probe never calls tools")
}

// Probe trial-connects one server entry and closes, never touching a pool: a
// probe rehearses a configuration the user is still editing, and a half-typed
// entry must not join the live set.
//
// The connection goes through the conn's own buildTransport, so the security
// shape of the answer is exact — bearer_token_env resolution, header
// expansion, stdio environment sanitization are the pool's code path, not a
// copy of it. What a probe deliberately skips is the *keeping*: no keep-alive
// (nothing to keep alive), no list-changed watch (no catalog to maintain),
// and the tool list is reported raw-but-filtered rather than built into
// MCPTools (see Tools for why).
//
// Failures are the payload, not the error return: the probe exists to answer
// "why doesn't this connect", so ok:false carrying the reason is a successful
// diagnostic. The startup budget is the entry's own StartupTimeout — probing
// must not be more patient than the real connection would be.
func Probe(ctx context.Context, name string, cfg *ServerConfig, logger *slog.Logger) *ProbeResult {
	c := newConn(ctx, name, cfg, logger)
	defer c.cancel()

	transport, err := c.buildTransport()
	if err != nil {
		return &ProbeResult{Error: err.Error()}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "pogo"}, nil)
	sctx, cancel := context.WithTimeout(c.ctx, cfg.StartupTimeout())
	defer cancel()
	cs, err := client.Connect(sctx, transport, nil)
	if err != nil {
		return &ProbeResult{Error: err.Error()}
	}
	defer func() { _ = cs.Close() }()

	out := &ProbeResult{OK: true}
	// Paginated, like refreshCatalog: nothing promises the list fits one page.
	for cursor := ""; ; {
		params := &mcp.ListToolsParams{}
		if cursor != "" {
			params.Cursor = cursor
		}
		res, err := cs.ListTools(sctx, params)
		if err != nil {
			return &ProbeResult{Error: err.Error()}
		}
		for _, t := range res.Tools {
			if !cfg.Allows(t.Name) {
				continue
			}
			out.Tools = append(out.Tools, t.Name)
			// The same validation the pool applies at registration, with the
			// caller stubbed — a rejected declaration here is exactly the one
			// refresh.go would skip with only a WARN log.
			if _, err := pogotools.NewMCPTool(name, t, cfg.ToolTimeout(), probeNoCall); err != nil {
				out.Unregistrable = append(out.Unregistrable, ProbeToolIssue{Name: t.Name, Reason: err.Error()})
			}
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	if ir := cs.InitializeResult(); ir != nil {
		out.ProtocolVersion = ir.ProtocolVersion
		if ir.ServerInfo != nil {
			out.ServerName = ir.ServerInfo.Name
			out.ServerVersion = ir.ServerInfo.Version
		}
	}
	return out
}
