package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/internal/testfs"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skillruntime/core"
)

type platformUploadExecutor struct {
	webStubExecutor
	platform string
	calls    int
}

func (e *platformUploadExecutor) ExecutionPlatform() string { return e.platform }
func (e *platformUploadExecutor) Execute(context.Context, skillruntime.Execution) ([]byte, error) {
	e.calls++
	return nil, errors.New("upload must not execute a package")
}
func (e *platformUploadExecutor) ExecuteDetailed(context.Context, skillruntime.Execution) (skillruntime.DetailedResult, error) {
	e.calls++
	return skillruntime.DetailedResult{}, errors.New("upload must not execute a package")
}

func platformUploadZIP(t *testing.T, arch, version string) []byte {
	t.Helper()
	data := webRuntimePackageZip(t, "org.test.platform", version)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var entries []webZipEntry
	for _, entry := range zr.File {
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if entry.Name == core.ManifestFileName {
			var manifest map[string]any
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest["runtimes"].([]any)[0].(map[string]any)["platform"].(map[string]any)["arch"] = arch
			data, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
		}
		entries = append(entries, webZipEntry{name: entry.Name, data: data, mode: entry.Mode()})
	}
	return webZip(t, entries)
}

func TestSkillUploadReportsPlatformReadinessWithoutExecuting(t *testing.T) {
	t.Setenv("POGO_PLATFORM", "")
	for _, tc := range []struct {
		name, hostArch, packageArch string
		ready                       bool
	}{
		{"amd64", "amd64", "amd64", true},
		{"arm64", "arm64", "arm64", true},
		{"amd64-package-on-arm64", "arm64", "amd64", false},
		{"arm64-package-on-amd64", "amd64", "arm64", false},
		{"executor-unavailable", "", "amd64", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := testfs.SealedTempDir(t)
			// Installed bundles are immutable. Restore only this test's private
			// directory permissions before testing.TempDir removes the fixture.
			t.Cleanup(func() {
				_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
					if err == nil && entry.IsDir() {
						return os.Chmod(path, 0o700)
					}
					return err
				})
			})
			service, err := skillruntime.NewManagementService(root)
			if err != nil {
				t.Fatal(err)
			}
			executor := &platformUploadExecutor{platform: "linux/" + tc.hostArch}
			if tc.hostArch != "" {
				service.Executor = executor
			} else {
				service.ExecutionIssue = "部署平台与当前运行架构不一致"
				service.ExecutionIssueCode = "PLATFORM_MISMATCH"
			}
			h, _ := newStudioHarness(t, []llm.Response{textTurn("unused")}, func(c *Config) { c.SkillRuntime = service })
			var result struct {
				Status    string
				Ready     bool
				SkillIDs  []string `json:"skill_ids"`
				Execution skillruntime.ExecutionStatus
				Issues    []skillruntime.SelectionIssue
			}
			runtimeSkillResponse(t, postSkillZip(t, h, platformUploadZIP(t, tc.packageArch, "1.0.0")), 201, &result)
			if result.Status != "installed" || result.Ready != tc.ready || len(result.SkillIDs) != 1 || len(service.List()) != 1 {
				t.Fatalf("installation and execution readiness were conflated: %+v", result)
			}
			if tc.ready && len(result.Issues) != 0 || !tc.ready && len(result.Issues) == 0 {
				t.Fatalf("readiness lacks matching diagnostics: %+v", result)
			}
			if tc.hostArch == "" && !strings.Contains(result.Issues[0].Message, service.ExecutionIssue) {
				t.Fatalf("lost execution failure: %+v", result)
			}
			if !tc.ready && tc.hostArch != "" {
				raw, _ := json.Marshal(result.Issues)
				if !bytes.Contains(raw, []byte("linux/amd64")) || !bytes.Contains(raw, []byte("linux/arm64")) {
					t.Fatalf("platform mismatch does not explain both platforms: %s", raw)
				}
			}
			var plan skillruntime.SelectionPlan
			runtimeSkillResponse(t, h.do("POST", "/api/manage/skill-selection", studioBody(map[string]any{"skill_ids": result.SkillIDs})), 200, &plan)
			if plan.Ready != result.Ready {
				t.Fatalf("upload and selection disagree: upload=%+v plan=%+v", result, plan)
			}
			// Replacing an installed bundle uses the same readiness contract.
			var confirmation struct {
				Status string
				Token  string `json:"confirm_token"`
			}
			runtimeSkillResponse(t, postSkillZip(t, h, platformUploadZIP(t, tc.packageArch, "2.0.0")), 200, &confirmation)
			if confirmation.Status != "confirm_replace" || confirmation.Token == "" {
				t.Fatalf("expected replacement confirmation: %+v", confirmation)
			}
			runtimeSkillResponse(t, resolveSkillUpload(t, h, confirmation.Token, "replace"), 201, &result)
			if result.Status != "installed" || result.Ready != tc.ready {
				t.Fatalf("replacement lost readiness: %+v", result)
			}
			if executor.calls != 0 {
				t.Fatalf("upload/selection ran package code %d times", executor.calls)
			}
		})
	}
}
