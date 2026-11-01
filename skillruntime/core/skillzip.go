package core

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/yosukeno/pogo/skills"
)

// InspectSkillZIP validates the common Skill shape before either installer
// extracts bytes. Runtime payloads are not read into memory.
func InspectSkillZIP(archive string) (bool, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return false, fmt.Errorf("not a readable Skill ZIP: %w", err)
	}
	defer zr.Close()
	if len(zr.File) > MaxArchiveFiles {
		return false, errors.New("Skill ZIP has too many entries")
	}
	entries := map[string]*zip.File{}
	tops := map[string]bool{}
	for _, file := range zr.File {
		name := strings.TrimSuffix(file.Name, "/")
		if name == "" || path.Clean(name) != name || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") {
			return false, errors.New("Skill ZIP contains an unsafe path")
		}
		if file.Mode()&(^file.Mode().Perm()) != 0 && !file.FileInfo().IsDir() {
			return false, errors.New("Skill ZIP may contain only regular files and directories")
		}
		if _, exists := entries[name]; exists {
			return false, errors.New("Skill ZIP contains duplicate paths")
		}
		entries[name] = file
		tops[strings.SplitN(name, "/", 2)[0]] = true
	}
	prefix, wrapper := "", ""
	if entries["SKILL.md"] == nil && len(tops) == 1 {
		for top := range tops {
			wrapper = top
			prefix = top + "/"
		}
	}
	entry := entries[prefix+"SKILL.md"]
	if entry == nil || entry.FileInfo().IsDir() {
		return false, errors.New("Skill ZIP 必须在根目录包含 SKILL.md，或只包含一个同名 Skill 文件夹；请重新打包为标准 Skill ZIP")
	}
	for name := range entries {
		if path.Base(name) == "SKILL.md" && name != prefix+"SKILL.md" {
			return false, errors.New("一个 ZIP 只能包含一个 Skill；其他说明请放入 references/*.md")
		}
	}
	r, err := entry.Open()
	if err != nil {
		return false, err
	}
	data, err := io.ReadAll(io.LimitReader(r, (256<<10)+1))
	closeErr := r.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	if _, _, err = skills.PackageMetadata(data, wrapper); err != nil {
		return false, err
	}
	manifest := entries[prefix+ManifestFileName]
	if manifest != nil && manifest.FileInfo().IsDir() {
		return false, errors.New("pogo-skill.json must be a regular file")
	}
	return manifest != nil, nil
}

// SkillZIPPeek is the pre-install view of a runtime package's identity:
// enough to recognize "same package, other version" before any bytes are
// verified or installed.
type SkillZIPPeek struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	DisplayName string `json:"displayName"`
}

// PeekSkillZIP reads only the manifest of a staged runtime archive — the same
// root-or-single-wrapper location rule InstallZIP accepts. It trusts nothing:
// the values route the upload (install now vs ask about replacing), and the
// full verification still happens at install time.
func PeekSkillZIP(archive string) (SkillZIPPeek, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return SkillZIPPeek{}, fmt.Errorf("not a readable Skill ZIP: %w", err)
	}
	defer zr.Close()
	topsWithManifest := map[string]bool{}
	for _, file := range zr.File {
		name := strings.TrimSuffix(file.Name, "/")
		if name == ManifestFileName {
			topsWithManifest[""] = true
			continue
		}
		if base, ok := strings.CutPrefix(name, strings.SplitN(name, "/", 2)[0]+"/"); ok && base == ManifestFileName {
			topsWithManifest[strings.SplitN(name, "/", 2)[0]] = true
		}
	}
	var prefix string
	switch {
	case topsWithManifest[""]:
	case len(topsWithManifest) == 1:
		for top := range topsWithManifest {
			prefix = top + "/"
		}
	default:
		return SkillZIPPeek{}, fmt.Errorf("%s must sit at the archive root or in one wrapper directory", ManifestFileName)
	}
	for _, file := range zr.File {
		if strings.TrimSuffix(file.Name, "/") != prefix+ManifestFileName {
			continue
		}
		if file.FileInfo().IsDir() {
			return SkillZIPPeek{}, errors.New("pogo-skill.json must be a regular file")
		}
		r, err := file.Open()
		if err != nil {
			return SkillZIPPeek{}, err
		}
		data, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
		closeErr := r.Close()
		if err != nil {
			return SkillZIPPeek{}, err
		}
		if closeErr != nil {
			return SkillZIPPeek{}, closeErr
		}
		if len(data) > 1<<20 {
			return SkillZIPPeek{}, errors.New("pogo-skill.json is implausibly large")
		}
		var peek SkillZIPPeek
		if err := json.Unmarshal(data, &peek); err != nil {
			return SkillZIPPeek{}, fmt.Errorf("%s is not readable: %w", ManifestFileName, err)
		}
		if peek.ID == "" || peek.Version == "" {
			return SkillZIPPeek{}, errors.New(ManifestFileName + " lacks id or version")
		}
		return peek, nil
	}
	return SkillZIPPeek{}, fmt.Errorf("Skill ZIP has no %s", ManifestFileName)
}
