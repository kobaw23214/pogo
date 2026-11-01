package web

import (
	"bytes"
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

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/workflow"
)

func workflowAccessFixture(t *testing.T) (*muHarness, Workflow, workflow.RunRecord) {
	t.Helper()
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return textTurn("unused"), nil })
	wf, err := h.mgr.workflows.put("", workflowInput{Name: "权限检查", Nodes: []workflow.Node{{ID: "review", Kind: workflow.KindHuman, Prompt: "private instructions"}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	owner := identity.LocalScope("alice")
	rec := workflow.RunRecord{ID: "run_access_fixture", WorkflowID: wf.ID, Definition: wf.definition(), Revision: wf.Revision, Status: workflow.RunWorking, Owner: &owner, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Nodes: map[string]workflow.NodeState{"review": {Status: workflow.NodeWaitingHuman}}}
	if err := writeWFRecord(h.mgr.wfruns.recordPath(rec.ID), rec); err != nil {
		t.Fatal(err)
	}
	if err := taskRunsDB(t, h.mgr).ImportWorkflowRun(workflowMeta(rec, "legacy", "")); err != nil {
		t.Fatal(err)
	}
	return h, wf, rec
}

func workflowLocalRequest(s *Server, username string) *http.Request {
	id := s.localIdentity(username)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	return r.WithContext(context.WithValue(r.Context(), requestIdentityKey, &id))
}

func TestWorkflowAccessActionsAndExplicitRevocation(t *testing.T) {
	h, wf, rec := workflowAccessFixture(t)
	bob := workflowLocalRequest(h.psrv, "bob")
	alice := workflowLocalRequest(h.psrv, "alice")
	assertRun := func(r *http.Request, action, node string, want bool) {
		t.Helper()
		got, err := h.psrv.workflowRunAllowed(r, rec, action, node)
		if err != nil || got != want {
			t.Fatalf("run action %s/%s got=%v err=%v want=%v", action, node, got, err, want)
		}
	}
	assertRun(bob, wfRunRead, "", false)
	assertRun(alice, wfRunManageParticipants, "", true)
	db := taskRunsDB(t, h.mgr)
	bobID := h.psrv.workflowPrincipal(bob).ID
	if err := db.SetRunGrant(rec.ID, bobID, "", []string{wfRunRead}); err != nil {
		t.Fatal(err)
	}
	assertRun(bob, wfRunRead, "", true)
	assertRun(bob, wfRunCancel, "", false)
	assertRun(bob, wfRunApprove, "review", false)
	assertRun(bob, wfArtifactRead, "review", false)
	if err := db.SetRunGrant(rec.ID, bobID, "review", []string{wfRunApprove}); err != nil {
		t.Fatal(err)
	}
	assertRun(bob, wfRunApprove, "review", true)
	assertRun(bob, wfRunApprove, "another", false)
	assertRun(bob, wfRunManageParticipants, "", false)
	caps := h.psrv.workflowRunCapabilities(bob, rec)
	if !caps.CanRead || !caps.CanApprove || caps.CanCancel || len(caps.CanApproveNodes) != 1 || caps.CanApproveNodes[0] != "review" {
		t.Fatalf("capabilities widened granular grant: %+v", caps)
	}
	allowed, err := h.psrv.workflowAllowed(bob, wf.ID, wfInvoke)
	if err != nil || !allowed {
		t.Fatalf("local invocation compatibility lost: %v %v", allowed, err)
	}
	if err := db.SetWorkflowGrant(wf.ID, bobID, []string{}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{wfRead, wfInvoke} {
		if allowed, err := h.psrv.workflowAllowed(bob, wf.ID, action); err != nil || allowed {
			t.Fatalf("explicit workflow revocation ignored for %s: %v %v", action, allowed, err)
		}
	}
	if err := db.SetAccessRole(bobID, "disabled"); err != nil {
		t.Fatal(err)
	}
	assertRun(bob, wfRunRead, "", false)
	assertRun(bob, wfRunApprove, "review", false)
}

func TestWorkflowAccessLegacyNilOwnerAndNonLocalPrincipal(t *testing.T) {
	h, wf, rec := workflowAccessFixture(t)
	rec.Owner = nil
	bob := workflowLocalRequest(h.psrv, "bob")
	admin := workflowLocalRequest(h.psrv, bootstrapUser)
	if ok, err := h.psrv.workflowRunAllowed(bob, rec, wfRunRead, ""); err != nil || ok {
		t.Fatalf("nil owner history disclosed to ordinary user: %v %v", ok, err)
	}
	if ok, err := h.psrv.workflowRunAllowed(admin, rec, wfRunRead, ""); err != nil || !ok {
		t.Fatalf("operator lost legacy history: %v %v", ok, err)
	}
	// Exercise the single-user compatibility branch with an explicitly scoped
	// service caller. An asserted operator bit does not turn it into a user.
	single := &Server{mgr: h.mgr, auth: &authStore{}}
	r := withWorkflowPrincipal(httptest.NewRequest(http.MethodGet, "/", nil), identity.Principal{ID: "service:integration", Kind: identity.PrincipalService, Operator: true})
	if single.adminRequest(r) {
		t.Fatal("service inherited platform operator permissions")
	}
	guard := single.admin(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	recorder := httptest.NewRecorder()
	guard(recorder, r)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("operator surface admitted service: %d", recorder.Code)
	}
	for _, action := range []string{wfRead, wfEdit, wfPublish, wfInvoke} {
		if ok, err := single.workflowAllowed(r, wf.ID, action); err != nil || ok {
			t.Fatalf("service inherited single-user action %s: %v %v", action, ok, err)
		}
	}
	if ok, err := single.workflowRunAllowed(r, rec, wfRunRead, ""); err != nil || ok {
		t.Fatalf("service inherited legacy record visibility: %v %v", ok, err)
	}
}

func TestWorkflowAccessParticipantAPIAndReadOnlyCatalog(t *testing.T) {
	h, wf, rec := workflowAccessFixture(t)
	base := "/api/workflows/" + wf.ID + "/runs/" + rec.ID + "/participants"
	resp := h.doAs(bobToken, http.MethodGet, base, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("non-owner participants management exposed: %d", resp.StatusCode)
	}
	bobID := identity.LocalPrincipal(identity.LocalScope("bob"), "bob", false).ID
	body, _ := json.Marshal(map[string]any{"principal_id": bobID, "node_id": "review", "actions": []string{wfRunApprove, wfRunRead}})
	resp = h.doAs(aliceToken, http.MethodPut, base, string(body))
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("participant update: %d %s", resp.StatusCode, data)
	}
	var out struct {
		Participants []workflowAccessGrantView `json:"participants"`
		Subjects     []workflowAccessSubject   `json:"subjects"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Participants) != 1 || len(out.Subjects) != 2 || out.Participants[0].DisplayName != "bob" {
		t.Fatalf("participant picker failed: %+v %v", out, err)
	}
	body, _ = json.Marshal(map[string]any{"principal_id": bobID, "node_id": "review", "actions": []string{wfRunCancel}})
	resp = h.doAs(aliceToken, http.MethodPut, base, string(body))
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("node grant widened to cancel: %d", resp.StatusCode)
	}
	// The ordinary catalog read may scrub its response, never the live draft.
	resp = h.doAs(bobToken, http.MethodGet, "/api/manage/workflows", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("catalog read: %d", resp.StatusCode)
	}
	stored, _ := h.mgr.workflows.get(wf.ID)
	if stored.Nodes[0].Prompt != "private instructions" {
		t.Fatal("read-only catalog changed the saved workflow prompt")
	}
	// Removing an account must not make its retained grant impossible to
	// revoke. The picker can omit the deleted user while history keeps the ID.
	h.psrv.auth.mu.Lock()
	delete(h.psrv.auth.users, "bob")
	h.psrv.auth.mu.Unlock()
	body, _ = json.Marshal(map[string]any{"principal_id": bobID, "node_id": "review", "actions": []string{}})
	resp = h.doAs(aliceToken, http.MethodPut, base, string(body))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cannot revoke retained grant for a deleted account: %d", resp.StatusCode)
	}
}

func TestWorkflowAccessPublicationDirtyUsesFullDraftBeforeRedaction(t *testing.T) {
	h, wf, _ := workflowAccessFixture(t)
	agent, err := h.mgr.studio.put("", studioInput{Name: "publication fixture", Model: "k3", Status: "ready", Instructions: "fixture"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	input := wf.workflowInput
	input.Nodes = append(input.Nodes, workflow.Node{ID: "worker", Agent: agent.ID, Task: "PRIVATE_NODE_TASK", After: []workflow.Dep{{Node: "review"}}})
	wf, err = h.mgr.workflows.put(wf.ID, input, wf.Revision)
	if err != nil {
		t.Fatal(err)
	}
	ptr, err := h.mgr.workflows.publish(wf.ID, "published", "")
	if err != nil {
		t.Fatal(err)
	}
	check := func(token string, dirty, redacted bool) {
		t.Helper()
		for _, path := range []string{"/api/manage/workflows", "/api/manage/workflows/" + wf.ID} {
			resp := h.doAs(token, http.MethodGet, path, "")
			raw, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("read %s: %d %s %v", path, resp.StatusCode, raw, err)
			}
			var view workflowView
			if path == "/api/manage/workflows" {
				var list struct {
					Workflows []workflowView `json:"workflows"`
				}
				if err := json.Unmarshal(raw, &list); err != nil || len(list.Workflows) != 1 {
					t.Fatalf("workflow list: %s %v", raw, err)
				}
				view = list.Workflows[0]
			} else if err := json.Unmarshal(raw, &view); err != nil {
				t.Fatal(err)
			}
			if view.Dirty != dirty || view.Published == nil || view.Published.VersionID != ptr.VersionID {
				t.Fatalf("publication changed through redaction (%s): %+v", path, view)
			}
			if redacted && (view.Nodes[0].Prompt != "" || view.Nodes[1].Task != "" || strings.Contains(string(raw), "PRIVATE_NODE_TASK") || strings.Contains(string(raw), "private instructions")) {
				t.Fatalf("private draft leaked through %s: %s", path, raw)
			}
		}
	}
	check(h.token, false, false)
	check(aliceToken, false, true)
	stored, _ := h.mgr.workflows.get(wf.ID)
	if stored.Nodes[0].Prompt != "private instructions" || stored.Nodes[1].Task != "PRIVATE_NODE_TASK" || h.mgr.workflows.dirty(stored) {
		t.Fatal("publication read changed the stored draft")
	}
	input = stored.workflowInput
	input.Nodes = append([]workflow.Node(nil), input.Nodes...)
	input.Nodes[1].Task = "PRIVATE_CHANGED_NODE_TASK"
	if _, err := h.mgr.workflows.put(wf.ID, input, stored.Revision); err != nil {
		t.Fatal(err)
	}
	check(h.token, true, false)
	check(aliceToken, true, true)
	stored, _ = h.mgr.workflows.get(wf.ID)
	if stored.Nodes[0].Prompt != "private instructions" || stored.Nodes[1].Task != "PRIVATE_CHANGED_NODE_TASK" {
		t.Fatal("dirty read changed the stored draft")
	}
}

func TestWorkflowAccessBuilderPublishAndDisableAPI(t *testing.T) {
	h, wf, _ := workflowAccessFixture(t)
	bobID := identity.LocalPrincipal(identity.LocalScope("bob"), "bob", false).ID
	putRole := func(token, role string, want int) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"principal_id": bobID, "role": role})
		resp := h.doAs(token, http.MethodPut, "/api/manage/workflow-access/roles", string(body))
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("put role %s: %d %s", role, resp.StatusCode, data)
		}
	}
	putRole(bobToken, "space_admin", http.StatusForbidden)
	putRole(h.token, "builder", http.StatusOK)
	bob := workflowLocalRequest(h.psrv, "bob")
	if ok, err := h.psrv.workflowAllowed(bob, wf.ID, wfEdit); err != nil || !ok {
		t.Fatalf("builder cannot edit: %v %v", ok, err)
	}
	resp := h.doAs(bobToken, http.MethodPost, "/api/manage/workflows/"+wf.ID+"/publish", `{"name":"builder"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("builder unexpectedly published: %d", resp.StatusCode)
	}
	body, _ := json.Marshal(map[string]any{"principal_id": bobID, "actions": []string{wfRead, wfEdit, wfPublish, wfInvoke}})
	resp = h.doAs(h.token, http.MethodPut, "/api/manage/workflows/"+wf.ID+"/access", string(body))
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("grant publish: %d %s", resp.StatusCode, data)
	}
	resp = h.doAs(bobToken, http.MethodPost, "/api/manage/workflows/"+wf.ID+"/publish", `{"name":"builder"}`)
	data, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("granted builder cannot publish: %d %s", resp.StatusCode, data)
	}
	putRole(h.token, "disabled", http.StatusOK)
	if ok, err := h.psrv.workflowAllowed(bob, wf.ID, wfPublish); err != nil || ok {
		t.Fatalf("disabled role ignored existing publish grant: %v %v", ok, err)
	}
}

func TestWorkflowAccessNodeReviewProjectionAndArtifactBoundary(t *testing.T) {
	h, wf, rec := workflowAccessFixture(t)
	rec.Error = "SECRET_OTHER_ERROR"
	rec.Definition.Nodes = append(rec.Definition.Nodes, workflow.Node{ID: "secret", Kind: workflow.KindHuman, Prompt: "SECRET_DEFINITION_PROMPT"})
	rec.Nodes["review"] = workflow.NodeState{Status: workflow.NodeWaitingHuman, Human: &workflow.HumanState{Prompt: "材料可供本人审核", Actions: []string{"approve", "reject"}}}
	rec.Nodes["secret"] = workflow.NodeState{Status: workflow.NodeSuccess, Summary: "SECRET_OTHER_SUMMARY", Artifact: "private.txt", Inputs: map[string]string{"x": "SECRET_OTHER_INPUT"}, Human: &workflow.HumanState{Prompt: "SECRET_OTHER_HUMAN"}}
	if err := writeWFRecord(h.mgr.wfruns.recordPath(rec.ID), rec); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(h.mgr.wfruns.recordPath(rec.ID))
	if err != nil {
		t.Fatal(err)
	}
	bobID := identity.LocalPrincipal(identity.LocalScope("bob"), "bob", false).ID
	db := taskRunsDB(t, h.mgr)
	if err := db.SetRunGrant(rec.ID, bobID, "review", []string{wfRunApprove}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/workflows/" + wf.ID + "/runs/" + rec.ID,
		"/api/workflows/" + wf.ID + "/runs",
		"/api/workflows/" + wf.ID + "/runs/" + rec.ID + "/stream",
	} {
		resp := h.doAs(bobToken, http.MethodGet, path, "")
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("review projection %s: %d %s %v", path, resp.StatusCode, raw, err)
		}
		for _, secret := range []string{"SECRET_OTHER_ERROR", "SECRET_OTHER_SUMMARY", "SECRET_OTHER_INPUT", "SECRET_OTHER_HUMAN", "SECRET_DEFINITION_PROMPT", "private instructions"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("node-scoped review leaked %s through %s", secret, path)
			}
		}
		if !strings.Contains(string(raw), "材料可供本人审核") {
			t.Fatalf("reviewer lost assigned material through %s: %s", path, raw)
		}
	}
	after, err := os.ReadFile(h.mgr.wfruns.recordPath(rec.ID))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("response projection changed authoritative record: %v", err)
	}
	t.Run("artifact symlink cannot leave run workspace", func(t *testing.T) {
		workspace := filepath.Join(h.mgr.wfruns.runDir(rec.ID), "workspace")
		if err := os.MkdirAll(workspace, 0700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.txt")
		if err := os.WriteFile(outside, []byte("SECRET_OUTSIDE_ARTIFACT"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(workspace, "escape.txt")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		state := rec.Nodes["review"]
		state.Artifact = "escape.txt"
		rec.Nodes["review"] = state
		if err := writeWFRecord(h.mgr.wfruns.recordPath(rec.ID), rec); err != nil {
			t.Fatal(err)
		}
		if err := db.SetRunGrant(rec.ID, bobID, "review", []string{wfRunApprove, wfArtifactRead}); err != nil {
			t.Fatal(err)
		}
		resp := h.doAs(bobToken, http.MethodGet, "/api/workflows/"+wf.ID+"/runs/"+rec.ID+"/nodes/review/artifact", "")
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound || strings.Contains(string(raw), "SECRET_OUTSIDE_ARTIFACT") {
			t.Fatalf("artifact escaped run root: %d %s", resp.StatusCode, raw)
		}
	})
}
