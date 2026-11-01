package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yosukeno/pogo/config"
)

// Exercise config.Load + Resolve + the actual llm.Client against a local fake
// provider. The only credential used here is a fixed, fictional test value.
func TestProviderFileUsesRealClientWithoutLoggingCredentials(t *testing.T) {
	var calls atomic.Int32
	var capturedSchema atomic.Value
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer provider-fixture-secret" || r.Header.Get("X-Fixture") != "configured-header" {
			t.Error("configured headers were not forwarded")
		}
		raw, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Error(readErr)
		}
		var declared struct {
			Tools json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(raw, &declared); err != nil {
			t.Error(err)
		}
		capturedSchema.Store([]byte(declared.Tools))
		assertNativeWire(t, declared.Tools, false, false)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
		}
		if body["model"] != "fixture-load-model" || body["max_tokens"] != float64(256) || body["fixture_option"] != "configured-body" {
			t.Error("configured client options were not forwarded")
		}
		list, ok := body["tools"].([]any)
		if !ok || len(list) != 1 || list[0].(map[string]any)["function"].(map[string]any)["name"] != "browser" {
			t.Error("wire tool set was not browser-only")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"verified\",\"reasoning_content\":\"PRIVATE_REASONING\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":17,\"completion_tokens\":3}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer provider.Close()
	content := map[string]any{"providers": map[string]any{"fixture-loader": map[string]any{"base_url": provider.URL, "key_env": "POGO_EVAL_TEST_PROVIDER_KEY", "headers": map[string]string{"X-Fixture": "configured-header"}, "extra_body": map[string]string{"fixture_option": "configured-body"}}}, "models": []any{map[string]any{"id": "fixture-load-model", "provider": "fixture-loader", "context_window": 8192, "max_tokens": 256}}}
	data, _ := json.Marshal(content)
	path := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.PathEnv, path)
	t.Setenv("POGO_EVAL_TEST_PROVIDER_KEY", "provider-fixture-secret")
	t.Setenv("POGO_BROWSER_EVAL_TOKEN", "worker-secret")
	worker := newWorkerFixture(t)
	request := defaultInput(worker.server.URL)
	raw, _ := json.Marshal(request)
	var out, diag bytes.Buffer
	code := runCLI(context.Background(), []string{"--providers", path, "--model", "fixture-load-model"}, bytes.NewReader(raw), &out, &diag, loadModel)
	if code != 0 {
		t.Fatal(out.String())
	}
	end := assertEnd(t, decodeEvents(t, out.String()), "completed")
	wire := capturedSchema.Load().([]byte)
	hash := sha256.Sum256(wire)
	if end["tool_schema_sha256"] != hex.EncodeToString(hash[:]) || end["schema_bytes"] != float64(len(wire)) {
		t.Fatal("reported schema did not match real client serialized tool array")
	}
	if end["context_window"] != float64(8192) || end["max_tokens"] != float64(256) || end["compatibility"] == nil {
		t.Fatal("public model metadata missing")
	}
	if calls.Load() != 1 || end["answer"] != "verified" || end["usage_measured"] != true || end["usage"].(map[string]any)["input"] != float64(17) {
		t.Fatalf("unexpected real-client result: %+v", end)
	}
	for _, private := range []string{"provider-fixture-secret", provider.URL, "configured-header", "configured-body", "PRIVATE_REASONING"} {
		if strings.Contains(out.String()+diag.String(), private) {
			t.Fatal("private provider configuration or reasoning was logged")
		}
	}
}
