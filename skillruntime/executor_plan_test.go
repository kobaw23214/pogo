//go:build linux

package skillruntime

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func planExecutor(t *testing.T) *LocalExecutor {
	t.Helper()
	root := t.TempDir()
	platform, ok := localPlatformSpec("linux/" + runtime.GOARCH)
	if !ok {
		t.Skip("local execution requires amd64 or arm64")
	}
	return &LocalExecutor{root: root, workDir: root, helper: "/usr/local/bin/pogo-skill-runner", capacity: make(chan struct{}, 1), platform: platform}
}

func TestLocalSandboxNamespacePlan(t *testing.T) {
	args := planExecutor(t).baseArgs()
	joined := strings.Join(args, " ")
	for _, flag := range []string{"--unshare-user", "--unshare-pid", "--unshare-net", "--unshare-ipc", "--unshare-uts", "--as-pid-1", "--die-with-parent", "--cap-drop ALL", "--clearenv", "--proc /proc", "--size 536870912 --tmpfs /tmp"} {
		if !strings.Contains(joined, flag) {
			t.Fatalf("missing isolation flag %s", flag)
		}
	}
	if strings.Contains(joined, "--ro-bind / /") || strings.Contains(joined, "--share-net") {
		t.Fatal("host filesystem or network exposed")
	}
}

func elfRuntimeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 32)
	copy(header, []byte{0x7f, 'E', 'L', 'F', 2, 1})
	spec, ok := localPlatformSpec("linux/" + runtime.GOARCH)
	if !ok {
		t.Skip("local execution requires amd64 or arm64")
	}
	header[18] = byte(spec.machine)
	if err := os.WriteFile(filepath.Join(dir, "bin", "tool"), header, 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func invokeExecution(runtimeDir string, ctx *ProcessContext) Execution {
	return Execution{
		RuntimeDir:       runtimeDir,
		Entrypoint:       "./bin/tool",
		IsolationProfile: LocalProfile,
		Limits:           ExecutorLimits{TimeoutSeconds: 60, MemoryMiB: 512, CPUMilliCores: 1000, MaxOutputBytes: 1048576},
		Request:          ProcessRequest{ProtocolVersion: "pogo.tool/1", CallID: "call:x", Type: "invoke", Operation: "op", Context: ctx},
	}
}

func TestExecutorOutputDirectoryGating(t *testing.T) {
	e := planExecutor(t)
	runtimeDir := elfRuntimeDir(t)
	e.root = filepath.Dir(runtimeDir)
	// Callers never pick the container output path themselves.
	preset := "/output"
	in := invokeExecution(runtimeDir, &ProcessContext{Files: []ProcessFile{}, Assets: []ProcessAsset{}, OutputDirectory: &preset})
	if _, err := e.ExecuteDetailed(context.Background(), in); err == nil || !strings.Contains(err.Error(), "UNSUPPORTED_EFFECT") {
		t.Fatalf("caller-supplied output directory accepted: %v", err)
	}
	// Artifact collection requires an invoke context.
	in = invokeExecution(runtimeDir, nil)
	in.CollectArtifacts = true
	if _, err := e.ExecuteDetailed(context.Background(), in); err == nil || !strings.Contains(err.Error(), "UNSUPPORTED_EFFECT") {
		t.Fatalf("context-free artifact collection accepted: %v", err)
	}
}
