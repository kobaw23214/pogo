package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/schedule"
)

// The schedule panel's API talks to the same jobs.json the CLI manages. These
// tests run the whole chain over HTTP with POGO_SCHEDULE_BACKEND=noop, so no
// launchd/systemd/crontab is ever touched, and a fake runner standing in for
// the pogo child RunOnce would spawn.

// scheduleTestServer isolates the schedule state in a temp dir, forces the
// noop backend, and serves the real routes behind the test token.
func scheduleTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(schedule.DirEnv, dir)
	t.Setenv(schedule.BackendEnv, "noop")
	mgr := sweepManager(t, 0)
	srv, err := NewServer(mgr, ServerOptions{Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(srv.Handler())
	t.Cleanup(front.Close)
	return front, dir
}

func scheduleRequest(t *testing.T, front *httptest.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, front.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func scheduleJobs(t *testing.T, front *httptest.Server) []any {
	t.Helper()
	code, got := scheduleRequest(t, front, "GET", "/api/schedule/jobs", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/schedule/jobs: %d (%v)", code, got)
	}
	jobs, _ := got["jobs"].([]any)
	return jobs
}

func TestScheduleAPIJobsCrudChain(t *testing.T) {
	front, dir := scheduleTestServer(t)

	// Empty store, noop backend named.
	code, got := scheduleRequest(t, front, "GET", "/api/schedule/jobs", "")
	if code != http.StatusOK || got["backend"] != "noop" {
		t.Fatalf("GET jobs: %d %v", code, got)
	}
	if jobs, _ := got["jobs"].([]any); len(jobs) != 0 {
		t.Errorf("jobs = %v, want empty", jobs)
	}

	// Create a cron job: 201 with the computed next run.
	code, got = scheduleRequest(t, front, "POST", "/api/schedule/jobs",
		`{"prompt":"morning report","kind":"cron","cron":"0 9 * * *","name":"morning","workdir":`+quoteJSON(dir)+`}`)
	if code != http.StatusCreated {
		t.Fatalf("POST create: %d (%v)", code, got)
	}
	job, _ := got["job"].(map[string]any)
	if job["id"] != "morning" || job["kind"] != "cron" || job["enabled"] != true {
		t.Errorf("created job = %v", job)
	}
	if job["next_run"] == nil || job["next_run"] == "" {
		t.Errorf("next_run = %v, want a timestamp", job["next_run"])
	}

	// The list carries it, not running.
	jobs := scheduleJobs(t, front)
	if len(jobs) != 1 {
		t.Fatalf("jobs after create = %v", jobs)
	}
	j0, _ := jobs[0].(map[string]any)
	if j0["id"] != "morning" || j0["next_run"] == nil {
		t.Errorf("listed job = %v", j0)
	}
	if _, running := j0["running"]; running {
		t.Errorf("running = %v, want absent", j0["running"])
	}

	// A duplicate name is a 409, validation failures are 400s.
	if code, _ = scheduleRequest(t, front, "POST", "/api/schedule/jobs",
		`{"prompt":"again","kind":"cron","cron":"0 9 * * *","name":"morning","workdir":`+quoteJSON(dir)+`}`); code != http.StatusConflict {
		t.Errorf("duplicate name: %d, want 409", code)
	}
	for name, body := range map[string]string{
		"missing prompt":  `{"kind":"cron","cron":"0 9 * * *"}`,
		"bad cron":        `{"prompt":"x","kind":"cron","cron":"61 * * * *"}`,
		"impossible cron": `{"prompt":"x","kind":"cron","cron":"0 0 30 2 *"}`,
		"two triggers":    `{"prompt":"x","kind":"cron","cron":"0 9 * * *","every_sec":60}`,
		"bad name":        `{"prompt":"x","kind":"cron","cron":"0 9 * * *","name":"bad name!"}`,
		"past at":         `{"prompt":"x","kind":"at","at":"2020-01-01T00:00"}`,
		"bad workdir":     `{"prompt":"x","kind":"every","every_sec":60,"workdir":"/no/such/dir/here"}`,
		"unknown kind":    `{"prompt":"x","kind":"sometimes"}`,
		"negative budget": `{"prompt":"x","kind":"every","every_sec":60,"budget_usd":-1}`,
	} {
		if code, got := scheduleRequest(t, front, "POST", "/api/schedule/jobs", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400 (%v)", name, code, got)
		}
	}

	// disable flips the flag and nulls the next run; enable restores both.
	code, got = scheduleRequest(t, front, "POST", "/api/schedule/jobs/morning/disable", "")
	if code != http.StatusOK {
		t.Fatalf("disable: %d (%v)", code, got)
	}
	job, _ = got["job"].(map[string]any)
	if job["enabled"] != false || job["next_run"] != nil {
		t.Errorf("after disable = %v", job)
	}
	stored, err := schedule.NewStore(dir).Find("morning")
	if err != nil || stored.Enabled {
		t.Errorf("stored after disable = %+v, %v", stored, err)
	}
	code, got = scheduleRequest(t, front, "POST", "/api/schedule/jobs/morning/enable", "")
	if code != http.StatusOK {
		t.Fatalf("enable: %d (%v)", code, got)
	}
	if job, _ = got["job"].(map[string]any); job["enabled"] != true || job["next_run"] == nil {
		t.Errorf("after enable = %v", job)
	}
	if code, _ = scheduleRequest(t, front, "POST", "/api/schedule/jobs/nope/enable", ""); code != http.StatusNotFound {
		t.Errorf("enable missing job: %d, want 404", code)
	}

	// Delete: gone from the list, and the store agrees.
	code, _ = scheduleRequest(t, front, "DELETE", "/api/schedule/jobs/morning", "")
	if code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
	if jobs := scheduleJobs(t, front); len(jobs) != 0 {
		t.Errorf("jobs after delete = %v", jobs)
	}
	if _, err := schedule.NewStore(dir).Find("morning"); err == nil {
		t.Error("deleted job still in the store")
	}
	if code, _ = scheduleRequest(t, front, "DELETE", "/api/schedule/jobs/morning", ""); code != http.StatusNotFound {
		t.Errorf("delete missing job: %d, want 404", code)
	}
}

func TestScheduleAPI503WithoutBackend(t *testing.T) {
	front, _ := scheduleTestServer(t)
	// A forced-but-unknown backend is the one refusal left: a typo should be
	// loud. A machine with no scheduler at all no longer lands here — it
	// falls back to the in-process backend (TestScheduleAPIFallsBackInProcess).
	t.Setenv(schedule.BackendEnv, "bogus")
	if code, got := scheduleRequest(t, front, "GET", "/api/schedule/jobs", ""); code != http.StatusServiceUnavailable {
		t.Errorf("GET without backend: %d, want 503 (%v)", code, got)
	}
	if code, got := scheduleRequest(t, front, "POST", "/api/schedule/jobs",
		`{"prompt":"x","kind":"every","every_sec":60}`); code != http.StatusServiceUnavailable {
		t.Errorf("POST without backend: %d, want 503 (%v)", code, got)
	}
}

func TestScheduleAPIRunNow(t *testing.T) {
	front, dir := scheduleTestServer(t)

	// The fake child: announces it started, waits for the test to let it
	// finish, leaves one line in the out log like the real tee would.
	started := make(chan struct{})
	finish := make(chan struct{})
	restore := schedRunner
	schedRunner = func(ctx context.Context, job schedule.Job, logs schedule.LogPaths, mirror io.Writer) schedule.RunOutcome {
		close(started)
		<-finish
		_ = os.MkdirAll(filepath.Dir(logs.Out), 0o700)
		_ = os.WriteFile(logs.Out, []byte("fake child output\n"), 0o600)
		return schedule.RunOutcome{SessionID: "sess-fake", EndReason: "completed", ExitCode: 0}
	}
	t.Cleanup(func() { schedRunner = restore })

	code, got := scheduleRequest(t, front, "POST", "/api/schedule/jobs",
		`{"prompt":"poll the queue","kind":"every","every_sec":3600,"name":"poll","workdir":`+quoteJSON(dir)+`}`)
	if code != http.StatusCreated {
		t.Fatalf("POST create: %d (%v)", code, got)
	}

	// run-now is asynchronous: 202 immediately, then the list shows it running.
	code, _ = scheduleRequest(t, front, "POST", "/api/schedule/jobs/poll/run-now", "")
	if code != http.StatusAccepted {
		t.Fatalf("run-now: %d, want 202", code)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never started")
	}
	jobs := scheduleJobs(t, front)
	if len(jobs) != 1 || jobs[0].(map[string]any)["running"] != true {
		t.Fatalf("jobs while running = %v, want running true", jobs)
	}

	// A second click while in flight is a 409, not a second agent.
	if code, _ = scheduleRequest(t, front, "POST", "/api/schedule/jobs/poll/run-now", ""); code != http.StatusConflict {
		t.Errorf("run-now while running: %d, want 409", code)
	}

	// Let the child finish: the bookkeeping lands — history record, status,
	// running flag cleared.
	close(finish)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var runs []any
		code, got = scheduleRequest(t, front, "GET", "/api/schedule/jobs/poll/runs", "")
		if code != http.StatusOK {
			t.Fatalf("GET runs: %d (%v)", code, got)
		}
		runs, _ = got["runs"].([]any)
		if len(runs) == 1 {
			r0 := runs[0].(map[string]any)
			if r0["trigger"] != "manual" || r0["status"] != "ok" || r0["session_id"] != "sess-fake" {
				t.Errorf("run record = %v", r0)
			}
			tail, _ := got["log_tail"].([]any)
			if len(tail) != 1 || tail[0] != "fake child output" {
				t.Errorf("log_tail = %v", tail)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no run recorded after run-now: %v", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if jobs := scheduleJobs(t, front); jobs[0].(map[string]any)["running"] == true {
		t.Error("running flag not cleared after the run")
	} else if jobs[0].(map[string]any)["last_run_status"] != "ok" {
		t.Errorf("last_run_status = %v", jobs[0])
	}

	// A disabled job refuses to run (the same rule RunOnce enforces).
	if code, _ := scheduleRequest(t, front, "POST", "/api/schedule/jobs/poll/disable", ""); code != http.StatusOK {
		t.Fatalf("disable: %d", code)
	}
	if code, _ := scheduleRequest(t, front, "POST", "/api/schedule/jobs/poll/run-now", ""); code != http.StatusConflict {
		t.Errorf("run-now disabled job: %d, want 409", code)
	}

	if code, _ := scheduleRequest(t, front, "GET", "/api/schedule/jobs/nope/runs", ""); code != http.StatusNotFound {
		t.Errorf("runs of missing job: %d, want 404", code)
	}
}
