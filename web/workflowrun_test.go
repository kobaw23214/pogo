package web

// Workflow run end-to-end tests: the real HTTP surface, real node agents with
// real tool registries, and a scripted model behind Config.NewClient. Covers
// the management API, sequencing, artifacts, failure cascade, cancellation,
// the SSE contract, and restart recovery.

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/workflow"
)

// wfNodeClient scripts the model per persona: each node's agent carries its
// Studio name in the system prompt, which is how the dispatch keys runs.
type wfNodeClient struct {
	model  string
	stream func(ctx context.Context, system, user string) (llm.Response, error)
}

func (c *wfNodeClient) Model() string { return c.model }

func (c *wfNodeClient) Stream(ctx context.Context, system string, history []llm.Message, _ []llm.ToolSchema, onDelta func(llm.Delta)) (llm.Response, error) {
	user := ""
	if len(history) > 0 {
		// The first message is the node's brief; later turns end in tool
		// results, which would overwrite it in a naive last-message read.
		user = studioMessageText(history[0])
	}
	resp, err := c.stream(ctx, system, user)
	if err != nil {
		return llm.Response{}, err
	}
	for _, b := range resp.Message.Content {
		if b.Type == llm.BlockText && onDelta != nil {
			onDelta(llm.Delta{Kind: llm.DeltaText, Text: b.Text})
		}
	}
	return resp, nil
}

func newWorkflowHarness(t *testing.T, stream func(ctx context.Context, system, user string) (llm.Response, error), tweaks ...func(*Config)) *harness {
	t.Helper()
	wireClient := func(c *Config) {
		c.NewClient = func(rc config.Resolved, _ func(llm.RetryInfo)) llm.Client {
			return &wfNodeClient{model: rc.Model, stream: stream}
		}
	}
	return newHarness(t, scriptedTurns(textTurn("unused")), append(tweaks, wireClient)...)
}

func workflowBody(agentA, agentB string) map[string]any {
	return map[string]any{
		"name": "分析流水线", "description": "two node pipeline", "status": "ready",
		"nodes": []map[string]any{
			{"id": "analyze", "agent": agentA, "task": "Analyze the sample.", "expect": map[string]any{"path": "analyze.md"}},
			{"id": "review", "agent": agentB, "task": "Review the analysis.", "after": []string{"analyze"}},
		},
	}
}

func createWorkflow(t *testing.T, h *harness, body map[string]any) Workflow {
	t.Helper()
	resp := h.do(http.MethodPost, "/api/manage/workflows", studioBody(body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("create workflow: %d (%s)", resp.StatusCode, msg)
	}
	var wf Workflow
	if err := json.NewDecoder(resp.Body).Decode(&wf); err != nil {
		t.Fatal(err)
	}
	return wf
}

func publishWorkflow(t *testing.T, h *harness, wid string) publishPointer {
	t.Helper()
	resp := h.do(http.MethodPost, "/api/manage/workflows/"+wid+"/publish", `{"name":"v1"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("publish workflow %s: %d (%s)", wid, resp.StatusCode, msg)
	}
	var ptr publishPointer
	if err := json.NewDecoder(resp.Body).Decode(&ptr); err != nil {
		t.Fatal(err)
	}
	return ptr
}

func startWorkflowRun(t *testing.T, h *harness, wid string) string {
	t.Helper()
	// The publish model: runs start from a snapshot. Tests that only mean
	// "run the current draft" publish it right here.
	publishWorkflow(t, h, wid)
	resp := h.do(http.MethodPost, "/api/workflows/"+wid+"/runs", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("start run for %s: %d (%s)", wid, resp.StatusCode, msg)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.ID
}

func getWorkflowRun(t *testing.T, h *harness, wid, rid string) workflow.RunRecord {
	t.Helper()
	resp := h.do(http.MethodGet, "/api/workflows/"+wid+"/runs/"+rid, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("get run %s: %d (%s)", rid, resp.StatusCode, msg)
	}
	var rec workflow.RunRecord
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func waitWorkflowRun(t *testing.T, h *harness, wid, rid string) workflow.RunRecord {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		rec := getWorkflowRun(t, h, wid, rid)
		if workflow.IsTerminal(rec.Status) {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s did not settle, last = %+v", rid, rec)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWorkflowManagementAPI(t *testing.T) {
	h := newWorkflowHarness(t, func(context.Context, string, string) (llm.Response, error) {
		return textTurn("ok"), nil
	})
	a := createStudioAgent(t, h, "Node One", "instructions one", "ready")
	b := createStudioAgent(t, h, "Node Two", "instructions two", "ready")

	wf := createWorkflow(t, h, workflowBody(a.ID, b.ID))
	if !strings.HasPrefix(wf.ID, "wf_") || wf.Revision != 1 || wf.Status != "" {
		t.Fatalf("created = %+v", wf)
	}

	// List carries the new definition, with the publish view fields.
	resp := h.do(http.MethodGet, "/api/manage/workflows", "")
	var list struct {
		Workflows []workflowView `json:"workflows"`
		CanManage bool           `json:"can_manage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(list.Workflows) != 1 || list.Workflows[0].ID != wf.ID || !list.CanManage {
		t.Fatalf("list = %+v", list)
	}
	if list.Workflows[0].Published != nil || list.Workflows[0].Dirty {
		t.Fatalf("fresh workflow must be unpublished and clean: %+v", list.Workflows[0])
	}

	// Unknown agent references fail at the door.
	bad := workflowBody("ag_ghost", b.ID)
	if code := studioStatus(t, h, http.MethodPost, "/api/manage/workflows", studioBody(bad)); code != http.StatusBadRequest {
		t.Fatalf("unknown agent workflow: %d, want 400", code)
	}

	// Revision conflict is a 409.
	stale := workflowBody(a.ID, b.ID)
	stale["revision"] = 7
	if code := studioStatus(t, h, http.MethodPatch, "/api/manage/workflows/"+wf.ID, studioBody(stale)); code != http.StatusConflict {
		t.Fatalf("stale revision: %d, want 409", code)
	}

	// Delete soft-removes: gone from the list, 404 on update.
	resp = h.do(http.MethodDelete, "/api/manage/workflows/"+wf.ID, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d, want 204", resp.StatusCode)
	}
	gone := workflowBody(a.ID, b.ID)
	gone["revision"] = 1
	if code := studioStatus(t, h, http.MethodPatch, "/api/manage/workflows/"+wf.ID, studioBody(gone)); code != http.StatusNotFound {
		t.Fatalf("update deleted: %d, want 404", code)
	}
}

func TestWorkflowRunEndToEnd(t *testing.T) {
	sessionDir := t.TempDir()
	var mu sync.Mutex
	turns := map[string]int{}
	prompts := map[string]string{}
	stream := func(ctx context.Context, system, user string) (llm.Response, error) {
		persona := "one"
		if strings.Contains(system, "You are Node Two.") {
			persona = "two"
		}
		mu.Lock()
		turns[persona]++
		n := turns[persona]
		prompts[persona] = user
		mu.Unlock()
		if persona == "one" && n == 1 {
			return toolTurn("write", `{"path":"analyze.md","content":"# analysis\nlooks benign\n"}`), nil
		}
		return textTurn("done " + persona), nil
	}
	h := newWorkflowHarness(t, stream, func(c *Config) { c.SessionDir = sessionDir })
	a := createStudioAgent(t, h, "Node One", "instructions one", "ready")
	b := createStudioAgent(t, h, "Node Two", "instructions two", "ready")
	wf := createWorkflow(t, h, workflowBody(a.ID, b.ID))

	rid := startWorkflowRun(t, h, wf.ID)
	if !strings.HasPrefix(rid, "run_") {
		t.Fatalf("run id = %q", rid)
	}
	rec := waitWorkflowRun(t, h, wf.ID, rid)
	if rec.Status != workflow.RunCompleted {
		t.Fatalf("run = %q (%q)", rec.Status, rec.Error)
	}
	an, rv := rec.Nodes["analyze"], rec.Nodes["review"]
	if an.Status != workflow.NodeSuccess || rv.Status != workflow.NodeSuccess {
		t.Fatalf("nodes = %+v", rec.Nodes)
	}
	if an.Artifact != "analyze.md" || an.Summary != "done one" || rv.Summary != "done two" {
		t.Fatalf("node outcomes: %+v / %+v", an, rv)
	}
	if an.StartedAt == "" || an.EndedAt == "" || rv.StartedAt < an.EndedAt {
		t.Fatalf("sequencing: analyze %+v then review %+v", an, rv)
	}
	// The downstream prompt carries the upstream artifact manifest and the
	// deliverable contract reached the upstream node.
	mu.Lock()
	p1, p2 := prompts["one"], prompts["two"]
	mu.Unlock()
	if !strings.Contains(p1, "Write your deliverable to analyze.md") {
		t.Fatalf("node one prompt missed the contract:\n%s", p1)
	}
	if !strings.Contains(p2, "- analyze.md (node analyze)") {
		t.Fatalf("node two prompt missed the artifact manifest:\n%s", p2)
	}

	// The artifact endpoint serves the file the node wrote, and run.json on
	// disk agrees with the API's answer.
	resp := h.do(http.MethodGet, "/api/workflows/"+wf.ID+"/runs/"+rid+"/nodes/analyze/artifact", "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "looks benign") {
		t.Fatalf("artifact: %d %q", resp.StatusCode, body)
	}
	raw, err := os.ReadFile(filepath.Join(sessionDir, "wf-runs", rid, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk workflow.RunRecord
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.Status != workflow.RunCompleted || disk.Nodes["analyze"].Artifact != "analyze.md" {
		t.Fatalf("disk record = %+v", disk)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, "wf-runs", rid, "workspace", "analyze.md")); err != nil {
		t.Fatal("artifact missing from the run workspace")
	}

	// The list endpoint reports the settled run.
	resp = h.do(http.MethodGet, "/api/workflows/"+wf.ID+"/runs", "")
	var runs struct {
		Runs []workflow.RunRecord `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&runs); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(runs.Runs) != 1 || runs.Runs[0].ID != rid {
		t.Fatalf("runs = %+v", runs.Runs)
	}

	// Unknown combinations are 404, and a settled run cannot be cancelled.
	if code := studioStatus(t, h, http.MethodGet, "/api/workflows/"+wf.ID+"/runs/"+rid+"/nodes/ghost/artifact", ""); code != http.StatusNotFound {
		t.Fatalf("ghost node artifact: %d, want 404", code)
	}
	resp = h.do(http.MethodPost, "/api/workflows/"+wf.ID+"/runs/"+rid+"/cancel", "")
	var cancelOut struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cancelOut); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if cancelOut.Cancelled {
		t.Fatal("settled run reported cancelled")
	}
}

func TestWorkflowRunFailureSkipsDownstream(t *testing.T) {
	h := newWorkflowHarness(t, func(_ context.Context, system, _ string) (llm.Response, error) {
		// Node one never writes its expected file; text is not the deliverable.
		return textTurn("no file for you"), nil
	})
	a := createStudioAgent(t, h, "Node One", "instructions one", "ready")
	b := createStudioAgent(t, h, "Node Two", "instructions two", "ready")
	wf := createWorkflow(t, h, workflowBody(a.ID, b.ID))

	rid := startWorkflowRun(t, h, wf.ID)
	rec := waitWorkflowRun(t, h, wf.ID, rid)
	if rec.Status != workflow.RunFailed || !strings.Contains(rec.Error, "analyze.md") {
		t.Fatalf("run = %q (%q)", rec.Status, rec.Error)
	}
	if rec.Nodes["analyze"].Status != workflow.NodeFailed || rec.Nodes["review"].Status != workflow.NodeSkipped {
		t.Fatalf("cascade = %+v", rec.Nodes)
	}
}

func TestWorkflowRunCancel(t *testing.T) {
	h := newWorkflowHarness(t, func(ctx context.Context, _, _ string) (llm.Response, error) {
		<-ctx.Done()
		return llm.Response{StopReason: llm.StopAborted}, nil
	})
	a := createStudioAgent(t, h, "Node One", "instructions one", "ready")
	b := createStudioAgent(t, h, "Node Two", "instructions two", "ready")
	wf := createWorkflow(t, h, workflowBody(a.ID, b.ID))

	rid := startWorkflowRun(t, h, wf.ID)
	// Wait for the node to actually start before cancelling.
	deadline := time.Now().Add(10 * time.Second)
	for {
		rec := getWorkflowRun(t, h, wf.ID, rid)
		if rec.Nodes["analyze"].Status == workflow.NodeRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("node never started: %+v", rec.Nodes)
		}
		time.Sleep(10 * time.Millisecond)
	}
	resp := h.do(http.MethodPost, "/api/workflows/"+wf.ID+"/runs/"+rid+"/cancel", "")
	var out struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !out.Cancelled {
		t.Fatal("active run not cancelled")
	}
	rec := waitWorkflowRun(t, h, wf.ID, rid)
	if rec.Status != workflow.RunCancelled {
		t.Fatalf("run = %q (%q)", rec.Status, rec.Error)
	}
	if rec.Nodes["analyze"].Status != workflow.NodeCancelled || rec.Nodes["review"].Status != workflow.NodeCancelled {
		t.Fatalf("cancel cascade = %+v", rec.Nodes)
	}
}

func TestWorkflowRunStartGuards(t *testing.T) {
	h := newWorkflowHarness(t, func(context.Context, string, string) (llm.Response, error) {
		return textTurn("ok"), nil
	})
	a := createStudioAgent(t, h, "Node One", "instructions one", "ready")
	b := createStudioAgent(t, h, "Node Two", "instructions two", "ready")

	// The publish model retired the ready gate: an unpublished workflow cannot
	// run, a published one can.
	wf := createWorkflow(t, h, workflowBody(a.ID, b.ID))
	resp := h.do(http.MethodPost, "/api/workflows/"+wf.ID+"/runs", "")
	msg, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(msg), "尚未发布") {
		t.Fatalf("unpublished workflow run: %d %q, want 409", resp.StatusCode, msg)
	}
	publishWorkflow(t, h, wf.ID)
	if code := studioStatus(t, h, http.MethodPost, "/api/workflows/"+wf.ID+"/runs", ""); code != http.StatusCreated {
		t.Fatalf("published workflow run: %d, want 201", code)
	}
	if code := studioStatus(t, h, http.MethodPost, "/api/workflows/wf_ghost/runs", ""); code != http.StatusNotFound {
		t.Fatalf("ghost workflow run: %d, want 404", code)
	}
}

func TestWorkflowRunStreamLiveAndSettled(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	h := newWorkflowHarness(t, func(ctx context.Context, _, _ string) (llm.Response, error) {
		select {
		case <-release:
			return textTurn("done one"), nil
		case <-ctx.Done():
			return llm.Response{StopReason: llm.StopAborted}, nil
		}
	})
	a := createStudioAgent(t, h, "Node One", "instructions one", "ready")
	b := createStudioAgent(t, h, "Node Two", "instructions two", "ready")
	body := workflowBody(a.ID, b.ID)
	// No expect contracts here: the stream test is about events, not files.
	delete(body["nodes"].([]map[string]any)[0], "expect")
	wf := createWorkflow(t, h, body)
	rid := startWorkflowRun(t, h, wf.ID)

	resp := h.do(http.MethodGet, "/api/workflows/"+wf.ID+"/runs/"+rid+"/stream", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream: %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	frames := make(chan Event, 64)
	go func() {
		r := bufio.NewReader(resp.Body)
		for {
			e, ok, err := readFrame(r)
			if err != nil {
				close(frames)
				return
			}
			if ok {
				frames <- e
			}
		}
	}()
	next := func() Event {
		t.Helper()
		select {
		case e, open := <-frames:
			if !open {
				t.Fatal("stream ended early")
			}
			return e
		case <-time.After(15 * time.Second):
			t.Fatal("timed out waiting for a frame")
			return Event{}
		}
	}

	// First frame is the record snapshot, live node frames follow the start.
	first := next()
	if first.Type != EvWorkflowRunStart || first.WorkflowRun == nil || first.WorkflowRun.ID != rid {
		t.Fatalf("first frame = %+v", first)
	}
	once.Do(func() { close(release) })
	sawNodeFrame := false
	for {
		e := next()
		if e.Type == EvWorkflowNodeFrame && e.NodeID == "analyze" {
			sawNodeFrame = true
		}
		if e.Type == EvWorkflowRunEnd {
			if e.WorkflowRun == nil || e.WorkflowRun.Status != workflow.RunCompleted {
				t.Fatalf("run_end frame = %+v", e)
			}
			break
		}
	}
	if !sawNodeFrame {
		t.Fatal("no workflow node frames on the live stream")
	}
	resp.Body.Close()

	// A settled run's stream is the final record and a close.
	resp2 := h.do(http.MethodGet, "/api/workflows/"+wf.ID+"/runs/"+rid+"/stream", "")
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("settled stream: %d", resp2.StatusCode)
	}
	defer resp2.Body.Close()
	r := bufio.NewReader(resp2.Body)
	e, ok, err := readFrame(r)
	if err != nil || !ok {
		t.Fatalf("settled frame: %v %v", e, err)
	}
	if e.Type != EvWorkflowRunEnd || e.WorkflowRun == nil || e.WorkflowRun.Status != workflow.RunCompleted {
		t.Fatalf("settled frame = %+v", e)
	}
}

func TestWorkflowRunRecoveryMarksWorkingFailed(t *testing.T) {
	dir := t.TempDir()
	rid := "run_deadbeef0000deadbeef0000de"
	workspace := filepath.Join(dir, "wf-runs", rid, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := workflow.RunRecord{
		ID: rid, WorkflowID: "wf_x", Revision: 1, Status: workflow.RunWorking,
		Definition: workflow.Definition{ID: "wf_x", Name: "x", Revision: 1, Nodes: []workflow.Node{
			{ID: "a", Agent: "ag_a", Task: "t"}, {ID: "b", Agent: "ag_b", Task: "t", After: []workflow.Dep{{Node: "a"}}},
		}},
		Nodes: map[string]workflow.NodeState{
			"a": {Status: workflow.NodeRunning, StartedAt: "2026-01-01T00:00:00Z"},
			"b": {Status: workflow.NodeWaiting},
		},
		CreatedAt: "2026-01-01T00:00:00Z",
	}
	raw, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wf-runs", rid, "run.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// Booting a manager over that directory settles the orphan.
	newWorkflowHarness(t, func(context.Context, string, string) (llm.Response, error) {
		return textTurn("ok"), nil
	}, func(c *Config) { c.SessionDir = dir })

	back, err := readWFRecord(filepath.Join(dir, "wf-runs", rid, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if back.Status != workflow.RunFailed || back.Error != "process restarted" || back.EndedAt == "" {
		t.Fatalf("recovered = %+v", back)
	}
	if back.Nodes["a"].Status != workflow.NodeFailed || back.Nodes["a"].Error != "process restarted" {
		t.Fatalf("running node = %+v", back.Nodes["a"])
	}
	if back.Nodes["b"].Status != workflow.NodeSkipped {
		t.Fatalf("waiting node = %+v", back.Nodes["b"])
	}
}
