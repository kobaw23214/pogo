package web

import (
	"net/http"

	"github.com/yosukeno/pogo/tools"
)

type workflowEditorTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReadOnly    bool   `json:"read_only"`
	Protected   bool   `json:"protected"`
}

// A workflow builder needs the native-tool names to narrow a node's Agent
// selection. This static editor metadata contains neither the administrator's
// Skill/MCP catalog nor connection configuration, paths or credentials.
func (s *Server) handleWorkflowEditorCapabilities(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mgr.workflows.get(r.PathValue("wid")); !ok {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	catalog := tools.BuiltinCatalog()
	toolsForEditor := make([]workflowEditorTool, 0, len(catalog))
	for _, tool := range catalog {
		toolsForEditor = append(toolsForEditor, workflowEditorTool{
			Name: tool.Name, Description: tool.Description, ReadOnly: tool.ReadOnly, Protected: tool.Protected,
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		BuiltinTools []workflowEditorTool `json:"builtin_tools"`
	}{BuiltinTools: toolsForEditor})
}
