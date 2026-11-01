package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/skillruntime"
)

// ---- read-only SQL whitelist (ported semantics from malquery sqlquery.go) ----

func TestAssertReadOnlyQuery(t *testing.T) {
	ok := []string{
		"SELECT 1",
		"select * from samples",
		"  WITH x AS (SELECT 1) SELECT * FROM x  ",
		"SELECT count(*) FROM samples;", // trailing semicolon tolerated
	}
	for _, q := range ok {
		if err := assertReadOnlyQuery(q); err != nil {
			t.Errorf("assertReadOnlyQuery(%q) = %v, want nil", q, err)
		}
	}
	bad := []string{
		"DELETE FROM samples",
		"PRAGMA writable_schema = 1",
		"ATTACH DATABASE '/tmp/x' AS x",
		"SELECT 1; DELETE FROM samples",
		"INSERT INTO samples VALUES ('x')",
		"  ",
	}
	for _, q := range bad {
		if err := assertReadOnlyQuery(q); err == nil {
			t.Errorf("assertReadOnlyQuery(%q) = nil, want error", q)
		}
	}
}

// ---- argv allowlist builders ----

func TestBuildSearchArgs(t *testing.T) {
	maxDet := 0
	limit := 5
	args := buildSearchArgs(searchInput{Family: "Qakbot", Type: "banker", Tag: "http", HasBinary: true, MaxDetections: &maxDet, Limit: &limit}, limit)
	want := "search --family Qakbot --type banker --tag http --max-detections 0 --has-binary -n 5"
	if strings.Join(args, " ") != want {
		t.Fatalf("%v", args)
	}
	if got := strings.Join(buildSearchArgs(searchInput{}, 20), " "); got != "search -n 20" {
		t.Fatalf("%v", got)
	}
}

func TestBuildFamilyArgs(t *testing.T) {
	days := 30
	args, ferr := buildFamilyArgs(familyInput{Name: "njrat", IncludeTrend: true, Metric: "propagation", Days: &days})
	if ferr != nil || strings.Join(args, " ") != "family njrat --trend --metric propagation --days 30" {
		t.Fatalf("%v %v", args, ferr)
	}
	args, ferr = buildFamilyArgs(familyInput{})
	if ferr != nil || strings.Join(args, " ") != "family" {
		t.Fatalf("%v %v", args, ferr)
	}
	if _, ferr = buildFamilyArgs(familyInput{IncludeGeo: true}); ferr == nil {
		t.Fatal("sections without a family name must be rejected")
	}
}

// ---- dataset grant verification ----

func assetStatOK(string) (fs.FileInfo, error) { return fakeFileInfo{regular: true, size: 4096}, nil }

func TestAssetGrantStrictness(t *testing.T) {
	// Valid grant passes.
	swapSeams(t, nil, assetStatOK, nil)
	if _, ferr := sampleIndexDB(invokeReq("library.stats", `{}`, assetCtx())); ferr != nil {
		t.Fatal(ferr.msg)
	}
	mutate := func(f func(*skillruntime.ProcessAsset)) *failErr {
		ctx := assetCtx()
		f(&ctx.Assets[0])
		_, ferr := sampleIndexDB(invokeReq("library.stats", `{}`, ctx))
		return ferr
	}
	if ferr := mutate(func(d *skillruntime.ProcessAsset) { d.AssetID = "other" }); ferr == nil || ferr.code != "INVALID_INPUT" {
		t.Fatal("foreign slot accepted")
	}
	if ferr := mutate(func(d *skillruntime.ProcessAsset) { d.Path = "/tmp/evil.db" }); ferr == nil || ferr.code != "INVALID_INPUT" {
		t.Fatal("user-supplied database path accepted")
	}
	if ferr := mutate(func(d *skillruntime.ProcessAsset) { d.Digest = "" }); ferr == nil || ferr.code != "INVALID_INPUT" {
		t.Fatal("missing revision accepted")
	}
	// Unknown dataset fields are rejected at frame decode, before any handler.
	frame := `{"protocolVersion":"pogo.tool/1","callId":"call:x","type":"invoke","operation":"library.stats","contract":{"id":"malware.library.stats","version":"1.0.0","digest":"` + operations["library.stats"].contract.Digest + `"},"input":{},"context":{"files":[],"assets":[{"assetId":"sample-index","path":"/assets/sample-index","digest":"r:1","extra":1}],"outputDirectory":null}}`
	var sink bytes.Buffer
	if err := serve(strings.NewReader(frame), &sink); err == nil {
		t.Fatal("unknown dataset field accepted")
	}
	// Missing grant.
	ctx := assetCtx()
	ctx.Assets = []skillruntime.ProcessAsset{}
	if _, ferr := sampleIndexDB(invokeReq("library.stats", `{}`, ctx)); ferr == nil {
		t.Fatal("missing dataset grant accepted")
	}
	// Database file absent.
	swapSeams(t, nil, func(string) (fs.FileInfo, error) { return nil, errors.New("gone") }, nil)
	if _, ferr := sampleIndexDB(invokeReq("library.stats", `{}`, assetCtx())); ferr == nil || ferr.code != "RESOURCE_UNAVAILABLE" {
		t.Fatalf("%v", ferr)
	}
}

// ---- handlers over canned malquery output ----

// fakeMalquery stubs the CLI seam, captures the argv, and returns canned
// stdout. The argv must always start with the fixed binary and -db wiring.
func fakeMalquery(t *testing.T, stdout string, runErr error) func(context.Context, string, []string, []string, int64) cliOutcome {
	t.Helper()
	return func(_ context.Context, _ string, _ []string, argv []string, _ int64) cliOutcome {
		if filepath.ToSlash(argv[0]) != "/runtime/bin/malquery" || argv[1] != "-db" || argv[2] != "/assets/sample-index/database.sqlite" {
			t.Fatalf("fixed argv wiring broken: %v", argv)
		}
		if runErr != nil {
			return cliOutcome{stderr: []byte("malquery: not found: 样本不在库中 abc"), err: runErr}
		}
		return cliOutcome{stdout: []byte(stdout)}
	}
}

func TestLibraryStats(t *testing.T) {
	swapSeams(t, nil, assetStatOK, fakeMalquery(t, `{"samples":10,"with_binary":8,"with_summary":3,"tags":55,"logs":12,"families":4}`, nil))
	got := serveInvoke(t, invokeReq("library.stats", `{}`, assetCtx()))
	if got["type"] != "result" || got["output"].(map[string]any)["samples"].(float64) != 10 {
		t.Fatalf("%v", got)
	}
	// Unknown field in tool output is a failure, not silent tolerance.
	swapSeams(t, nil, assetStatOK, fakeMalquery(t, `{"samples":1,"with_binary":0,"with_summary":0,"tags":0,"logs":0,"families":0,"surprise":1}`, nil))
	got = serveInvoke(t, invokeReq("library.stats", `{}`, assetCtx()))
	if got["error"].(map[string]any)["code"] != "TOOL_FAILED" {
		t.Fatalf("%v", got)
	}
	// Non-empty input object rejected by the contract shape.
	got = serveInvoke(t, invokeReq("library.stats", `{"verbose":true}`, assetCtx()))
	if got["error"].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("%v", got)
	}
}

func TestSampleSearch(t *testing.T) {
	swapSeams(t, nil, assetStatOK, fakeMalquery(t, `{"count":1,"samples":[{"sha256":"`+strings.Repeat("a", 64)+`","family":"Qakbot","has_binary":true,"source":"bazaar"}]}`, nil))
	got := serveInvoke(t, invokeReq("sample.search", `{"family":"Qakbot","limit":5}`, assetCtx()))
	if got["type"] != "result" {
		t.Fatalf("%v", got)
	}
	out := got["output"].(map[string]any)
	if out["count"].(float64) != 1 || len(out["samples"].([]any)) != 1 {
		t.Fatalf("%v", out)
	}
	// Count/rows mismatch from the tool is a failure.
	swapSeams(t, nil, assetStatOK, fakeMalquery(t, `{"count":5,"samples":[]}`, nil))
	got = serveInvoke(t, invokeReq("sample.search", `{}`, assetCtx()))
	if got["error"].(map[string]any)["code"] != "TOOL_FAILED" {
		t.Fatalf("%v", got)
	}
	// Limit out of range.
	swapSeams(t, nil, assetStatOK, fakeMalquery(t, `{}`, nil))
	got = serveInvoke(t, invokeReq("sample.search", `{"limit":500}`, assetCtx()))
	if got["error"].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("%v", got)
	}
}

func TestSampleGet(t *testing.T) {
	sha := strings.Repeat("b", 64)
	swapSeams(t, nil, assetStatOK, fakeMalquery(t, `{"sample":{"sha256":"`+sha+`","has_binary":true,"source":"cape-dataset"},"tags":{"capa":["create mutex"]},"logs_omitted":{"reason":"brief"}}`, nil))
	got := serveInvoke(t, invokeReq("sample.get", `{"sha256":"`+sha+`","brief":true}`, assetCtx()))
	if got["type"] != "result" {
		t.Fatalf("%v", got)
	}
	res := got["output"].(map[string]any)["result"].(map[string]any)
	if res["sample"].(map[string]any)["sha256"] != sha || res["tags"] == nil {
		t.Fatalf("%v", res)
	}
	// Returned sample must match the requested hash.
	swapSeams(t, nil, assetStatOK, fakeMalquery(t, `{"sample":{"sha256":"`+strings.Repeat("c", 64)+`","has_binary":false,"source":"x"}}`, nil))
	got = serveInvoke(t, invokeReq("sample.get", `{"sha256":"`+sha+`"}`, assetCtx()))
	if got["error"].(map[string]any)["code"] != "TOOL_FAILED" {
		t.Fatalf("%v", got)
	}
	// Not found maps to a clean error, never a fabricated result.
	swapSeams(t, nil, assetStatOK, fakeMalquery(t, "", errors.New("exit status 1")))
	got = serveInvoke(t, invokeReq("sample.get", `{"sha256":"`+sha+`"}`, assetCtx()))
	if got["type"] != "error" || !strings.Contains(got["error"].(map[string]any)["message"].(string), "样本不在库中") {
		t.Fatalf("%v", got)
	}
	// Bad hash shape rejected before any exec.
	called := false
	swapSeams(t, nil, assetStatOK, func(context.Context, string, []string, []string, int64) cliOutcome {
		called = true
		return cliOutcome{}
	})
	got = serveInvoke(t, invokeReq("sample.get", `{"sha256":"ABC"}`, assetCtx()))
	if called || got["error"].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("%v called=%v", got, called)
	}
}

func TestSampleSimilar(t *testing.T) {
	sha := strings.Repeat("d", 64)
	swapSeams(t, nil, assetStatOK, fakeMalquery(t, `{"target":"`+sha+`","similar":{"results":[{"sha256":"`+strings.Repeat("e", 64)+`","ssdeep":88,"tlsh_diff":4}],"note":"similarity evidence"}}`, nil))
	got := serveInvoke(t, invokeReq("sample.similar", `{"sha256":"`+sha+`","limit":3}`, assetCtx()))
	if got["type"] != "result" {
		t.Fatalf("%v", got)
	}
	out := got["output"].(map[string]any)
	if out["target"] != sha || len(out["similar"].([]any)) != 1 {
		t.Fatalf("%v", out)
	}
	swapSeams(t, nil, assetStatOK, fakeMalquery(t, `{"target":"`+strings.Repeat("f", 64)+`","similar":{"results":[]}}`, nil))
	got = serveInvoke(t, invokeReq("sample.similar", `{"sha256":"`+sha+`"}`, assetCtx()))
	if got["error"].(map[string]any)["code"] != "TOOL_FAILED" {
		t.Fatalf("%v", got)
	}
}

func TestFamilyGet(t *testing.T) {
	swapSeams(t, nil, assetStatOK, fakeMalquery(t, `{"families":[{"family":"Qakbot","samples":10,"with_binary":9}]}`, nil))
	got := serveInvoke(t, invokeReq("family.get", `{}`, assetCtx()))
	if got["type"] != "result" {
		t.Fatalf("%v", got)
	}
	if got["output"].(map[string]any)["result"].(map[string]any)["families"] == nil {
		t.Fatalf("%v", got)
	}
	swapSeams(t, nil, assetStatOK, fakeMalquery(t, `{"family":"njrat","samples":4,"with_binary":4,"first_seen":"2026-01-01","last_seen":"2026-02-01","types":{"rat":4},"top_tags":[]}`, nil))
	got = serveInvoke(t, invokeReq("family.get", `{"name":"njrat"}`, assetCtx()))
	if got["type"] != "result" {
		t.Fatalf("%v", got)
	}
	// Name mismatch is a failure.
	got = serveInvoke(t, invokeReq("family.get", `{"name":"qakbot"}`, assetCtx()))
	if got["error"].(map[string]any)["code"] != "TOOL_FAILED" {
		t.Fatalf("%v", got)
	}
	// Invalid metric enum rejected before exec.
	got = serveInvoke(t, invokeReq("family.get", `{"name":"njrat","metric":"cpu"}`, assetCtx()))
	if got["error"].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("%v", got)
	}
}

func TestLibraryQuery(t *testing.T) {
	swapSeams(t, nil, assetStatOK, fakeMalquery(t, `{"columns":["family","n"],"rows":[{"family":"Qakbot","n":10}],"note":"超过 200 行，仅返回前 200 行"}`, nil))
	got := serveInvoke(t, invokeReq("library.query", `{"query":"SELECT family, COUNT(*) AS n FROM samples GROUP BY family"}`, assetCtx()))
	if got["type"] != "result" {
		t.Fatalf("%v", got)
	}
	out := got["output"].(map[string]any)
	if len(out["columns"].([]any)) != 2 || len(out["rows"].([]any)) != 1 || out["note"] == nil {
		t.Fatalf("%v", out)
	}
	// Write statements are rejected by the provider before any exec.
	called := false
	swapSeams(t, nil, assetStatOK, func(context.Context, string, []string, []string, int64) cliOutcome {
		called = true
		return cliOutcome{}
	})
	for _, q := range []string{
		"DELETE FROM samples",
		"SELECT 1; DROP TABLE samples",
		"PRAGMA writable_schema = 1",
	} {
		got = serveInvoke(t, invokeReq("library.query", `{"query":`+quoteJSON(q)+`}`, assetCtx()))
		if got["error"].(map[string]any)["code"] != "INVALID_INPUT" {
			t.Fatalf("%q: %v", q, got)
		}
	}
	if called {
		t.Fatal("rejected query reached the CLI")
	}
}

func quoteJSON(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
