package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yosukeno/pogo/skillruntime"
)

// handleDecompile implements malware.file.decompile with the pinned Ghidra +
// JDK bundle. The Go entrypoint drives java directly (fixed main class, fixed
// classpath from the verified ghidra tree) — no shell launcher anywhere in the
// chain. The post-script is the fixed PseudoCExporter.java shipped in the
// runtime bundle. Success requires the exporter's JSON marker, at least one
// recovered function and verified artifacts; a bare exit code of 0 is never
// treated as success. Samples are only imported for static analysis, never
// executed.

const (
	ghidraDefaultTimeout = 300
	ghidraMinTimeout     = 60
	ghidraMaxTimeout     = 900
	ghidraPseudoCMax     = 8 << 20
	ghidraManifestMax    = 64 << 10
	ghidraLogCaptureMax  = 1 << 20

	exportMarkerPrefix = "MALAGENT_EXPORT_OK "
	pseudoCName        = "pseudo.c"
	manifestName       = "manifest.json"
)

// exportManifest is written by PseudoCExporter.java both as the marker line on
// stdout and as manifest.json in the output directory.
type exportManifest struct {
	Tool              string `json:"tool"`
	Program           string `json:"program"`
	Language          string `json:"language"`
	ExecutableFormat  string `json:"executableFormat"`
	FunctionsTotal    int    `json:"functionsTotal"`
	FunctionsExported int    `json:"functionsExported"`
	FunctionsFailed   int    `json:"functionsFailed"`
}

func handleDecompile(req *skillruntime.ProcessRequest, raw json.RawMessage) (any, []artifact, *failErr) {
	var in struct {
		FileID         string `json:"fileId"`
		SampleSHA256   string `json:"sampleSha256"`
		TimeoutSeconds *int   `json:"timeoutSeconds"`
	}
	if err := decodeTypedStrict(raw, &in); err != nil {
		return nil, nil, fail("INVALID_INPUT", "input does not match the contract: %v", err)
	}
	timeout := ghidraDefaultTimeout
	if in.TimeoutSeconds != nil {
		if *in.TimeoutSeconds < ghidraMinTimeout || *in.TimeoutSeconds > ghidraMaxTimeout {
			return nil, nil, fail("INVALID_INPUT", "timeoutSeconds must be within [%d, %d]", ghidraMinTimeout, ghidraMaxTimeout)
		}
		timeout = *in.TimeoutSeconds
	}
	inputPath, _, ferr := inputFile(req, in.FileID, in.SampleSHA256)
	if ferr != nil {
		return nil, nil, ferr
	}
	outDir, ferr := grantedOutputDir(req)
	if ferr != nil {
		return nil, nil, ferr
	}
	root, err := runtimeRoot()
	if err != nil {
		return nil, nil, fail("RESOURCE_UNAVAILABLE", "tool runtime root is unavailable")
	}
	classpath, err := ghidraClasspath(root)
	if err != nil {
		return nil, nil, fail("RESOURCE_UNAVAILABLE", "ghidra runtime is incomplete: %v", err)
	}
	// Project workspace lives on the scratch tmpfs only; the output directory
	// receives exactly the two declared artifacts.
	proj, err := mkdirTemp(tempRoot, "ghidra-proj-")
	if err != nil {
		return nil, nil, fail("RESOURCE_UNAVAILABLE", "scratch space is unavailable")
	}
	defer removeAll(proj)
	outC := filepath.Join(outDir, pseudoCName)
	manifestPath := filepath.Join(outDir, manifestName)
	argv := []string{
		filepath.Join(root, "jdk", "bin", "java"),
		"-Djava.awt.headless=true",
		"-Duser.home=" + tempRoot,
		"-Djava.io.tmpdir=" + tempRoot,
		"-Xmx1536m",
		"-XX:ParallelGCThreads=2", "-XX:CICompilerCount=2",
		"-Djava.system.class.loader=ghidra.GhidraClassLoader",
		"-Dfile.encoding=UTF8", "-Xshare:off", "--enable-native-access=ALL-UNNAMED",
		"-cp", classpath,
		"ghidra.Ghidra",
		"ghidra.app.util.headless.AnalyzeHeadless",
		proj, "pogo",
		"-import", inputPath,
		"-scriptPath", filepath.Join(root, "ghidra-scripts"),
		"-postScript", "PseudoCExporter.java", outC, manifestPath,
		"-deleteProject",
		"-analysisTimeoutPerFile", strconv.Itoa(timeout),
	}
	env := toolEnv("JAVA_TOOL_OPTIONS=-Duser.home=" + tempRoot)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()
	res := runCLI(ctx, tempRoot, env, argv, ghidraLogCaptureMax)
	if res.timedOut {
		return nil, nil, fail("TOOL_FAILED", "ghidra analysis timed out after %ds", timeout)
	}
	if res.err != nil {
		detail := clipString(string(res.stderr), 300)
		if detail == "" {
			detail = clipString(string(res.stdout), 300)
		}
		return nil, nil, fail("TOOL_FAILED", "ghidra headless failed: %s", detail)
	}
	if res.overflow {
		return nil, nil, fail("TOOL_FAILED", "ghidra diagnostic output exceeded its limit")
	}
	logs := strings.ToLower(string(res.stdout) + "\n" + string(res.stderr))
	if strings.Contains(logs, "analysis timed out") || strings.Contains(logs, "analysis timeout occurred") {
		return nil, nil, fail("TOOL_FAILED", "ghidra analysis did not complete within its budget")
	}
	marker := parseExportMarker(res.stdout)
	if marker == nil {
		return nil, nil, fail("TOOL_FAILED", "ghidra export did not complete: exporter marker missing")
	}
	if marker.FunctionsExported <= 0 {
		return nil, nil, fail("TOOL_FAILED", "analysis recovered no functions; the input may be packed, corrupted or unsupported")
	}
	if strings.TrimSpace(marker.Language) == "" {
		return nil, nil, fail("TOOL_FAILED", "ghidra export reported an empty language id")
	}
	pseudoCBytes, ferr := verifyArtifactFile(outDir, pseudoCName, ghidraPseudoCMax)
	if ferr != nil {
		return nil, nil, ferr
	}
	if _, ferr := verifyArtifactFile(outDir, manifestName, ghidraManifestMax); ferr != nil {
		return nil, nil, ferr
	}
	written, err := readFile(manifestPath)
	if err != nil {
		return nil, nil, fail("TOOL_FAILED", "export manifest is unreadable")
	}
	var diskManifest exportManifest
	if err := decodeTypedStrict(written, &diskManifest); err != nil {
		return nil, nil, fail("TOOL_FAILED", "export manifest is malformed")
	}
	if diskManifest.FunctionsExported != marker.FunctionsExported ||
		diskManifest.Language != marker.Language || diskManifest.Tool != "ghidra" {
		return nil, nil, fail("TOOL_FAILED", "export manifest does not match the exporter report")
	}
	preview, err := readFile(outC)
	if err != nil {
		return nil, nil, fail("TOOL_FAILED", "pseudo C preview is unreadable")
	}
	truncated := len(preview) > 64<<10
	if truncated {
		preview = preview[:64<<10]
	}
	format := clipString(marker.ExecutableFormat, 128)
	if format == "" {
		// Raw/unknown images decompile fine but have no format string; the
		// contract requires a non-empty label.
		format = "unrecognized"
	}
	return map[string]any{
			"language":           clipString(marker.Language, 128),
			"format":             format,
			"functionsRecovered": marker.FunctionsExported,
			"functionsTotal":     marker.FunctionsTotal,
			"functionsFailed":    marker.FunctionsFailed,
			"pseudoCBytes":       pseudoCBytes,
			"pseudoCPreview":     string(preview),
			"previewTruncated":   truncated,
		}, []artifact{
			{Name: pseudoCName, Path: "./" + pseudoCName, MediaType: "text/x-c"},
			{Name: manifestName, Path: "./" + manifestName, MediaType: "application/json"},
		}, nil
}

// grantedOutputDir validates the host-granted output directory: it must be
// present (the contract declares writesArtifacts), absolute and an existing
// directory. The provider only ever writes the two fixed artifact names into
// it and reports them as relative ./ paths.
func grantedOutputDir(req *skillruntime.ProcessRequest) (string, *failErr) {
	if req.Context.OutputDirectory == nil || *req.Context.OutputDirectory == "" {
		return "", fail("INVALID_INPUT", "an output directory grant is required for decompilation artifacts")
	}
	dir := filepath.Clean(*req.Context.OutputDirectory)
	if !filepath.IsAbs(dir) {
		return "", fail("INVALID_INPUT", "output directory must be an absolute host-granted path")
	}
	info, err := statFile(dir)
	if err != nil || !info.IsDir() {
		return "", fail("RESOURCE_UNAVAILABLE", "granted output directory is unavailable")
	}
	return dir, nil
}

// verifyArtifactFile proves one declared artifact is a real, non-empty,
// size-bounded regular file directly inside the granted output directory.
func verifyArtifactFile(dir, name string, maxBytes int64) (int64, *failErr) {
	p := filepath.Join(dir, name)
	if filepath.Dir(p) != dir {
		return 0, fail("TOOL_FAILED", "artifact path escapes the output directory")
	}
	info, err := statFile(p)
	if err != nil {
		return 0, fail("TOOL_FAILED", "expected artifact %s was not produced", name)
	}
	if !info.Mode().IsRegular() {
		return 0, fail("TOOL_FAILED", "artifact %s is not a regular file", name)
	}
	if info.Size() <= 0 {
		return 0, fail("TOOL_FAILED", "artifact %s is empty", name)
	}
	if info.Size() > maxBytes {
		return 0, fail("TOOL_FAILED", "artifact %s exceeds the %d byte limit", name, maxBytes)
	}
	return info.Size(), nil
}

// parseExportMarker finds the exporter's JSON report line in headless stdout.
func parseExportMarker(stdout []byte) *exportManifest {
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, exportMarkerPrefix) {
			continue
		}
		var m exportManifest
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, exportMarkerPrefix)), &m); err == nil {
			return &m
		}
	}
	return nil
}

// Ghidra's fixed bootstrap initializes its application layout and module
// loader. AnalyzeHeadless is a GhidraLaunchable, not a Java main class.
func ghidraClasspath(root string) (string, error) {
	bootstrap := filepath.Join(root, "ghidra", "Ghidra", "Framework", "Utility", "lib", "Utility.jar")
	info, err := os.Stat(bootstrap)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("Ghidra bootstrap Utility.jar is unavailable")
	}
	return bootstrap, nil
}
