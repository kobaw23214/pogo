package skillruntime

import (
	"errors"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/yosukeno/pogo/skillruntime/core"
	"github.com/yosukeno/pogo/skills"
)

func (s *Service) SkillInstructions(id string) (string, bool, error) {
	body, truncated, err := s.SkillDocument(id, "SKILL.md")
	return string(body), truncated, err
}

// SkillDocument previews only the same instruction/reference metadata exposed
// by a bound Skill. Runtime payloads and the installation directory stay private.
func (s *Service) SkillDocument(id, relative string) ([]byte, bool, error) {
	relative, err := skills.DocumentPath(relative)
	if err != nil || (relative != "SKILL.md" && !strings.HasPrefix(relative, "references/")) {
		return nil, false, skills.ErrDocumentPath
	}
	for _, installed := range s.Catalog() {
		if PackageSkillID(installed.Ref) != id {
			continue
		}
		pkg, err := s.catalogPackage(installed.Ref)
		if err != nil {
			return nil, false, err
		}
		return pkg.ReadMetadataLimit("./"+relative, skills.DocumentPreviewLimit)
	}
	return nil, false, errors.New("Skill 不存在")
}

// SkillTools returns metadata only for one exact, installed Skill identity.
func (s *Service) SkillTools(id string) []Descriptor {
	for _, installed := range s.Catalog() {
		if PackageSkillID(installed.Ref) != id || installed.Revoked {
			continue
		}
		pkg, err := s.catalogPackage(installed.Ref)
		if err != nil {
			return nil
		}
		out := []Descriptor{}
		for _, ref := range pkg.Manifest.RequiresCapabilities {
			d := describeContract(pkg.Contracts[ref.ID])
			d.SkillID = id
			d.SkillName = pkg.Manifest.Skill.ID
			out = append(out, d)
		}
		resolveDescriptorNames(out)
		return out
	}
	return nil
}

// wireNameMaxLength is the provider-side tool name limit, mirrored from
// tools.MaxWireNameLength (that package imports this one, so the constant
// cannot be shared without an import cycle). wireNameKeepLength is how much
// of a long name survives truncation; the hash tail keeps the entropy.
const (
	wireNameMaxLength  = 64
	wireNameKeepLength = 55
)

func describeContract(c core.Contract) Descriptor {
	return Descriptor{ID: c.ID, Name: skillToolName(c.ID), Description: c.Description, InputSchema: c.InputSchema, ReadOnly: !c.Effects.WritesArtifacts && !c.Effects.MutatesExternalState, FileInputs: len(c.FileInputs) > 0, WritesArtifacts: c.Effects.WritesArtifacts}
}

// skillToolName derives the tool name a contract exposes to the model: the
// human-readable contract ID ("malware.sample.search") under a "skill_"
// prefix, sanitized to the wire alphabet ("skill_malware_sample_search").
// Over-long IDs truncate with an fnv32a tail, the same scheme
// tools.WireNameFor uses for MCP tools, and the reserved skill_read name gets
// a digest suffix — decided once here, not discovered per call.
func skillToolName(id string) string {
	safe := "skill_" + strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, id)
	if len(safe) > wireNameMaxLength {
		safe = safe[:wireNameKeepLength] + "_" + fnvHash8(safe)
	}
	if safe == "skill_read" {
		safe += "_" + digest8(id)
	}
	return safe
}

// resolveDescriptorNames keeps tool names unique within one exposed set:
// distinct contract IDs that sanitize to the same name (only possible past
// the schema's ID alphabet, e.g. "a/b" vs "a.b") each get a short digest of
// their ID appended. Every member of a colliding group is suffixed, so the
// outcome does not depend on iteration order.
func resolveDescriptorNames(descriptors []Descriptor) {
	counts := map[string]int{}
	for _, d := range descriptors {
		counts[d.Name]++
	}
	for i := range descriptors {
		if counts[descriptors[i].Name] > 1 {
			descriptors[i].Name += "_" + digest8(descriptors[i].ID)
		}
	}
}

func fnvHash8(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%08x", h.Sum32())
}

func digest8(id string) string {
	return core.DigestBytes([]byte(id))[7:15]
}
