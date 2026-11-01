package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yosukeno/pogo/config"
)

type metadataProbeTestResult struct {
	OK          bool   `json:"ok"`
	Status      int    `json:"status"`
	Body        string `json:"body"`
	Error       string `json:"error"`
	Truncated   bool   `json:"truncated"`
	Method      string `json:"method"`
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
}

func metadataProbeCall(t *testing.T, body any) (*httptest.ResponseRecorder, metadataProbeTestResult) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/config/probe-metadata", bytes.NewReader(raw))
	recorder := httptest.NewRecorder()
	(&Server{}).handleProbeMetadata(recorder, request)
	var out metadataProbeTestResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid response %d: %s", recorder.Code, recorder.Body.String())
	}
	return recorder, out
}

func TestMetadataProbeFixedReadOnlyRequests(t *testing.T) {
	for _, tc := range []struct {
		preset, basePath, method, path, body string
	}{
		{"vllm", "/gateway/compatible-mode/v1///", "GET", "/gateway/compatible-mode/v1/models", ""},
		{"ollama-running", "/gateway/v1/", "GET", "/gateway/api/ps", ""},
		{"ollama-running", "/gateway/api/", "GET", "/gateway/api/ps", ""},
		{"ollama-show", "/gateway/v1", "POST", "/gateway/api/show", `{"model":"local-model"}`},
		{"ollama-show", "/gateway/api", "POST", "/gateway/api/show", `{"model":"local-model"}`},
		{"llamacpp", "/gateway/v1/", "GET", "/gateway/props", ""},
		{"llamacpp", "/gateway", "GET", "/gateway/props", ""},
	} {
		t.Run(tc.preset+tc.basePath, func(t *testing.T) {
			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				data, _ := io.ReadAll(r.Body)
				if r.Method != tc.method || r.URL.RequestURI() != tc.path || string(data) != tc.body {
					t.Errorf("request=%s %s %q, want %s %s %q", r.Method, r.URL.RequestURI(), data, tc.method, tc.path, tc.body)
				}
				if r.Header.Get("Authorization") != "Bearer synthetic-typed" || r.Header.Get("X-Detect") != "synthetic-custom" {
					t.Error("typed credential or custom header missing")
				}
				if tc.body != "" && r.Header.Get("Content-Type") != "application/json" {
					t.Error("JSON content type missing")
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"data":[{"id":"local-model","max_model_len":32768}]}`)
			}))
			defer upstream.Close()
			code, out := metadataProbeCall(t, map[string]any{"base_url": upstream.URL + tc.basePath, "preset": tc.preset, "model": "local-model", "key": "synthetic-typed", "headers": map[string]string{"X-Detect": "synthetic-custom"}, "extra_body": map[string]any{"messages": "must not escape into metadata request"}})
			if code.Code != 200 || !out.OK || out.Status != 200 || out.Method != tc.method || out.URL != upstream.URL+tc.path || out.ContentType != "application/json" || requests.Load() != 1 {
				t.Fatalf("metadata result=%+v HTTP=%d calls=%d", out, code.Code, requests.Load())
			}
		})
	}
}

func TestMetadataProbeCredentialResolutionAndNoConfigurationWrites(t *testing.T) {
	t.Setenv("METADATA_FIXTURE_API_KEY", "synthetic-env")
	t.Setenv("METADATA_FIXTURE_BASE", "unchanged-base")
	var auth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		io.WriteString(w, `{"data":[]}`)
	}))
	defer upstream.Close()
	priorProviders, priorCatalog := config.Providers(), config.Catalog()
	t.Cleanup(func() { config.SetProvidersForTest(priorProviders); config.SetCatalogForTest(priorCatalog) })
	providers := map[string]config.Provider{"metadata_fixture": {Name: "metadata_fixture", BaseURL: upstream.URL + "/v1", KeyEnv: "METADATA_FIXTURE_API_KEY", BaseURLEnv: "METADATA_FIXTURE_BASE"}}
	models := []config.Model{{ID: "local-model", Provider: "metadata_fixture", ContextWindow: 12345, MaxTokens: 456}}
	config.SetProvidersForTest(providers)
	config.SetCatalogForTest(models)
	beforeProviders, _ := json.Marshal(config.Providers())
	beforeModels, _ := json.Marshal(config.Catalog())
	beforeDefault := config.DefaultModel()
	path := filepath.Join(t.TempDir(), "providers.json")
	beforeFile := []byte(`{"models":[{"id":"local-model","context_window":12345}],"manual_only":true}`)
	if err := os.WriteFile(path, beforeFile, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.PathEnv, path)
	for _, tc := range []struct{ key, override, want string }{
		{"", "", "Bearer synthetic-env"},
		{"synthetic-typed", "", "Bearer synthetic-typed"},
		{"synthetic-typed", "synthetic-custom-auth", "synthetic-custom-auth"},
	} {
		headers := map[string]string{}
		if tc.override != "" {
			headers["Authorization"] = tc.override
		}
		code, out := metadataProbeCall(t, map[string]any{"name": "metadata_fixture", "base_url": upstream.URL + "/v1", "preset": "vllm", "key": tc.key, "headers": headers, "env": map[string]string{"METADATA_FIXTURE_API_KEY": "must-not-save"}, "context_window": 9999999})
		if code.Code != 200 || !out.OK || auth != tc.want {
			t.Fatalf("credential resolution HTTP=%d result=%+v", code.Code, out)
		}
	}
	afterFile, _ := os.ReadFile(path)
	afterProviders, _ := json.Marshal(config.Providers())
	afterModels, _ := json.Marshal(config.Catalog())
	if !bytes.Equal(beforeFile, afterFile) || !bytes.Equal(beforeProviders, afterProviders) || !bytes.Equal(beforeModels, afterModels) || config.DefaultModel() != beforeDefault || os.Getenv("METADATA_FIXTURE_API_KEY") != "synthetic-env" || os.Getenv("METADATA_FIXTURE_BASE") != "unchanged-base" {
		t.Fatal("metadata query changed manually configured file, catalog, default or environment")
	}
}

func TestMetadataProbeBindsEnvironmentKeysToConfiguredHosts(t *testing.T) {
	t.Setenv("METADATA_FIXTURE_API_KEY", "synthetic-env")
	prior := config.Providers()
	config.SetProvidersForTest(map[string]config.Provider{"fixture": {BaseURL: "https://configured.invalid/v1"}})
	t.Cleanup(func() { config.SetProvidersForTest(prior) })
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, `{}`) }))
	defer upstream.Close()
	body := map[string]any{"base_url": upstream.URL, "preset": "vllm", "key_env": "METADATA_FIXTURE_API_KEY"}
	code, _ := metadataProbeCall(t, body)
	if code.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatal("server-held key escaped to an unconfigured host")
	}
	body["key"] = "synthetic-deliberately-typed"
	code, out := metadataProbeCall(t, body)
	if code.Code != 200 || !out.OK || calls.Load() != 1 {
		t.Fatalf("explicit typed key did not permit configured metadata request: %+v", out)
	}
}

func TestMetadataProbeNeverFollowsCredentialRedirects(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls.Add(1) }))
	defer destination.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/secret-destination", http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	code, out := metadataProbeCall(t, map[string]any{"base_url": upstream.URL, "preset": "vllm", "headers": map[string]string{"X-Private-Token": "synthetic-custom-secret"}})
	if code.Code != 200 || out.OK || out.Status != http.StatusTemporaryRedirect || destinationCalls.Load() != 0 {
		t.Fatalf("redirect was followed or not exposed: %+v destinationCalls=%d", out, destinationCalls.Load())
	}
}

func TestMetadataProbePreservesFailuresAndResponseLimit(t *testing.T) {
	for _, tc := range []struct {
		name           string
		status, length int
		ok, truncated  bool
	}{
		{"upstream-error", 404, 80, false, false},
		{"exact-limit", 200, metadataProbeMaxResponse, true, false},
		{"above-limit", 200, metadataProbeMaxResponse + 20, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, strings.Repeat("x", tc.length))
			}))
			defer upstream.Close()
			code, out := metadataProbeCall(t, map[string]any{"base_url": upstream.URL, "preset": "vllm"})
			if code.Code != 200 || out.OK != tc.ok || out.Status != tc.status || out.Truncated != tc.truncated || len(out.Body) != min(tc.length, metadataProbeMaxResponse) {
				t.Fatalf("response limit/status result=%+v body length=%d", out, len(out.Body))
			}
		})
	}
}

func TestMetadataProbeRejectsInvalidRequestsBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	for _, patch := range []map[string]any{
		{"preset": "arbitrary-post"}, {"preset": "ollama-show", "model": " "},
		{"base_url": "file:///tmp/file"}, {"base_url": "http://"}, {"base_url": upstream.URL + "?key=hidden"}, {"base_url": upstream.URL + "?"},
		{"base_url": upstream.URL + "#fragment"}, {"base_url": strings.Replace(upstream.URL, "http://", "http://user:pass@", 1)},
		{"base_url": upstream.URL + "/bad\\path"}, {"base_url": upstream.URL + "/bad path"},
		{"headers": map[string]string{"Invalid Header": "value"}}, {"headers": map[string]string{"X-Valid": "invalid\r\nInjected: value"}},
		{"key_env": "not-an-env-name"}, {"key": "synthetic\ninvalid"},
	} {
		body := map[string]any{"base_url": upstream.URL, "preset": "vllm"}
		for k, v := range patch {
			body[k] = v
		}
		code, _ := metadataProbeCall(t, body)
		if code.Code != http.StatusBadRequest {
			t.Errorf("invalid request accepted: fields=%v HTTP=%d", reflect.ValueOf(patch).MapKeys(), code.Code)
		}
	}
	for _, raw := range []string{`{`, strings.Repeat(" ", (1<<20)+1)} {
		request := httptest.NewRequest(http.MethodPost, "/api/config/probe-metadata", strings.NewReader(raw))
		recorder := httptest.NewRecorder()
		(&Server{}).handleProbeMetadata(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("invalid JSON/body limit accepted HTTP=%d", recorder.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid metadata request reached an upstream")
	}
}

func TestMetadataProbeCancellationIsReportedWithoutInference(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodPost, "/api/config/probe-metadata", strings.NewReader(`{"base_url":"http://127.0.0.1:1/v1","preset":"vllm"}`)).WithContext(ctx)
	recorder := httptest.NewRecorder()
	(&Server{}).handleProbeMetadata(recorder, request)
	var out metadataProbeTestResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != 200 || out.OK || out.Error == "" {
		t.Fatalf("cancelled query=%+v HTTP=%d", out, recorder.Code)
	}
}

func TestMetadataProbeRequiresAdministrator(t *testing.T) {
	h := newMultiuserHarness(t, idleRespond)
	h.armUsers(map[string]userRecord{"alice": {Username: "alice", Token: aliceToken, Admin: true}, "bob": {Username: "bob", Token: bobToken}})
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, `{}`) }))
	defer upstream.Close()
	body, _ := json.Marshal(map[string]string{"base_url": upstream.URL, "preset": "vllm", "key": "synthetic-typed"})
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {bobToken, 403}, {aliceToken, 200}} {
		res := h.doAs(tc.token, http.MethodPost, "/api/config/probe-metadata", string(body))
		res.Body.Close()
		if res.StatusCode != tc.status {
			t.Fatalf("metadata administrator gate HTTP=%d, want %d", res.StatusCode, tc.status)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("unauthorized queries reached upstream: calls=%d", calls.Load())
	}
}
