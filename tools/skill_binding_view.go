package tools

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/yosukeno/pogo/skills"
)

// Skill-bound tool layering, the registry half (the agent half is
// agent/skillview.go). A binding group may be large enough that declaring it
// on every request costs more than the rest of a session's tools together.
// Such a group can stay out of the model's view until the conversation loads
// its trusted Skill.
//
// Withholding is only safe where loading is possible, so this file answers two
// narrow questions from the registry's own trusted state: which deferrable
// groups have a working discovery path here, and which of them one read call
// loaded. Visibility is never authorization: withheld tools stay registered,
// callable, validated and gated exactly as before.
//
// The same trusted state also serves compaction (agent/compact_rehydrate.go):
// which registered Skill documents a read call loaded, and the current text of
// one of them, so a loaded Skill can be restored rather than summarised.

// deferredSkillBindings names the groups worth withholding. The other bindings
// cost a few hundred tokens each and keep their eager declarations.
var deferredSkillBindings = []string{"agent-workflows"}

// DeferredSkillBinding is one withholdable group as a registry holds it.
type DeferredSkillBinding struct {
	// Name is the binding name, which is also its trusted embedded Skill's name.
	Name string
	// Tools are the group's tools registered here, in binding order.
	Tools []string
}

// SkillDocument is a registered Skill's SKILL.md as the read tool attributes
// it: the loader-registered identity plus the resolved path. Only files the
// loader registered have one; any other path, including a same-named copy,
// is an ordinary file.
type SkillDocument struct {
	Name   string
	Source string
	// Path is the resolved absolute path, the form a later read resolves to.
	Path string
}

// DeferredSkillBindings lists the deferrable groups whose tools are registered
// and whose trusted embedded SKILL.md the read tool attributes. Without that
// discovery path a group is never withheld: hiding it would strand granted
// tools, for example an older Agent granted the tools without the Skill.
func (r *Registry) DeferredSkillBindings() []DeferredSkillBinding {
	read, _ := r.byName["read"].(*Read)
	if read == nil {
		return nil
	}
	var out []DeferredSkillBinding
	for _, name := range deferredSkillBindings {
		if !read.attributesEmbeddedSkill(name) {
			continue
		}
		var present []string
		for _, tool := range SkillBindingTools(name) {
			if _, ok := r.byName[tool]; ok {
				present = append(present, tool)
			}
		}
		if len(present) > 0 {
			out = append(out, DeferredSkillBinding{Name: name, Tools: present})
		}
	}
	return out
}

// SkillDocumentAt reports the registered Skill whose SKILL.md path resolves
// to, using exactly the read tool's resolution, roots and loader-registered
// attribution.
func (r *Registry) SkillDocumentAt(path string) (SkillDocument, bool) {
	read, _ := r.byName["read"].(*Read)
	if read == nil || filepath.Base(path) != "SKILL.md" {
		return SkillDocument{}, false
	}
	resolved, err := resolve(read.Cwd, path, read.Roots...)
	if err != nil {
		return SkillDocument{}, false
	}
	identity, ok := read.skillReads[skillReadKey(resolved)]
	if !ok || identity.Name == "" {
		return SkillDocument{}, false
	}
	return SkillDocument{Name: identity.Name, Source: identity.Source, Path: resolved}, true
}

// SkillBindingForDocument reports the deferrable group whose trusted embedded
// SKILL.md path resolves to. Any other file, including a same-named user
// Skill, belongs to no group.
func (r *Registry) SkillBindingForDocument(path string) (string, bool) {
	doc, ok := r.SkillDocumentAt(path)
	if !ok || !doc.isDeferredBinding() {
		return "", false
	}
	return doc.Name, true
}

func (d SkillDocument) isDeferredBinding() bool {
	return d.Source == "embedded" && slices.Contains(deferredSkillBindings, d.Name)
}

// SkillDocumentsLoadedByRead reports the registered Skill documents a read
// call loaded, in argument order and without duplicates. The caller passes
// only calls whose result was not an error; a batch read still counts each
// file by its own outcome in details.
func (r *Registry) SkillDocumentsLoadedByRead(input, details json.RawMessage) []SkillDocument {
	var a readArgs
	if json.Unmarshal(input, &a) != nil {
		return nil
	}
	targets, err := a.targets()
	if err != nil {
		return nil
	}
	succeeded := map[string]bool{}
	if len(targets) == 1 {
		succeeded[targets[0]] = true
	} else {
		var many ReadManyDetails
		if json.Unmarshal(details, &many) != nil {
			return nil
		}
		for _, file := range many.Files {
			if file.Error == "" && file.BodyLength > 0 {
				succeeded[file.Path] = true
			}
		}
	}
	var out []SkillDocument
	for _, path := range targets {
		if !succeeded[path] {
			continue
		}
		doc, ok := r.SkillDocumentAt(path)
		if ok && !slices.ContainsFunc(out, func(seen SkillDocument) bool { return seen.Path == doc.Path }) {
			out = append(out, doc)
		}
	}
	return out
}

// SkillBindingsLoadedByRead reports the deferrable groups a read call loaded.
func (r *Registry) SkillBindingsLoadedByRead(input, details json.RawMessage) []string {
	var out []string
	for _, doc := range r.SkillDocumentsLoadedByRead(input, details) {
		if doc.isDeferredBinding() && !slices.Contains(out, doc.Name) {
			out = append(out, doc.Name)
		}
	}
	return out
}

// errSkillDocumentMoved refuses a document whose path no longer resolves to
// the identity it was loaded as, for example after the Skill set changed.
var errSkillDocumentMoved = errors.New("the Skill document is no longer registered at this path")

// SkillInvocation re-reads a registered Skill document and formats it as an
// explicit invocation (skills.Invocation), the form a restored Skill takes in a
// compacted conversation. The path is resolved and re-attributed first, so a
// document can only be restored from where the loader registered it. The
// read is a host action and records no Skill load.
func (r *Registry) SkillInvocation(doc SkillDocument) (string, error) {
	current, ok := r.SkillDocumentAt(doc.Path)
	if !ok || current.Name != doc.Name || current.Source != doc.Source {
		return "", errSkillDocumentMoved
	}
	data, err := withFileLock(current.Path, func() ([]byte, error) { return os.ReadFile(current.Path) })
	if err != nil {
		return "", err
	}
	return skills.Invocation(skills.Skill{
		Name: current.Name, Source: current.Source, Path: current.Path, Dir: filepath.Dir(current.Path),
		ReadContent: func() ([]byte, error) { return data, nil },
	}, "")
}

func (t *Read) attributesEmbeddedSkill(name string) bool {
	for _, identity := range t.skillReads {
		if identity.Source == "embedded" && identity.Name == name {
			return true
		}
	}
	return false
}
