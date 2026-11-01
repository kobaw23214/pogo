package agentworkflow

import "strings"

// RunResources records immutable, node-local resource snapshots. Omission keeps
// legacy Run JSON unchanged. Bytes live under the Run workspace, not this record.
type RunResources struct {
	Version int                      `json:"version"`
	Nodes   map[string]NodeResources `json:"nodes"`
}
type NodeResources struct {
	Skills    []ResourceDocument `json:"skills,omitempty"`
	Shortcuts []ResourceShortcut `json:"shortcuts,omitempty"`
	Files     []ResourceFile     `json:"files,omitempty"`
}
type ResourceDocument struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
}
type ResourceShortcut struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Version string `json:"version"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
}
type ResourceFile struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func ValidRunResources(d Definition, resources *RunResources) bool {
	if resources == nil {
		for _, step := range ResourceSteps(d) {
			if len(step.Skills)+len(step.Shortcuts) != 0 {
				return false
			}
		}
		return true
	}
	if resources.Version != 1 || len(resources.Nodes) > len(d.Steps) {
		return false
	}
	steps := map[string]Step{}
	for _, step := range ResourceSteps(d) {
		steps[step.ID] = step
	}
	count := 0
	for id, node := range resources.Nodes {
		step, ok := steps[id]
		if !ok || step.Kind != "agent" || len(node.Skills) > 64 || len(node.Shortcuts) != len(step.Shortcuts) || len(node.Files) > 128 {
			return false
		}
		files := map[string]ResourceFile{}
		for _, f := range node.Files {
			if !SafePath(f.Path) || !strings.HasPrefix(f.Path, "resources/") || !ValidHash(f.SHA256) || f.Size < 0 || f.Size > 4<<20 {
				return false
			}
			if _, exists := files[f.Path]; exists {
				return false
			}
			files[f.Path] = f
			count++
		}
		if count > 4096 {
			return false
		}
		skills := map[string]bool{}
		for _, sk := range node.Skills {
			f, ok := files[sk.Path]
			if !ValidResourceID(sk.ID) || skills[sk.ID] || sk.Name == "" || !ok || sk.SHA256 != f.SHA256 || sk.Version == "" {
				return false
			}
			skills[sk.ID] = true
		}
		for _, ref := range step.Skills {
			if !skills[ref] {
				return false
			}
		}
		for i, sc := range node.Shortcuts {
			f, ok := files[sc.Path]
			if sc.ID != step.Shortcuts[i] || sc.Title == "" || sc.Version == "" || !ok || sc.SHA256 != f.SHA256 {
				return false
			}
		}
	}
	for _, step := range ResourceSteps(d) {
		if len(step.Skills)+len(step.Shortcuts) > 0 {
			if _, ok := resources.Nodes[step.ID]; !ok {
				return false
			}
		}
	}
	return true
}
