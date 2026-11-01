package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var installedID = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[.-][a-z0-9]+)*$`)
var installedVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// Remove deletes only an exact, unreferenced installation. The management
// layer must first remove its authority from the catalog and journal cleanup.
// It is idempotent so an interrupted cleanup can resume on service startup.
func (s *Store) Remove(ref PackageRef) error {
	if !installedID.MatchString(ref.ID) || len(ref.ID) > 128 || !installedVersion.MatchString(ref.Version) || !IsDigest(ref.PackageDigest) || !IsDigest(ref.ManifestDigest) {
		return errors.New("invalid Skill removal reference")
	}
	root, err := filepath.Abs(s.root)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "packages", ref.ID, ref.Version)
	rel, err := filepath.Rel(root, dir)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return errors.New("Skill removal escapes store")
	}
	// Reject symlinked parents before granting owner write permission to this
	// sealed tree. Child links are never followed by removeInstallStage.
	path := root
	for _, part := range []string{"packages", ref.ID, ref.Version} {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("Skill removal requires real store directories")
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "install.json"))
	if err == nil {
		var record installRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		if record.PackageID != ref.ID || record.Version != ref.Version || record.PackageDigest != ref.PackageDigest || record.ManifestDigest != ref.ManifestDigest {
			return errors.New("Skill removal reference does not match installed version")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := removeInstallStage(dir); err != nil {
		return fmt.Errorf("remove Skill files: %w", err)
	}
	return nil
}
