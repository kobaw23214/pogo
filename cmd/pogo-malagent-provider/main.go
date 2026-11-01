// pogo-malagent-provider adapts the pinned MalAgent analysis toolchain
// (CAPA, Ghidra+JDK, malquery) to the pogo.tool/1 process ABI. One binary
// serves every operation of the malware-analysis pkg; each operation is
// bound to exactly one embedded capability contract whose digest is computed
// over the contract file bytes. The provider never executes samples, never
// takes programs, paths, CLI flags or environment from the request, and never
// writes outside the host-granted output directory.
package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skillruntime/core"
)

// contracts holds the embedded capability contracts; digests are computed over
// the exact file bytes, so describe stays consistent with the packaged files
// as long as they are shipped byte-identically (LF endings pinned by
// .gitattributes).
//
//go:embed contracts/*.json
var contracts embed.FS

// artifact is one declared output file relative to context.outputDirectory.
type artifact struct {
	Name      string
	Path      string
	MediaType string
}

// failErr is a business failure mapped to the closed process-response error
// code enum; it always produces an error frame, never a partial result.
type failErr struct {
	code string
	msg  string
}

func fail(code, format string, args ...any) *failErr {
	return &failErr{code: code, msg: fmt.Sprintf(format, args...)}
}

// handler returns the contract-conformant output object and declared
// artifacts, or a failErr. Handlers must not write to stdout.
type handler func(req *skillruntime.ProcessRequest, input json.RawMessage) (any, []artifact, *failErr)

type operation struct {
	name     string
	contract skillruntime.ContractIdentity
	handle   handler
}

// handlers binds contract IDs to implementations; buildOperations verifies the
// embedded contracts against this table at startup so a contract/handler drift
// fails fast instead of silently mis-describing the interface.
var handlers = map[string]handler{
	"malware.file.hash":         handleHash,
	"malware.file.capabilities": handleCapabilities,
	"malware.file.decompile":    handleDecompile,
	"malware.sample.search":     handleSampleSearch,
	"malware.sample.get":        handleSampleGet,
	"malware.sample.similar":    handleSampleSimilar,
	"malware.family.get":        handleFamilyGet,
	"malware.library.stats":     handleLibraryStats,
	"malware.library.query":     handleLibraryQuery,
}

var operations = buildOperations()

func buildOperations() map[string]operation {
	entries, err := fs.ReadDir(contracts, "contracts")
	if err != nil {
		panic(err)
	}
	ops := map[string]operation{}
	for _, e := range entries {
		raw, err := contracts.ReadFile("contracts/" + e.Name())
		if err != nil {
			panic(err)
		}
		var head struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		}
		// Tolerant header read: the host validator owns full contract
		// conformance; here we only need identity fields.
		if err := json.Unmarshal(raw, &head); err != nil {
			panic(fmt.Sprintf("contract %s: %v", e.Name(), err))
		}
		handle, ok := handlers[head.ID]
		if !ok {
			panic("no handler bound to contract " + head.ID)
		}
		name, ok := strings.CutPrefix(head.ID, "malware.")
		if !ok || name == "" {
			panic("contract id outside malware.* namespace: " + head.ID)
		}
		sum := sha256.Sum256(raw)
		ops[name] = operation{
			name: name,
			contract: skillruntime.ContractIdentity{
				ID:      head.ID,
				Version: head.Version,
				Digest:  "sha256:" + hex.EncodeToString(sum[:]),
			},
			handle: handle,
		}
	}
	if len(ops) != len(handlers) {
		panic("handler without embedded contract")
	}
	return ops
}

func main() {
	if err := serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func serve(input io.Reader, output io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(input, maxFrameBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxFrameBytes {
		return errors.New("request exceeds 1 MiB")
	}
	// Frame-level strict pass: duplicate keys, depth and trailing garbage are
	// rejected before any typed decoding, same as the host.
	if _, err := decodeStrict(raw); err != nil {
		return fmt.Errorf("invalid request frame: %v", err)
	}
	var req skillruntime.ProcessRequest
	if err := decodeTypedStrict(raw, &req); err != nil {
		return err
	}
	if req.ProtocolVersion != "pogo.tool/1" || req.CallID == "" || len(req.CallID) > 192 {
		return errors.New("invalid protocol request")
	}
	base := map[string]any{"protocolVersion": "pogo.tool/1", "callId": req.CallID}
	if req.Type == "describe" {
		names := make([]string, 0, len(operations))
		for name := range operations {
			names = append(names, name)
		}
		sort.Strings(names)
		ops := make([]any, 0, len(names))
		for _, name := range names {
			ops = append(ops, map[string]any{"operation": name, "contract": operations[name].contract})
		}
		base["type"] = "describe"
		base["operations"] = ops
		return json.NewEncoder(output).Encode(base)
	}
	failFrame := func(f *failErr) error {
		base["type"] = "error"
		base["error"] = map[string]string{"code": f.code, "message": f.msg}
		return json.NewEncoder(output).Encode(base)
	}
	op, ok := operations[req.Operation]
	if req.Type != "invoke" || !ok || req.Contract == nil || *req.Contract != op.contract {
		return failFrame(fail("UNSUPPORTED_OPERATION", "provider operation or contract does not match"))
	}
	if req.Context == nil {
		return failFrame(fail("INVALID_INPUT", "verified execution context is required"))
	}
	if len(req.Input) == 0 {
		return failFrame(fail("INVALID_INPUT", "input object is required"))
	}
	out, artifacts, ferr := op.handle(&req, req.Input)
	if ferr != nil {
		return failFrame(ferr)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return failFrame(fail("TOOL_FAILED", "internal output encoding failure"))
	}
	if len(encoded) == 0 || encoded[0] != '{' {
		return failFrame(fail("TOOL_FAILED", "internal output is not a JSON object"))
	}
	base["type"] = "result"
	base["output"] = json.RawMessage(encoded)
	declared := make([]any, 0, len(artifacts))
	for _, a := range artifacts {
		declared = append(declared, map[string]any{"name": a.Name, "path": a.Path, "mediaType": a.MediaType})
	}
	base["artifacts"] = declared
	return json.NewEncoder(output).Encode(base)
}

// ---- shared context verification ----

var opaqueRefPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{0,191}$`)
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Test seams: the real provider always reads paths the host staged inside the
// container; unit tests substitute these to stay hermetic on any OS.
var (
	readFile       = os.ReadFile
	statFile       = os.Lstat
	mkdirTemp      = os.MkdirTemp
	removeAll      = os.RemoveAll
	executablePath = os.Executable
	tempRoot       = "/tmp"
)

const maxInputFileBytes = 64 << 20

func bytesDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// stagedFile resolves and verifies the single declared file input: the context
// mapping must match the /fileId pointer and the requested fileId exactly, the
// path must be the canonical executor staging location, and the content digest
// must match the host-attested digest.
func stagedFile(req *skillruntime.ProcessRequest, fileID string) (skillruntime.ProcessFile, []byte, *failErr) {
	if fileID == "" || !opaqueRefPattern.MatchString(fileID) {
		return skillruntime.ProcessFile{}, nil, fail("INVALID_INPUT", "fileId is missing or malformed")
	}
	if len(req.Context.Files) != 1 {
		return skillruntime.ProcessFile{}, nil, fail("INVALID_INPUT", "expected exactly the declared file input")
	}
	f := req.Context.Files[0]
	if f.InputPointer != "/fileId" || f.FileID != fileID {
		return skillruntime.ProcessFile{}, nil, fail("INVALID_INPUT", "input mapping does not match fileId")
	}
	// Pure string check (the provider runs on Linux): must be the canonical
	// staged path, no traversal, no redundant separators.
	if !strings.HasPrefix(f.Path, "/input/") || path.Clean(f.Path) != f.Path || strings.Contains(f.Path, "..") {
		return skillruntime.ProcessFile{}, nil, fail("INVALID_INPUT", "file must be staged by the executor")
	}
	data, err := readFile(f.Path)
	if err != nil {
		return skillruntime.ProcessFile{}, nil, fail("RESOURCE_UNAVAILABLE", "input file is unavailable")
	}
	if len(data) > maxInputFileBytes {
		return skillruntime.ProcessFile{}, nil, fail("INVALID_INPUT", "input file exceeds provider limit")
	}
	if bytesDigest(data) != f.Digest {
		return skillruntime.ProcessFile{}, nil, fail("INVALID_INPUT", "input content digest mismatch")
	}
	return f, data, nil
}

// sampleBinariesDir holds the library samples bundled inside the digest-pinned
// sample-index asset, named by content hash: binaries/<sha256>.
const sampleBinariesDir = "/assets/sample-index/binaries"

// sampleBinary resolves a library sample from the bundled sample-index asset:
// the asset mapping must be host-fixed, the file name is the requested hash,
// and the content must hash back to it — a package whose binaries were
// tampered with after installation fails here even though the host pins the
// asset digest.
func sampleBinary(req *skillruntime.ProcessRequest, sampleSHA string) ([]byte, *failErr) {
	if !sha256Pattern.MatchString(sampleSHA) {
		return nil, fail("INVALID_INPUT", "sampleSha256 must be a lowercase sha256 hex string")
	}
	mapped := false
	for _, a := range req.Context.Assets {
		if a.AssetID == sampleIndexSlot && a.Path == "/assets/"+sampleIndexSlot && core.IsDigest(a.Digest) {
			mapped = true
		}
	}
	if !mapped {
		return nil, fail("INVALID_INPUT", "the bundled sample-index asset mapping is required for sampleSha256")
	}
	data, err := readFile(sampleBinariesDir + "/" + sampleSHA)
	if err != nil {
		return nil, fail("RESOURCE_UNAVAILABLE", "sample %s… has no bundled binary; pick one with has_binary=true or ask the user to upload the file", clipString(sampleSHA, 16))
	}
	if len(data) == 0 || len(data) > maxInputFileBytes {
		return nil, fail("TOOL_FAILED", "bundled sample binary has an invalid size")
	}
	if bytesDigest(data) != "sha256:"+sampleSHA {
		return nil, fail("TOOL_FAILED", "bundled sample binary content does not match its name hash")
	}
	return data, nil
}

// inputFile funnels the two file sources every file-analysis operation takes:
// an uploaded session file staged by the host (fileId), or a library sample
// from the bundled index asset (sampleSha256). Exactly one must be set.
// Handlers get a real filesystem path either way: library bytes are staged on
// the scratch tmpfs because the bundled tools import by path.
func inputFile(req *skillruntime.ProcessRequest, fileID, sampleSHA string) (string, []byte, *failErr) {
	if (fileID == "") == (sampleSHA == "") {
		return "", nil, fail("INVALID_INPUT", "exactly one of fileId and sampleSha256 is required")
	}
	if fileID != "" {
		f, data, ferr := stagedFile(req, fileID)
		if ferr != nil {
			return "", nil, ferr
		}
		return f.Path, data, nil
	}
	data, ferr := sampleBinary(req, sampleSHA)
	if ferr != nil {
		return "", nil, ferr
	}
	dir, err := mkdirTemp(tempRoot, "sample-")
	if err != nil {
		return "", nil, fail("RESOURCE_UNAVAILABLE", "scratch space is unavailable")
	}
	p := filepath.Join(dir, "sample.bin")
	if err := os.WriteFile(p, data, 0600); err != nil {
		removeAll(dir)
		return "", nil, fail("RESOURCE_UNAVAILABLE", "could not stage the sample into scratch space")
	}
	return p, data, nil
}

// runtimeRoot resolves the tool runtime root from the provider executable
// location (<root>/bin/pogo-malagent-provider -> <root>); the tree below it is
// the host-verified read-only mount with fixed bin/, jdk/, ghidra/ and
// ghidra-scripts/ directories.
func runtimeRoot() (string, error) {
	exe, err := executablePath()
	if err != nil {
		return "", err
	}
	return filepath.Dir(filepath.Dir(exe)), nil
}

// toolEnv is the fixed, minimal environment for every subprocess. The request
// and the host environment are never inherited.
func toolEnv(extra ...string) []string {
	env := []string{"HOME=/tmp", "TMPDIR=/tmp", "LANG=C.UTF-8", "PATH=/runtime/bin:/usr/bin:/bin", "LD_LIBRARY_PATH=/runtime/lib"}
	return append(env, extra...)
}

func clipString(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
