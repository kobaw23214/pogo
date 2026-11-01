package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installMCP installs the synthetic MCP pkg into the same store and
// returns its package.
func installMCP(t *testing.T, store *Store, contractB []byte) *Package {
	t.Helper()
	snapshot := []byte(`{"tool": "index_query", "note": "synthetic snapshot"}`)
	manifest := buildMCPManifest(t, snapshot, contractB)
	archive := writeZip(t, []zipEnt{
		{name: "SKILL.md", data: []byte("---\nname: remote-query\ndescription: Remote query fixture.\n---\n"), mode: 0644},
		{name: ManifestFileName, data: manifest, mode: 0o644},
		{name: "contracts/query.json", data: contractB, mode: 0o644},
		{name: "snapshots/query.json", data: snapshot, mode: 0o644},
	})
	dir := t.TempDir()
	p := filepath.Join(dir, "mcp.zip")
	if err := os.WriteFile(p, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	pkg, err := store.InstallZIP(p)
	if err != nil {
		t.Fatalf("install MCP pkg: %v", err)
	}
	return pkg
}

// goodBinding builds a valid binding against the demo and MCP packages: the
// analysis Skill's query capability is served by the MCP provider.
func goodBinding(demo, mcp *Package) Binding {
	ca := demo.Manifest.Contracts[0].Ref // test.file.inspect
	cb := demo.Manifest.Contracts[1].Ref // test.index.query
	return Binding{
		APIVersion: APIVersion,
		Kind:       "AgentBinding",
		AgentID:    "agent:tester",
		Revision:   1,
		Packages:   []PackageRef{demo.ref(), mcp.ref()},
		Skills:     []SkillRef{{PackageID: demo.Manifest.ID, SkillID: "analysis"}},
		Capabilities: []CapabilityChoice{
			{Contract: ca, Provider: ProviderRef{PackageID: demo.Manifest.ID, ProviderID: "inspect"}},
			{Contract: cb, Provider: ProviderRef{PackageID: mcp.Manifest.ID, ProviderID: "remote-query"}},
		},
		Resources: []ResourceAssignment{
			{PackageID: demo.Manifest.ID, SlotID: "samples", ResourceRef: "files:current-session"},
			{PackageID: demo.Manifest.ID, SlotID: "results", ResourceRef: "artifacts:current-call"},
			{PackageID: mcp.Manifest.ID, SlotID: "connection", ResourceRef: "mcp:conn-1"},
		},
		Grants: []Grant{
			{
				Mode:                GrantModeSandbox,
				Provider:            ProviderRef{PackageID: demo.Manifest.ID, ProviderID: "inspect"},
				IsolationProfileRef: "sandbox:local-bwrap-v1",
				Ceiling: &Permissions{Network: "none", Limits: Limits{
					TimeoutSeconds: 60, MemoryMiB: 512, CPUMilliCores: 1000, MaxOutputBytes: 1048576,
				}},
			},
			{
				Mode:                GrantModeTrustedRemote,
				Provider:            ProviderRef{PackageID: mcp.Manifest.ID, ProviderID: "remote-query"},
				ConnectionPolicyRef: "policy:trusted-mcp-v1",
			},
		},
		UpgradePolicy: "new-sessions-only",
	}
}

func TestValidateBindingAgainstInstalledPackages(t *testing.T) {
	store, demo, _ := installDemo(t)
	mcp := installMCP(t, store, contractJSON("test.index.query", false))

	b := goodBinding(demo, mcp)
	if err := ValidateBinding(b, []*Package{demo, mcp}); err != nil {
		t.Fatalf("valid binding rejected: %v", err)
	}

	// The same binding must round-trip through the wire format.
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeBinding(data)
	if err != nil {
		t.Fatalf("DecodeBinding round trip: %v", err)
	}
	if err := ValidateBinding(decoded, []*Package{demo, mcp}); err != nil {
		t.Fatalf("decoded binding rejected: %v", err)
	}
}

func TestValidateBindingRejections(t *testing.T) {
	store, demo, _ := installDemo(t)
	mcp := installMCP(t, store, contractJSON("test.index.query", false))

	mut := func(name string, change func(*Binding), want string) {
		t.Run(name, func(t *testing.T) {
			b := goodBinding(demo, mcp)
			change(&b)
			err := ValidateBinding(b, []*Package{demo, mcp})
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("expected rejection containing %q, got %v", want, err)
			}
		})
	}

	inspectRef := ProviderRef{PackageID: demo.Manifest.ID, ProviderID: "inspect"}
	remoteRef := ProviderRef{PackageID: mcp.Manifest.ID, ProviderID: "remote-query"}

	mut("manifest digest mismatch", func(b *Binding) {
		b.Packages[0].ManifestDigest = "sha256:" + strings.Repeat("0", 64)
	}, "manifest digest mismatch")

	mut("package digest mismatch", func(b *Binding) {
		b.Packages[0].PackageDigest = "sha256:" + strings.Repeat("0", 64)
	}, "package digest mismatch")

	mut("wrong Skill version", func(b *Binding) {
		b.Packages[0].Version = "9.9.9"
	}, "not among the supplied packages")

	mut("skill capability not bound", func(b *Binding) {
		b.Capabilities = b.Capabilities[:1] // drop the query capability
		b.Resources = b.Resources[:2]       // keep resource set consistent
		b.Grants = b.Grants[:1]
	}, "which is not bound")

	mut("duplicate capability", func(b *Binding) {
		b.Capabilities = append(b.Capabilities, b.Capabilities[0])
	}, "bound more than once")

	mut("provider does not implement contract", func(b *Binding) {
		b.Capabilities[0].Provider = ProviderRef{PackageID: demo.Manifest.ID, ProviderID: "query"}
		b.Grants = append(b.Grants, Grant{
			Mode: GrantModeSandbox, Provider: ProviderRef{PackageID: demo.Manifest.ID, ProviderID: "query"},
			IsolationProfileRef: "sandbox:local-bwrap-v1",
			Ceiling:             &Permissions{Network: "none", Limits: Limits{TimeoutSeconds: 30, MemoryMiB: 256, CPUMilliCores: 500, MaxOutputBytes: 524288}},
		})
	}, "not the bound ref")

	mut("missing resource", func(b *Binding) {
		b.Resources = b.Resources[1:] // drop samples
	}, "no assignment")

	mut("extra resource", func(b *Binding) {
		b.Resources = append(b.Resources, ResourceAssignment{
			PackageID: demo.Manifest.ID, SlotID: "samples", ResourceRef: "files:other-session",
		})
	}, "duplicate resource assignment")

	mut("missing grant", func(b *Binding) {
		b.Capabilities = b.Capabilities[:1] // only inspect
		b.Resources = b.Resources[:2]       // only demo slots
		b.Grants = nil                      // no grants at all
		b.Skills = nil                      // isolate the grant completeness check
	}, "no grant")

	mut("grant for unused provider", func(b *Binding) {
		b.Grants = append(b.Grants, Grant{
			Mode:                GrantModeTrustedRemote,
			Provider:            ProviderRef{PackageID: demo.Manifest.ID, ProviderID: "query"},
			ConnectionPolicyRef: "policy:x",
		})
	}, "not used by any capability")

	mut("ceiling exceeds requests", func(b *Binding) {
		b.Grants[0].Ceiling.Limits.MemoryMiB = 4096 // provider requests 512
	}, "exceeds provider request")

	mut("sandbox grant for mcp provider", func(b *Binding) {
		b.Grants[1] = Grant{
			Mode:                GrantModeSandbox,
			Provider:            remoteRef,
			IsolationProfileRef: "sandbox:local-bwrap-v1",
			Ceiling:             &Permissions{Network: "none", Limits: Limits{TimeoutSeconds: 60, MemoryMiB: 512, CPUMilliCores: 1000, MaxOutputBytes: 1048576}},
		}
	}, "grant mode must be trusted-remote")

	mut("trusted-remote grant for process provider", func(b *Binding) {
		b.Grants[0] = Grant{
			Mode:                GrantModeTrustedRemote,
			Provider:            inspectRef,
			ConnectionPolicyRef: "policy:x",
		}
	}, "grant mode must be sandbox")
}

// TestBindingNeedsInstalledPackages proves LoadPackage output (no package
// digest) cannot back a binding.
func TestBindingNeedsInstalledPackages(t *testing.T) {
	archive, _ := buildDemoOuterZip(t, nil)
	dir := t.TempDir()
	budget := &zipBudget{}
	if _, err := extractZIP(archive, dir, budget, "outer"); err != nil {
		t.Fatal(err)
	}
	unpacked, err := LoadPackage(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, demo, _ := installDemo(t)
	mcp := installMCP(t, store, contractJSON("test.index.query", false))
	b := goodBinding(demo, mcp)
	err = ValidateBinding(b, []*Package{unpacked, mcp})
	if err == nil || !strings.Contains(err.Error(), "packageDigest") {
		t.Fatalf("binding against non-installed package not rejected: %v", err)
	}
}
