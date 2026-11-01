package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/skillruntime"
)

const capaFixture = `{
  "meta": {"analysis": {"format": "pe", "os": "windows", "arch": "amd64"}},
  "rules": {
    "inject with APC": {"meta": {"namespace": "anti-analysis/injection", "att&ck": [
      {"tactic": "DEFENSE EVASION", "technique": "Process Injection", "id": "T1055"},
      {"tactic": "DEFENSE EVASION", "technique": "Process Injection", "subtechnique": "Process Injection: APC", "id": "T1055.004"}
    ]}},
    "create mutex": {"meta": {"namespace": "host-interaction/mutex", "att&ck": []}},
    "resolve DNS": {"meta": {"namespace": "communication/dns"}}
  }
}`

func capaAssetCtx(t *testing.T, fileID, content string) *skillruntime.ProcessContext {
	t.Helper()
	ctx := fileCtx(t, fileID, content)
	ctx.Assets = []skillruntime.ProcessAsset{{AssetID: "capa-rules", Path: "/assets/capa-rules", Digest: "sha256:" + strings.Repeat("a", 64)}}
	return ctx
}

func capaStat(string) (fs.FileInfo, error) { return fakeFileInfo{dir: true}, nil }

func TestCapabilitiesSuccessNormalized(t *testing.T) {
	swapSeams(t, func(string) ([]byte, error) { return []byte("fakepe"), nil }, capaStat,
		func(_ context.Context, dir string, env []string, argv []string, _ int64) cliOutcome {
			if filepath.ToSlash(argv[0]) != "/runtime/bin/capa" {
				t.Fatalf("argv[0] = %q", argv[0])
			}
			joined := strings.Join(argv, " ")
			if !strings.Contains(joined, "--rules /assets/capa-rules") || !strings.Contains(joined, "--json") || !strings.HasSuffix(joined, "/input/0.bin") {
				t.Fatalf("argv = %v", argv)
			}
			if dir != tempRoot {
				t.Fatalf("working dir = %q", dir)
			}
			return cliOutcome{stdout: []byte(capaFixture)}
		})
	got := serveInvoke(t, invokeReq("file.capabilities", `{"fileId":"file:t"}`, capaAssetCtx(t, "file:t", "fakepe")))
	if got["type"] != "result" {
		t.Fatalf("%v", got)
	}
	out := got["output"].(map[string]any)
	if out["format"] != "pe" || out["capabilityCount"].(float64) != 3 || out["truncated"].(bool) {
		t.Fatalf("%v", out)
	}
	caps := out["capabilities"].([]any)
	first := caps[0].(map[string]any)
	if first["namespace"] != "anti-analysis/injection" || first["rule"] != "inject with APC" {
		t.Fatalf("not sorted by namespace: %v", caps[0])
	}
	attack := first["attack"].([]any)
	if len(attack) != 2 || attack[1].(map[string]any)["technique"] != "Process Injection: APC" {
		t.Fatalf("subtechnique should override technique: %v", attack)
	}
}

func TestCapabilitiesFailureMapping(t *testing.T) {
	read := func(string) ([]byte, error) { return []byte("fakepe"), nil }
	// Non-zero exit (e.g. unsupported format) must be an error, not an empty scan.
	swapSeams(t, read, capaStat, func(context.Context, string, []string, []string, int64) cliOutcome {
		return cliOutcome{stderr: []byte("capa: unsupported format"), err: errors.New("exit status 1")}
	})
	got := serveInvoke(t, invokeReq("file.capabilities", `{"fileId":"file:t"}`, capaAssetCtx(t, "file:t", "fakepe")))
	if got["type"] != "error" || got["error"].(map[string]any)["code"] != "TOOL_FAILED" {
		t.Fatalf("%v", got)
	}
	// Timeout.
	swapSeams(t, read, capaStat, func(context.Context, string, []string, []string, int64) cliOutcome {
		return cliOutcome{timedOut: true, err: context.DeadlineExceeded}
	})
	got = serveInvoke(t, invokeReq("file.capabilities", `{"fileId":"file:t"}`, capaAssetCtx(t, "file:t", "fakepe")))
	if code := got["error"].(map[string]any)["code"]; code != "TOOL_FAILED" ||
		!strings.Contains(got["error"].(map[string]any)["message"].(string), "timed out") {
		t.Fatalf("%v", got)
	}
	// Garbage stdout with exit 0 is not a successful scan.
	swapSeams(t, read, capaStat, func(context.Context, string, []string, []string, int64) cliOutcome {
		return cliOutcome{stdout: []byte("not json")}
	})
	got = serveInvoke(t, invokeReq("file.capabilities", `{"fileId":"file:t"}`, capaAssetCtx(t, "file:t", "fakepe")))
	if got["type"] != "error" {
		t.Fatalf("%v", got)
	}
	// Missing rules asset.
	swapSeams(t, read, func(string) (fs.FileInfo, error) { return nil, errors.New("gone") }, nil)
	got = serveInvoke(t, invokeReq("file.capabilities", `{"fileId":"file:t"}`, capaAssetCtx(t, "file:t", "fakepe")))
	if got["error"].(map[string]any)["code"] != "RESOURCE_UNAVAILABLE" {
		t.Fatalf("%v", got)
	}
	// Wrong asset mapping.
	swapSeams(t, read, capaStat, nil)
	ctx := capaAssetCtx(t, "file:t", "fakepe")
	ctx.Assets[0].AssetID = "other-rules"
	got = serveInvoke(t, invokeReq("file.capabilities", `{"fileId":"file:t"}`, ctx))
	if got["error"].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("%v", got)
	}
}

func TestNormalizeCapaBounds(t *testing.T) {
	doc, err := parseCapaDocument([]byte(capaFixture))
	if err != nil {
		t.Fatal(err)
	}
	caps, truncated := normalizeCapa(doc)
	if truncated || len(caps) != 3 {
		t.Fatalf("%v %v", len(caps), truncated)
	}
	// Over-limit rules are truncated and flagged.
	var b strings.Builder
	b.WriteString(`{"meta":{},"rules":{`)
	for i := 0; i < capaMaxCapabilities+10; i++ {
		fmt.Fprintf(&b, "%s\"rule-%d\": {\"meta\": {\"namespace\": \"n\"}}", map[bool]string{true: ",", false: ""}[i > 0], i)
	}
	b.WriteString("}}")
	doc, err = parseCapaDocument([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	caps, truncated = normalizeCapa(doc)
	if !truncated || len(caps) != capaMaxCapabilities {
		t.Fatalf("%v %v", len(caps), truncated)
	}
	if _, err := parseCapaDocument([]byte(`{"meta":{}}`)); err == nil {
		t.Fatal("document without rules must be rejected")
	}
}
