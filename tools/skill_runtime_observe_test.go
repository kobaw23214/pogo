package tools

// RuntimeTool observation: one execute/runtime span per call, identity bound
// by the registration path (never the arguments), outcome classified like
// SynthTool's span. The fixture is a real BoundSession over the recording
// executor — the same shape as skillruntime's wire_protocol_test.go.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/internal/testfs"
	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/skillobserve"
	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skillruntime/core"
)

// runtimeObsExecutor answers the pogo.tool/1 describe and invoke frames
// without starting anything.
type runtimeObsExecutor struct{ contract []byte }

func (r *runtimeObsExecutor) ExecutionPlatform() string { return "linux/amd64" }
func (r *runtimeObsExecutor) Identity() string          { return "local-bwrap-v1:" + strings.Repeat("ab", 32) }
func (r *runtimeObsExecutor) Execute(ctx context.Context, e skillruntime.Execution) ([]byte, error) {
	out, err := r.ExecuteDetailed(ctx, e)
	return out.Output, err
}
func (r *runtimeObsExecutor) ExecuteDetailed(_ context.Context, e skillruntime.Execution) (skillruntime.DetailedResult, error) {
	frame := map[string]any{"protocolVersion": "pogo.tool/1", "callId": e.Request.CallID}
	switch e.Request.Type {
	case skillruntime.ProcessRequestDescribe:
		frame["type"] = skillruntime.ProcessRequestDescribe
		frame["operations"] = []any{map[string]any{"operation": "check", "contract": skillruntime.ContractIdentity{ID: "test.runtime.check", Version: "1.0.0", Digest: core.DigestBytes(r.contract)}}}
	case skillruntime.ProcessRequestInvoke:
		frame["type"] = "result"
		frame["output"] = map[string]any{"ok": true}
		frame["artifacts"] = []any{}
	default:
		frame["type"] = "error"
		frame["error"] = map[string]string{"code": "UNSUPPORTED_OPERATION", "message": "unexpected request"}
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return skillruntime.DetailedResult{}, err
	}
	return skillruntime.DetailedResult{Output: raw}, nil
}

func runtimeObsZip(t *testing.T, contract []byte) []byte {
	t.Helper()
	ref := core.ContractRef{ID: "test.runtime.check", Version: "1.0.0", Digest: core.DigestBytes(contract)}
	payload := runtimeObsInnerZip(t)
	artifact := core.Artifact{Path: "./payload/rt.zip", Digest: core.DigestBytes(payload), SizeBytes: int64(len(payload)), Format: "zip"}
	manifest := map[string]any{
		"apiVersion": core.APIVersion, "kind": "SkillRuntime", "id": "org.test.obs", "version": "1.0.0",
		"displayName":          "Observe fixture",
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
	return runtimeObsZipOf(t, []runtimeObsZipEnt{
		{name: core.ManifestFileName, data: raw},
		{name: "SKILL.md", data: []byte("---\nname: runtime-fixture\ndescription: Observe fixture.\n---\nUse the bound tool.\n")},
		{name: "contracts/check.json", data: contract},
		{name: "payload/rt.zip", data: payload},
	})
}

type runtimeObsZipEnt struct {
	name string
	data []byte
	mode int64
}

func runtimeObsZipOf(t *testing.T, entries []runtimeObsZipEnt) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(fs.FileMode(e.mode))
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

func runtimeObsInnerZip(t *testing.T) []byte {
	t.Helper()
	return runtimeObsZipOf(t, []runtimeObsZipEnt{{name: "bin/tool", data: []byte("#!/bin/true\n"), mode: 0o755}})
}

// newObservedRuntimeTool binds a RuntimeTool end to end: package installed in
// a private store, session opened through the recording executor, identity
// resolved from the descriptor's bundle attribution the way the web layer
// resolves it.
func newObservedRuntimeTool(t *testing.T) *RuntimeTool {
	t.Helper()
	contract := []byte(`{
  "apiVersion": "pogo.skill/v1",
  "kind": "CapabilityContract",
  "id": "test.runtime.check",
  "version": "1.0.0",
  "description": "synthetic observe contract",
  "inputSchema": {"type": "object", "properties": {"query": {"type": "string", "minLength": 1}}, "required": ["query"], "additionalProperties": false},
  "outputSchema": {"type": "object", "properties": {"ok": {"type": "boolean"}}, "required": ["ok"], "additionalProperties": false},
  "fileInputs": [],
  "effects": {"readsUserFiles": false, "writesArtifacts": false, "mutatesExternalState": false},
  "retry": "never-automatic"
}`)
	service, err := skillruntime.NewManagementService(testfs.SealedTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := service.Import(bytes.NewReader(runtimeObsZip(t, contract)))
	if err != nil {
		t.Fatal(err)
	}
	service.Executor = &runtimeObsExecutor{contract: contract}
	rsID := skillruntime.PackageSkillID(installed.Ref)
	plan, err := service.PlanSelection(skillruntime.Selection{SkillIDs: []string{rsID}})
	if err != nil || !plan.Ready {
		t.Fatalf("fixture selection not ready: %+v %v", plan, err)
	}
	lock, err := service.MakeLock(plan.Binding, "sess-tools")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := service.Session(plan.Binding, lock, "sess-tools")
	if err != nil {
		t.Fatal(err)
	}
	descriptors := bound.Descriptors()
	if len(descriptors) != 1 {
		t.Fatalf("descriptors = %+v", descriptors)
	}
	d := descriptors[0]
	identity := skillobserve.Identify(d.SkillName, "bundle", "skill-runtime://"+d.SkillID+"/SKILL.md")
	if identity.ID == "" {
		t.Fatalf("descriptor carried no bundle attribution: %+v", d)
	}
	return &RuntimeTool{Session: bound, Descriptor: d, Identity: identity}
}

func TestSkillObserveRuntimeExecuteOK(t *testing.T) {
	tool := newObservedRuntimeTool(t)
	rec := &skillRec{}
	md := mcpobserve.Metadata{TraceID: mcpobserve.NewID(), SessionID: "sess", RunID: "run", OwnerID: "own", ToolCallID: "tc1", Entrypoint: "pogo"}
	res, err := tool.Execute(skillObsCtx(rec, "internal", "", md), json.RawMessage(`{"query":"x"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Text, `"ok":true`) {
		t.Fatalf("output = %q", res.Text)
	}
	finals := rec.final()
	if len(finals) != 1 {
		t.Fatalf("events = %d, want 1: %+v", len(finals), finals)
	}
	e := finals[0]
	if e.Stage != "execute" || e.Mechanism != "runtime" || e.Status != "ok" || e.ErrorKind != "" {
		t.Fatalf("event = %+v", e)
	}
	if e.SkillID != tool.Identity.ID || e.SkillName != "runtime-fixture" || e.SkillSource != "bundle" {
		t.Fatalf("identity = %+v, want %q / runtime-fixture / bundle", e, tool.Identity.ID)
	}
	if e.Tool != "test.runtime.check" || e.WireTool != tool.Descriptor.Name {
		t.Fatalf("tool = %q/%q, want the contract ID and the descriptor name", e.Tool, e.WireTool)
	}
	if e.Caller != "internal" || e.Entrypoint != "pogo" {
		t.Fatalf("source = %s/%s", e.Caller, e.Entrypoint)
	}
	if e.SessionID != "sess" || e.RunID != "run" || e.OwnerID != "own" || e.ToolCallID != "tc1" {
		t.Fatalf("correlation = %+v", e)
	}
	if e.TraceID != md.TraceID {
		t.Fatalf("trace = %q, want %q", e.TraceID, md.TraceID)
	}
	if e.FinishedAt < e.StartedAt || e.DurationMs < 0 {
		t.Fatalf("times = %+v", e)
	}
	if got := len(rec.snapshot()); got != 2 {
		t.Fatalf("snapshots = %d, want running + final", got)
	}
	// The arguments never enter the event.
	js, _ := json.Marshal(e)
	if strings.Contains(string(js), `"query"`) {
		t.Fatalf("argument leak: %s", js)
	}
}

func TestSkillObserveRuntimeExecuteInvalidInput(t *testing.T) {
	tool := newObservedRuntimeTool(t)
	rec := &skillRec{}
	_, err := tool.Execute(skillObsCtx(rec, "internal", "pogo", mcpobserve.Metadata{TraceID: "t"}), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("want error")
	}
	finals := rec.final()
	if len(finals) != 1 {
		t.Fatalf("events = %+v", finals)
	}
	if e := finals[0]; e.Status != "error" || e.ErrorKind != "execution_error" || e.Mechanism != "runtime" {
		t.Fatalf("event = %+v", e)
	}
}

func TestSkillObserveRuntimeExternalSource(t *testing.T) {
	// The /mcp direct-invocation shape: external caller, mcp entrypoint, trace
	// inherited from the inbound span, no session/run invented.
	tool := newObservedRuntimeTool(t)
	rec := &skillRec{}
	md := mcpobserve.Metadata{TraceID: mcpobserve.NewID(), ParentID: mcpobserve.NewID(), Entrypoint: "mcp"}
	if _, err := tool.Execute(skillObsCtx(rec, "external", "mcp", md), json.RawMessage(`{"query":"x"}`)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	e := rec.final()[0]
	if e.Caller != "external" || e.Entrypoint != "mcp" {
		t.Fatalf("source = %s/%s", e.Caller, e.Entrypoint)
	}
	if e.TraceID != md.TraceID || e.ParentID != md.ParentID {
		t.Fatalf("trace = %q parent = %q", e.TraceID, e.ParentID)
	}
	if e.SessionID != "" || e.RunID != "" {
		t.Fatalf("invented correlation: %+v", e)
	}
}

func TestSkillObserveRuntimeNoIdentitySilent(t *testing.T) {
	// A RuntimeTool constructed without identity (the pre-instrumentation
	// shape) behaves exactly as before and records nothing.
	tool := newObservedRuntimeTool(t)
	tool.Identity = skillobserve.Identity{}
	rec := &skillRec{}
	res, err := tool.Execute(skillObsCtx(rec, "internal", "pogo", mcpobserve.Metadata{TraceID: "t"}), json.RawMessage(`{"query":"x"}`))
	if err != nil || !strings.Contains(res.Text, `"ok":true`) {
		t.Fatalf("res = %v err = %v", res.Text, err)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("events = %d, want 0", got)
	}
}
