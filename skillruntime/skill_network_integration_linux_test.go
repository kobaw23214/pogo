//go:build linux

package skillruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/yosukeno/pogo/skillruntime/core"
)

const networkFixtureEntry = "/runtime/bin/pogo-network-fixture"

// Only the copy launched inside LocalExecutor is a provider. The parent test
// serves a reachable loopback socket; the provider must be unable to dial it.
func init() {
	if os.Args[0] != networkFixtureEntry {
		return
	}
	var request ProcessRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		os.Exit(1)
	}
	var input struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(request.Input, &input); err != nil {
		os.Exit(2)
	}
	conn, err := net.DialTimeout("tcp", input.Query, time.Second)
	if err == nil {
		conn.Close()
		fmt.Fprintln(os.Stderr, "Skill reached the parent container loopback socket")
		os.Exit(3)
	}
	if os.Getenv("POGO_SANDBOX_NET") != "" || os.Getenv("POGO_SKILL_NETWORK_TEST_CANARY") != "" {
		fmt.Fprintln(os.Stderr, "Skill inherited the parent network switch or canary")
		os.Exit(4)
	}
	// /usr/bin is intentionally shared read-only. Installed native clients can
	// be executable without granting the Skill network or parent credentials.
	if _, err := exec.LookPath("curl"); err != nil {
		fmt.Fprintln(os.Stderr, "system curl is not visible in the Skill filesystem")
		os.Exit(5)
	}
	frame := map[string]any{"protocolVersion": "pogo.tool/1", "callId": request.CallID, "type": "result", "output": map[string]bool{"ok": true}, "artifacts": []DeclaredArtifact{}}
	if err := json.NewEncoder(os.Stdout).Encode(frame); err != nil {
		os.Exit(6)
	}
	os.Exit(0)
}

// Run in the matching Linux ARM64 runtime image with the deployment seccomp
// profile and pogo-skill-runner. A skipped host run is not a network acceptance.
func TestLocalSkillNetworkIsolationWithShellNetworkEnabled(t *testing.T) {
	if os.Getenv("POGO_SKILL_RUNTIME_TEST_PLATFORM") != "1" {
		t.Skip("set POGO_SKILL_RUNTIME_TEST_PLATFORM=1 in the matching Linux sandbox image")
	}
	t.Setenv("POGO_PLATFORM", "linux/"+runtime.GOARCH)
	t.Setenv("POGO_SANDBOX", "enforce")
	t.Setenv("POGO_SANDBOX_NET", "1")
	t.Setenv("POGO_SKILL_NETWORK_TEST_CANARY", "network-boundary-canary")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	// Positive control: the socket is reachable from the parent container.
	control, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("parent socket is not reachable: %v", err)
	}
	control.Close()
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "runtime")
	if err := os.MkdirAll(filepath.Join(runtimeDir, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "bin", filepath.Base(networkFixtureEntry)), provider, 0755); err != nil {
		t.Fatal(err)
	}
	executor, err := NewLocalExecutor(context.Background(), root)
	if err != nil {
		t.Fatalf("Skill executor unavailable: %v", err)
	}
	input, err := json.Marshal(map[string]string{"query": listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	request := ProcessRequest{
		ProtocolVersion: "pogo.tool/1", CallID: "call:network-boundary", Type: ProcessRequestInvoke, Operation: "inspect",
		Contract: &ContractIdentity{ID: "test.network.boundary", Version: "1.0.0", Digest: bytesDigest(fixtureContractJSON("test.network.boundary", false, false))},
		Input:    input, Context: &ProcessContext{Files: []ProcessFile{}, Assets: []ProcessAsset{}},
	}
	out, err := executor.Execute(context.Background(), Execution{
		RuntimeDir: runtimeDir, Entrypoint: "./bin/pogo-network-fixture", IsolationProfile: LocalProfile,
		Limits: ExecutorLimits{TimeoutSeconds: 10, MemoryMiB: 512, CPUMilliCores: 1000, MaxOutputBytes: 1 << 20}, Request: request,
	})
	if err != nil {
		t.Fatalf("network-isolated provider failed: %v", err)
	}
	if err := core.ValidateDocument(core.KindProcessResponse, out); err != nil {
		t.Fatalf("provider response violates the protocol: %v", err)
	}
	var response ProcessResponse
	if err := json.Unmarshal(out, &response); err != nil || response.Type != "result" || response.CallID != request.CallID || string(response.Output) != `{"ok":true}` {
		t.Fatalf("invalid isolated provider result: %v", err)
	}
}
