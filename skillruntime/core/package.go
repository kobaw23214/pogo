package core

import (
	"errors"
	"fmt"
	"github.com/yosukeno/pogo/skills"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ManifestFileName is the manifest entry name at the package root.
const ManifestFileName = "pogo-skill.json"

// ReadManifest reads only the current declarative extension. A package must
// independently contain its standard root SKILL.md.
func ReadManifest(dir string) ([]byte, error) {
	return readPackageMetadata(dir, "./"+ManifestFileName)
}

func readPackageMetadata(dir, rel string) ([]byte, error) {
	full, err := resolveExistingPath(dir, rel)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 256<<10 {
		return nil, fmt.Errorf("%s exceeds 256 KiB", rel)
	}
	return b, nil
}

// Package is a verified pkg package: validated manifest, verified
// contract documents and content digests. A Package carries no execution
// capability.
type Package struct {
	Manifest       Manifest
	ManifestDigest string // sha256 digest of the raw manifest bytes
	PackageDigest  string // sha256 digest of the exact outer archive bytes; empty for LoadPackage
	Root           string // package root directory (distribution or installed layout)
	Contracts      map[string]Contract

	// installed describes the verified on-disk state when the package came
	// from a Store. nil for packages loaded with LoadPackage.
	installed *installRecord
}

// ref returns the exact PackageRef for this package.
func (p *Package) ref() PackageRef {
	return PackageRef{
		ID:             p.Manifest.ID,
		Version:        p.Manifest.Version,
		ManifestDigest: p.ManifestDigest,
		PackageDigest:  p.PackageDigest,
	}
}

// safeJoin maps a manifest "./relative/path" onto a filesystem path inside
// root, rejecting any escape. The schema pattern already excludes "..",
// absolute paths and odd characters; this is the defense-in-depth check
// applied at the filesystem boundary.
func safeJoin(root, rel string) (string, error) {
	if !strings.HasPrefix(rel, "./") {
		return "", fmt.Errorf("path %q must start with ./", rel)
	}
	clean := filepath.Clean(rel[2:])
	if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) ||
		strings.Contains(clean, string(filepath.Separator)+".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the package root", rel)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	full := filepath.Join(abs, clean)
	if full != abs && !strings.HasPrefix(full, abs+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the package root", rel)
	}
	return full, nil
}

// resolveExisting resolves rel inside root and additionally rejects symlink
// escapes by evaluating every existing path component.
func resolveExisting(root, rel string) (string, []byte, error) {
	full, err := resolveExistingPath(root, rel)
	if err != nil {
		return "", nil, err
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return "", nil, fmt.Errorf("read %q: %v", rel, err)
	}
	return full, b, nil
}

func resolveExistingPath(root, rel string) (string, error) {
	full, err := safeJoin(root, rel)
	if err != nil {
		return "", err
	}
	// Reject symlinked components along the path; walking each component with
	// Lstat is the escape defense (portable, unlike EvalSymlinks on Windows).
	abs, _ := filepath.Abs(root)
	cur := abs
	for _, seg := range strings.Split(filepath.Clean(rel[2:]), string(filepath.Separator)) {
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		if err != nil {
			return "", fmt.Errorf("missing payload %q: %v", rel, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("path %q contains a symlink component", rel)
		}
	}
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("path %q must be a regular file", rel)
	}
	return full, nil
}

// LoadPackage loads and strictly verifies a package in distribution layout:
// dir contains pogo-skill.json, the referenced contract and Skill files,
// and the payload archives. Every referenced digest (contracts, Skill entry
// presence, runtime/asset artifact bytes, MCP schema snapshots) is verified
// against actual file content; missing payload is a hard failure. Payload
// archives are verified but NOT extracted here, and nothing is executed.
//
// The returned Package has no installation metadata, so RuntimePath and
// AssetPath report that payloads are not extracted.
func LoadPackage(dir string) (*Package, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("package dir: %v", err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("package dir %q is not a directory", dir)
	}
	manifestBytes, err := ReadManifest(dir)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %v", err)
	}
	pkg, err := loadPackageFromBytes(dir, manifestBytes)
	if err != nil {
		return nil, err
	}
	return pkg, nil
}

// loadPackageFromBytes verifies the manifest and every referenced file under
// dir. dir is the package content root (distribution root or the extracted
// "package" tree of an installed version).
func loadPackageFromBytes(dir string, manifestBytes []byte) (*Package, error) {
	m, err := decodeManifest(manifestBytes)
	if err != nil {
		return nil, err
	}
	entry, err := readPackageMetadata(dir, "./SKILL.md")
	if err != nil {
		return nil, err
	}
	name, description, err := skills.PackageMetadata(entry, "")
	if err != nil {
		return nil, err
	}
	m.Skill = Skill{ID: name, DisplayName: m.DisplayName, Description: description, Entry: "./SKILL.md", RequiresCapabilities: m.RequiresCapabilities}
	pkg := &Package{
		Manifest:       *m,
		ManifestDigest: DigestBytes(manifestBytes),
		Root:           dir,
		Contracts:      map[string]Contract{},
	}
	var errs []error

	// Contracts: file must exist, digest must match the published bytes, and
	// the document id/version must match the ref.
	for _, ce := range m.Contracts {
		_, b, err := resolveExisting(dir, ce.Path)
		if err != nil {
			errs = append(errs, fmt.Errorf("contract %s: %v", ce.Ref.ID, err))
			continue
		}
		if err := verifyDigest("contract "+ce.Path, b, ce.Ref.Digest); err != nil {
			errs = append(errs, err)
			continue
		}
		c, err := decodeContract(b)
		if err != nil {
			errs = append(errs, fmt.Errorf("contract %s: %v", ce.Path, err))
			continue
		}
		if c.ID != ce.Ref.ID || c.Version != ce.Ref.Version {
			errs = append(errs, fmt.Errorf(
				"contract %s: document id/version %s@%s does not match ref %s@%s",
				ce.Path, c.ID, c.Version, ce.Ref.ID, ce.Ref.Version))
			continue
		}
		pkg.Contracts[c.ID] = *c
	}

	// Runtimes and assets: payload archives must exist with matching digest
	// and size. tar.zst payloads are verified but never decompressed here.
	for _, r := range m.Runtimes {
		if err := verifyArtifactFile(dir, r.Artifact, "runtime "+r.ID); err != nil {
			errs = append(errs, err)
		}
	}
	for _, a := range m.Assets {
		if err := verifyArtifactFile(dir, a.Artifact, "asset "+a.ID); err != nil {
			errs = append(errs, err)
		}
	}

	// MCP schema snapshots: raw bytes must exist and match the digest.
	for _, p := range m.Providers {
		if p.Driver != DriverMCP || p.SchemaSnapshot == nil {
			continue
		}
		_, b, err := resolveExisting(dir, p.SchemaSnapshot.Path)
		if err != nil {
			errs = append(errs, fmt.Errorf("provider %q schema snapshot: %v", p.ID, err))
			continue
		}
		if err := verifyDigest("schema snapshot "+p.SchemaSnapshot.Path, b, p.SchemaSnapshot.Digest); err != nil {
			errs = append(errs, err)
		}
	}

	if err := crossCheckProviderContracts(m, pkg.Contracts); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return pkg, nil
}

func verifyArtifactFile(dir string, a Artifact, label string) error {
	full, err := resolveExistingPath(dir, a.Path)
	if err != nil {
		return fmt.Errorf("%s payload: %v", label, err)
	}
	f, err := os.Open(full)
	if err != nil {
		return err
	}
	defer f.Close()
	got, size, err := digestFile(f, MaxOuterArchiveBytes)
	if err != nil {
		return fmt.Errorf("%s payload: %w", label, err)
	}
	if size != a.SizeBytes {
		return fmt.Errorf("%s payload %q: size mismatch: got %d bytes, declared %d",
			label, a.Path, size, a.SizeBytes)
	}
	if !IsDigest(a.Digest) || got != a.Digest {
		return fmt.Errorf("%s payload %q: digest mismatch", label, a.Path)
	}
	return nil
}

// RuntimePath returns the verified extracted runtime directory for
// runtimeID. It fails for packages that were not installed through a Store
// (payloads not extracted) and for unknown runtime IDs.
func (p *Package) RuntimePath(runtimeID string) (string, error) {
	found := false
	for _, r := range p.Manifest.Runtimes {
		if r.ID == runtimeID {
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("unknown runtime id %q", runtimeID)
	}
	if p.installed == nil {
		return "", fmt.Errorf("runtime %q payload is not extracted: package was loaded with LoadPackage, not installed via a Store", runtimeID)
	}
	if _, ok := p.installed.Runtimes[runtimeID]; !ok {
		return "", fmt.Errorf("runtime %q has no verified extracted payload", runtimeID)
	}
	return filepath.Join(p.Root, "runtimes", runtimeID), nil
}

// AssetPath returns the verified extracted asset directory for assetID. It
// fails for packages that were not installed through a Store and for unknown
// asset IDs.
func (p *Package) AssetPath(assetID string) (string, error) {
	found := false
	for _, a := range p.Manifest.Assets {
		if a.ID == assetID {
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("unknown asset id %q", assetID)
	}
	if p.installed == nil {
		return "", fmt.Errorf("asset %q payload is not extracted: package was loaded with LoadPackage, not installed via a Store", assetID)
	}
	if _, ok := p.installed.Assets[assetID]; !ok {
		return "", fmt.Errorf("asset %q has no verified extracted payload", assetID)
	}
	return filepath.Join(p.Root, "assets", assetID), nil
}

// AssetDigest returns the verified artifact digest of one installed asset —
// the identity workspace materialization pins its marker to.
func (p *Package) AssetDigest(assetID string) (string, error) {
	for _, a := range p.Manifest.Assets {
		if a.ID == assetID {
			if p.installed == nil {
				return "", fmt.Errorf("asset %q payload is not extracted: package was loaded with LoadPackage, not installed via a Store", assetID)
			}
			d, ok := p.installed.Assets[assetID]
			if !ok {
				return "", fmt.Errorf("asset %q has no verified extracted payload", assetID)
			}
			return d, nil
		}
	}
	return "", fmt.Errorf("unknown asset id %q", assetID)
}
