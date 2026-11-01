package skillruntime

import (
	"encoding/json"
	"testing"

	"github.com/yosukeno/pogo/skillruntime/core"
)

// The shell's deployment-wide network switch must not become a package grant.
// Exercise the installed-package selection and binding boundary, rather than
// constructing an executor whose namespace behavior could hide an invalid grant.
func TestSkillNetworkPermissionDoesNotInheritShellNetwork(t *testing.T) {
	for _, shellNetwork := range []string{"0", "1"} {
		t.Run("shell-network-"+shellNetwork, func(t *testing.T) {
			t.Setenv("POGO_SANDBOX", "enforce")
			t.Setenv("POGO_SANDBOX_NET", shellNetwork)
			s := newStubService(t)
			installed, _ := installSkillFixture(t, s)
			plan, err := s.PlanSelection(Selection{SkillIDs: []string{PackageSkillID(installed.Ref)}})
			if err != nil || !plan.Ready {
				t.Fatalf("selection failed: ready=%v error=%v", plan.Ready, err)
			}
			binding, err := core.DecodeBinding(plan.Binding)
			if err != nil || len(binding.Grants) != 1 || binding.Grants[0].Ceiling == nil {
				t.Fatalf("invalid selected grant: %v", err)
			}
			if binding.Grants[0].Mode != core.GrantModeSandbox || binding.Grants[0].Ceiling.Network != "none" {
				t.Fatalf("shell network leaked into package grant: %+v", binding.Grants[0])
			}
			if err := s.ValidateBinding(plan.Binding); err != nil {
				t.Fatalf("valid offline binding rejected: %v", err)
			}
			for _, network := range []string{"host", "all", "inherit", "*"} {
				binding.Grants[0].Ceiling.Network = network
				raw, err := json.Marshal(binding)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.ValidateBinding(raw); err == nil {
					t.Fatalf("forged network grant %q accepted", network)
				}
			}
		})
	}
}

// A package cannot request networking merely because the parent container has
// network clients and the ordinary shell has been configured to connect out.
func TestSkillNetworkManifestRequestsRemainOfflineOnly(t *testing.T) {
	t.Setenv("POGO_SANDBOX_NET", "1")
	s := newStubService(t)
	installed, _ := installSkillFixture(t, s)
	pkg, err := s.catalogPackage(installed.Ref)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(pkg.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	provider := document["providers"].([]any)[0].(map[string]any)
	// Typed metadata omits empty optional slices, but the wire manifest requires
	// the declared empty set of assets for this process-json provider.
	provider["assetIds"] = []string{}
	requests := provider["requests"].(map[string]any)
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.ValidateDocument(core.KindSkillRuntime, raw); err != nil {
		t.Fatalf("offline fixture invalid before modification: %v", err)
	}
	for _, network := range []any{"host", "all", "inherit", true, nil} {
		requests["network"] = network
		modified, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err := core.ValidateDocument(core.KindSkillRuntime, modified); err == nil {
			t.Fatalf("package network request %#v accepted", network)
		}
	}
}
