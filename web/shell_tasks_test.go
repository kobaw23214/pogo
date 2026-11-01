package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/tools"
)

func TestHostedExecutionMetadataKeepsShellSynchronous(t *testing.T) {
	for _, kind := range []string{"agent_workflow", "schedule", "sentinel"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			var store *session.Store
			var err error
			switch kind {
			case "agent_workflow":
				store, err = session.CreateAgentWorkflowWebConversation(dir, dir, "k3", session.AgentWorkflowIdentity{RunID: "fixture-run"})
			case "schedule":
				store, err = session.CreateBackgroundTaskWebConversation(dir, dir, "k3", session.BackgroundTaskIdentity{Source: "schedule"})
			case "sentinel":
				store, err = session.CreateSentinelWebConversation(dir, dir, "k3", session.SentinelIdentity{WatchID: "fixture-watch"})
			}
			if err != nil {
				t.Fatal(err)
			}
			sess := &Session{ID: "fixture", mgr: &Manager{cfg: Config{SessionDir: dir}}, store: store, hub: NewHub()}
			sess.initShellJobs(context.Background())
			if sess.shellJobs != nil {
				sess.shellJobs.Close()
				t.Fatal("hosted execution was granted detached command execution")
			}
		})
	}
}

func TestShellTaskStateSurvivesRunEndAndRejectsLateOutput(t *testing.T) {
	h := NewHub()
	record := tools.ShellJobRecord{TaskID: "shell_fixture", Status: "running", StartedAt: 1000, Revision: 1, Background: true}
	h.Publish(Event{Type: EvRunStart, RunID: "run-1"})
	h.Publish(Event{Type: EvShellTaskStart, ShellTask: shellJobRecordPtr(record)})
	record.Revision, record.OutputTail = 2, "still running\n"
	h.Publish(Event{Type: EvShellTaskOutput, ShellTask: shellJobRecordPtr(record)})
	h.Publish(Event{Type: EvRunEnd})
	if tasks := h.Snapshot().ShellTasks; len(tasks) != 1 || tasks[0].OutputTail != "still running\n" {
		t.Fatalf("run end lost the command state: %+v", tasks)
	}
	code := 0
	record.Status, record.Revision, record.ExitCode, record.OutputTail = "completed", 3, &code, "finished\n"
	h.Publish(Event{Type: EvShellTaskEnd, ShellTask: shellJobRecordPtr(record)})
	late := record
	late.Status, late.Revision, late.OutputTail = "running", 2, "stale\n"
	h.Publish(Event{Type: EvShellTaskOutput, ShellTask: shellJobRecordPtr(late)})
	snapshot := h.Snapshot()
	if got := snapshot.ShellTasks[0]; got.Status != "completed" || got.OutputTail != "finished\n" {
		t.Fatalf("late output replaced the terminal record: %+v", got)
	}
	*snapshot.ShellTasks[0].ExitCode = 99
	if got := *h.Snapshot().ShellTasks[0].ExitCode; got != 0 {
		t.Fatalf("snapshot retained a shared exit-code pointer: %d", got)
	}
	for _, event := range h.log {
		if event.Type == EvShellTaskOutput {
			t.Fatal("incremental command output entered the replay log")
		}
	}
	h.Reset(nil, llm.Usage{})
	if got := h.Snapshot().ShellTasks; len(got) != 1 || got[0].Status != "completed" {
		t.Fatalf("conversation reset lost session-owned command state: %+v", got)
	}
}

func installShellTaskFixture(t *testing.T, m *Manager, sess *Session) string {
	t.Helper()
	if sess.shellJobs == nil {
		t.Fatal("session did not receive a shell runner")
	}
	sess.shellJobs.Close()
	id := "shell_" + strings.Repeat("a", 32)
	dir := m.shellJobStoreDir(sess.ID, sess.owner)
	disk := map[string]any{
		"version": 1, "task_id": id, "command": "private fixture command", "workdir": sess.cwd,
		"status": "completed", "started_at": time.Now().Add(-time.Second).UnixMilli(), "ended_at": time.Now().UnixMilli(), "revision": 3,
		"background": true, "exit_code": 0, "output_tail": "PRIVATE_SHELL_OUTPUT\n", "output_bytes": 21,
	}
	data, err := json.Marshal(disk)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".log"), []byte("PRIVATE_SHELL_OUTPUT\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runner, err := tools.NewShellJobRunner(sess.sessCtx, dir, tools.ShellJobHooks{})
	if err != nil {
		t.Fatal(err)
	}
	sess.shellJobs = runner
	sess.hub.seedShellJobs(runner.List(false))
	return id
}

// Records are synthetic and already terminal. The API must not start either a
// process or a model merely to inspect output, and a task ID grants no scope.
func TestShellTaskAPIUsesSessionOwnershipAndTaskScope(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) {
		t.Error("reading a shell record contacted a model")
		return llm.Response{}, nil
	})
	parentID, siblingID, bobID := h.createAs(aliceToken), h.createAs(aliceToken), h.createAs(bobToken)
	id := installShellTaskFixture(t, h.mgr, h.session(parentID))
	for _, tc := range []struct {
		token, sid, method, suffix string
		status                     int
	}{
		{aliceToken, parentID, http.MethodGet, "", 200},
		{bobToken, parentID, http.MethodGet, "", 404},
		{aliceToken, parentID, http.MethodGet, "/" + id + "/output?offset=0&limit=65536", 200},
		{bobToken, parentID, http.MethodGet, "/" + id + "/output", 404},
		{aliceToken, siblingID, http.MethodGet, "/" + id + "/output", 404},
		{bobToken, bobID, http.MethodGet, "/" + id + "/output", 404},
		{aliceToken, parentID, http.MethodGet, "/" + id + "/output?offset=-1", 400},
		{aliceToken, parentID, http.MethodPost, "/" + id + "/stop", 200},
		{bobToken, parentID, http.MethodPost, "/" + id + "/stop", 404},
		{aliceToken, siblingID, http.MethodPost, "/" + id + "/stop", 404},
	} {
		response := h.doAs(tc.token, tc.method, "/api/sessions/"+tc.sid+"/shell-tasks"+tc.suffix, "")
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != tc.status {
			t.Fatalf("%+v returned %d: %s", tc, response.StatusCode, body)
		}
		if tc.status == 404 && (strings.Contains(string(body), "PRIVATE_SHELL_OUTPUT") || strings.Contains(string(body), "private fixture command")) {
			t.Fatal("foreign task response leaked command data")
		}
	}
}
