package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skillruntime/core"
)

// ---- shared fakes ----

// fakeFileInfo is a minimal fs.FileInfo for the statFile seam.
type fakeFileInfo struct {
	name    string
	dir     bool
	regular bool
	size    int64
}

func (f fakeFileInfo) Name() string { return f.name }
func (f fakeFileInfo) Size() int64  { return f.size }
func (f fakeFileInfo) Mode() fs.FileMode {
	if f.dir {
		return fs.ModeDir | 0o755
	}
	if !f.regular {
		return fs.ModeSymlink
	}
	return 0o444
}
func (f fakeFileInfo) ModTime() time.Time { return time.Unix(0, 0) }
func (f fakeFileInfo) IsDir() bool        { return f.dir }
func (f fakeFileInfo) Sys() any           { return nil }

// swapSeams installs test doubles and restores the real ones afterwards.
func swapSeams(t *testing.T, read func(string) ([]byte, error), stat func(string) (fs.FileInfo, error), run func(context.Context, string, []string, []string, int64) cliOutcome) {
	t.Helper()
	origRead, origStat, origRun := readFile, statFile, runCLI
	origExe, origTemp := executablePath, tempRoot
	if read != nil {
		readFile = read
	}
	if stat != nil {
		statFile = stat
	}
	if run != nil {
		runCLI = run
	}
	executablePath = func() (string, error) { return "/runtime/bin/pogo-malagent-provider", nil }
	tempRoot = t.TempDir()
	t.Cleanup(func() {
		readFile, statFile, runCLI = origRead, origStat, origRun
		executablePath, tempRoot = origExe, origTemp
	})
}

func invokeReq(opName, input string, ctx *skillruntime.ProcessContext) *skillruntime.ProcessRequest {
	op := operations[opName]
	identity := op.contract
	return &skillruntime.ProcessRequest{
		ProtocolVersion: "pogo.tool/1",
		CallID:          "call:test",
		Type:            "invoke",
		Operation:       opName,
		Contract:        &identity,
		Input:           json.RawMessage(input),
		Context:         ctx,
	}
}

// fileCtx stages one file input with a correct digest over content.
func fileCtx(t *testing.T, fileID, content string) *skillruntime.ProcessContext {
	t.Helper()
	return &skillruntime.ProcessContext{
		Files: []skillruntime.ProcessFile{{
			InputPointer: "/fileId",
			FileID:       fileID,
			Path:         "/input/0.bin",
			Digest:       bytesDigest([]byte(content)),
		}},

		Assets: []skillruntime.ProcessAsset{},
	}
}

func assetCtx() *skillruntime.ProcessContext {
	return &skillruntime.ProcessContext{Files: []skillruntime.ProcessFile{}, Assets: []skillruntime.ProcessAsset{{AssetID: "sample-index", Path: "/assets/sample-index", Digest: "sha256:" + strings.Repeat("a", 64)}}}
}

func serveInvoke(t *testing.T, req *skillruntime.ProcessRequest) map[string]any {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := serve(bytes.NewReader(raw), &out); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("response is not one JSON frame: %v\n%s", err, out.String())
	}
	// Every response frame must pass the same document validation the host
	// applies before accepting a call result.
	if err := core.ValidateDocument(core.KindProcessResponse, out.Bytes()); err != nil {
		t.Fatalf("response fails host process-response validation: %v\n%s", err, out.String())
	}
	return got
}

// ---- protocol frame ----

func TestDescribeListsAllOperationsWithExactDigests(t *testing.T) {
	var out bytes.Buffer
	if err := serve(strings.NewReader(`{"protocolVersion":"pogo.tool/1","callId":"call:d","type":"describe"}`), &out); err != nil {
		t.Fatal(err)
	}
	if err := core.ValidateDocument(core.KindProcessResponse, out.Bytes()); err != nil {
		t.Fatalf("describe fails host process-response validation: %v", err)
	}
	var got struct {
		Type       string `json:"type"`
		Operations []struct {
			Operation string `json:"operation"`
			Contract  struct {
				ID      string `json:"id"`
				Version string `json:"version"`
				Digest  string `json:"digest"`
			} `json:"contract"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "describe" || len(got.Operations) != 9 {
		t.Fatalf("want 9 operations, got %d", len(got.Operations))
	}
	wantOps := []string{
		"family.get", "file.capabilities", "file.decompile", "file.hash",
		"library.query", "library.stats", "sample.get", "sample.search", "sample.similar",
	}
	for i, w := range wantOps {
		if got.Operations[i].Operation != w {
			t.Fatalf("operation[%d] = %q, want %q", i, got.Operations[i].Operation, w)
		}
	}
	for _, o := range got.Operations {
		if o.Contract.Version == "" || !strings.HasPrefix(o.Contract.Digest, "sha256:") {
			t.Fatalf("bad contract identity: %+v", o.Contract)
		}
		if operations[o.Operation].contract.Digest != o.Contract.Digest ||
			operations[o.Operation].contract.Version != o.Contract.Version {
			t.Fatalf("describe identity drift for %s", o.Operation)
		}
		if !strings.HasPrefix(o.Contract.ID, "malware.") || strings.TrimPrefix(o.Contract.ID, "malware.") != o.Operation {
			t.Fatalf("operation/contract namespace mismatch: %+v", o)
		}
	}
}

func TestStrictFrameRejection(t *testing.T) {
	cases := map[string]string{
		"duplicate keys":   `{"protocolVersion":"pogo.tool/1","callId":"call:x","callId":"call:y","type":"describe"}`,
		"trailing garbage": `{"protocolVersion":"pogo.tool/1","callId":"call:x","type":"describe"} {}`,
		"unknown field":    `{"protocolVersion":"pogo.tool/1","callId":"call:x","type":"describe","extra":1}`,
		"non-finite":       `{"protocolVersion":"pogo.tool/1","callId":"call:x","type":"describe","n":1e999}`,
	}
	for name, frame := range cases {
		var out bytes.Buffer
		if err := serve(strings.NewReader(frame), &out); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	// Depth limit: 40 nested arrays inside the input object exceed 32 levels.
	identity := operations["file.hash"].contract
	deep := `{"protocolVersion":"pogo.tool/1","callId":"call:x","type":"invoke","operation":"file.hash","contract":{"id":"` + identity.ID + `","version":"` + identity.Version + `","digest":"` + identity.Digest + `"},"input":{"fileId":"file:t","n":` + strings.Repeat("[", 40) + strings.Repeat("]", 40) + `},"context":{"files":[],"assets":[],"outputDirectory":null}}`
	var out bytes.Buffer
	if err := serve(strings.NewReader(deep), &out); err == nil {
		t.Fatal("deep document accepted")
	}
}

func TestInvokeRejectsUnknownOperationAndContractMismatch(t *testing.T) {
	req := invokeReq("file.hash", `{"fileId":"file:t"}`, fileCtx(t, "file:t", "abc"))
	req.Operation = "run.shell"
	got := serveInvoke(t, req)
	if got["type"] != "error" || got["error"].(map[string]any)["code"] != "UNSUPPORTED_OPERATION" {
		t.Fatalf("%v", got)
	}
	req = invokeReq("file.hash", `{"fileId":"file:t"}`, fileCtx(t, "file:t", "abc"))
	bad := req.Contract
	bad.Digest = "sha256:" + strings.Repeat("0", 64)
	got = serveInvoke(t, req)
	if got["type"] != "error" || got["error"].(map[string]any)["code"] != "UNSUPPORTED_OPERATION" {
		t.Fatalf("%v", got)
	}
	bad = req.Contract
	bad.Version = "9.9.9"
	got = serveInvoke(t, req)
	if got["type"] != "error" || got["error"].(map[string]any)["code"] != "UNSUPPORTED_OPERATION" {
		t.Fatalf("%v", got)
	}
}

// ---- malware.file.hash via bundled library sample ----

func TestHashSampleFromBundledIndex(t *testing.T) {
	// sha256("abc") staged as a bundled library binary.
	sha := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	swapSeams(t, func(p string) ([]byte, error) {
		if p != "/assets/sample-index/binaries/"+sha {
			return nil, errors.New("unexpected path " + p)
		}
		return []byte("abc"), nil
	}, nil, nil)
	got := serveInvoke(t, invokeReq("file.hash", `{"sampleSha256":"`+sha+`"}`, assetCtx()))
	if got["type"] != "result" {
		t.Fatalf("%v", got)
	}
	out := got["output"].(map[string]any)
	if out["sha256"] != sha {
		t.Fatalf("sha256 = %v", out["sha256"])
	}
}

func TestHashSampleInputValidation(t *testing.T) {
	sha := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	swapSeams(t, func(string) ([]byte, error) { return []byte("abc"), nil }, nil, nil)

	// Exactly one of fileId / sampleSha256.
	got := serveInvoke(t, invokeReq("file.hash", `{}`, fileCtx(t, "file:t", "abc")))
	if got["error"].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("neither: %v", got)
	}
	got = serveInvoke(t, invokeReq("file.hash", `{"fileId":"file:t","sampleSha256":"`+sha+`"}`, fileCtx(t, "file:t", "abc")))
	if got["error"].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("both: %v", got)
	}
	// Malformed hash.
	got = serveInvoke(t, invokeReq("file.hash", `{"sampleSha256":"ABC"}`, assetCtx()))
	if got["error"].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("malformed: %v", got)
	}
	// Missing the bundled asset mapping.
	got = serveInvoke(t, invokeReq("file.hash", `{"sampleSha256":"`+sha+`"}`, fileCtx(t, "file:t", "abc")))
	if got["error"].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("no asset mapping: %v", got)
	}
	// Content that does not hash back to its name.
	swapSeams(t, func(string) ([]byte, error) { return []byte("different"), nil }, nil, nil)
	got = serveInvoke(t, invokeReq("file.hash", `{"sampleSha256":"`+sha+`"}`, assetCtx()))
	if got["error"].(map[string]any)["code"] != "TOOL_FAILED" {
		t.Fatalf("content mismatch: %v", got)
	}
	// A sample the package does not carry.
	swapSeams(t, func(string) ([]byte, error) { return nil, errors.New("not found") }, nil, nil)
	got = serveInvoke(t, invokeReq("file.hash", `{"sampleSha256":"`+sha+`"}`, assetCtx()))
	if got["error"].(map[string]any)["code"] != "RESOURCE_UNAVAILABLE" {
		t.Fatalf("missing binary: %v", got)
	}
}


func TestHashSuccess(t *testing.T) {
	swapSeams(t, func(p string) ([]byte, error) {
		if p != "/input/0.bin" {
			return nil, errors.New("unexpected path " + p)
		}
		return []byte("abc"), nil
	}, nil, nil)
	got := serveInvoke(t, invokeReq("file.hash", `{"fileId":"file:t"}`, fileCtx(t, "file:t", "abc")))
	if got["type"] != "result" {
		t.Fatalf("%v", got)
	}
	out := got["output"].(map[string]any)
	// sha256("abc") is a well-known constant.
	if out["sha256"] != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("sha256 = %v", out["sha256"])
	}
	if out["md5"] != "900150983cd24fb0d6963f7d28e17f72" || out["sha1"] != "a9993e364706816aba3e25717850c26c9cd0d89d" {
		t.Fatalf("%v", out)
	}
	if out["sizeBytes"].(float64) != 3 {
		t.Fatalf("sizeBytes = %v", out["sizeBytes"])
	}
}

func TestHashRejectsDigestMappingAndPathMismatch(t *testing.T) {
	swapSeams(t, func(string) ([]byte, error) { return []byte("abc"), nil }, nil, nil)
	// Digest mismatch.
	ctx := fileCtx(t, "file:t", "different")
	got := serveInvoke(t, invokeReq("file.hash", `{"fileId":"file:t"}`, ctx))
	if got["error"].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("%v", got)
	}
	// fileId mapping mismatch.
	ctx = fileCtx(t, "file:other", "abc")
	got = serveInvoke(t, invokeReq("file.hash", `{"fileId":"file:t"}`, ctx))
	if got["error"].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("%v", got)
	}
	// Path outside the executor staging root.
	ctx = fileCtx(t, "file:t", "abc")
	ctx.Files[0].Path = "/etc/passwd"
	got = serveInvoke(t, invokeReq("file.hash", `{"fileId":"file:t"}`, ctx))
	if got["error"].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("%v", got)
	}
	// Traversal.
	ctx = fileCtx(t, "file:t", "abc")
	ctx.Files[0].Path = "/input/../input/0.bin"
	got = serveInvoke(t, invokeReq("file.hash", `{"fileId":"file:t"}`, ctx))
	if got["error"].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("%v", got)
	}
	// Unknown input fields rejected.
	got = serveInvoke(t, invokeReq("file.hash", `{"fileId":"file:t","path":"/x"}`, fileCtx(t, "file:t", "abc")))
	if got["error"].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("%v", got)
	}
}
