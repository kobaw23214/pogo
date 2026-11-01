// Package skillruntime runs declarative pkg bundles in Pogo's local sandbox.
package skillruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/yosukeno/pogo/skillruntime/core"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Executor is the trusted host boundary. Packages cannot choose a backend.
type Executor interface {
	Identity() string
	Execute(context.Context, Execution) ([]byte, error)
	ExecuteDetailed(context.Context, Execution) (DetailedResult, error)
}

var assetIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[.-][a-z0-9]+)*$`)

// ExecutorLimits is an effective host grant, never supplied by the model.
type ExecutorLimits struct {
	TimeoutSeconds int `json:"timeoutSeconds"`
	MemoryMiB      int `json:"memoryMiB"`
	CPUMilliCores  int `json:"cpuMilliCores"`
	MaxOutputBytes int `json:"maxOutputBytes"`
}

type ContractIdentity struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// Process-request frame types of the pogo.tool/1 wire protocol. Every host
// construction site must use these constants; the schema enum has exactly
// these two values and providers reject anything else.
const (
	ProcessRequestDescribe = "describe"
	ProcessRequestInvoke   = "invoke"
)

// marshalOutboundRequest encodes the final wire frame and validates it against
// the process-request schema at the executor boundary — after sandbox paths
// are filled in, right before the provider reads it. Responses get the same
// schema gate on the way back; a drift in request construction now fails here
// with the schema error instead of surfacing as a provider-side
// UNSUPPORTED_OPERATION that blames an innocent contract.
func marshalOutboundRequest(req *ProcessRequest) ([]byte, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode process request: %w", err)
	}
	if err := core.ValidateDocument(core.KindProcessRequest, raw); err != nil {
		return nil, fmt.Errorf("INVALID_REQUEST: outbound frame violates pogo.tool/1: %w", err)
	}
	return raw, nil
}

type ProcessRequest struct {
	ProtocolVersion string            `json:"protocolVersion"`
	CallID          string            `json:"callId"`
	Type            string            `json:"type"`
	Operation       string            `json:"operation,omitempty"`
	Contract        *ContractIdentity `json:"contract,omitempty"`
	Input           json.RawMessage   `json:"input,omitempty"`
	Context         *ProcessContext   `json:"context,omitempty"`
}

type ProcessContext struct {
	Files           []ProcessFile  `json:"files"`
	Assets          []ProcessAsset `json:"assets"`
	OutputDirectory *string        `json:"outputDirectory"`
}

type ProcessFile struct {
	InputPointer string `json:"inputPointer"`
	FileID       string `json:"fileId"`
	Path         string `json:"path"`
	Digest       string `json:"digest"`
}

type ProcessAsset struct {
	AssetID string `json:"assetId"`
	Path    string `json:"path"`
	Digest  string `json:"digest"`
}

// DeclaredArtifact is one artifact the provider claims to have written under
// its output directory. Path stays relative ("./...") and is matched by the
// host against the bounded collection of the container output filesystem;
// it is never interpreted as a host path.
type DeclaredArtifact struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	MediaType string `json:"mediaType"`
}

type ProcessResponse struct {
	ProtocolVersion string `json:"protocolVersion"`
	CallID          string `json:"callId"`
	Type            string `json:"type"`
	Operations      []struct {
		Operation string           `json:"operation"`
		Contract  ContractIdentity `json:"contract"`
	} `json:"operations,omitempty"`
	Output    json.RawMessage    `json:"output,omitempty"`
	Artifacts []DeclaredArtifact `json:"artifacts,omitempty"`
	Error     *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Execution contains only paths resolved by the host from installed packages.
// The sandbox never receives model-supplied mounts, environment or command arguments.
type Execution struct {
	Package    core.PackageRef
	ProviderID string
	RuntimeDir string
	Entrypoint string
	Assets     map[string]string
	Files      map[string][]byte // input pointer -> immutable authorized bytes
	Request    ProcessRequest
	Limits     ExecutorLimits
	// IsolationProfile is a host-resolved grant, never chosen by the model.
	IsolationProfile string
	// CollectArtifacts enables a bounded private /output tmpfs. The trusted
	// supervisor collects regular files after all provider processes exit.
	CollectArtifacts bool
}

// CollectedArtifact is one regular file safely extracted from the bounded
// container output directory before it is matched against the provider
// declaration and published by the service.
type CollectedArtifact struct {
	Path string // cleaned relative path inside /output, e.g. "reports/a.bin"
	Data []byte
}

// DetailedResult extends the Execute result with the safely collected
// output-directory files (still unpublished and unverified at this stage).
type DetailedResult struct {
	Output    []byte
	Artifacts []CollectedArtifact
}

const (
	maxArtifactFiles      = 16
	maxArtifactFileBytes  = 16 << 20
	maxArtifactTotalBytes = 32 << 20
)

func opaqueID(prefix string) string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(value[:])
}

func validLimits(l ExecutorLimits) bool {
	return l.TimeoutSeconds >= 1 && l.TimeoutSeconds <= 3600 && l.MemoryMiB >= 16 && l.MemoryMiB <= 65536 &&
		l.CPUMilliCores >= 100 && l.CPUMilliCores <= 64000 && l.MaxOutputBytes >= 1024 && l.MaxOutputBytes <= 67108864
}

type boundedCapture struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	max      int
	overflow bool
	cancel   context.CancelFunc
}

func (b *boundedCapture) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if b.buf.Len()+n > b.max {
		remaining := b.max - b.buf.Len()
		if remaining > 0 {
			_, _ = b.buf.Write(p[:remaining])
		}
		b.overflow = true
		b.cancel()
		return n, nil
	}
	return b.buf.Write(p)
}

// cleanArtifactPath normalizes an artifact name to a safe relative path.
// Absolute paths, drive/UNC tricks, ".." segments and backslashes are
// rejected so an entry can never escape the staging area.
func cleanArtifactPath(raw string) (string, error) {
	if raw == "" || strings.ContainsRune(raw, 0) || strings.Contains(raw, "\\") || path.IsAbs(raw) || strings.Contains(raw, ":") {
		return "", errors.New("OUTPUT_INVALID: unsafe artifact entry name")
	}
	parts := strings.Split(raw, "/")
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			return "", errors.New("OUTPUT_INVALID: artifact path escapes output directory")
		default:
			clean = append(clean, part)
		}
	}
	return strings.Join(clean, "/"), nil
}

func confinedPath(root, source string) (string, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, source)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes executor store")
	}
	return rel, nil
}
