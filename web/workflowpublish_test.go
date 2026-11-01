package web

// Wave B2 end-to-end tests: the publish model (snapshot isolation, dirty,
// rollback, migration), workflow schedules, and the markdown agent import.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/workflow"
)

// startWorkflowRunRaw posts a run without publishing first — the publish
// model's own tests need the explicit gate, unlike the legacy helpers.
func startWorkflowRunRaw(t *testing.T, h *harness, wid string) string {
	t.Helper()
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

func publishWorkflowNamed(t *testing.T, h *harness, wid, name, desc string) publishPointer {
	t.Helper()
	resp := h.do(http.MethodPost, "/api/manage/workflows/"+wid+"/publish", studioBody(map[string]any{"name": name, "desc": desc}))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("publish %s: %d (%s)", wid, resp.StatusCode, msg)
	}
	var ptr publishPointer
	if err := json.NewDecoder(resp.Body).Decode(&ptr); err != nil {
		t.Fatal(err)
	}
	return ptr
}

func patchWorkflow(t *testing.T, h *harness, wid string, revision int, body map[string]any) {
	t.Helper()
	body["revision"] = revision
	resp := h.do(http.MethodPatch, "/api/manage/workflows/"+wid, studioBody(body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("patch %s: %d (%s)", wid, resp.StatusCode, msg)
	}
}

func getWorkflowView(t *testing.T, h *harness, wid string) workflowView {
	t.Helper()
	resp := h.do(http.MethodGet, "/api/manage/workflows/"+wid, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("get %s: %d (%s)", wid, resp.StatusCode, msg)
	}
	var v workflowView
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func soloBody(agentID, task string) map[string]any {
	return map[string]any{
		"name": "快照", "description": "d", "status": "ready",
		"nodes": []map[string]any{{"id": "n", "agent": agentID, "task": task}},
	}
}

func TestWorkflowPublishSnapshotIsolation(t *testing.T) {
	var mu sync.Mutex
	prompts := []string{}
	h := newWorkflowHarness(t, func(ctx context.Context, system, user string) (llm.Response, error) {
		mu.Lock()
		prompts = append(prompts, user)
		mu.Unlock()
		return textTurn("done"), nil
	})
	a := createStudioAgent(t, h, "Solo", "s", "ready")
	wf := createWorkflow(t, h, soloBody(a.ID, "version one task"))
	ptr1 := publishWorkflowNamed(t, h, wf.ID, "v1", "first release")

	rid1 := startWorkflowRunRaw(t, h, wf.ID)
	if rec := waitWorkflowRun(t, h, wf.ID, rid1); rec.Status != workflow.RunCompleted {
		t.Fatalf("run1 = %q", rec.Status)
	}

	// The draft moves on; the pointer and the dirty flag say so.
	patchWorkflow(t, h, wf.ID, 1, soloBody(a.ID, "version two task"))
	v := getWorkflowView(t, h, wf.ID)
	if v.Published == nil || v.Published.VersionID != ptr1.VersionID || !v.Dirty {
		t.Fatalf("view after edit = %+v", v)
	}

	// A run still executes the published snapshot, not the draft.
	rid2 := startWorkflowRunRaw(t, h, wf.ID)
	if rec := waitWorkflowRun(t, h, wf.ID, rid2); rec.Status != workflow.RunCompleted {
		t.Fatalf("run2 = %q", rec.Status)
	}
	// Publish v2: the dirty flag clears, the version id moves.
	ptr2 := publishWorkflowNamed(t, h, wf.ID, "v2", "second release")
	if ptr2.VersionID == ptr1.VersionID {
		t.Fatal("v2 hash equals v1: changed draft produced the same snapshot")
	}
	if v := getWorkflowView(t, h, wf.ID); v.Dirty {
		t.Fatal("dirty after republish")
	}
	rid3 := startWorkflowRunRaw(t, h, wf.ID)
	if rec := waitWorkflowRun(t, h, wf.ID, rid3); rec.Status != workflow.RunCompleted {
		t.Fatalf("run3 = %q", rec.Status)
	}
	// Versions list: two snapshots, v2 current.
	resp := h.do(http.MethodGet, "/api/manage/workflows/"+wf.ID+"/versions", "")
	var versions struct {
		Versions []workflowVersionView `json:"versions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&versions); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(versions.Versions) != 2 {
		t.Fatalf("versions = %+v", versions.Versions)
	}
	current := map[string]bool{}
	for _, ver := range versions.Versions {
		current[ver.VersionID] = ver.Current
	}
	if !current[ptr2.VersionID] || current[ptr1.VersionID] {
		t.Fatalf("current flags = %+v", versions.Versions)
	}

	// Rollback is a pointer move: the next run is v1 again, and the draft
	// (still v2 content) is dirty against it.
	resp = h.do(http.MethodPost, "/api/manage/workflows/"+wf.ID+"/rollback", studioBody(map[string]any{"version_id": ptr1.VersionID}))
	var back publishPointer
	if err := json.NewDecoder(resp.Body).Decode(&back); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || back.VersionID != ptr1.VersionID || back.Name != "v1" {
		t.Fatalf("rollback = %d %+v", resp.StatusCode, back)
	}
	if v := getWorkflowView(t, h, wf.ID); !v.Dirty {
		t.Fatal("rollback to an older snapshot must mark the newer draft dirty")
	}
	rid4 := startWorkflowRunRaw(t, h, wf.ID)
	if rec := waitWorkflowRun(t, h, wf.ID, rid4); rec.Status != workflow.RunCompleted {
		t.Fatalf("run4 = %q", rec.Status)
	}
	// Rollback of a version that does not exist is a 404.
	if code := studioStatus(t, h, http.MethodPost, "/api/manage/workflows/"+wf.ID+"/rollback", `{"version_id":"000000000000"}`); code != http.StatusNotFound {
		t.Fatalf("rollback missing version: %d, want 404", code)
	}

	mu.Lock()
	got := prompts
	mu.Unlock()
	if len(got) != 4 {
		t.Fatalf("prompts = %v", got)
	}
	for i, want := range []string{"version one task", "version one task", "version two task", "version one task"} {
		if !strings.Contains(got[i], want) {
			t.Fatalf("run %d prompt = %q, want %q", i+1, got[i], want)
		}
	}
}

func TestWorkflowPublishMigration(t *testing.T) {
	dir := t.TempDir()
	h1 := newWorkflowHarness(t, func(ctx context.Context, system, user string) (llm.Response, error) {
		return textTurn("migrated done"), nil
	}, func(c *Config) { c.SessionDir = dir })
	a := createStudioAgent(t, h1, "Mig Bot", "m", "ready")

	// Plant a v1-shaped registry: status ready, no publish layer.
	legacy := fmt.Sprintf(`{"version":1,"workflows":{"wf_legacy":{"id":"wf_legacy","revision":1,"name":"遗留流水线","status":"ready","nodes":[{"id":"n","agent":%q,"task":"legacy task"}],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}}}`, a.ID)
	if err := os.WriteFile(filepath.Join(dir, "workflows.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	h1.mgr.Close()

	h2 := newWorkflowHarness(t, func(ctx context.Context, system, user string) (llm.Response, error) {
		return textTurn("migrated done"), nil
	}, func(c *Config) { c.SessionDir = dir })
	ptr, ok := h2.mgr.workflows.pointer("wf_legacy")
	if !ok || ptr.Name != "migrated" || ptr.VersionID == "" {
		t.Fatalf("migration pointer = %+v", ptr)
	}
	if _, err := os.Stat(filepath.Join(dir, "workflows", "versions", "wf_legacy", ptr.VersionID+".json")); err != nil {
		t.Fatal("migration snapshot not written")
	}
	if v := getWorkflowView(t, h2, "wf_legacy"); v.Published == nil || v.Dirty {
		t.Fatalf("migrated view = %+v", v)
	}
	rid := startWorkflowRunRaw(t, h2, "wf_legacy")
	if rec := waitWorkflowRun(t, h2, "wf_legacy", rid); rec.Status != workflow.RunCompleted {
		t.Fatalf("migrated run = %q (%q)", rec.Status, rec.Error)
	}
}

func TestWorkflowScheduleAPI(t *testing.T) {
	t.Setenv("POGO_SCHEDULE_DIR", t.TempDir())
	h := newWorkflowHarness(t, func(ctx context.Context, system, user string) (llm.Response, error) {
		return textTurn("done"), nil
	})
	a := createStudioAgent(t, h, "Solo", "s", "ready")
	wf := createWorkflow(t, h, soloBody(a.ID, "scheduled task"))
	publishWorkflow(t, h, wf.ID)

	put := func(body string, want int) wfScheduleView {
		t.Helper()
		resp := h.do(http.MethodPost, "/api/manage/workflows/"+wf.ID+"/schedule", body)
		defer resp.Body.Close()
		if resp.StatusCode != want {
			msg, _ := io.ReadAll(resp.Body)
			t.Fatalf("put schedule %s: %d, want %d (%s)", body, resp.StatusCode, want, msg)
		}
		var v wfScheduleView
		if want == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
				t.Fatal(err)
			}
		}
		return v
	}

	v := put(`{"kind":"every","every_sec":3600}`, 200)
	if v.Kind != "every" || v.EverySec != 3600 || !v.Enabled || v.JobID != wf.ID {
		t.Fatalf("schedule view = %+v", v)
	}
	if got := getWorkflowView(t, h, wf.ID).Schedule; got == nil || got.Kind != "every" {
		t.Fatalf("workflow view schedule = %+v", got)
	}

	// Re-posting replaces: one job, now cron-shaped.
	v = put(`{"kind":"cron","cron":"0 9 * * 1-5"}`, 200)
	if v.Kind != "cron" || v.Cron != "0 9 * * 1-5" {
		t.Fatalf("replaced schedule = %+v", v)
	}
	jobs, err := schedule.NewStore(schedule.DefaultDir()).Load()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, j := range jobs {
		if j.WorkflowID == wf.ID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("workflow jobs = %d, want 1 (replace)", count)
	}
	put(`{"kind":"whenever"}`, 400)
	put(`{"kind":"every","every_sec":0}`, 400)

	resp := h.do(http.MethodDelete, "/api/manage/workflows/"+wf.ID+"/schedule", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete schedule: %d", resp.StatusCode)
	}
	if got := getWorkflowView(t, h, wf.ID).Schedule; got != nil {
		t.Fatalf("schedule after delete = %+v", got)
	}
	if code := studioStatus(t, h, http.MethodDelete, "/api/manage/workflows/"+wf.ID+"/schedule", ""); code != http.StatusNotFound {
		t.Fatalf("second delete: %d, want 404", code)
	}
}

func TestWorkflowJobRunnerFiresPublishedSnapshot(t *testing.T) {
	var mu sync.Mutex
	prompts := []string{}
	h := newWorkflowHarness(t, func(ctx context.Context, system, user string) (llm.Response, error) {
		mu.Lock()
		prompts = append(prompts, user)
		mu.Unlock()
		return textTurn("scheduled done"), nil
	})
	a := createStudioAgent(t, h, "Solo", "s", "ready")
	wf := createWorkflow(t, h, soloBody(a.ID, "scheduled task"))
	publishWorkflow(t, h, wf.ID)

	// The dispatch itself, straight through — RunOnce's flock is a unix
	// feature, so this machine exercises the runner without it.
	out := h.mgr.runWorkflowJob(context.Background(), schedule.Job{ID: wf.ID, WorkflowID: wf.ID})
	if out.Err != "" || out.ExitCode != 0 || !strings.HasPrefix(out.SessionID, "run_") || out.EndReason != workflow.RunCompleted {
		t.Fatalf("outcome = %+v", out)
	}
	mu.Lock()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "scheduled task") {
		t.Fatalf("prompts = %v", prompts)
	}
	mu.Unlock()

	// Unpublished workflow: a clean error, not a crash.
	wf2 := createWorkflow(t, h, soloBody(a.ID, "never published"))
	out = h.mgr.runWorkflowJob(context.Background(), schedule.Job{ID: wf2.ID, WorkflowID: wf2.ID})
	if out.ExitCode == 0 || !strings.Contains(out.Err, "尚未发布") {
		t.Fatalf("unpublished outcome = %+v", out)
	}

	// A run that suspends on a person ends the job's part cleanly.
	gated := map[string]any{
		"name": "gated", "description": "d", "status": "ready",
		"nodes": []map[string]any{{"id": "gate", "kind": "human", "prompt": "ok?"}},
	}
	wf3 := createWorkflow(t, h, gated)
	publishWorkflow(t, h, wf3.ID)
	out = h.mgr.runWorkflowJob(context.Background(), schedule.Job{ID: wf3.ID, WorkflowID: wf3.ID})
	if out.Err != "" || out.ExitCode != 0 || out.EndReason != "waiting_human" {
		t.Fatalf("suspended outcome = %+v", out)
	}
}

func TestStudioImportMarkdown(t *testing.T) {
	h := newWorkflowHarness(t, func(ctx context.Context, system, user string) (llm.Response, error) {
		return textTurn("ok"), nil
	})
	importDoc := func(doc string, want int) studioAgent {
		t.Helper()
		resp := h.do(http.MethodPost, "/api/manage/agents/import", studioBody(map[string]any{"markdown": doc}))
		defer resp.Body.Close()
		if resp.StatusCode != want {
			msg, _ := io.ReadAll(resp.Body)
			t.Fatalf("import: %d, want %d (%s)", resp.StatusCode, want, msg)
		}
		var a studioAgent
		if want == http.StatusCreated {
			if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
				t.Fatal(err)
			}
		}
		return a
	}

	full := "---\nname: 分析员\ndescription: 中文描述：样本分析\nmodel: k3\ntools: read, ls, grep\n---\n\n你是一位样本分析员。只读分析，绝不执行样本。\n"
	a := importDoc(full, http.StatusCreated)
	if a.Name != "分析员" || a.Description != "中文描述：样本分析" || a.Model != "k3" || a.Status != "ready" {
		t.Fatalf("imported = %+v", a)
	}
	if !strings.Contains(a.Instructions, "绝不执行样本") {
		t.Fatalf("instructions = %q", a.Instructions)
	}
	if a.Tools == nil || len(*a.Tools) != 3 || (*a.Tools)[0] != "read" {
		t.Fatalf("tools = %v", a.Tools)
	}

	// Model omitted: the server default steps in.
	other := importDoc("---\nname: 二号\n---\n正文指令。\n", http.StatusCreated)
	if other.Model != "k3" {
		t.Fatalf("default model = %q", other.Model)
	}

	importDoc("---\ndescription: 没有名字\n---\n正文\n", http.StatusBadRequest)
	importDoc("没有 frontmatter 的纯文本\n", http.StatusBadRequest)
	importDoc(full, http.StatusConflict) // 同名
}

// An empty draft saves and loads (create → orchestrate on the canvas);
// publish is where runnability is enforced.
func TestWorkflowEmptyDraftLifecycle(t *testing.T) {
	h := newWorkflowHarness(t, func(ctx context.Context, system, user string) (llm.Response, error) {
		return textTurn("done"), nil
	})

	resp := h.do(http.MethodPost, "/api/manage/workflows", studioBody(map[string]any{"name": "空草稿", "nodes": []map[string]any{}}))
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create empty draft: %d (%s)", resp.StatusCode, raw)
	}
	var wf Workflow
	if err := json.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}

	resp = h.do(http.MethodPost, "/api/manage/workflows/"+wf.ID+"/publish", studioBody(map[string]any{}))
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "无法发布") {
		t.Fatalf("publish empty draft: %d (%s)", resp.StatusCode, raw)
	}

	a := createStudioAgent(t, h, "Solo", "s", "ready")
	patchWorkflow(t, h, wf.ID, wf.Revision, map[string]any{
		"name":  "空草稿",
		"nodes": []map[string]any{{"id": "n", "agent": a.ID, "task": "t"}},
	})
	ptr := publishWorkflowNamed(t, h, wf.ID, "v1", "")
	if ptr.VersionID == "" {
		t.Fatal("expected version id")
	}
	if rid := startWorkflowRunRaw(t, h, wf.ID); rid == "" {
		t.Fatal("expected run id")
	}
}
