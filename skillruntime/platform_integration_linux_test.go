//go:build linux

package skillruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/internal/testfs"
	"github.com/yosukeno/pogo/skillruntime/core"
)

const platformFixtureEntry = "/runtime/bin/pogo-platform-fixture"
const platformFixtureContract = "test.platform.inspect"

// The compiled test binary doubles as a harmless provider only when executed
// at this exact sandbox entrypoint. No compiler or external Skill is needed
// inside the runtime image to exercise the full describe/invoke protocol.
func init() {
	if os.Args[0] != platformFixtureEntry {
		return
	}
	var request ProcessRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	contract := fixtureContractJSON(platformFixtureContract, true, false)
	frame := map[string]any{"protocolVersion": "pogo.tool/1", "callId": request.CallID}
	switch request.Type {
	case ProcessRequestDescribe:
		frame["type"] = "describe"
		frame["operations"] = []any{map[string]any{"operation": "inspect", "contract": ContractIdentity{ID: platformFixtureContract, Version: "1.0.0", Digest: bytesDigest(contract)}}}
	case ProcessRequestInvoke:
		if request.Context == nil || len(request.Context.Files) != 1 {
			os.Exit(2)
		}
		data, err := os.ReadFile(request.Context.Files[0].Path)
		if err != nil {
			os.Exit(3)
		}
		if string(data) == "sleep" {
			time.Sleep(10 * time.Second)
		}
		interfaces, err := net.Interfaces()
		isolatedNetwork := err == nil
		for _, iface := range interfaces {
			// Some kernels create down tunnel interfaces in every new namespace.
			if iface.Flags&net.FlagLoopback == 0 && iface.Flags&net.FlagUp != 0 {
				isolatedNetwork = false
			}
		}
		_, homeErr := os.Stat("/home/pogo")
		if string(data) != "native platform input" || !isolatedNetwork || !os.IsNotExist(homeErr) {
			fmt.Fprintf(os.Stderr, "fixture check: input=%q interfaces=%v network_error=%v home_error=%v\n", data, interfaces, err, homeErr)
			os.Exit(6)
		}
		frame["type"] = "result"
		frame["output"] = map[string]bool{"ok": true}
		frame["artifacts"] = []any{}
	default:
		os.Exit(4)
	}
	if err := json.NewEncoder(os.Stdout).Encode(frame); err != nil {
		os.Exit(5)
	}
	os.Exit(0)
}

func nativePlatformSkillZIP(t *testing.T, arch string, executable []byte) []byte {
	t.Helper()
	contract := fixtureContractJSON(platformFixtureContract, true, false)
	ref := core.ContractRef{ID: platformFixtureContract, Version: "1.0.0", Digest: bytesDigest(contract)}
	payload := testZip(t, []testZipEnt{{name: "bin/pogo-platform-fixture", data: executable, mode: 0o755}})
	manifest := map[string]any{
		"apiVersion": core.APIVersion, "kind": "SkillRuntime", "id": "org.test.native-" + arch, "version": "1.0.0", "displayName": "Native platform fixture",
		"requiredFeatures":     []string{"bundle.v1", "process-json.v1", "isolated-execution.v1"},
		"contracts":            []core.ManifestContractEntry{{Ref: ref, Path: "./contracts/inspect.json"}},
		"requiresCapabilities": []core.ContractRef{ref},
		"runtimes": []core.Runtime{{ID: "rt", Driver: "bundle", Artifact: core.Artifact{Path: "./payload/rt.zip", Digest: bytesDigest(payload), SizeBytes: int64(len(payload)), Format: "zip"},
			Platform: core.Platform{OS: "linux", Arch: arch, Libc: "glibc", MinLibcVersion: "2.36"}, RequiresIsolation: []string{"filesystem", "network", "process-limits"}}},
		"providers": []any{map[string]any{"id": "inspect", "implements": ref, "driver": "process-json", "runtimeId": "rt", "entrypoint": "./bin/pogo-platform-fixture", "operation": "inspect",
			"resourceSlots": []string{"samples"}, "assetIds": []string{}, "requests": core.Permissions{Network: "none", Limits: core.Limits{TimeoutSeconds: 20, MemoryMiB: 512, CPUMilliCores: 1000, MaxOutputBytes: 1 << 20}}}},
		"assets": []core.Asset{}, "resourceSlots": []core.ResourceSlot{{ID: "samples", Kind: "files", Description: "Fixture input"}},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return testZip(t, []testZipEnt{
		{name: core.ManifestFileName, data: raw, mode: 0o644},
		{name: "SKILL.md", data: []byte("---\nname: native-platform\ndescription: Native platform acceptance fixture.\n---\nInspect the supplied fixture.\n"), mode: 0o644},
		{name: "contracts/inspect.json", data: contract, mode: 0o644},
		{name: "payload/rt.zip", data: payload, mode: 0o644},
	})
}

// Run the compiled test in a Linux runtime image with the deployment's seccomp
// configuration and the matching pogo-skill-runner on PATH. This is opt-in
// because it requires functioning user/PID/network namespaces, not a mock.
func TestLocalPlatformIntegration(t *testing.T) {
	if os.Getenv("POGO_SKILL_RUNTIME_TEST_PLATFORM") != "1" {
		t.Skip("set POGO_SKILL_RUNTIME_TEST_PLATFORM=1 in the matching Linux sandbox image")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Fatal("platform acceptance requires amd64 or arm64")
	}
	t.Setenv("POGO_PLATFORM", "linux/"+runtime.GOARCH)
	root := testfs.SealedTempDir(t)
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				return os.Chmod(path, 0o700)
			}
			return err
		})
	})
	service, err := NewService(context.Background(), root)
	if err != nil {
		t.Fatalf("native executor initialization: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := service.Import(bytes.NewReader(nativePlatformSkillZIP(t, runtime.GOARCH, provider)))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanSelection(Selection{SkillIDs: []string{PackageSkillID(installed.Ref)}})
	if err != nil || !plan.Ready {
		t.Fatalf("matching platform not ready: %+v %v", plan, err)
	}
	if err := service.Prepare(context.Background(), plan.Binding); err != nil {
		t.Fatalf("native describe handshake: %v", err)
	}
	lock, err := service.MakeLock(plan.Binding, "native-platform-test")
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Session(plan.Binding, lock, "native-platform-test")
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(ctx context.Context, content string) error {
		file, err := session.Upload([]byte(content))
		if err != nil {
			return err
		}
		args, _ := json.Marshal(map[string]string{"fileId": file.FileID})
		result, err := session.Call(ctx, platformFixtureContract, args)
		if err == nil && !bytes.Contains(result.Data, []byte(`"ok":true`)) {
			return fmt.Errorf("fixture input or namespace isolation failed: %s", result.Data)
		}
		return err
	}
	if err := invoke(context.Background(), "native platform input"); err != nil {
		t.Fatalf("native invoke: %v", err)
	}
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		started := time.Now()
		if err := invoke(ctx, "sleep"); err == nil {
			t.Fatal("cancelled provider call succeeded")
		}
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Fatalf("cancellation did not stop the provider promptly: %s", elapsed)
		}
	})
	other := "arm64"
	if runtime.GOARCH == "arm64" {
		other = "amd64"
	}
	t.Run("foreign-package", func(t *testing.T) {
		installed, err := service.Import(bytes.NewReader(nativePlatformSkillZIP(t, other, provider)))
		if err != nil {
			t.Fatal(err)
		}
		plan, err := service.PlanSelection(Selection{SkillIDs: []string{PackageSkillID(installed.Ref)}})
		if err != nil || plan.Ready || len(plan.Issues) == 0 {
			t.Fatalf("foreign package readiness: %+v %v", plan, err)
		}
		if err := service.Prepare(context.Background(), plan.Binding); err == nil {
			t.Fatal("foreign package passed execution preparation")
		}
	})
	t.Run("incorrect-deployment-marker", func(t *testing.T) {
		t.Setenv("POGO_PLATFORM", "linux/"+other)
		_, err := NewLocalExecutor(context.Background(), t.TempDir())
		if err == nil || !strings.Contains(err.Error(), runtime.GOARCH) || !strings.Contains(err.Error(), other) {
			t.Fatalf("incorrect deployment platform was not explained: %v", err)
		}
	})
}
