package web

// Workflow store boundary tests: the registry file's refuse-to-boot matrix
// and the CRUD semantics, cloned from the studio store's guarantees.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/workflow"
)

func wfTestNodes() []workflow.Node {
	return []workflow.Node{
		{ID: "analyze", Agent: "ag_a", Task: "look", Expect: &workflow.Expect{Path: "analyze.md"}},
		{ID: "review", Agent: "ag_b", Task: "check", After: []workflow.Dep{{Node: "analyze"}}},
	}
}

func wfTestInput() workflowInput {
	return workflowInput{Name: "流水线", Description: "test pipeline", Status: "ready", Nodes: wfTestNodes()}
}

func bootWorkflowStore(t *testing.T, dir string) *workflowStore {
	t.Helper()
	s, err := openWorkflowStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func writeWorkflowsFile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workflows.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workflows.json.initialized"), []byte("workflows-v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowStoreFirstBootSeedsRegistry(t *testing.T) {
	dir := t.TempDir()
	s := bootWorkflowStore(t, dir)
	if list := s.list(true); len(list) != 0 {
		t.Fatalf("fresh registry = %+v", list)
	}
	if _, err := os.Stat(filepath.Join(dir, "workflows.json.initialized")); err != nil {
		t.Fatal("initialized marker not written")
	}
	// Reopening the seeded registry is not a refusal.
	bootWorkflowStore(t, dir)
}

func TestWorkflowStoreRefuseBootMatrix(t *testing.T) {
	valid := `{"version":1,"workflows":{}}`
	cases := []struct {
		name    string
		prepare func(t *testing.T, dir string)
		want    string
	}{
		{"missing file on initialized dir", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "workflows.json.initialized"), []byte("workflows-v1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "missing from an initialized"},
		{"corrupt json", func(t *testing.T, dir string) { writeWorkflowsFile(t, dir, `{"version":1,"workflows":{`) }, "workflow registry"},
		{"trailing data", func(t *testing.T, dir string) { writeWorkflowsFile(t, dir, valid+` {}`) }, "trailing data"},
		{"unknown field", func(t *testing.T, dir string) {
			writeWorkflowsFile(t, dir, `{"version":1,"workflows":{},"surprise":true}`)
		}, "unknown field"},
		{"version mismatch", func(t *testing.T, dir string) {
			writeWorkflowsFile(t, dir, `{"version":99,"workflows":{}}`)
		}, "invalid workflow registry"},
		{"null map", func(t *testing.T, dir string) { writeWorkflowsFile(t, dir, `{"version":1,"workflows":null}`) }, "invalid workflow registry"},
		{"id mismatch", func(t *testing.T, dir string) {
			writeWorkflowsFile(t, dir, `{"version":1,"workflows":{"wf_a":{"id":"wf_b","revision":1,"name":"x","status":"ready","nodes":[{"id":"n","agent":"ag_a","task":"t"}],"created_at":"t","updated_at":"t"}}}`)
		}, "invalid workflow definition"},
		{"zero revision", func(t *testing.T, dir string) {
			writeWorkflowsFile(t, dir, `{"version":1,"workflows":{"wf_a":{"id":"wf_a","revision":0,"name":"x","status":"ready","nodes":[{"id":"n","agent":"ag_a","task":"t"}],"created_at":"t","updated_at":"t"}}}`)
		}, "invalid workflow definition"},
		{"cyclic definition", func(t *testing.T, dir string) {
			writeWorkflowsFile(t, dir, `{"version":1,"workflows":{"wf_a":{"id":"wf_a","revision":1,"name":"x","status":"ready","nodes":[{"id":"a","agent":"ag_a","task":"t","after":["b"]},{"id":"b","agent":"ag_b","task":"t","after":["a"]}],"created_at":"t","updated_at":"t"}}}`)
		}, "cycle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.prepare(t, dir)
			_, err := openWorkflowStore(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("open = %v, want something containing %q", err, tc.want)
			}
		})
	}
}

func TestWorkflowStorePutRevisionAndDelete(t *testing.T) {
	s := bootWorkflowStore(t, t.TempDir())
	w, err := s.put("", wfTestInput(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(w.ID, "wf_") || w.Revision != 1 {
		t.Fatalf("created = %+v", w)
	}
	up, err := s.put(w.ID, wfTestInput(), 1)
	if err != nil || up.Revision != 2 {
		t.Fatalf("update = %+v, %v", up, err)
	}
	if _, err := s.put(w.ID, wfTestInput(), 1); err == nil || !strings.Contains(err.Error(), "已被更新") {
		t.Fatalf("stale revision = %v, want conflict", err)
	}
	if _, err := s.put("wf_ghost", wfTestInput(), 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing update = %v, want ErrNotFound", err)
	}
	if err := s.remove(w.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.get(w.ID); ok {
		t.Fatal("deleted workflow still visible")
	}
	if err := s.remove(w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
	// The file on disk agrees.
	raw, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	var disk workflowData
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	if !disk.Workflows[w.ID].Deleted {
		t.Fatalf("disk record = %+v", disk.Workflows[w.ID])
	}
}

func TestWorkflowStoreListScrubbing(t *testing.T) {
	s := bootWorkflowStore(t, t.TempDir())
	if _, err := s.put("", wfTestInput(), 0); err != nil {
		t.Fatal(err)
	}
	second := wfTestInput()
	second.Name = "另一条流水线"
	if _, err := s.put("", second, 0); err != nil {
		t.Fatal(err)
	}
	admin := s.list(true)
	if len(admin) != 2 {
		t.Fatalf("admin list = %d, want 2", len(admin))
	}
	// The publish model has no visibility gate: the public list carries every
	// live workflow, tasks and prompts scrubbed.
	public := s.list(false)
	if len(public) != 2 {
		t.Fatalf("public list = %+v", public)
	}
	for _, w := range public {
		for _, n := range w.Nodes {
			if n.Task != "" || n.Prompt != "" {
				t.Fatalf("public list leaks task/prompt: %+v", n)
			}
		}
	}
}

func TestWorkflowStoreValidationMessages(t *testing.T) {
	s := bootWorkflowStore(t, t.TempDir())
	cases := []struct {
		name   string
		mutate func(in *workflowInput)
		want   string
	}{
		{"empty name", func(in *workflowInput) { in.Name = " " }, "名称"},
		{"unknown dependency", func(in *workflowInput) { in.Nodes[1].After = []workflow.Dep{{Node: "ghost"}} }, "unknown dependency"},
		{"turn cap", func(in *workflowInput) { in.Nodes[0].MaxTurns = 101 }, "100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := wfTestInput()
			tc.mutate(&in)
			_, err := s.put("", in, 0)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("put = %v, want something containing %q", err, tc.want)
			}
		})
	}

	// An empty draft saves fine — publish is where runnability is enforced.
	in := wfTestInput()
	in.Nodes = nil
	if _, err := s.put("", in, 0); err != nil {
		t.Fatalf("empty draft put = %v, want nil", err)
	}
}

func TestNarrowWorkflowTools(t *testing.T) {
	list := func(names ...string) *[]string { return &names }
	if got := narrowWorkflowTools(nil, nil); got != nil {
		t.Fatalf("nil/nil = %v", *got)
	}
	if got := narrowWorkflowTools(list("read", "write"), nil); got == nil || len(*got) != 2 {
		t.Fatalf("agent only = %v", got)
	}
	if got := narrowWorkflowTools(nil, list("read")); got == nil || (*got)[0] != "read" {
		t.Fatalf("node only = %v", got)
	}
	got := narrowWorkflowTools(list("read", "ls"), list("read", "write"))
	if got == nil || len(*got) != 1 || (*got)[0] != "read" {
		t.Fatalf("intersection = %v", got)
	}
	got = narrowWorkflowTools(list("bash"), list("read"))
	if got == nil || len(*got) != 0 {
		t.Fatalf("disjoint = %v, want explicit empty", got)
	}
}
