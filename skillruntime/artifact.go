package skillruntime

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Session artifacts are capability outputs collected from the bounded
// container output directory and published under a host-generated, private,
// session-scoped store. Callers only ever see opaque artifact IDs; payload
// paths are never derived from container or model input.
var artifactIDPattern = regexp.MustCompile(`^artifact:[0-9a-f]{32}$`)

// Artifact is the public metadata of one published session artifact.
// It deliberately carries no filesystem path.
type Artifact struct {
	ArtifactID string `json:"artifactId"`
	Name       string `json:"name"`
	MediaType  string `json:"mediaType"`
	Digest     string `json:"digest"`
	SizeBytes  int64  `json:"sizeBytes"`
}

// ArtifactContent is returned by BoundSession.ReadArtifact after the web
// layer has authorized the session owner.
type ArtifactContent struct {
	Artifact Artifact
	Data     []byte
}

func (b *BoundSession) artifactSessionDir() string {
	return filepath.Join(b.service.Root, "session-artifacts", strings.TrimPrefix(bytesDigest([]byte(b.SessionID)), "sha256:"))
}

func (b *BoundSession) artifactDir(artifactID string) string {
	hexID := strings.TrimPrefix(artifactID, "artifact:")
	return filepath.Join(b.artifactSessionDir(), hexID[:16], hexID)
}

// matchArtifacts enforces the exact declared set of the provider response
// against the safely collected output files. Undeclared files, missing
// declarations, duplicate names or paths and limit violations all fail the
// call; nothing is published on failure.
func matchArtifacts(declared []DeclaredArtifact, collected []CollectedArtifact) ([]Artifact, [][]byte, error) {
	if len(declared) == 0 && len(collected) == 0 {
		return nil, nil, nil
	}
	byPath := map[string]DeclaredArtifact{}
	byName := map[string]bool{}
	for _, d := range declared {
		key, ok := strings.CutPrefix(d.Path, "./")
		if !ok || key == "" || strings.ContainsAny(key, "\x00\\") || strings.Contains(key, "../") || strings.HasSuffix(key, "/..") || key == ".." {
			return nil, nil, errors.New("OUTPUT_INVALID: artifact declaration has an unsafe path")
		}
		if byName[d.Name] || byPath[key].Name != "" {
			return nil, nil, errors.New("OUTPUT_INVALID: duplicate artifact declaration")
		}
		byName[d.Name] = true
		byPath[key] = d
	}
	arts := make([]Artifact, 0, len(collected))
	blobs := make([][]byte, 0, len(collected))
	var total int64
	for _, c := range collected {
		d, ok := byPath[c.Path]
		if !ok {
			return nil, nil, errors.New("OUTPUT_INVALID: provider wrote an undeclared artifact")
		}
		if len(c.Data) > maxArtifactFileBytes || total+int64(len(c.Data)) > maxArtifactTotalBytes || len(arts) >= maxArtifactFiles {
			return nil, nil, errors.New("OUTPUT_INVALID: artifact limits exceeded")
		}
		total += int64(len(c.Data))
		arts = append(arts, Artifact{ArtifactID: opaqueID("artifact:"), Name: d.Name, MediaType: d.MediaType, Digest: bytesDigest(c.Data), SizeBytes: int64(len(c.Data))})
		blobs = append(blobs, c.Data)
		delete(byPath, c.Path)
	}
	if len(byPath) > 0 {
		return nil, nil, errors.New("TOOL_FAILED: provider declared an artifact it did not write")
	}
	return arts, blobs, nil
}

// publishArtifacts stages every artifact privately and only then moves it
// into the session store, so validation or I/O failures publish nothing.
func (b *BoundSession) publishArtifacts(arts []Artifact, blobs [][]byte) error {
	if len(arts) == 0 {
		return nil
	}
	if len(arts) != len(blobs) || len(arts) > maxArtifactFiles {
		return errors.New("OUTPUT_INVALID: artifact publication set is invalid")
	}
	var total int64
	for _, blob := range blobs {
		total += int64(len(blob))
		if total > maxArtifactTotalBytes {
			return errors.New("OUTPUT_INVALID: artifact publication exceeds total limit")
		}
	}
	base := b.artifactSessionDir()
	if err := os.MkdirAll(base, 0700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(base, ".publish-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	// One opaque batch prefix lets every artifact retain its 128-bit opaque
	// ID while the entire publication becomes one atomic directory rename.
	batch := opaqueID("")[:16]
	for i := range arts {
		if !artifactIDPattern.MatchString(arts[i].ArtifactID) || len(blobs[i]) > maxArtifactFileBytes ||
			bytesDigest(blobs[i]) != arts[i].Digest || int64(len(blobs[i])) != arts[i].SizeBytes {
			return errors.New("OUTPUT_INVALID: artifact payload failed pre-publish verification")
		}
		arts[i].ArtifactID = "artifact:" + batch + strings.TrimPrefix(arts[i].ArtifactID, "artifact:")[16:]
		dir := filepath.Join(staging, strings.TrimPrefix(arts[i].ArtifactID, "artifact:"))
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "payload"), blobs[i], 0600); err != nil {
			return err
		}
		if err := atomicJSON(filepath.Join(dir, "meta.json"), arts[i]); err != nil {
			return err
		}
	}
	target := filepath.Join(base, batch)
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		return errors.New("artifact store collision")
	}
	return os.Rename(staging, target)
}

// ReadArtifact returns one session-owned artifact by opaque ID. The session
// (binding, lock and dataset integrity) is revalidated on every read; an ID
// minted by another session resolves to a different private directory and
// can never be read through this session.
func (b *BoundSession) ReadArtifact(artifactID string) (ArtifactContent, error) {
	var out ArtifactContent
	if !artifactIDPattern.MatchString(artifactID) {
		return out, errors.New("RESOURCE_NOT_AUTHORIZED: unknown artifactId")
	}
	if _, err := b.service.Session(b.Binding, b.Lock, b.SessionID); err != nil {
		return out, err
	}
	dir := b.artifactDir(artifactID)
	for _, path := range []string{b.artifactSessionDir(), filepath.Dir(dir), dir} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return out, errors.New("RESOURCE_NOT_AUTHORIZED: artifact is not owned by this session")
		}
	}
	for _, name := range []string{"meta.json", "payload"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxArtifactFileBytes {
			return out, errors.New("ARTIFACT_INTEGRITY: artifact storage is invalid")
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return out, errors.New("RESOURCE_NOT_AUTHORIZED: artifact is not owned by this session")
	}
	var meta Artifact
	if err := json.Unmarshal(raw, &meta); err != nil || meta.ArtifactID != artifactID {
		return out, errors.New("ARTIFACT_INTEGRITY: artifact metadata unreadable")
	}
	f, err := os.Open(filepath.Join(dir, "payload"))
	if err != nil {
		return out, errors.New("ARTIFACT_INTEGRITY: artifact payload missing")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxArtifactFileBytes+1))
	if err != nil {
		return out, errors.New("ARTIFACT_INTEGRITY: artifact payload unreadable")
	}
	if len(data) > maxArtifactFileBytes || bytesDigest(data) != meta.Digest || int64(len(data)) != meta.SizeBytes {
		return out, errors.New("ARTIFACT_INTEGRITY: artifact payload changed after publish")
	}
	return ArtifactContent{Artifact: meta, Data: data}, nil
}
