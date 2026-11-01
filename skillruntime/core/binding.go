package core

import (
	"errors"
	"fmt"
)

// ValidateBinding checks an AgentBinding against the given installed (or at
// least digest-pinned) packages. It enforces:
//
//   - closed references: every pkg, skill, contract, provider and slot
//     named by the binding resolves inside the supplied packages;
//   - exact match: pkg id+version+manifestDigest+packageDigest must equal
//     the loaded package metadata;
//   - distinct IDs: no duplicate packages, skills, capabilities or grants;
//   - every capability required by every selected Skill is explicitly bound;
//   - exactly one explicit provider per capability, and the provider must
//     implement exactly that contract;
//   - the resource set is exactly the union of slots required by the chosen
//     providers (no missing, no extra);
//   - grants are complete (exactly one per used provider), driver-compatible
//     (sandbox for process-json, trusted-remote for mcp), and each grant
//     ceiling is at or below the provider's requested limits.
//
// resourceRef strings are opaque identifiers here; they are never treated as
// real authorization. The host supplies an authoritative resource resolver.
func ValidateBinding(binding Binding, packages []*Package) error {
	var errs []error
	errf := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	// Index packages; duplicates among the supplied packages are an error.
	type pkgKey struct{ id, version string }
	byKey := map[pkgKey]*Package{}
	for _, p := range packages {
		if p == nil {
			errf("nil package in packages list")
			continue
		}
		k := pkgKey{p.Manifest.ID, p.Manifest.Version}
		if _, dup := byKey[k]; dup {
			errf("duplicate package %s@%s in packages list", k.id, k.version)
			continue
		}
		byKey[k] = p
	}

	// Packages: distinct ids and exact metadata match.
	seenPackage := map[string]bool{}
	bound := map[string]*Package{}
	for _, pr := range binding.Packages {
		if seenPackage[pr.ID] {
			errf("duplicate Skill id %q in binding", pr.ID)
			continue
		}
		seenPackage[pr.ID] = true
		p, ok := byKey[pkgKey{pr.ID, pr.Version}]
		if !ok {
			errf("binding references Skill %s@%s which is not among the supplied packages", pr.ID, pr.Version)
			continue
		}
		if p.ManifestDigest != pr.ManifestDigest {
			errf("Skill %s@%s: manifest digest mismatch: binding %s, package %s",
				pr.ID, pr.Version, pr.ManifestDigest, p.ManifestDigest)
			continue
		}
		if p.PackageDigest == "" {
			errf("Skill %s@%s: package has no archive digest (not installed via Store); cannot verify packageDigest",
				pr.ID, pr.Version)
			continue
		}
		if p.PackageDigest != pr.PackageDigest {
			errf("Skill %s@%s: package digest mismatch: binding %s, package %s",
				pr.ID, pr.Version, pr.PackageDigest, p.PackageDigest)
			continue
		}
		bound[pr.ID] = p
	}

	// Skills: distinct, resolvable, and their required capabilities closed.
	capabilityBound := map[string]CapabilityChoice{}
	for _, c := range binding.Capabilities {
		if _, dup := capabilityBound[c.Contract.ID]; dup {
			errf("capability %q is bound more than once; exactly one explicit provider per capability is required", c.Contract.ID)
			continue
		}
		capabilityBound[c.Contract.ID] = c
	}
	seenSkill := map[string]bool{}
	for _, sr := range binding.Skills {
		key := sr.PackageID + "/" + sr.SkillID
		if seenSkill[key] {
			errf("duplicate skill %q in binding", key)
			continue
		}
		seenSkill[key] = true
		p, ok := bound[sr.PackageID]
		if !ok {
			errf("skill %q references Skill %q which is not bound", key, sr.PackageID)
			continue
		}
		var skill *Skill
		if p.Manifest.Skill.ID == sr.SkillID {
			skill = &p.Manifest.Skill
		}

		if skill == nil {
			errf("skill %q does not exist in Skill %q", sr.SkillID, sr.PackageID)
			continue
		}
		for _, rc := range skill.RequiresCapabilities {
			choice, ok := capabilityBound[rc.ID]
			if !ok {
				errf("skill %q requires capability %s@%s which is not bound", skill.ID, rc.ID, rc.Version)
				continue
			}
			if choice.Contract != rc {
				errf("skill %q requires capability %s@%s digest %s but binding uses %s",
					skill.ID, rc.ID, rc.Version, rc.Digest, choice.Contract.Digest)
			}
		}
	}

	// Capabilities: provider exists, implements exactly the contract, and the
	// pkg is bound. Contract ref must also match the manifest contract.
	usedProviders := map[ProviderRef]Provider{}
	usedPackageOf := map[ProviderRef]*Package{}
	for _, c := range binding.Capabilities {
		p, ok := bound[c.Provider.PackageID]
		if !ok {
			errf("capability %q: provider Skill %q is not bound", c.Contract.ID, c.Provider.PackageID)
			continue
		}
		var prov *Provider
		for i := range p.Manifest.Providers {
			if p.Manifest.Providers[i].ID == c.Provider.ProviderID {
				prov = &p.Manifest.Providers[i]
				break
			}
		}
		if prov == nil {
			errf("capability %q: provider %q does not exist in Skill %q",
				c.Contract.ID, c.Provider.ProviderID, c.Provider.PackageID)
			continue
		}
		if prov.Implements != c.Contract {
			errf("capability %q: provider %q implements %s@%s (%s), not the bound ref (%s@%s %s)",
				c.Contract.ID, prov.ID,
				prov.Implements.ID, prov.Implements.Version, prov.Implements.Digest,
				c.Contract.ID, c.Contract.Version, c.Contract.Digest)
			continue
		}
		manifestMatch := false
		for _, ce := range p.Manifest.Contracts {
			if ce.Ref == c.Contract {
				manifestMatch = true
				break
			}
		}
		if !manifestMatch {
			errf("capability %q: contract ref is not part of Skill %q manifest", c.Contract.ID, p.Manifest.ID)
			continue
		}
		usedProviders[c.Provider] = *prov
		usedPackageOf[c.Provider] = p
	}

	// Resources: exact set match against the union of provider requirements.
	type slotKey struct{ packageID, slotID string }
	required := map[slotKey]bool{}
	for pref, prov := range usedProviders {
		if prov.Driver == DriverMCP {
			required[slotKey{pref.PackageID, prov.ConnectionSlot}] = true
			continue
		}
		for _, sid := range prov.ResourceSlots {
			required[slotKey{pref.PackageID, sid}] = true
		}
	}
	given := map[slotKey]bool{}
	for _, r := range binding.Resources {
		k := slotKey{r.PackageID, r.SlotID}
		if given[k] {
			errf("duplicate resource assignment for %s/%s", r.PackageID, r.SlotID)
			continue
		}
		given[k] = true
		if !required[k] {
			errf("resource assignment %s/%s is not required by any chosen provider", r.PackageID, r.SlotID)
			continue
		}
		if p, ok := bound[r.PackageID]; ok {
			slotExists := false
			for _, s := range p.Manifest.ResourceSlots {
				if s.ID == r.SlotID {
					slotExists = true
					break
				}
			}
			if !slotExists {
				errf("resource slot %q does not exist in Skill %q", r.SlotID, r.PackageID)
			}
		}
		// r.ResourceRef is opaque: not interpreted, not authorization.
	}
	for k := range required {
		if !given[k] {
			errf("resource slot %s/%s required by a chosen provider has no assignment", k.packageID, k.slotID)
		}
	}

	// Grants: exactly one per used provider, driver-compatible, ceilings
	// within provider requests.
	granted := map[ProviderRef]Grant{}
	for _, g := range binding.Grants {
		if _, dup := granted[g.Provider]; dup {
			errf("duplicate grant for provider %s/%s", g.Provider.PackageID, g.Provider.ProviderID)
			continue
		}
		granted[g.Provider] = g
		prov, used := usedProviders[g.Provider]
		if !used {
			errf("grant for provider %s/%s which is not used by any capability",
				g.Provider.PackageID, g.Provider.ProviderID)
			continue
		}
		switch prov.Driver {
		case DriverProcessJSON:
			if g.Mode != GrantModeSandbox {
				errf("provider %s/%s is process-json; grant mode must be sandbox, got %q",
					g.Provider.PackageID, g.Provider.ProviderID, g.Mode)
				continue
			}
			if g.Ceiling == nil {
				errf("sandbox grant for %s/%s lacks a ceiling", g.Provider.PackageID, g.Provider.ProviderID)
				continue
			}
			if prov.Requests == nil {
				continue // malformed manifest; reported at package load
			}
			if g.Ceiling.Network != prov.Requests.Network {
				errf("grant ceiling network %q differs from provider request %q for %s/%s",
					g.Ceiling.Network, prov.Requests.Network, g.Provider.PackageID, g.Provider.ProviderID)
			}
			cl, rl := g.Ceiling.Limits, prov.Requests.Limits
			switch {
			case cl.TimeoutSeconds > rl.TimeoutSeconds:
				errf("grant ceiling timeoutSeconds %d exceeds provider request %d for %s/%s",
					cl.TimeoutSeconds, rl.TimeoutSeconds, g.Provider.PackageID, g.Provider.ProviderID)
			case cl.MemoryMiB > rl.MemoryMiB:
				errf("grant ceiling memoryMiB %d exceeds provider request %d for %s/%s",
					cl.MemoryMiB, rl.MemoryMiB, g.Provider.PackageID, g.Provider.ProviderID)
			case cl.CPUMilliCores > rl.CPUMilliCores:
				errf("grant ceiling cpuMilliCores %d exceeds provider request %d for %s/%s",
					cl.CPUMilliCores, rl.CPUMilliCores, g.Provider.PackageID, g.Provider.ProviderID)
			case cl.MaxOutputBytes > rl.MaxOutputBytes:
				errf("grant ceiling maxOutputBytes %d exceeds provider request %d for %s/%s",
					cl.MaxOutputBytes, rl.MaxOutputBytes, g.Provider.PackageID, g.Provider.ProviderID)
			}
		case DriverMCP:
			if g.Mode != GrantModeTrustedRemote {
				errf("provider %s/%s is mcp; grant mode must be trusted-remote, got %q",
					g.Provider.PackageID, g.Provider.ProviderID, g.Mode)
				continue
			}
			if g.ConnectionPolicyRef == "" {
				errf("trusted-remote grant for %s/%s lacks connectionPolicyRef",
					g.Provider.PackageID, g.Provider.ProviderID)
			}
		}
	}
	for pref := range usedProviders {
		if _, ok := granted[pref]; !ok {
			errf("no grant for used provider %s/%s; grants must be complete", pref.PackageID, pref.ProviderID)
		}
	}

	return errors.Join(errs...)
}
