package web

// Model endpoint settings: GET/PUT /api/config.
//
// The dialog edits providers.json — base URLs, which environment variable
// holds each key, the model list, the default — and optionally supplies key
// values, which are applied to this process's environment so they take effect
// on the next Resolve without a restart. The file itself never holds a
// credential (see config/file.go for why that line exists); the values set
// here live in process memory and whatever environment-management the
// operator does elsewhere.
//
// Same trust bar as the rest of /api: whoever holds the token can already
// point bash at any host, so redirecting where a credential is sent grants
// nothing new — but the endpoint is still an authenticated mutation of a
// security-relevant file, which is why validation is config.Validate rather
// than a trusting decode.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/config"
)

// envNameRE mirrors config's own check: a key must name a variable, not be
// pasted content. Duplicated here rather than exported because the check the
// dialog needs is on the env map's keys, a shape config has no type for.
var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var envKeySanitize = regexp.MustCompile(`[^A-Z0-9_]`)

// Serialize credentials, catalog save and live-session reconciliation as one
// settings operation. This mutex is never taken by a running model request.
var modelSettingsSaveMu sync.Mutex

// conventionalKeyEnv is the variable a provider without an explicit key_env is
// understood to mean: <NAME>_API_KEY, the naming deploy.env already follows
// (kimi → KIMI_API_KEY). The dialog's own placeholder teaches the same rule.
func conventionalKeyEnv(name string) string {
	n := envKeySanitize.ReplaceAllString(strings.ToUpper(strings.TrimSpace(name)), "_")
	if n == "" {
		return ""
	}
	return n + "_API_KEY"
}

// envKeyFor resolves the credential this provider would send: the configured
// key_env, or its conventional fallback — so a row with key_env left empty
// still shows (and probes with) the key the operator most likely means.
func envKeyFor(name, keyEnv string) string {
	if env := strings.TrimSpace(keyEnv); env != "" {
		return strings.TrimSpace(os.Getenv(env))
	}
	if conventional := conventionalKeyEnv(name); conventional != "" {
		return strings.TrimSpace(os.Getenv(conventional))
	}
	return ""
}

// configuredProviderHost reports whether host names a provider the config
// already trusts — the boundary a server-resolved key may be sent within.
func configuredProviderHost(host string) bool {
	for _, p := range config.Providers() {
		if pu, err := url.Parse(p.BaseURL); err == nil && strings.EqualFold(pu.Hostname(), host) {
			return true
		}
	}
	return false
}

// handleGetConfig serves the settings dialog's initial state: the file as it
// sits on disk (parsed when possible, raw always) plus which providers
// currently have a key in the environment.
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	path, err := config.Path()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		// A missing or unreadable file is a state to render (the raw editor
		// starts empty), not a 500: the whole point of the dialog is to bring
		// that file into existence.
		writeJSON(w, http.StatusOK, map[string]any{
			"path": path, "raw": "", "file": nil, "read_error": err.Error(),
			"providers": []map[string]any{},
		})
		return
	}

	var f config.FileShape
	// Strict like config.Load: the dialog should see the same refusal a
	// restart would see, not a silently half-parsed file.
	parseErr := ""
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		parseErr = err.Error()
	}

	providers := make([]map[string]any, 0, len(f.Providers))
	for name, pf := range f.Providers {
		entry := map[string]any{
			"name":                name,
			"base_url":            pf.BaseURL,
			"key_env":             pf.KeyEnv,
			"headers":             pf.Headers,
			"extra_body":          pf.ExtraBody,
			"allow_insecure_http": pf.AllowInsecureHTTP,
			"base_url_env":        pf.BaseURLEnv,
			"configured":          config.Configured(name),
		}
		// The live value behind key_env (or its conventional fallback), so the
		// dialog's curl preview can show the request exactly as the server
		// would send it. Same trust bar as the rest of /api (see the header
		// comment): the file still never holds a credential — this reads
		// memory, it does not persist one.
		if v := envKeyFor(name, pf.KeyEnv); v != "" {
			entry["key_value"] = v
		}
		providers = append(providers, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":        path,
		"raw":         string(raw),
		"file":        &f,
		"parse_error": parseErr,
		"providers":   providers,
		"default":     config.DefaultModel(),
	})
}

// handleSaveConfig validates and writes the edited configuration, then reloads
// the in-memory tables. Body: {"file": {...}} or {"raw": "..."} for the JSON
// editor, plus an optional "env" map of variable-name → key value applied to
// this process before the reload so Configured answers truthfully at once.
func (s *Server) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		File *config.FileShape `json:"file"`
		Raw  *string           `json:"raw"`
		Env  map[string]string `json:"env"`
	}
	rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := decodeLenientJSON(rawBody, &body); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}

	var f config.FileShape
	switch {
	case body.Raw != nil:
		// The raw editor's text is parsed strictly — same decoder, same rules
		// as a restart — so what saves here is exactly what would load there.
		dec := json.NewDecoder(bytes.NewReader([]byte(*body.Raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid providers.json: %w", err))
			return
		}
	case body.File != nil:
		f = *body.File
	default:
		writeErr(w, http.StatusBadRequest, errors.New(`nothing to save: pass "file" or "raw"`))
		return
	}

	modelSettingsSaveMu.Lock()
	defer modelSettingsSaveMu.Unlock()

	// Key values first, so the reload's merge and every later Configured call
	// see them. Empty values are left alone: the dialog sends only what the
	// user typed, so an untouched key never wipes the environment.
	for k, v := range body.Env {
		if !envNameRE.MatchString(k) {
			writeErr(w, http.StatusBadRequest, fmt.Errorf(
				"env key %q is not an environment variable name", k))
			return
		}
		if v = strings.TrimSpace(v); v != "" {
			os.Setenv(k, v)
		}
	}

	warnings, err := config.Save(f)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Reloading the catalog alone leaves existing sessions' clients, context
	// limits and compression thresholds pointing at their original snapshots.
	warnings = append(warnings, s.mgr.syncModelConfigs()...)
	if s.opts.Logger != nil {
		s.opts.Logger.Printf("provider configuration updated via settings dialog")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"warnings": warnings,
		"default":  config.DefaultModel(),
		"models":   len(config.Catalog()),
	})
}

// handleProbeConfig runs the dialog's curl for real, from this process: the
// server sits on the networks the gateways live on and a browser does not
// (intranet base URLs, custom headers a preflight would sink). The request it
// sends is the one the preview shows — same join, same header order, same
// extra_body merge — with the key resolved the same way the wire client would
// (typed value first, then key_env). Same trust bar as the rest of /api: the
// token holder can already point bash at any host.
func (s *Server) handleProbeConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string            `json:"name"`
		BaseURL   string            `json:"base_url"`
		KeyEnv    string            `json:"key_env"`
		Key       string            `json:"key"`
		Model     string            `json:"model"`
		Headers   map[string]string `json:"headers"`
		ExtraBody map[string]any    `json:"extra_body"`
		Stream    bool              `json:"stream"`
	}
	rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := decodeLenientJSON(rawBody, &body); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	u, err := url.Parse(strings.TrimSuffix(strings.TrimSpace(body.BaseURL), "/") + "/chat/completions")
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeErr(w, http.StatusBadRequest, errors.New("base_url must be an absolute http(s) URL"))
		return
	}

	key := strings.TrimSpace(body.Key)
	envKey := ""
	if key == "" {
		envKey = envKeyFor(body.Name, body.KeyEnv)
		key = envKey
	}
	// A key the server resolved from its own environment is a credential this
	// process holds, and where it may be sent is bounded by the configuration,
	// not by whatever this request names. The trust bar in this file's header
	// — "the token holder can already point bash at any host" — died with the
	// sandbox: bash has no network and no keys now, so this endpoint is the
	// easier exfiltration path, and it has to be the stricter one. An
	// env-resolved key therefore rides only to a host providers.json already
	// names. Probing a brand-new host takes a key typed into the dialog,
	// which is the user handing it over deliberately.
	if envKey != "" && !configuredProviderHost(u.Hostname()) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf(
			"probing %s with a server-resolved key is refused: the host is not in providers.json; type the key explicitly to send it to a new host", u.Hostname()))
		return
	}
	payload := map[string]any{
		"model":    body.Model,
		"messages": []map[string]string{{"role": "user", "content": "ping"}},
		"stream":   body.Stream,
	}
	if body.Stream {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	for k, v := range body.ExtraBody {
		payload[k] = v // custom keys win — the merge rule everywhere else here
	}
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(jsonBody))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body.Stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	for k, v := range body.Headers {
		if strings.TrimSpace(k) != "" {
			req.Header.Set(k, v)
		}
	}

	start := time.Now()
	// A key is never re-posted across a redirect: the 3xx becomes the probe
	// result, surfacing a moved or misleading gateway instead of silently
	// following it wherever it points.
	client := http.DefaultClient
	if key != "" {
		client = &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "elapsed_ms": time.Since(start).Milliseconds(), "error": err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	// A streaming probe stops at 64 KiB or the deadline — whichever comes
	// first; bytes in hand before a timeout count as a live stream, not a
	// failure.
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	res := map[string]any{
		"ok":           true,
		"status":       resp.StatusCode,
		"status_text":  resp.Status,
		"content_type": resp.Header.Get("Content-Type"),
		"elapsed_ms":   time.Since(start).Milliseconds(),
		"body":         string(data),
	}
	if readErr != nil {
		if len(data) > 0 {
			res["truncated"] = true
		} else {
			res["ok"] = false
			res["error"] = readErr.Error()
		}
	}
	writeJSON(w, http.StatusOK, res)
}
