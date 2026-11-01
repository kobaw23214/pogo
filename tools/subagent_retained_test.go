//go:build unix

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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/session"
)

func retainedTool(t *testing.T, root, parent, owner string) *Subagent {
	t.Helper()
	store, err := NewSubagentStore(filepath.Join(root, "sessions"), parent, owner)
	if err != nil {
		t.Fatal(err)
	}
	return &Subagent{Store: store, Cwd: root, Model: "fixture-model", ExplicitSkills: true}
}

func retainedCall(t *testing.T, s *Subagent, a subagentArgs) (Result, error) {
	t.Helper()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateArgs(s, b); err != nil {
		return Result{}, err
	}
	return s.Execute(context.Background(), b)
}

func TestRetainedSubagentScopesLeasesRecoveryAndArchive(t *testing.T) {
	root := t.TempDir()
	s := retainedTool(t, root, "parent-a", "owner-a")
	r, err := s.prepareRetained(subagentArgs{Action: "spawn", Mode: ModeExplore, Task: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	id := r.record.AgentID
	t.Cleanup(func() { r.lease.Close() })
	for _, tc := range []struct{ parent, owner string }{{"parent-b", "owner-a"}, {"parent-a", "owner-b"}} {
		other := retainedTool(t, root, tc.parent, tc.owner)
		if _, err := other.prepareRetained(subagentArgs{Action: "continue", AgentID: id, Task: "followup"}); err == nil {
			t.Fatal("child crossed parent/owner boundary")
		}
		list, err := other.Store.list()
		if err != nil || len(list) != 0 {
			t.Fatalf("foreign list: %+v %v", list, err)
		}
		if _, err := other.Store.Lookup(id); !errors.Is(err, ErrSubagentNotFound) {
			t.Fatalf("foreign lookup: %v", err)
		}
	}
	// A second Store instance simulates a concurrent manager/process. It cannot
	// resume or archive a running child, or mistake its live lease for a crash.
	second := retainedTool(t, root, "parent-a", "owner-a")
	if _, err := second.prepareRetained(subagentArgs{Action: "continue", AgentID: id, Task: "race"}); !errors.Is(err, errSubagentContextBusy) {
		t.Fatalf("concurrent continuation: %v", err)
	}
	if _, err := second.Store.archive(id); !errors.Is(err, errSubagentContextBusy) {
		t.Fatalf("active archive: %v", err)
	}
	list, err := second.Store.list()
	if err != nil || len(list) != 1 || list[0].Status != "queued" {
		t.Fatalf("live context: %+v %v", list, err)
	}
	if info, err := second.Store.Lookup(id); err != nil || info.Session != r.record.Session {
		t.Fatalf("read-only active lookup: %+v %v", info, err)
	}
	// Releasing the process lease without settlement is a crash. Inspection
	// records interrupted but never starts another run or discards the history.
	r.lease.Close()
	second = retainedTool(t, root, "parent-a", "owner-a")
	if info, err := second.Store.Lookup(id); err != nil || info.Status != "interrupted" {
		t.Fatalf("constructor recovery: %+v %v", info, err)
	}
	list, err = second.Store.list()
	if err != nil || list[0].Status != "interrupted" {
		t.Fatalf("crash recovery: %+v %v", list, err)
	}
	resumed, err := second.prepareRetained(subagentArgs{Action: "continue", AgentID: id, Task: "inspect partial work and continue"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.record.Session != r.record.Session || resumed.record.RunID == r.record.RunID {
		t.Fatal("continuation lost transcript or reused run ID")
	}
	if _, err := resumed.finish(second, Result{Text: "done"}, nil); err != nil {
		t.Fatal(err)
	}
	archived, err := second.Store.archive(id)
	if err != nil || archived.Status != "archived" {
		t.Fatalf("archive: %+v %v", archived, err)
	}
	if _, err := os.Stat(r.record.Session); err != nil {
		t.Fatal("archive deleted transcript")
	}
	if info, err := second.Store.Lookup(id); err != nil || info.Status != "archived" {
		t.Fatalf("archived lookup: %+v %v", info, err)
	}
	if _, err := second.prepareRetained(subagentArgs{Action: "continue", AgentID: id, Task: "bad"}); err == nil {
		t.Fatal("archived child ran")
	}
}

func TestRetainedSubagentContinuationCannotBroadenPermissions(t *testing.T) {
	root := t.TempDir()
	s := retainedTool(t, root, "parent", "owner")
	r, err := s.prepareRetained(subagentArgs{Action: "spawn", Mode: ModeExplore, Task: "read"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.finish(s, Result{Text: "done"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, a := range []subagentArgs{
		{Action: "continue", AgentID: r.record.AgentID, Mode: ModeEdit, Task: "write"},
		{Action: "continue", AgentID: "../outside", Task: "path traversal"},
	} {
		if _, err := s.prepareRetained(a); err == nil {
			t.Fatalf("accepted escalation: %+v", a)
		}
	}
	s.SkillPaths = []string{"/new/capability"}
	if _, err := s.prepareRetained(subagentArgs{Action: "continue", AgentID: r.record.AgentID, Task: "read"}); err == nil {
		t.Fatal("changed capability boundary accepted")
	}
	s.StoreError = errors.New("disk unavailable")
	if _, err := retainedCall(t, s, subagentArgs{Action: "spawn", Mode: ModeExplore, Task: "read"}); err == nil || !strings.Contains(err.Error(), "storage is unavailable") {
		t.Fatal("storage failure degraded to one-shot spawn")
	}
}

func TestRetainedSubagentQueuedCancellationSettlesAndRemainsReusable(t *testing.T) {
	s := retainedTool(t, t.TempDir(), "parent", "owner")
	cap := newBgCapture()
	s.Bg = NewBgRunner(context.Background(), cap.hooks())
	t.Cleanup(s.Bg.Close)
	s.Concurrency = 1
	if err := s.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.release()
	s.Exe = "must-not-start"
	res, err := retainedCall(t, s, subagentArgs{Action: "spawn", Mode: ModeExplore, Task: "queued task"})
	if err != nil {
		t.Fatal(err)
	}
	d := details(t, res)
	if !d.Background || d.AgentID == "" || d.RunID == "" {
		t.Fatalf("dispatch details: %+v", d)
	}
	if !s.Bg.Cancel(d.RunID) {
		t.Fatal("cancel missed queued retained run")
	}
	out := cap.awaitComplete(t)
	if out.Err == nil || out.Details.AgentID != d.AgentID || out.Details.Status != "idle" {
		t.Fatalf("cancel outcome: %+v", out)
	}
	list, err := s.Store.list()
	if err != nil || len(list) != 1 || list[0].LastRunStatus != "cancelled" {
		t.Fatalf("cancel registry: %+v %v", list, err)
	}
	next, err := s.prepareRetained(subagentArgs{Action: "continue", AgentID: d.AgentID, Task: "followup"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = next.finish(s, Result{}, context.Canceled); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRetainedSubagentSchemaAndScheduling(t *testing.T) {
	s := retainedTool(t, t.TempDir(), "parent", "owner")
	for _, raw := range []string{`{"action":"list"}`, `{"action":"archive","agent_id":"agent_a"}`, `{"action":"continue","agent_id":"agent_a","task":"followup"}`, `{"action":"spawn","mode":"explore","task":"read"}`} {
		if err := ValidateArgs(s, json.RawMessage(raw)); err != nil {
			t.Fatalf("retained schema rejected %s: %v", raw, err)
		}
	}
	if got := s.ExecutionModeForArgs(json.RawMessage(`{"action":"spawn","mode":"explore","task":"read"}`)); got != Parallel {
		t.Fatal("explicit explore spawn no longer parallel")
	}
	if got := s.ExecutionModeForArgs(json.RawMessage(`{"action":"spawn","mode":"edit","task":"write"}`)); got != Sequential {
		t.Fatal("edit spawn lost write barrier")
	}
	r, err := s.prepareRetained(subagentArgs{Action: "spawn", Mode: ModeExplore, Task: "read"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.finish(s, Result{}, nil)
	b, _ := json.Marshal(subagentArgs{Action: "continue", AgentID: r.record.AgentID, Task: "followup"})
	if s.ExecutionModeForArgs(b) != Parallel {
		t.Fatal("read-only continuation lost parallel scheduling")
	}
}

// This drives the actual Pogo binary, HTTP model protocol, read-only tool
// registry, process exit/restart and JSONL restoration. The endpoint controls
// only model decisions; no mocked child output or in-memory history is used.
func TestRetainedSubagentRealProcessContinuesContextAndUsage(t *testing.T) {
	if testing.Short() {
		t.Skip("real CLI subprocess acceptance")
	}
	root := t.TempDir()
	binary := os.Getenv("POGO_SUBAGENT_TEST_BINARY")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "pogo")
		cmd := exec.Command("go", "build", "-o", binary, "..")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build actual Pogo: %v\n%s", err, out)
		}
	}
	const fact = "cobalt-orchid-7319"
	factPath := filepath.Join(root, "fact.txt")
	if err := os.WriteFile(factPath, []byte(fact), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []map[string]any
	var requestErrors []string
	cancelEntered := make(chan struct{})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		mu.Lock()
		requests = append(requests, req)
		n := len(requests)
		if req["model"] != "fixture-model" {
			requestErrors = append(requestErrors, "continued with a changed model")
		}
		toolJSON, _ := json.Marshal(req["tools"])
		for _, forbidden := range []string{`"name":"bash"`, `"name":"write"`, `"name":"edit"`, `"name":"subagent"`, `"name":"todo"`} {
			if strings.Contains(string(toolJSON), forbidden) {
				requestErrors = append(requestErrors, "read-only child gained "+forbidden)
			}
		}
		msgJSON, _ := json.Marshal(req["messages"])
		if n >= 3 && !strings.Contains(string(msgJSON), fact) {
			requestErrors = append(requestErrors, "restored model request lost the prior fact")
		}
		mu.Unlock()
		if n == 5 {
			close(cancelEntered)
			<-r.Context().Done()
			return
		}
		var delta map[string]any
		finish := "stop"
		switch n {
		case 1:
			args, _ := json.Marshal(map[string]any{"path": factPath})
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "read-fact", "type": "function", "function": map[string]any{"name": "read", "arguments": string(args)}}}}
			finish = "tool_calls"
		case 2:
			if !strings.Contains(string(msgJSON), fact) {
				mu.Lock()
				requestErrors = append(requestErrors, "real read tool did not return fact")
				mu.Unlock()
			}
			delta = map[string]any{"content": "Remembered the file fact: " + fact + " [1](#cite:S1)"}
		case 3:
			// Even an explicit model request cannot reach an absent write tool.
			args, _ := json.Marshal(map[string]any{"path": filepath.Join(root, "forbidden.txt"), "content": "must not be written"})
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "attempt-write", "type": "function", "function": map[string]any{"name": "write", "arguments": string(args)}}}}
			finish = "tool_calls"
		default:
			delta = map[string]any{"content": "The retained fact is " + fact + ". [1](#cite:S1) The write was unavailable."}
		}
		frame := map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}}
		encoded, _ := json.Marshal(frame)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", encoded)
	}))
	defer model.Close()
	cfg := map[string]any{"default": "fixture-model", "providers": map[string]any{"fixture": map[string]any{"base_url": model.URL, "key_env": ""}}, "models": []any{map[string]any{"id": "fixture-model", "provider": "fixture", "context_window": 100000, "max_tokens": 1000}, map[string]any{"id": "changed-parent-model", "provider": "fixture", "context_window": 100000, "max_tokens": 1000}}}
	configPath := filepath.Join(root, "providers.json")
	encoded, _ := json.Marshal(cfg)
	if err := os.WriteFile(configPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"POGO_CONFIG": configPath, "POGO_SESSION_DIR": filepath.Join(root, "sessions"), "POGO_OTLP": "0", "OTEL_SDK_DISABLED": "true", "POGO_CLI_CALLS": "0", "POGO_MCP_CALLS": "0", "POGO_SKILL_CALLS": "0", "POGO_READ_ROOTS": ""} {
		t.Setenv(key, value)
	}
	s := retainedTool(t, root, "parent-a", "owner-a")
	s.Exe, s.Timeout, s.MaxTurns = binary, 30*time.Second, 5
	cap := newBgCapture()
	s.Bg = NewBgRunner(context.Background(), cap.hooks())
	t.Cleanup(s.Bg.Close)
	ack, err := retainedCall(t, s, subagentArgs{Action: "spawn", Mode: ModeExplore, Task: "Read fact.txt and remember its exact content."})
	if err != nil {
		t.Fatalf("first process: %v", err)
	}
	completed := cap.awaitComplete(t)
	if completed.Err != nil {
		t.Fatal(completed.Err)
	}
	first := Result{Text: completed.Text, Details: completed.Details}
	d1 := details(t, first)
	if details(t, ack).AgentID != d1.AgentID || details(t, ack).RunID != d1.RunID {
		t.Fatal("background completion lost stable identity")
	}
	if d1.InputTok != 200 || d1.OutTok != 20 || d1.Status != "idle" || !strings.Contains(first.Text, fact) {
		t.Fatalf("first result: %+v %s", d1, first.Text)
	}
	// Re-open the host registry too: the test does not retain a child process or
	// an in-memory Agent between delegations, and changes the parent's model.
	s2 := retainedTool(t, root, "parent-a", "owner-a")
	s2.Exe, s2.Timeout, s2.MaxTurns = binary, 30*time.Second, 5
	s2.SetParentModel("changed-parent-model")
	second, err := retainedCall(t, s2, subagentArgs{Action: "continue", AgentID: d1.AgentID, Task: "What exact fact did you read before? Do not read the file again.", Wait: true})
	if err != nil {
		t.Fatalf("continued process: %v", err)
	}
	d2 := details(t, second)
	if d2.AgentID != d1.AgentID || d2.Session != d1.Session || d2.RunID == d1.RunID || d2.Model != "fixture-model" || d2.Status != "idle" {
		t.Fatalf("identity/model/context changed: first=%+v second=%+v", d1, d2)
	}
	if d2.InputTok != 200 || d2.OutTok != 20 {
		t.Fatalf("old tokens charged again: %+v", d2)
	}
	if !strings.Contains(second.Text, fact) {
		t.Fatal("second process did not see retained context")
	}
	if _, err := os.Stat(filepath.Join(root, "forbidden.txt")); !os.IsNotExist(err) {
		t.Fatal("read-only child wrote a file")
	}
	transcript, err := session.Open(d2.Session)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := transcript.UsageTotals()
	if u.Input != 400 || u.Output != 40 {
		t.Fatalf("transcript totals: %+v", u)
	}
	for _, tc := range []struct{ parent, owner string }{{"parent-b", "owner-a"}, {"parent-a", "owner-b"}} {
		other := retainedTool(t, root, tc.parent, tc.owner)
		other.Exe = binary
		if _, err := retainedCall(t, other, subagentArgs{Action: "continue", AgentID: d1.AgentID, Task: "read foreign history", Wait: true}); err == nil {
			t.Fatal("foreign continuation succeeded")
		}
	}
	if _, err := retainedCall(t, s2, subagentArgs{Action: "continue", AgentID: d1.AgentID, Mode: ModeEdit, Task: "change permissions", Wait: true}); err == nil {
		t.Fatal("mode escalation succeeded")
	}
	// Cancel a real in-flight child process, then explicitly start another run.
	// The running child's lease rejects an overlapping follow-up; cancellation
	// releases it without erasing the previous useful conversation.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	raw, _ := json.Marshal(subagentArgs{Action: "continue", AgentID: d1.AgentID, Task: "wait-for-cancel", Wait: true})
	go func() { res, err := s2.Execute(ctx, raw); done <- outcome{res, err} }()
	select {
	case <-cancelEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("real child did not reach cancellation point")
	}
	if _, err := retainedCall(t, s2, subagentArgs{Action: "continue", AgentID: d1.AgentID, Task: "overlap", Wait: true}); !errors.Is(err, errSubagentContextBusy) {
		t.Fatalf("overlapping actual run: %v", err)
	}
	cancel()
	select {
	case out := <-done:
		if out.err == nil || details(t, out.res).AgentID != d1.AgentID {
			t.Fatalf("cancelled actual run: %+v", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("real child did not stop")
	}
	afterCancel, err := retainedCall(t, s2, subagentArgs{Action: "continue", AgentID: d1.AgentID, Task: "resume-after-cancel: recall the original fact", Wait: true})
	if err != nil || !strings.Contains(afterCancel.Text, fact) {
		t.Fatalf("after cancel: %v %s", err, afterCancel.Text)
	}
	if d := details(t, afterCancel); d.InputTok != 100 || d.OutTok != 10 {
		t.Fatalf("resume after cancellation usage: %+v", d)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 6 || len(requestErrors) > 0 {
		t.Fatalf("model requests=%d errors=%v", len(requests), requestErrors)
	}
}
