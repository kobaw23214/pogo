package core

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/internal/testfs"
)

// --- synthetic package fixtures -------------------------------------------

// zipEnt is one entry for writeZip.
type zipEnt struct {
	name string
	data []byte
	mode os.FileMode // 0 leaves the default; 0o755 marks an executable
}

// writeZip builds an in-memory ZIP. Entries are written in order; duplicate
// names are intentionally possible for hostile fixtures.
func writeZip(t *testing.T, entries []zipEnt) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatalf("zip create %q: %v", e.name, err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatalf("zip write %q: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// contractJSON renders a small capability contract.
func contractJSON(id string, withFile bool) []byte {
	fileInputs := "[]"
	reads := "false"
	writes := "false"
	props := `"query": {"type": "string", "minLength": 1}`
	required := `"query"`
	if withFile {
		fileInputs = `["/fileId"]`
		reads = "true"
		writes = "true"
		props = `"fileId": {"type": "string", "minLength": 1}`
		required = `"fileId"`
	}
	doc := fmt.Sprintf(`{
  "apiVersion": "pogo.skill/v1",
  "kind": "CapabilityContract",
  "id": "%s",
  "version": "1.0.0",
  "description": "synthetic test contract",
  "inputSchema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "properties": {%s},
    "required": [%s],
    "additionalProperties": false
  },
  "outputSchema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "properties": {"ok": {"type": "boolean"}},
    "required": ["ok"],
    "additionalProperties": false
  },
  "fileInputs": %s,
  "effects": {"readsUserFiles": %s, "writesArtifacts": %s, "mutatesExternalState": false},
  "retry": "never-automatic"
}`, id, props, required, fileInputs, reads, writes)
	return []byte(doc)
}

// runtimeZip is a synthetic, harmless native payload: a small executable
// marker file plus one data file. No real tool is executed anywhere.
func runtimeZip(t *testing.T) []byte {
	return writeZip(t, []zipEnt{
		{name: "bin/tool", data: []byte("#!/bin/true\n# synthetic fixture, never executed\n"), mode: 0o755},
		{name: "lib/data.txt", data: []byte("synthetic runtime data\n"), mode: 0o644},
	})
}

func assetZip(t *testing.T) []byte {
	return writeZip(t, []zipEnt{
		{name: "rules/demo.rule", data: []byte("rule demo { condition: false }\n"), mode: 0o644},
	})
}

// buildDemoManifest renders the manifest of the synthetic local pkg and
// returns it together with the contract documents.
func buildDemoManifest(t *testing.T, runtimeArtifact, assetArtifact Artifact) (manifest []byte, contractA, contractB []byte) {
	t.Helper()
	contractA = contractJSON("test.file.inspect", true)
	contractB = contractJSON("test.index.query", false)
	doc := fmt.Sprintf(`{
  "apiVersion": "pogo.skill/v1",
  "kind": "SkillRuntime",
  "id": "org.test.demo",
  "version": "1.0.0",
  "displayName": "Synthetic demo pkg",
  "requiredFeatures": ["bundle.v1", "process-json.v1", "isolated-execution.v1"],
  "contracts": [
    {"ref": {"id": "test.file.inspect", "version": "1.0.0", "digest": "%s"}, "path": "./contracts/inspect.json"},
    {"ref": {"id": "test.index.query", "version": "1.0.0", "digest": "%s"}, "path": "./contracts/query.json"}
  ],
  "requiresCapabilities": [
    {"id": "test.file.inspect", "version": "1.0.0", "digest": "%s"},
    {"id": "test.index.query", "version": "1.0.0", "digest": "%s"}
  ],
  "runtimes": [
    {"id": "rt", "driver": "bundle",
     "artifact": {"path": "%s", "digest": "%s", "sizeBytes": %d, "format": "%s"},
     "platform": {"os": "linux", "arch": "amd64", "libc": "glibc", "minLibcVersion": "2.36"},
     "requiresIsolation": ["filesystem", "network", "process-limits"]}
  ],
  "assets": [
    {"id": "rules", "artifact": {"path": "%s", "digest": "%s", "sizeBytes": %d, "format": "%s"}}
  ],
  "resourceSlots": [
    {"id": "samples", "kind": "files", "description": "session files"},
    {"id": "results", "kind": "artifacts", "description": "call artifacts"}
  ],
  "providers": [
    {"id": "inspect", "implements": {"id": "test.file.inspect", "version": "1.0.0", "digest": "%s"},
     "driver": "process-json", "runtimeId": "rt", "entrypoint": "./bin/tool", "operation": "inspect",
     "resourceSlots": ["samples", "results"], "assetIds": ["rules"],
     "requests": {"network": "none", "limits": {"timeoutSeconds": 60, "memoryMiB": 512, "cpuMilliCores": 1000, "maxOutputBytes": 1048576}}},
    {"id": "query", "implements": {"id": "test.index.query", "version": "1.0.0", "digest": "%s"},
     "driver": "process-json", "runtimeId": "rt", "entrypoint": "./bin/tool", "operation": "query",
     "resourceSlots": [], "assetIds": [],
     "requests": {"network": "none", "limits": {"timeoutSeconds": 30, "memoryMiB": 256, "cpuMilliCores": 500, "maxOutputBytes": 524288}}}
  ]
}`,
		DigestBytes(contractA), DigestBytes(contractB),
		DigestBytes(contractA), DigestBytes(contractB),
		runtimeArtifact.Path, runtimeArtifact.Digest, runtimeArtifact.SizeBytes, runtimeArtifact.Format,
		assetArtifact.Path, assetArtifact.Digest, assetArtifact.SizeBytes, assetArtifact.Format,
		DigestBytes(contractA), DigestBytes(contractB))
	return []byte(doc), contractA, contractB
}

// buildMCPManifest renders the manifest of the synthetic MCP pkg.
func buildMCPManifest(t *testing.T, snapshot []byte, contractB []byte) []byte {
	t.Helper()
	doc := fmt.Sprintf(`{
  "apiVersion": "pogo.skill/v1",
  "kind": "SkillRuntime",
  "id": "org.test.mcp",
  "version": "1.0.0",
  "displayName": "Synthetic MCP pkg",
  "requiredFeatures": ["mcp.v1"],
  "contracts": [
    {"ref": {"id": "test.index.query", "version": "1.0.0", "digest": "%s"}, "path": "./contracts/query.json"}
  ],
  "requiresCapabilities": [{"id": "test.index.query", "version": "1.0.0", "digest": "%s"}],
  "runtimes": [],
  "assets": [],
  "resourceSlots": [
    {"id": "connection", "kind": "mcp-connection", "description": "admin-managed MCP connection"}
  ],
  "providers": [
    {"id": "remote-query", "implements": {"id": "test.index.query", "version": "1.0.0", "digest": "%s"},
     "driver": "mcp", "connectionSlot": "connection", "toolName": "index_query",
     "inputMapping": "identity", "resultMapping": "structuredContent",
     "schemaSnapshot": {"path": "./snapshots/query.json", "digest": "%s"},
     "timeoutSeconds": 60, "maxOutputBytes": 1048576}
  ]
}`, DigestBytes(contractB), DigestBytes(contractB), DigestBytes(contractB), DigestBytes(snapshot))
	return []byte(doc)
}

// buildDemoOuterZip assembles the full distribution archive of the demo
// pkg. opts can mutate the entries before zipping (for hostile variants).
func buildDemoOuterZip(t *testing.T, mutate func(entries []zipEnt) []zipEnt) ([]byte, []byte) {
	t.Helper()
	rtZip := runtimeZip(t)
	asZip := assetZip(t)
	rtArt := Artifact{Path: "./payload/rt.zip", Digest: DigestBytes(rtZip), SizeBytes: int64(len(rtZip)), Format: "zip"}
	asArt := Artifact{Path: "./payload/rules.zip", Digest: DigestBytes(asZip), SizeBytes: int64(len(asZip)), Format: "zip"}
	manifest, contractA, contractB := buildDemoManifest(t, rtArt, asArt)
	entries := []zipEnt{
		{name: ManifestFileName, data: manifest, mode: 0o644},
		{name: "contracts/inspect.json", data: contractA, mode: 0o644},
		{name: "contracts/query.json", data: contractB, mode: 0o644},
		{name: "SKILL.md", data: []byte("---\nname: analysis\ndescription: Synthetic file analysis.\n---\n# synthetic skill\n"), mode: 0o644},
		{name: "payload/rt.zip", data: rtZip, mode: 0o644},
		{name: "payload/rules.zip", data: asZip, mode: 0o644},
	}
	if mutate != nil {
		entries = mutate(entries)
	}
	return writeZip(t, entries), manifest
}

// installDemo installs the synthetic demo pkg into a fresh store.
func installDemo(t *testing.T) (*Store, *Package, []byte) {
	t.Helper()
	archive, _ := buildDemoOuterZip(t, nil)
	dir := testfs.SealedTempDir(t)
	archivePath := filepath.Join(dir, "demo.zip")
	if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := store.InstallZIP(archivePath)
	if err != nil {
		t.Fatalf("InstallZIP: %v", err)
	}
	return store, pkg, archive
}

// --- tests -----------------------------------------------------------------

func TestInstallAndReload(t *testing.T) {
	store, pkg, archive := installDemo(t)

	if pkg.Manifest.ID != "org.test.demo" || pkg.Manifest.Version != "1.0.0" {
		t.Fatalf("unexpected manifest identity %s@%s", pkg.Manifest.ID, pkg.Manifest.Version)
	}
	if pkg.PackageDigest != DigestBytes(archive) {
		t.Fatalf("package digest not derived from the exact outer archive bytes")
	}
	if len(pkg.Contracts) != 2 {
		t.Fatalf("expected 2 contracts, got %d", len(pkg.Contracts))
	}

	// Reload through the exact ref.
	reloaded, err := store.Load(pkg.ref())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.ManifestDigest != pkg.ManifestDigest || reloaded.PackageDigest != pkg.PackageDigest {
		t.Fatalf("reloaded digests differ")
	}

	// Extracted runtime/asset paths resolve and contain the payload files.
	rtPath, err := reloaded.RuntimePath("rt")
	if err != nil {
		t.Fatalf("RuntimePath: %v", err)
	}
	tool := filepath.Join(rtPath, "bin", "tool")
	fi, err := os.Stat(tool)
	if err != nil {
		t.Fatalf("entrypoint missing in extracted runtime: %v", err)
	}
	asPath, err := reloaded.AssetPath("rules")
	if err != nil {
		t.Fatalf("AssetPath: %v", err)
	}
	if _, err := os.Stat(filepath.Join(asPath, "rules", "demo.rule")); err != nil {
		t.Fatalf("asset payload missing: %v", err)
	}

	if runtime.GOOS != "windows" {
		if fi.Mode().Perm()&0o100 == 0 {
			t.Errorf("executable bit not preserved on %s", tool)
		}
		data := filepath.Join(rtPath, "lib", "data.txt")
		dfi, err := os.Stat(data)
		if err != nil {
			t.Fatal(err)
		}
		if dfi.Mode().Perm() != 0o444 {
			t.Errorf("installed data file not read-only: mode %v", dfi.Mode().Perm())
		}
		if fi.Mode().Perm() != 0o555 {
			t.Errorf("installed executable mode = %v, want 0555", fi.Mode().Perm())
		}
	}

	// Unknown ids and a LoadPackage-loaded package behave sanely.
	if _, err := reloaded.RuntimePath("nope"); err == nil {
		t.Errorf("RuntimePath accepted unknown runtime id")
	}
}

func TestReinstallIdenticalIsNoOp(t *testing.T) {
	store, pkg, archive := installDemo(t)
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "demo.zip")
	if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := store.InstallZIP(archivePath)
	if err != nil {
		t.Fatalf("reinstall of identical bytes: %v", err)
	}
	if again.PackageDigest != pkg.PackageDigest {
		t.Fatalf("digest changed on identical reinstall")
	}
}

func TestImmutableVersionCollision(t *testing.T) {
	store, pkg, _ := installDemo(t)
	// Same id+version, different content (extra harmless file).
	archive2, _ := buildDemoOuterZip(t, func(entries []zipEnt) []zipEnt {
		return append(entries, zipEnt{name: "README.md", data: []byte("changed\n"), mode: 0o644})
	})
	dir := t.TempDir()
	p2 := filepath.Join(dir, "demo2.zip")
	if err := os.WriteFile(p2, archive2, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := store.InstallZIP(p2)
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("expected ErrVersionConflict, got %v", err)
	}
	// The installed version is intact and loadable.
	if _, err := store.Load(pkg.ref()); err != nil {
		t.Fatalf("installed version damaged by conflicting install: %v", err)
	}
}

func TestTamperedContractDigest(t *testing.T) {
	archive, _ := buildDemoOuterZip(t, func(entries []zipEnt) []zipEnt {
		for i := range entries {
			if entries[i].name == "contracts/inspect.json" {
				entries[i].data = append(entries[i].data, '\n') // reformat -> new digest
			}
		}
		return entries
	})
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.zip")
	if err := os.WriteFile(p, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.InstallZIP(p)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("tampered contract not detected: %v", err)
	}
}

func TestIncompletePayloadRejected(t *testing.T) {
	archive, _ := buildDemoOuterZip(t, func(entries []zipEnt) []zipEnt {
		var out []zipEnt
		for _, e := range entries {
			if e.name == "payload/rt.zip" {
				continue // drop the runtime payload
			}
			out = append(out, e)
		}
		return out
	})
	dir := t.TempDir()
	p := filepath.Join(dir, "incomplete.zip")
	if err := os.WriteFile(p, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.InstallZIP(p)
	if err == nil || !strings.Contains(err.Error(), "payload") {
		t.Fatalf("missing payload not detected: %v", err)
	}
}

func TestTarZstExplicitlyUnsupported(t *testing.T) {
	fakeTar := []byte("pretend this is tar.zst; never decompressed\n")
	rtArt := Artifact{Path: "./payload/rt.tar.zst", Digest: DigestBytes(fakeTar), SizeBytes: int64(len(fakeTar)), Format: "tar.zst"}
	asZip := assetZip(t)
	asArt := Artifact{Path: "./payload/rules.zip", Digest: DigestBytes(asZip), SizeBytes: int64(len(asZip)), Format: "zip"}
	manifest, contractA, contractB := buildDemoManifest(t, rtArt, asArt)
	archive := writeZip(t, []zipEnt{
		{name: ManifestFileName, data: manifest, mode: 0o644},
		{name: "contracts/inspect.json", data: contractA, mode: 0o644},
		{name: "contracts/query.json", data: contractB, mode: 0o644},
		{name: "SKILL.md", data: []byte("---\nname: analysis\ndescription: Synthetic file analysis.\n---\n# synthetic skill\n"), mode: 0o644},
		{name: "payload/rt.tar.zst", data: fakeTar, mode: 0o644},
		{name: "payload/rules.zip", data: asZip, mode: 0o644},
	})
	dir := t.TempDir()
	p := filepath.Join(dir, "tarzst.zip")
	if err := os.WriteFile(p, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.InstallZIP(p)
	if !errors.Is(err, ErrUnsupportedArchiveFormat) {
		t.Fatalf("expected ErrUnsupportedArchiveFormat, got %v", err)
	}
}

func TestMaliciousArchivesRejected(t *testing.T) {
	good := func() []zipEnt {
		return []zipEnt{{name: "ok.txt", data: []byte("ok"), mode: 0o644}}
	}
	deepName := strings.Repeat("a/", MaxArchiveDepth) + "x.txt"

	cases := []struct {
		name    string
		entries []zipEnt
		want    string
	}{
		{"parent traversal", append(good(), zipEnt{name: "../evil.txt", data: []byte("x")}), "dot segment"},
		{"nested traversal", append(good(), zipEnt{name: "a/../../evil.txt", data: []byte("x")}), "dot segment"},
		{"absolute path", append(good(), zipEnt{name: "/etc/passwd", data: []byte("x")}), "absolute"},
		{"drive letter", append(good(), zipEnt{name: "C:/evil.txt", data: []byte("x")}), "drive letter"},
		{"backslash", append(good(), zipEnt{name: `a\..\evil.txt`, data: []byte("x")}), "backslash"},
		{"invalid utf8", append(good(), zipEnt{name: string([]byte{'x', 0xff, 0xfe, '.', 't', 'x', 't'}), data: []byte("x")}), "UTF-8"},
		{"duplicate name", append(good(), zipEnt{name: "ok.txt", data: []byte("y")}), "duplicate"},
		{"case collision", append(good(), zipEnt{name: "OK.TXT", data: []byte("y")}), "case folding"},
		{"symlink", append(good(), zipEnt{name: "link.txt", data: []byte("/etc/passwd"), mode: os.ModeSymlink | 0o777}), "symlink"},
		{"too deep", append(good(), zipEnt{name: deepName, data: []byte("x")}), "depth"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := writeZip(t, tc.entries)
			budget := &zipBudget{}
			dest := t.TempDir()
			_, err := extractZIP(archive, dest, budget, "test")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected rejection containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestZipBombRatioRejected(t *testing.T) {
	// 17 MiB of zeros compress far beyond the allowed ratio.
	zeros := make([]byte, 17<<20)
	archive := writeZip(t, []zipEnt{{name: "zeros.bin", data: zeros, mode: 0o644}})
	budget := &zipBudget{}
	_, err := extractZIP(archive, t.TempDir(), budget, "test")
	if err == nil || !strings.Contains(err.Error(), "compression ratio") {
		t.Fatalf("zip bomb not rejected: %v", err)
	}
}

func TestFailedInstallRollback(t *testing.T) {
	store, pkg, _ := installDemo(t)

	// A broken archive for a different pkg id must not damage the store.
	archive, _ := buildDemoOuterZip(t, func(entries []zipEnt) []zipEnt {
		for i := range entries {
			if entries[i].name == "payload/rules.zip" {
				entries[i].data = append(entries[i].data, 0x00) // corrupt payload bytes
			}
		}
		return entries
	})
	dir := t.TempDir()
	p := filepath.Join(dir, "broken.zip")
	if err := os.WriteFile(p, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InstallZIP(p); err == nil {
		t.Fatal("broken archive installed successfully")
	}
	// Old version intact.
	if _, err := store.Load(pkg.ref()); err != nil {
		t.Fatalf("installed version damaged by failed install: %v", err)
	}
	// No staging leftovers.
	leftovers, err := filepath.Glob(filepath.Join(store.root, ".staging-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("staging leftovers remain: %v", leftovers)
	}
}

func TestLoadDetectsTamperedInstall(t *testing.T) {
	store, pkg, _ := installDemo(t)
	rtPath, err := pkg.RuntimePath("rt")
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(rtPath, "lib", "data.txt")
	// Installed files are read-only where the OS supports it; make writable first.
	_ = os.Chmod(data, 0o644)
	if err := os.WriteFile(data, []byte("tampered\n"), 0o644); err != nil {
		t.Fatalf("tamper setup: %v", err)
	}
	if _, err := store.Load(pkg.ref()); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("tampered installed content not detected: %v", err)
	}
}

func TestLoadPackageOnUnpackedDistribution(t *testing.T) {
	// LoadPackage works on a plain directory (no store) when all payloads
	// are present and consistent.
	archive, _ := buildDemoOuterZip(t, nil)
	dir := t.TempDir()
	budget := &zipBudget{}
	if _, err := extractZIP(archive, dir, budget, "outer"); err != nil {
		t.Fatal(err)
	}
	pkg, err := LoadPackage(dir)
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	if pkg.PackageDigest != "" {
		t.Fatalf("LoadPackage must not invent a package digest")
	}
	if _, err := pkg.RuntimePath("rt"); err == nil {
		t.Fatalf("RuntimePath must fail before store installation extracts payloads")
	}
}
