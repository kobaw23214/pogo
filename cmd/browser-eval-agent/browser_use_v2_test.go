package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

func TestBrowserUseV2ProfileUsesSharedSkillWithoutChangingOlderIdentity(t *testing.T) {
	content, err := os.ReadFile("../../skills/builtin/browser-use/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, body := skills.ParseFrontmatter(string(content))
	t.Chdir(t.TempDir())
	selected, err := selectProfile("browser-use-v2")
	if err != nil || selected.id != "pogo-agent-browser-use-v2" || selected.auth {
		t.Fatalf("unexpected profile: %+v %v", selected, err)
	}
	if selected.prompt != browserUseV2IsolationPrompt+"\n\n"+body {
		t.Fatal("new profile must use the production embedded skill")
	}
	wantHash := sha256.Sum256([]byte(body))
	if selected.sharedSkillBodySHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatal("incorrect shared skill fingerprint")
	}
	if !strings.Contains(selected.prompt, "iframe and dedicated-worker") || !strings.Contains(selected.prompt, "Controller preparation is distinct") {
		t.Fatal("new profile lost multi-target or preparation boundaries")
	}
	older, err := selectProfile("general-web")
	if err != nil || older.id != generalWebProfile || older.prompt != generalWebIsolationPrompt+"\n\n"+body || older.id == selected.id {
		t.Fatal("older general-web profile was reinterpreted")
	}
}

func TestBrowserUseV2CLIExposesTargetedDiagnosticsAndNaturalAnswer(t *testing.T) {
	selected, err := selectProfile("browser-use-v2")
	if err != nil {
		t.Fatal(err)
	}
	worker := newWorkerFixture(t)
	t.Setenv("POGO_BROWSER_EVAL_TOKEN", "worker-secret")
	target := "dt_" + strings.Repeat("a", 24) + "_2"
	worker.execute = func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(tools.BrowserReply{OK: true, Backend: "native", URL: "https://fixture.invalid/start",
			Data: json.RawMessage(`{"provenance":{"target_type":"worker"},"result":{"result":{"value":"observed-worker-value"}}}`)})
	}
	client := &fakeClient{t: t, wantSystem: selected.prompt, run: func(_ context.Context, turn int, history []llm.Message) (llm.Response, error) {
		if turn == 1 {
			raw, _ := json.Marshal(map[string]any{"action": "devtools", "method": "Runtime.evaluate", "target": target,
				"params": map[string]any{"expression": "self.fixtureValue", "returnByValue": true}})
			return llm.Response{StopReason: llm.StopToolUse, Usage: llm.Usage{Input: 9, Output: 3}, Message: llm.Message{
				Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, Name: "browser", ID: "target-call", Input: raw}},
			}}, nil
		}
		observed := false
		for _, message := range history {
			for _, block := range message.Content {
				if block.Type == llm.BlockToolResult && strings.Contains(block.Text, "observed-worker-value") {
					observed = true
				}
			}
		}
		if !observed {
			t.Error("targeted protocol result did not reach the model")
		}
		return answerResponse("已读取 worker 的结果 observed-worker-value。", llm.Usage{Input: 12, Output: 5}), nil
	}}
	raw, _ := json.Marshal(defaultInput(worker.server.URL))
	var out, diag bytes.Buffer
	code := runCLI(context.Background(), []string{"--profile", "browser-use-v2"}, bytes.NewReader(raw), &out, &diag,
		func(string, string) (llm.Client, config.Resolved, error) {
			return client, config.Resolved{Model: "fixture-model"}, nil
		})
	if code != 0 || diag.Len() != 0 {
		t.Fatalf("new profile failed: %d %s %s", code, out.String(), diag.String())
	}
	end := assertEnd(t, decodeEvents(t, out.String()), "completed")
	if end["profile"] != browserUseV2Profile || end["shared_skill_body_sha256"] != selected.sharedSkillBodySHA256 || end["outcome"] != nil {
		t.Fatal("incorrect result identity or auth-only parsing", end)
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if len(worker.actions) != 1 || worker.actions[0]["target"] != target || worker.actions[0]["method"] != "Runtime.evaluate" {
		t.Fatalf("targeted protocol request was changed: %+v", worker.actions)
	}
}
