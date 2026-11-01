package web

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/session"
)

const mcpDiagnosticHistoryMaxBytes = 32 << 20
const mcpDiagnosticHistoryMaxInput = 64 << 10

// historicalMCPDiagnostic is called only after the existing detail permission
// check. It does not load a runtime Session, publish an event, repair a file or
// backfill the observation DB. Missing/ambiguous evidence stays missing.
func (s *Server) historicalMCPDiagnostic(ctx context.Context, call *mcpobserve.Call) *mcpobserve.Call {
	if call == nil || call.ErrorSummary != "" || call.Direction != "outbound" || (call.Status != "error" && call.Status != "timeout") || !validID(call.SessionID) || call.ToolCallID == "" || call.WireTool == "" || s.mgr == nil || s.mgr.cfg.Catalog == nil || s.mgr.studio == nil {
		return call
	}
	owner, err := s.mgr.cfg.Catalog.OwnerOf(ctx, call.SessionID)
	if err != nil || !owner.Scope.Valid() || owner.Scope.SubjectID != call.OwnerID {
		return call
	}
	if _, err = s.mgr.cfg.Catalog.Get(ctx, owner.Scope, call.SessionID); err != nil {
		return call
	}
	binding, bound := s.mgr.studio.binding(call.SessionID)
	if (call.AgentID != "" && (!bound || binding.Agent.ID != call.AgentID)) || (bound && (binding.Owner == nil || *binding.Owner != owner.Scope)) {
		return call
	}
	raw := s.mcpDiagnosticTranscript(ctx, call.SessionID)
	if raw == nil {
		return call
	}
	d := historicalMCPDiagnostic(raw, call, func(meta *session.Meta) bool {
		if bound {
			return meta != nil && meta.Runtime == binding.runtime()
		}
		return meta != nil && meta.Runtime == ""
	})
	if d.Summary == "" {
		return call
	}
	copy := *call
	copy.ErrorSummary = d.Summary
	if copy.ErrorCode == "" {
		copy.ErrorCode = d.Code
	}
	copy.ErrorSource = "session"
	return &copy
}

// mcpDiagnosticTranscript reads only a stable regular file below the private
// transcript root, without following session/subagent symlinks or reopening an
// unchecked path. A concurrent append or deletion simply yields no enrichment.
func (s *Server) mcpDiagnosticTranscript(ctx context.Context, id string) []byte {
	root, err := os.OpenRoot(s.mgr.cfg.SessionDir)
	if err != nil {
		return nil
	}
	defer root.Close()
	rel := id + ".jsonl"
	before, err := root.Lstat(rel)
	if os.IsNotExist(err) {
		dir, e := root.Lstat(session.SubagentDirName)
		if e != nil || !dir.IsDir() || dir.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		rel = filepath.Join(session.SubagentDirName, rel)
		before, err = root.Lstat(rel)
	}
	if err != nil || !before.Mode().IsRegular() || before.Size() > mcpDiagnosticHistoryMaxBytes || before.Size() < 1 {
		return nil
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(before, actual) {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(mcpDiagnosticReader{ctx: ctx, Reader: f}, mcpDiagnosticHistoryMaxBytes+1))
	if err != nil || len(raw) > mcpDiagnosticHistoryMaxBytes || int64(len(raw)) != before.Size() {
		return nil
	}
	after, err := root.Lstat(rel)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || ctx.Err() != nil {
		return nil
	}
	return raw
}

type mcpDiagnosticReader struct {
	ctx context.Context
	io.Reader
}

func (r mcpDiagnosticReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func historicalMCPDiagnostic(raw []byte, call *mcpobserve.Call, validMeta func(*session.Meta) bool) mcpobserve.Diagnostic {
	// Check uniqueness over the entire file, including abandoned branches. An
	// otherwise valid current branch must never steal an earlier reused call ID.
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), mcpDiagnosticHistoryMaxBytes)
	uses, results := 0, 0
	resultID := ""
	recordIDs := map[string]bool{}
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var record session.Record
		if json.Unmarshal(scanner.Bytes(), &record) != nil || record.ID == "" || recordIDs[record.ID] {
			return mcpobserve.Diagnostic{}
		}
		recordIDs[record.ID] = true
		if record.Message == nil {
			continue
		}
		for _, block := range record.Message.Content {
			if block.Type == llm.BlockToolUse && block.ID == call.ToolCallID {
				uses++
			}
			if block.Type == llm.BlockToolResult && block.ToolUseID == call.ToolCallID {
				results++
				resultID = record.ID
			}
		}
	}
	if scanner.Err() != nil || uses != 1 || results != 1 {
		return mcpobserve.Diagnostic{}
	}
	store, err := session.Read(bytes.NewReader(raw))
	if err != nil || len(store.Diagnostics()) != 0 || !validMeta(store.Meta()) {
		return mcpobserve.Diagnostic{}
	}
	var input json.RawMessage
	var result *llm.Block
	foundUse := false
	for _, record := range store.BranchRecords(resultID) {
		if record.Message == nil {
			continue
		}
		for _, block := range record.Message.Content {
			if block.Type == llm.BlockToolUse && block.ID == call.ToolCallID {
				if record.Message.Role != "assistant" || block.Name != call.WireTool || len(block.Input) == 0 || len(block.Input) > mcpDiagnosticHistoryMaxInput || !json.Valid(block.Input) {
					return mcpobserve.Diagnostic{}
				}
				foundUse = true
				input = block.Input
			}
			if block.Type == llm.BlockToolResult && block.ToolUseID == call.ToolCallID {
				if !foundUse || record.Message.Role != "user" || !block.IsError || record.Time < call.StartedAt {
					return mcpobserve.Diagnostic{}
				}
				copy := block
				result = &copy
			}
		}
	}
	if result == nil {
		return mcpobserve.Diagnostic{}
	}
	text := result.Text
	if strings.HasPrefix(text, "[cite:") {
		// Strip only a verified host-added citation header, never arbitrary remote
		// prose. Its saved call ID and raw-output digest bind the remainder exactly.
		header, body, ok := strings.Cut(text, "\n")
		var source struct {
			Label  string `json:"label"`
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Hash   string `json:"hash"`
		}
		if !ok || json.Unmarshal(result.Source, &source) != nil || source.Type != "mcp_tool" || source.CallID != call.ToolCallID || source.Label == "" || !strings.HasPrefix(header, "[cite:"+source.Label+" | "+call.WireTool+" | ") || !strings.HasSuffix(header, "]") {
			return mcpobserve.Diagnostic{}
		}
		sum := sha256.Sum256([]byte(body))
		if source.Hash != hex.EncodeToString(sum[:])[:12] {
			return mcpobserve.Diagnostic{}
		}
		text = body
	}
	return mcpobserve.DiagnoseResult(&mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}, input)
}
