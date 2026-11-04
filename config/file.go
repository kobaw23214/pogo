package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/yosukeno/pogo/llm"
)

// FileName is the user's provider and model configuration.
//
// It lives in the home directory and is read from nowhere else. That is a security
// decision, not a convenience one, and it has a specific precedent: CVE-2026-21852
// in Claude Code, where a repository could ship a settings file that pointed the
// tool's base URL at an attacker's endpoint, and the API key was sent there when
// the project was opened — before the user was asked to trust anything. A file that
// says "which host receives your credential" must not be something a `git clone`
// can deliver. The home directory is already as trusted as the binary; a working
// directory is not.
//
// So there is deliberately no per-project provider config, and no merge of one.
// pogo warns if it finds this file inside a working directory, because the only
// two explanations are a mistake and an attempt.
const FileName = "providers.json"

// PathEnv overrides the config location, for tests and for anyone keeping their
// configuration somewhere unusual. Still not read from the working directory: this
// is the user's own environment, which a cloned repository does not write.
const PathEnv = "POGO_CONFIG"

var saveMu sync.Mutex

// fileConfig is the on-disk shape.
//
// Providers and models are separate because they nest: a provider is a host plus
// the name of the variable holding its credential, and models belong to one. Both
// merge onto the built-ins rather than replacing them, so a file can add a local
// endpoint without restating the catalog, and pogo still works with no file at
// all.
type fileConfig struct {
	// Default overrides which model is used when none is named.
	Default string `json:"default,omitempty"`
	// VisionModel is the model that sees images on behalf of text-only ones:
	// it writes the descriptions attached images carry, and answers
	// analyze_image. Empty disables the vision helper — images then reach only
	// models that see them natively.
	VisionModel string `json:"vision_model,omitempty"`
	// Providers are keyed by the name models refer to. A key matching a built-in
	// replaces it, which is how a corporate gateway gets used without pretending
	// to be a different provider.
	Providers map[string]providerFile `json:"providers,omitempty"`
	// Models are appended, except that an id matching a built-in replaces it.
	Models []modelFile `json:"models,omitempty"`
}

type providerFile struct {
	BaseURL string `json:"base_url"`
	// KeyEnv is the *name* of an environment variable, never a credential. See
	// validate: a non-empty value that does not look like a variable name is
	// refused rather than tried, so a pasted key fails with an explanation
	// instead of ending up in an error message. Empty means the endpoint needs
	// no credential at all (a local server, say).
	KeyEnv string `json:"key_env"`
	// Headers are extra request headers sent on every call to this provider —
	// a vendor-specific switch a standard OpenAI client would never know about,
	// DashScope's X-DashScope-SSE being the case that created the field. Names
	// and values are validated (token grammar, no newlines) because they land
	// in requests verbatim; they are configuration, not credentials, so unlike
	// key_env they may hold literal values.
	Headers map[string]string `json:"headers,omitempty"`
	// ExtraBody is merged over the request body's top level on every call —
	// the body-side sibling of Headers, for gateways that demand a key a
	// standard OpenAI client would never send (DashScope's native leftover
	// `parameters` object being the case that created the field). Shallow
	// merge, custom key wins a collision: an override here was configured
	// knowingly, and there is no silently-more-correct side to prefer.
	ExtraBody map[string]any `json:"extra_body,omitempty"`
	// TimeoutSeconds bounds one model request. Zero preserves compatibility with
	// older provider files and selects the client's default timeout.
	TimeoutSeconds int64 `json:"timeout_seconds,omitempty"`
	// AllowInsecureHTTP is the explicit opt-out for the plain-http-to-a-remote-
	// host refusal. Loopback and private-network addresses are exempt without
	// it; a gateway on an address that is neither — reachable only over a
	// network the operator does not control end to end — needs this flag, which
	// exists so the refusal stays a refusal for everyone who did not read the
	// error message and think about it.
	AllowInsecureHTTP bool   `json:"allow_insecure_http,omitempty"`
	BaseURLEnv        string `json:"base_url_env,omitempty"`
}

type modelFile struct {
	Compatibility llm.Compatibility `json:"compatibility,omitzero"`
	ID            string            `json:"id"`
	Provider      string            `json:"provider"`
	Aliases       []string          `json:"aliases,omitempty"`
	ContextWindow int               `json:"context_window,omitempty"`
	MaxTokens     int64             `json:"max_tokens,omitempty"`
	// SubagentModel is the model a read-only subagent runs instead of this one.
	//
	// Read-only delegation is the case where a smaller model usually suffices —
	// finding where something is implemented is not the same work as writing it —
	// and it is also the case where latency is most visible, because the parent's
	// turn is blocked until the answer comes back. Left empty, a subagent inherits
	// its parent's model, which is the existing behaviour.
	//
	// No built-in mapping ships with pogo. Which model is the cheaper sibling of
	// which is a pricing claim, and pogo has no basis for making one on the user's
	// behalf.
	SubagentModel string `json:"subagent_model,omitempty"`
	// FollowupModel is the model that writes the follow-up suggestion chips after
	// a run ends, instead of this one. The generation is a small, latency-tolerant
	// call made off the answer's critical path, which is the case a cheaper model
	// is made for. Left empty, follow-ups are written by the session's own model.
	//
	// No built-in mapping ships with pogo, for the same reason subagent_model
	// carries none: which model is the cheaper sibling of which is a pricing claim.
	FollowupModel string `json:"followup_model,omitempty"`
	// Subagent marks the model for the explore-subagent pool. Marked models share
	// parallel read-only children least-in-flight first, so a batch of subagents
	// spreads across providers instead of all landing on the parent's.
	Subagent bool `json:"subagent,omitempty"`
	// Vision declares whether this model accepts images natively. Tri-state by
	// design: unset means judge by the id (glm-4v, moonshot vision, gemma4… all
	// say so in the name), because the file should not have to restate what the
	// id already says; an explicit true/false is for ids that lie either way.
	Vision *bool `json:"vision,omitempty"`
	// Price is what this model charges per million tokens. Optional, and only
	// -cost-budget reads it.
	//
	// A pointer so that "declared as free" and "not declared" stay different things:
	// a local model server legitimately costs nothing, while a model nobody priced
	// must not be silently treated as free by a flag whose entire job is to stop
	// spending. See llm.Price for why none of these ship built in.
	Price *priceFile `json:"price,omitempty"`
}

// priceFile is the on-disk shape of a rate.
//
// Rates are per *million* tokens because that is how every vendor publishes them,
// so a user transcribing a price page does not have to move a decimal point six
// places and does not have to wonder whether pogo wanted per-token. The unit of
// the number itself is whatever the user is billed in; pogo never names a currency
// and never converts. See llm.Price.
type priceFile struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
	// CacheRead defaults to 0, which reads as "cached prompt tokens are free here".
	// That is a real arrangement and a reasonable default, but it is also the
	// optimistic one, so validatePrice requires it to be no larger than Input —
	// a cache that cost more than a miss would mean the fields were swapped.
	CacheRead float64 `json:"cache_read,omitempty"`
}

// envName is the conventional shape of an environment variable name. Used to
// refuse a credential pasted into key_env.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Path is where the configuration is read from.
func Path() (string, error) {
	if p := strings.TrimSpace(os.Getenv(PathEnv)); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pogo", FileName), nil
}

// Load merges the user's configuration into the catalog.
//
// Returns warnings rather than printing them, so the caller decides where
// diagnostics go — in json mode they must not reach stdout.
//
// This fork compiles in no providers or models, so the file is the only source:
// a missing file, or one that declares no models, is an error that says where to
// create the file and what a minimal one looks like. (Upstream treats a missing
// file as the normal case because it ships built-ins; with an empty built-in
// catalog, "carry on" would mean a later catalog[0] panic.)
//
// A malformed file *is* an error. The alternative, carrying on regardless, would
// mean a typo silently changes which endpoint receives the credential, and the
// user would have no way to notice.
func Load() (warnings []string, err error) {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	return loadLocked(false)
}

func loadLocked(replace bool) (warnings []string, err error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no provider configuration: create %s "+
				"(set %s to point elsewhere). No models are compiled in, so there is "+
				"nothing to run without it. Minimal file:\n%s",
				path, PathEnv, minimalConfig)
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var f fileConfig
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	// Strict: an unrecognised key is far more likely to be a misspelling that
	// silently does nothing than a forward-compatible extension, and the cost of the
	// former is a setting the user believes is in effect.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	baseProviders, baseModels := providers, catalog
	if replace {
		baseProviders, baseModels = nil, nil
	}
	ps, ms, def, vm, warnings, err := merge(baseProviders, baseModels, f)
	if err != nil {
		return warnings, fmt.Errorf("%s: %w", path, err)
	}
	if len(ms) == 0 {
		return warnings, fmt.Errorf("%s declares no models: add at least one entry "+
			"to \"models\" — with nothing compiled in, the file is the only source", path)
	}
	providers, catalog = ps, ms
	if replace || def != "" {
		defaultModel = def
	}
	visionModel = vm
	return warnings, nil
}

// minimalConfig is the example Load points a first-time user at: one provider,
// one model, every field spelled the way the decoder expects.
const minimalConfig = `{
  "providers": {
    "zhipu": { "base_url": "https://open.bigmodel.cn/api/coding/paas/v4", "key_env": "ZHIPU_API_KEY" }
  },
  "models": [
    { "id": "glm-5.2", "provider": "zhipu", "context_window": 1048576, "max_tokens": 16384 }
  ]
}`

// WarnIfInWorkingDir reports a configuration file found somewhere it will not be
// read from.
//
// Ignoring it silently would be safe but unhelpful in both directions: a user who
// put it in the wrong place would see their settings do nothing, and a repository
// that shipped one to redirect the credential would go unremarked.
// FileShape is providers.json's on-disk shape, exported for the settings
// editor: the web layer decodes and re-encodes it, while validation, merging
// and reloading stay here. An alias rather than a copy so the two can never
// drift apart.
type FileShape = fileConfig

// Validate checks a candidate configuration against the same rules Load
// applies, without touching the loaded tables or the file on disk. The
// settings dialog calls this implicitly through Save; it exists on its own for
// callers that want the verdict before deciding to write.
func Validate(f FileShape) ([]string, error) {
	// Merged onto an empty base rather than the live tables: the fork compiles
	// nothing in, so what comes out is exactly what the file alone provides —
	// the same state a fresh process would load.
	_, ms, _, _, warnings, err := merge(map[string]Provider{}, nil, f)
	if err != nil {
		return warnings, err
	}
	if len(ms) == 0 {
		return warnings, fmt.Errorf("configuration declares no models: add at least one entry to \"models\"")
	}
	return warnings, nil
}

// Save validates f, writes it to the config path, and reloads the in-memory
// tables, so the change is live for every later Resolve without a restart.
//
// The write prefers temp-file-plus-rename (atomic), but falls back to an
// in-place truncate when rename refuses: a single-file bind mount — the
// container deployment's providers.json — cannot be renamed over (EBUSY),
// and a Windows target held open refuses the rename the same way. The
// fallback reopens the window a half-written file could squeeze through, so
// it writes the already-marshalled bytes in one Write call: small file, one
// syscall, no interleaving. A read-only mount (EROFS/EPERM/EACCES on the
// fallback) is reported as a deployment problem rather than a bug.
func Save(f FileShape) ([]string, error) {
	saveMu.Lock()
	defer saveMu.Unlock()
	if warnings, err := Validate(f); err != nil {
		return warnings, err
	}
	path, err := Path()
	if err != nil {
		return nil, err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')

	if tmp, err := os.CreateTemp(filepath.Dir(path), FileName+".tmp-*"); err == nil {
		tmpName := tmp.Name()
		_, werr := tmp.Write(data)
		cerr := tmp.Close()
		if werr == nil && cerr == nil {
			if rerr := os.Rename(tmpName, path); rerr == nil {
				return Reload()
			}
			os.Remove(tmpName)
		} else {
			os.Remove(tmpName)
		}
		// Fall through to the in-place write: the rename failing is the
		// bind-mount case this fallback exists for.
	}

	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("writing %s: %w (is the file on a read-only mount?)", path, err)
	}
	if _, err := out.Write(data); err != nil {
		out.Close()
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}
	if err := out.Close(); err != nil {
		return nil, err
	}
	return Reload()
}

// Reload re-reads the configuration file and replaces the in-memory tables.
//
// Merge against an empty base and commit all tables together only after a
// successful load. Concurrent Resolve calls retain the previous complete
// generation, including if the replacement file is unreadable or invalid.
func Reload() ([]string, error) {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	return loadLocked(true)
}

func WarnIfInWorkingDir(dir string) string {
	if dir == "" {
		return ""
	}
	for _, rel := range []string{FileName, filepath.Join(".pogo", FileName)} {
		p := filepath.Join(dir, rel)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		home, _ := Path()
		return fmt.Sprintf("ignoring %s: provider configuration is only read from %s, "+
			"because a file that decides where your API key is sent must not arrive with a "+
			"checkout", p, home)
	}
	return ""
}

// lostFields names the fields the built-in entry had and the replacement does not.
//
// Only the ones whose absence changes behaviour, and each for a stated reason:
//
//   - context_window drives -context-edit auto (half of it) and the browser's
//     context gauge (fractions of it), so a missing one disables both.
//   - max_tokens caps output, and zero would send no cap at all.
//   - aliases are how the model is reachable from -model; losing them silently
//     breaks a command line that used to work.
//   - subagent_model and subagent select the explore-subagent pool.
//   - followup_model selects the model that writes follow-up suggestion chips.
//
// Provider is deliberately not checked: pointing an id at a different provider is
// the whole reason someone re-declares a built-in model, not an accident.
func lostFields(old, new Model) []string {
	var out []string
	if old.ContextWindow != 0 && new.ContextWindow == 0 {
		out = append(out, "context_window")
	}
	if old.MaxTokens != 0 && new.MaxTokens == 0 {
		out = append(out, "max_tokens")
	}
	if len(old.Aliases) > 0 && len(new.Aliases) == 0 {
		out = append(out, "aliases")
	}
	if old.SubagentModel != "" && new.SubagentModel == "" {
		out = append(out, "subagent_model")
	}
	if old.FollowupModel != "" && new.FollowupModel == "" {
		out = append(out, "followup_model")
	}
	if old.Subagent && !new.Subagent {
		out = append(out, "subagent")
	}
	if old.Vision != nil && new.Vision == nil {
		out = append(out, "vision")
	}
	if old.Price != nil && new.Price == nil {
		out = append(out, "price")
	}
	return out
}

// toLLM converts a declared rate, or returns nil when none was declared.
//
// A method on the pointer so the nil case is handled once here rather than at the
// call site: nil in means nil out, and nil is the value -cost-budget refuses on.
func (p *priceFile) toLLM() *llm.Price {
	if p == nil {
		return nil
	}
	return &llm.Price{Input: p.Input, Output: p.Output, CacheRead: p.CacheRead}
}

// validatePrice refuses a rate that cannot be what the user meant.
//
// Refusing rather than warning, unlike a bad subagent_model reference. The
// difference is what the field is for: an unusable subagent mapping degrades to
// inheriting the parent's model and the session still works, whereas a wrong price
// is silently wrong for the entire run and the only thing reading it is a spend
// ceiling. There is no degraded mode for "your ceiling is off by a factor of ten".
func validatePrice(id string, p *priceFile) error {
	if p == nil {
		return nil
	}
	for _, f := range []struct {
		name string
		v    float64
	}{{"input", p.Input}, {"output", p.Output}, {"cache_read", p.CacheRead}} {
		if f.v < 0 {
			return fmt.Errorf("model %q: price.%s is negative", id, f.name)
		}
		// NaN and +Inf survive JSON only as the results of arithmetic, but a rate that
		// is not a real number would make every comparison against a ceiling false,
		// which is the one failure mode that looks exactly like "no ceiling was set".
		if f.v != f.v || f.v > 1e12 {
			return fmt.Errorf("model %q: price.%s is not a usable rate", id, f.name)
		}
	}
	if p.CacheRead > p.Input {
		return fmt.Errorf("model %q: price.cache_read (%g) is higher than price.input (%g); "+
			"a cached read costs less than a miss, so these look swapped",
			id, p.CacheRead, p.Input)
	}
	return nil
}

// merge folds a file onto the built-ins. Pure, so the rules are testable without
// touching a filesystem or package state.
func merge(baseProviders map[string]Provider, baseModels []Model, f fileConfig) (
	map[string]Provider, []Model, string, string, []string, error) {

	ps := make(map[string]Provider, len(baseProviders)+len(f.Providers))
	for k, v := range baseProviders {
		ps[k] = v
	}
	var warnings []string

	for name, pf := range f.Providers {
		if err := validateProvider(name, pf); err != nil {
			return nil, nil, "", "", warnings, err
		}
		if _, replaced := ps[name]; replaced {
			warnings = append(warnings, fmt.Sprintf(
				"provider %q from the config file replaces the built-in one", name))
		}
		// A file base_url is pinned against ambient env — unless the entry itself
		// declares base_url_env, which is the same file speaking with the same
		// authority, explicitly opting this provider into redirection (a proxy
		// on one machine, a gateway in CI) without editing the file.
		ps[name] = Provider{
			Name: name, BaseURL: pf.BaseURL, KeyEnv: pf.KeyEnv, BaseURLEnv: pf.BaseURLEnv,
			Headers: pf.Headers, ExtraBody: pf.ExtraBody, TimeoutSeconds: pf.TimeoutSeconds, pinnedBaseURL: pf.BaseURLEnv == "",
		}
	}

	ms := make([]Model, len(baseModels))
	copy(ms, baseModels)
	index := func(id string) int {
		for i, m := range ms {
			if strings.EqualFold(m.ID, id) {
				return i
			}
		}
		return -1
	}
	for _, mf := range f.Models {
		if strings.TrimSpace(mf.ID) == "" {
			return nil, nil, "", "", warnings, fmt.Errorf("a model entry has no id")
		}
		if _, ok := ps[mf.Provider]; !ok {
			return nil, nil, "", "", warnings, fmt.Errorf(
				"model %q names provider %q, which is neither built in nor defined in this file",
				mf.ID, mf.Provider)
		}
		if err := validatePrice(mf.ID, mf.Price); err != nil {
			return nil, nil, "", "", warnings, err
		}
		if err := mf.Compatibility.Validate(); err != nil {
			return nil, nil, "", "", warnings, fmt.Errorf("model %q: %w", mf.ID, err)
		}
		m := Model{
			Compatibility: mf.Compatibility,
			ID:            mf.ID, Provider: mf.Provider, Aliases: mf.Aliases,
			ContextWindow: mf.ContextWindow, MaxTokens: mf.MaxTokens,
			SubagentModel: mf.SubagentModel, FollowupModel: mf.FollowupModel,
			Subagent: mf.Subagent,
			Vision:   mf.Vision,
			Price:    mf.Price.toLLM(),
		}
		if i := index(mf.ID); i >= 0 {
			// Replacing a built-in model is announced, like replacing a built-in
			// provider is. It was not, and that silence had a cost: glm-5.2's window
			// was corrected in the catalogue from 200,000 to 1,048,576, and a config
			// file that re-declared the id kept the old figure with nothing said —
			// which quietly kept -context-edit auto clearing at a tenth of the real
			// window and the browser gauge going red at a sixth of it.
			//
			// The dropped fields are named rather than just counted, because the
			// replacement is wholesale: a field the file omits does not fall back to
			// the built-in, it is gone. That is the part nobody expects, and the
			// warning is the only place it can be said.
			if dropped := lostFields(ms[i], m); len(dropped) > 0 {
				warnings = append(warnings, fmt.Sprintf(
					"model %q from the config file replaces the built-in one, dropping %s "+
						"(the whole entry is replaced, not merged field by field)",
					mf.ID, strings.Join(dropped, ", ")))
			} else {
				warnings = append(warnings, fmt.Sprintf(
					"model %q from the config file replaces the built-in one", mf.ID))
			}
			ms[i] = m
		} else {
			ms = append(ms, m)
		}
	}

	// Checked after everything is merged, because a mapped model may be defined
	// later in the file than the model that names it. A bad reference is dropped
	// with a warning rather than refused: the parent model still works, and losing
	// a session over an optional field would be the wrong trade.
	known := func(id string) bool {
		for _, m := range ms {
			if strings.EqualFold(m.ID, id) {
				return true
			}
		}
		return false
	}
	for i := range ms {
		if ms[i].SubagentModel != "" && !known(ms[i].SubagentModel) {
			warnings = append(warnings, fmt.Sprintf(
				"model %q names subagent_model %q, which is not a known model; subagents "+
					"will inherit %s instead", ms[i].ID, ms[i].SubagentModel, ms[i].ID))
			ms[i].SubagentModel = ""
		}
		if ms[i].FollowupModel != "" && !known(ms[i].FollowupModel) {
			warnings = append(warnings, fmt.Sprintf(
				"model %q names followup_model %q, which is not a known model; follow-up "+
					"suggestions will use %s instead", ms[i].ID, ms[i].FollowupModel, ms[i].ID))
			ms[i].FollowupModel = ""
		}
	}

	def := strings.TrimSpace(f.Default)
	if def != "" && !known(def) {
		return nil, nil, "", "", warnings, fmt.Errorf("default names %q, which is not a known model", def)
	}
	// vision_model is dropped with a warning rather than refused, on the same
	// trade as subagent_model: the sessions it would help still run, just
	// without their images being described.
	vm := strings.TrimSpace(f.VisionModel)
	if vm != "" && !known(vm) {
		warnings = append(warnings, fmt.Sprintf(
			"vision_model %q is not a known model; images will only reach models "+
				"that see them natively", vm))
		vm = ""
	}
	return ps, ms, def, vm, warnings, nil
}

func validateProvider(name string, p providerFile) error {
	if err := llm.ValidateProtocolOverrides(p.ExtraBody); err != nil {
		return fmt.Errorf("provider %q: %w", name, err)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("a provider has an empty name")
	}
	if p.TimeoutSeconds < 0 {
		return fmt.Errorf("provider %q: timeout_seconds must be >= 0", name)
	}
	// Empty key_env is legitimate: the endpoint needs no credential. A non-empty
	// value must be a variable *name* — deliberately not echoed on failure,
	// because the most likely reason it fails this check is that someone pasted
	// the credential itself.
	if p.KeyEnv != "" && !envName.MatchString(p.KeyEnv) {
		return fmt.Errorf("provider %q: key_env must be the name of an environment "+
			"variable holding the key, not the key itself", name)
	}
	if p.BaseURLEnv != "" && !envName.MatchString(p.BaseURLEnv) {
		return fmt.Errorf("provider %q: base_url_env must be an environment variable name", name)
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil {
		return fmt.Errorf("provider %q: base_url %q is not a URL", name, p.BaseURL)
	}
	// Scheme before host, because the scheme is the part that decides whether the
	// credential is protected, and because it produces the better message: a bare
	// "api.example/v1" parses as a path with no scheme, and "add https://" is more
	// use than "no host".
	switch u.Scheme {
	case "https":
	case "http":
		// The credential travels in a header on every request, so plaintext to a
		// remote host means handing it to anything on the path. Loopback is exempted
		// because a local model server is the main reason to write this file at all,
		// and there is no network to observe; private-network addresses are exempted
		// on the same reasoning — RFC 1918 and friends are not internet-routable, so
		// the operator already owns every hop the header crosses. Anything else
		// needs the provider's explicit allow_insecure_http, so the refusal below
		// stays the default for configurations nobody thought about.
		if !loopback(u.Hostname()) && !privateAddr(u.Hostname()) && !p.AllowInsecureHTTP {
			what := p.KeyEnv
			if what == "" {
				what = "the conversation"
			}
			return fmt.Errorf("provider %q: base_url %q is plain http to a remote host, "+
				"which would send %s in cleartext; use https, or a loopback/private address "+
				"for an intranet gateway, or set allow_insecure_http on this provider",
				name, p.BaseURL, what)
		}
	default:
		return fmt.Errorf("provider %q: base_url %q must start with https:// (or http:// "+
			"for a loopback address)", name, p.BaseURL)
	}
	if u.Host == "" {
		return fmt.Errorf("provider %q: base_url %q names no host", name, p.BaseURL)
	}
	// Headers land in requests verbatim, so their grammar is checked here rather
	// than trusted: a name outside RFC 9110's token set or a value with a newline
	// is not a header anyone could send, and the newline case specifically is
	// header injection.
	for h, v := range p.Headers {
		if !headerName.MatchString(h) {
			return fmt.Errorf("provider %q: %q is not a valid HTTP header name", name, h)
		}
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("provider %q: header %q has a newline in its value", name, h)
		}
	}
	// ExtraBody keys become top-level body fields, so an empty key — legal JSON,
	// meaningless body — is refused rather than sent. Values are whatever JSON
	// parsed into; there is nothing further to check them against.
	for k := range p.ExtraBody {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("provider %q: extra_body has an empty key", name)
		}
	}
	return nil
}

// headerName is RFC 9110's token grammar — the characters legal in a header
// field name. Everything else would be mangled or rejected on the wire.
var headerName = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+$`)

// privateAddr reports whether host is a private-network address: RFC 1918 (and
// its IPv6 equivalent, the unique-local range), plus link-local. These are not
// routable from the public internet, which is the property the plain-http
// refusal exists to enforce. A DNS name is not resolved here: validation must
// not do network I/O, so a hostname gateway gets the refusal (and the explicit
// opt-in) rather than a lookup.
func privateAddr(host string) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && (ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

func loopback(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	return strings.HasSuffix(strings.ToLower(host), ".localhost")
}

// SubagentModel is the model a read-only subagent should run for a given parent
// model, or "" to inherit.
//
// Falls back to inheriting when the mapped model's provider has no key, because
// the alternative is a subagent that cannot start for a reason the parent did not
// choose and cannot see. The parent's own model is known to work — it is running.
func SubagentModel(parent string) string {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	m, ok := lookup(parent)
	if !ok || m.SubagentModel == "" {
		return ""
	}
	sub, ok := lookup(m.SubagentModel)
	if !ok || !configuredLocked(sub.Provider) {
		return ""
	}
	return sub.ID
}

// FollowupModel is the model that should write the follow-up suggestion chips
// after a run of the given model ends, or "" to use the session's own model.
//
// Falls back to the session model when the mapped model's provider has no key,
// for the same reason SubagentModel falls back to inheriting: a chip that never
// appears for a reason the user did not choose degrades to the rule-based
// fallback, while a call against a keyless provider would just fail.
func FollowupModel(parent string) string {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	m, ok := lookup(parent)
	if !ok || m.FollowupModel == "" {
		return ""
	}
	sub, ok := lookup(m.FollowupModel)
	if !ok || !configuredLocked(sub.Provider) {
		return ""
	}
	return sub.ID
}

// SubagentPool is the set of models parallel explore subagents are balanced
// across, in catalog order: every model marked "subagent": true whose provider
// has a key. An empty pool means subagents inherit the parent's model, which is
// the behaviour with no file at all.
//
// Order is the tie-break for equal in-flight counts, so the file's writing
// order is the preference order. The key check happens here rather than at pick
// time because an unconfigured provider never becomes usable mid-session: the
// environment does not change under a running process.
func SubagentPool() []Model {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	var out []Model
	for _, m := range catalog {
		if m.Subagent && configuredLocked(m.Provider) {
			out = append(out, m)
		}
	}
	return out
}
