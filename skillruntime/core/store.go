package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// installRecordFormat is the on-disk metadata format version.
const installRecordFormat = "pogo-skill-install/1"

// installRecord is the persisted verified metadata of one installed pkg
// version. It lets Load re-verify the content tree against the digests
// recorded at install time.
type installRecord struct {
	Format         string            `json:"format"`
	PackageID      string            `json:"packageId"`
	Version        string            `json:"version"`
	ManifestDigest string            `json:"manifestDigest"`
	PackageDigest  string            `json:"packageDigest"`
	Runtimes       map[string]string `json:"runtimes"`
	Assets         map[string]string `json:"assets"`
	Files          map[string]string `json:"files"`
}

// Store is an immutable pkg installation store. Each pkg version is
// staged, fully verified, then atomically published; an installed version is
// never overwritten with different content. A failed install leaves any
// previously installed versions untouched.
type Store struct {
	root string
}

// OpenStore opens (creating if needed) the installation store rooted at root.
func OpenStore(root string) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("store root is empty")
	}
	if err := os.MkdirAll(filepath.Join(root, "packages"), 0o755); err != nil {
		return nil, fmt.Errorf("create store root: %v", err)
	}
	return &Store{root: root}, nil
}

// versionDir returns the install directory of one pkg version.
func (s *Store) versionDir(id, version string) string {
	return filepath.Join(s.root, "packages", id, version)
}

// ErrUnsupportedArchiveFormat reports a payload format this core cannot
// extract. v1 installation supports ZIP payloads only; tar.zst is parsed
// and digest-verified but MUST NOT be piped through any decompressor shell.
var ErrUnsupportedArchiveFormat = errors.New("unsupported payload archive format")

// ErrVersionConflict reports an attempt to install different content under
// an already installed id+version.
var ErrVersionConflict = errors.New("Skill version already installed with different content")

// InstallZIP installs one pkg distribution archive (the outer ZIP
// containing pogo-skill.json, contracts, skills, snapshots and payload
// ZIPs). The archive is staged, fully verified, then atomically published.
// On any failure the staged content is removed and previously installed
// versions are untouched.
//
// Reinstalling byte-identical content is a no-op returning the installed
// package. Installing different content under the same id+version fails
// with ErrVersionConflict.
//
// There are no install hooks and no shell execution at any point.
func (s *Store) InstallZIP(sourceArchive string) (installed *Package, retErr error) {
	runtime, err := InspectSkillZIP(sourceArchive)
	if err != nil {
		return nil, err
	}
	if !runtime {
		return nil, errors.New("Skill has no pogo-skill.json runtime declaration")
	}
	archiveFile, err := os.Open(sourceArchive)
	if err != nil {
		return nil, fmt.Errorf("read source archive: %v", err)
	}
	defer archiveFile.Close()
	packageDigest, _, err := digestFile(archiveFile, MaxOuterArchiveBytes)
	if err != nil {
		return nil, fmt.Errorf("outer archive: %w", err)
	}
	budget := &zipBudget{}
	unpackRoot, err := os.MkdirTemp(s.root, ".staging-")
	if err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}
	defer func() {
		if err := removeInstallStage(unpackRoot); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("clean unpack staging dir: %w", err))
		}
	}()
	pkgStage := filepath.Join(unpackRoot, "package")
	if err := os.MkdirAll(pkgStage, 0o755); err != nil {
		return nil, err
	}
	pkgFiles, err := extractZIPFile(archiveFile, pkgStage, budget, "outer archive")
	if err != nil {
		return nil, err
	}
	// A single enclosing folder is accepted just like an ordinary Skill ZIP.
	entries, err := os.ReadDir(pkgStage)
	if err != nil {
		return nil, err
	}
	if len(entries) == 1 && entries[0].IsDir() {
		prefix := entries[0].Name() + "/"
		pkgStage = filepath.Join(pkgStage, entries[0].Name())
		unwrapped := make(map[string]extractedFile, len(pkgFiles))
		for name, file := range pkgFiles {
			if !strings.HasPrefix(name, prefix) {
				return nil, errors.New("invalid Skill ZIP wrapper")
			}
			unwrapped[strings.TrimPrefix(name, prefix)] = file
		}
		pkgFiles = unwrapped
	}
	manifestBytes, err := ReadManifest(pkgStage)
	if err != nil {
		return nil, fmt.Errorf("outer archive lacks %s at its root: %v", ManifestFileName, err)
	}
	pkg, err := loadPackageFromBytes(pkgStage, manifestBytes)
	if err != nil {
		return nil, err
	}
	pkg.PackageDigest = packageDigest
	m := &pkg.Manifest
	finalDir := s.versionDir(m.ID, m.Version)
	if existing, err := s.loadVersionDir(finalDir); err == nil {
		if existing.PackageDigest == packageDigest {
			return existing, nil
		}
		return nil, fmt.Errorf("%w: %s@%s", ErrVersionConflict, m.ID, m.Version)
	}
	// Linux requires write permission on a directory moved between parents
	// (its ".." entry changes). Stage beside the final version before sealing
	// so a non-root service can atomically publish the fully read-only tree.
	if err := os.MkdirAll(filepath.Dir(finalDir), 0o755); err != nil {
		return nil, fmt.Errorf("create Skill version parent: %w", err)
	}
	stageRoot, err := os.MkdirTemp(filepath.Dir(finalDir), ".staging-")
	if err != nil {
		return nil, fmt.Errorf("create publish staging dir: %w", err)
	}
	published := false
	defer func() {
		if !published {
			if err := removeInstallStage(stageRoot); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("clean publish staging dir: %w", err))
			}
		}
	}()
	stagedPackage := filepath.Join(stageRoot, "package")
	if err := os.Rename(pkgStage, stagedPackage); err != nil {
		return nil, fmt.Errorf("stage verified package: %w", err)
	}
	pkgStage = stagedPackage
	record := &installRecord{
		Format:         installRecordFormat,
		PackageID:      m.ID,
		Version:        m.Version,
		ManifestDigest: pkg.ManifestDigest,
		PackageDigest:  packageDigest,
		Runtimes:       map[string]string{},
		Assets:         map[string]string{},
		Files:          map[string]string{},
	}
	for rel, d := range pkgFiles {
		record.Files[filepath.Join("package", filepath.FromSlash(rel))] = d.Digest
	}
	extractPayload := func(a Artifact, subdir, id string) (map[string]extractedFile, error) {
		if a.Format != "zip" {
			return nil, fmt.Errorf("%w: %q (format %s); only zip payloads are installable in this core, tar.zst must not reach any decompressor",
				ErrUnsupportedArchiveFormat, a.Path, a.Format)
		}
		full, err := resolveExistingPath(pkgStage, a.Path)
		if err != nil {
			return nil, err
		}
		f, err := os.Open(full)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		dest := filepath.Join(stageRoot, subdir, id)
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return nil, err
		}
		return extractZIPFile(f, dest, budget, "payload "+a.Path)
	}
	runtimeFiles := map[string]map[string]extractedFile{}
	for _, r := range m.Runtimes {
		files, err := extractPayload(r.Artifact, "runtimes", r.ID)
		if err != nil {
			return nil, fmt.Errorf("runtime %q: %w", r.ID, err)
		}
		record.Runtimes[r.ID] = r.Artifact.Digest
		runtimeFiles[r.ID] = files
		for rel, d := range files {
			record.Files[filepath.Join("runtimes", r.ID, filepath.FromSlash(rel))] = d.Digest
		}
	}
	for _, a := range m.Assets {
		files, err := extractPayload(a.Artifact, "assets", a.ID)
		if err != nil {
			return nil, fmt.Errorf("asset %q: %w", a.ID, err)
		}
		record.Assets[a.ID] = a.Artifact.Digest
		for rel, d := range files {
			record.Files[filepath.Join("assets", a.ID, filepath.FromSlash(rel))] = d.Digest
		}
	}
	for _, p := range m.Providers {
		if p.Driver != DriverProcessJSON {
			continue
		}
		entry := strings.TrimPrefix(p.Entrypoint, "./")
		if _, ok := record.Runtimes[p.RuntimeID]; !ok {
			return nil, fmt.Errorf("provider %q: runtime %q not extracted", p.ID, p.RuntimeID)
		}
		f, ok := runtimeFiles[p.RuntimeID][entry]
		if !ok {
			return nil, fmt.Errorf("provider %q: entrypoint %q missing from runtime payload", p.ID, p.Entrypoint)
		}
		if !f.Exec {
			return nil, fmt.Errorf("provider %q: entrypoint %q lacks the executable bit in the runtime payload", p.ID, p.Entrypoint)
		}
	}
	recBytes, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(stageRoot, "install.json"), recBytes, 0o644); err != nil {
		return nil, err
	}
	if err := makeReadOnly(stageRoot); err != nil {
		return nil, fmt.Errorf("seal staged install: %w", err)
	}
	if err := os.Rename(stageRoot, finalDir); err != nil {
		return nil, fmt.Errorf("publish staged install: %w", err)
	}
	published = true
	installed, err = s.loadVersionDir(finalDir)
	if err != nil {
		return nil, fmt.Errorf("reload after publish: %v", err)
	}
	return installed, nil
}

// Load returns the installed package exactly matching ref: id, version,
// manifest digest and package digest must all match the persisted verified
// metadata, and the whole recorded content tree is re-verified against the
// digests recorded at install time.
func (s *Store) Load(ref PackageRef) (*Package, error) {
	dir := s.versionDir(ref.ID, ref.Version)
	pkg, err := s.loadVersionDir(dir)
	if err != nil {
		return nil, err
	}
	if pkg.ManifestDigest != ref.ManifestDigest {
		return nil, fmt.Errorf("manifest digest mismatch for %s@%s: store has %s, ref wants %s",
			ref.ID, ref.Version, pkg.ManifestDigest, ref.ManifestDigest)
	}
	if pkg.PackageDigest != ref.PackageDigest {
		return nil, fmt.Errorf("package digest mismatch for %s@%s: store has %s, ref wants %s",
			ref.ID, ref.Version, pkg.PackageDigest, ref.PackageDigest)
	}
	return pkg, nil
}

// loadVersionDir loads and re-verifies one installed version directory.
func (s *Store) loadVersionDir(dir string) (*Package, error) {
	recBytes, err := os.ReadFile(filepath.Join(dir, "install.json"))
	if err != nil {
		return nil, fmt.Errorf("not an installed version directory %q: %v", dir, err)
	}
	var rec installRecord
	dec := json.NewDecoder(bytes.NewReader(recBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return nil, fmt.Errorf("corrupt install.json in %q: %v", dir, err)
	}
	if rec.Format != installRecordFormat {
		return nil, fmt.Errorf("install.json in %q has unknown format %q", dir, rec.Format)
	}
	var errs []error
	seen := map[string]bool{}
	for rel := range rec.Files {
		normalized, pathErr := normalizeZipName(filepath.ToSlash(rel))
		if pathErr != nil || normalized != filepath.ToSlash(rel) {
			return nil, fmt.Errorf("invalid installed record path %q", rel)
		}
	}
	// A single non-following traversal checks every ancestor and every file.
	// Re-walking all path prefixes per file makes large native bundles spend
	// most of their time in repeated metadata syscalls on Windows.
	for _, sub := range []string{"package", "runtimes", "assets"} {
		base := filepath.Join(dir, sub)
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		}
		walkErr := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 || (!d.IsDir() && !d.Type().IsRegular()) {
				return fmt.Errorf("installed tree contains a symlink or special file: %s", path)
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			want, recorded := rec.Files[rel]
			if !recorded {
				errs = append(errs, fmt.Errorf("unrecorded extra file %q in installed tree", rel))
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			got, _, hashErr := digestFile(f, MaxExtractedTotalBytes)
			_ = f.Close()
			if hashErr != nil || !IsDigest(want) || got != want {
				errs = append(errs, fmt.Errorf("installed file %q digest mismatch or unreadable content: %v", rel, hashErr))
			}
			seen[rel] = true
			return nil
		})
		if walkErr != nil {
			errs = append(errs, walkErr)
		}
	}
	for rel := range rec.Files {
		if !seen[rel] {
			errs = append(errs, fmt.Errorf("installed file %q is missing or unreadable", rel))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("installed content of %q failed verification: %v", dir, err)
	}
	manifestBytes, err := ReadManifest(filepath.Join(dir, "package"))
	if err != nil {
		return nil, fmt.Errorf("installed manifest unreadable in %q: %v", dir, err)
	}
	pkg, err := loadPackageFromBytes(filepath.Join(dir, "package"), manifestBytes)
	if err != nil {
		return nil, fmt.Errorf("installed manifest of %q failed validation: %v", dir, err)
	}
	if pkg.ManifestDigest != rec.ManifestDigest {
		return nil, fmt.Errorf("manifest digest mismatch in %q", dir)
	}
	pkg.PackageDigest = rec.PackageDigest
	pkg.Root = dir
	pkg.installed = &rec
	return pkg, nil
}

// makeReadOnly marks the whole tree read-only. Files that were
// executable stay executable (0o555); other files become 0o444; directories
// become 0o555. On Windows this maps to the read-only attribute, which the
// owning user can trivially clear — callers MUST enforce true OS isolation
// (separate user, container or sandbox) against same-user writes.
func makeReadOnly(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.Chmod(path, 0o555)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if fi.Mode()&0o100 != 0 {
			return os.Chmod(path, 0o555)
		}
		return os.Chmod(path, 0o444)
	})
}

// removeInstallStage only operates on this install's private temporary tree.
// Restore owner write access before removal, including after partial sealing.
// WalkDir does not follow links; never chmod a link target during cleanup.
func removeInstallStage(root string) error {
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return nil
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			return os.Chmod(path, 0o700)
		}
		return os.Chmod(path, 0o600)
	})
	if err != nil {
		return err
	}
	return os.RemoveAll(root)
}
