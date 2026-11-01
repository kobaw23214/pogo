package web

// Configuration detection is a read-only request inspector. It neither invokes
// inference nor applies discovered values to providers, models, or environment.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const metadataProbeMaxResponse = 64 << 10

var metadataProbeHeaderName = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+$`)

type metadataProbeRequest struct {
	Name    string            `json:"name"`
	BaseURL string            `json:"base_url"`
	KeyEnv  string            `json:"key_env"`
	Key     string            `json:"key"`
	Model   string            `json:"model"`
	Headers map[string]string `json:"headers"`
	Preset  string            `json:"preset"`
}

// Keep these fixed endpoint rules aligned with the curl preview. In particular,
// a gateway prefix is never removed or replaced with the URL origin.
func metadataProbeTarget(body metadataProbeRequest) (method string, target *url.URL, payload []byte, err error) {
	base := strings.TrimRight(strings.TrimSpace(body.BaseURL), "/")
	u, parseErr := url.Parse(base)
	if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" || strings.ContainsAny(base, "?#\\") || strings.IndexFunc(base, unicode.IsSpace) >= 0 || strings.IndexFunc(base, unicode.IsControl) >= 0 {
		return "", nil, nil, errors.New("base_url must be an absolute http(s) URL without credentials, query, fragment, or whitespace")
	}
	method = http.MethodGet
	switch body.Preset {
	case "vllm":
		base += "/models"
	case "ollama-running", "ollama-show":
		base = strings.TrimSuffix(base, "/v1")
		if !strings.HasSuffix(base, "/api") {
			base += "/api"
		}
		if body.Preset == "ollama-running" {
			base += "/ps"
		} else {
			model := strings.TrimSpace(body.Model)
			if model == "" {
				return "", nil, nil, errors.New("model is required for Ollama model details")
			}
			method = http.MethodPost
			base += "/show"
			payload, err = json.Marshal(map[string]string{"model": model})
			if err != nil {
				return "", nil, nil, err
			}
		}
	case "llamacpp":
		base = strings.TrimSuffix(base, "/v1") + "/props"
	default:
		return "", nil, nil, errors.New("unsupported metadata preset")
	}
	target, err = url.Parse(base)
	return method, target, payload, err
}

func (s *Server) handleProbeMetadata(w http.ResponseWriter, r *http.Request) {
	var body metadataProbeRequest
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := decodeLenientJSON(raw, &body); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	method, target, payload, err := metadataProbeTarget(body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if env := strings.TrimSpace(body.KeyEnv); env != "" && !envNameRE.MatchString(env) {
		writeErr(w, http.StatusBadRequest, errors.New("key_env must be an environment variable name"))
		return
	}
	for name, value := range body.Headers {
		if !metadataProbeHeaderName.MatchString(name) || strings.IndexFunc(value, func(r rune) bool { return r == 127 || r < 32 && r != '\t' }) >= 0 {
			writeErr(w, http.StatusBadRequest, errors.New("invalid metadata request header"))
			return
		}
	}
	key := strings.TrimSpace(body.Key)
	if key == "" {
		key = envKeyFor(body.Name, body.KeyEnv)
		// Reading an existing credential is bounded by the same trusted host
		// policy as the existing chat/vision probes; it is never saved here.
		if key != "" && !configuredProviderHost(target.Hostname()) {
			writeErr(w, http.StatusBadRequest, errors.New("metadata detection with a server-resolved key is refused: the host is not in providers.json; type the key explicitly to send it to a new host"))
			return
		}
	}
	if strings.IndexFunc(key, unicode.IsControl) >= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("invalid metadata request credential"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(payload))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.Header.Set("Accept", "application/json")
	if len(payload) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for name, value := range body.Headers {
		req.Header.Set(name, value)
	}
	// Even when Authorization is empty a custom header can carry credentials.
	// Never follow a metadata redirect to another destination.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	start := time.Now()
	response, err := client.Do(req)
	result := map[string]any{"method": method, "url": target.String(), "elapsed_ms": time.Since(start).Milliseconds()}
	if err != nil {
		result["ok"], result["error"] = false, err.Error()
		writeJSON(w, http.StatusOK, result)
		return
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, metadataProbeMaxResponse+1))
	truncated := len(data) > metadataProbeMaxResponse
	if truncated {
		data = data[:metadataProbeMaxResponse]
	}
	result["ok"] = response.StatusCode >= 200 && response.StatusCode < 300 && readErr == nil
	result["status"], result["status_text"] = response.StatusCode, response.Status
	result["content_type"], result["body"] = response.Header.Get("Content-Type"), string(data)
	result["elapsed_ms"] = time.Since(start).Milliseconds()
	if truncated || readErr != nil && len(data) > 0 {
		result["truncated"] = true
	}
	if readErr != nil {
		result["error"] = readErr.Error()
	} else if response.StatusCode < 200 || response.StatusCode >= 300 {
		result["error"] = response.Status
	}
	writeJSON(w, http.StatusOK, result)
}
