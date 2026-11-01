package core

import (
	"path/filepath"

	"github.com/yosukeno/pogo/skills"
)

// Files enumerates only the original package tree. Installed runtime/data/output
// trees are deliberately outside this root and never enter administrator JSON.
func (p *Package) Files(relative string) (skills.FileList, error) {
	return skills.ListDirectoryFiles(p.staticRoot(), relative, nil)
}

// StaticDocument is only for the authenticated administrator's package viewer.
// Unlike the Agent metadata reader it can show manifest/script source, but it
// still cannot leave the original package tree or execute any uploaded content.
func (p *Package) StaticDocument(relative string) ([]byte, bool, error) {
	return skills.ReadDocument(skills.Skill{Dir: p.staticRoot()}, relative)
}

func (p *Package) staticRoot() string {
	root := p.Root
	if p.installed != nil {
		root = filepath.Join(root, "package")
	}
	return root
}
