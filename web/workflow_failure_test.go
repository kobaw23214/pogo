package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/workflow"
)

func TestWorkflowOnlyCleanAgentCompletionSucceeds(t *testing.T) {
	for _, reason := range agent.AllEndReasons {
		err := workflowAgentError(context.Background(), &agent.Event{EndReason: reason})
		if (err == nil) != (reason == agent.EndCompleted) {
			t.Errorf("reason %s err=%v", reason, err)
		}
	}
	for _, end := range []*agent.Event{nil, {}, {EndReason: agent.EndCompleted, Err: errors.New("late model failure")}} {
		if workflowAgentError(context.Background(), end) == nil {
			t.Fatalf("accepted incomplete/error event: %+v", end)
		}
	}
	if err := workflowAgentError(context.Background(), &agent.Event{EndReason: agent.EndAborted, Err: errors.New("provider aborted")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("aborted reason lost cancellation: %v", err)
	}
}

func TestWorkflowModelFailuresCannotBeMaskedByExistingArtifact(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		response   func() (llm.Response, error)
	}{
		{"transport", "transport_error", func() (llm.Response, error) { return llm.Response{}, errors.New("model unavailable") }},
		{"truncated", "max_tokens", func() (llm.Response, error) {
			r := textTurn("partial output")
			r.StopReason = llm.StopMaxTokens
			return r, nil
		}},
		{"turn budget", "turn_limit", func() (llm.Response, error) { return toolTurn("read", `{"path":"old.txt"}`), nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newWorkflowHarness(t, func(context.Context, string, string) (llm.Response, error) { return tc.response() })
			a := createStudioAgent(t, h, "Failure fixture", "test", "ready")
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "old.txt"), []byte("old successful output"), 0600); err != nil {
				t.Fatal(err)
			}
			run := &wfRun{id: "run_failure", wid: "wf_failure", hub: NewHub()}
			defer run.hub.Close()
			rm := h.mgr.wfruns
			in := workflow.NodeInput{Workspace: workspace, Node: workflow.Node{ID: "n", Agent: a.ID, Task: "do new work", MaxTurns: 1, Expect: &workflow.Expect{Path: "old.txt"}}}
			res, err := rm.executor(run)(context.Background(), in)
			if err == nil || !strings.Contains(err.Error(), tc.want) || res.Artifact != "" {
				t.Fatalf("node result=%+v err=%v", res, err)
			}
			coll := newDispatchCollector(&workflow.Orchestrate{})
			dispatch := &dispatchTool{rm: rm, run: run, parent: in, cand: workflow.Candidate{Agent: a.ID}, coll: coll}
			_, err = dispatch.runDispatch(context.Background(), "do new work")
			records := coll.records()
			if err == nil || len(records) != 1 || records[0].Status != workflow.NodeFailed || !strings.Contains(records[0].Error, tc.want) {
				t.Fatalf("dispatch=%+v err=%v", records, err)
			}
		})
	}
}

func TestWorkflowDiskFailurePreservesAuthorityAndQuarantinesRun(t *testing.T) {
	for _, failure := range []string{"node_start", "node_end", "run_end"} {
		t.Run(failure, func(t *testing.T) {
			var calls atomic.Int32
			h := newWorkflowHarness(t, func(context.Context, string, string) (llm.Response, error) {
				calls.Add(1)
				return textTurn("done"), nil
			})
			a := createStudioAgent(t, h, "Disk fixture", "test", "ready")
			rm := h.mgr.wfruns
			rm.persist = func(path string, rec workflow.RunRecord) error {
				st := rec.Nodes["n"].Status
				fail := failure == "node_start" && st == workflow.NodeRunning || failure == "node_end" && st == workflow.NodeSuccess && rec.Status == workflow.RunWorking || failure == "run_end" && rec.Status == workflow.RunCompleted
				if fail {
					return &os.PathError{Op: "write", Path: path, Err: errors.New("simulated disk full")}
				}
				return writeWFRecord(path, rec)
			}
			def := workflow.Definition{ID: "wf_disk", Nodes: []workflow.Node{{ID: "n", Agent: a.ID, Task: "work"}}}
			run, err := rm.start(def, nil)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-run.done:
			case <-time.After(5 * time.Second):
				t.Fatal("disk failure did not stop run")
			}
			live, ok := rm.snapshot(run.id)
			if !ok || live.PersistenceError == "" {
				t.Fatalf("missing explicit error: %+v", live)
			}
			if live.PersistenceError != wfPersistenceFailure || strings.Contains(live.PersistenceError, rm.dir) {
				t.Fatalf("storage details exposed in public diagnostic: %s", live.PersistenceError)
			}
			disk, err := readWFRecord(rm.recordPath(run.id))
			if err != nil {
				t.Fatal(err)
			}
			if disk.Status == workflow.RunCompleted || disk.PersistenceError != "" {
				t.Fatalf("published unsaved success: %+v", disk)
			}
			withoutDiagnostic := live
			withoutDiagnostic.PersistenceError = ""
			liveJSON, _ := json.Marshal(withoutDiagnostic)
			diskJSON, _ := json.Marshal(disk)
			if !bytes.Equal(liveJSON, diskJSON) {
				t.Fatalf("live advanced past disk: live=%+v disk=%+v", live, disk)
			}
			if failure == "node_start" && calls.Load() != 0 {
				t.Fatalf("model ran before durable start: %d", calls.Load())
			}
			if _, err := rm.resumeRecord(disk, nil); !errors.Is(err, errWFRunPersistence) {
				t.Fatalf("quarantined run resumed: %v", err)
			}
			resp := h.do(http.MethodGet, "/api/workflows/"+def.ID+"/runs/"+run.id+"/stream", "")
			frame, ok, streamErr := readFrame(bufio.NewReader(resp.Body))
			resp.Body.Close()
			if streamErr != nil || !ok || frame.Type != EvError || frame.Error == "" || frame.WorkflowRun == nil || frame.WorkflowRun.PersistenceError == "" {
				t.Fatalf("persistence failure presented as success/suspension: %+v err=%v", frame, streamErr)
			}
			// Recovery on repaired storage never replays an in-flight node.
			recovery := newWFRunManager(rm.dir, h.mgr)
			recovery.recover()
			if failure == "node_end" {
				recovered, err := readWFRecord(rm.recordPath(run.id))
				if err != nil {
					t.Fatal(err)
				}
				if recovered.Status != workflow.RunFailed || len(recovery.pending) != 0 {
					t.Fatalf("in-flight work eligible for replay: %+v", recovered)
				}
			}
		})
	}
}

func TestWorkflowSinkFailureDoesNotPublishSuccess(t *testing.T) {
	rm := newWFRunManager(t.TempDir(), nil)
	rm.persist = func(string, workflow.RunRecord) error { return errors.New("disk unavailable") }
	last := workflow.RunRecord{ID: "run_sink", Status: workflow.RunWorking}
	run := &wfRun{id: last.ID, hub: NewHub(), record: last}
	defer run.hub.Close()
	next := last
	next.Status = workflow.RunCompleted
	if err := (&wfRunSink{rm: rm, run: run}).Publish(workflow.Event{Kind: workflow.EventRunEnd, Record: next}); err == nil {
		t.Fatal("disk error swallowed")
	}
	if run.record.Status != workflow.RunWorking {
		t.Fatal("live record advanced after write failure")
	}
	backlog, _, cancel := run.hub.Subscribe(0)
	defer cancel()
	for _, event := range backlog {
		if event.Type == EvWorkflowRunEnd {
			t.Fatal("published a false success event")
		}
	}
}

func TestWorkflowCancelDuringPersistenceDrainNeverRewritesLiveSnapshot(t *testing.T) {
	rm := newWFRunManager(t.TempDir(), nil)
	initial := workflow.RunRecord{ID: "run_slow_drain", WorkflowID: "wf_drain", Status: workflow.RunWorking, Definition: workflow.Definition{ID: "wf_drain", Nodes: []workflow.Node{
		{ID: "first", Agent: "ag_first", Task: "first"}, {ID: "slow", Agent: "ag_slow", Task: "slow"}, {ID: "later", Agent: "ag_later", Task: "later", After: []workflow.Dep{{Node: "first"}}},
	}}}
	if err := writeWFRecord(rm.recordPath(initial.ID), initial); err != nil {
		t.Fatal(err)
	}
	rm.persist = func(path string, rec workflow.RunRecord) error {
		if rec.Nodes["first"].Status == workflow.NodeSuccess || rec.Status == workflow.RunCancelled {
			return errors.New("disk full")
		}
		return writeWFRecord(path, rec)
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &wfRun{id: initial.ID, wid: initial.WorkflowID, ctx: ctx, cancel: cancel, hub: NewHub(), done: make(chan struct{}), record: initial}
	started, draining, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var later atomic.Int32
	exec := func(ctx context.Context, in workflow.NodeInput) (workflow.NodeResult, error) {
		switch in.Node.ID {
		case "first":
			<-started
			return workflow.NodeResult{}, nil
		case "slow":
			close(started)
			<-ctx.Done()
			close(draining)
			<-release
			return workflow.NodeResult{}, ctx.Err()
		default:
			later.Add(1)
			return workflow.NodeResult{}, nil
		}
	}
	if err := rm.drive(run, &workflow.Runner{Record: initial, Executor: exec, Sink: &wfRunSink{rm: rm, run: run}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { close(release); <-run.done })
	select {
	case <-draining:
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not reach drain")
	}
	before, _ := rm.snapshot(run.id)
	if before.PersistenceError == "" {
		t.Fatal("write failure remained invisible until drain finished")
	}
	exists, sent := rm.cancel(run.id)
	if !exists || !sent {
		t.Fatalf("cancel signal result=%v/%v", exists, sent)
	}
	live, _ := rm.snapshot(run.id)
	disk, err := readWFRecord(rm.recordPath(run.id))
	if err != nil {
		t.Fatal(err)
	}
	if live.Status != workflow.RunWorking || live.Nodes["later"].Status != workflow.NodeWaiting || disk.Nodes["later"].Status != workflow.NodeWaiting || later.Load() != 0 {
		t.Fatalf("cancel advanced unsaved state: live=%+v disk=%+v", live, disk)
	}
}
