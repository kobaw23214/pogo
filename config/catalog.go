package config

import (
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/yosukeno/pogo/llm"
)

// Provider is an OpenAI-compatible endpoint.
//
// Every provider comes from ~/.pogo/providers.json (see file.go, including why
// the file is read from the home directory and nowhere else): this fork compiles
// none in, so the file can define any endpoint — a vendor, a local model server,
// a mirror, a gateway — and pogo refuses to start without one.
type Provider struct {
	Name    string
	BaseURL string
	// KeyEnv is the only place a credential is read from. Empty means the
	// endpoint needs no credential at all — a local model server, say — and the
	// client then sends no Authorization header.
	KeyEnv string
	// Headers are extra headers the client sends on every call: vendor-specific
	// switches a standard OpenAI request would not carry. Validated at load
	// (token grammar, no newlines) because they are written into requests
	// verbatim. Nil on providers that need none — which is all of them until a
	// gateway asks for something like X-DashScope-SSE.
	Headers map[string]string
	// ExtraBody is merged over the request body's top level on every call —
	// the body-side sibling of Headers, for gateways demanding a key standard
	// OpenAI clients never send. Nil unless the provider's file entry declares
	// one.
	ExtraBody map[string]any
	// BaseURLEnv names an environment variable that, when set, replaces BaseURL
	// at resolve time — the escape hatch for pointing at a proxy or a mirror
	// without editing the configuration. It is only honoured where it was
	// explicitly declared: an ambient variable must never reroute a base_url
	// whose owner did not mark it as reroutable.
	BaseURLEnv string
	// pinnedBaseURL marks a base_url that ignores BaseURLEnv: every file-loaded
	// entry that did not itself declare the variable.
	pinnedBaseURL bool
}

// Model is one entry in the catalog. MaxTokens caps output; ContextWindow is what
// the gauges read against and what agent.ParseContextEdit halves to get a clearing
// threshold, so it now constrains what the model is actually sent rather than being
// informational as it was before context editing existed.
type Model struct {
	Compatibility llm.Compatibility
	ID            string
	Provider      string
	Aliases       []string
	ContextWindow int
	MaxTokens     int64
	// SubagentModel is the model a read-only subagent runs instead of this one, or
	// "" to inherit it. Only ever set from the user's config file: see modelFile.
	SubagentModel string
	// FollowupModel is the model that writes follow-up suggestion chips after this
	// one's runs, or "" to use this one. Only ever set from the user's config
	// file: see modelFile.
	FollowupModel string
	// Subagent marks the model as eligible for the explore-subagent pool: parallel
	// read-only children are balanced across every marked model, one provider at a
	// time. Only ever set from the user's config file: see modelFile.
	Subagent bool
	// Vision declares whether this model accepts images natively. nil means the
	// id decides — see SeesImages. Only ever set from the user's config file.
	Vision *bool
	// Price is the per-million-token rate, or nil when nobody has declared one.
	//
	// Never set for a built-in, and that is not an omission waiting to be filled in:
	// see llm.Price for why pogo declines to assert what a model costs, and why the
	// two built-in subscription plans have no per-token cost to assert. nil is what
	// makes -cost-budget refuse rather than silently pass, so it has to stay
	// distinguishable from a declared rate of zero.
	Price *llm.Price
}

// No providers or models are compiled in: ~/.pogo/providers.json is the single
// source of truth (this fork's choice — upstream pogo ships two built-in
// endpoints so it works with no file at all). Load() refuses to start with an
// empty catalog, so a missing file is an explained error here, not a silent
// fallback to nothing.
// Readers must see one complete generation while settings reload between runs.
var catalogMu sync.RWMutex
var providers = map[string]Provider{}

// catalog lists the models worth exposing. Order matters: the first entry is the
// default unless the file names one explicitly.
//
// ContextWindow is load-bearing beyond display: -context-edit auto takes half of
// it, and the browser's context gauge turns yellow and red at fractions of it. An
// under-declared window therefore makes pogo clear too early — paying cache misses
// and re-reads — and advise starting a new session while most of the window is
// still free. So the figures in the shipped providers.json are checked against
// the endpoints rather than copied once.
var catalog = []Model{}

// Catalog returns the known models in display order.
func Catalog() []Model {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	return slices.Clone(catalog)
}

// visionModel is the configured helper that sees images on behalf of text-only
// models, or "" when nobody configured one.
var visionModel string

// VisionModel is the model that writes image descriptions and answers
// analyze_image, or "" when the feature is unconfigured (images then reach only
// models that see them natively).
func VisionModel() string {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	return visionModel
}

// defaultModel is empty until a config file names one, so that the default stays
// a property of catalog order rather than a second thing to keep in sync with it.
var defaultModel string

// DefaultModel is the configured default, or the first catalog entry. Empty when
// the catalog is empty — Load() refuses that state at startup, so "" here is a
// test-time answer, not a runtime one.
func DefaultModel() string {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	return defaultModelLocked()
}

func defaultModelLocked() string {
	if defaultModel != "" {
		return defaultModel
	}
	if len(catalog) == 0 {
		return ""
	}
	return catalog[0].ID
}

// lookup resolves an id or alias, case-insensitively.
func lookup(name string) (Model, bool) {
	for _, m := range catalog {
		if strings.EqualFold(m.ID, name) {
			return m, true
		}
		for _, a := range m.Aliases {
			if strings.EqualFold(a, name) {
				return m, true
			}
		}
	}
	return Model{}, false
}

// visionNameParts are substrings an id contains when the model behind it sees
// images. The list is ids that say so in the name — vendors market vision as a
// suffix or a letter pair, and the honest reading of an id without any of these
// is that nobody promised the model pixels. A file entry's explicit
// "vision": true/false overrides the guess, which is why the list can stay
// conservative.
var visionNameParts = []string{
	"vision", "-4v", "4v-", "-vl", "vl-", "qvq", // glm-4v, qwen-vl, moonshot vision…
	"gpt-4o", "gpt-4.1", "gpt-4-turbo", "gpt-5", "chatgpt", // OpenAI vision era
	"claude-",                                                      // every Claude accepts images
	"gemini", "gemma", "llava", "pixtral", "internvl", "minicpm-v", // open multimodal families
	"kimi-k3", "kimi-k4", // Kimi's multimodal flagships
}

// SeesImages reports whether this model accepts image content natively. An
// explicit Vision flag wins in both directions; otherwise the id is read
// against visionNameParts.
func (m Model) SeesImages() bool {
	if m.Vision != nil {
		return *m.Vision
	}
	id := strings.ToLower(m.ID)
	for _, part := range visionNameParts {
		if strings.Contains(id, part) {
			return true
		}
	}
	return false
}

// SetCatalogForTest replaces the catalogue. It exists so that tests in other
// packages can describe the behaviour they mean — "a model with a subagent mapping"
// — instead of depending on whichever real models happen to be listed today.
//
// Exported reluctantly and named to say so. The alternative was making every
// catalogue reader take a parameter, which is a large change to a small number of
// call sites that all want the same global answer.
func SetCatalogForTest(ms []Model) {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	catalog = slices.Clone(ms)
}

// SetProvidersForTest replaces the provider table, and exists for the same
// reason SetCatalogForTest does with the same reluctance: the catalogue is
// file-only now, so a test machine has no providers either, and the two are
// meaningless apart — a model resolves against its provider's key_env, and
// KeyEnv/Configured answer questions about providers alone.
func SetProvidersForTest(ps map[string]Provider) {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	providers = maps.Clone(ps)
}

// Providers returns a snapshot of the provider table, including for tests'
// SetProvidersForTest restore. Nested
// header and body values are immutable configuration and must not be modified.
func Providers() map[string]Provider {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	return maps.Clone(providers)
}
