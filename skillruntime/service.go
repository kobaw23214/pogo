package skillruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/yosukeno/pogo/skillruntime/core"
)

type Installed struct {
	Ref              core.PackageRef `json:"ref"`
	Name             string          `json:"name"`
	Revoked          bool            `json:"revoked"`
	Skill            string          `json:"skill"`
	Capabilities     []string        `json:"capabilities,omitempty"`
	RuntimeCount     *int            `json:"runtime_count,omitempty"`
	AssetCount       *int            `json:"asset_count,omitempty"`
	PackageSizeBytes int64           `json:"package_size_bytes,omitempty"`
	InstalledAt      string          `json:"installed_at,omitempty"`
}

// Service owns trusted installation state. Its root must be outside every
// ordinary Agent workspace; only administrator routes may import packages.
type Service struct {
	Root               string
	Store              *core.Store
	Executor           Executor
	ExecutionIssue     string
	ExecutionIssueCode string
	platform           *platformState
	mu                 sync.Mutex
	installed          []Installed
	active             map[string]map[string]context.CancelFunc
	catalogMu          sync.Mutex
	metadata           map[core.PackageRef]*core.Package
	// WorkspaceDataMaxBytes caps the total bytes one binding may materialize
	// into a workspace (workspaceData manifests); 0 selects the default.
	WorkspaceDataMaxBytes int64
}

func NewService(ctx context.Context, root string) (*Service, error) {
	s, err := NewManagementService(root)
	if err != nil {
		return nil, err
	}
	s.Executor, err = NewLocalExecutor(ctx, s.Root)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// NewManagementService installs and catalogs verified packages without running
// their programs. Execution remains unavailable until an executor is configured.
func NewManagementService(root string) (*Service, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("SKILL_RUNTIME_STORE_REQUIRED")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	store, err := core.OpenStore(filepath.Join(abs, "packages"))
	if err != nil {
		return nil, err
	}
	platform := currentPlatform()
	s := &Service{Root: abs, Store: store, platform: &platform, installed: []Installed{}, active: map[string]map[string]context.CancelFunc{}}
	raw, err := os.ReadFile(filepath.Join(abs, "catalog.json"))
	if err == nil {
		if err = json.Unmarshal(raw, &s.installed); err != nil {
			return nil, fmt.Errorf("Skill catalog: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := s.recoverDeletes(); err != nil {
		return nil, err
	}
	return s, nil
}

func atomicJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicBytes(path, raw)
}

func atomicBytes(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (s *Service) List() []Installed {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Installed, 0, len(s.installed))
	for _, entry := range s.installed {
		out = append(out, cloneInstalled(entry))
	}
	return out
}

func cloneInstalled(entry Installed) Installed {
	entry.Capabilities = append([]string(nil), entry.Capabilities...)
	if entry.RuntimeCount != nil {
		n := *entry.RuntimeCount
		entry.RuntimeCount = &n
	}
	if entry.AssetCount != nil {
		n := *entry.AssetCount
		entry.AssetCount = &n
	}
	return entry
}

func (s *Service) requireExecutor() error {
	if s != nil && s.Executor == nil {
		status := s.ExecutionStatus()
		if status.IssueCode != "EXECUTOR_REQUIRED" {
			return &ExecutionError{Code: status.IssueCode, Message: status.Issue}
		}
	}
	if s == nil || s.Executor == nil {
		return errors.New("HOST_CAPABILITY_MISSING: Skill execution is not configured; package management remains available")
	}
	return nil
}

func (s *Service) Import(reader io.Reader) (Installed, error) {
	var out Installed
	temp, err := os.CreateTemp(s.Root, ".upload-*.zip")
	if err != nil {
		return out, err
	}
	defer os.Remove(temp.Name())
	// The outer cap is host policy (0 = unlimited): the copy streams to disk
	// either way, the LimitReader just adds the tripwire when a cap is set.
	var src io.Reader = reader
	if core.MaxOuterArchiveBytes > 0 {
		src = io.LimitReader(reader, core.MaxOuterArchiveBytes+1)
	}
	n, err := io.Copy(temp, src)
	closeErr := temp.Close()
	if err != nil {
		return out, err
	}
	if closeErr != nil {
		return out, closeErr
	}
	if core.MaxOuterArchiveBytes > 0 && n > core.MaxOuterArchiveBytes {
		return out, fmt.Errorf("Skill ZIP exceeds %d MiB", core.MaxOuterArchiveBytes>>20)
	}
	return s.ImportFile(temp.Name())
}

// ImportFile consumes a host-staged Skill ZIP without a second upload copy.
// The path must remain in the private store; it is never a request parameter.
func (s *Service) ImportFile(archive string) (Installed, error) {
	var out Installed
	if _, err := confinedPath(s.Root, archive); err != nil {
		return out, err
	}
	info, err := os.Lstat(archive)
	if err != nil {
		return out, err
	}
	if !info.Mode().IsRegular() {
		return out, errors.New("Skill upload must be a regular file")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.recoverDeletes(); err != nil {
		return out, err
	}
	pkg, err := s.Store.InstallZIP(archive)
	if err != nil {
		return out, err
	}
	out = Installed{Ref: core.PackageRef{ID: pkg.Manifest.ID, Version: pkg.Manifest.Version, ManifestDigest: pkg.ManifestDigest, PackageDigest: pkg.PackageDigest}, Name: pkg.Manifest.DisplayName}
	out.PackageSizeBytes = info.Size()
	out.InstalledAt = time.Now().UTC().Format(time.RFC3339)
	runtimes, assets := len(pkg.Manifest.Runtimes), len(pkg.Manifest.Assets)
	out.RuntimeCount, out.AssetCount = &runtimes, &assets
	if pkg.Manifest.Skill.ID != "" {
		out.Skill = pkg.Manifest.Skill.ID
	}
	for _, contract := range pkg.Manifest.Contracts {
		out.Capabilities = append(out.Capabilities, contract.Ref.ID)
	}
	for _, old := range s.installed {
		if old.Ref == out.Ref {
			return cloneInstalled(old), nil
		}
		existing, loadErr := s.Store.Load(old.Ref)
		if loadErr != nil {
			return out, errors.New("CONTRACT_REGISTRY_UNAVAILABLE: an installed version failed integrity validation")
		}
		if err := consistentContracts(existing.Manifest.Contracts, pkg.Manifest.Contracts); err != nil {
			return out, err
		}
	}
	next := append(append([]Installed{}, s.installed...), out)
	if err = atomicJSON(filepath.Join(s.Root, "catalog.json"), next); err != nil {
		return out, err
	}
	s.installed = next
	return cloneInstalled(out), nil
}

// Import is the administrator's approval boundary for a new contract. Once an
// ID/version has been accepted, another pkg cannot redefine its bytes.
func consistentContracts(current, incoming []core.ManifestContractEntry) error {
	for _, next := range incoming {
		for _, old := range current {
			if old.Ref.ID == next.Ref.ID && old.Ref.Version == next.Ref.Version && old.Ref.Digest != next.Ref.Digest {
				return fmt.Errorf("CONTRACT_VERSION_CONFLICT: %s@%s already has a different content digest", next.Ref.ID, next.Ref.Version)
			}
		}
	}
	return nil
}

func (s *Service) enabled(ref core.PackageRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabledLocked(ref)
}
func (s *Service) enabledLocked(ref core.PackageRef) error {
	for _, entry := range s.installed {
		if entry.Ref == ref {
			if entry.Revoked {
				return errors.New("REVOKED: Skill version is disabled")
			}
			return nil
		}
	}
	return errors.New("PROVIDER_NOT_READY: package was not imported by this host")
}
func (s *Service) Revoke(digest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := append([]Installed{}, s.installed...)
	found := false
	for i := range next {
		if next[i].Ref.PackageDigest == digest {
			next[i].Revoked = true
			found = true
		}
	}
	if !found {
		return errors.New("unknown package digest")
	}
	if err := atomicJSON(filepath.Join(s.Root, "catalog.json"), next); err != nil {
		return err
	}
	s.installed = next
	for _, cancel := range s.active[digest] {
		cancel()
	}
	return nil
}

func NormalizeBinding(raw json.RawMessage, agentID string, revision int) (json.RawMessage, error) {
	b, err := core.DecodeBinding(raw)
	if err != nil {
		return nil, err
	}
	b.AgentID = agentID
	b.Revision = revision
	return json.Marshal(b)
}

type resolvedCapability struct {
	choice   core.CapabilityChoice
	provider core.Provider
	contract core.Contract
	pkg      *core.Package
	grant    core.Grant
}

type resolvedBinding struct {
	binding      core.Binding
	packages     map[string]*core.Package
	capabilities []resolvedCapability
}

// slotKindOf returns the manifest kind of one resource slot ("" if absent).
func slotKindOf(pkg *core.Package, slotID string) string {
	for _, slot := range pkg.Manifest.ResourceSlots {
		if slot.ID == slotID {
			return slot.Kind
		}
	}
	return ""
}

// hostCeiling limits the local supervisor's admitted budgets. The container
// provides the shared hard ceiling; these are not per-call cgroup guarantees.
func hostCeiling(profile string, l core.Limits) error {
	if profile != LocalProfile {
		return errors.New("RESOURCE_NOT_AUTHORIZED: unsupported isolation profile")
	}
	if l.MemoryMiB > 8192 || l.CPUMilliCores > 4000 || l.TimeoutSeconds > 900 || l.MaxOutputBytes > 4<<20 {
		return errors.New("RESOURCE_NOT_AUTHORIZED: grant exceeds local sandbox host ceiling")
	}
	return nil
}

func (s *Service) resolve(raw []byte) (*resolvedBinding, error) {
	b, err := core.DecodeBinding(raw)
	if err != nil {
		return nil, err
	}
	r := &resolvedBinding{binding: b, packages: map[string]*core.Package{}}
	packages := []*core.Package{}
	for _, ref := range b.Packages {
		if err := s.enabled(ref); err != nil {
			return nil, err
		}
		// Verified content comes from the catalog cache: install verifies
		// every byte once, the first resolve after that pays Store.Load's
		// full re-hash, and every later resolve reuses the verified package —
		// a multi-GB bundle must not be re-hashed on every session open or
		// capability call. Deletion evicts the cache; revocation is the
		// enabled() check above, re-evaluated on every resolve.
		pkg, err := s.catalogPackage(ref)
		if err != nil {
			return nil, err
		}
		r.packages[ref.ID] = pkg
		packages = append(packages, pkg)
	}
	if err := core.ValidateBinding(b, packages); err != nil {
		return nil, err
	}
	for _, resource := range b.Resources {
		pkg := r.packages[resource.PackageID]
		switch slotKindOf(pkg, resource.SlotID) {
		case "files":
			if resource.ResourceRef != "files:current-session" {
				return nil, errors.New("UNSUPPORTED_RESOURCE: files slots only support session-owned files")
			}
		case "artifacts":
			// Artifact output directories are generated per call by the
			// host; the binding must use the reserved opaque ref and can
			// never point at host storage.
			if resource.ResourceRef != "artifacts:session-private" {
				return nil, errors.New("UNSUPPORTED_RESOURCE: artifacts slots are host-generated per call")
			}
		default:
			return nil, errors.New("RESOURCE_NOT_AUTHORIZED: unsupported resource slot kind")
		}
	}
	for _, choice := range b.Capabilities {
		pkg := r.packages[choice.Provider.PackageID]
		var p core.Provider
		for _, candidate := range pkg.Manifest.Providers {
			if candidate.ID == choice.Provider.ProviderID {
				p = candidate
				break
			}
		}
		var grant core.Grant
		for _, candidate := range b.Grants {
			if candidate.Provider == choice.Provider {
				grant = candidate
				break
			}
		}
		c := pkg.Contracts[choice.Contract.ID]
		if p.Driver != "process-json" || c.Effects.MutatesExternalState {
			return nil, errors.New("UNSUPPORTED_EFFECT: prototype enables process-json analyses without external mutation")
		}
		if grant.Mode != "sandbox" || grant.Ceiling == nil || grant.Ceiling.Network != "none" {
			return nil, errors.New("RESOURCE_NOT_AUTHORIZED: unsupported isolation grant")
		}
		if err := hostCeiling(grant.IsolationProfileRef, grant.Ceiling.Limits); err != nil {
			return nil, err
		}
		if err := s.checkExecutionProfile(grant.IsolationProfileRef); err != nil {
			return nil, err
		}
		for _, runtime := range pkg.Manifest.Runtimes {
			if runtime.ID == p.RuntimeID {
				if issue := runtimePlatformIssue(runtime.Platform, s.executionPlatform()); issue != nil {
					return nil, issue
				}
			}
		}
		r.capabilities = append(r.capabilities, resolvedCapability{choice: choice, provider: p, contract: c, pkg: pkg, grant: grant})
	}
	return r, nil
}

func limitsOf(g core.Grant) ExecutorLimits {
	l := g.Ceiling.Limits
	return ExecutorLimits{l.TimeoutSeconds, l.MemoryMiB, l.CPUMilliCores, l.MaxOutputBytes}
}

func (s *Service) ValidateBinding(raw []byte) error { _, err := s.resolve(raw); return err }

// Prepare verifies the fixed ABI before a new conversation is made visible.
func (s *Service) Prepare(ctx context.Context, raw []byte) error {
	if err := s.requireExecutor(); err != nil {
		return err
	}
	r, err := s.resolve(raw)
	if err != nil {
		return err
	}
	for _, cap := range r.capabilities {
		dir, err := cap.pkg.RuntimePath(cap.provider.RuntimeID)
		if err != nil {
			return err
		}
		id := opaqueID("call:")
		request := ProcessRequest{ProtocolVersion: "pogo.tool/1", CallID: id, Type: ProcessRequestDescribe}
		ref := core.PackageRef{ID: cap.pkg.Manifest.ID, Version: cap.pkg.Manifest.Version, ManifestDigest: cap.pkg.ManifestDigest, PackageDigest: cap.pkg.PackageDigest}
		out, err := s.Executor.Execute(ctx, Execution{Package: ref, ProviderID: cap.provider.ID, RuntimeDir: dir, Entrypoint: cap.provider.Entrypoint, Request: request, Limits: limitsOf(cap.grant), IsolationProfile: cap.grant.IsolationProfileRef})
		if err != nil {
			return err
		}
		if err = core.ValidateDocument("process-response", out); err != nil {
			return err
		}
		var response ProcessResponse
		if err = json.Unmarshal(out, &response); err != nil {
			return err
		}
		matched := false
		for _, op := range response.Operations {
			if op.Operation == cap.provider.Operation && op.Contract == (ContractIdentity{cap.choice.Contract.ID, cap.choice.Contract.Version, cap.choice.Contract.Digest}) {
				matched = true
			}
		}
		if response.CallID != id || response.Type != "describe" || !matched {
			return errors.New("CONTRACT_MISMATCH: provider handshake")
		}
	}
	return nil
}

func (s *Service) MakeLock(raw []byte, sessionID string) (json.RawMessage, error) {
	if err := s.requireExecutor(); err != nil {
		return nil, err
	}
	r, err := s.resolve(raw)
	if err != nil {
		return nil, err
	}
	return s.makeResolvedLock(r, sessionID)
}

func (s *Service) makeResolvedLock(r *resolvedBinding, sessionID string) (json.RawMessage, error) {
	if err := s.requireExecutor(); err != nil {
		return nil, err
	}
	b := r.binding
	// The registry may pretty-print an embedded RawMessage. Publish a separate
	// immutable host-normalized document so its exact bytes remain authoritative.
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	bindingDir := filepath.Join(s.Root, "bindings")
	if err = os.MkdirAll(bindingDir, 0700); err != nil {
		return nil, err
	}
	bindingPath := filepath.Join(bindingDir, strings.TrimPrefix(bytesDigest(raw), "sha256:")+".json")
	if old, readErr := os.ReadFile(bindingPath); readErr == nil {
		if bytesDigest(old) != bytesDigest(raw) {
			return nil, errors.New("binding content was modified")
		}
	} else if os.IsNotExist(readErr) {
		if err = atomicBytes(bindingPath, raw); err != nil {
			return nil, err
		}
	} else {
		return nil, readErr
	}
	registryBytes, err := json.Marshal(b.Packages)
	if err != nil {
		return nil, err
	}
	policy := "local-bwrap-v1-supervised"
	l := core.Lock{APIVersion: core.APIVersion, Kind: "ResolvedLock", LockID: opaqueID("lock:"), AgentID: b.AgentID, SessionID: "session:" + sessionID,
		BindingRevision: b.Revision, BindingDigest: bytesDigest(raw), PolicyRevision: policy + ":" + strings.TrimPrefix(s.Executor.Identity(), "sha256:"),
		RegistryRevision: bytesDigest(registryBytes), Drivers: []core.DriverVersion{{ID: "bundle", Version: "1"}, {ID: "process-json", Version: "1"}},
		Packages: b.Packages, Skills: []core.LockSkill{}, Capabilities: b.Capabilities, Runtimes: []core.LockRuntime{}, Assets: []core.LockAsset{}, Resources: []core.LockResource{},
		Connections: []core.LockConnection{}, EffectiveGrants: b.Grants, CreatedBy: "pogo-host"}
	for _, sr := range b.Skills {
		pkg := r.packages[sr.PackageID]
		if skill := pkg.Manifest.Skill; skill.ID == sr.SkillID {
			data, err := pkg.ReadMetadata(skill.Entry)
			if err != nil {
				return nil, err
			}
			l.Skills = append(l.Skills, core.LockSkill{Skill: sr, EntryDigest: bytesDigest(data)})
		}
	}
	seenRuntime := map[string]bool{}
	seenAsset := map[string]bool{}
	for _, cap := range r.capabilities {
		for _, runtime := range cap.pkg.Manifest.Runtimes {
			key := cap.pkg.Manifest.ID + "/" + runtime.ID
			if runtime.ID == cap.provider.RuntimeID && !seenRuntime[key] {
				seenRuntime[key] = true
				l.Runtimes = append(l.Runtimes, core.LockRuntime{PackageID: cap.pkg.Manifest.ID, RuntimeID: runtime.ID, ArtifactDigest: runtime.Artifact.Digest})
			}
		}
		for _, asset := range cap.pkg.Manifest.Assets {
			for _, id := range cap.provider.AssetIDs {
				key := cap.pkg.Manifest.ID + "/" + id
				if asset.ID == id && !seenAsset[key] {
					seenAsset[key] = true
					l.Assets = append(l.Assets, core.LockAsset{PackageID: cap.pkg.Manifest.ID, AssetID: id, ArtifactDigest: asset.Artifact.Digest})
				}
			}
		}
	}
	for _, resource := range b.Resources {
		switch {
		case resource.ResourceRef == "artifacts:session-private":
			l.Resources = append(l.Resources, core.LockResource{PackageID: resource.PackageID, SlotID: resource.SlotID, ResourceRef: resource.ResourceRef, ResourceRevision: "session-artifacts:v1", AuthorizationRevision: "session-owner:v1"})
		default:
			l.Resources = append(l.Resources, core.LockResource{PackageID: resource.PackageID, SlotID: resource.SlotID, ResourceRef: resource.ResourceRef, ResourceRevision: "session-files:v1", AuthorizationRevision: "session-owner:v1"})
		}
	}
	data, err := json.Marshal(l)
	if err != nil {
		return nil, err
	}
	if err = core.ValidateDocument("resolved-lock", data); err != nil {
		return nil, err
	}
	return data, nil
}

type BoundSession struct {
	service   *Service
	Binding   json.RawMessage
	Lock      json.RawMessage
	SessionID string
	resolved  *resolvedBinding
}

// Live reports whether every pinned package is still installed and enabled.
// A version removed by a forced delete or a replace makes the bound
// conversation drop its skills (dynamic unload) instead of serving the
// stale in-memory copy validated at bind time.
func (b *BoundSession) Live() bool {
	if b == nil || b.service == nil || b.resolved == nil {
		return false
	}
	b.service.mu.Lock()
	defer b.service.mu.Unlock()
	for _, cap := range b.resolved.capabilities {
		ref := core.PackageRef{ID: cap.pkg.Manifest.ID, Version: cap.pkg.Manifest.Version, ManifestDigest: cap.pkg.ManifestDigest, PackageDigest: cap.pkg.PackageDigest}
		if err := b.service.enabledLocked(ref); err != nil {
			return false
		}
	}
	return true
}

func (s *Service) Session(binding, lock json.RawMessage, id string) (*BoundSession, error) {
	if err := s.requireExecutor(); err != nil {
		return nil, err
	}
	if err := core.ValidateDocument("resolved-lock", lock); err != nil {
		return nil, err
	}
	var supplied core.Lock
	if err := json.Unmarshal(lock, &supplied); err != nil {
		return nil, err
	}
	published, err := os.ReadFile(filepath.Join(s.Root, "bindings", strings.TrimPrefix(supplied.BindingDigest, "sha256:")+".json"))
	if err != nil || bytesDigest(published) != supplied.BindingDigest {
		return nil, errors.New("STALE_BINDING: published binding is missing or modified")
	}
	publishedBinding, err := core.DecodeBinding(published)
	if err != nil {
		return nil, err
	}
	currentBinding, err := core.DecodeBinding(binding)
	if err != nil || !reflect.DeepEqual(publishedBinding, currentBinding) {
		return nil, errors.New("STALE_BINDING: conversation binding differs from published bytes")
	}
	r, err := s.resolve(binding)
	if err != nil {
		return nil, err
	}
	expected, err := s.makeResolvedLock(r, id)
	if err != nil {
		return nil, err
	}
	var actual core.Lock
	if err = json.Unmarshal(expected, &actual); err != nil {
		return nil, err
	}
	actual.LockID = supplied.LockID
	if !reflect.DeepEqual(actual, supplied) {
		return nil, errors.New("STALE_BINDING: lock does not match the host binding, session, policy or artifacts")
	}
	return &BoundSession{service: s, Binding: append([]byte(nil), binding...), Lock: append([]byte(nil), lock...), SessionID: id, resolved: r}, nil
}

type Descriptor struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Description     string         `json:"description"`
	InputSchema     map[string]any `json:"inputSchema"`
	ReadOnly        bool           `json:"readOnly"`
	FileInputs      bool           `json:"fileInputs"`
	WritesArtifacts bool           `json:"writesArtifacts"`
	// SkillID and SkillName attribute the capability to its owning bundle:
	// the rs_ package skill id and the SKILL.md name. Observation identity is
	// derived from these, never from the tool name.
	SkillID   string `json:"skillId"`
	SkillName string `json:"skillName"`
}

func (b *BoundSession) Descriptors() []Descriptor {
	refs := map[string]core.PackageRef{}
	for _, ref := range b.resolved.binding.Packages {
		refs[ref.ID] = ref
	}
	out := []Descriptor{}
	for _, cap := range b.resolved.capabilities {
		d := describeContract(cap.contract)
		d.SkillName = cap.pkg.Manifest.Skill.ID
		if ref, ok := refs[cap.choice.Provider.PackageID]; ok {
			d.SkillID = PackageSkillID(ref)
		}
		out = append(out, d)
	}
	resolveDescriptorNames(out)
	return out
}

func (b *BoundSession) SkillText() string {
	var text strings.Builder
	for _, ref := range b.resolved.binding.Skills {
		pkg := b.resolved.packages[ref.PackageID]
		if sk := pkg.Manifest.Skill; sk.ID == ref.SkillID {
			data, err := pkg.ReadMetadata(sk.Entry)
			if err == nil && len(data) <= 128<<10 {
				text.WriteString("\n\n")
				text.Write(data)
			}
		}
	}
	return text.String()
}

type UploadedFile struct {
	FileID string `json:"fileId"`
	Digest string `json:"digest"`
	Size   int    `json:"size"`
}

func (b *BoundSession) fileDir() string {
	return filepath.Join(b.service.Root, "session-files", strings.TrimPrefix(bytesDigest([]byte(b.SessionID)), "sha256:"))
}
func (b *BoundSession) Upload(data []byte) (UploadedFile, error) {
	if len(data) > 32<<20 {
		return UploadedFile{}, errors.New("file exceeds 32 MiB")
	}
	if _, err := b.service.Session(b.Binding, b.Lock, b.SessionID); err != nil {
		return UploadedFile{}, err
	}
	if err := os.MkdirAll(b.fileDir(), 0700); err != nil {
		return UploadedFile{}, err
	}
	id := opaqueID("file:")
	if err := os.WriteFile(filepath.Join(b.fileDir(), strings.TrimPrefix(id, "file:")), data, 0600); err != nil {
		return UploadedFile{}, err
	}
	return UploadedFile{FileID: id, Digest: bytesDigest(data), Size: len(data)}, nil
}

func validateValue(schema map[string]any, raw []byte) error {
	encoded, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	var shape jsonschema.Schema
	if err = json.Unmarshal(encoded, &shape); err != nil {
		return err
	}
	resolved, err := shape.Resolve(&jsonschema.ResolveOptions{})
	if err != nil {
		return err
	}
	value, err := strictValue(raw)
	if err != nil {
		return err
	}
	return resolved.Validate(value)
}

type CallResult struct {
	Data             json.RawMessage `json:"data"`
	Artifacts        []Artifact      `json:"artifacts"`
	CallID           string          `json:"callId"`
	Capability       string          `json:"capability"`
	PackageDigest    string          `json:"packageDigest"`
	ExecutorIdentity string          `json:"executorIdentity"`
}

func (b *BoundSession) Call(ctx context.Context, capability string, input json.RawMessage) (CallResult, error) {
	var result CallResult
	// Revocation state and the lock are re-checked on every call; immutable
	// content integrity rides the catalog cache (verified at install and on
	// first load) rather than a per-call re-hash of the whole package tree.
	fresh, err := b.service.Session(b.Binding, b.Lock, b.SessionID)
	if err != nil {
		return result, err
	}
	var cap resolvedCapability
	found := false
	for _, candidate := range fresh.resolved.capabilities {
		if candidate.contract.ID == capability {
			cap = candidate
			found = true
			break
		}
	}
	if !found {
		return result, errors.New("RESOURCE_NOT_AUTHORIZED: capability is not bound")
	}
	if err = validateValue(cap.contract.InputSchema, input); err != nil {
		return result, fmt.Errorf("INVALID_INPUT: %w", err)
	}
	var args map[string]any
	if err = json.Unmarshal(input, &args); err != nil {
		return result, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	callID := opaqueID("call:")
	ref := core.PackageRef{ID: cap.pkg.Manifest.ID, Version: cap.pkg.Manifest.Version, ManifestDigest: cap.pkg.ManifestDigest, PackageDigest: cap.pkg.PackageDigest}
	s := b.service
	s.mu.Lock()
	if err = s.enabledLocked(ref); err != nil {
		s.mu.Unlock()
		return result, err
	}
	if s.active[ref.PackageDigest] == nil {
		s.active[ref.PackageDigest] = map[string]context.CancelFunc{}
	}
	s.active[ref.PackageDigest][callID] = cancel
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.active[ref.PackageDigest], callID); s.mu.Unlock() }()
	pc := &ProcessContext{Files: []ProcessFile{}, Assets: []ProcessAsset{}}
	files := map[string][]byte{}
	for _, ptr := range cap.contract.FileInputs {
		key := strings.ReplaceAll(strings.ReplaceAll(strings.TrimPrefix(ptr, "/"), "~1", "/"), "~0", "~")
		raw, present := args[key]
		if !present {
			// The input schema already ran: a file input it does not require
			// is optional (e.g. the caller used the library sample reference
			// instead). Absent means absent — nothing is staged.
			continue
		}
		id, _ := raw.(string)
		if len(id) != 37 || !strings.HasPrefix(id, "file:") || strings.Trim(id[5:], "0123456789abcdef") != "" {
			// The model sees this string; an actionable message stops it from
			// retrying every plausible id shape.
			return result, fmt.Errorf("RESOURCE_NOT_AUTHORIZED: %q is not a session fileId: fileIds are issued only for files uploaded to this conversation (form file:<32 lowercase hex>); library sample hashes and store paths are not fileIds — ask the user to upload the sample first", id)
		}
		data, err := os.ReadFile(filepath.Join(b.fileDir(), id[5:]))
		if err != nil {
			return result, errors.New("RESOURCE_NOT_AUTHORIZED: file is not owned by this session")
		}
		files[ptr] = data
		pc.Files = append(pc.Files, ProcessFile{InputPointer: ptr, FileID: id, Digest: bytesDigest(data)})
	}
	assets := map[string]string{}
	for _, id := range cap.provider.AssetIDs {
		dir, err := cap.pkg.AssetPath(id)
		if err != nil {
			return result, err
		}
		assets[id] = dir
		for _, asset := range cap.pkg.Manifest.Assets {
			if asset.ID == id {
				pc.Assets = append(pc.Assets, ProcessAsset{AssetID: id, Digest: asset.Artifact.Digest})
			}
		}
	}
	dir, err := cap.pkg.RuntimePath(cap.provider.RuntimeID)
	if err != nil {
		return result, err
	}
	// The call request mirrors the describe handshake's shapes: same protocol
	// spelling, the provider's operation and contract identity, the validated
	// input, and the invoke context carrying the authorized files/assets.
	// collect gates the executor's private /output tmpfs — a contract that
	// does not declare writesArtifacts never gets one.
	req := ProcessRequest{
		ProtocolVersion: "pogo.tool/1",
		CallID:          callID,
		Type:            ProcessRequestInvoke,
		Operation:       cap.provider.Operation,
		Contract:        &ContractIdentity{cap.choice.Contract.ID, cap.choice.Contract.Version, cap.choice.Contract.Digest},
		Input:           input,
		Context:         pc,
	}
	collect := cap.contract.Effects.WritesArtifacts
	detailed, err := s.Executor.ExecuteDetailed(ctx, Execution{Package: ref, ProviderID: cap.provider.ID, RuntimeDir: dir, Entrypoint: cap.provider.Entrypoint, Assets: assets, Files: files, Request: req, Limits: limitsOf(cap.grant), IsolationProfile: cap.grant.IsolationProfileRef, CollectArtifacts: collect})
	if err != nil {
		return result, err
	}
	// Revocation during the call must also win against late results: nothing
	// (data or artifacts) is delivered or published after this point when the
	// version was disabled mid-flight.
	if err = s.enabled(ref); err != nil {
		return result, err
	}
	if err = core.ValidateDocument("process-response", detailed.Output); err != nil {
		return result, fmt.Errorf("OUTPUT_INVALID: %w", err)
	}
	var response ProcessResponse
	if err = json.Unmarshal(detailed.Output, &response); err != nil {
		return result, err
	}
	if response.CallID == callID && response.Type == "error" && response.Error != nil {
		return result, fmt.Errorf("TOOL_FAILED[%s]: %s", response.Error.Code, response.Error.Message)
	}
	if response.CallID != callID || response.Type != "result" || response.Error != nil {
		return result, errors.New("TOOL_FAILED: invalid or unsuccessful provider result")
	}
	// A contract without writesArtifacts never gets an output directory and
	// must not declare artifacts either.
	if !collect && len(response.Artifacts) > 0 {
		return result, errors.New("TOOL_FAILED: invalid or unsuccessful provider result")
	}
	if err = validateValue(cap.contract.OutputSchema, response.Output); err != nil {
		return result, fmt.Errorf("OUTPUT_INVALID: %w", err)
	}
	published := []Artifact{}
	if collect {
		arts, blobs, err := matchArtifacts(response.Artifacts, detailed.Artifacts)
		if err != nil {
			return result, err
		}
		// Publish only after every check passed: failures publish nothing.
		if err = b.publishArtifacts(arts, blobs); err != nil {
			return result, err
		}
		if arts != nil {
			published = arts
		}
	}
	if err := s.enabled(ref); err != nil {
		return CallResult{}, err
	}
	result = CallResult{Data: response.Output, Artifacts: published, CallID: callID, Capability: capability, PackageDigest: ref.PackageDigest, ExecutorIdentity: s.Executor.Identity()}
	return result, nil
}

func (s *Service) Catalog() []Installed {
	out := s.List()
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.ID+out[i].Ref.Version < out[j].Ref.ID+out[j].Ref.Version })
	return out
}
