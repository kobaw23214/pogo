package skillruntime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/skillruntime/core"
)

// installWorkspaceFixture installs a package whose manifest declares one
// workspaceData asset: a docs tree (subdir + two files) that must land in the
// binding workspace. Returns the service plus a plan whose binding drives the
// materializer.
func installWorkspaceFixture(t *testing.T, s *Service, extraManifest func(map[string]any)) Installed {
	t.Helper()
	rt := testZip(t, []testZipEnt{{name: "bin/tool", data: []byte("#!/bin/true\n"), mode: 0o755}})
	docs := testZip(t, []testZipEnt{
		{name: "raw/report.pdf", data: []byte("%PDF-1.4 fake"), mode: 0o644},
		{name: "index.json", data: []byte(`{"reports":1}`), mode: 0o644},
	})
	c := fixtureContractJSON("test.file.inspect", true, false)
	ref := core.ContractRef{ID: "test.file.inspect", Version: "1.0.0", Digest: bytesDigest(c)}
	m := core.Manifest{
		APIVersion: core.APIVersion, Kind: "SkillRuntime", ID: testPackageID, Version: "1.0.0", DisplayName: "File analysis",
		RequiredFeatures:     []string{"bundle.v1", "process-json.v1", "isolated-execution.v1"},
		Contracts:            []core.ManifestContractEntry{{Ref: ref, Path: "./contracts/inspect.json"}},
		RequiresCapabilities: []core.ContractRef{ref},
		Runtimes: []core.Runtime{{ID: "rt", Driver: "bundle",
			Artifact:          core.Artifact{Path: "./payload/rt.zip", Digest: bytesDigest(rt), SizeBytes: int64(len(rt)), Format: "zip"},
			Platform:          core.Platform{OS: "linux", Arch: "amd64", Libc: "glibc", MinLibcVersion: "2.36"},
			RequiresIsolation: []string{"filesystem", "network", "process-limits"}}},
		Assets:        []core.Asset{{ID: "docs", Artifact: core.Artifact{Path: "./payload/docs.zip", Digest: bytesDigest(docs), SizeBytes: int64(len(docs)), Format: "zip"}}},
		ResourceSlots: []core.ResourceSlot{{ID: "samples", Kind: "files", Description: "Session files"}},
		Providers: []core.Provider{{ID: "inspect", Implements: ref, Driver: "process-json", RuntimeID: "rt", Entrypoint: "./bin/tool",
			Operation: "inspect", ResourceSlots: []string{"samples"},
			Requests:  &core.Permissions{Network: "none", Limits: core.Limits{TimeoutSeconds: 60, MemoryMiB: 512, CPUMilliCores: 1000, MaxOutputBytes: 1048576}}}},
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["providers"].([]any)[0].(map[string]any)["assetIds"] = []string{}
	doc["workspaceData"] = []any{map[string]any{"assetId": "docs", "dest": "atkweb"}}
	if extraManifest != nil {
		extraManifest(doc)
	}
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	archive := testZip(t, []testZipEnt{
		{name: core.ManifestFileName, data: raw, mode: 0o644},
		{name: "SKILL.md", data: []byte("---\nname: file-analysis\ndescription: Inspect files.\n---\nUse the bound inspection tool.\n"), mode: 0o644},
		{name: "contracts/inspect.json", data: c, mode: 0o644},
		{name: "payload/rt.zip", data: rt, mode: 0o644},
		{name: "payload/docs.zip", data: docs, mode: 0o644},
	})
	installed, err := s.Import(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	return installed
}

func TestMaterializeWorkspaceData(t *testing.T) {
	s := newStubService(t)
	installed := installWorkspaceFixture(t, s, nil)
	plan, err := s.PlanSelection(Selection{SkillIDs: []string{PackageSkillID(installed.Ref)}})
	if err != nil || !plan.Ready {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	ws := t.TempDir()
	if err := s.MaterializeWorkspaceData(plan.Binding, ws); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	for _, rel := range []string{"atkweb/index.json", "atkweb/raw/report.pdf", "atkweb/" + workspaceDataMarker} {
		if _, err := os.Stat(filepath.Join(ws, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
	// Files are read-only best effort: the skill's data is not the agent's to
	// edit, even though it lives in the agent's tree.
	fi, err := os.Stat(filepath.Join(ws, "atkweb", "index.json"))
	if err != nil || fi.Mode().Perm()&0o222 != 0 {
		t.Fatalf("materialized file writable: %v %v", fi, err)
	}
	// Idempotent: a second pass over the same marker neither errors nor
	// touches mtimes.
	before, _ := os.Stat(filepath.Join(ws, "atkweb", "index.json"))
	if err := s.MaterializeWorkspaceData(plan.Binding, ws); err != nil {
		t.Fatalf("second materialize: %v", err)
	}
	after, _ := os.Stat(filepath.Join(ws, "atkweb", "index.json"))
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("second materialize rewrote a current copy")
	}
	// A stale marker (hand edit or package upgrade) rebuilds the copy.
	if err := os.WriteFile(filepath.Join(ws, "atkweb", workspaceDataMarker), []byte(`{"assetDigest":"stale"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.MaterializeWorkspaceData(plan.Binding, ws); err != nil {
		t.Fatalf("refresh materialize: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(ws, "atkweb", "index.json"))
	if err != nil || string(data) != `{"reports":1}` {
		t.Fatalf("refresh did not restore content: %q %v", data, err)
	}
}

func TestMaterializeWorkspaceDataBudget(t *testing.T) {
	s := newStubService(t)
	s.WorkspaceDataMaxBytes = 1
	installed := installWorkspaceFixture(t, s, nil)
	plan, err := s.PlanSelection(Selection{SkillIDs: []string{PackageSkillID(installed.Ref)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MaterializeWorkspaceData(plan.Binding, t.TempDir()); err == nil {
		t.Fatal("budget cap not enforced")
	}
}

func TestManifestRejectsUnknownWorkspaceAsset(t *testing.T) {
	// 声明了不存在资产的 workspaceData 必须在安装校验期被拒绝。
	s2 := newStubService(t)
	rt := testZip(t, []testZipEnt{{name: "bin/tool", data: []byte("#!/bin/true\n"), mode: 0o755}})
	docs := testZip(t, []testZipEnt{{name: "index.json", data: []byte("{}"), mode: 0o644}})
	c := fixtureContractJSON("test.file.inspect", true, false)
	ref := core.ContractRef{ID: "test.file.inspect", Version: "1.0.0", Digest: bytesDigest(c)}
	m := map[string]any{
		"apiVersion": core.APIVersion, "kind": "SkillRuntime", "id": testPackageID, "version": "1.0.0", "displayName": "File analysis",
		"requiredFeatures": []string{"bundle.v1", "process-json.v1", "isolated-execution.v1"},
		"contracts":            []any{map[string]any{"ref": map[string]any{"id": ref.ID, "version": ref.Version, "digest": ref.Digest}, "path": "./contracts/inspect.json"}},
		"requiresCapabilities": []any{map[string]any{"id": ref.ID, "version": ref.Version, "digest": ref.Digest}},
		"runtimes": []any{map[string]any{"id": "rt", "driver": "bundle",
			"artifact":  map[string]any{"path": "./payload/rt.zip", "digest": bytesDigest(rt), "sizeBytes": len(rt), "format": "zip"},
			"platform":  map[string]any{"os": "linux", "arch": "amd64", "libc": "glibc", "minLibcVersion": "2.36"},
			"requiresIsolation": []string{"filesystem", "network", "process-limits"}}},
		"assets":        []any{map[string]any{"id": "docs", "artifact": map[string]any{"path": "./payload/docs.zip", "digest": bytesDigest(docs), "sizeBytes": len(docs), "format": "zip"}}},
		"resourceSlots": []any{map[string]any{"id": "samples", "kind": "files", "description": "Session files"}},
		"providers":     []any{map[string]any{"id": "inspect", "implements": map[string]any{"id": ref.ID, "version": ref.Version, "digest": ref.Digest}, "driver": "process-json", "runtimeId": "rt", "entrypoint": "./bin/tool", "operation": "inspect", "assetIds": []string{}, "resourceSlots": []string{"samples"}, "requests": map[string]any{"network": "none", "limits": map[string]any{"timeoutSeconds": 60, "memoryMiB": 512, "cpuMilliCores": 1000, "maxOutputBytes": 1048576}}}},
		"workspaceData": []any{map[string]any{"assetId": "no-such-asset", "dest": "atkweb"}},
	}
	raw, _ := json.Marshal(m)
	archive := testZip(t, []testZipEnt{
		{name: core.ManifestFileName, data: raw, mode: 0o644},
		{name: "SKILL.md", data: []byte("---\nname: file-analysis\ndescription: Inspect files.\n---\nUse the tool.\n"), mode: 0o644},
		{name: "contracts/inspect.json", data: c, mode: 0o644},
		{name: "payload/rt.zip", data: rt, mode: 0o644},
		{name: "payload/docs.zip", data: docs, mode: 0o644},
	})
	_, err2 := s2.Import(bytes.NewReader(archive))
	if err2 == nil || !strings.Contains(err2.Error(), "unknown asset id") {
		t.Fatalf("want unknown-asset rejection, got: %v", err2)
	}
}
