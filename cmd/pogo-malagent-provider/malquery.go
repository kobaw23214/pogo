package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skillruntime/core"
)

// Library capabilities: read-only views over the host-granted sample index.
// The Skill carries its index as an immutable sample-index asset. The provider accepts
// host-fixed resource mappings and drives the
// pinned malquery binary with a fixed allowlist of subcommands and flags. The
// sql escape hatch re-uses malquery's own read-only statement whitelist
// (single SELECT/WITH statement); the pinned binary additionally opens the
// database with mode=ro, so writes fail at the driver layer.

const (
	sampleIndexSlot      = "sample-index"
	sampleIndexAssetPath = "/assets/sample-index/database.sqlite"

	malqueryTimeout        = 60 * time.Second
	malqueryMaxStdout      = 8 << 20
	malqueryMaxResultBytes = 900 << 10
)

// sampleIndexDB accepts only the digest-pinned asset mounted by the host.
func sampleIndexDB(req *skillruntime.ProcessRequest) (string, *failErr) {
	if req.Context == nil || len(req.Context.Assets) != 1 {
		return "", fail("INVALID_INPUT", "expected only the bundled sample-index asset")
	}
	a := req.Context.Assets[0]
	if a.AssetID != sampleIndexSlot || a.Path != "/assets/sample-index" || !core.IsDigest(a.Digest) {
		return "", fail("INVALID_INPUT", "unexpected bundled sample index mapping")
	}
	info, err := statFile(sampleIndexAssetPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", fail("RESOURCE_UNAVAILABLE", "bundled sample index is unavailable")
	}
	return sampleIndexAssetPath, nil
}

// runMalquery executes the pinned binary with the fixed -db wiring and the
// allowlisted argv tail; stdout must be one strict JSON document.
func runMalquery(req *skillruntime.ProcessRequest, argvTail []string) ([]byte, *failErr) {
	dbPath, ferr := sampleIndexDB(req)
	if ferr != nil {
		return nil, ferr
	}
	root, err := runtimeRoot()
	if err != nil {
		return nil, fail("RESOURCE_UNAVAILABLE", "tool runtime root is unavailable")
	}
	argv := append([]string{filepath.Join(root, "bin", "malquery"), "-db", dbPath}, argvTail...)
	ctx, cancel := context.WithTimeout(context.Background(), malqueryTimeout)
	defer cancel()
	res := runCLI(ctx, tempRoot, toolEnv(), argv, malqueryMaxStdout)
	if res.timedOut {
		return nil, fail("TOOL_FAILED", "malquery timed out after %s", malqueryTimeout)
	}
	if res.err != nil {
		msg := clipString(string(res.stderr), 400)
		if msg == "" {
			msg = "exited without diagnostics"
		}
		return nil, fail("TOOL_FAILED", "malquery: %s", msg)
	}
	if res.overflow {
		return nil, fail("TOOL_FAILED", "malquery output exceeded the provider capture limit")
	}
	out := bytes.TrimSpace(res.stdout)
	if _, err := decodeStrict(out); err != nil {
		return nil, fail("TOOL_FAILED", "unexpected malquery output: %v", err)
	}
	return out, nil
}

// boundedPassthrough validates a malquery JSON object for verbatim return
// under the contract's result field: top level must be an object and the
// serialized size is capped so a response can never exceed the provider's
// maxOutputBytes grant.
func boundedPassthrough(raw []byte) (map[string]any, *failErr) {
	if len(raw) > malqueryMaxResultBytes {
		return nil, fail("TOOL_FAILED", "result exceeds the %d byte provider limit; narrow the query or use a brief form", malqueryMaxResultBytes)
	}
	v, err := decodeStrict(raw)
	if err != nil {
		return nil, fail("TOOL_FAILED", "unexpected malquery output: %v", err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fail("TOOL_FAILED", "unexpected malquery output: top-level value is not an object")
	}
	return obj, nil
}

// ---- malware.sample.search ----

type searchInput struct {
	Family        string `json:"family"`
	Type          string `json:"type"`
	Tag           string `json:"tag"`
	HasBinary     bool   `json:"hasBinary"`
	MaxDetections *int   `json:"maxDetections"`
	Limit         *int   `json:"limit"`
}

type sampleRow struct {
	SHA256     string  `json:"sha256"`
	Family     *string `json:"family,omitempty"`
	Type       *string `json:"type,omitempty"`
	Detections *int64  `json:"detections,omitempty"`
	HasBinary  bool    `json:"has_binary"`
	Size       *int64  `json:"size,omitempty"`
	FirstSeen  *string `json:"first_seen,omitempty"`
	Source     string  `json:"source"`
}

type searchResult struct {
	Count   int         `json:"count"`
	Samples []sampleRow `json:"samples"`
}

func buildSearchArgs(in searchInput, limit int) []string {
	args := []string{"search"}
	if in.Family != "" {
		args = append(args, "--family", in.Family)
	}
	if in.Type != "" {
		args = append(args, "--type", in.Type)
	}
	if in.Tag != "" {
		args = append(args, "--tag", in.Tag)
	}
	if in.MaxDetections != nil {
		args = append(args, "--max-detections", strconv.Itoa(*in.MaxDetections))
	}
	if in.HasBinary {
		args = append(args, "--has-binary")
	}
	return append(args, "-n", strconv.Itoa(limit))
}

func handleSampleSearch(req *skillruntime.ProcessRequest, raw json.RawMessage) (any, []artifact, *failErr) {
	var in searchInput
	if err := decodeTypedStrict(raw, &in); err != nil {
		return nil, nil, fail("INVALID_INPUT", "input does not match the contract: %v", err)
	}
	for _, s := range []string{in.Family, in.Type, in.Tag} {
		if len(s) > 128 {
			return nil, nil, fail("INVALID_INPUT", "filter values are limited to 128 bytes")
		}
	}
	if in.MaxDetections != nil && (*in.MaxDetections < 0 || *in.MaxDetections > 1000) {
		return nil, nil, fail("INVALID_INPUT", "maxDetections must be within [0, 1000]")
	}
	limit := 20
	if in.Limit != nil {
		if *in.Limit < 1 || *in.Limit > 100 {
			return nil, nil, fail("INVALID_INPUT", "limit must be within [1, 100]")
		}
		limit = *in.Limit
	}
	out, ferr := runMalquery(req, buildSearchArgs(in, limit))
	if ferr != nil {
		return nil, nil, ferr
	}
	var sr searchResult
	if err := decodeTypedStrict(out, &sr); err != nil {
		return nil, nil, fail("TOOL_FAILED", "unexpected malquery search output: %v", err)
	}
	if sr.Samples == nil {
		sr.Samples = []sampleRow{}
	}
	if sr.Count != len(sr.Samples) || sr.Count > 100 {
		return nil, nil, fail("TOOL_FAILED", "malquery search result count mismatch")
	}
	for _, s := range sr.Samples {
		if s.SHA256 == "" || len(s.SHA256) > 128 {
			return nil, nil, fail("TOOL_FAILED", "malquery returned a malformed sample row")
		}
	}
	return sr, nil, nil
}

// ---- malware.sample.get ----

func handleSampleGet(req *skillruntime.ProcessRequest, raw json.RawMessage) (any, []artifact, *failErr) {
	var in struct {
		SHA256 string `json:"sha256"`
		Brief  bool   `json:"brief"`
	}
	if err := decodeTypedStrict(raw, &in); err != nil {
		return nil, nil, fail("INVALID_INPUT", "input does not match the contract: %v", err)
	}
	if !sha256Pattern.MatchString(in.SHA256) {
		return nil, nil, fail("INVALID_INPUT", "sha256 must be 64 lowercase hex characters")
	}
	argv := []string{"sample", in.SHA256}
	if in.Brief {
		argv = append(argv, "--brief")
	}
	out, ferr := runMalquery(req, argv)
	if ferr != nil {
		return nil, nil, ferr
	}
	obj, ferr := boundedPassthrough(out)
	if ferr != nil {
		return nil, nil, ferr
	}
	sample, ok := obj["sample"].(map[string]any)
	if !ok {
		return nil, nil, fail("TOOL_FAILED", "unexpected sample detail shape: missing sample object")
	}
	if got, _ := sample["sha256"].(string); got != in.SHA256 {
		return nil, nil, fail("TOOL_FAILED", "returned sample does not match the requested sha256")
	}
	return map[string]any{"result": obj}, nil, nil
}

// ---- malware.sample.similar ----

type similarResult struct {
	Target  string        `json:"target"`
	Similar []similarItem `json:"similar"`
	Note    string        `json:"note,omitempty"`
}

type similarItem struct {
	SHA256   string `json:"sha256"`
	SSDeep   int    `json:"ssdeep"`
	TLSHDiff *int   `json:"tlsh_diff"`
}

func handleSampleSimilar(req *skillruntime.ProcessRequest, raw json.RawMessage) (any, []artifact, *failErr) {
	var in struct {
		SHA256 string `json:"sha256"`
		Limit  *int   `json:"limit"`
	}
	if err := decodeTypedStrict(raw, &in); err != nil {
		return nil, nil, fail("INVALID_INPUT", "input does not match the contract: %v", err)
	}
	if !sha256Pattern.MatchString(in.SHA256) {
		return nil, nil, fail("INVALID_INPUT", "sha256 must be 64 lowercase hex characters")
	}
	limit := 10
	if in.Limit != nil {
		if *in.Limit < 1 || *in.Limit > 50 {
			return nil, nil, fail("INVALID_INPUT", "limit must be within [1, 50]")
		}
		limit = *in.Limit
	}
	out, ferr := runMalquery(req, []string{"similar", in.SHA256, "-n", strconv.Itoa(limit)})
	if ferr != nil {
		return nil, nil, ferr
	}
	var source struct {
		Target  string `json:"target"`
		Similar struct {
			Results []similarItem `json:"results"`
			Note    string        `json:"note"`
		} `json:"similar"`
	}
	if err := decodeTypedStrict(out, &source); err != nil {
		return nil, nil, fail("TOOL_FAILED", "unexpected malquery similar output: %v", err)
	}
	sr := similarResult{Target: source.Target, Similar: source.Similar.Results, Note: clipString(source.Similar.Note, 500)}
	if sr.Target != in.SHA256 {
		return nil, nil, fail("TOOL_FAILED", "similar result target does not match the requested sha256")
	}
	if len(sr.Similar) > limit {
		return nil, nil, fail("TOOL_FAILED", "malquery returned more similar rows than allowed")
	}
	if sr.Similar == nil {
		sr.Similar = []similarItem{}
	}
	for _, row := range sr.Similar {
		if !sha256Pattern.MatchString(row.SHA256) || row.SSDeep < 0 || row.SSDeep > 100 || (row.TLSHDiff != nil && (*row.TLSHDiff < 0 || *row.TLSHDiff > 1000000)) {
			return nil, nil, fail("TOOL_FAILED", "malquery returned an invalid similarity score or sample identity")
		}
	}
	return sr, nil, nil
}

// ---- malware.family.get ----

type familyInput struct {
	Name           string `json:"name"`
	IncludeProfile bool   `json:"includeProfile"`
	IncludeInfra   bool   `json:"includeInfra"`
	IncludeTrend   bool   `json:"includeTrend"`
	IncludeGeo     bool   `json:"includeGeo"`
	Metric         string `json:"metric"`
	Days           *int   `json:"days"`
}

func buildFamilyArgs(in familyInput) ([]string, *failErr) {
	if in.Name == "" {
		if in.IncludeProfile || in.IncludeInfra || in.IncludeTrend || in.IncludeGeo || in.Metric != "" || in.Days != nil {
			return nil, fail("INVALID_INPUT", "profile/infra/trend/geo sections require a family name")
		}
		return []string{"family"}, nil
	}
	args := []string{"family", in.Name}
	if in.IncludeProfile {
		args = append(args, "--profile")
	}
	if in.IncludeInfra {
		args = append(args, "--infra")
	}
	if in.IncludeTrend {
		args = append(args, "--trend")
	}
	if in.IncludeGeo {
		args = append(args, "--geo")
	}
	if in.Metric != "" {
		args = append(args, "--metric", in.Metric)
	}
	if in.Days != nil {
		args = append(args, "--days", strconv.Itoa(*in.Days))
	}
	return args, nil
}

func handleFamilyGet(req *skillruntime.ProcessRequest, raw json.RawMessage) (any, []artifact, *failErr) {
	var in familyInput
	if err := decodeTypedStrict(raw, &in); err != nil {
		return nil, nil, fail("INVALID_INPUT", "input does not match the contract: %v", err)
	}
	if len(in.Name) > 128 {
		return nil, nil, fail("INVALID_INPUT", "family name is limited to 128 bytes")
	}
	if in.Metric != "" && in.Metric != "propagation" && in.Metric != "domain_requests" {
		return nil, nil, fail("INVALID_INPUT", "metric must be propagation or domain_requests")
	}
	if in.Days != nil && (*in.Days < 0 || *in.Days > 366) {
		return nil, nil, fail("INVALID_INPUT", "days must be within [0, 366]")
	}
	args, ferr := buildFamilyArgs(in)
	if ferr != nil {
		return nil, nil, ferr
	}
	out, ferr := runMalquery(req, args)
	if ferr != nil {
		return nil, nil, ferr
	}
	obj, ferr := boundedPassthrough(out)
	if ferr != nil {
		return nil, nil, ferr
	}
	if in.Name == "" {
		families, ok := obj["families"].([]any)
		if !ok || len(families) > 4096 {
			return nil, nil, fail("TOOL_FAILED", "unexpected family list shape")
		}
	} else if got, _ := obj["family"].(string); got != in.Name {
		return nil, nil, fail("TOOL_FAILED", "returned family does not match the requested name")
	}
	return map[string]any{"result": obj}, nil, nil
}

// ---- malware.library.stats ----

type statsResult struct {
	Samples     int64 `json:"samples"`
	WithBinary  int64 `json:"with_binary"`
	WithSummary int64 `json:"with_summary"`
	Tags        int64 `json:"tags"`
	Logs        int64 `json:"logs"`
	Families    int64 `json:"families"`
}

func handleLibraryStats(req *skillruntime.ProcessRequest, raw json.RawMessage) (any, []artifact, *failErr) {
	var in struct{}
	if err := decodeTypedStrict(raw, &in); err != nil {
		return nil, nil, fail("INVALID_INPUT", "input does not match the contract: %v", err)
	}
	out, ferr := runMalquery(req, []string{"stats"})
	if ferr != nil {
		return nil, nil, ferr
	}
	var sr statsResult
	if err := decodeTypedStrict(out, &sr); err != nil {
		return nil, nil, fail("TOOL_FAILED", "unexpected malquery stats output: %v", err)
	}
	if sr.Samples < 0 || sr.WithBinary < 0 || sr.WithSummary < 0 || sr.Tags < 0 || sr.Logs < 0 || sr.Families < 0 {
		return nil, nil, fail("TOOL_FAILED", "malquery returned negative counters")
	}
	return sr, nil, nil
}

// ---- malware.library.query ----

type queryResult struct {
	Columns []string         `json:"columns"`
	Rows    []map[string]any `json:"rows"`
	Note    string           `json:"note,omitempty"`
}

// assertReadOnlyQuery is the same statement whitelist the pinned malquery sql
// subcommand enforces: exactly one statement, SELECT or WITH first, trailing
// semicolon tolerated, any interior semicolon rejected. The provider rejects
// here so the model gets a clean INVALID_INPUT; the binary repeats the check
// and opens the database mode=ro as the real wall.
func assertReadOnlyQuery(q string) error {
	q = strings.TrimSpace(q)
	q = strings.TrimSuffix(q, ";")
	q = strings.TrimSpace(q)
	if q == "" {
		return errors.New("empty statement")
	}
	if strings.Contains(q, ";") {
		return errors.New("only a single statement is accepted (semicolons rejected)")
	}
	tok := strings.ToUpper(strings.Fields(q)[0])
	if strings.HasPrefix(tok, "SELECT") || strings.HasPrefix(tok, "WITH") {
		return nil
	}
	return fmt.Errorf("only read-only SELECT/WITH queries are accepted, got %q", clipString(q, 24))
}

func handleLibraryQuery(req *skillruntime.ProcessRequest, raw json.RawMessage) (any, []artifact, *failErr) {
	var in struct {
		Query string `json:"query"`
		Limit *int   `json:"limit"`
	}
	if err := decodeTypedStrict(raw, &in); err != nil {
		return nil, nil, fail("INVALID_INPUT", "input does not match the contract: %v", err)
	}
	if in.Query == "" || len(in.Query) > 4096 {
		return nil, nil, fail("INVALID_INPUT", "query length must be within [1, 4096]")
	}
	if err := assertReadOnlyQuery(in.Query); err != nil {
		return nil, nil, fail("INVALID_INPUT", "read-only query rejected: %v", err)
	}
	limit := 200
	if in.Limit != nil {
		if *in.Limit < 1 || *in.Limit > 200 {
			return nil, nil, fail("INVALID_INPUT", "limit must be within [1, 200]")
		}
		limit = *in.Limit
	}
	out, ferr := runMalquery(req, []string{"sql", in.Query, "-n", strconv.Itoa(limit)})
	if ferr != nil {
		return nil, nil, ferr
	}
	var qr queryResult
	if err := decodeTypedStrict(out, &qr); err != nil {
		return nil, nil, fail("TOOL_FAILED", "unexpected malquery sql output: %v", err)
	}
	if len(qr.Columns) > 64 || len(qr.Rows) > 200 {
		return nil, nil, fail("TOOL_FAILED", "malquery returned more columns or rows than allowed")
	}
	if qr.Columns == nil {
		qr.Columns = []string{}
	}
	if qr.Rows == nil {
		qr.Rows = []map[string]any{}
	}
	if len(qr.Note) > 512 {
		return nil, nil, fail("TOOL_FAILED", "malquery returned an oversized note")
	}
	return qr, nil, nil
}
