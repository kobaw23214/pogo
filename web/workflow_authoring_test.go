package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/workflow"
)

func authoringCall(t *testing.T, h *muHarness, token, action string, body any, want int) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response := h.doAs(token, http.MethodPost, "/api/manage/workflow-authoring/"+action, string(raw))
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != want {
		t.Fatalf("authoring %s: %d %s %v", action, response.StatusCode, data, err)
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("invalid authoring JSON: %s", data)
	}
	return value
}
func authoringBuilder(t *testing.T, h *muHarness, name string) {
	t.Helper()
	p := identity.LocalPrincipal(identity.LocalScope(name), name, false)
	if err := taskRunsDB(t, h.mgr).SetAccessRole(p.ID, "builder"); err != nil {
		t.Fatal(err)
	}
}
func authoringWorkflow(t *testing.T, value map[string]json.RawMessage) Workflow {
	t.Helper()
	var w Workflow
	if err := json.Unmarshal(value["workflow"], &w); err != nil {
		t.Fatal(err)
	}
	return w
}
func authoringString(raw json.RawMessage) string { var s string; _ = json.Unmarshal(raw, &s); return s }
func TestWorkflowAuthoringDiscoveryScopedEffectiveCapabilities(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { t.Fatal("discovery called model"); return textTurn("unused"), nil })
	empty := []string{}
	a, err := h.mgr.studio.put("", studioInput{Name: "safe fixture", Description: "合成资源", Instructions: "SECRET instructions", Model: "k3", Status: "ready", Tools: &empty, Skills: &empty, MCPServers: &empty}, 0)
	if err != nil {
		t.Fatal(err)
	}
	authoringCall(t, h, aliceToken, "discover", map[string]any{}, 403)
	authoringBuilder(t, h, "alice")
	got := authoringCall(t, h, aliceToken, "discover", map[string]any{"query": "safe fixture"}, 200)
	raw, _ := json.Marshal(got)
	if bytes.Contains(raw, []byte("SECRET")) || bytes.Contains(raw, []byte("base_url")) || bytes.Contains(raw, []byte("key_env")) {
		t.Fatalf("resource secrets exposed: %s", raw)
	}
	var agents []struct {
		ID           string                        `json:"id"`
		Ready        bool                          `json:"ready_for_test"`
		Capabilities workflowAuthoringCapabilities `json:"effective_capabilities"`
	}
	_ = json.Unmarshal(got["agents"], &agents)
	if len(agents) != 1 || agents[0].ID != a.ID || !agents[0].Ready || slices.Contains(agents[0].Capabilities.NativeTools, "bash") {
		t.Fatalf("incorrect effective capabilities: %s", raw)
	}
	_, err = h.mgr.studio.put("", studioInput{Name: "shell fixture", Model: "k3", Status: "ready"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	got = authoringCall(t, h, aliceToken, "discover", map[string]any{"query": "shell fixture"}, 200)
	_ = json.Unmarshal(got["agents"], &agents)
	if len(agents) != 1 || agents[0].Ready || !slices.Contains(agents[0].Capabilities.NativeTools, "bash") {
		t.Fatalf("unsupported shell advertised runnable: %s", got["agents"])
	}
}
func TestWorkflowAuthoringAtomicPatchReceiptsAndExactPublication(t *testing.T) {
	h, wf, history := workflowAccessFixture(t)
	authoringBuilder(t, h, "alice")
	if _, err := h.mgr.workflows.publish(wf.ID, "original", ""); err != nil {
		t.Fatal(err)
	}
	ptr, _ := h.mgr.workflows.pointer(wf.ID)
	oldPublished, err := os.ReadFile(h.mgr.workflows.versionPath(wf.ID, ptr.VersionID))
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"action": "patch", "workflow_id": wf.ID, "expected_revision": wf.Revision, "request_id": "add-second-review", "operations": []any{
		map[string]any{"op": "add_node", "node": map[string]any{"id": "second", "kind": "human", "prompt": "Inspect {{ nodes.review.artifact }}"}},
		map[string]any{"op": "connect", "from": "review", "to": "second"},
		map[string]any{"op": "update_node", "node_id": "review", "patch": map[string]any{"prompt": "New review"}},
	}}
	value := authoringCall(t, h, aliceToken, "edit", request, 200)
	updated := authoringWorkflow(t, value)
	if updated.Revision != wf.Revision+1 || updated.Nodes[0].ID != "review" || updated.Nodes[0].Prompt != "New review" || len(updated.Nodes) != 2 || updated.Nodes[1].After[0].Node != "review" {
		t.Fatalf("partial edit lost stable graph: %+v", updated)
	}
	reused := authoringCall(t, h, aliceToken, "edit", request, 200)
	again := authoringWorkflow(t, reused)
	if again.ID != updated.ID || again.Revision != updated.Revision || string(reused["reused"]) != "true" {
		t.Fatal("retry duplicated mutation")
	}
	reopened, err := openWorkflowStore(h.mgr.cfg.SessionDir)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(request)
	var decoded workflowAuthoringEditRequest
	_ = json.Unmarshal(raw, &decoded)
	alice := identity.LocalPrincipal(identity.LocalScope("alice"), "alice", false)
	receipt, replay, err := reopened.authoringEdit(alice.ID, decoded)
	if err != nil || !replay || receipt.Workflow.Revision != updated.Revision {
		t.Fatalf("receipt not durable: %+v %v %v", receipt, replay, err)
	}
	request["operations"] = []any{map[string]any{"op": "set_metadata", "name": "different intent"}}
	authoringCall(t, h, aliceToken, "edit", request, 409)
	authoringCall(t, h, aliceToken, "publish", map[string]any{"workflow_id": wf.ID, "expected_revision": updated.Revision, "expected_hash": authoringString(value["definition_hash"])}, 403)
	authoringCall(t, h, h.token, "publish", map[string]any{"workflow_id": wf.ID, "expected_revision": wf.Revision, "expected_hash": ptr.VersionID}, 409)
	currentPtr, _ := h.mgr.workflows.pointer(wf.ID)
	if currentPtr.VersionID != ptr.VersionID {
		t.Fatal("stale publication changed pointer")
	}
	authoringCall(t, h, h.token, "publish", map[string]any{"workflow_id": wf.ID, "expected_revision": updated.Revision, "expected_hash": authoringString(value["definition_hash"])}, 200)
	after, err := os.ReadFile(h.mgr.workflows.versionPath(wf.ID, ptr.VersionID))
	if err != nil || !bytes.Equal(oldPublished, after) {
		t.Fatal("editing rewrote immutable old version")
	}
	retained, err := readWFRecord(h.mgr.wfruns.recordPath(history.ID))
	if err != nil || retained.Nodes["review"].Status != workflow.NodeWaitingHuman || retained.Definition.Nodes[0].Prompt != "private instructions" {
		t.Fatal("editing changed historical Run")
	}
	if err := taskRunsDB(t, h.mgr).SetWorkflowGrant(wf.ID, alice.ID, []string{wfRead}); err != nil {
		t.Fatal(err)
	}
	decodedRaw, _ := json.Marshal(decoded)
	var original any
	_ = json.Unmarshal(decodedRaw, &original)
	authoringCall(t, h, aliceToken, "edit", original, 403)
}
func TestWorkflowAuthoringCreateIdempotencyAndStrictRequests(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return textTurn("unused"), nil })
	authoringBuilder(t, h, "alice")
	body := map[string]any{"action": "create", "request_id": "create-once", "name": "合成流程", "nodes": []any{map[string]any{"id": "review", "kind": "human", "prompt": "Review"}}}
	first := authoringWorkflow(t, authoringCall(t, h, aliceToken, "edit", body, 200))
	second := authoringWorkflow(t, authoringCall(t, h, aliceToken, "edit", body, 200))
	if first.ID != second.ID || len(h.mgr.workflows.list(true)) != 1 {
		t.Fatal("create replay created duplicate workflow")
	}
	body["owner_id"] = "somebody-else"
	authoringCall(t, h, aliceToken, "edit", body, 400)
	delete(body, "owner_id")
	body["request_id"] = "other-intent"
	body["name"] = ""
	authoringCall(t, h, aliceToken, "edit", body, 400)
	if len(h.mgr.workflows.list(true)) != 1 {
		t.Fatal("invalid edit mutated registry")
	}
}
func TestWorkflowAuthoringValidationIsStructuredAndReadOnly(t *testing.T) {
	h, wf, _ := workflowAccessFixture(t)
	authoringBuilder(t, h, "alice")
	before, err := os.ReadFile(h.mgr.workflows.path)
	if err != nil {
		t.Fatal(err)
	}
	value := authoringCall(t, h, aliceToken, "validate", map[string]any{"workflow_id": wf.ID, "expected_revision": wf.Revision, "definition": map[string]any{"name": "valid name", "nodes": []any{
		map[string]any{"id": "one", "agent": "missing-one", "task": "Use {{ inputs.order_id }}", "after": []string{"absent"}},
		map[string]any{"id": "two", "agent": "missing-two", "task": "Use {{ inputs.amount }}"},
	}}, "inputs": map[string]string{}}, 200)
	var diagnostics []workflowAuthoringDiagnostic
	_ = json.Unmarshal(value["diagnostics"], &diagnostics)
	if string(value["valid"]) != "false" || len(diagnostics) < 5 {
		t.Fatalf("expected multi-error report: %s", value["diagnostics"])
	}
	codes := map[string]bool{}
	for _, d := range diagnostics {
		codes[d.Code] = true
		if d.FieldPath == "" || d.Severity == "" {
			t.Fatalf("unlocated diagnostic: %+v", d)
		}
	}
	for _, code := range []string{"agent_unavailable", "unknown_dependency", "missing_input"} {
		if !codes[code] {
			t.Fatalf("missing diagnostic %s: %+v", code, diagnostics)
		}
	}
	after, _ := os.ReadFile(h.mgr.workflows.path)
	if !bytes.Equal(before, after) {
		t.Fatal("validation changed saved draft")
	}
	authoringCall(t, h, bobToken, "validate", map[string]any{"workflow_id": wf.ID}, 403)
}
func TestWorkflowPublishValidatesAndSnapshotsSameLockedDraft(t *testing.T) {
	store, err := openWorkflowStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w, err := store.put("", workflowInput{Name: "stable", Nodes: []workflow.Node{{ID: "human", Kind: "human", Prompt: "Review"}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, hash, _ := store.canonicalDraft(w)
	validating, release, secondRef := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	store.refs = func(*workflowInput) error {
		switch calls.Add(1) {
		case 1:
			close(validating)
			<-release
		case 2:
			close(secondRef)
		}
		return nil
	}
	publishDone := make(chan error, 1)
	go func() { _, err := store.publishExact(w.ID, "reviewed", "", w.Revision, hash); publishDone <- err }()
	<-validating
	updateDone := make(chan error, 1)
	go func() {
		_, err := store.put(w.ID, workflowInput{Name: "after review", Nodes: w.Nodes}, w.Revision)
		updateDone <- err
	}()
	<-secondRef
	select {
	case <-updateDone:
		t.Fatal("update passed publication lock")
	default:
	}
	close(release)
	if err := <-publishDone; err != nil {
		t.Fatal(err)
	}
	if err := <-updateDone; err != nil {
		t.Fatal(err)
	}
	definition, err := store.publishedDefinition(w.ID)
	if err != nil || definition.Name != "stable" {
		t.Fatalf("published unreviewed content: %+v %v", definition, err)
	}
}

func TestWorkflowAuthoringArtifactReadBoundedAndAuthorized(t *testing.T) {
	h, wf, rec := workflowAccessFixture(t)
	node := rec.Nodes["review"]
	node.Artifact = "large.txt"
	rec.Nodes["review"] = node
	if err := writeWFRecord(h.mgr.wfruns.recordPath(rec.ID), rec); err != nil {
		t.Fatal(err)
	}
	workspace := h.mgr.wfruns.runDir(rec.ID) + "/workspace"
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspace+"/large.txt", bytes.Repeat([]byte("x"), 1<<20), 0600); err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"action": "artifact", "workflow_id": wf.ID, "run_id": rec.ID, "node_id": "review"}
	value := authoringCall(t, h, aliceToken, "read", request, 200)
	if string(value["inline"]) != "false" || len(value["artifact_url"]) == 0 || value["content"] != nil {
		t.Fatalf("large artifact was inlined: %+v", value)
	}
	authoringCall(t, h, bobToken, "read", request, 404)
	if err := os.WriteFile(workspace+"/large.txt", []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	value = authoringCall(t, h, aliceToken, "read", request, 200)
	if string(value["inline"]) != "true" || authoringString(value["content"]) != "" {
		t.Fatalf("empty text artifact unavailable: %+v", value)
	}
}

func TestWorkflowCanonicalizationDoesNotMutateNestedDefinition(t *testing.T) {
	store, err := openWorkflowStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := Workflow{workflowInput: workflowInput{Name: "name", Nodes: []workflow.Node{
		{ID: "one", Agent: "agent-one", Task: " task ", Expect: &workflow.Expect{Path: " output.md "}},
		{ID: "two", Agent: "agent-two", Task: "task", After: []workflow.Dep{{Node: "one", When: ""}, {Node: "one", When: "success"}}, Actions: []string{" approve "}, Orchestrate: &workflow.Orchestrate{Candidates: []workflow.Candidate{{Agent: "agent-one", Hint: " hint "}}}},
	}}, Revision: 1}
	before, _ := json.Marshal(w)
	_, _, first, err := store.canonicalDraft(w)
	if err != nil {
		t.Fatal(err)
	}
	_, _, second, err := store.canonicalDraft(w)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(w)
	if first != second || !bytes.Equal(before, after) {
		t.Fatal("canonicalization mutated a source slice/pointer or changed its stable hash")
	}
}

func TestWorkflowAuthoringRejectsUnsupportedConditionInsteadOfDroppingIt(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return textTurn("unused"), nil })
	authoringBuilder(t, h, "alice")
	authoringCall(t, h, aliceToken, "edit", map[string]any{"action": "create", "request_id": "bad-condition", "name": "condition fixture", "nodes": []any{
		map[string]any{"id": "one", "kind": "human", "prompt": "Review"},
		map[string]any{"id": "two", "kind": "human", "prompt": "Review", "after": []any{map[string]any{"node": "one", "when": "success", "condition": "amount > 100"}}},
	}}, 400)
	if len(h.mgr.workflows.list(true)) != 0 {
		t.Fatal("unsupported data condition was silently saved as unconditional edge")
	}
}
