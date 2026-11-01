package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"strings"
	"testing"
)

func assertNativeWire(t *testing.T, wire []byte, vision, diagnostics bool) {
	t.Helper()
	var definitions []struct {
		Function struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  map[string]any `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(wire, &definitions); err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 || definitions[0].Function.Name != "browser" {
		t.Fatal("expected exactly the browser tool")
	}
	fn := definitions[0].Function
	props, ok := fn.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("missing action schema")
	}
	actions := props["action"].(map[string]any)["enum"].([]any)
	has := func(values []any, want string) bool {
		for _, value := range values {
			if value == want {
				return true
			}
		}
		return false
	}
	if !has(actions, "batch") || !has(actions, "tabs") || !has(actions, "tab") || has(actions, "eval") || has(actions, "devtools") != diagnostics || has(actions, "screenshot") != vision {
		t.Fatalf("wrong native capabilities: %v", actions)
	}
	var walk func(any, int)
	walk = func(value any, depth int) {
		switch value := value.(type) {
		case map[string]any:
			if enums, ok := value["enum"].([]any); ok {
				if depth > 3 && (has(enums, "eval") || has(enums, "devtools")) {
					t.Error("standalone diagnostics advertised inside native batch")
				}
				if depth > 3 && has(enums, "batch") {
					t.Error("nested batch advertised")
				}
				if !vision && has(enums, "screenshot") {
					t.Error("screenshot advertised without vision")
				}
			}
			for key, child := range value {
				if !vision && (key == "x" || key == "y" || key == "version") {
					t.Errorf("coordinate property %s advertised without vision", key)
				}
				walk(child, depth+1)
			}
		case []any:
			for _, child := range value {
				walk(child, depth+1)
			}
		}
	}
	walk(fn.Parameters, 0)
	if !vision {
		description := strings.ToLower(fn.Description)
		if (!strings.Contains(description, "cannot") && !strings.Contains(description, "no image inspection path")) || !strings.Contains(description, "image") {
			t.Errorf("text-only description must explicitly state it cannot see images: %s", fn.Description)
		}
	}
}

func TestCapabilitySchemaAuditSamples(t *testing.T) {
	hashes := map[string]string{}
	for _, vision := range []bool{false, true} {
		name := "none"
		if vision {
			name = "direct"
		}
		t.Run(name, func(t *testing.T) {
			_, wire, err := browserRegistry(&browserLink{}, "", vision)
			if err != nil {
				t.Fatal(err)
			}
			assertNativeWire(t, wire, vision, true)
			hash := sha256.Sum256(wire)
			hashes[name] = hex.EncodeToString(hash[:])
			t.Logf("schema_%s=%s", name, wire)
			t.Logf("schema_%s_sha256=%s bytes=%d", name, hashes[name], len(wire))
		})
	}
	if hashes["none"] == hashes["direct"] {
		t.Fatal("vision capability did not change declaration")
	}
}

func TestSystemPromptUnchangedFromBaseline(t *testing.T) {
	hash := sha256.Sum256([]byte(systemPrompt))
	const baseline = "9abcefdd1e931ba0b19286c77b032587d5a02f8122bdbc5bfe7ecbd436a3ca95"
	if hex.EncodeToString(hash[:]) != baseline {
		t.Fatal("capability A/B must not modify the baseline system prompt")
	}
}

func TestNativeUnsupportedActionsNeverReachWorker(t *testing.T) {
	for _, tc := range []struct{ args, want string }{
		{`{"action":"devtools","method":"help","params":[]}`, ""},
		{`{"action":"batch","actions":[{"action":"eval","expression":"document.title"}]}`, ""},
		{`{"action":"batch","actions":[{"action":"batch","actions":[{"action":"snapshot"}]}]}`, ""},
		{`{"action":"click","x":1,"y":2,"version":"v1"}`, ""},
		// The two mid-run errors of the capability comparison (catalog-a,
		// iframe-a): each must be refused with its specific correction.
		{`{"actions":[{"action":"type","target":"e1","text":"Nova"},{"action":"click","target":"e5"}]}`, `{"action":"batch","actions":[...]}`},
		{`{"action":"wait","seconds":3}`, "seconds only limits the wait"},
	} {
		args := tc.args
		t.Run(args, func(t *testing.T) {
			worker := newWorkerFixture(t)
			client := &fakeClient{t: t, run: func(_ context.Context, n int, _ []llm.Message) (llm.Response, error) {
				if n == 1 {
					response := toolResponse("snapshot", llm.Usage{Input: 10, Output: 5})
					response.Message.Content[0].Input = json.RawMessage(args)
					return response, nil
				}
				return answerResponse("The requested operation is unavailable.", llm.Usage{Input: 10, Output: 3}), nil
			}}
			code, events, raw := runFixture(t, defaultInput(worker.server.URL), client, config.Resolved{Model: "fixture-model", SeesImages: false})
			if code != 0 {
				t.Fatal(raw)
			}
			_, actions, _ := worker.counts()
			if actions != 0 {
				t.Fatal("unsupported action reached native worker")
			}
			found := false
			for _, event := range events {
				if event["type"] == "tool_end" {
					found = true
					if event["ok"] != false {
						t.Fatal("unsupported tool call reported success")
					}
					if output, _ := event["output"].(string); tc.want != "" && !strings.Contains(output, tc.want) {
						t.Fatalf("rejection lacks the correction %q: %s", tc.want, output)
					}
				}
			}
			if !found {
				t.Fatal("missing rejected tool event")
			}
		})
	}
}
