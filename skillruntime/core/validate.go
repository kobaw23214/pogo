package core

import (
	"errors"
	"fmt"
	"strings"
)

// ValidateDocument strictly validates one v1 protocol document of the
// given kind (schema base name, e.g. "skill-runtime"):
//
//  1. raw JSON resource checks (size, depth, duplicate keys, finite numbers);
//  2. JSON Schema validation against the embedded Draft 2020-12 schemas with
//     offline refs only;
//  3. typed decoding with unknown-field rejection;
//  4. document-specific semantic checks (embedded schema profile, fileInputs
//     coherence, manifest reference closure).
//
// Typed decoding alone is not sufficient JSON Schema validation, and schema
// validation alone does not cover the resource checks; all layers run.
func ValidateDocument(kind string, data []byte) error {
	raw, err := validateSchemaLayer(kind, data)
	if err != nil {
		return err
	}
	switch kind {
	case KindCapabilityContract:
		var c Contract
		if err := decodeTyped(data, &c); err != nil {
			return err
		}
		return validateContractSemantics(&c, raw)
	case KindSkillRuntime:
		var m Manifest
		if err := decodeTyped(data, &m); err != nil {
			return err
		}
		return validateManifestSemantics(&m, nil)
	case KindAgentBinding:
		var b Binding
		if err := decodeTyped(data, &b); err != nil {
			return err
		}
		return nil
	case KindResolvedLock:
		var l Lock
		if err := decodeTyped(data, &l); err != nil {
			return err
		}
		return nil
	case KindCommon, KindProcessRequest, KindProcessResponse:
		// No exported typed struct is required for these; raw + schema layers
		// already enforce the closed structure.
		return nil
	default:
		return fmt.Errorf("unknown document kind %q", kind)
	}
}

// validateSchemaLayer runs the raw decode and JSON Schema validation layers
// and returns the decoded generic document.
func validateSchemaLayer(kind string, data []byte) (any, error) {
	if _, ok := schemaIDs[kind]; !ok {
		return nil, fmt.Errorf("unknown document kind %q", kind)
	}
	raw, err := decodeRawJSON(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", kind, err)
	}
	rs, err := resolvedSchema(kind)
	if err != nil {
		return nil, err
	}
	if err := rs.Validate(raw); err != nil {
		return nil, fmt.Errorf("%s: schema validation failed: %v", kind, err)
	}
	return raw, nil
}

// decodeContract fully validates and decodes a CapabilityContract.
func decodeContract(data []byte) (*Contract, error) {
	raw, err := validateSchemaLayer(KindCapabilityContract, data)
	if err != nil {
		return nil, err
	}
	var c Contract
	if err := decodeTyped(data, &c); err != nil {
		return nil, err
	}
	if err := validateContractSemantics(&c, raw); err != nil {
		return nil, err
	}
	return &c, nil
}

// decodeManifest fully validates and decodes a SkillRuntime.
func decodeManifest(data []byte) (*Manifest, error) {
	raw, err := validateSchemaLayer(KindSkillRuntime, data)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := decodeTyped(data, &m); err != nil {
		return nil, err
	}
	if err := validateManifestSemantics(&m, raw); err != nil {
		return nil, err
	}
	return &m, nil
}

// DecodeBinding fully validates and decodes an AgentBinding document.
func DecodeBinding(data []byte) (Binding, error) {
	var b Binding
	if _, err := validateSchemaLayer(KindAgentBinding, data); err != nil {
		return b, err
	}
	if err := decodeTyped(data, &b); err != nil {
		return Binding{}, err
	}
	return b, nil
}

// validateContractSemantics enforces the v1 contract rules that go beyond
// the structural schema: embedded schema profile and fileInputs coherence.
func validateContractSemantics(c *Contract, raw any) error {
	var errs []error
	rawMap, _ := raw.(map[string]any)
	var inRaw, outRaw any = c.InputSchema, c.OutputSchema
	if rawMap != nil {
		inRaw = rawMap["inputSchema"]
		outRaw = rawMap["outputSchema"]
	}
	if err := validateEmbeddedSchema(inRaw, "contract "+c.ID+" inputSchema"); err != nil {
		errs = append(errs, err)
	}
	if err := validateEmbeddedSchema(outRaw, "contract "+c.ID+" outputSchema"); err != nil {
		errs = append(errs, err)
	}

	// fileInputs: v1 only allows pointers to explicit string properties of
	// the root object (SPEC section 4). The property may be optional when the
	// contract offers another file source (e.g. a bundled library sample);
	// the host stages the file only when the caller actually supplies it.
	props, _, ok := embeddedSchemaStringProps(inRaw)
	seen := map[string]bool{}
	for _, ptr := range c.FileInputs {
		if seen[ptr] {
			errs = append(errs, fmt.Errorf("contract %s: duplicate fileInput %q", c.ID, ptr))
			continue
		}
		seen[ptr] = true
		if !ok || !strings.HasPrefix(ptr, "/") || strings.Contains(ptr[1:], "/") ||
			strings.ContainsAny(ptr[1:], "~") {
			errs = append(errs, fmt.Errorf(
				"contract %s: fileInput %q must point to a root-level property", c.ID, ptr))
			continue
		}
		name := ptr[1:]
		if !props[name] {
			errs = append(errs, fmt.Errorf(
				"contract %s: fileInput %q must reference an explicit string property of the root object",
				c.ID, ptr))
		}
	}
	if len(c.FileInputs) > 0 && !c.Effects.ReadsUserFiles {
		errs = append(errs, fmt.Errorf(
			"contract %s: fileInputs is non-empty but effects.readsUserFiles is false", c.ID))
	}
	if c.Effects.MutatesExternalState {
		errs = append(errs, fmt.Errorf("contract %s: mutatesExternalState must be false in v1", c.ID))
	}
	return errors.Join(errs...)
}

// validateManifestSemantics enforces manifest-level reference closure and
// provider/effects coherence. raw is the generic document when available.
func validateManifestSemantics(m *Manifest, raw any) error {
	var errs []error
	errf := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("manifest %s: "+format, append([]any{m.ID}, args...)...))
	}

	// Uniqueness of identifiers.
	contractByID := map[string]ContractRef{}
	for _, ce := range m.Contracts {
		if prev, dup := contractByID[ce.Ref.ID]; dup {
			if prev != ce.Ref {
				errf("contract id %q declared twice with different refs", ce.Ref.ID)
			} else {
				errf("contract id %q declared twice", ce.Ref.ID)
			}
		}
		contractByID[ce.Ref.ID] = ce.Ref
	}
	runtimeByID := map[string]Runtime{}
	for _, r := range m.Runtimes {
		if _, dup := runtimeByID[r.ID]; dup {
			errf("duplicate runtime id %q", r.ID)
		}
		runtimeByID[r.ID] = r
	}
	assetByID := map[string]Asset{}
	for _, a := range m.Assets {
		if _, dup := assetByID[a.ID]; dup {
			errf("duplicate asset id %q", a.ID)
		}
		assetByID[a.ID] = a
	}
	slotByID := map[string]ResourceSlot{}
	for _, s := range m.ResourceSlots {
		if _, dup := slotByID[s.ID]; dup {
			errf("duplicate resource slot id %q", s.ID)
		}
		slotByID[s.ID] = s
	}
	providerByID := map[string]Provider{}
	for _, p := range m.Providers {
		if _, dup := providerByID[p.ID]; dup {
			errf("duplicate provider id %q", p.ID)
		}
		providerByID[p.ID] = p
	}
	destSeen := map[string]string{}
	for _, w := range m.WorkspaceData {
		if _, ok := assetByID[w.AssetID]; !ok {
			errf("workspaceData: unknown asset id %q", w.AssetID)
		}
		if prev, dup := destSeen[w.Dest]; dup {
			errf("workspaceData: dest %q mapped twice (assets %q and %q)", w.Dest, prev, w.AssetID)
		}
		destSeen[w.Dest] = w.AssetID
	}

	// Every capability belongs to this Skill and has exactly one local provider.
	required := map[ContractRef]bool{}
	for _, rc := range m.RequiresCapabilities {
		if ref, ok := contractByID[rc.ID]; !ok || ref != rc {
			errf("Skill requires unknown contract %s@%s", rc.ID, rc.Version)
		}
		required[rc] = true
		count := 0
		for _, p := range m.Providers {
			if p.Implements == rc {
				count++
			}
		}
		if count != 1 {
			errf("capability %s must have exactly one bundled provider", rc.ID)
		}
	}
	for _, p := range m.Providers {
		if !required[p.Implements] {
			errf("provider %s is not required by this Skill", p.ID)
		}
	}

	// Provider coherence.
	features := map[string]bool{}
	for _, f := range m.RequiredFeatures {
		features[f] = true
	}
	needBundle, needProcess, needMCP := false, false, false
	for _, p := range m.Providers {
		contractRef, ok := contractByID[p.Implements.ID]
		if !ok || contractRef != p.Implements {
			errf("provider %q implements unknown contract %s@%s", p.ID, p.Implements.ID, p.Implements.Version)
			continue
		}
		switch p.Driver {
		case DriverProcessJSON:
			needProcess = true
			if p.RuntimeID == "" {
				errf("provider %q: runtimeId is required", p.ID)
			} else if _, ok := runtimeByID[p.RuntimeID]; !ok {
				needBundle = true
				errf("provider %q: unknown runtimeId %q", p.ID, p.RuntimeID)
			} else {
				needBundle = true
			}
			if p.Entrypoint == "" {
				errf("provider %q: entrypoint is required", p.ID)
			}
			if p.Operation == "" {
				errf("provider %q: operation is required", p.ID)
			}
			if p.Requests == nil {
				errf("provider %q: requests is required", p.ID)
			}
			for _, sid := range p.ResourceSlots {
				slot, ok := slotByID[sid]
				if !ok {
					errf("provider %q: unknown resource slot %q", p.ID, sid)
					continue
				}
				if slot.Kind == SlotKindMCPConnection {
					errf("provider %q: mcp-connection slot %q is not usable by a process-json provider", p.ID, sid)
				}
			}
			for _, aid := range p.AssetIDs {
				if _, ok := assetByID[aid]; !ok {
					errf("provider %q: unknown asset id %q", p.ID, aid)
				}
			}
		case DriverMCP:
			needMCP = true
			slot, ok := slotByID[p.ConnectionSlot]
			if p.ConnectionSlot == "" {
				errf("provider %q: connectionSlot is required", p.ID)
			} else if !ok {
				errf("provider %q: unknown connection slot %q", p.ID, p.ConnectionSlot)
			} else if slot.Kind != SlotKindMCPConnection {
				errf("provider %q: connection slot %q has kind %q, want mcp-connection", p.ID, p.ConnectionSlot, slot.Kind)
			}
			if p.ToolName == "" {
				errf("provider %q: toolName is required", p.ID)
			}
			if p.SchemaSnapshot == nil {
				errf("provider %q: schemaSnapshot is required", p.ID)
			}
		default:
			errf("provider %q: unsupported driver %q", p.ID, p.Driver)
		}
	}
	for _, r := range m.Runtimes {
		if r.Driver == DriverBundle {
			needBundle = true
		}
	}
	if needBundle && !features["bundle.v1"] {
		errf("bundle runtimes/providers require requiredFeatures to include bundle.v1")
	}
	if needProcess && !features["process-json.v1"] {
		errf("process-json providers require requiredFeatures to include process-json.v1")
	}
	if needMCP && !features["mcp.v1"] {
		errf("mcp providers require requiredFeatures to include mcp.v1")
	}
	_ = raw
	return errors.Join(errs...)
}

// crossCheckProviderContracts checks provider/contract coherence that needs
// the actual contract documents (effects and fileInputs). contracts maps
// contract ID to the verified contract document.
func crossCheckProviderContracts(m *Manifest, contracts map[string]Contract) error {
	var errs []error
	slotByID := map[string]ResourceSlot{}
	for _, s := range m.ResourceSlots {
		slotByID[s.ID] = s
	}
	for _, p := range m.Providers {
		c, ok := contracts[p.Implements.ID]
		if !ok {
			continue // already reported by manifest semantics
		}
		switch p.Driver {
		case DriverProcessJSON:
			hasFiles, hasArtifacts := false, false
			for _, sid := range p.ResourceSlots {
				if slot, ok := slotByID[sid]; ok {
					switch slot.Kind {
					case SlotKindFiles:
						hasFiles = true
					case SlotKindArtifacts:
						hasArtifacts = true
					}
				}
			}
			if len(c.FileInputs) > 0 || c.Effects.ReadsUserFiles {
				if !hasFiles {
					errs = append(errs, fmt.Errorf(
						"provider %q: contract %s reads user files (fileInputs=%d) but the provider has no files-kind resource slot",
						p.ID, c.ID, len(c.FileInputs)))
				}
			}
			if c.Effects.WritesArtifacts && !hasArtifacts {
				errs = append(errs, fmt.Errorf(
					"provider %q: contract %s writes artifacts but the provider has no artifacts-kind resource slot",
					p.ID, c.ID))
			}
		case DriverMCP:
			// v1 does not upload local files to remotes or accept remote
			// file artifacts; MCP providers may only implement purely
			// data-in/data-out contracts.
			if len(c.FileInputs) > 0 || c.Effects.ReadsUserFiles {
				errs = append(errs, fmt.Errorf(
					"provider %q: mcp driver cannot implement contract %s with file inputs (v1 does not transfer local files to remotes)",
					p.ID, c.ID))
			}
			if c.Effects.WritesArtifacts {
				errs = append(errs, fmt.Errorf(
					"provider %q: mcp driver cannot implement contract %s that writes artifacts (structuredContent only)",
					p.ID, c.ID))
			}
		}
	}
	return errors.Join(errs...)
}
