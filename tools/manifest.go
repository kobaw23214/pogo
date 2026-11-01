package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/yosukeno/pogo/skillobserve"
)

// Manifest-driven tools: a skill bundle may carry a tools.json next to its
// SKILL.md declaring command-line tools as first-class agent tools. The entry
// format is the MCP Tool object (name, title, description, inputSchema,
// annotations) plus an x-pogo-exec extension saying how to run the underlying
// command. Deliberately that shape rather than a private one: the manifest is
// the durable asset (it already describes the tool the way every MCP consumer
// describes tools), while the protocol around it — which pogo does not
// implement, see docs/harness-design.md §13 — is the part that can be added
// later as a thin layer serving these same files.
//
// The Agent Skills specification allows arbitrary extra files in a skill
// directory, so a tools.json breaks no other harness: implementations that do
// not know it simply ignore it.

// ManifestFilename is the file a skill directory may carry.
const ManifestFilename = "tools.json"

// Limits carried over from the two formats this bridges. Tool names follow the
// MCP Tool rules where they are declared, but the wire name every provider API
// accepts is narrower — ^[a-zA-Z0-9_-]{1,64}$, no dots — and Claude Code has
// shipped real API errors on over-long MCP names (anthropics/claude-code
// #20830), so the mapping is validated at load rather than discovered per call.
const (
	// MaxManifestNameLength is the MCP Tool name limit (1-128, [A-Za-z0-9_.-]).
	MaxManifestNameLength = 128
	// MaxWireNameLength is the provider-side function-name limit both OpenAI-
	// and Anthropic-compatible endpoints enforce.
	MaxWireNameLength = 64
	// MaxSynthDescriptionLength matches the skills package's limit so one
	// bundle obeys one rule everywhere.
	MaxSynthDescriptionLength = 1024
	// DefaultSynthTimeout matches bash's, for the same reason: an unattended
	// agent needs a ceiling more than it needs the flexibility.
	DefaultSynthTimeout = 120
	// MaxSynthTimeout bounds what a manifest may ask for. bash's per-call
	// timeout is model-chosen and bounded by nothing; a manifest is written
	// once and read forever, so its ceiling deserves a ceiling.
	MaxSynthTimeout = 3600
	// warnSynthRegistered is the direct-registration count past which the
	// loader says so on stderr. Tool-selection accuracy degrades as the tool
	// count grows into the tens, and every registration pays schema tokens on
	// every call; the catalog is free, registration is not.
	warnSynthRegistered = 10
)

var (
	manifestNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	wireNameRE     = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	// builtinNames is what a manifest name may never map onto. Overriding a
	// built-in would change the loop's contract from underneath it — the
	// registry is not a runtimeSkill point for the harness's own verbs.
	builtinNames = map[string]bool{
		"read": true, "ls": true, "find": true, "grep": true,
		ToolResultReadName: true,
		"write":            true, "edit": true, "bash": true,
		ShellTasksName: true, ShellOutputName: true, ShellStopName: true,
		"subagent": true, "todo": true, "browser": true,
	}
	// synthRenderers is the closed set the web UI knows how to draw. Validated
	// here rather than in the browser so a typo is an author-time message, the
	// same deal the starters icon whitelist gets.
	synthRenderers = map[string]bool{
		"text": true, "code": true, "table": true, "graph": true, "card": true,
	}
)

// SynthSource is one skill directory to read a manifest from. Skill is the
// owning skill's name, used in diagnostics and in the catalog, so a card in
// the browser can say where a tool came from without re-deriving it from the
// path.
type SynthSource struct {
	Skill string
	Dir   string
	// Identity is the owning skill's trusted identity, resolved by the
	// caller that loaded the skill set. It is bound to every tool the
	// manifest yields, so the tool instance itself carries its attribution:
	// a session built from an older snapshot keeps that snapshot's identity,
	// and an external caller handed the same instance observes the same
	// skill. Zero when the loader could not resolve one, and zero records
	// nothing — a tool is never attributed by matching its name against the
	// current catalog.
	Identity skillobserve.Identity
}

// SynthDiag is one problem found while loading manifests. Manifests live in
// skill bundles, which can be anyone's, so the failure mode is skip-and-warn
// — the skills package's rule — rather than the providers.json rule of
// refusing to start: a third-party bundle with a bad manifest must not take
// the agent down with it.
type SynthDiag struct {
	Path    string
	Message string
}

// manifestFile is the on-disk shape. Decoding is strict at this level —
// unknown fields are an authoring error the loader reports rather than a hint
// it silently drops — while inputSchema and outputSchema stay raw: they are
// JSON Schema, whose keyword set is open, and raw passthrough is what keeps a
// manifest usable by an MCP server later without this loader knowing why.
type manifestFile struct {
	Tools []manifestTool `json:"tools"`
}

type manifestTool struct {
	Name         string               `json:"name"`
	Title        string               `json:"title,omitempty"`
	Description  string               `json:"description"`
	InputSchema  json.RawMessage      `json:"inputSchema"`
	OutputSchema json.RawMessage      `json:"outputSchema,omitempty"`
	Annotations  *manifestAnnotations `json:"annotations,omitempty"`
	Exec         *manifestExec        `json:"x-pogo-exec"`
	Renderer     string               `json:"x-pogo-renderer,omitempty"`
	// Registered is a pointer so the default can be true — an author who wrote
	// a tool means it to be callable — while an explicit false still registers
	// it in the catalog only, for the tiered pattern where a handful of tools
	// pay schema tokens and the long tail stays behind the skill's docs.
	Registered *bool `json:"x-pogo-registered,omitempty"`
}

// manifestAnnotations is the MCP tool annotations block. Per the spec these
// are untrusted hints from a server; here they are statements by the bundle's
// author inside the same trust boundary every skill already enjoys (the user
// dir, or a project dir behind -project-skills), which is exactly the
// boundary the prompt-injection warnings about skills are drawn around. A
// manifest cannot grant itself anything a SKILL.md in the same directory
// could not.
type manifestAnnotations struct {
	ReadOnlyHint    *bool `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool `json:"openWorldHint,omitempty"`
}

// manifestExec says how to run the command behind a declared tool.
//
// Argv is a real argument vector, never a shell command line, and parameter
// values join it as single elements. That is the whole injection story: there
// is no shell to escape from, so a sha256 argument reading `; rm -rf ~` is a
// literal string the command receives, not something the harness executes.
// A template language would look more convenient and would reopen the one
// surface bash's single-argv rule exists to keep closed.
type manifestExec struct {
	Argv []string `json:"argv"`
	// Map assigns every inputSchema property to an argument position. Every
	// property must be mapped: an argument the model supplies that the command
	// silently never receives is a plausible wrong answer, the one failure
	// mode this whole design exists to rule out.
	Map map[string]argMapping `json:"map,omitempty"`
	// TimeoutSeconds overrides DefaultSynthTimeout. Bounded by
	// MaxSynthTimeout at load.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// argMapping is one property's argv mapping: either the bare string "append"
// (the value follows the fixed argv as one element) or an object with a flag.
// Booleans must use presence — appending the string "true" to a flag that
// wanted none of it is another silent-wrong-answer, so the ambiguity is
// rejected at load instead.
type argMapping struct {
	Kind string // "append" | "flag" | "presence"
	Flag string
}

// UnmarshalJSON accepts the two spellings a manifest author can write.
func (a *argMapping) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if s != "append" {
			return fmt.Errorf("mapping must be \"append\" or {\"flag\": ...}, got %q", s)
		}
		*a = argMapping{Kind: "append"}
		return nil
	}
	var obj struct {
		Flag     string `json:"flag"`
		Presence *bool  `json:"presence"`
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&obj); err != nil {
		return fmt.Errorf("mapping must be \"append\" or {\"flag\": ..., \"presence\": bool}: %v", err)
	}
	if obj.Presence == nil || !*obj.Presence {
		if obj.Flag == "" {
			return fmt.Errorf("flag mapping needs a flag name")
		}
		*a = argMapping{Kind: "flag", Flag: obj.Flag}
		return nil
	}
	if obj.Flag == "" {
		return fmt.Errorf("presence mapping needs a flag name")
	}
	*a = argMapping{Kind: "presence", Flag: obj.Flag}
	return nil
}

// LoadSynthTools reads tools.json from each source directory and returns the
// tools to register, the full catalog (registered or not, for the UI and a
// future MCP server), and the diagnostics. Order follows the sources, entries
// within a file follow the file; the registry preserves it, and a stable
// declaration order is worth keeping because it is the order the model sees.
func LoadSynthTools(srcs []SynthSource) ([]Tool, []SynthInfo, []SynthDiag) {
	var (
		toolsOut   []Tool
		catalogOut []SynthInfo
		diags      []SynthDiag
		seen       = map[string]bool{} // wire names, collisions among manifests
		registered int
	)
	for _, src := range srcs {
		dir, err := filepath.Abs(src.Dir)
		if err != nil {
			diags = append(diags, SynthDiag{
				Path:    filepath.Join(src.Dir, ManifestFilename),
				Message: "resolve skill directory: " + err.Error(),
			})
			continue
		}
		path := filepath.Join(dir, ManifestFilename)
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				diags = append(diags, SynthDiag{Path: path, Message: err.Error()})
			}
			continue
		}
		var mf manifestFile
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&mf); err != nil {
			diags = append(diags, SynthDiag{Path: path, Message: "not loaded: " + err.Error()})
			continue
		}
		for i, mt := range mf.Tools {
			st, info, err := mt.validate()
			if err != nil {
				diags = append(diags, SynthDiag{
					Path:    fmt.Sprintf("%s tools[%d]", path, i),
					Message: err.Error(),
				})
				continue
			}
			if builtinNames[st.Name()] || hostShortcutName(st.Name()) || hostSkillAuthorName(st.Name()) || hostWatchName(st.Name()) || seen[st.Name()] {
				diags = append(diags, SynthDiag{
					Path:    fmt.Sprintf("%s tools[%d]", path, i),
					Message: fmt.Sprintf("name %q maps to %q, which is already taken; tool not loaded", mt.Name, st.Name()),
				})
				continue
			}
			seen[st.Name()] = true
			info.Skill = src.Skill
			// Keep bundle paths anchored when execution switches to a workspace.
			st.expandSkillDir(dir)
			st.identity = src.Identity
			catalogOut = append(catalogOut, info)
			if info.Registered {
				registered++
				toolsOut = append(toolsOut, st)
			}
		}
	}
	if registered > warnSynthRegistered {
		diags = append(diags, SynthDiag{
			Message: fmt.Sprintf("%d manifest tools are directly registered; tool selection degrades as the count grows. Mark all but the handful the model needs every session \"x-pogo-registered\": false — the catalog still shows them, the skill's docs still teach them", registered),
		})
	}
	return toolsOut, catalogOut, diags
}

// validate checks one manifest entry and, on success, returns the runnable
// tool (with the wire name applied) and its catalog projection. Every rule
// here is checked at load so the model never meets it: a bad manifest costs
// an author a stderr message, not a session a wasted turn.
func (mt manifestTool) validate() (*SynthTool, SynthInfo, error) {
	name := strings.TrimSpace(mt.Name)
	if !manifestNameRE.MatchString(name) {
		return nil, SynthInfo{}, fmt.Errorf("name %q is invalid: 1-128 characters, letters, digits, '_', '-' and '.' only", mt.Name)
	}
	wire := strings.ReplaceAll(name, ".", "_")
	if len(wire) > MaxWireNameLength || !wireNameRE.MatchString(wire) {
		return nil, SynthInfo{}, fmt.Errorf("name %q becomes %q on the wire, which exceeds the %d-character function-name limit every provider enforces; shorten it", name, wire, MaxWireNameLength)
	}

	desc := strings.TrimSpace(mt.Description)
	if desc == "" {
		return nil, SynthInfo{}, fmt.Errorf("description is required; it is the entire basis on which the model chooses the tool")
	}
	if len([]rune(desc)) > MaxSynthDescriptionLength {
		return nil, SynthInfo{}, fmt.Errorf("description exceeds %d characters", MaxSynthDescriptionLength)
	}
	if bad, ch := firstInvisible(desc); bad {
		return nil, SynthInfo{}, fmt.Errorf("description contains an invisible or control character (U+%04X); descriptions enter the model's context and this is how tool-poisoning payloads hide", ch)
	}

	if len(mt.InputSchema) == 0 {
		return nil, SynthInfo{}, fmt.Errorf("inputSchema is required, even for a no-argument tool ({\"type\":\"object\"})")
	}
	var schema map[string]any
	if err := json.Unmarshal(mt.InputSchema, &schema); err != nil {
		return nil, SynthInfo{}, fmt.Errorf("inputSchema must be a JSON object: %v", err)
	}
	props, err := shallowSchema(schema)
	if err != nil {
		return nil, SynthInfo{}, err
	}

	if mt.Exec == nil {
		return nil, SynthInfo{}, fmt.Errorf("x-pogo-exec is required: without it there is nothing to run")
	}
	spec, err := mt.Exec.validate(props)
	if err != nil {
		return nil, SynthInfo{}, err
	}

	renderer := mt.Renderer
	if renderer == "" {
		renderer = "text"
	}
	if !synthRenderers[renderer] {
		return nil, SynthInfo{}, fmt.Errorf("x-pogo-renderer %q is not one of text, code, table, graph, card", mt.Renderer)
	}

	an := mt.Annotations
	if an == nil {
		an = &manifestAnnotations{}
	}
	hint := func(p *bool) bool { return p != nil && *p }
	registered := mt.Registered == nil || *mt.Registered

	info := SynthInfo{
		Name: name, WireName: wire, Title: mt.Title, Description: desc,
		InputSchema: schema, Renderer: renderer, Registered: registered,
		ReadOnly: hint(an.ReadOnlyHint), Destructive: hint(an.DestructiveHint),
		Idempotent: hint(an.IdempotentHint), OpenWorld: hint(an.OpenWorldHint),
	}
	return &SynthTool{info: info, exec: spec}, info, nil
}

// validate on manifestExec checks the argv and that the mapping covers exactly
// the schema's properties — no silent drops in either direction.
func (me *manifestExec) validate(props map[string]any) (synthExec, error) {
	if len(me.Argv) == 0 || strings.TrimSpace(me.Argv[0]) == "" {
		return synthExec{}, fmt.Errorf("x-pogo-exec.argv must name the command to run")
	}
	for prop, spec := range props {
		m, ok := me.Map[prop]
		if !ok {
			return synthExec{}, fmt.Errorf("property %q has no x-pogo-exec.map entry; every property must be mapped or the argument would be silently dropped", prop)
		}
		typ, _ := spec.(map[string]any)["type"].(string)
		if m.Kind == "presence" && typ != "boolean" {
			return synthExec{}, fmt.Errorf("property %q uses presence but is %s, not boolean", prop, typ)
		}
		if m.Kind != "presence" && typ == "boolean" {
			return synthExec{}, fmt.Errorf("property %q is boolean and must use {\"flag\": ..., \"presence\": true}; a bare boolean has no useful value form", prop)
		}
	}
	timeout := me.TimeoutSeconds
	if timeout == 0 {
		timeout = DefaultSynthTimeout
	}
	if timeout < 1 || timeout > MaxSynthTimeout {
		return synthExec{}, fmt.Errorf("timeout_seconds %d is outside 1-%d", me.TimeoutSeconds, MaxSynthTimeout)
	}
	return synthExec{argv: me.Argv, argMap: me.Map, timeout: timeout}, nil
}

// shallowSchema checks the parts of inputSchema the runtime validator actually
// reads (type/properties/required/enum — see validate.go) and nothing deeper.
// The point is not to police JSON Schema; it is that a malformed schema would
// otherwise surface per call as a confusing validation error instead of once,
// at load, naming the file.
func shallowSchema(schema map[string]any) (map[string]any, error) {
	if typ, _ := schema["type"].(string); typ != "object" {
		return nil, fmt.Errorf("inputSchema.type must be \"object\"")
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		// No properties declared: a no-argument tool. The map field is then
		// required to be absent-or-empty below by the exact-coverage rule.
		props = map[string]any{}
		schema["properties"] = props
	}
	for prop, spec := range props {
		if _, ok := spec.(map[string]any); !ok {
			return nil, fmt.Errorf("inputSchema.properties.%s must be an object describing the parameter", prop)
		}
	}
	switch req := schema["required"].(type) {
	case nil:
	case []any:
		for _, r := range req {
			name, _ := r.(string)
			if _, ok := props[name]; !ok {
				return nil, fmt.Errorf("required lists %q, which is not a declared property", name)
			}
		}
	default:
		return nil, fmt.Errorf("inputSchema.required must be an array of property names")
	}
	return props, nil
}

// firstInvisible reports the first rune a description should not contain:
// control characters, and the format (Cf) class where zero-width and bidi
// overrides live. Tool descriptions are model input, and hidden text in model
// input is an attack, so it is rejected rather than stripped — the author
// needs to know it is there.
func firstInvisible(s string) (bool, rune) {
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return true, r
		}
	}
	return false, 0
}
