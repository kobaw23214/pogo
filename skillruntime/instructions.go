package skillruntime

import (
	"fmt"
	"path"
	"strings"

	"github.com/yosukeno/pogo/skills"
)

// Skills exposes only the bound Skill instructions. Runtime/data paths stay
// private; ordinary filesystem tools never receive these directories.
func (b *BoundSession) Skills() []skills.Skill {
	out := []skills.Skill{}
	for _, ref := range b.resolved.binding.Packages {
		pkg := b.resolved.packages[ref.ID]
		instruction := pkg.Manifest.Skill
		location := "skill-runtime://" + PackageSkillID(ref) + "/SKILL.md"
		out = append(out, skills.Skill{Name: instruction.ID, Description: instruction.Description, Path: location, Dir: strings.TrimSuffix(location, "/SKILL.md"), Source: "bundle", ReadContent: func() ([]byte, error) {
			if err := b.service.enabled(ref); err != nil {
				return nil, err
			}
			verified, err := b.service.Store.Load(ref)
			if err != nil {
				return nil, err
			}
			body, truncated, err := verified.ReadMetadataLimit("./SKILL.md", 256<<10)
			if truncated {
				return nil, fmt.Errorf("Skill instructions exceed size limit")
			}
			return body, err
		}, ReadDocument: func(relative string) ([]byte, bool, error) {
			return b.ReadDocument(instruction.ID, relative)
		}})
	}
	return out
}

// ReadDocument exposes only instructions and references of a selected Skill.
// Executables, payloads, contracts, data assets and host paths are excluded.
func (b *BoundSession) ReadDocument(name, relative string) ([]byte, bool, error) {
	relative = strings.TrimPrefix(relative, "./")
	if path.Clean(relative) != relative || strings.ContainsAny(relative, "\\:\x00") ||
		(relative != "SKILL.md" && !strings.HasPrefix(relative, "references/")) {
		return nil, false, fmt.Errorf("only SKILL.md and relative references/ documents can be read")
	}
	for _, ref := range b.resolved.binding.Packages {
		pkg := b.resolved.packages[ref.ID]
		if pkg.Manifest.Skill.ID != name {
			continue
		}
		if err := b.service.enabled(ref); err != nil {
			return nil, false, err
		}
		data, truncated, err := pkg.ReadMetadataLimit("./"+relative, 256<<10)
		if err != nil {
			return nil, false, fmt.Errorf("Skill document is unavailable")
		}
		return data, truncated, nil
	}
	return nil, false, fmt.Errorf("Skill is not selected for this conversation")
}
