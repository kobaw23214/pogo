package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/skillruntime"
)

// setupFakeRuntime builds a minimal runtime tree: bin/ entrypoint path,
// jdk/bin/java placeholder and a Ghidra jar layout with Utility.jar.
func setupFakeRuntime(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	jars := []string{
		"ghidra/Ghidra/Features/Base/lib/Base.jar",
		"ghidra/Ghidra/Framework/Utility/lib/Utility.jar",
		"ghidra/Ghidra/Framework/SoftwareModeling/lib/SoftwareModeling.jar",
	}
	for _, j := range jars {
		p := filepath.Join(root, filepath.FromSlash(j))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("jar"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestGhidraClasspathUsesFixedBootstrap(t *testing.T) {
	root := setupFakeRuntime(t)
	cp, err := ghidraClasspath(root)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(cp, string(os.PathListSeparator))
	if len(parts) != 1 {
		t.Fatalf("%v", parts)
	}
	if !strings.HasSuffix(filepath.ToSlash(parts[0]), "Framework/Utility/lib/Utility.jar") {
		t.Fatalf("Utility.jar must come first: %v", parts)
	}
	if _, err := ghidraClasspath(t.TempDir()); err == nil {
		t.Fatal("empty ghidra tree must fail")
	}
}

func TestParseExportMarker(t *testing.T) {
	m := parseExportMarker([]byte("INFO  x\nMALAGENT_EXPORT_OK {\"tool\":\"ghidra\",\"program\":\"a\",\"language\":\"x86:LE:64:default\",\"executableFormat\":\"Portable Executable (PE)\",\"functionsTotal\":3,\"functionsExported\":2,\"functionsFailed\":1}\r\n"))
	if m == nil || m.FunctionsExported != 2 || m.ExecutableFormat != "Portable Executable (PE)" {
		t.Fatalf("%+v", m)
	}
	if parseExportMarker([]byte("MALAGENT_EXPORT_OK {bad json}")) != nil {
		t.Fatal("malformed marker accepted")
	}
	if parseExportMarker([]byte("INFO nothing here")) != nil {
		t.Fatal("phantom marker")
	}
}

func decompileCtx(t *testing.T, outDir string) *skillruntime.ProcessContext {
	t.Helper()
	ctx := fileCtx(t, "file:t", "MZ fake")
	ctx.OutputDirectory = &outDir
	return ctx
}

// readSampleThenDisk serves the staged sample for /input/ paths and falls back
// to the real filesystem for everything else (artifacts under the temp output
// directory must really be read back).
func readSampleThenDisk(p string) ([]byte, error) {
	if strings.HasPrefix(p, "/input/") {
		return []byte("MZ fake"), nil
	}
	return os.ReadFile(p)
}

// fakeGhidraRun emulates a successful headless run: the exporter writes both
// artifacts and prints the marker, exactly like PseudoCExporter.java.
func fakeGhidraRun(t *testing.T, exported, failed int) func(context.Context, string, []string, []string, int64) cliOutcome {
	return func(_ context.Context, dir string, env []string, argv []string, _ int64) cliOutcome {
		if filepath.Base(argv[0]) != "java" {
			t.Fatalf("argv[0] = %q, want the fixed jdk java", argv[0])
		}
		joined := strings.Join(argv, "\x00")
		if !strings.Contains(joined, "ghidra.app.util.headless.AnalyzeHeadless") ||
			!strings.Contains(joined, "ghidra.Ghidra\x00ghidra.app.util.headless.AnalyzeHeadless") ||
			!strings.Contains(joined, "-postScript\x00PseudoCExporter.java") ||
			!strings.Contains(joined, "-deleteProject") {
			t.Fatalf("argv = %v", argv)
		}
		foundEnv := false
		for _, e := range env {
			if strings.HasPrefix(e, "JAVA_TOOL_OPTIONS=") {
				foundEnv = true
			}
		}
		if !foundEnv || dir != tempRoot {
			t.Fatalf("env=%v dir=%q", env, dir)
		}
		var outC, manifestPath string
		for i, a := range argv {
			if a == "PseudoCExporter.java" {
				outC, manifestPath = argv[i+1], argv[i+2]
			}
		}
		m := exportManifest{
			Tool: "ghidra", Program: "0.bin", Language: "x86:LE:64:default",
			ExecutableFormat:  "Portable Executable (PE)",
			FunctionsTotal:    exported + failed,
			FunctionsExported: exported,
			FunctionsFailed:   failed,
		}
		mb, _ := json.Marshal(m)
		if err := os.WriteFile(outC, []byte("// ==== FUN_1 ====\nint f(void) { return 0; }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifestPath, mb, 0o644); err != nil {
			t.Fatal(err)
		}
		return cliOutcome{stdout: []byte("INFO  headless done\n" + exportMarkerPrefix + string(mb) + "\n")}
	}
}

func TestDecompileSuccess(t *testing.T) {
	root := setupFakeRuntime(t)
	swapSeams(t, readSampleThenDisk, nil, fakeGhidraRun(t, 2, 1))
	executablePath = func() (string, error) { return filepath.Join(root, "bin", "pogo-malagent-provider"), nil }
	outDir := t.TempDir()
	got := serveInvoke(t, invokeReq("file.decompile", `{"fileId":"file:t","timeoutSeconds":120}`, decompileCtx(t, outDir)))
	if got["type"] != "result" {
		t.Fatalf("%v", got)
	}
	out := got["output"].(map[string]any)
	if out["functionsRecovered"].(float64) != 2 || out["functionsTotal"].(float64) != 3 || out["functionsFailed"].(float64) != 1 {
		t.Fatalf("%v", out)
	}
	if out["language"] != "x86:LE:64:default" || out["format"] != "Portable Executable (PE)" {
		t.Fatalf("%v", out)
	}
	arts := got["artifacts"].([]any)
	if len(arts) != 2 {
		t.Fatalf("%v", arts)
	}
	a0 := arts[0].(map[string]any)
	if a0["name"] != "pseudo.c" || a0["path"] != "./pseudo.c" || a0["mediaType"] != "text/x-c" {
		t.Fatalf("artifact paths must be relative to the output directory: %v", a0)
	}
	a1 := arts[1].(map[string]any)
	if a1["name"] != "manifest.json" || a1["path"] != "./manifest.json" {
		t.Fatalf("%v", a1)
	}
}

func TestDecompileFailureModes(t *testing.T) {
	root := setupFakeRuntime(t)
	read := readSampleThenDisk
	newReq := func() *skillruntime.ProcessRequest {
		return invokeReq("file.decompile", `{"fileId":"file:t"}`, decompileCtx(t, t.TempDir()))
	}
	// Zero recovered functions is a failure even when the process exits 0.
	swapSeams(t, read, nil, fakeGhidraRun(t, 0, 0))
	executablePath = func() (string, error) { return filepath.Join(root, "bin", "pogo-malagent-provider"), nil }
	got := serveInvoke(t, newReq())
	if got["type"] != "error" || !strings.Contains(got["error"].(map[string]any)["message"].(string), "no functions") {
		t.Fatalf("%v", got)
	}
	// Missing exporter marker with exit 0.
	swapSeams(t, read, nil, func(context.Context, string, []string, []string, int64) cliOutcome {
		return cliOutcome{stdout: []byte("INFO done\n")}
	})
	executablePath = func() (string, error) { return filepath.Join(root, "bin", "pogo-malagent-provider"), nil }
	got = serveInvoke(t, newReq())
	if got["type"] != "error" || got["error"].(map[string]any)["code"] != "TOOL_FAILED" {
		t.Fatalf("%v", got)
	}
	// Timeout.
	swapSeams(t, read, nil, func(context.Context, string, []string, []string, int64) cliOutcome {
		return cliOutcome{timedOut: true, err: context.DeadlineExceeded}
	})
	executablePath = func() (string, error) { return filepath.Join(root, "bin", "pogo-malagent-provider"), nil }
	got = serveInvoke(t, newReq())
	if !strings.Contains(got["error"].(map[string]any)["message"].(string), "timed out") {
		t.Fatalf("%v", got)
	}
	// Exit 0 with marker but pseudo.c missing.
	swapSeams(t, read, nil, func(_ context.Context, _ string, _ []string, argv []string, _ int64) cliOutcome {
		m := exportManifest{Tool: "ghidra", Program: "x", Language: "x86:LE:64:default", ExecutableFormat: "PE", FunctionsTotal: 1, FunctionsExported: 1}
		mb, _ := json.Marshal(m)
		return cliOutcome{stdout: []byte(exportMarkerPrefix + string(mb) + "\n")}
	})
	executablePath = func() (string, error) { return filepath.Join(root, "bin", "pogo-malagent-provider"), nil }
	got = serveInvoke(t, newReq())
	if !strings.Contains(got["error"].(map[string]any)["message"].(string), "pseudo.c") {
		t.Fatalf("%v", got)
	}
	// Manifest contradicting the marker.
	swapSeams(t, read, nil, func(_ context.Context, _ string, _ []string, argv []string, _ int64) cliOutcome {
		var outC, manifestPath string
		for i, a := range argv {
			if a == "PseudoCExporter.java" {
				outC, manifestPath = argv[i+1], argv[i+2]
			}
		}
		m := exportManifest{Tool: "ghidra", Program: "x", Language: "x86:LE:64:default", ExecutableFormat: "PE", FunctionsTotal: 1, FunctionsExported: 1}
		mb, _ := json.Marshal(m)
		_ = os.WriteFile(outC, []byte("x"), 0o644)
		_ = os.WriteFile(manifestPath, []byte(`{"tool":"ghidra","program":"x","language":"MIPS","executableFormat":"PE","functionsTotal":9,"functionsExported":9,"functionsFailed":0}`), 0o644)
		return cliOutcome{stdout: []byte(exportMarkerPrefix + string(mb) + "\n")}
	})
	executablePath = func() (string, error) { return filepath.Join(root, "bin", "pogo-malagent-provider"), nil }
	got = serveInvoke(t, newReq())
	if !strings.Contains(got["error"].(map[string]any)["message"].(string), "does not match") {
		t.Fatalf("%v", got)
	}
	// No output directory grant.
	swapSeams(t, read, nil, nil)
	executablePath = func() (string, error) { return filepath.Join(root, "bin", "pogo-malagent-provider"), nil }
	ctx := fileCtx(t, "file:t", "MZ fake")
	got = serveInvoke(t, invokeReq("file.decompile", `{"fileId":"file:t"}`, ctx))
	if got["error"].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("%v", got)
	}
	// timeoutSeconds outside the contract bounds.
	got = serveInvoke(t, invokeReq("file.decompile", `{"fileId":"file:t","timeoutSeconds":30}`, decompileCtx(t, t.TempDir())))
	if got["error"].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("%v", got)
	}
	// Process failure surfaces stderr.
	swapSeams(t, read, nil, func(context.Context, string, []string, []string, int64) cliOutcome {
		return cliOutcome{stderr: []byte("java.lang.OutOfMemoryError"), err: errors.New("exit status 1")}
	})
	executablePath = func() (string, error) { return filepath.Join(root, "bin", "pogo-malagent-provider"), nil }
	got = serveInvoke(t, newReq())
	if !strings.Contains(got["error"].(map[string]any)["message"].(string), "OutOfMemoryError") {
		t.Fatalf("%v", got)
	}
}

func TestVerifyArtifactFileRejectsNonRegular(t *testing.T) {
	dir := t.TempDir()
	swapSeams(t, nil, func(p string) (fs.FileInfo, error) {
		return fakeFileInfo{name: filepath.Base(p), regular: false}, nil
	}, nil)
	if _, ferr := verifyArtifactFile(dir, "pseudo.c", 1024); ferr == nil || !strings.Contains(ferr.msg, "regular") {
		t.Fatalf("%v", ferr)
	}
}
