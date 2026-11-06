package web

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
)

// canonicalMCPInput recognizes only the leading command in a top-level typed
// text part. A quoted command, a file/page observation or frozen shortcut text
// is never scanned. Completion uses explicit atoms and needs no text scanning.
func canonicalMCPInput(prompt string, attachments []string, parts []MessageInputPart, layout string) ([]MessageInputPart, error) {
	if parts != nil {
		if msg := skills.ValidateInputParts(parts, layout, "workspace"); msg != "" {
			return nil, fmt.Errorf("%w: %s", errMessageInputInvalid, msg)
		}
	}
	parse := func(text string) ([]MessageInputPart, bool, error) {
		trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
		if !strings.HasPrefix(trimmed, "/mcp:") {
			return nil, false, nil
		}
		end := strings.IndexFunc(trimmed, unicode.IsSpace)
		if end < 0 {
			end = len(trimmed)
		}
		server, name, found := strings.Cut(trimmed[len("/mcp:"):end], "/")
		var err error
		if found {
			server, err = url.PathUnescape(server)
			if err == nil {
				name, err = url.PathUnescape(name)
			}
		}
		if !found || err != nil || !skills.ValidInputMCPReference(server, name) {
			return nil, true, fmt.Errorf("%w: 请选择 MCP 方法，格式为 /mcp:服务名/方法名", errMessageInputInvalid)
		}
		out := []MessageInputPart{}
		if prefix := text[:len(text)-len(trimmed)]; prefix != "" {
			out = append(out, MessageInputPart{Type: "text", Text: prefix})
		}
		out = append(out, MessageInputPart{Type: "mcp", Server: server, Name: name})
		if end < len(trimmed) {
			out = append(out, MessageInputPart{Type: "text", Text: trimmed[end:]})
		}
		return out, true, nil
	}
	if parts == nil {
		expanded, found, err := parse(prompt)
		if err != nil || !found {
			return nil, err
		}
		// The legacy API displays attachments before prose. Keep those parts
		// after canonicalization instead of silently dropping its attachment list.
		out := make([]MessageInputPart, 0, len(attachments)+len(expanded))
		for _, p := range attachments {
			out = append(out, MessageInputPart{Type: "file", Path: p})
		}
		return append(out, expanded...), nil
	}
	var out []MessageInputPart
	changed := false
	for _, part := range parts {
		if part.Type == "text" {
			expanded, found, err := parse(part.Text)
			if err != nil {
				return nil, err
			}
			if found {
				changed = true
				out = append(out, expanded...)
				continue
			}
		}
		out = append(out, part)
	}
	if changed {
		return out, nil
	}
	return parts, nil
}

func (s *Session) inputMCPMethods(parts []MessageInputPart) (map[int]agent.MCPInvocationMethod, error) {
	refs := map[int]agent.MCPInvocationMethod{}
	flat := flatInputParts(parts)
	wants := false
	for _, p := range flat {
		wants = wants || p.Type == "mcp"
	}
	if !wants {
		return refs, nil
	}
	var methods []agent.MCPInvocationMethod
	if s.agent != nil {
		s.refreshMCPInputCatalog()
		methods = s.agent.MCPInvocationMethods()
	} else if s.studioID != "" {
		// Schedule validation uses an inert candidate Session. Resolve the
		// requested published Agent, never the wider deployment capabilities.
		if a, ok := s.mgr.studio.get(s.studioID); ok && !a.Deleted && a.Status == "ready" {
			methods = s.mgr.draftMCPInvocationMethods(s.owner, &a)
		}
	}
	for index, part := range flat {
		if part.Type != "mcp" {
			continue
		}
		for _, method := range methods {
			if method.Server == part.Server && method.Name == part.Name {
				refs[index] = method
				break
			}
		}
		if _, ok := refs[index]; !ok {
			return nil, fmt.Errorf("%w: 当前会话不可用的 MCP 方法 %q/%q，请检查 Agent 的 MCP 选择或刷新方法列表", errMessageInputInvalid, part.Server, part.Name)
		}
	}
	return refs, nil
}

func mcpInputBlock(method agent.MCPInvocationMethod) llm.Block {
	ref := &inputref.MCPReference{Server: method.Server, Name: method.Name}
	selector, _ := json.Marshal(map[string]string{"server": method.Server, "tool": method.WireName})
	identity, _ := json.Marshal(map[string]string{"server": method.Server, "name": method.Name, "wire_name": method.WireName})
	return llm.Block{Type: llm.BlockText, InputMCP: ref, Text: "[User-selected MCP method] " + string(identity) + "\nUse this exact method for the user's task. If its executable input schema is not currently visible, inspect it with mcp_describe " + string(selector) + " before calling the exact wire_name. Derive arguments from the user's request and ask for missing required information; never invent values. This reference does not execute a call, grant new capability, or bypass the existing approval policy.\n"}
}
