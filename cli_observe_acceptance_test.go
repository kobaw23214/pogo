package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/memory"
	"github.com/yosukeno/pogo/observability"
	"github.com/yosukeno/pogo/skillobserve"
	"github.com/yosukeno/pogo/skills"
)

// Drive the actual CLI run, its HTTP model stream, read tool, durable stores,
// and exporter shutdown. The only simulated part is the model's decisions.
func TestCLIActualRunRecordsSkillAndDrainsOTLP(t *testing.T) {
	root := t.TempDir()
	sk := writeSkill(t, root, "fixture-skill")
	var turn atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if turn.Add(1) == 1 {
			args, _ := json.Marshal(map[string]string{"path": sk.Path})
			frame := map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{
				"tool_calls": []any{map[string]any{"index": 0, "id": "fixture-read", "type": "function", "function": map[string]any{"name": "read", "arguments": string(args)}}},
			}, "finish_reason": "tool_calls"}}}
			encoded, _ := json.Marshal(frame)
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", encoded)
		} else {
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"fixture complete\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer model.Close()
	var exportMu sync.Mutex
	var exports [][]byte
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		exportMu.Lock()
		exports = append(exports, body)
		exportMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer collector.Close()
	oldModels, oldProviders := config.Catalog(), config.Providers()
	config.SetCatalogForTest([]config.Model{{ID: "fixture-model", Provider: "fixture", ContextWindow: 100000}})
	config.SetProvidersForTest(map[string]config.Provider{"fixture": {Name: "fixture", BaseURL: model.URL}})
	defer func() { config.SetCatalogForTest(oldModels); config.SetProvidersForTest(oldProviders) }()
	for key, value := range map[string]string{
		"POGO_CLI_CALLS": "1", "POGO_MCP_CALLS": "1", "POGO_SKILL_CALLS": "1", "POGO_OTLP": "1",
		"POGO_SESSION_DIR": filepath.Join(root, "sessions"), "POGO_MCP_LOG_DB": filepath.Join(root, "history.db"),
		"POGO_MCP_FILE": filepath.Join(root, "empty-mcp.json"), "OTEL_SDK_DISABLED": "false",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": collector.URL + "/v1/traces", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL": "http/json",
		"OTEL_EXPORTER_OTLP_TRACES_HEADERS": "", "OTEL_EXPORTER_OTLP_HEADERS": "",
	} {
		t.Setenv(key, value)
	}
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout, oldDiag := os.Stdout, diagOut
	os.Stdout = writePipe
	var diagnostics bytes.Buffer
	diagOut = &diagnostics
	defer func() { os.Stdout = oldStdout; diagOut = oldDiag; readPipe.Close(); writePipe.Close() }()
	captured := make(chan []byte, 1)
	go func() { body, _ := io.ReadAll(readPipe); captured <- body }()
	err = run(options{mode: modeJSON, prompt: "/skill:fixture-skill", model: "fixture-model", cwd: root,
		maxTurns: 3, maxRuns: 1, compact: "off", contextEdit: "off", skills: skills.Options{Disabled: true, Paths: []string{sk.Dir}}, memory: memory.Options{}})
	writePipe.Close()
	os.Stdout = oldStdout
	diagOut = oldDiag
	stdout := <-captured
	if err != nil {
		t.Fatalf("actual CLI run: %v; diagnostics: %s", err, diagnostics.String())
	}
	if turn.Load() != 2 {
		t.Fatalf("model requests = %d", turn.Load())
	}
	var sessionPath string
	for _, line := range bytes.Split(bytes.TrimSpace(stdout), []byte("\n")) {
		var obj map[string]any
		if err := json.Unmarshal(line, &obj); err != nil {
			t.Fatalf("stdout is not JSONL: %q", line)
		}
		if path, ok := obj["session"].(string); ok {
			sessionPath = path
		}
	}
	if sessionPath == "" {
		t.Fatal("CLI session header missing")
	}
	db, err := observability.OpenDatabase(filepath.Join(root, "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := observability.Open(observability.Options{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	page, err := runtime.Skills.Query(context.Background(), skillobserve.Filter{Entrypoint: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	wantSession := strings.TrimSuffix(filepath.Base(sessionPath), ".jsonl")
	stages := map[string]int{}
	trace := ""
	for _, event := range page.Events {
		if event.SessionID != wantSession || event.Caller != "internal" || event.Status != "ok" || len(event.SpanID) != 16 {
			t.Fatalf("event: %+v", event)
		}
		if trace == "" {
			trace = event.TraceID
		}
		if event.TraceID != trace {
			t.Fatal("explicit load and run split their trace")
		}
		stages[event.Mechanism]++
	}
	if stages["catalog"] != 1 || stages["explicit"] != 1 || stages["file_read"] != 1 {
		t.Fatalf("actual skill operations: %+v", stages)
	}
	exportMu.Lock()
	defer exportMu.Unlock()
	if len(exports) == 0 {
		t.Fatal("CLI returned before flushing its exporter")
	}
	encodedPath, _ := json.Marshal(root)
	for _, body := range exports {
		if !json.Valid(body) || bytes.Contains(body, encodedPath[1:len(encodedPath)-1]) || bytes.Contains(body, []byte("SKILL.md")) {
			t.Fatalf("invalid or path-bearing export: %s", body)
		}
	}
}
