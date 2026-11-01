package skillruntime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/skillruntime/core"
)

func TestRuntimeSkillSelectionNormalizationBeyondFormerCap(t *testing.T) {
	ids := make([]string, 0, 410)
	for i := 204; i >= 0; i-- {
		id := fmt.Sprintf("rs_%064x", i+1)
		ids = append(ids, id, id)
	}
	normalized, err := normalizeSelectionIDs(ids)
	if err != nil || len(normalized) != 205 || !slices.IsSorted(normalized) {
		t.Fatalf("large selection was rejected, truncated or not normalized: %d %v", len(normalized), err)
	}
	for _, bad := range []string{"", " ", " leading", "trailing ", strings.Repeat("x", 201)} {
		if _, err := normalizeSelectionIDs(append(slices.Clone(ids), bad)); err == nil {
			t.Fatalf("invalid final ID was hidden by a large valid selection: %q", bad)
		}
	}
	for _, empty := range [][]string{nil, {}} {
		got, err := normalizeSelectionIDs(empty)
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("empty normalization changed: %v %v", got, err)
		}
	}
}

func TestRuntimePlanSelectionBeyondFormerCapPreservesClosure(t *testing.T) {
	const count = 205
	s := newStubService(t)
	// Each real imported package has its own contract and provider. This checks
	// the complete selection/binding path rather than bypassing catalog integrity
	// or permitting ambiguous providers merely to reach a larger item count.
	runtimeZIP := testZip(t, []testZipEnt{{name: "bin/tool", data: []byte("#!/bin/true\n"), mode: 0755}})
	ids := make([]string, 0, count+1)
	for i := 0; i < count; i++ {
		contract := fixtureContractJSON(fmt.Sprintf("test.selection.c%03d", i), true, false)
		contractRef := core.ContractRef{ID: fmt.Sprintf("test.selection.c%03d", i), Version: "1.0.0", Digest: bytesDigest(contract)}
		manifest := core.Manifest{
			APIVersion: core.APIVersion, Kind: "SkillRuntime", ID: fmt.Sprintf("org.test.selection.p%03d", i), Version: "1.0.0", DisplayName: fmt.Sprintf("Selection %03d", i),
			RequiredFeatures:     []string{"bundle.v1", "process-json.v1", "isolated-execution.v1"},
			Contracts:            []core.ManifestContractEntry{{Ref: contractRef, Path: "./contracts/inspect.json"}},
			RequiresCapabilities: []core.ContractRef{contractRef},
			Runtimes:             []core.Runtime{{ID: "rt", Driver: "bundle", Artifact: core.Artifact{Path: "./payload/rt.zip", Digest: bytesDigest(runtimeZIP), SizeBytes: int64(len(runtimeZIP)), Format: "zip"}, Platform: core.Platform{OS: "linux", Arch: "amd64", Libc: "glibc", MinLibcVersion: "2.36"}, RequiresIsolation: []string{"filesystem", "network", "process-limits"}}},
			Assets:               []core.Asset{},
			ResourceSlots:        []core.ResourceSlot{{ID: "samples", Kind: "files", Description: "Session files"}},
			Providers:            []core.Provider{{ID: "inspect", Implements: contractRef, Driver: "process-json", RuntimeID: "rt", Entrypoint: "./bin/tool", Operation: "inspect", ResourceSlots: []string{"samples"}, Requests: &core.Permissions{Network: "none", Limits: core.Limits{TimeoutSeconds: 60, MemoryMiB: 512, CPUMilliCores: 1000, MaxOutputBytes: 1048576}}}},
		}
		encoded, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(encoded, &doc); err != nil {
			t.Fatal(err)
		}
		doc["providers"].([]any)[0].(map[string]any)["assetIds"] = []string{}
		encoded, err = json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		archive := testZip(t, []testZipEnt{
			{name: core.ManifestFileName, data: encoded, mode: 0644},
			{name: "SKILL.md", data: []byte(fmt.Sprintf("---\nname: selection-%03d\ndescription: Isolated selection fixture.\n---\nUse the bound inspection capability.\n", i)), mode: 0644},
			{name: "contracts/inspect.json", data: contract, mode: 0644},
			{name: "payload/rt.zip", data: runtimeZIP, mode: 0644},
		})
		installed, err := s.Import(bytes.NewReader(archive))
		if err != nil {
			t.Fatalf("import independent fixture %d: %v", i, err)
		}
		ids = append(ids, PackageSkillID(installed.Ref))
	}
	slices.Reverse(ids)
	ids = append(ids, ids[0])
	plan, err := s.PlanSelection(Selection{SkillIDs: ids})
	if err != nil || !plan.Ready || len(plan.Selection.SkillIDs) != count || !slices.IsSorted(plan.Selection.SkillIDs) || len(plan.Tools) != count || len(plan.Resources) != count {
		t.Fatalf("large runtime plan lost selection or dependency closure: skills=%d tools=%d resources=%d ready=%v err=%v", len(plan.Selection.SkillIDs), len(plan.Tools), len(plan.Resources), plan.Ready, err)
	}
	binding, err := core.DecodeBinding(plan.Binding)
	if err != nil || len(binding.Packages) != count || len(binding.Skills) != count || len(binding.Capabilities) != count || len(binding.Grants) != count {
		t.Fatalf("large binding was truncated: packages=%d skills=%d capabilities=%d grants=%d err=%v", len(binding.Packages), len(binding.Skills), len(binding.Capabilities), len(binding.Grants), err)
	}
	if err := s.ValidateBinding(plan.Binding); err != nil {
		t.Fatalf("large binding failed existing integrity/permission validation: %v", err)
	}
	if _, err := s.PlanSelection(Selection{SkillIDs: append(slices.Clone(ids), "rs_"+strings.Repeat("f", 64))}); err == nil || !strings.Contains(err.Error(), "已停用或不可用") {
		t.Fatalf("large selection bypassed unknown-package validation: %v", err)
	}
}
