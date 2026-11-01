package skillruntime

import "github.com/yosukeno/pogo/skills"

// SkillFiles is an administrator-only projection of the exact installed bundle.
// It returns names and sizes, never private runtime roots or execution access.
func (s *Service) SkillFiles(id, relative string) (skills.FileList, error) {
	for _, installed := range s.Catalog() {
		if PackageSkillID(installed.Ref) != id {
			continue
		}
		pkg, err := s.catalogPackage(installed.Ref)
		if err != nil {
			return skills.FileList{}, skills.ErrDocumentUnavailable
		}
		return pkg.Files(relative)
	}
	return skills.FileList{}, skills.ErrDocumentUnavailable
}

// SkillStaticDocument serves only the administrator's original-package preview.
// Agent/session readers deliberately keep using SkillDocument/ReadDocument.
func (s *Service) SkillStaticDocument(id, relative string) ([]byte, bool, error) {
	for _, installed := range s.Catalog() {
		if PackageSkillID(installed.Ref) != id {
			continue
		}
		pkg, err := s.catalogPackage(installed.Ref)
		if err != nil {
			return nil, false, skills.ErrDocumentUnavailable
		}
		return pkg.StaticDocument(relative)
	}
	return nil, false, skills.ErrDocumentUnavailable
}
