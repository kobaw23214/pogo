package web

// MCP client configuration API: GET/PUT /api/mcp, GET /api/mcp/status and
// POST /api/mcp/probe — the editing surface for mcp.json, the file that
// connects this agent to *other* MCP servers (mcpclient/). Not to be confused
// with the bare /mcp route, which is the opposite direction: other agents
// connecting to this one.
//
// mcpclient owns the file and its semantics; this file only adapts them to
// HTTP, and it follows prefsapi's one rule: a config file that cannot be read
// never blocks the editor — GET reports the error alongside an empty config,
// and PUT (which validates, then reconciles the pool) is also how a broken
// file gets fixed. Same trust bar as the rest of /api: the token holder can
// already point bash at any host.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/yosukeno/pogo/mcpclient"
)

// mcpPath is where this server's mcp.json lives: the store wired in serveWeb,
// with mcpclient.Path() as the same default the CLI resolves.
func (s *Server) mcpPath() string {
	if s.opts.MCPClientStore != "" {
		return s.opts.MCPClientStore
	}
	return mcpclient.Path()
}

// mcpView is what GET /api/mcp answers: the file's raw content beside the
// pool's live per-server status and the server side's effective state
// (mcpserve.go). load_error carries the parse failure when the file will not
// load — the dialog renders the section anyway (with an empty config to
// edit), because a broken file is precisely when the user needs the editor.
type mcpView struct {
	Path      string             `json:"path"`
	Config    mcpclient.Config   `json:"config"`
	LoadError string             `json:"load_error,omitempty"`
	Status    []mcpclient.Status `json:"status"`
	// ServerState is the MCP server side's effective configuration. Absent
	// only when the layer is absent too (no store, no static options — a
	// test arrangement, never production).
	ServerState *ServeState `json:"server_state,omitempty"`
	// ServeCatalog is the picker inventory for the server side's exposure:
	// the builtin agent_* tools, the skill manifest tools, and every pooled
	// server's tools under the <server>.<tool> names a forwarding whitelist
	// entry would match. Same presence rule as ServerState.
	ServeCatalog *mcpServeCatalog `json:"serve_catalog,omitempty"`
}

// mcpServeCatalog is serve_catalog's shape: four families, each row named
// exactly as tools/list and include_tools know it, so the whitelist editor
// ticks rows instead of typing strings.
type mcpServeCatalog struct {
	Builtin []mcpServeBuiltinTool `json:"builtin"`
	Skills  []mcpServeSkillTool   `json:"skills"`
	Servers []mcpServeServer      `json:"servers"`
	Runtime []mcpServeRuntimePkg  `json:"runtime"`
}

type mcpServeBuiltinTool struct {
	Name  string `json:"name"`
	Title string `json:"title,omitempty"`
}

type mcpServeSkillTool struct {
	Name       string `json:"name"`
	Title      string `json:"title,omitempty"`
	Registered bool   `json:"registered"`
}

// mcpServeRuntimePkg is one installed runtime Skill package as the picker
// lists it: identity and state, plus one row per contract tool the package
// exposes under its skill_* name once whitelisted. Revoked versions keep
// their row (state says so, tools is empty), like a disconnected server's.
type mcpServeRuntimePkg struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	PackageName string                `json:"package_name"`
	Version     string                `json:"version"`
	State       string                `json:"state"`
	Tools       []mcpServeRuntimeTool `json:"tools"`
}

// mcpServeRuntimeTool is one contract's tool row: name is the exposed
// skill_* name an include_tools entry matches, contract the honest identity.
type mcpServeRuntimeTool struct {
	Name            string `json:"name"`
	Contract        string `json:"contract"`
	Description     string `json:"description,omitempty"`
	ReadOnly        bool   `json:"read_only"`
	FileInputs      bool   `json:"file_inputs"`
	WritesArtifacts bool   `json:"writes_artifacts"`
}

type mcpServeServer struct {
	Name      string               `json:"name"`
	State     string               `json:"state"`
	LastError string               `json:"last_error,omitempty"`
	Tools     []mcpServeForwardRow `json:"tools"`
}

// mcpServeForwardRow is one external tool as this server would forward it:
// name is the exposed <server>.<tool> form an include_tools entry matches,
// remote the name the owning server knows the tool by.
type mcpServeForwardRow struct {
	Name        string `json:"name"`
	Remote      string `json:"remote"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	ReadOnly    bool   `json:"read_only,omitempty"`
}

func (s *Server) mcpSnapshot() mcpView {
	v := mcpView{Path: s.mcpPath(), Status: s.mcpStatus()}
	c, err := mcpclient.Load(v.Path)
	if err != nil {
		v.LoadError = err.Error()
	} else {
		v.Config = c
	}
	if s.mcp != nil {
		st := s.mcp.serveState()
		v.ServerState = &st
		v.ServeCatalog = s.mcp.serveCatalog()
	}
	return v
}

// mcpStatus is the pool's per-server projection; an absent pool (the feature
// off) is an empty list, not an error — the file is still editable.
func (s *Server) mcpStatus() []mcpclient.Status {
	status := []mcpclient.Status{}
	if s.mgr.mcpPool != nil {
		if _, _, st := s.mgr.mcpPool.Snapshot(); st != nil {
			status = st
		}
	}
	// Accounts with their own credentials surface their pools alongside the
	// shared ones, prefixed by owner (see ownerMCPStatus).
	return append(status, s.mgr.ownerMCPStatus()...)
}

func (s *Server) handleGetMCPClient(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.mcpSnapshot())
}

func (s *Server) handleMCPClientStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": s.mcpStatus()})
}

// handleSaveMCPClient replaces mcp.json wholesale. Decoding is strict for the
// same reason the file load is — this file names commands to run and hosts to
// call, so an unknown field is a misspelling the user believes is in effect
// (config/file.go's stance). The saved configuration reconciles into the pool
// immediately; connections are the pool's background business, so the answer
// does not wait on them — status shows the new servers as connecting.
func (s *Server) handleSaveMCPClient(w http.ResponseWriter, r *http.Request) {
	rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var c mcpclient.Config
	dec := json.NewDecoder(bytes.NewReader(rawBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid mcp.json: %w", err))
		return
	}
	if err := c.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := mcpclient.Save(s.mcpPath(), c); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// The conns Reconcile spawns derive their lifetime from this context, so
	// it cannot be the request's — a conn parented on r.Context() would die
	// with the response that created it. The pool's own Close (Manager.Close,
	// at server shutdown) is their deadline instead.
	// ReconcileMCP fans the new base out to the shared pool and every
	// per-account pool, so an editor save reaches all owners at once.
	s.mgr.ReconcileMCP(context.Background(), c)
	// The server section hot-applies: enabled, token and policy take effect
	// on the next request; the caps are construction-fixed, and serveState
	// flags their drift as restart_required (see mcpserve.go).
	if s.mcp != nil {
		s.mcp.setFileConfig(c.Server)
		// include_tools also gates which external tools are forwarded, so an
		// edit registers/removes them now rather than at the next tools/list.
		s.mcp.resyncForward()
		// The runtime Skill contract tools are gated by the same whitelist —
		// same edit, same eager resync.
		s.mcp.resyncRuntime()
	}
	if s.opts.Logger != nil {
		s.opts.Logger.Printf("mcp client configuration updated (%d servers)", len(c.Servers))
	}
	writeJSON(w, http.StatusOK, s.mcpSnapshot())
}

// handleMCPClientProbe trial-connects one server entry without joining the
// pool — the editor's "test this entry" button. A connect failure is a 200
// with ok:false and the reason: a probe is a diagnostic, not an accident
// (handleProbeConfig's style), and a half-configured server is the normal
// thing to point it at. Validation goes through the same validateServer a
// file load applies, under a stand-in name that satisfies the name rule.
func (s *Server) handleMCPClientProbe(w http.ResponseWriter, r *http.Request) {
	rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var sc mcpclient.ServerConfig
	dec := json.NewDecoder(bytes.NewReader(rawBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sc); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid server entry: %w", err))
		return
	}
	probeCfg := mcpclient.Config{Servers: map[string]*mcpclient.ServerConfig{"probe": &sc}}
	if err := probeCfg.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// r.Context() is right here, unlike in PUT: a probe is request-scoped,
	// and a client that hung up has no use for the answer.
	writeJSON(w, http.StatusOK, mcpclient.Probe(r.Context(), "probe", &sc, nil))
}
