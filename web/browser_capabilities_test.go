package web

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func TestBrowserBackendMetadataDoesNotAuthorizeExecution(t *testing.T) {
	gate := NewBrowserGate(nil)
	if tools.BrowserBackendOf(gate) != "" || tools.BrowserBackendOf(subPane{gate}) != "" {
		t.Fatal("unconfigured gate or child advertised an executor")
	}
	gate.executor = newNativeBrowser(nil) // No worker needed to advertise capability.
	if tools.BrowserBackendOf(gate) != tools.BrowserBackendNative || tools.BrowserBackendOf(subPane{gate}) != tools.BrowserBackendNative {
		t.Fatal("native gate or child lost its backend")
	}
	if _, err := gate.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`)); err != ErrBrowserNotArmed {
		t.Fatalf("backend metadata changed the task grant: %v", err)
	}
	if _, err := (subPane{gate}).Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`)); err != ErrBrowserNotArmed {
		t.Fatalf("subagent backend metadata changed the task grant: %v", err)
	}
}

type browserSchemaClient struct{ declarations []string }

func (*browserSchemaClient) Model() string { return "capability-fixture" }
func (c *browserSchemaClient) Stream(_ context.Context, _ string, _ []llm.Message, schemas []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	for _, schema := range schemas {
		if schema.Name == "browser" {
			raw, _ := llm.ToolsWireJSON([]llm.ToolSchema{schema})
			c.declarations = append(c.declarations, string(raw))
		}
	}
	return plainTurn()
}

func TestSessionBrowserContractTracksModelAndRegisteredImageHelper(t *testing.T) {
	for _, helper := range []bool{false, true} {
		name := "without-helper"
		if helper {
			name = "with-helper"
		}
		t.Run(name, func(t *testing.T) {
			oldVision := visionModelName
			visionModelName = func() string {
				if helper {
					return "fixture-vision-helper"
				}
				return ""
			}
			t.Cleanup(func() { visionModelName = oldVision })
			client := &browserSchemaClient{}
			h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() }, func(c *Config) {
				c.BrowserURL, c.BrowserToken = "http://127.0.0.1:9", "test-only-token"
				c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return client }
			})
			no, yes := false, true
			config.SetCatalogForTest([]config.Model{
				{ID: "k3", Provider: "kimi", ContextWindow: 200000, Vision: &no},
				{ID: "fixture-visual", Provider: "kimi", ContextWindow: 200000, Vision: &yes},
			})
			s := h.session(h.createSession())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			observe := func(wantImages bool) {
				t.Helper()
				for range s.agent.Run(ctx, "Return without operating the browser") {
				}
				wire := client.declarations[len(client.declarations)-1]
				if strings.Contains(wire, `"eval"`) || strings.Contains(wire, `"expression"`) || !strings.Contains(wire, `"devtools"`) {
					t.Fatal("production native session lost standalone diagnostics")
				}
				if strings.Contains(wire, `"screenshot"`) != wantImages {
					t.Fatalf("screenshot availability mismatches image path, want=%v", wantImages)
				}
			}
			observe(helper)
			if _, err := s.SetModel("fixture-visual"); err != nil {
				t.Fatal(err)
			}
			observe(true)
			if _, err := s.SetModel("k3"); err != nil {
				t.Fatal(err)
			}
			observe(helper)
		})
	}
}
