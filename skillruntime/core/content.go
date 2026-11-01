package core

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// ReadMetadataLimit is the bounded preview counterpart of ReadMetadata.
func (p *Package) ReadMetadataLimit(relative string, limit int) ([]byte, bool, error) {
	if limit < 0 {
		return nil, false, errors.New("invalid metadata preview limit")
	}
	root := p.Root
	if p.installed != nil {
		root = filepath.Join(root, "package")
	}
	full, err := resolveExistingPath(root, relative)
	if err != nil {
		return nil, false, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, false, err
	}
	return data[:min(len(data), limit)], len(data) > limit, nil
}

// ReadMetadata resolves a package-relative metadata file in either a source
// distribution or an installed tree. Callers must not infer store layout from
// Package.Root; runtime and asset paths have their own accessors.
func (p *Package) ReadMetadata(relative string) ([]byte, error) {
	root := p.Root
	if p.installed != nil {
		root = filepath.Join(root, "package")
	}
	_, data, err := resolveExisting(root, relative)
	return data, err
}
