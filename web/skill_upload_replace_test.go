package web

// Upload-replacement and force-delete tests: a second version of the same
// package asks before touching the catalog (replace = delete old versions and
// install, keep-old = discard the staged bytes), and a referenced Skill can
// be force-deleted — the sessions keep their transcripts and simply lose the
// Skill (dynamic unload).

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/internal/testfs"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skillruntime/core"
	"github.com/yosukeno/pogo/skills"
)

// webStubExecutor satisfies the executor interface without starting anything:
// the describe handshake is answered from the fixture's known contract, calls
// are refused (nothing in these tests invokes a tool).
type webStubExecutor struct{}

func (webStubExecutor) ExecutionPlatform() string { return "linux/amd64" }
func (webStubExecutor) Identity() string          { return "local-bwrap-v1:" + strings.Repeat("ab", 32) }
func (webStubExecutor) Execute(ctx context.Context, e skillruntime.Execution) ([]byte, error) {
	return webStubExecutor{}.describe(ctx, e)
}
func (webStubExecutor) ExecuteDetailed(ctx context.Context, e skillruntime.Execution) (skillruntime.DetailedResult, error) {
	raw, err := webStubExecutor{}.describe(ctx, e)
	return skillruntime.DetailedResult{Output: raw}, err
}
func (webStubExecutor) describe(_ context.Context, e skillruntime.Execution) ([]byte, error) {
	if e.Request.Type != "describe" {
		return nil, errors.New("unexpected execution in web fixture")
	}
	contract := webContractJSON("test.replace.check")
	response := map[string]any{
		"protocolVersion": "pogo.tool/1", "callId": e.Request.CallID, "type": "describe",
		"operations": []any{map[string]any{
			"operation": "check",
			"contract":  map[string]string{"id": "test.replace.check", "version": "1.0.0", "digest": core.DigestBytes(contract)},
		}},
	}
	return json.Marshal(response)
}

// webZipEntry is one file in a synthetic archive.
type webZipEntry struct {
	name string
	data []byte
	mode os.FileMode
}

func webZip(t *testing.T, entries []webZipEntry) []byte {
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

// webContractJSON is one valid capability contract document.
func webContractJSON(id string) []byte {
	return []byte(fmt.Sprintf(`{
  "apiVersion": "pogo.skill/v1",
  "kind": "CapabilityContract",
  "id": %q,
  "version": "1.0.0",
  "description": "synthetic contract",
  "inputSchema": {"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object", "properties": {"query": {"type": "string", "minLength": 1}}, "required": ["query"], "additionalProperties": false},
  "outputSchema": {"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object", "properties": {"ok": {"type": "boolean"}}, "required": ["ok"], "additionalProperties": false},
  "fileInputs": [],
  "effects": {"readsUserFiles": false, "writesArtifacts": false, "mutatesExternalState": false},
  "retry": "never-automatic"
}`, id))
}

// webRuntimePackageZip builds a minimal but fully valid runtime Skill ZIP at
// the requested version — the same shape the publisher tools emit. Contract
// content is stable across versions so package upgrades stay consistent.
func webRuntimePackageZip(t *testing.T, id, version string) []byte {
	t.Helper()
	contract := webContractJSON("test.replace.check")
	ref := core.ContractRef{ID: "test.replace.check", Version: "1.0.0", Digest: core.DigestBytes(contract)}
	payload := webZip(t, []webZipEntry{{name: "bin/tool", data: []byte("#!/bin/true\n"), mode: 0o755}})
	artifact := core.Artifact{Path: "./payload/rt.zip", Digest: core.DigestBytes(payload), SizeBytes: int64(len(payload)), Format: "zip"}
	manifest := map[string]any{
		"apiVersion": core.APIVersion, "kind": "SkillRuntime", "id": id, "version": version,
		"displayName":          "Replace fixture",
		"requiredFeatures":     []string{"bundle.v1", "process-json.v1", "isolated-execution.v1"},
		"contracts":            []any{map[string]any{"ref": ref, "path": "./contracts/check.json"}},
		"requiresCapabilities": []any{ref},
		"runtimes": []any{map[string]any{"id": "rt", "driver": "bundle", "artifact": artifact,
			"platform":          core.Platform{OS: "linux", Arch: "amd64", Libc: "glibc", MinLibcVersion: "2.36"},
			"requiresIsolation": []string{"filesystem", "network", "process-limits"}}},
		"assets": []any{},
		"providers": []any{map[string]any{"id": "check", "implements": ref, "driver": "process-json", "runtimeId": "rt",
			"entrypoint": "./bin/tool", "operation": "check", "resourceSlots": []string{"samples"}, "assetIds": []string{},
			"requests": map[string]any{"network": "none", "limits": map[string]any{"timeoutSeconds": 60, "memoryMiB": 256, "cpuMilliCores": 1000, "maxOutputBytes": 1 << 20}}}},
		"resourceSlots": []any{map[string]string{"id": "samples", "kind": "files", "description": "Session files"}},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return webZip(t, []webZipEntry{
		{name: core.ManifestFileName, data: raw, mode: 0o644},
		{name: "SKILL.md", data: []byte("---\nname: replace-fixture\ndescription: Upgrade flow fixture.\n---\nUse the bound tool.\n"), mode: 0o644},
		{name: "contracts/check.json", data: contract, mode: 0o644},
		{name: "payload/rt.zip", data: payload, mode: 0o644},
	})
}

func resolveSkillUpload(t *testing.T, h *harness, token, action string) *http.Response {
	t.Helper()
	return h.do("POST", "/api/skills/upload/resolve", studioBody(map[string]string{"token": token, "action": action}))
}

func responseText(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSkillUploadReplaceConfirmAndResolve(t *testing.T) {
	service, err := skillruntime.NewManagementService(testfs.SealedTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) { c.SkillRuntime = service })

	// The first version installs outright — an empty catalog has nothing to
	// replace — and byte-identical re-uploads keep deduplicating to it.
	var first map[string]any
	runtimeSkillResponse(t, postSkillZip(t, h, webRuntimePackageZip(t, "org.test.replace", "1.0.0")), 201, &first)
	runtimeSkillResponse(t, postSkillZip(t, h, webRuntimePackageZip(t, "org.test.replace", "1.0.0")), 201, &first)
	if first["version"] != "1.0.0" {
		t.Fatalf("re-upload did not dedup: %v", first)
	}

	// A new version parks the bytes and asks instead of installing.
	var confirm map[string]any
	runtimeSkillResponse(t, postSkillZip(t, h, webRuntimePackageZip(t, "org.test.replace", "2.0.0")), 200, &confirm)
	if confirm["status"] != "confirm_replace" {
		t.Fatalf("expected confirm_replace, got: %v", confirm)
	}
	token, _ := confirm["confirm_token"].(string)
	if token == "" {
		t.Fatal("confirm response lacks a token")
	}
	incoming, _ := confirm["incoming"].(map[string]any)
	if incoming["version"] != "2.0.0" || incoming["id"] != "org.test.replace" {
		t.Fatalf("incoming identity: %v", incoming)
	}
	existing, _ := confirm["existing"].([]any)
	if len(existing) != 1 {
		t.Fatalf("existing versions: %v", existing)
	}

	// keep-old discards the staged archive and leaves the catalog untouched.
	var kept map[string]any
	runtimeSkillResponse(t, resolveSkillUpload(t, h, token, "keep-old"), 200, &kept)
	if kept["status"] != "kept_old" {
		t.Fatalf("keep-old response: %v", kept)
	}
	installed := service.List()
	if len(installed) != 1 || installed[0].Ref.Version != "1.0.0" {
		t.Fatalf("keep-old changed the catalog: %+v", installed)
	}
	// A token resolves exactly once.
	if resp := resolveSkillUpload(t, h, token, "keep-old"); resp.StatusCode != 404 {
		t.Fatalf("spent token resolved again: %d", resp.StatusCode)
	}

	// Ask again, then replace: the new version lands and the old one is gone.
	runtimeSkillResponse(t, postSkillZip(t, h, webRuntimePackageZip(t, "org.test.replace", "2.0.0")), 200, &confirm)
	token, _ = confirm["confirm_token"].(string)
	var replaced map[string]any
	runtimeSkillResponse(t, resolveSkillUpload(t, h, token, "replace"), 201, &replaced)
	if replaced["version"] != "2.0.0" {
		t.Fatalf("replace did not install v2: %v", replaced)
	}
	versions := map[string]bool{}
	for _, entry := range service.List() {
		versions[entry.Ref.Version] = true
	}
	if len(versions) != 1 || !versions["2.0.0"] {
		t.Fatalf("replace left extra versions: %v", versions)
	}
	replacedList, _ := replaced["replaced_versions"].([]any)
	if len(replacedList) != 1 || replacedList[0] != "1.0.0" {
		t.Fatalf("replaced_versions: %v", replaced["replaced_versions"])
	}
	// No parked decision file survives the resolve.
	if entries, err := os.ReadDir(filepath.Join(service.Root, ".skill-uploads")); err == nil {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "confirm-") || strings.HasPrefix(entry.Name(), "resolve-") {
				t.Fatalf("decision file left behind: %s", entry.Name())
			}
		}
	}
}

func TestRuntimeSkillForceDeleteUnblocksReferences(t *testing.T) {
	service, err := skillruntime.NewManagementService(testfs.SealedTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	service.Executor = webStubExecutor{}
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) { c.SkillRuntime = service })

	var first map[string]any
	runtimeSkillResponse(t, postSkillZip(t, h, webRuntimePackageZip(t, "org.test.force", "1.0.0")), 201, &first)
	ids, _ := first["skill_ids"].([]any)
	if len(ids) != 1 {
		t.Fatalf("skill ids: %v", first)
	}
	skillID, _ := ids[0].(string)

	body := studioAgentBody("References skill", "", "ready")
	body["skills"] = []string{skillID}
	var definition studioAgent
	runtimeSkillResponse(t, h.do("POST", "/api/manage/agents", studioBody(body)), 201, &definition)
	conversation := createStudioConversationBody(t, h, definition.ID, "")

	// The plain delete is blocked and names the blocker, not just a count.
	resp := h.do("DELETE", "/api/manage/skills/"+skillID, "")
	if resp.StatusCode != 409 {
		t.Fatalf("referenced skill delete returned %d", resp.StatusCode)
	}
	message := responseText(t, resp)
	if !strings.Contains(message, "强制删除") || !strings.Contains(message, "「References skill」") {
		t.Fatalf("409 does not name the blocker or the escape hatch: %s", message)
	}

	// Force succeeds, and the conversation that referenced the version still
	// answers its capability view — minus the deleted Skill, not with a 404.
	runtimeSkillResponse(t, h.do("DELETE", "/api/manage/skills/"+skillID+"?force=1", ""), 200, nil)
	if remaining := service.List(); len(remaining) != 0 {
		t.Fatalf("force delete left versions: %+v", remaining)
	}
	var view map[string]any
	runtimeSkillResponse(t, h.do("GET", "/api/skills?session_id="+conversation.ID, ""), 200, &view)
	if _, ok := view["skills"]; !ok {
		t.Fatalf("capability view after dynamic unload: %v", view)
	}
	for _, entry := range view["skills"].([]any) {
		if name, _ := entry.(map[string]any)["name"].(string); name == "replace-fixture" {
			t.Fatal("deleted skill still visible to the session")
		}
	}
}

func TestOrdinarySkillForceDelete(t *testing.T) {
	userDir := t.TempDir()
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) {
		c.SkillOptions = skills.Options{UserDir: userDir}
	})
	archive := webZip(t, []webZipEntry{{name: "ordinary-skill/SKILL.md", data: []byte("---\nname: ordinary-skill\ndescription: Ordinary fixture.\n---\nBody.\n"), mode: 0o644}})
	var result struct {
		SkillIDs []string `json:"skill_ids"`
	}
	runtimeSkillResponse(t, postSkillZip(t, h, archive), 201, &result)
	if len(result.SkillIDs) != 1 {
		t.Fatalf("skill ids: %+v", result)
	}

	body := studioAgentBody("Uses ordinary", "", "ready")
	body["skills"] = result.SkillIDs
	runtimeSkillResponse(t, h.do("POST", "/api/manage/agents", studioBody(body)), 201, nil)

	if resp := h.do("DELETE", "/api/manage/skills/"+result.SkillIDs[0], ""); resp.StatusCode != 409 {
		t.Fatalf("referenced ordinary delete returned %d: %s", resp.StatusCode, responseText(t, resp))
	}
	runtimeSkillResponse(t, h.do("DELETE", "/api/manage/skills/"+result.SkillIDs[0]+"?force=1", ""), 200, nil)
	if _, err := os.Stat(filepath.Join(userDir, "ordinary-skill")); !os.IsNotExist(err) {
		t.Fatalf("ordinary skill directory survived force delete: %v", err)
	}
}
