package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"time"

	"github.com/yosukeno/pogo/skillruntime"
)

// handleCapabilities implements malware.file.capabilities with the pinned CAPA
// standalone binary and the locked capa-rules asset. The raw CAPA JSON
// document is normalized into a bounded capability/ATT&CK list; exit code,
// JSON validity and output size are all checked so a failed analysis can
// never surface as an empty-but-successful result.

const (
	capaTimeout          = 240 * time.Second
	capaMaxStdout        = 32 << 20
	capaMaxCapabilities  = 512
	capaMaxAttackPerRule = 8
)

// CAPA JSON document (v9 standalone, `capa --json`). Unknown fields are
// tolerated: only the stable top-level structure is validated.
type capaDocument struct {
	Meta struct {
		Analysis struct {
			Format string `json:"format"`
			OS     string `json:"os"`
			Arch   string `json:"arch"`
		} `json:"analysis"`
	} `json:"meta"`
	Rules map[string]capaRule `json:"rules"`
}

type capaRule struct {
	Meta capaRuleMeta `json:"meta"`
}

type capaRuleMeta struct {
	Namespace string           `json:"namespace"`
	Attack    []capaAttackPart `json:"att&ck"`
}

type capaAttackPart struct {
	Tactic       string `json:"tactic"`
	Technique    string `json:"technique"`
	Subtechnique string `json:"subtechnique"`
	ID           string `json:"id"`
}

type normalizedAttack struct {
	Tactic    string `json:"tactic"`
	Technique string `json:"technique"`
	ID        string `json:"id"`
}

type normalizedCapability struct {
	Rule      string             `json:"rule"`
	Namespace string             `json:"namespace"`
	Attack    []normalizedAttack `json:"attack"`
}

func handleCapabilities(req *skillruntime.ProcessRequest, raw json.RawMessage) (any, []artifact, *failErr) {
	var in struct {
		FileID       string `json:"fileId"`
		SampleSHA256 string `json:"sampleSha256"`
	}
	if err := decodeTypedStrict(raw, &in); err != nil {
		return nil, nil, fail("INVALID_INPUT", "input does not match the contract: %v", err)
	}
	inputPath, _, ferr := inputFile(req, in.FileID, in.SampleSHA256)
	if ferr != nil {
		return nil, nil, ferr
	}
	rulesDir, ferr := capaRulesAsset(req)
	if ferr != nil {
		return nil, nil, ferr
	}
	root, err := runtimeRoot()
	if err != nil {
		return nil, nil, fail("RESOURCE_UNAVAILABLE", "tool runtime root is unavailable")
	}
	argv := []string{filepath.Join(root, "bin", "capa"), "--rules", rulesDir, "--json", "--quiet", inputPath}
	ctx, cancel := context.WithTimeout(context.Background(), capaTimeout)
	defer cancel()
	res := runCLI(ctx, tempRoot, toolEnv(), argv, capaMaxStdout)
	if res.timedOut {
		return nil, nil, fail("TOOL_FAILED", "capa analysis timed out after %s", capaTimeout)
	}
	if res.err != nil {
		return nil, nil, fail("TOOL_FAILED", "capa could not analyze the input: %s", clipString(string(res.stderr), 400))
	}
	if res.overflow {
		return nil, nil, fail("TOOL_FAILED", "capa output exceeded the provider capture limit")
	}
	doc, err := parseCapaDocument(res.stdout)
	if err != nil {
		return nil, nil, fail("TOOL_FAILED", "unexpected capa output: %v", err)
	}
	capabilities, truncated := normalizeCapa(doc)
	return map[string]any{
		"format":          clipString(doc.Meta.Analysis.Format, 64),
		"capabilities":    capabilities,
		"capabilityCount": len(capabilities),
		"truncated":       truncated,
	}, nil, nil
}

// capaRulesAsset verifies the locked rules asset mapping and returns its
// fixed mount path. The asset id and path are contract-fixed; the request
// cannot substitute another ruleset. The bundled sample-index asset rides
// alongside so the operation can also take a library sample.
func capaRulesAsset(req *skillruntime.ProcessRequest) (string, *failErr) {
	for _, a := range req.Context.Assets {
		if a.AssetID == "capa-rules" && a.Path == "/assets/capa-rules" {
			info, err := statFile(a.Path)
			if err != nil || !info.IsDir() {
				return "", fail("RESOURCE_UNAVAILABLE", "locked CAPA rules are unavailable")
			}
			return a.Path, nil
		}
	}
	return "", fail("INVALID_INPUT", "expected the locked capa-rules asset mapping")
}

var (
	errCapaNotObject    = errors.New("top-level value is not an object")
	errCapaMissingRules = errors.New("missing rules object")
)

func parseCapaDocument(stdout []byte) (*capaDocument, error) {
	// Strict generic pass first: a truncated or non-JSON blob (e.g. a crash
	// banner) must not be mistaken for a successful empty scan.
	v, err := decodeStrict(stdout)
	if err != nil {
		return nil, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errCapaNotObject
	}
	if _, ok := obj["rules"].(map[string]any); !ok {
		return nil, errCapaMissingRules
	}
	var doc capaDocument
	if err := json.Unmarshal(stdout, &doc); err != nil {
		return nil, err
	}
	if doc.Rules == nil {
		return nil, errCapaMissingRules
	}
	return &doc, nil
}

// normalizeCapa flattens rule hits into a deterministic, bounded list.
func normalizeCapa(doc *capaDocument) ([]normalizedCapability, bool) {
	names := make([]string, 0, len(doc.Rules))
	for name := range doc.Rules {
		names = append(names, name)
	}
	sort.Strings(names)
	truncated := false
	if len(names) > capaMaxCapabilities {
		names = names[:capaMaxCapabilities]
		truncated = true
	}
	out := make([]normalizedCapability, 0, len(names))
	for _, name := range names {
		meta := doc.Rules[name].Meta
		attack := make([]normalizedAttack, 0, len(meta.Attack))
		for i, p := range meta.Attack {
			if i >= capaMaxAttackPerRule {
				truncated = true
				break
			}
			technique := p.Technique
			if p.Subtechnique != "" {
				technique = p.Subtechnique
			}
			attack = append(attack, normalizedAttack{
				Tactic:    clipString(p.Tactic, 128),
				Technique: clipString(technique, 256),
				ID:        clipString(p.ID, 64),
			})
		}
		out = append(out, normalizedCapability{
			Rule:      clipString(name, 256),
			Namespace: clipString(meta.Namespace, 256),
			Attack:    attack,
		})
	}
	// Deterministic presentation: namespace first, then rule name.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Rule < out[j].Rule
	})
	return out, truncated
}
