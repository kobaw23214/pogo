package skillruntime

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/internal/testfs"
	"github.com/yosukeno/pogo/skillruntime/core"
)

// newStubService exercises trusted host state without starting any process.
type stubExecutor struct{}

func (stubExecutor) Identity() string { return "local-bwrap-v1:" + strings.Repeat("ab", 32) }
func (stubExecutor) Execute(context.Context, Execution) ([]byte, error) {
	return nil, fmt.Errorf("unexpected execution in host-state test")
}
func (stubExecutor) ExecuteDetailed(context.Context, Execution) (DetailedResult, error) {
	return DetailedResult{}, fmt.Errorf("unexpected execution in host-state test")
}

func newStubService(t *testing.T) *Service {
	t.Helper()
	return openStubService(t, testfs.SealedTempDir(t))
}

func openStubService(t *testing.T, root string) *Service {
	t.Helper()
	store, err := core.OpenStore(filepath.Join(root, "packages"))
	if err != nil {
		t.Fatal(err)
	}
	platform := detectPlatform("linux", "amd64", "")
	s := &Service{
		Root:      root,
		Store:     store,
		Executor:  stubExecutor{},
		platform:  &platform,
		installed: []Installed{},
		active:    map[string]map[string]context.CancelFunc{},
	}
	if raw, err := os.ReadFile(filepath.Join(root, "catalog.json")); err == nil {
		if err := json.Unmarshal(raw, &s.installed); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

type testZipEnt struct {
	name string
	data []byte
	mode os.FileMode
}

func testZip(t *testing.T, entries []testZipEnt) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func fixtureContractJSON(id string, fileInput, writes bool) []byte {
	fileInputs, reads := "[]", "false"
	props := `"query": {"type": "string", "minLength": 1}`
	required := `"query"`
	if fileInput {
		fileInputs, reads = `["/fileId"]`, "true"
		props = `"fileId": {"type": "string", "minLength": 1}`
		required = `"fileId"`
	}
	return []byte(fmt.Sprintf(`{
  "apiVersion": "pogo.skill/v1",
  "kind": "CapabilityContract",
  "id": "%s",
  "version": "1.0.0",
  "description": "synthetic Skill contract",
  "inputSchema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "properties": {%s},
    "required": [%s],
    "additionalProperties": false
  },
  "outputSchema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "properties": {"ok": {"type": "boolean"}},
    "required": ["ok"],
    "additionalProperties": false
  },
  "fileInputs": %s,
  "effects": {"readsUserFiles": %s, "writesArtifacts": %v, "mutatesExternalState": false},
  "retry": "never-automatic"
}`, id, props, required, fileInputs, reads, writes))
}

const testPackageID = "org.test.skill"

func installSkillFixture(t *testing.T, s *Service) (Installed, map[string]core.ContractRef) {
	return installSkillFixtureForPlatform(t, s, "amd64")
}

func installSkillFixtureForPlatform(t *testing.T, s *Service, arch string) (Installed, map[string]core.ContractRef) {
	t.Helper()
	rt := testZip(t, []testZipEnt{{name: "bin/tool", data: []byte("#!/bin/true\n"), mode: 0755}})
	c := fixtureContractJSON("test.file.inspect", true, false)
	ref := core.ContractRef{ID: "test.file.inspect", Version: "1.0.0", Digest: bytesDigest(c)}
	m := core.Manifest{APIVersion: core.APIVersion, Kind: "SkillRuntime", ID: testPackageID, Version: "1.0.0", DisplayName: "File analysis", RequiredFeatures: []string{"bundle.v1", "process-json.v1", "isolated-execution.v1"},
		Contracts: []core.ManifestContractEntry{{Ref: ref, Path: "./contracts/inspect.json"}}, RequiresCapabilities: []core.ContractRef{ref},
		Runtimes: []core.Runtime{{ID: "rt", Driver: "bundle", Artifact: core.Artifact{Path: "./payload/rt.zip", Digest: bytesDigest(rt), SizeBytes: int64(len(rt)), Format: "zip"}, Platform: core.Platform{OS: "linux", Arch: arch, Libc: "glibc", MinLibcVersion: "2.36"}, RequiresIsolation: []string{"filesystem", "network", "process-limits"}}},
		Assets:   []core.Asset{}, ResourceSlots: []core.ResourceSlot{{ID: "samples", Kind: "files", Description: "Session files"}},
		Providers: []core.Provider{{ID: "inspect", Implements: ref, Driver: "process-json", RuntimeID: "rt", Entrypoint: "./bin/tool", Operation: "inspect", ResourceSlots: []string{"samples"}, AssetIDs: []string{}, Requests: &core.Permissions{Network: "none", Limits: core.Limits{TimeoutSeconds: 60, MemoryMiB: 512, CPUMilliCores: 1000, MaxOutputBytes: 1048576}}}},
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
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	archive := testZip(t, []testZipEnt{{name: core.ManifestFileName, data: raw, mode: 0644}, {name: "SKILL.md", data: []byte("---\nname: file-analysis\ndescription: Inspect files.\n---\nUse the bound inspection tool.\n"), mode: 0644}, {name: "contracts/inspect.json", data: c, mode: 0644}, {name: "payload/rt.zip", data: rt, mode: 0644}})
	installed, err := s.Import(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	return installed, map[string]core.ContractRef{"inspect": ref}
}
