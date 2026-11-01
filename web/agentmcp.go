package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/tools"
)

// Each Agent has a task registry/transport, but all endpoints share the live
// MCP credential, policy, submit limiter, wait slots and execution budget.
func (s *Server) handleAgentMCP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("aid")
	a, ok := s.mgr.studio.get(id)
	if !ok || a.Status != "ready" || s.mcp == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 48<<20)
	}
	s.agentMCPMu.Lock()
	if s.agentMCP == nil {
		s.agentMCP = map[string]http.Handler{}
	}
	h := s.agentMCP[id]
	if h == nil {
		l := newMCPLayer(s.mgr, s.mcp.static, s.mcp.staticOn, nil)
		l.parent, l.agentID = s.mcp, id
		l.maxConcurrent, l.submits, l.waitSlots, l.runSlots = s.mcp.maxConcurrent, s.mcp.submits, s.mcp.waitSlots, s.mcp.runSlots
		l.exportSnapshots = map[string]agentToolSnapshot{}
		srv := l.server()
		h = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
		s.agentMCP[id] = h
	}
	s.agentMCPMu.Unlock()
	h.ServeHTTP(w, r)
}

func (l *mcpLayer) createSession(model, workspace string) (*Session, error) {
	if l.agentID == "" {
		return l.mgr.Create(model, workspace)
	}
	if workspace != "" {
		return nil, errors.New("Agent MCP sessions use the Agent's own workspace")
	}
	a, ok := l.mgr.studio.get(l.agentID)
	if !ok || a.Status != "ready" {
		return nil, ErrNotFound
	}
	if model != "" {
		allowed := false
		for _, id := range a.allowedModels() {
			if id == model {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, errors.New("model is not selected for this Agent")
		}
	}
	sess, err := l.mgr.createStudioBoundConversation(a, nil, true)
	if err != nil {
		return nil, err
	}
	if model != "" && model != a.Model {
		if _, err := sess.SetModel(model); err != nil {
			_ = l.mgr.Delete(sess.ID)
			return nil, err
		}
	}
	return sess, nil
}

// An Agent MCP token is not a user's web identity. It cannot continue a web
// conversation or another Agent's MCP conversation, even if its ID is known.
func (l *mcpLayer) getSession(id string) (*Session, error) {
	if l.agentID != "" {
		b, ok := l.mgr.studio.binding(id)
		if !ok || b.Agent.ID != l.agentID || b.Owner != nil || !b.MCPExport {
			return nil, ErrNotFound
		}
	}
	return l.mgr.Get(id)
}

type agentToolSnapshot struct {
	revision      string
	skillRevision int
	toolsRevision int
	expires       time.Time
	tools         map[string]tools.Tool
}

func agentObject(properties map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": append([]string{}, required...), "additionalProperties": false}
}
func agentString(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func (l *mcpLayer) addAgentTools(srv *mcp.Server) {
	// Recheck on every protocol call, including an already-initialized transport.
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if a, ok := l.mgr.studio.get(l.agentID); !ok || a.Status != "ready" {
				return nil, errors.New("Agent is unavailable")
			}
			return next(ctx, method, req)
		}
	})
	srv.AddTool(&mcp.Tool{Name: "agent_create_session", Description: "Create a conversation pinned to this Agent's model, Skills and MCP selection. Then call agent_list_tools or agent_submit_task with its session_id.", InputSchema: agentObject(map[string]any{"model": agentString("Optional model from this Agent's selected models.")})}, l.agentCreateSession)
	srv.AddTool(&mcp.Tool{Name: "agent_list_tools", Description: "List the selected Skill and external MCP tools, their exact schemas and a catalog_revision for this conversation. Built-in shell/filesystem tools are available through agent_submit_task only.", InputSchema: agentObject(map[string]any{"session_id": agentString("Agent MCP conversation ID")}, "session_id"), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, l.agentListTools)
	srv.AddTool(&mcp.Tool{Name: "agent_call_tool", Description: "Call one tool from agent_list_tools using its name, arguments and catalog_revision. No LLM is invoked. Stale catalogs are refused, never retried automatically. Reviewable operations must use agent_submit_task and the approval flow.", InputSchema: agentObject(map[string]any{"session_id": agentString("Agent MCP conversation ID"), "catalog_revision": agentString("Revision returned by agent_list_tools"), "name": agentString("Exact tool name from agent_list_tools"), "arguments": map[string]any{"type": "object"}}, "session_id", "catalog_revision", "name", "arguments")}, l.agentCallTool)
	srv.AddTool(&mcp.Tool{Name: "agent_upload_file", Description: "Upload a file (base64, at most 32 MiB decoded) to this conversation's Skill tools. Returns a private fileId; never pass a host path to a Skill.", InputSchema: agentObject(map[string]any{"session_id": agentString("Agent MCP conversation ID"), "data_base64": agentString("Base64 file bytes")}, "session_id", "data_base64")}, l.agentUploadFile)
	srv.AddTool(&mcp.Tool{Name: "agent_read_artifact", Description: "Read a chunk of a Skill artifact owned by this conversation. Returns base64 data, next_offset and eof; repeat to download the artifact.", InputSchema: agentObject(map[string]any{"session_id": agentString("Agent MCP conversation ID"), "artifact_id": agentString("Artifact ID returned by a Skill call"), "offset": map[string]any{"type": "integer", "minimum": 0}, "max_bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": 1048576}}, "session_id", "artifact_id"), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, l.agentReadArtifact)
}

func (l *mcpLayer) agentCreateSession(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		Model string `json:"model"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
			return mcpErr("invalid arguments: %v", err), nil
		}
	}
	if _, ok := l.submits.allow(); !ok {
		return mcpErr("MCP request rate limit reached"), nil
	}
	sess, err := l.createSession(in.Model, "")
	if err != nil {
		return mcpErr("create Agent conversation: %v", err), nil
	}
	b, _ := l.mgr.studio.binding(sess.ID)
	return mcpOut(map[string]any{"agent_id": l.agentID, "session_id": sess.ID, "revision": b.Agent.Revision, "model": sess.Model().Model}, "Agent conversation created; call agent_list_tools next"), nil
}

// reserveAgentCall shares the existing exclusive session maintenance slot.
// Start/model switch/close/cancel already honor this reservation and its cancel
// function. No Agent registry or history is mutated by a direct tool call.
func (l *mcpLayer) reserveAgentCall(ctx context.Context, sess *Session) (context.Context, func(), error) {
	if _, err := sess.SyncModelConfig(); err != nil {
		return nil, nil, err
	}
	if sess.studioID != "" {
		if _, err := sess.SyncStudioToolPolicy(); err != nil {
			return nil, nil, err
		}
		if _, err := sess.SyncStudioSkillPolicy(); err != nil {
			return nil, nil, err
		}
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	release, err := l.mgr.studio.permit(l.agentID)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	if sess.closing {
		return nil, nil, ErrNotFound
	}
	if sess.active != nil || sess.compacting {
		return nil, nil, ErrRunActive
	}
	ctx, cancel := context.WithTimeout(ctx, 16*time.Minute)
	done := make(chan struct{})
	sess.compacting, sess.compactCancel, sess.compactDone = true, cancel, done
	sess.lastUsed = time.Now()
	return ctx, func() {
		cancel()
		sess.mu.Lock()
		sess.compacting, sess.compactCancel, sess.compactDone = false, nil, nil
		close(done)
		sess.mu.Unlock()
		// Direct MCP calls use the same busy reservation as compaction and do
		// not pass through the conversation run's finish hook.
		_, _ = sess.SyncModelConfig()
		if sess.studioID != "" {
			_, _ = sess.SyncStudioSkillPolicy()
			_, _ = sess.SyncStudioToolPolicy()
		}
	}, nil
}

func (l *mcpLayer) selectedTools(ctx context.Context, sess *Session) ([]tools.Tool, []string, error) {
	var remote []tools.Tool
	var trusted []string
	if pool := l.mgr.poolFor(sess.owner); pool != nil {
		if err := pool.Refresh(ctx); err != nil {
			return nil, nil, err
		}
		all, grants, _ := pool.SnapshotServers(sess.mcpOnly)
		remote, trusted = dedupeMCP(all, grants, sess.mcpReserved)
	}
	return append(append([]tools.Tool(nil), sess.mcpReserved...), remote...), trusted, nil
}

func (l *mcpLayer) agentListTools(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, _ := mcpArg(req, "session_id")
	sess, err := l.getSession(id)
	if err != nil {
		return mcpErr("unknown Agent conversation"), nil
	}
	ctx, done, err := l.reserveAgentCall(ctx, sess)
	if err != nil {
		return mcpErr("%v", err), nil
	}
	defer done()
	selected, _, err := l.selectedTools(ctx, sess)
	if err != nil {
		return mcpErr("catalog refresh failed: %v", err), nil
	}
	binding, _ := l.mgr.studio.binding(sess.ID)
	snapshot := agentToolSnapshot{skillRevision: binding.skillRevision(), toolsRevision: binding.toolsRevision(), revision: mcpobserve.NewID(), expires: time.Now().Add(30 * time.Minute), tools: map[string]tools.Tool{}}
	rows := []map[string]any{}
	for _, tool := range selected {
		if !l.toolAllowed(tool.Name()) {
			continue
		}
		if _, exists := snapshot.tools[tool.Name()]; exists {
			return mcpErr("duplicate selected tool name: %s", tool.Name()), nil
		}
		snapshot.tools[tool.Name()] = tool
		rows = append(rows, map[string]any{"name": tool.Name(), "description": tool.Description(), "input_schema": tool.InputSchema()})
	}
	l.exportMu.Lock()
	for key, value := range l.exportSnapshots {
		if time.Now().After(value.expires) {
			delete(l.exportSnapshots, key)
		}
	}
	if len(l.exportSnapshots) >= 256 { // bounded independently of task retention
		for key := range l.exportSnapshots {
			delete(l.exportSnapshots, key)
			break
		}
	}
	l.exportSnapshots[id] = snapshot
	l.exportMu.Unlock()
	b, _ := l.mgr.studio.binding(id)
	return mcpOut(map[string]any{"session_id": id, "agent_id": l.agentID, "agent_revision": b.Agent.Revision, "catalog_revision": snapshot.revision, "tools": rows}, "%d selected tools", len(rows)), nil
}

func (l *mcpLayer) agentCallTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		SessionID string          `json:"session_id"`
		Revision  string          `json:"catalog_revision"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
		return mcpErr("invalid arguments: %v", err), nil
	}
	sess, err := l.getSession(in.SessionID)
	if err != nil {
		return mcpErr("unknown Agent conversation"), nil
	}
	if !l.toolAllowed(in.Name) {
		return mcpErr("tool is not exposed"), nil
	}
	l.exportMu.Lock()
	snapshot := l.exportSnapshots[in.SessionID]
	l.exportMu.Unlock()
	tool := snapshot.tools[in.Name]
	if tool == nil || in.Revision != snapshot.revision || time.Now().After(snapshot.expires) {
		return mcpErr("catalog is stale; call agent_list_tools before retrying"), nil
	}
	if err := tools.ValidateArgs(tool, in.Arguments); err != nil {
		return mcpErr("invalid tool arguments: %v", err), nil
	}
	if _, ok := l.submits.allow(); !ok {
		return mcpErr("MCP request rate limit reached"), nil
	}
	select {
	case l.runSlots <- struct{}{}:
		defer func() { <-l.runSlots }()
	default:
		return mcpErr("MCP concurrency limit reached"), nil
	}
	ctx, done, err := l.reserveAgentCall(ctx, sess)
	if err != nil {
		return mcpErr("%v", err), nil
	}
	defer done()
	// Skill edits revoke previously described local capabilities as well as
	// the conversational registry. A captured object cannot outlive its grant.
	if binding, ok := l.mgr.studio.binding(sess.ID); !ok || binding.skillRevision() != snapshot.skillRevision || binding.toolsRevision() != snapshot.toolsRevision {
		return mcpErr("catalog is stale; call agent_list_tools before retrying"), nil
	}
	// Refresh connection policy, but execute the client's captured tool object.
	// MCP's callBound rejects changes to its definition instead of redirecting.
	current, trusted, err := l.selectedTools(ctx, sess)
	if err != nil {
		return mcpErr("catalog refresh failed: %v", err), nil
	}
	policy := NewPolicy()
	policy.Set(l.effectivePolicy(), 0)
	policy.ReplaceMCP(current, trusted)
	if _, remote := tool.(*tools.MCPTool); !remote {
		if ro, ok := tool.(tools.ReadOnlyCapable); ok && ro.IsReadOnly() {
			policy.MarkReadOnly(tool.Name())
		}
	}
	if _, allowed := policy.Decide(agent.GateRequest{ToolName: tool.Name(), Args: in.Arguments}); !allowed {
		return mcpErr("tool requires approval under the MCP policy; use agent_submit_task and agent_task_approve"), nil
	}
	md := mcpobserve.MetadataFrom(ctx)
	md.SessionID, md.AgentID, md.AgentName = sess.ID, sess.studioID, sess.studioName
	if sess.owner != nil {
		md.OwnerID = sess.owner.SubjectID
	}
	ctx = mcpobserve.WithMetadata(ctx, md)
	if span := inboundSpanFrom(ctx); span != nil {
		span.Correlate(md)
	}
	result, err := tool.Execute(ctx, in.Arguments)
	l.audit("Agent %s direct tool=%s session=%s failed=%v", l.agentID, in.Name, sess.ID, err != nil)
	if err != nil {
		return mcpErr("%v", err), nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: result.Text}}, StructuredContent: result.Details}, nil
}

func (l *mcpLayer) agentUploadFile(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		SessionID string `json:"session_id"`
		Data      string `json:"data_base64"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
		return mcpErr("invalid arguments: %v", err), nil
	}
	if len(in.Data) > base64.StdEncoding.EncodedLen(32<<20) {
		return mcpErr("file exceeds 32 MiB"), nil
	}
	if _, ok := l.submits.allow(); !ok {
		return mcpErr("MCP request rate limit reached"), nil
	}
	sess, err := l.getSession(in.SessionID)
	if err != nil || sess.runtimeSkillSnapshot() == nil {
		return mcpErr("Skill conversation is unavailable"), nil
	}
	_, done, err := l.reserveAgentCall(ctx, sess)
	if err != nil {
		return mcpErr("%v", err), nil
	}
	defer done()
	data, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil {
		return mcpErr("invalid base64"), nil
	}
	runtime := sess.runtimeSkillSnapshot()
	if runtime == nil {
		return mcpErr("Skill session is unavailable"), nil
	}
	file, err := runtime.Upload(data)
	if err != nil {
		return mcpErr("%v", err), nil
	}
	return mcpOut(map[string]any{"session_id": sess.ID, "fileId": file.FileID, "digest": file.Digest, "size": file.Size}, "file uploaded; use fileId with Skill tools"), nil
}

func (l *mcpLayer) agentReadArtifact(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		SessionID  string `json:"session_id"`
		ArtifactID string `json:"artifact_id"`
		Offset     int    `json:"offset"`
		MaxBytes   int    `json:"max_bytes"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
		return mcpErr("invalid arguments: %v", err), nil
	}
	sess, err := l.getSession(in.SessionID)
	if err != nil || sess.runtimeSkillSnapshot() == nil {
		return mcpErr("Skill conversation is unavailable"), nil
	}
	runtime := sess.runtimeSkillSnapshot()
	if runtime == nil {
		return mcpErr("Skill session is unavailable"), nil
	}
	artifact, err := runtime.ReadArtifact(in.ArtifactID)
	if err != nil {
		return mcpErr("%v", err), nil
	}
	if in.Offset < 0 || in.Offset > len(artifact.Data) {
		return mcpErr("invalid artifact offset"), nil
	}
	if in.MaxBytes <= 0 {
		in.MaxBytes = 256 << 10
	}
	end := min(len(artifact.Data), in.Offset+min(in.MaxBytes, 1<<20))
	return mcpOut(map[string]any{"artifact_id": in.ArtifactID, "data_base64": base64.StdEncoding.EncodeToString(artifact.Data[in.Offset:end]), "next_offset": end, "eof": end == len(artifact.Data)}, "artifact chunk"), nil
}
