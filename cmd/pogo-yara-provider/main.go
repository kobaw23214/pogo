// pogo-yara-provider is a separately packaged tool implementation, not part of
// Pogo's base image. It adapts a pinned YARA executable to pogo.tool/1.
package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yosukeno/pogo/skillruntime"
)

//go:embed contract.json
var contractBytes []byte

func contractIdentity() skillruntime.ContractIdentity {
	var head struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(contractBytes, &head); err != nil || head.ID == "" || head.Version == "" {
		panic("embedded contract identity is unreadable")
	}
	hash := sha256.Sum256(contractBytes)
	return skillruntime.ContractIdentity{ID: head.ID, Version: head.Version, Digest: "sha256:" + hex.EncodeToString(hash[:])}
}

func main() {
	if err := serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func serve(input io.Reader, output io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return errors.New("request exceeds 1 MiB")
	}
	var req skillruntime.ProcessRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return err
	}
	if dec.Decode(new(any)) != io.EOF {
		return errors.New("expected one JSON request")
	}
	if req.ProtocolVersion != "pogo.tool/1" || req.CallID == "" {
		return errors.New("invalid protocol request")
	}
	base := map[string]any{"protocolVersion": "pogo.tool/1", "callId": req.CallID}
	if req.Type == "describe" {
		base["type"] = "describe"
		base["operations"] = []any{map[string]any{"operation": "file.yara.scan", "contract": contractIdentity()}}
		return json.NewEncoder(output).Encode(base)
	}
	fail := func(code, message string) error {
		base["type"] = "error"
		base["error"] = map[string]string{"code": code, "message": message}
		return json.NewEncoder(output).Encode(base)
	}
	identity := contractIdentity()
	if req.Type != "invoke" || req.Operation != "file.yara.scan" || req.Contract == nil || *req.Contract != identity {
		return fail("UNSUPPORTED_OPERATION", "provider operation or contract does not match")
	}
	var args struct {
		FileID       string `json:"fileId"`
		SampleSHA256 string `json:"sampleSha256"`
	}
	if err := json.Unmarshal(req.Input, &args); err != nil || req.Context == nil {
		return fail("INVALID_INPUT", "fileId or sampleSha256 and the verified context are required")
	}
	if (args.FileID == "") == (args.SampleSHA256 == "") {
		return fail("INVALID_INPUT", "exactly one of fileId and sampleSha256 is required")
	}
	scanPath := ""
	if args.FileID != "" {
		if len(req.Context.Files) != 1 || req.Context.Files[0].InputPointer != "/fileId" || req.Context.Files[0].FileID != args.FileID {
			return fail("INVALID_INPUT", "input mapping does not match fileId")
		}
		file := req.Context.Files[0]
		if !strings.HasPrefix(file.Path, "/input/") || filepath.Clean(file.Path) != file.Path {
			return fail("INVALID_INPUT", "file must be staged by the executor")
		}
		data, err := os.ReadFile(file.Path)
		if err != nil {
			return fail("RESOURCE_UNAVAILABLE", "input file is unavailable")
		}
		hash := sha256.Sum256(data)
		if "sha256:"+hex.EncodeToString(hash[:]) != file.Digest {
			return fail("INVALID_INPUT", "input content digest mismatch")
		}
		scanPath = file.Path
	} else {
		// A library sample: bytes come from the digest-pinned sample-index
		// asset, named by content hash and verified against it.
		hasIndex := false
		for _, asset := range req.Context.Assets {
			if asset.AssetID == "sample-index" && asset.Path == "/assets/sample-index" {
				hasIndex = true
			}
		}
		if !hasIndex {
			return fail("INVALID_INPUT", "the bundled sample-index asset mapping is required for sampleSha256")
		}
		sha := args.SampleSHA256
		if len(sha) != 64 || strings.Trim(sha, "0123456789abcdef") != "" {
			return fail("INVALID_INPUT", "sampleSha256 must be a lowercase sha256 hex string")
		}
		data, err := os.ReadFile("/assets/sample-index/binaries/" + sha)
		if err != nil {
			return fail("RESOURCE_UNAVAILABLE", "sample has no bundled binary; pick one with has_binary=true or ask the user to upload the file")
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != sha {
			return fail("TOOL_FAILED", "bundled sample binary content does not match its name hash")
		}
		staged := filepath.Join(os.TempDir(), "yara-sample-"+sha[:16])
		if err := os.WriteFile(staged, data, 0600); err != nil {
			return fail("RESOURCE_UNAVAILABLE", "could not stage the sample into scratch space")
		}
		defer os.Remove(staged)
		scanPath = staged
	}
	rules := ""
	for _, asset := range req.Context.Assets {
		if asset.AssetID == "yara-rules" && asset.Path == "/assets/yara-rules" {
			rules = filepath.Join(asset.Path, "rules.yar")
		}
	}
	if rules == "" {
		return fail("RESOURCE_UNAVAILABLE", "locked YARA rules are required")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	runtimeRoot := filepath.Dir(filepath.Dir(executable))
	cmd := exec.Command(filepath.Join(runtimeRoot, "bin", "yara"), "--no-warnings", "--print-namespace", rules, scanPath)
	cmd.Env = []string{"LD_LIBRARY_PATH=" + filepath.Join(runtimeRoot, "lib"), "LANG=C", "HOME=/tmp", "TMPDIR=/tmp"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	result, err := cmd.Output()
	if err != nil {
		return fail("TOOL_FAILED", "YARA could not scan the input using the locked rules")
	}
	matches, err := parseMatches(string(result))
	if err != nil {
		return fail("TOOL_FAILED", err.Error())
	}
	base["type"] = "result"
	base["output"] = map[string]any{"matches": matches}
	base["artifacts"] = []any{}
	return json.NewEncoder(output).Encode(base)
}

type match struct {
	Rule      string `json:"rule"`
	Namespace string `json:"namespace"`
}

func parseMatches(output string) ([]match, error) {
	matches := []match{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, errors.New("unexpected YARA output")
		}
		ns, rule, ok := strings.Cut(fields[0], ":")
		if !ok || rule == "" || ns == "" {
			return nil, errors.New("missing YARA namespace")
		}
		matches = append(matches, match{Rule: rule, Namespace: ns})
		if len(matches) > 10000 {
			return nil, errors.New("too many rule matches")
		}
	}
	return matches, nil
}
