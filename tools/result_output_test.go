package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yosukeno/pogo/information"
)

func newPreparedOutputTestRegistry(t *testing.T) *Registry {
	t.Helper()
	base := t.TempDir()
	return &Registry{results: newResultStore(filepath.Join(base, "results")), spillDir: base}
}

func loadPreparedOutputRecord(t *testing.T, r *Registry, id string) StoredToolResult {
	t.Helper()
	if !validResultID(id) {
		t.Fatalf("missing host-assigned result ID: %q", id)
	}
	record, err := r.results.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("load committed result: %v", err)
	}
	return record
}

func decodePreparedOutputJSON(t *testing.T, text string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("invalid result envelope: %v\n%s", err, resultUTF8Prefix(text, 2048))
	}
	return value
}

// Go through the public reader, including its continuation receipts, rather
// than assuming a successful Save means the model can actually recover bytes.
func recoverPreparedOutputText(t *testing.T, r *Registry, id string) string {
	t.Helper()
	reader := &ToolResultRead{Store: r.results}
	var recovered strings.Builder
	offset := 0
	for pages := 0; pages < 1000; pages++ {
		args, _ := json.Marshal(map[string]any{"result_id": id, "view": "text", "offset": offset, "limit": 2047})
		page, err := reader.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("read text page at %d: %v", offset, err)
		}
		_, body, found := strings.Cut(page.Text, "\n---\n")
		if !found || !utf8.ValidString(body) {
			t.Fatalf("missing reader boundary or invalid UTF-8 at offset %d", offset)
		}
		details, ok := page.Details.(ToolResultReadDetails)
		if !ok || details.Offset != offset {
			t.Fatalf("unexpected page receipt at %d: %#v", offset, page.Details)
		}
		recovered.WriteString(body)
		if details.NextOffset == nil {
			if !details.Complete || recovered.Len() != details.Total {
				t.Fatalf("terminal page did not cover saved text: %#v; bytes=%d", details, recovered.Len())
			}
			return recovered.String()
		}
		if *details.NextOffset <= offset || *details.NextOffset != offset+len(body) {
			t.Fatalf("continuation failed to advance over exactly the shown bytes: %#v", details)
		}
		offset = *details.NextOffset
	}
	t.Fatal("text recovery did not terminate")
	return ""
}

func TestPrepareResultLargeOutputRecoversEveryByte(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	full := "start\n" + strings.Repeat("这是一条真实工具观察，保留中间内容。\n", 5000) + "END"
	input := Result{Text: "legacy preview", ResultID: "R" + strings.Repeat("a", 64), Payload: &ResultPayload{Text: full}}
	got := r.PrepareResult(context.Background(), "external_mcp", input, nil)
	if got.ResultID == input.ResultID || got.Payload != nil || len(got.Text) > MaxBytes {
		t.Fatalf("untrusted ID, payload or inline limit escaped preparation: ID=%q bytes=%d", got.ResultID, len(got.Text))
	}
	if got.Text == full || !strings.Contains(got.Text, got.ResultID) {
		t.Fatal("large result did not produce a host recovery view")
	}
	record := loadPreparedOutputRecord(t, r, got.ResultID)
	if record.Text != full || !record.TextComplete || record.IsError {
		t.Fatal("the saved observation differs from the full successful output")
	}
	if restored := recoverPreparedOutputText(t, r, got.ResultID); restored != full {
		t.Fatal("paged recovery lost or duplicated source bytes")
	}
}

func TestPrepareResultErrorRemainsReadableAsFailedObservation(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	full := "tool returned partial rows\n" + strings.Repeat("error evidence retained\n", 5000)
	callErr := errors.New(strings.Repeat("upstream failure 网络错误; ", 1000))
	got := r.PrepareResult(context.Background(), "external_mcp", Result{Text: full}, callErr)
	record := loadPreparedOutputRecord(t, r, got.ResultID)
	if !record.IsError || record.Error == "" || len(record.Error) > 2048 || record.Text != full || len(got.Text) > MaxBytes {
		t.Fatal("failure status, bounded synopsis or complete error evidence was lost")
	}
	args, _ := json.Marshal(map[string]any{"result_id": got.ResultID})
	read, err := (&ToolResultRead{Store: r.results}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("reading a failed original action should itself succeed: %v", err)
	}
	envelope := decodePreparedOutputJSON(t, read.Text)
	if envelope["original_is_error"] != true || envelope["original_error"] == "" {
		t.Fatal("reader hid the original failure")
	}
}

func TestPrepareResultFullTextRecoveryPreservesPayloadIncomplete(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "missing-native"}[incomplete], func(t *testing.T) {
			r := newPreparedOutputTestRegistry(t)
			full := "text recovered from the complete legacy observation\n"
			path := filepath.Join(r.spillDir, "observation.log")
			if err := os.WriteFile(path, []byte(full), 0600); err != nil {
				t.Fatal(err)
			}
			got := r.PrepareResult(context.Background(), "external_mcp", Result{Text: "preview", Payload: &ResultPayload{
				Text: "preview", FullTextPath: path, Incomplete: incomplete,
			}}, nil)
			record := loadPreparedOutputRecord(t, r, got.ResultID)
			if record.Text != full || record.TextComplete == incomplete {
				t.Fatalf("text recovery overwrote payload preservation state: %#v", record)
			}
		})
	}
}

func TestPrepareResultPayloadStorageFallbackRetainsRecoverableParts(t *testing.T) {
	for _, tc := range []struct {
		name             string
		structured       json.RawMessage
		native           json.RawMessage
		structuredKept   bool
		structuredAbsent bool
	}{
		{"invalid-native", json.RawMessage(`{"rows":[{"name":"保留结构"}]}`), json.RawMessage(`{"content":`), true, false},
		{"invalid-structured", json.RawMessage(`{"rows":`), json.RawMessage(`{"content":[]}`), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPreparedOutputTestRegistry(t)
			full := "recover every text byte\n" + strings.Repeat("middle value that is not only a preview\n", 2000)
			got := r.PrepareResult(context.Background(), "external_mcp", Result{Text: "preview", Payload: &ResultPayload{
				Text: full, Structured: tc.structured, Native: tc.native,
			}}, nil)
			record := loadPreparedOutputRecord(t, r, got.ResultID)
			if record.Text != full || record.TextComplete || !strings.Contains(got.Text, "Storage fallback:") {
				t.Fatal("fallback must preserve text, expose omissions and stay incomplete")
			}
			metadata := decodePreparedOutputJSON(t, string(record.Native))["pogo_storage_fallback"].(map[string]any)
			if metadata["native_not_preserved"] != true {
				t.Fatal("fallback disguised replacement metadata as the original native protocol")
			}
			if tc.structuredKept && string(record.Structured) != string(tc.structured) {
				t.Fatal("retainable structured data was unnecessarily dropped")
			}
			if tc.structuredAbsent && (len(record.Structured) != 0 || metadata["structured_not_preserved"] != true) {
				t.Fatal("unretainable structured data was not explicitly marked missing")
			}
			if recovered := recoverPreparedOutputText(t, r, got.ResultID); recovered != full {
				t.Fatal("fallback recovery returned only a preview")
			}
		})
	}
}

func TestPrepareResultDoesNotAdvertiseMetadataOnlyRecovery(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	got := r.PrepareResult(context.Background(), "external_mcp", Result{Payload: &ResultPayload{
		Structured: json.RawMessage(`{"unserializable":`), Native: json.RawMessage(`{"content":[]}`),
	}}, nil)
	if got.ResultID != "" || !strings.Contains(got.Text, "No result_id is available") {
		t.Fatal("an ID for host fallback metadata alone cannot recover the observation")
	}
}

func TestPrepareResultFailedStorageNeverInventsResultID(t *testing.T) {
	base := t.TempDir()
	blocked := filepath.Join(base, "regular-file")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	r := &Registry{results: newResultStore(filepath.Join(blocked, "results"))}
	got := r.PrepareResult(context.Background(), "external_mcp", Result{Payload: &ResultPayload{
		Text: strings.Repeat("full output\n", 10000), Structured: json.RawMessage(`{"rows":[]}`), Native: json.RawMessage(`{}`),
	}}, nil)
	if got.ResultID != "" || !strings.Contains(got.Text, "No result_id is available") || len(got.Text) > MaxBytes {
		t.Fatal("storage failure should retain a bounded explicit failure without a trusted ID")
	}
}

func TestPrepareResultHostPagesAreNotTruncatedAgain(t *testing.T) {
	full := strings.Repeat("one returned page\n", MaxLines+100)
	for _, name := range []string{"read", "ls", "find", "grep", "todo", ToolResultReadName} {
		t.Run(name, func(t *testing.T) {
			r := newPreparedOutputTestRegistry(t)
			got := r.PrepareResult(context.Background(), name, Result{Text: full}, nil)
			if got.Text != full || got.ResultID != "" {
				t.Fatal("a bounded host page must retain its own bytes and pagination semantics")
			}
		})
	}
}

func TestPrepareResultInvalidUTF8IsMarkedIncomplete(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	got := r.PrepareResult(context.Background(), "external_mcp", Result{Text: "before\xffafter"}, nil)
	record := loadPreparedOutputRecord(t, r, got.ResultID)
	if !utf8.ValidString(record.Text) || record.TextComplete || !strings.Contains(record.Text, "\uFFFD") {
		t.Fatal("lossy UTF-8 normalization must remain explicit")
	}
}

func TestPrepareResultEmptySuccessHasNoSyntheticEvidence(t *testing.T) {
	for _, name := range []string{"bash", "read", "external_mcp"} {
		for _, store := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/without-store", true: "/with-store"}[store], func(t *testing.T) {
				r := &Registry{}
				if store {
					r = newPreparedOutputTestRegistry(t)
				}
				got := r.PrepareResult(context.Background(), name, Result{}, nil)
				if got.Text != "" || got.ResultID != "" {
					t.Fatalf("empty success became synthetic readable evidence: text=%q id=%q", got.Text, got.ResultID)
				}
			})
		}
	}
}

func TestPrepareResultEmptyLegacyPreviewStillRecoversFullText(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	full := "actual output retained outside the empty legacy preview\n"
	path := filepath.Join(r.spillDir, "observation.log")
	if err := os.WriteFile(path, []byte(full), 0600); err != nil {
		t.Fatal(err)
	}
	got := r.PrepareResult(context.Background(), "bash", Result{Details: BashDetails{Truncated: true, FullOutputPath: path}}, nil)
	record := loadPreparedOutputRecord(t, r, got.ResultID)
	if record.Text != full || !record.TextComplete {
		t.Fatal("a genuinely empty success must remain distinct from a recoverable legacy full-output path")
	}
}

// Keep the existing multi-mode Information acceptance assertion intact. This
// smaller wire-level case makes any missing observation diagnosable instead of
// having a scripted final answer conceal a broken tool-result/model boundary.
func TestPrepareResultInformationReadSurvivesActualChildWire(t *testing.T) {
	if testing.Short() || runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("actual CLI subprocess regression on supported Unix platforms")
	}
	store, cwd := informationFixture(t)
	const fact = "prepared-information-wire-9417"
	asset, err := store.PutAsset("shared.csv", "", strings.NewReader("name,value\n"+fact+",1\n"), 4096)
	if err != nil {
		t.Fatal(err)
	}
	entry := createInformation(t, store, information.CreateInput{Kind: "file", Name: "shared-data", File: &asset})
	binary := os.Getenv("POGO_SUBAGENT_TEST_BINARY")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "pogo")
		if output, err := exec.Command("go", "build", "-o", binary, "..").CombinedOutput(); err != nil {
			t.Fatalf("build actual child: %v\n%s", err, output)
		}
	}
	var requests atomic.Int64
	observed := make(chan string, 1)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, "bad model request", http.StatusBadRequest)
			return
		}
		n := requests.Add(1)
		var delta map[string]any
		finish := "stop"
		if n == 1 {
			args, _ := json.Marshal(informationArgs{Action: "read", ID: entry.ID, Version: 1})
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "information-1", "type": "function", "function": map[string]any{"name": InformationToolName, "arguments": string(args)}}}}
			finish = "tool_calls"
		} else {
			messages, _ := json.Marshal(request["messages"])
			select {
			case observed <- string(messages):
			default:
			}
			delta = map[string]any{"content": "Verified " + fact}
		}
		frame, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
	}))
	defer model.Close()
	config, _ := json.Marshal(map[string]any{"default": "information-fixture", "providers": map[string]any{"fixture": map[string]any{"base_url": model.URL, "key_env": ""}}, "models": []any{map[string]any{"id": "information-fixture", "provider": "fixture", "context_window": 100000, "max_tokens": 1000}}})
	configPath := filepath.Join(cwd, "providers.json")
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"POGO_CONFIG": configPath, "POGO_SESSION_DIR": filepath.Join(cwd, "sessions"), "POGO_OTLP": "0", "OTEL_SDK_DISABLED": "true", "POGO_CLI_CALLS": "0", "POGO_MCP_CALLS": "0", "POGO_SKILL_CALLS": "0", "POGO_READ_ROOTS": "", EnvInformationRoot: "/forged-global-owner", EnvInformationScope: strings.Repeat("f", 64)} {
		t.Setenv(key, value)
	}
	child := &Subagent{Cwd: cwd, Exe: binary, Model: "information-fixture", ExplicitSkills: true, MaxTurns: 4, Timeout: 30 * time.Second, InformationRoot: store.Root(), InformationScope: store.ScopeID()}
	if _, err := call(t, child, ModeExplore, "Read the shared information entry."); err != nil {
		t.Fatalf("actual child failed: %v", err)
	}
	select {
	case messages := <-observed:
		if !strings.Contains(messages, fact) || !strings.Contains(messages, asset.SHA256) {
			t.Fatalf("actual information observation disappeared before model request (fact=%t sha=%t):\n%s", strings.Contains(messages, fact), strings.Contains(messages, asset.SHA256), messages)
		}
	default:
		t.Fatal("child never requested a model turn after its information read")
	}
}
