package core

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// Embedded schema profile limits from SPEC section 3.
const (
	// MaxSchemaNodes bounds the expanded node count of one embedded schema.
	MaxSchemaNodes = 10000
	// MaxPatternLen bounds the length of one regex pattern.
	MaxPatternLen = 256

	draft202012URI = "https://json-schema.org/draft/2020-12/schema"
)

// profileKeywords is the closed set of JSON Schema keywords supported for
// embedded contract input/output schemas in v1. Anything else, notably
// format, content*, dynamic refs, anchors, dependencies or pattern
// properties, is rejected.
var profileKeywords = map[string]bool{
	"$schema": true, "$defs": true, "$ref": true,
	"type": true, "properties": true, "required": true,
	"additionalProperties": true, "items": true,
	"minItems": true, "maxItems": true, "uniqueItems": true,
	"enum": true, "const": true,
	"minimum": true, "maximum": true,
	"minLength": true, "maxLength": true, "pattern": true,
	"description": true, "title": true,
	"oneOf": true, "anyOf": true, "allOf": true, "not": true,
}

var profileTypes = map[string]bool{
	"object": true, "array": true, "string": true,
	"integer": true, "number": true, "boolean": true, "null": true,
}

var defsRefPattern = regexp.MustCompile(`^#/\$defs/([A-Za-z0-9][A-Za-z0-9_.-]*)$`)

// validateEmbeddedSchema enforces the Pogo v1 closed-schema profile on one
// embedded contract input/output schema. raw must be the generic value from
// decodeRawJSON. label identifies the schema in error messages.
//
// In addition to the profile walk, the schema is resolved once with
// jsonschema-go so that "valid JSON Schema Draft 2020-12" remains a separate
// requirement: passing the profile is not accepted as schema validity, and
// vice versa.
func validateEmbeddedSchema(raw any, label string) error {
	root, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: embedded schema must be a JSON object", label)
	}
	if v, present := root["$schema"]; present {
		s, ok := v.(string)
		if !ok || s != draft202012URI {
			return fmt.Errorf("%s: $schema must be %q", label, draft202012URI)
		}
	}

	defs := map[string]map[string]any{}
	if d, present := root["$defs"]; present {
		dm, ok := d.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: $defs must be an object", label)
		}
		for name, dv := range dm {
			dvm, ok := dv.(map[string]any)
			if !ok {
				return fmt.Errorf("%s: $defs/%s must be an object", label, name)
			}
			defs[name] = dvm
		}
	}
	p := &profileWalk{label: label, defs: defs}
	if err := p.node(root, "$", true); err != nil {
		return err
	}
	if p.nodes > MaxSchemaNodes {
		return fmt.Errorf("%s: schema has more than %d nodes", label, MaxSchemaNodes)
	}
	if err := p.checkRefCycles(); err != nil {
		return err
	}

	// Independent validity pass with the real Draft 2020-12 implementation.
	b, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("%s: re-encode embedded schema: %v", label, err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("%s: not a valid JSON Schema: %v", label, err)
	}
	// No loader: any external reference fails resolution here.
	if _, err := s.Resolve(nil); err != nil {
		return fmt.Errorf("%s: not a valid/resolvable Draft 2020-12 schema: %v", label, err)
	}
	return nil
}

type profileWalk struct {
	label string
	defs  map[string]map[string]any
	nodes int
}

// node checks one schema node. isRoot marks the document root.
func (p *profileWalk) node(m map[string]any, path string, isRoot bool) error {
	p.nodes++
	if p.nodes > MaxSchemaNodes {
		return fmt.Errorf("%s: schema has more than %d nodes", p.label, MaxSchemaNodes)
	}
	for k := range m {
		if !profileKeywords[k] {
			return fmt.Errorf("%s: %s: unsupported schema keyword %q", p.label, path, k)
		}
		if k == "$schema" && !isRoot {
			return fmt.Errorf("%s: %s: $schema is only allowed at the schema root", p.label, path)
		}
		if k == "$defs" && !isRoot {
			return fmt.Errorf("%s: %s: $defs is only allowed at the schema root", p.label, path)
		}
	}

	types, err := schemaTypes(m, p.label, path)
	if err != nil {
		return err
	}
	if isRoot && !types["object"] {
		return fmt.Errorf("%s: root schema type must be object", p.label)
	}
	if types["object"] {
		ap, present := m["additionalProperties"]
		if !present {
			return fmt.Errorf("%s: %s: object schema must declare additionalProperties: false", p.label, path)
		}
		if ap != false {
			return fmt.Errorf("%s: %s: additionalProperties must be false (v1 closed objects)", p.label, path)
		}
	}

	if pat, present := m["pattern"]; present {
		s, ok := pat.(string)
		if !ok {
			return fmt.Errorf("%s: %s: pattern must be a string", p.label, path)
		}
		if len(s) > MaxPatternLen {
			return fmt.Errorf("%s: %s: pattern longer than %d bytes", p.label, path, MaxPatternLen)
		}
		if _, err := regexp.Compile(s); err != nil {
			return fmt.Errorf("%s: %s: pattern does not compile with the bounded Go engine: %v", p.label, path, err)
		}
	}

	if ref, present := m["$ref"]; present {
		s, ok := ref.(string)
		if !ok {
			return fmt.Errorf("%s: %s: $ref must be a string", p.label, path)
		}
		match := defsRefPattern.FindStringSubmatch(s)
		if match == nil {
			return fmt.Errorf("%s: %s: $ref %q must be a local #/$defs/<name> reference; remote and dynamic refs are forbidden", p.label, path, s)
		}
		target := match[1]
		if _, ok := p.defs[target]; !ok {
			return fmt.Errorf("%s: %s: $ref %q has no matching $defs entry", p.label, path, s)
		}
	}

	if props, present := m["properties"]; present {
		pm, ok := props.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: %s: properties must be an object", p.label, path)
		}
		for name, sub := range pm {
			if err := p.sub(sub, path+".properties["+name+"]"); err != nil {
				return err
			}
		}
	}
	if req, present := m["required"]; present {
		ra, ok := req.([]any)
		if !ok {
			return fmt.Errorf("%s: %s: required must be an array", p.label, path)
		}
		seen := map[string]bool{}
		for _, r := range ra {
			rs, ok := r.(string)
			if !ok {
				return fmt.Errorf("%s: %s: required entries must be strings", p.label, path)
			}
			if seen[rs] {
				return fmt.Errorf("%s: %s: duplicate required entry %q", p.label, path, rs)
			}
			seen[rs] = true
		}
	}
	if items, present := m["items"]; present {
		if err := p.sub(items, path+".items"); err != nil {
			return err
		}
	}
	if nt, present := m["not"]; present {
		if err := p.sub(nt, path+".not"); err != nil {
			return err
		}
	}
	for _, comb := range []string{"oneOf", "anyOf", "allOf"} {
		if arr, present := m[comb]; present {
			aa, ok := arr.([]any)
			if !ok || len(aa) == 0 {
				return fmt.Errorf("%s: %s: %s must be a non-empty array of schemas", p.label, path, comb)
			}
			for i, sub := range aa {
				if err := p.sub(sub, fmt.Sprintf("%s.%s[%d]", path, comb, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (p *profileWalk) sub(v any, path string) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: %s: subschema must be an object (boolean schemas not in v1 profile)", p.label, path)
	}
	return p.node(m, path, false)
}

// checkRefCycles rejects recursive references among $defs. The walk above
// guarantees every $ref is local and points at an existing def; here we
// build the def-to-def graph and demand it be acyclic.
func (p *profileWalk) checkRefCycles() error {
	edges := map[string][]string{}
	for name, dm := range p.defs {
		edges[name] = collectDefRefs(dm)
	}
	const (
		white = 0 // unvisited
		gray  = 1 // on stack
		black = 2 // done
	)
	color := map[string]int{}
	var visit func(n string, stack []string) error
	visit = func(n string, stack []string) error {
		switch color[n] {
		case gray:
			return fmt.Errorf("%s: recursive $defs reference cycle: %s -> %s",
				p.label, strings.Join(stack, " -> "), n)
		case black:
			return nil
		}
		color[n] = gray
		for _, m := range edges[n] {
			if err := visit(m, append(stack, n)); err != nil {
				return err
			}
		}
		color[n] = black
		return nil
	}
	for name := range p.defs {
		if err := visit(name, nil); err != nil {
			return err
		}
	}
	return nil
}

// collectDefRefs returns the names of all $defs referenced (transitively,
// through nested subschemas) from one $defs entry.
func collectDefRefs(m map[string]any) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if r, ok := t["$ref"].(string); ok {
				if match := defsRefPattern.FindStringSubmatch(r); match != nil {
					out = append(out, match[1])
				}
			}
			for k, sub := range t {
				if k == "$ref" {
					continue
				}
				walk(sub)
			}
		case []any:
			for _, sub := range t {
				walk(sub)
			}
		}
	}
	walk(m)
	return out
}

// schemaTypes extracts the type set of one schema node.
func schemaTypes(m map[string]any, label, path string) (map[string]bool, error) {
	out := map[string]bool{}
	v, present := m["type"]
	if !present {
		return out, nil
	}
	switch t := v.(type) {
	case string:
		if !profileTypes[t] {
			return nil, fmt.Errorf("%s: %s: unknown type %q", label, path, t)
		}
		out[t] = true
	case []any:
		for _, e := range t {
			s, ok := e.(string)
			if !ok || !profileTypes[s] {
				return nil, fmt.Errorf("%s: %s: unknown type entry %v", label, path, e)
			}
			out[s] = true
		}
	default:
		return nil, fmt.Errorf("%s: %s: type must be a string or array of strings", label, path)
	}
	return out, nil
}

// embeddedSchemaStringProps returns the root-level string properties of an
// embedded schema, for fileInputs coherence checks. ok is false when the
// schema does not have the simple flat shape v1 requires (root object with
// direct properties), in which case no pointer may be accepted.
func embeddedSchemaStringProps(raw any) (props map[string]bool, required map[string]bool, ok bool) {
	root, isMap := raw.(map[string]any)
	if !isMap {
		return nil, nil, false
	}
	types, err := schemaTypes(root, "schema", "$")
	if err != nil || !types["object"] {
		return nil, nil, false
	}
	props = map[string]bool{}
	required = map[string]bool{}
	pm, _ := root["properties"].(map[string]any)
	for name, sub := range pm {
		sm, _ := sub.(map[string]any)
		if sm == nil {
			continue
		}
		st, err := schemaTypes(sm, "schema", "$.properties")
		if err == nil && st["string"] && len(st) == 1 {
			props[name] = true
		}
	}
	if ra, ok := root["required"].([]any); ok {
		for _, r := range ra {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}
	return props, required, true
}
