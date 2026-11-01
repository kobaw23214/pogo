package skillruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/yosukeno/pogo/skillruntime/core"
)

// Workspace materialization: a package may declare document-type assets
// (workspaceData in pogo-skill.json) that every Agent binding the Skill needs
// as plain files — report libraries the docs skill opens, reference pages.
// Providers stay the query path for structured data; this is the file path,
// filled in when a bound conversation is created so the data reaches exactly
// the Agents that selected the Skill, nobody else.
//
// The trust story is deliberately narrow: only assets the manifest explicitly
// lists, only into the conversation's own workspace, capped in total size,
// extracted without following links, and marked read-only best effort. A
// marker file records the asset digest so re-creating a conversation is a
// stat, and a package upgrade re-materializes in one pass.

const (
	// workspaceDataMarker is the file written inside a materialized
	// destination; its presence with the same digest is the skip condition.
	workspaceDataMarker = ".skill-workspace-data.json"
	// defaultWorkspaceDataMaxBytes caps what one binding may materialize.
	defaultWorkspaceDataMaxBytes = int64(512 << 20)
)

type workspaceMarker struct {
	PackageID     string `json:"packageId"`
	AssetID       string `json:"assetId"`
	AssetDigest   string `json:"assetDigest"`
	ManifestDigest string `json:"manifestDigest"`
}

// MaterializeWorkspaceData copies every workspaceData asset the binding's
// packages declare into workspaceDir. Idempotent and version-aware: a
// destination whose marker matches the installed asset digest is left alone,
// anything else (missing, stale, hand-removed) is rebuilt from the store.
// The binding is the caller's verified SkillBinding JSON.
func (s *Service) MaterializeWorkspaceData(binding []byte, workspaceDir string) error {
	if s == nil {
		return errors.New("skill runtime unavailable")
	}
	if len(binding) == 0 || string(workspaceDir) == "" {
		return nil
	}
	r, err := s.resolve(binding)
	if err != nil {
		return err
	}
	for _, pkg := range r.packages {
		for _, w := range pkg.Manifest.WorkspaceData {
			if err := s.materializeOne(pkg, w, workspaceDir); err != nil {
				return fmt.Errorf("workspace data %s/%s: %w", pkg.Manifest.ID, w.Dest, err)
			}
		}
	}
	return nil
}

func (s *Service) materializeOne(pkg *core.Package, w core.WorkspaceDataEntry, workspaceDir string) error {
	assetDigest, err := pkg.AssetDigest(w.AssetID)
	if err != nil {
		return err
	}
	src, err := pkg.AssetPath(w.AssetID)
	if err != nil {
		return err
	}
	dest := filepath.Join(workspaceDir, w.Dest)
	markerPath := filepath.Join(dest, workspaceDataMarker)

	if data, err := os.ReadFile(markerPath); err == nil {
		var m workspaceMarker
		if json.Unmarshal(data, &m) == nil &&
			m.PackageID == pkg.Manifest.ID && m.AssetID == w.AssetID &&
			m.AssetDigest == assetDigest && m.ManifestDigest == pkg.ManifestDigest {
			return nil // current copy already materialized
		}
	}
	if err := os.RemoveAll(dest); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	// Walk the extracted asset dir. The store layout is digest-verified plain
	// files, but the walk stays defensive: nothing but regular files and
	// directories crosses, with a hard ceiling on copied bytes.
	maxBytes := s.workspaceDataMaxBytes()
	copied := int64(0)
	walkErr := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dest, filepath.FromSlash(rel)), 0o755)
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular entry %q", rel)
		}
		copied += info.Size()
		if copied > maxBytes {
			return fmt.Errorf("exceeds the %d byte workspace-data budget", maxBytes)
		}
		return copyFileReadonly(path, filepath.Join(dest, filepath.FromSlash(rel)))
	})
	if walkErr != nil {
		return walkErr
	}
	marker := workspaceMarker{
		PackageID: pkg.Manifest.ID, AssetID: w.AssetID,
		AssetDigest: assetDigest, ManifestDigest: pkg.ManifestDigest,
	}
	raw, err := json.MarshalIndent(marker, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(markerPath, append(raw, '\n'), 0o644)
}

func copyFileReadonly(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o444)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func (s *Service) workspaceDataMaxBytes() int64 {
	if s.WorkspaceDataMaxBytes > 0 {
		return s.WorkspaceDataMaxBytes
	}
	return defaultWorkspaceDataMaxBytes
}
