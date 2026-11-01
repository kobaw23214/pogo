package web

// Packaged runtime Skill tools on the global /mcp endpoint: opt-in exposure
// through the include_tools whitelist, the sessionless policy gate with the
// executor standing in for the bash sandbox, the serve_catalog picker rows,
// and the install/disable/delete hook resync. The wire side is exercised for
// real (the SDK's own client), the same fixture philosophy as mcp_test.go;
// the executor is an in-process stand-in for bwrap that answers the
// pogo.tool/1 frames (webStubExecutor only describes — nothing in its tests
// invokes — so this one adds the invoke half, mirroring skillruntime's
// wire_protocol_test.go recordingExecutor).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/internal/testfs"
	"github.com/yosukeno/pogo/skillobserve"
	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skillruntime/core"
)

// webRecordingExecutor answers both halves of the pogo.tool/1 conversation
// without starting anything: describe from the fixture's contract registry,
// invoke with a schema-conformant result.
type webRecordingExecutor struct {
	mu        sync.Mutex
	ops       map[string]string                        // package ID → operation
	contracts map[string]skillruntime.ContractIdentity // package ID → contract
	events    []skillruntime.Execution
}

func newWebRecordingExecutor() *webRecordingExecutor {
	return &webRecordingExecutor{
		ops:       map[string]string{},
		contracts: map[string]skillruntime.ContractIdentity{},
	}
}

// register teaches the describe handshake one package's operation and
// contract identity.
func (r *webRecordingExecutor) register(packageID, operation, contractID string, contract []byte) {
	r.ops[packageID] = operation
	r.contracts[packageID] = skillruntime.ContractIdentity{ID: contractID, Version: "1.0.0", Digest: core.DigestBytes(contract)}
}

func (r *webRecordingExecutor) ExecutionPlatform() string { return "linux/amd64" }
func (r *webRecordingExecutor) Identity() string          { return "local-bwrap-v1:" + strings.Repeat("ab", 32) }

func (r *webRecordingExecutor) Execute(ctx context.Context, e skillruntime.Execution) ([]byte, error) {
	out, err := r.ExecuteDetailed(ctx, e)
	return out.Output, err
}

func (r *webRecordingExecutor) ExecuteDetailed(_ context.Context, e skillruntime.Execution) (skillruntime.DetailedResult, error) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
	frame := map[string]any{"protocolVersion": "pogo.tool/1", "callId": e.Request.CallID}
	switch e.Request.Type {
	case skillruntime.ProcessRequestDescribe:
		contract, ok := r.contracts[e.Package.ID]
		if !ok {
			return skillruntime.DetailedResult{}, errors.New("describe of unregistered package " + e.Package.ID)
		}
		frame["type"] = skillruntime.ProcessRequestDescribe
		frame["operations"] = []any{map[string]any{"operation": r.ops[e.Package.ID], "contract": contract}}
	case skillruntime.ProcessRequestInvoke:
		frame["type"] = "result"
		frame["output"] = map[string]any{"ok": true}
		frame["artifacts"] = []any{}
	default:
		frame["type"] = "error"
		frame["error"] = map[string]string{"code": "UNSUPPORTED_OPERATION", "message": fmt.Sprintf("unknown request type %q", e.Request.Type)}
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return skillruntime.DetailedResult{}, err
	}
	return skillruntime.DetailedResult{Output: raw}, nil
}

func (r *webRecordingExecutor) invocations() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e.Request.Type == skillruntime.ProcessRequestInvoke {
			n++
		}
	}
	return n
}

// webFullContractJSON is one valid capability contract that reads a file
// input and writes artifacts — the non-read-only counterpart of
// webContractJSON, for the policy-refusal and catalog-flag tests.
func webFullContractJSON(id string) []byte {
	return []byte(fmt.Sprintf(`{
  "apiVersion": "pogo.skill/v1",
  "kind": "CapabilityContract",
  "id": %q,
  "version": "1.0.0",
  "description": "synthetic file contract",
  "inputSchema": {"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object", "properties": {"sample": {"type": "string"}}, "additionalProperties": false},
  "outputSchema": {"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object", "properties": {"ok": {"type": "boolean"}}, "required": ["ok"], "additionalProperties": false},
  "fileInputs": ["/sample"],
  "effects": {"readsUserFiles": true, "writesArtifacts": true, "mutatesExternalState": false},
  "retry": "never-automatic"
}`, id))
}

// webRuntimePackageZipFull builds a package around webFullContractJSON: the
// provider declares both a files slot (uploads) and an artifacts slot
// (outputs), which the contract's effects require.
func webRuntimePackageZipFull(t *testing.T, id, version string) []byte {
	t.Helper()
	contract := webFullContractJSON("test.full.inspect")
	ref := core.ContractRef{ID: "test.full.inspect", Version: "1.0.0", Digest: core.DigestBytes(contract)}
	payload := webZip(t, []webZipEntry{{name: "bin/tool", data: []byte("#!/bin/true\n"), mode: 0o755}})
	artifact := core.Artifact{Path: "./payload/rt.zip", Digest: core.DigestBytes(payload), SizeBytes: int64(len(payload)), Format: "zip"}
	manifest := map[string]any{
		"apiVersion": core.APIVersion, "kind": "SkillRuntime", "id": id, "version": version,
		"displayName":          "Full fixture",
		"requiredFeatures":     []string{"bundle.v1", "process-json.v1", "isolated-execution.v1"},
		"contracts":            []any{map[string]any{"ref": ref, "path": "./contracts/inspect.json"}},
		"requiresCapabilities": []any{ref},
		"runtimes": []any{map[string]any{"id": "rt", "driver": "bundle", "artifact": artifact,
			"platform":          core.Platform{OS: "linux", Arch: "amd64", Libc: "glibc", MinLibcVersion: "2.36"},
			"requiresIsolation": []string{"filesystem", "network", "process-limits"}}},
		"assets": []any{},
		"providers": []any{map[string]any{"id": "inspect", "implements": ref, "driver": "process-json", "runtimeId": "rt",
			"entrypoint": "./bin/tool", "operation": "inspect", "resourceSlots": []string{"samples", "output"}, "assetIds": []string{},
			"requests": map[string]any{"network": "none", "limits": map[string]any{"timeoutSeconds": 60, "memoryMiB": 256, "cpuMilliCores": 1000, "maxOutputBytes": 1 << 20}}}},
		"resourceSlots": []any{
			map[string]string{"id": "samples", "kind": "files", "description": "Session files"},
			map[string]string{"id": "output", "kind": "artifacts", "description": "Produced artifacts"},
		},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return webZip(t, []webZipEntry{
		{name: core.ManifestFileName, data: raw, mode: 0o644},
		{name: "SKILL.md", data: []byte("---\nname: full-fixture\ndescription: File-in/artifact-out fixture.\n---\nUse the bound tool.\n"), mode: 0o644},
		{name: "contracts/inspect.json", data: contract, mode: 0o644},
		{name: "payload/rt.zip", data: payload, mode: 0o644},
	})
}

// runtimeServeFixture builds the standard MCP serve harness with a packaged
// Skill runtime behind it: a management service over a private root (outside
// the workspace, as NewManager requires), with the recording executor
// standing in for bwrap when withExecutor is set.
func runtimeServeFixture(t *testing.T, withExecutor bool) (*mcpClientAPI, *skillruntime.Service, *webRecordingExecutor) {
	t.Helper()
	service, err := skillruntime.NewManagementService(testfs.SealedTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	var rec *webRecordingExecutor
	if withExecutor {
		rec = newWebRecordingExecutor()
		rec.register("org.test.mcp", "check", "test.replace.check", webContractJSON("test.replace.check"))
		rec.register("org.test.full", "inspect", "test.full.inspect", webFullContractJSON("test.full.inspect"))
		service.Executor = rec
	}
	a := serveFixture(t, func(c *Config) { c.SkillRuntime = service })
	return a, service, rec
}

// installRuntimeZip posts a package archive through the real upload endpoint —
// the path whose success fires the OnRuntimeSkillChange hook.
func installRuntimeZip(t *testing.T, a *mcpClientAPI, data []byte) map[string]any {
	t.Helper()
	var out map[string]any
	runtimeSkillResponse(t, postSkillZip(t, a.h, data), 201, &out)
	return out
}

// TestMCPRuntimeOptInEmptyWhitelist: an empty include_tools exposes no runtime
// contract tool at all — the forwarded tools' opt-in rule verbatim — while
// the pipeline helpers sit in the builtin category (present whenever a
// package exists, filtered by the middleware like agent_*).
func TestMCPRuntimeOptInEmptyWhitelist(t *testing.T) {
	a, _, _ := runtimeServeFixture(t, true)
	installRuntimeZip(t, a, webRuntimePackageZip(t, "org.test.mcp", "1.0.0"))
	a.putMCP(t, `{"servers":{},"server":{"enabled":true,"policy":"auto"}}`)
	cs := connectStreamable(t, a.srv.URL, "")

	names := listMCPNames(t, cs)
	if names["skill_test_replace_check"] {
		t.Fatal("runtime contract tool registered without a whitelist match")
	}
	if !names["skill_upload_file"] || !names["skill_read_artifact"] {
		t.Fatalf("helpers missing from the unfiltered list: %v", names)
	}

	// A non-empty whitelist that matches no skill_* name registers no contract
	// tool, and the middleware filters the helpers out of tools/list.
	a.putMCP(t, `{"servers":{},"server":{"enabled":true,"policy":"auto","include_tools":["agent_*"]}}`)
	names = listMCPNames(t, cs)
	if names["skill_test_replace_check"] || names["skill_upload_file"] || names["skill_read_artifact"] {
		t.Fatalf("list = %v, want no skill_* entries", names)
	}
}

// TestMCPRuntimeWhitelistHitAndDirectCall: a prefix entry lands the contract
// tool on tools/list, a direct call reaches the executor, and the upload
// helper mints a fileId against the pseudo session.
func TestMCPRuntimeWhitelistHitAndDirectCall(t *testing.T) {
	a, _, rec := runtimeServeFixture(t, true)
	installRuntimeZip(t, a, webRuntimePackageZip(t, "org.test.mcp", "1.0.0"))
	a.putMCP(t, `{"servers":{},"server":{"enabled":true,"policy":"auto","include_tools":["skill_test_*","skill_upload_file","skill_read_artifact"]}}`)
	cs := connectStreamable(t, a.srv.URL, "")

	names := listMCPNames(t, cs)
	if !names["skill_test_replace_check"] {
		t.Fatalf("list = %v, want skill_test_replace_check", names)
	}
	res := callTool(t, cs, "skill_test_replace_check", map[string]any{"query": "x"})
	if res.IsError {
		t.Fatalf("direct call failed: %s", resultText(res))
	}
	if text := resultText(res); !strings.Contains(text, `"ok":true`) {
		t.Fatalf("call result = %q, want the stub's ok:true output", text)
	}
	if rec.invocations() == 0 {
		t.Fatal("the executor saw no invoke — the call did not reach the runtime")
	}

	up := callTool(t, cs, "skill_upload_file", map[string]any{"data_base64": base64.StdEncoding.EncodeToString([]byte("payload"))})
	m := structured(t, up)
	if fileID, _ := m["fileId"].(string); !strings.HasPrefix(fileID, "file:") {
		t.Fatalf("upload result = %v, want a fileId", m)
	}
}

// TestMCPRuntimePolicyGate: the sessionless matrix with the executor standing
// in for the bash sandbox — standard runs read-only contracts while the
// executor is up, refuses non-read-only ones (environment-independent), and
// strict refuses everything. The refusal wording is the synth tools'.
func TestMCPRuntimePolicyGate(t *testing.T) {
	a, _, _ := runtimeServeFixture(t, true)
	installRuntimeZip(t, a, webRuntimePackageZip(t, "org.test.mcp", "1.0.0"))
	installRuntimeZip(t, a, webRuntimePackageZipFull(t, "org.test.full", "1.0.0"))
	a.putMCP(t, `{"servers":{},"server":{"enabled":true,"policy":"standard","include_tools":["skill_test_*"]}}`)
	cs := connectStreamable(t, a.srv.URL, "")

	if res := callTool(t, cs, "skill_test_replace_check", map[string]any{"query": "x"}); res.IsError {
		t.Fatalf("standard + read-only + executor up refused: %s", resultText(res))
	}
	res := callTool(t, cs, "skill_test_full_inspect", map[string]any{})
	if !res.IsError || !strings.Contains(resultText(res), "agent_submit_task") {
		t.Fatalf("standard + non-read-only = (isError=%v) %q, want the gate refusal", res.IsError, resultText(res))
	}

	a.putMCP(t, `{"servers":{},"server":{"enabled":true,"policy":"strict","include_tools":["skill_test_*"]}}`)
	if res := callTool(t, cs, "skill_test_replace_check", map[string]any{"query": "x"}); !res.IsError {
		t.Fatal("strict + read-only ran; want a refusal")
	}
}

// TestMCPRuntimeServeCatalog: GET /api/mcp's runtime group lists one row per
// installed version with each contract's tool row and effect flags, revoked
// versions keep their row with empty tools, and the helpers join the builtin
// group while any package is installed.
func TestMCPRuntimeServeCatalog(t *testing.T) {
	a, _, _ := runtimeServeFixture(t, true)
	installed := installRuntimeZip(t, a, webRuntimePackageZip(t, "org.test.mcp", "1.0.0"))
	installRuntimeZip(t, a, webRuntimePackageZipFull(t, "org.test.full", "2.3.0"))

	cat := a.getMCP(t).ServeCatalog
	if cat == nil {
		t.Fatal("GET /api/mcp carries no serve_catalog")
	}
	if len(cat.Runtime) != 2 {
		t.Fatalf("runtime = %#v, want two rows", cat.Runtime)
	}
	byVersion := map[string]mcpServeRuntimePkg{}
	for _, row := range cat.Runtime {
		byVersion[row.Version] = row
	}
	plain, ok := byVersion["1.0.0"]
	if !ok || !strings.HasPrefix(plain.ID, "rs_") || plain.Name != "Replace fixture" || plain.PackageName != "Replace fixture" || plain.State != "available" {
		t.Fatalf("plain row = %#v", plain)
	}
	if len(plain.Tools) != 1 {
		t.Fatalf("plain tools = %#v", plain.Tools)
	}
	tool := plain.Tools[0]
	if tool.Name != "skill_test_replace_check" || tool.Contract != "test.replace.check" || tool.Description == "" ||
		!tool.ReadOnly || tool.FileInputs || tool.WritesArtifacts {
		t.Fatalf("plain tool row = %#v", tool)
	}
	full, ok := byVersion["2.3.0"]
	if !ok || len(full.Tools) != 1 {
		t.Fatalf("full row = %#v", full)
	}
	tool = full.Tools[0]
	if tool.Name != "skill_test_full_inspect" || tool.Contract != "test.full.inspect" ||
		tool.ReadOnly || !tool.FileInputs || !tool.WritesArtifacts {
		t.Fatalf("full tool row = %#v", tool)
	}

	// The helpers are builtin rows while packages exist — and the mirror is
	// honest against a live tools/list.
	builtin := map[string]bool{}
	for _, b := range cat.Builtin {
		builtin[b.Name] = true
	}
	if !builtin["skill_upload_file"] || !builtin["skill_read_artifact"] {
		t.Fatalf("builtin = %v, want the two helpers", builtin)
	}
	a.putMCP(t, `{"servers":{},"server":{"enabled":true}}`)
	cs := connectStreamable(t, a.srv.URL, "")
	if names := listMCPNames(t, cs); !names["skill_upload_file"] || !names["skill_read_artifact"] {
		t.Fatalf("helpers in the catalog but not on the live list: %v", names)
	}

	// Revocation keeps the row, flips the state and empties the tools.
	ids, _ := installed["skill_ids"].([]any)
	skillID, _ := ids[0].(string)
	runtimeSkillResponse(t, a.h.do("POST", "/api/manage/skills/"+skillID+"/disable", ""), 200, nil)
	cat = a.getMCP(t).ServeCatalog
	for _, row := range cat.Runtime {
		if row.Version == "1.0.0" && (row.State != "revoked" || len(row.Tools) != 0) {
			t.Fatalf("revoked row = %#v", row)
		}
	}
}

// TestMCPRuntimeHookResync: install, disable and delete each fire
// OnRuntimeSkillChange, and the endpoint's surface follows without a restart.
func TestMCPRuntimeHookResync(t *testing.T) {
	a, _, _ := runtimeServeFixture(t, true)
	a.putMCP(t, `{"servers":{},"server":{"enabled":true,"policy":"auto","include_tools":["skill_test_*","skill_upload_file","skill_read_artifact"]}}`)
	cs := connectStreamable(t, a.srv.URL, "")
	if names := listMCPNames(t, cs); names["skill_test_replace_check"] || names["skill_upload_file"] {
		t.Fatalf("list before any install = %v", names)
	}

	installed := installRuntimeZip(t, a, webRuntimePackageZip(t, "org.test.mcp", "1.0.0"))
	if names := listMCPNames(t, cs); !names["skill_test_replace_check"] || !names["skill_upload_file"] {
		t.Fatalf("list after install = %v, want the contract tool and the helpers", names)
	}

	ids, _ := installed["skill_ids"].([]any)
	skillID, _ := ids[0].(string)
	runtimeSkillResponse(t, a.h.do("POST", "/api/manage/skills/"+skillID+"/disable", ""), 200, nil)
	names := listMCPNames(t, cs)
	if names["skill_test_replace_check"] {
		t.Fatal("the contract tool survived revocation")
	}
	// The catalog still holds the (revoked) version, so the helpers stay.
	if !names["skill_upload_file"] {
		t.Fatal("helpers dropped while the revoked version is still installed")
	}

	runtimeSkillResponse(t, a.h.do("DELETE", "/api/manage/skills/"+skillID+"?force=1", ""), 200, nil)
	if names := listMCPNames(t, cs); names["skill_test_replace_check"] || names["skill_upload_file"] || names["skill_read_artifact"] {
		t.Fatalf("delete left runtime tools behind: %v", names)
	}
}

// TestMCPRuntimeExecutorMissing: a host without the bwrap executor (non-Linux)
// registers no contract tool but still lists the package in serve_catalog —
// the picker can plan, the endpoint just cannot run.
func TestMCPRuntimeExecutorMissing(t *testing.T) {
	a, _, _ := runtimeServeFixture(t, false)
	installRuntimeZip(t, a, webRuntimePackageZip(t, "org.test.mcp", "1.0.0"))
	a.putMCP(t, `{"servers":{},"server":{"enabled":true,"policy":"auto","include_tools":["skill_*"]}}`)
	cs := connectStreamable(t, a.srv.URL, "")

	names := listMCPNames(t, cs)
	if names["skill_test_replace_check"] {
		t.Fatal("contract tool registered without an executor")
	}
	if !names["skill_upload_file"] {
		t.Fatalf("helpers missing: %v", names)
	}
	// The helper fails closed at call time: nothing is bound without an
	// executor.
	if res := callTool(t, cs, "skill_upload_file", map[string]any{"data_base64": "cGF5bG9hZA=="}); !res.IsError {
		t.Fatal("upload ran without an executor")
	}

	cat := a.getMCP(t).ServeCatalog
	if cat == nil || len(cat.Runtime) != 1 {
		t.Fatalf("runtime catalog = %#v", cat)
	}
	row := cat.Runtime[0]
	if row.State != "needs_configuration" || len(row.Tools) != 1 || row.Tools[0].Name != "skill_test_replace_check" {
		t.Fatalf("runtime row = %#v", row)
	}
}

// TestMCPRuntimeCallEmitsSkillEvent: a whitelisted direct call over the real
// wire records one execute/runtime event on the skill recorder — the external
// caller through the mcp entrypoint, the bundle's rs_ identity, no invented
// session/run. This is the /mcp channel of the Skill 调用监控 gap.
func TestMCPRuntimeCallEmitsSkillEvent(t *testing.T) {
	a, _, _ := runtimeServeFixture(t, true)
	installed := installRuntimeZip(t, a, webRuntimePackageZip(t, "org.test.mcp", "1.0.0"))
	rec := &skillObserveRecorder{}
	a.h.mgr.SetSkillRecorder(rec)
	a.putMCP(t, `{"servers":{},"server":{"enabled":true,"policy":"auto","include_tools":["skill_test_*"]}}`)
	cs := connectStreamable(t, a.srv.URL, "")

	res := callTool(t, cs, "skill_test_replace_check", map[string]any{"query": "x"})
	if res.IsError {
		t.Fatalf("direct call failed: %s", resultText(res))
	}

	ids, _ := installed["skill_ids"].([]any)
	skillID, _ := ids[0].(string)
	finals := rec.finals()
	if len(finals) != 1 {
		t.Fatalf("events = %+v, want exactly one", finals)
	}
	e := finals[0]
	if e.Stage != "execute" || e.Mechanism != "runtime" || e.Status != "ok" || e.ErrorKind != "" {
		t.Fatalf("event = %+v", e)
	}
	if e.Caller != "external" || e.Entrypoint != "mcp" {
		t.Fatalf("source = %s/%s, want external/mcp", e.Caller, e.Entrypoint)
	}
	// The event's skill_id is the identity hash of the bundle projection —
	// the rs_ id figures as the projection's path, never verbatim.
	want := skillobserve.Identify("replace-fixture", "bundle", "skill-runtime://"+skillID+"/SKILL.md")
	if e.SkillID != want.ID || e.SkillName != "replace-fixture" || e.SkillSource != "bundle" {
		t.Fatalf("identity = %q/%q/%q, want %s/replace-fixture/bundle", e.SkillID, e.SkillName, e.SkillSource, want.ID)
	}
	if e.Tool != "test.replace.check" || e.WireTool != "skill_test_replace_check" {
		t.Fatalf("tool = %q/%q, want the contract ID and the wire name", e.Tool, e.WireTool)
	}
	if e.TraceID == "" || e.FinishedAt < e.StartedAt || e.DurationMs < 0 {
		t.Fatalf("trace/times = %+v", e)
	}
	if e.SessionID != "" || e.RunID != "" {
		t.Fatalf("invented correlation: %+v", e)
	}
	// Two snapshots of one event: running, then the final classification.
	rec.mu.Lock()
	snapshots := len(rec.events)
	rec.mu.Unlock()
	if snapshots != 2 {
		t.Fatalf("snapshots = %d, want 2", snapshots)
	}
}
