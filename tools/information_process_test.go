//go:build unix

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/information"
)

// Actual CLI children, the actual model wire protocol, actual retained history
// and actual asset exports. Only model choices are scripted, not tool results.
func TestInformationActualChildProcessesForegroundBackgroundAndRetained(t *testing.T) {
	if testing.Short() {
		t.Skip("real CLI subprocess acceptance")
	}
	store, cwd := informationFixture(t)
	const fact = "information-real-child-7319"
	asset, err := store.PutAsset("shared.csv", "", strings.NewReader("name,value\n"+fact+",1\n"), 4096)
	if err != nil {
		t.Fatal(err)
	}
	entry := createInformation(t, store, information.CreateInput{Kind: "file", Name: "shared-data", File: &asset})
	binary := os.Getenv("POGO_SUBAGENT_TEST_BINARY")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "pogo")
		if out, err := exec.Command("go", "build", "-o", binary, "..").CombinedOutput(); err != nil {
			t.Fatalf("build real CLI: %v\n%s", err, out)
		}
	}
	var mu sync.Mutex
	requests := 0
	var problems []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request map[string]any
		if json.Unmarshal(body, &request) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		mu.Lock()
		requests++
		n := requests
		toolJSON, _ := json.Marshal(request["tools"])
		if !strings.Contains(string(toolJSON), `"name":"information"`) {
			problems = append(problems, "child lost library tool")
		}
		messageJSON, _ := json.Marshal(request["messages"])
		if n%2 == 0 && (!strings.Contains(string(messageJSON), fact) || !strings.Contains(string(messageJSON), asset.SHA256)) {
			problems = append(problems, "real information read did not reach model")
			if messages, ok := request["messages"].([]any); ok {
				for _, message := range messages {
					if fields, ok := message.(map[string]any); ok && fields["role"] == "tool" {
						t.Logf("information child request %d tool result: %v", n, fields["content"])
					}
				}
			}
		}
		mu.Unlock()
		var delta map[string]any
		finish := "stop"
		if n%2 == 1 {
			args, _ := json.Marshal(informationArgs{Action: "read", ID: entry.ID, Version: 1})
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("information-%d", n), "type": "function", "function": map[string]any{"name": InformationToolName, "arguments": string(args)}}}}
			finish = "tool_calls"
		} else {
			delta = map[string]any{"content": "Verified " + fact}
		}
		frame, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
	}))
	defer model.Close()
	cfg := map[string]any{"default": "information-fixture", "providers": map[string]any{"fixture": map[string]any{"base_url": model.URL, "key_env": ""}}, "models": []any{map[string]any{"id": "information-fixture", "provider": "fixture", "context_window": 100000, "max_tokens": 1000}}}
	raw, _ := json.Marshal(cfg)
	configPath := filepath.Join(cwd, "providers.json")
	if err = os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"POGO_CONFIG": configPath, "POGO_SESSION_DIR": filepath.Join(cwd, "sessions"), "POGO_OTLP": "0", "OTEL_SDK_DISABLED": "true", "POGO_CLI_CALLS": "0", "POGO_MCP_CALLS": "0", "POGO_SKILL_CALLS": "0", "POGO_READ_ROOTS": "", EnvInformationRoot: "/forged-global-owner", EnvInformationScope: strings.Repeat("f", 64)} {
		t.Setenv(key, value)
	}
	newTool := func() *Subagent {
		return &Subagent{Cwd: cwd, Exe: binary, Model: "information-fixture", ExplicitSkills: true, MaxTurns: 4, Timeout: 30 * time.Second, InformationRoot: store.Root(), InformationScope: store.ScopeID()}
	}
	foreground := newTool()
	for _, mode := range []string{ModeExplore, ModeEdit} {
		result, err := call(t, foreground, mode, "Read the shared information entry.")
		if err != nil || !strings.Contains(result.Text, fact) {
			t.Fatalf("foreground %s: %v %s", mode, err, result.Text)
		}
	}
	contexts, err := NewSubagentStore(filepath.Join(cwd, "sessions"), "parent-information", "owner-information")
	if err != nil {
		t.Fatal(err)
	}
	cap := newBgCapture()
	bg := NewBgRunner(context.Background(), cap.hooks())
	defer bg.Close()
	background := newTool()
	background.Store = contexts
	background.Bg = bg
	ack, err := retainedCall(t, background, subagentArgs{Action: "spawn", Mode: ModeExplore, Task: "Read the shared information entry in the background."})
	if err != nil || !details(t, ack).Background {
		t.Fatalf("background dispatch %v %+v", err, ack)
	}
	completed := cap.awaitComplete(t)
	if completed.Err != nil || !strings.Contains(completed.Text, fact) {
		t.Fatalf("background completion %v %s", completed.Err, completed.Text)
	}
	// A fresh parent tool and child process resume the exact authorized context.
	continued := newTool()
	continued.Store = contexts
	result, err := retainedCall(t, continued, subagentArgs{Action: "continue", AgentID: completed.Details.AgentID, Task: "Read version 1 again and compare it to the earlier result."})
	if err != nil || !strings.Contains(result.Text, fact) || details(t, result).Session != completed.Details.Session {
		t.Fatalf("retained continuation %v %s", err, result.Text)
	}
	continued.InformationScope = strings.Repeat("b", 64)
	if _, err = retainedCall(t, continued, subagentArgs{Action: "continue", AgentID: completed.Details.AgentID, Task: "Try a different owner."}); err == nil {
		t.Fatal("retained child crossed changed information scope")
	}
	files, err := filepath.Glob(filepath.Join(cwd, "uploads", "information", "*", "shared.csv"))
	wantCopies := 4
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		wantCopies = 1
	}
	if err != nil || len(files) != wantCopies {
		t.Fatalf("actual child asset copies=%v %v", files, err)
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), fact) {
			t.Fatal("bad actual child copy")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 8 || len(problems) > 0 {
		t.Fatalf("real model requests=%d problems=%v", requests, problems)
	}
}
