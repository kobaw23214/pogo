package core

// Document type constants accepted by ValidateDocument. The kind is the base
// name of the schema file without the ".schema.json" suffix.
const (
	KindCommon             = "common"
	KindCapabilityContract = "capability-contract"
	KindSkillRuntime       = "skill-runtime"
	KindAgentBinding       = "agent-binding"
	KindProcessRequest     = "process-request"
	KindProcessResponse    = "process-response"
	KindResolvedLock       = "resolved-lock"
)

// APIVersion is the only accepted Skill runtime protocol version.
const APIVersion = "pogo.skill/v1"

// Driver identifiers.
const (
	DriverBundle      = "bundle"
	DriverProcessJSON = "process-json"
	DriverMCP         = "mcp"
)

// Grant modes.
const (
	GrantModeSandbox       = "sandbox"
	GrantModeTrustedRemote = "trusted-remote"
)

// Resource slot kinds.
const (
	SlotKindFiles         = "files"
	SlotKindArtifacts     = "artifacts"
	SlotKindMCPConnection = "mcp-connection"
)

// ContractRef pins one capability contract by exact ID, version and digest.
type ContractRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// PackageRef pins one pkg version by manifest and package digests.
type PackageRef struct {
	ID             string `json:"id"`
	Version        string `json:"version"`
	ManifestDigest string `json:"manifestDigest"`
	PackageDigest  string `json:"packageDigest"`
}

// ProviderRef identifies a provider inside a pkg.
type ProviderRef struct {
	PackageID  string `json:"packageId"`
	ProviderID string `json:"providerId"`
}

// SkillRef identifies a Skill inside a pkg.
type SkillRef struct {
	PackageID string `json:"packageId"`
	SkillID   string `json:"skillId"`
}

// Artifact describes a payload archive inside a pkg package.
type Artifact struct {
	Path      string `json:"path"`
	Digest    string `json:"digest"`
	SizeBytes int64  `json:"sizeBytes"`
	Format    string `json:"format"` // "zip" or "tar.zst"
}

// Platform describes the runtime platform requirement of a bundle runtime.
type Platform struct {
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	Libc           string `json:"libc"`
	MinLibcVersion string `json:"minLibcVersion"`
}

// Runtime is a prebuilt bundle runtime declared by a manifest.
type Runtime struct {
	ID                string   `json:"id"`
	Driver            string   `json:"driver"`
	Artifact          Artifact `json:"artifact"`
	Platform          Platform `json:"platform"`
	RequiresIsolation []string `json:"requiresIsolation"`
}

// Asset is a versioned rule/data payload declared by a manifest.
type Asset struct {
	ID       string   `json:"id"`
	Artifact Artifact `json:"artifact"`
}

// ResourceSlot is a declared external-resource need of a pkg.
type ResourceSlot struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

// Limits caps the resources of one invocation.
type Limits struct {
	TimeoutSeconds int `json:"timeoutSeconds"`
	MemoryMiB      int `json:"memoryMiB"`
	CPUMilliCores  int `json:"cpuMilliCores"`
	MaxOutputBytes int `json:"maxOutputBytes"`
}

// Permissions is a network policy plus numeric limits.
type Permissions struct {
	Network string `json:"network"`
	Limits  Limits `json:"limits"`
}

// SchemaSnapshot pins the raw bytes of an MCP tool schema snapshot.
type SchemaSnapshot struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

// Provider implements one capability contract. Driver selects which fields
// are meaningful: DriverProcessJSON uses RuntimeID/Entrypoint/Operation/
// ResourceSlots/AssetIDs/Requests; DriverMCP uses ConnectionSlot/ToolName/
// InputMapping/ResultMapping/SchemaSnapshot/TimeoutSeconds/MaxOutputBytes.
type Provider struct {
	ID         string      `json:"id"`
	Implements ContractRef `json:"implements"`
	Driver     string      `json:"driver"`

	// process-json driver fields.
	RuntimeID     string       `json:"runtimeId,omitempty"`
	Entrypoint    string       `json:"entrypoint,omitempty"`
	Operation     string       `json:"operation,omitempty"`
	ResourceSlots []string     `json:"resourceSlots,omitempty"`
	AssetIDs      []string     `json:"assetIds,omitempty"`
	Requests      *Permissions `json:"requests,omitempty"`

	// mcp driver fields.
	ConnectionSlot string          `json:"connectionSlot,omitempty"`
	ToolName       string          `json:"toolName,omitempty"`
	InputMapping   string          `json:"inputMapping,omitempty"`
	ResultMapping  string          `json:"resultMapping,omitempty"`
	SchemaSnapshot *SchemaSnapshot `json:"schemaSnapshot,omitempty"`
	TimeoutSeconds int             `json:"timeoutSeconds,omitempty"`
	MaxOutputBytes int             `json:"maxOutputBytes,omitempty"`
}

// ManifestContractEntry binds a contract ref to its file inside the package.
type ManifestContractEntry struct {
	Ref  ContractRef `json:"ref"`
	Path string      `json:"path"`
}

// Skill is a pkg-owned Skill declaration.
type Skill struct {
	ID                   string        `json:"id"`
	DisplayName          string        `json:"displayName,omitempty"`
	Description          string        `json:"description,omitempty"`
	Entry                string        `json:"entry"`
	RequiresCapabilities []ContractRef `json:"requiresCapabilities"`
}

// Manifest is the SkillRuntime document (pogo-skill.json).
type Manifest struct {
	APIVersion           string                  `json:"apiVersion"`
	Kind                 string                  `json:"kind"`
	ID                   string                  `json:"id"`
	Version              string                  `json:"version"`
	DisplayName          string                  `json:"displayName"`
	RequiredFeatures     []string                `json:"requiredFeatures"`
	Contracts            []ManifestContractEntry `json:"contracts"`
	RequiresCapabilities []ContractRef           `json:"requiresCapabilities"`
	// Skill metadata comes only from the root SKILL.md, not a second registry.
	Skill         Skill          `json:"-"`
	Runtimes      []Runtime      `json:"runtimes"`
	Assets        []Asset        `json:"assets"`
	ResourceSlots []ResourceSlot `json:"resourceSlots"`
	Providers     []Provider     `json:"providers"`
	// WorkspaceData materializes declared assets into the workspace of every
	// Agent that binds the Skill: document-type payloads (report libraries,
	// reference files) the agent must open as files, not query through
	// providers. Dest is a single path segment under the workspace; the host
	// re-materializes when the asset digest changes and marks the copy
	// read-only best effort.
	WorkspaceData []WorkspaceDataEntry `json:"workspaceData,omitempty"`
}

// WorkspaceDataEntry is one asset-to-workspace mapping in a manifest.
type WorkspaceDataEntry struct {
	AssetID string `json:"assetId"`
	Dest    string `json:"dest"`
}

// Effects declares the externally visible effects of a capability.
type Effects struct {
	ReadsUserFiles       bool `json:"readsUserFiles"`
	WritesArtifacts      bool `json:"writesArtifacts"`
	MutatesExternalState bool `json:"mutatesExternalState"`
}

// Contract is the CapabilityContract document. InputSchema and OutputSchema
// are kept as raw decoded objects; they must additionally pass the Pogo
// embedded-schema profile (see validateEmbeddedSchema).
type Contract struct {
	APIVersion   string         `json:"apiVersion"`
	Kind         string         `json:"kind"`
	ID           string         `json:"id"`
	Version      string         `json:"version"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema"`
	FileInputs   []string       `json:"fileInputs"`
	Effects      Effects        `json:"effects"`
	Retry        string         `json:"retry"`
}

// CapabilityChoice binds one contract to one explicit provider.
type CapabilityChoice struct {
	Contract ContractRef `json:"contract"`
	Provider ProviderRef `json:"provider"`
}

// ResourceAssignment binds one resource slot to an opaque host resource
// reference. ResourceRef is NOT an authorization; the host resolves it.
type ResourceAssignment struct {
	PackageID   string `json:"packageId"`
	SlotID      string `json:"slotId"`
	ResourceRef string `json:"resourceRef"`
}

// Grant authorizes one provider. Mode selects the variant: sandbox grants
// carry IsolationProfileRef and Ceiling; trusted-remote grants carry
// ConnectionPolicyRef.
type Grant struct {
	Mode     string      `json:"mode"`
	Provider ProviderRef `json:"provider"`

	// sandbox grant fields.
	IsolationProfileRef string       `json:"isolationProfileRef,omitempty"`
	Ceiling             *Permissions `json:"ceiling,omitempty"`

	// trusted-remote grant fields.
	ConnectionPolicyRef string `json:"connectionPolicyRef,omitempty"`
}

// Binding is the AgentBinding document.
type Binding struct {
	APIVersion    string               `json:"apiVersion"`
	Kind          string               `json:"kind"`
	AgentID       string               `json:"agentId"`
	Revision      int                  `json:"revision"`
	Packages      []PackageRef         `json:"packages"`
	Skills        []SkillRef           `json:"skills"`
	Capabilities  []CapabilityChoice   `json:"capabilities"`
	Resources     []ResourceAssignment `json:"resources"`
	Grants        []Grant              `json:"grants"`
	UpgradePolicy string               `json:"upgradePolicy"`
}

// DriverVersion pins a driver ABI version in a ResolvedLock.
type DriverVersion struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// LockSkill pins a Skill entry digest in a ResolvedLock.
type LockSkill struct {
	Skill       SkillRef `json:"skill"`
	EntryDigest string   `json:"entryDigest"`
}

// LockRuntime pins a runtime artifact digest in a ResolvedLock.
type LockRuntime struct {
	PackageID      string `json:"packageId"`
	RuntimeID      string `json:"runtimeId"`
	ArtifactDigest string `json:"artifactDigest"`
}

// LockAsset pins an asset artifact digest in a ResolvedLock.
type LockAsset struct {
	PackageID      string `json:"packageId"`
	AssetID        string `json:"assetId"`
	ArtifactDigest string `json:"artifactDigest"`
}

// LockResource records the authorized resource and revisions of one slot.
type LockResource struct {
	PackageID             string `json:"packageId"`
	SlotID                string `json:"slotId"`
	ResourceRef           string `json:"resourceRef"`
	ResourceRevision      string `json:"resourceRevision"`
	AuthorizationRevision string `json:"authorizationRevision"`
}

// LockConnection records an MCP connection snapshot in a ResolvedLock.
type LockConnection struct {
	Provider                 ProviderRef `json:"provider"`
	ConnectionRevision       string      `json:"connectionRevision"`
	ConnectionPolicyRevision string      `json:"connectionPolicyRevision"`
	SchemaSnapshotDigest     string      `json:"schemaSnapshotDigest"`
}

// Lock is the ResolvedLock document. Only the host may create valid locks;
// CreatedBy is a format marker, not proof.
type Lock struct {
	APIVersion       string             `json:"apiVersion"`
	Kind             string             `json:"kind"`
	LockID           string             `json:"lockId"`
	AgentID          string             `json:"agentId"`
	SessionID        string             `json:"sessionId"`
	BindingRevision  int                `json:"bindingRevision"`
	BindingDigest    string             `json:"bindingDigest"`
	PolicyRevision   string             `json:"policyRevision"`
	RegistryRevision string             `json:"registryRevision"`
	Drivers          []DriverVersion    `json:"drivers"`
	Packages         []PackageRef       `json:"packages"`
	Skills           []LockSkill        `json:"skills"`
	Capabilities     []CapabilityChoice `json:"capabilities"`
	Runtimes         []LockRuntime      `json:"runtimes"`
	Assets           []LockAsset        `json:"assets"`
	Resources        []LockResource     `json:"resources"`
	Connections      []LockConnection   `json:"connections"`
	EffectiveGrants  []Grant            `json:"effectiveGrants"`
	CreatedBy        string             `json:"createdBy"`
}
