// Command pogo-knowledge-provider implements the pogo.tool/1 process-json
// driver for the malagent knowledge contracts: malware.attck.lookup (ATT&CK
// entry search over a packaged snapshot) and malware.tiweb.stats (threat
// intelligence aggregate snapshot). Stdlib only, so the binary cross-builds
// static for the sandbox runtime bundle.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const protocolVersion = "pogo.tool/1"

type contractIdentity struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

type processRequest struct {
	ProtocolVersion string             `json:"protocolVersion"`
	CallID          string             `json:"callId"`
	Type            string             `json:"type"`
	Operation       string             `json:"operation,omitempty"`
	Contract        *contractIdentity `json:"contract,omitempty"`
	Input           json.RawMessage    `json:"input,omitempty"`
}

type responseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type declaredArtifact struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	MediaType string `json:"mediaType"`
}

type processResponse struct {
	ProtocolVersion string           `json:"protocolVersion"`
	CallID          string           `json:"callId"`
	Type            string           `json:"type"`
	Operations      []operationEntry `json:"operations,omitempty"`
	Output          json.RawMessage  `json:"output,omitempty"`
	// artifacts is required by the result frame even when empty, and
	// forbidden on describe/error frames — a pointer keeps both: nil omits,
	// &[] marshals as the empty array.
	Artifacts *[]declaredArtifact `json:"artifacts,omitempty"`
	Error     *responseError     `json:"error,omitempty"`
}

type operationEntry struct {
	Operation string           `json:"operation"`
	Contract  contractIdentity `json:"contract"`
}

// The two contracts this provider implements, digests included: the
// prepare handshake sends a contract-less describe and matches the reply
// against the manifest pins, so the identities are baked in at build time
// by the packaging script (contracts_gen.go).
var (
	attckContract = contractIdentity{ID: attckContractID, Version: attckContractVersion, Digest: attckContractDigest}
	tiwebContract = contractIdentity{ID: tiwebContractID, Version: tiwebContractVersion, Digest: tiwebContractDigest}
)

func main() {
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		fmt.Fprintf(os.Stderr, "read request: %v", err)
		os.Exit(1)
	}
	var req processRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeResponse(processResponse{ProtocolVersion: protocolVersion, Type: "error",
			Error: &responseError{Code: "INVALID_REQUEST", Message: err.Error()}})
		return
	}
	resp := dispatch(req)
	resp.CallID = req.CallID
	if resp.ProtocolVersion == "" {
		resp.ProtocolVersion = protocolVersion
	}
	writeResponse(resp)
}

func dispatch(req processRequest) processResponse {
	switch req.Type {
	case "describe":
		return describeReply(req)
	case "invoke":
		return invokeReply(req)
	default:
		return processResponse{Type: "error", Error: &responseError{
			Code: "UNSUPPORTED_OPERATION", Message: fmt.Sprintf("unknown request type %q", req.Type)}}
	}
}

func describeReply(req processRequest) processResponse {
	// The prepare handshake describes without a pinned contract and matches
	// the reply against the manifest pins, so the provider self-describes
	// with its baked-in identities. A contract-bearing describe still echoes
	// it back verbatim.
	ops := []operationEntry{
		{Operation: operationOf(attckContract.ID), Contract: attckContract},
		{Operation: operationOf(tiwebContract.ID), Contract: tiwebContract},
	}
	if req.Contract != nil {
		ops = []operationEntry{{Operation: operationOf(req.Contract.ID), Contract: *req.Contract}}
	}
	return processResponse{Type: "describe", Operations: ops}
}

func invokeReply(req processRequest) processResponse {
	if req.Contract == nil {
		return processResponse{Type: "error", Error: &responseError{Code: "INVALID_REQUEST", Message: "missing contract"}}
	}
	switch req.Contract.ID {
	case attckContract.ID:
		out, err := attckLookup(assetPath("attck-entries", "entries.json"), req.Input)
		if err != nil {
			return errReply(err)
		}
		return processResponse{Type: "result", Output: out, Artifacts: &[]declaredArtifact{}}
	case tiwebContract.ID:
		out, err := tiwebStats(assetPath("tiweb-stats", "stats.json"), req.Input)
		if err != nil {
			return errReply(err)
		}
		return processResponse{Type: "result", Output: out, Artifacts: &[]declaredArtifact{}}
	default:
		return processResponse{Type: "error", Error: &responseError{
			Code: "UNSUPPORTED_OPERATION", Message: fmt.Sprintf("unknown contract %q", req.Contract.ID)}}
	}
}

func errReply(err error) processResponse {
	// The response schema's error enum is closed: provider-internal codes map
	// onto the closest protocol meaning.
	code, msg := "TOOL_FAILED", err.Error()
	if e, ok := err.(*toolError); ok {
		msg = e.message
		switch e.code {
		case "INVALID_INPUT", "INVALID_REQUEST":
			code = "INVALID_INPUT"
		case "ASSET_MISSING", "ASSET_INVALID":
			code = "RESOURCE_UNAVAILABLE"
		default:
			code = "TOOL_FAILED"
		}
	}
	return processResponse{Type: "error", Error: &responseError{Code: code, Message: msg}}
}

type toolError struct {
	code    string
	message string
}

func (e *toolError) Error() string { return e.code + ": " + e.message }

func operationOf(contractID string) string {
	// malware.attck.lookup → attck.lookup (the manifest's operation names).
	if len(contractID) > len("malware.") {
		return contractID[len("malware."):]
	}
	return contractID
}

func assetPath(assetID, file string) string {
	return "/assets/" + assetID + "/" + file
}

func writeResponse(resp processResponse) {
	out, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal response:", err)
		os.Exit(1)
	}
	os.Stdout.Write(out)
}
