package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/yosukeno/pogo/internal/testfs"
	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skillruntime/core"
)

func selectionFixtureZIP(t *testing.T) []byte {
	t.Helper()
	zipFiles := func(files map[string][]byte, executable bool) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, data := range files {
			h := &zip.FileHeader{Name: name, Method: zip.Deflate}
			if executable {
				h.SetMode(0755)
			} else {
				h.SetMode(0644)
			}
			w, err := zw.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.Write(data); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	contract, err := os.ReadFile("../cmd/pogo-yara-provider/contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var c core.Contract
	if err := json.Unmarshal(contract, &c); err != nil {
		t.Fatal(err)
	}
	ref := core.ContractRef{ID: c.ID, Version: c.Version, Digest: core.DigestBytes(contract)}
	runtime := zipFiles(map[string][]byte{"bin/tool": []byte("#!/bin/true\n# fixture; never executed\n")}, true)
	manifest := map[string]any{
		"apiVersion": core.APIVersion, "kind": "SkillRuntime", "id": "org.test.selection", "version": "1.0.0", "displayName": "Selection fixture",
		"requiredFeatures":     []string{"bundle.v1", "process-json.v1", "isolated-execution.v1"},
		"contracts":            []core.ManifestContractEntry{{Ref: ref, Path: "./contracts/scan.json"}},
		"requiresCapabilities": []core.ContractRef{ref},
		"runtimes":             []core.Runtime{{ID: "rt", Driver: "bundle", Artifact: core.Artifact{Path: "./payload/rt.zip", Digest: core.DigestBytes(runtime), SizeBytes: int64(len(runtime)), Format: "zip"}, Platform: core.Platform{OS: "linux", Arch: "amd64", Libc: "glibc", MinLibcVersion: "2.36"}, RequiresIsolation: []string{"filesystem", "network", "process-limits"}}},
		"assets":               []core.Asset{}, "resourceSlots": []core.ResourceSlot{{ID: "samples", Kind: "files", Description: "当前会话上传文件"}},
		"providers": []any{map[string]any{"id": "scan", "implements": ref, "driver": "process-json", "runtimeId": "rt", "entrypoint": "./bin/tool", "operation": "scan", "resourceSlots": []string{"samples"}, "assetIds": []string{}, "requests": core.Permissions{Network: "none", Limits: core.Limits{TimeoutSeconds: 30, MemoryMiB: 256, CPUMilliCores: 1000, MaxOutputBytes: 1048576}}}},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return zipFiles(map[string][]byte{core.ManifestFileName: raw, "contracts/scan.json": contract, "payload/rt.zip": runtime, "SKILL.md": []byte("---\nname: scan\ndescription: Scan files.\n---\nUse the selected scan capability.")}, false)
}

func TestRuntimeSkillSkillStudioDraftAndPublishBoundary(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	service, err := skillruntime.NewManagementService(testfs.SealedTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	h.mgr.cfg.SkillRuntime = service
	installed, err := service.Import(bytes.NewReader(selectionFixtureZIP(t)))
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Skills []struct{ ID, Source, State string }
		Tools  []skillruntime.ToolEntry `json:"runtime_tools"`
	}
	runtimeSkillResponse(t, h.doAs(h.token, "GET", "/api/manage/capabilities", ""), 200, &catalog)
	id := skillruntime.PackageSkillID(installed.Ref)
	found := false
	for _, sk := range catalog.Skills {
		if sk.ID == id && sk.Source == "bundle" && sk.State == "needs_configuration" {
			found = true
		}
	}
	if !found || len(catalog.Tools) != 1 {
		t.Fatalf("Skill Skill absent from unified catalog: %+v", catalog)
	}
	selection := skillruntime.Selection{SkillIDs: []string{id}}
	raw, _ := json.Marshal(selection)
	runtimeSkillResponse(t, h.doAs(aliceToken, "POST", "/api/manage/skill-selection", string(raw)), 403, nil)
	var preview skillruntime.SelectionPlan
	runtimeSkillResponse(t, h.doAs(h.token, "POST", "/api/manage/skill-selection", string(raw)), 200, &preview)
	if preview.Ready || len(preview.Tools) != 1 || len(preview.Resources) != 1 {
		t.Fatalf("unconfigured executor preview: %+v", preview)
	}
	body := studioAgentCapsBody("Skill draft", "draft", map[string]any{"skills": selection.SkillIDs, "mcp_servers": []string{}})
	var a studioAgent
	runtimeSkillResponse(t, h.doAs(h.token, "POST", "/api/manage/agents", studioBody(body)), 201, &a)
	if len(a.SkillBinding) == 0 || a.Status != "draft" {
		t.Fatalf("draft did not retain generated binding: %+v", a)
	}
	body["status"] = "ready"
	body["revision"] = a.Revision
	runtimeSkillResponse(t, h.doAs(h.token, "PATCH", "/api/manage/agents/"+a.ID, studioBody(body)), 400, nil)
	body["status"] = "draft"
	body["skills"] = []string{}
	var detached studioAgent
	runtimeSkillResponse(t, h.doAs(h.token, "PATCH", "/api/manage/agents/"+a.ID, studioBody(body)), 200, &detached)
	if len(detached.SkillBinding) > 0 {
		t.Fatal("deselecting Skill retained Skill authority")
	}
}
