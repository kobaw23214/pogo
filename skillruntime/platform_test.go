package skillruntime

import (
	"context"
	"debug/elf"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/internal/testfs"
	"github.com/yosukeno/pogo/skillruntime/core"
)

func TestPlatformDetectionAndEnvironmentAssertion(t *testing.T) {
	for _, tc := range []struct{ os, arch, expected, code string }{
		{"linux", "amd64", "", ""},
		{"linux", "arm64", "", ""},
		{"linux", "amd64", "linux/amd64", ""},
		{"linux", "arm64", "linux/arm64", ""},
		{"linux", "arm64", "linux/amd64", "PLATFORM_MISMATCH"},
		{"linux", "amd64", "linux/arm64", "PLATFORM_MISMATCH"},
		{"linux", "amd64", "linux/x86_64", "INVALID_PLATFORM"},
		{"linux", "arm64", "aarch64", "INVALID_PLATFORM"},
		{"darwin", "arm64", "", "UNSUPPORTED_HOST_PLATFORM"},
		{"linux", "386", "", "UNSUPPORTED_HOST_PLATFORM"},
	} {
		t.Run(tc.os+"_"+tc.arch+"_"+tc.expected, func(t *testing.T) {
			p := detectPlatform(tc.os, tc.arch, tc.expected)
			if p.host != tc.os+"/"+tc.arch || p.expected != tc.expected {
				t.Fatalf("platform assertion changed host identity: %+v", p)
			}
			if tc.code == "" {
				if p.issue != nil || p.spec.name != p.host {
					t.Fatalf("supported platform rejected: %+v", p)
				}
			} else if p.issue == nil || p.issue.Code != tc.code {
				t.Fatalf("wanted %s: %+v", tc.code, p)
			}
		})
	}
}

func TestPlatformSystemLibrariesAndELF(t *testing.T) {
	for _, tc := range []struct {
		name, triplet string
		machine       elf.Machine
	}{
		{"linux/amd64", "x86_64-linux-gnu", elf.EM_X86_64},
		{"linux/arm64", "aarch64-linux-gnu", elf.EM_AARCH64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, ok := localPlatformSpec(tc.name)
			if !ok || p.machine != tc.machine {
				t.Fatalf("invalid platform spec: %+v", p)
			}
			want := []string{"/lib/" + tc.triplet + "/libc.so.6", "/lib/" + tc.triplet + "/libgcc_s.so.1", "/usr/lib/" + tc.triplet + "/libstdc++.so.6", "/lib/" + tc.triplet + "/libz.so.1"}
			if !reflect.DeepEqual(p.systemLibraries(), want) {
				t.Fatalf("wrong platform libraries: %v", p.systemLibraries())
			}
			header := make([]byte, 20)
			copy(header, []byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB)})
			binary.LittleEndian.PutUint16(header[18:], uint16(tc.machine))
			if err := p.validateELFHeader(header); err != nil {
				t.Fatalf("native executable rejected: %v", err)
			}
			other := elf.EM_AARCH64
			if tc.machine == other {
				other = elf.EM_X86_64
			}
			binary.LittleEndian.PutUint16(header[18:], uint16(other))
			var issue *ExecutionError
			if err := p.validateELFHeader(header); !errors.As(err, &issue) || issue.Code != "UNSUPPORTED_PLATFORM" {
				t.Fatalf("foreign executable accepted: %v", err)
			}
			binary.LittleEndian.PutUint16(header[18:], uint16(tc.machine))
			for _, bad := range [][]byte{nil, []byte("#!/bin/sh\necho hi\n"), append([]byte{}, header[:19]...), append([]byte{}, header...)} {
				if len(bad) == 20 {
					bad[5] = byte(elf.ELFDATA2MSB)
				}
				if err := p.validateELFHeader(bad); !errors.As(err, &issue) || issue.Code != "INVALID_MANIFEST" {
					t.Fatalf("invalid ELF accepted: %v", err)
				}
			}
		})
	}
}

func TestPlatformRuntimeABICompatibility(t *testing.T) {
	for _, host := range supportedLocalPlatforms() {
		for _, arch := range []string{"amd64", "arm64"} {
			p := core.Platform{OS: "linux", Arch: arch, Libc: "glibc", MinLibcVersion: "2.36"}
			issue := runtimePlatformIssue(p, host)
			if (issue == nil) != (host == "linux/"+arch) {
				t.Fatalf("wrong host/package compatibility: %s %+v %v", host, p, issue)
			}
		}
	}
	for _, p := range []core.Platform{
		{OS: "linux", Arch: "amd64", Libc: "musl", MinLibcVersion: "2.36"},
		{OS: "linux", Arch: "arm64", Libc: "glibc", MinLibcVersion: "2.35"},
		{OS: "linux", Arch: "arm64", Libc: "glibc", MinLibcVersion: "2.37"},
		{OS: "darwin", Arch: "arm64", Libc: "glibc", MinLibcVersion: "2.36"},
	} {
		if issue := runtimePlatformIssue(p, p.OS+"/"+p.Arch); issue == nil || issue.Code != "UNSUPPORTED_PLATFORM" {
			t.Fatalf("unverified ABI accepted: %+v %v", p, issue)
		}
	}
}

func TestPlatformExecutionStatusUsesStructuredIssue(t *testing.T) {
	p := detectPlatform("linux", "arm64", "linux/amd64")
	s := &Service{platform: &p}
	status := s.ExecutionStatus()
	if status.Configured || status.HostPlatform != "linux/arm64" || status.ExpectedPlatform != "linux/amd64" || status.IssueCode != "PLATFORM_MISMATCH" || len(status.SupportedPlatforms) != 2 {
		t.Fatalf("incorrect mismatch diagnostic: %+v", status)
	}
	s.SetExecutionError(fmt.Errorf("startup: %w", &ExecutionError{Code: "RUNNER_UNAVAILABLE", Message: "runner missing"}))
	if got := s.ExecutionStatus(); got.IssueCode != "RUNNER_UNAVAILABLE" || got.Issue != "runner missing" {
		t.Fatalf("wrapped structured issue lost: %+v", got)
	}
	s.SetExecutionError(errors.New("custom startup failure"))
	if got := s.ExecutionStatus(); got.IssueCode != "EXECUTOR_UNAVAILABLE" || got.Issue != "custom startup failure" {
		t.Fatalf("unclassified startup issue lost: %+v", got)
	}
	s.SetExecutionError(nil)
	if s.ExecutionIssue != "" || s.ExecutionIssueCode != "" {
		t.Fatal("startup issue was not cleared")
	}
	s.Executor = stubExecutor{}
	if got := s.ExecutionStatus(); !got.Configured || got.IssueCode != "" || got.Issue != "" {
		t.Fatalf("ready executor retained stale issue: %+v", got)
	}
	status.SupportedPlatforms[0] = "changed"
	if s.ExecutionStatus().SupportedPlatforms[0] != "linux/amd64" {
		t.Fatal("caller mutated shared supported-platform list")
	}
}

func TestPlatformEnvironmentMismatchBlocksBeforeSandboxDiscovery(t *testing.T) {
	expected := "linux/amd64"
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		expected = "linux/arm64"
	}
	t.Setenv(EnvPlatform, expected)
	t.Setenv("PATH", "")
	_, err := NewLocalExecutor(context.Background(), t.TempDir())
	var issue *ExecutionError
	if !errors.As(err, &issue) || issue.Code != "PLATFORM_MISMATCH" {
		t.Fatalf("environment assertion did not block initialization: %v", err)
	}
	s, err := NewManagementService(testfs.SealedTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s.SetExecutionError(issue)
	if got := s.ExecutionStatus(); got.ExpectedPlatform != expected || got.HostPlatform != runtime.GOOS+"/"+runtime.GOARCH || got.Configured {
		t.Fatalf("management state lost actual platform: %+v", got)
	}
}

// Installed runtime files are intentionally immutable; restore directory write
// permission only in this fixture's cleanup so non-root test runners can remove it.
func cleanupPlatformFixture(t *testing.T, root string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				return os.Chmod(path, 0700)
			}
			return nil
		})
	})
}

func TestPlatformSelectionCatalogPlanAndBindingAgree(t *testing.T) {
	for _, hostArch := range []string{"amd64", "arm64"} {
		for _, packageArch := range []string{"amd64", "arm64"} {
			t.Run(hostArch+"_package_"+packageArch, func(t *testing.T) {
				s := newStubService(t)
				cleanupPlatformFixture(t, s.Root)
				p := detectPlatform("linux", hostArch, "")
				s.platform = &p
				installed, _ := installSkillFixtureForPlatform(t, s, packageArch)
				selection := Selection{SkillIDs: []string{PackageSkillID(installed.Ref)}}
				catalog := s.SelectionCatalog()
				plan, err := s.PlanSelection(selection)
				if err != nil || len(plan.Binding) == 0 {
					t.Fatalf("package preview lost draft binding: %+v %v", plan, err)
				}
				wantReady := hostArch == packageArch
				if plan.Ready != wantReady || (catalog.Skills[0].State == "available") != wantReady || (s.ValidateBinding(plan.Binding) == nil) != wantReady {
					t.Fatalf("catalog, preview and execution binding diverged: %+v %+v", catalog, plan)
				}
				if !wantReady && (catalog.Skills[0].State != "needs_configuration" || len(plan.Issues) != 1 || plan.Issues[0].Code != "UNSUPPORTED_PLATFORM") {
					t.Fatalf("incompatibility was not actionable: %+v", plan)
				}
			})
		}
	}

	t.Run("executor_issue_and_empty_selection", func(t *testing.T) {
		s := newStubService(t)
		cleanupPlatformFixture(t, s.Root)
		installed, _ := installSkillFixture(t, s)
		s.Executor = nil
		s.SetExecutionError(&ExecutionError{Code: "SANDBOX_UNAVAILABLE", Message: "namespace probe failed"})
		catalog := s.SelectionCatalog()
		plan, err := s.PlanSelection(Selection{SkillIDs: []string{PackageSkillID(installed.Ref)}})
		if err != nil || plan.Ready || len(plan.Binding) == 0 || len(plan.Issues) != 1 || plan.Issues[0] != catalog.Skills[0].Issues[0] || plan.Issues[0].Code != "SANDBOX_UNAVAILABLE" || plan.Issues[0].Message != "namespace probe failed" {
			t.Fatalf("executor diagnostic lost: %+v %v", plan, err)
		}
		empty, err := s.PlanSelection(Selection{})
		if err != nil || !empty.Ready || len(empty.Issues) != 0 {
			t.Fatalf("ordinary agent blocked by optional Skill executor: %+v %v", empty, err)
		}
		raw, _ := json.Marshal(plan)
		if strings.Contains(string(raw), s.Root) || strings.Contains(string(raw), "\"binding\"") {
			t.Fatal("platform diagnostics exposed private binding or store path")
		}
	})
}
