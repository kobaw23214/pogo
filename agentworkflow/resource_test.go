package agentworkflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRunResourcesLegacyAndBoundSnapshotValidation(t *testing.T) {
	d := Definition{Steps: []Step{{ID: "task", Kind: "agent", Task: "read"}}}
	if !ValidRunResources(d, nil) {
		t.Fatal("legacy nil resources rejected")
	}
	old, _ := json.Marshal(Run{Definition: d})
	if strings.Contains(string(old), `"resources"`) {
		t.Fatal("legacy Run JSON gained a resource field")
	}
	d.Steps[0].Skills = []string{"skill:one"}
	if ValidRunResources(d, nil) {
		t.Fatal("new binding admitted without frozen snapshots")
	}
	hash := strings.Repeat("a", 64)
	r := &RunResources{Version: 1, Nodes: map[string]NodeResources{"task": {Skills: []ResourceDocument{{ID: "skill:one", Name: "guide", Version: "r1", Path: "resources/task/skill/001-SKILL.md", SHA256: hash}}, Files: []ResourceFile{{Path: "resources/task/skill/001-SKILL.md", Name: "SKILL.md", Size: 20, SHA256: hash}}}}}
	if !ValidRunResources(d, r) {
		t.Fatal("valid frozen snapshot rejected")
	}
	for _, which := range []string{"path", "hash", "binding", "duplicate", "node", "version"} {
		t.Run(which, func(t *testing.T) {
			raw, _ := json.Marshal(r)
			var changed RunResources
			json.Unmarshal(raw, &changed)
			node := changed.Nodes["task"]
			switch which {
			case "path":
				node.Files[0].Path = "../other-user/secret"
			case "hash":
				node.Files[0].SHA256 = "bad"
			case "binding":
				node.Skills[0].ID = "other:skill"
			case "duplicate":
				node.Files = append(node.Files, node.Files[0])
			case "node":
				changed.Nodes["unknown"] = node
			case "version":
				changed.Version = 2
			}
			changed.Nodes["task"] = node
			if ValidRunResources(d, &changed) {
				t.Fatal("invalid immutable snapshot accepted", which)
			}
		})
	}
}
