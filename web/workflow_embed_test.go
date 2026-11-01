package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/workflow"
)

func workflowEmbedFixture(t *testing.T) (*muHarness, Workflow, workflowPublication, string) {
	t.Helper()
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return textTurn("fixture only"), nil })
	wf, err := h.mgr.workflows.put("", workflowInput{Name: "固定嵌入版本", Nodes: []workflow.Node{{ID: "review", Kind: workflow.KindHuman, Prompt: "审批 {{ inputs.case_id }}", Fields: []workflow.FieldSpec{{Key: "note", Label: "说明", Type: "text", Required: true}}}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	ptr, err := h.mgr.workflows.publish(wf.ID, "v1", "")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(workflowPublication{Name: "外部系统入口", VersionID: ptr.VersionID, Enabled: true, AllowedOrigins: []string{"https://business.example", "http://127.0.0.1:5191"}, InputFields: []workflowPublicationInput{{Key: "case_id", Label: "事项", Required: true}}, Mappings: []workflowPublicationMapping{{Subject: "employee-a", Username: "alice"}, {Subject: "employee-b", Username: "bob"}}})
	status, raw := workflowServiceRequest(t, h.srv.URL, h.token, "POST", "/api/manage/workflows/"+wf.ID+"/publications", string(body), "")
	if status != 201 {
		t.Fatalf("create publication: %d %s", status, raw)
	}
	var response struct {
		Publication workflowPublication `json:"publication"`
		Secret      string              `json:"client_secret"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || response.Secret == "" {
		t.Fatalf("invalid publication receipt: %s %v", raw, err)
	}
	return h, wf, response.Publication, response.Secret
}
func embedPath(p workflowPublication, rest string) string {
	return workflowEmbedPrefix + p.ID + "/" + rest
}
func embedJSON(t *testing.T, h *muHarness, token, method, path string, body any, want int) map[string]json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(body)
	status, data := workflowServiceRequest(t, h.srv.URL, token, method, path, string(raw), "")
	if status != want {
		t.Fatalf("%s %s: %d %s, want %d", method, path, status, data, want)
	}
	out := map[string]json.RawMessage{}
	_ = json.Unmarshal(data, &out)
	return out
}
func embedString(t *testing.T, obj map[string]json.RawMessage, key string) string {
	t.Helper()
	var v string
	if err := json.Unmarshal(obj[key], &v); err != nil {
		t.Fatalf("missing %s: %v", key, err)
	}
	return v
}
func embedLaunch(t *testing.T, h *muHarness, p workflowPublication, secret, subject string) string {
	t.Helper()
	receipt := embedJSON(t, h, secret, "POST", embedPath(p, "launch"), map[string]any{"subject": subject, "origin": "https://business.example", "business_id": "business-42", "inputs": map[string]string{"case_id": "事项42"}}, 201)
	return embedString(t, receipt, "launch_code")
}
func embedSession(t *testing.T, h *muHarness, p workflowPublication, secret, subject string) string {
	t.Helper()
	code := embedLaunch(t, h, p, secret, subject)
	receipt := embedJSON(t, h, "", "POST", embedPath(p, "session"), map[string]string{"code": code, "parent_origin": "https://business.example"}, 201)
	return embedString(t, receipt, "session_token")
}

func TestWorkflowPublicationValidationPersistenceAndManagement(t *testing.T) {
	h, wf, p, secret := workflowEmbedFixture(t)
	if p.Revision != 1 || p.WorkflowID != wf.ID || p.VersionID == "" {
		t.Fatalf("bad pinned publication: %+v", p)
	}
	stored, err := os.ReadFile(h.psrv.workflowEmbed.publications.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), secret) || !strings.Contains(string(stored), "secret_hash") {
		t.Fatal("clear integration secret persisted")
	}
	reopened, err := openWorkflowPublications(h.mgr.cfg.SessionDir)
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := reopened.get(p.ID)
	if !ok || loaded.VersionID != p.VersionID || loaded.SecretHash == "" {
		t.Fatal("publication did not survive reopen")
	}
	meta := embedJSON(t, h, h.token, "GET", "/api/manage/workflows/"+wf.ID+"/publications", nil, 200)
	if strings.Contains(string(meta["publications"]), "secret_hash") {
		t.Fatal("secret hash leaked through list")
	}
	embedJSON(t, h, aliceToken, "GET", "/api/manage/workflows/"+wf.ID+"/publications", nil, 403)
	for _, origin := range []string{"*", "https://*.example", "https://business.example/path", "http://business.example", "null", "https://business.example; frame-ancestors *"} {
		if _, err := normalizeWorkflowEmbedOrigin(origin); err == nil {
			t.Fatalf("accepted broad/unsafe origin %q", origin)
		}
	}
	if got, err := normalizeWorkflowEmbedOrigin("https://BUSINESS.example:443/"); err != nil || got != "https://business.example" {
		t.Fatalf("canonical origin %q %v", got, err)
	}
	p.Mappings = []workflowPublicationMapping{{Subject: "admin", Username: bootstrapUser}}
	if err := h.psrv.validatePublication(&p); err == nil {
		t.Fatal("operator account mapping accepted")
	}
	if err := taskRunsDB(t, h.mgr).SetAccessRole(identity.LocalPrincipal(identity.LocalScope("alice"), "alice", false).ID, "space_admin"); err != nil {
		t.Fatal(err)
	}
	p.Mappings = []workflowPublicationMapping{{Subject: "elevated", Username: "alice"}}
	if err := h.psrv.validatePublication(&p); err == nil {
		t.Fatal("space administrator mapping accepted")
	}
}

func TestWorkflowEmbedSingleUseCredentialsExpiryAndRevocation(t *testing.T) {
	h, _, p, secret := workflowEmbedFixture(t)
	embedJSON(t, h, "wrong", "POST", embedPath(p, "launch"), map[string]string{"subject": "employee-a", "origin": "https://business.example"}, 401)
	embedJSON(t, h, secret, "POST", embedPath(p, "launch"), map[string]string{"subject": "unknown", "origin": "https://business.example"}, 403)
	embedJSON(t, h, secret, "POST", embedPath(p, "launch"), map[string]string{"subject": "employee-a", "origin": "https://elsewhere.example"}, 403)
	code := embedLaunch(t, h, p, secret, "employee-a")
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := `{"code":"` + code + `","parent_origin":"https://business.example"}`
			status, _ := workflowServiceRequest(t, h.srv.URL, "", "POST", embedPath(p, "session"), body, "")
			statuses <- status
		}()
	}
	wg.Wait()
	close(statuses)
	success := 0
	for status := range statuses {
		if status == 201 {
			success++
		} else if status != 401 {
			t.Fatalf("unexpected replay status %d", status)
		}
	}
	if success != 1 {
		t.Fatalf("single code redeemed %d times", success)
	}
	code = embedLaunch(t, h, p, secret, "employee-a")
	now := time.Now()
	h.psrv.workflowEmbed.now = func() time.Time { return now.Add(61 * time.Second) }
	embedJSON(t, h, "", "POST", embedPath(p, "session"), map[string]string{"code": code, "parent_origin": "https://business.example"}, 401)
	h.psrv.workflowEmbed.now = time.Now
	session := embedSession(t, h, p, secret, "employee-a")
	for _, path := range []string{"/api/manage/workflows", "/api/sessions", "/api/config"} {
		embedJSON(t, h, session, "GET", path, nil, 401)
	}
	cookieValue, _ := h.psrv.auth.signSession("alice", time.Now())
	for _, path := range []string{"/api/manage/workflows", embedPath(p, "session")} {
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookieValue})
		if path == "/api/manage/workflows" {
			req.Header.Set("Authorization", "Bearer "+session)
		}
		rr := httptest.NewRecorder()
		h.psrv.Handler().ServeHTTP(rr, req)
		if rr.Code != 401 {
			t.Fatal("embedded credential failure fell back to valid login cookie")
		}
	}
	// Prefixes must not reserve existing operator-configured account tokens.
	legacyAccountToken := "wes_fixture-existing-account-token"
	h.psrv.auth.mu.Lock()
	account := h.psrv.auth.users["alice"]
	account.Token = legacyAccountToken
	h.psrv.auth.users["alice"] = account
	h.psrv.auth.mu.Unlock()
	embedJSON(t, h, legacyAccountToken, "GET", "/api/manage/workflows", nil, 200)
	embedJSON(t, h, h.token, "GET", embedPath(p, "session"), nil, 401) // normal token is no alternate channel
	embedJSON(t, h, "", "GET", embedPath(p, "session")+"?token="+session, nil, 401)
	view := embedJSON(t, h, session, "GET", embedPath(p, "session"), nil, 200)
	if strings.Contains(string(view["publication"]), "mappings") {
		t.Fatal("other identity mappings leaked")
	}
	h.psrv.workflowEmbed.now = func() time.Time { return now.Add(16 * time.Minute) }
	embedJSON(t, h, session, "GET", embedPath(p, "session"), nil, 401)
	h.psrv.workflowEmbed.now = time.Now
	session = embedSession(t, h, p, secret, "employee-a")
	code = embedLaunch(t, h, p, secret, "employee-a")
	embedJSON(t, h, h.token, "POST", "/api/manage/workflows/"+p.WorkflowID+"/publications/"+p.ID+"/rotate-secret", nil, 200)
	embedJSON(t, h, session, "GET", embedPath(p, "session"), nil, 401)
	embedJSON(t, h, "", "POST", embedPath(p, "session"), map[string]string{"code": code, "parent_origin": "https://business.example"}, 401)
	embedJSON(t, h, secret, "POST", embedPath(p, "launch"), map[string]string{"subject": "employee-a", "origin": "https://business.example"}, 401)
}

func TestWorkflowEmbedPinnedRunsApprovalHistoryAndRetry(t *testing.T) {
	h, wf, p, secret := workflowEmbedFixture(t)
	alice := embedSession(t, h, p, secret, "employee-a")
	bob := embedSession(t, h, p, secret, "employee-b")
	// A later draft and current publish pointer must not move this entry.
	input := wf.workflowInput
	input.Name = "后来发布的流程"
	input.Nodes[0].Prompt = "changed"
	updated, err := h.mgr.workflows.put(wf.ID, input, wf.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.mgr.workflows.publish(updated.ID, "v2", ""); err != nil {
		t.Fatal(err)
	}
	start := map[string]any{"inputs": map[string]string{"case_id": "事项42"}, "request_key": "same-start"}
	receipt := embedJSON(t, h, alice, "POST", embedPath(p, "runs"), start, 201)
	rid := embedString(t, receipt, "id")
	if got := embedString(t, embedJSON(t, h, alice, "POST", embedPath(p, "runs"), start, 201), "id"); got != rid {
		t.Fatal("duplicate start created a second run")
	}
	embedJSON(t, h, alice, "POST", embedPath(p, "runs"), map[string]any{"inputs": map[string]string{"case_id": "different"}, "request_key": "same-start"}, 409)
	embedJSON(t, h, alice, "POST", embedPath(p, "runs"), map[string]any{"inputs": map[string]string{"unknown": "injected"}}, 400)
	rec := awaitWorkflowService(t, h.mgr, rid, func(r workflow.RunRecord) bool { return r.Nodes["review"].Status == workflow.NodeWaitingHuman })
	if rec.Definition.Name != "固定嵌入版本" || rec.Inputs["case_id"] != "事项42" || rec.Nodes["review"].Human.Prompt != "审批 事项42" {
		t.Fatalf("entry lost version/input snapshot: %+v", rec)
	}
	embedJSON(t, h, bob, "GET", embedPath(p, "runs/"+rid), nil, 404)
	embedJSON(t, h, alice, "POST", embedPath(p, "runs/"+rid+"/nodes/review/resume"), map[string]any{"action": "approve", "fields": map[string]string{"note": "确认"}}, 200)
	rec = awaitWorkflowService(t, h.mgr, rid, func(r workflow.RunRecord) bool { return r.Status == workflow.RunCompleted })
	if rec.ID != rid {
		t.Fatal("approval changed logical run")
	}
	meta, err := taskRunsDB(t, h.mgr).GetWorkflowRun(rid)
	if err != nil || meta.VersionID != p.VersionID || meta.PublicationID != p.ID || meta.ExternalSubject != "employee-a" || meta.BusinessID != "business-42" {
		t.Fatalf("missing embedded attribution: %+v %v", meta, err)
	}
	executions, err := taskRunsDB(t, h.mgr).ListWorkflowExecutions(rid)
	if err != nil || len(executions) != 2 {
		t.Fatalf("approval segments %d %v", len(executions), err)
	}
	// Existing management history receives exactly the same Run, no new store.
	status, raw := workflowServiceRequest(t, h.srv.URL, aliceToken, "GET", "/api/workflows/"+wf.ID+"/runs/"+rid, "", "")
	if status != 200 || !strings.Contains(string(raw), rid) {
		t.Fatalf("run not visible in old management API %d %s", status, raw)
	}
	second := embedString(t, embedJSON(t, h, alice, "POST", embedPath(p, "runs"), map[string]any{"inputs": map[string]string{"case_id": "cancel"}, "request_key": "cancel"}, 201), "id")
	awaitWorkflowService(t, h.mgr, second, func(r workflow.RunRecord) bool { return r.Nodes["review"].Status == workflow.NodeWaitingHuman })
	embedJSON(t, h, alice, "POST", embedPath(p, "runs/"+second+"/cancel"), nil, 200)
	awaitWorkflowService(t, h.mgr, second, func(r workflow.RunRecord) bool { return r.Status == workflow.RunCancelled })
	third := embedString(t, embedJSON(t, h, alice, "POST", embedPath(p, "runs/"+second+"/retry"), nil, 201), "id")
	if third == second {
		t.Fatal("retry reused old Run")
	}
	retry, err := taskRunsDB(t, h.mgr).GetWorkflowRun(third)
	if err != nil || retry.RetryOf != second || retry.PublicationID != p.ID || retry.VersionID != p.VersionID || retry.Inputs["case_id"] != "cancel" {
		t.Fatalf("retry lost publication attribution/input: %+v %v", retry, err)
	}
	embedJSON(t, h, alice, "POST", embedPath(p, "runs/"+third+"/cancel"), nil, 200)
	// Shared node reviewers must not receive whole-run business inputs.
	bobID := identity.LocalPrincipal(identity.LocalScope("bob"), "bob", false).ID
	if err := taskRunsDB(t, h.mgr).SetRunGrant(rid, bobID, "review", []string{wfRunRead}); err != nil {
		t.Fatal(err)
	}
	projected := h.psrv.projectWorkflowRun(workflowLocalRequest(h.psrv, "bob"), rec)
	if len(projected.Inputs) != 0 || projected.Origin != nil {
		t.Fatal("node reviewer received run-wide inputs or business attribution")
	}
	if err := taskRunsDB(t, h.mgr).SetWorkflowGrant(wf.ID, identity.LocalPrincipal(identity.LocalScope("alice"), "alice", false).ID, []string{wfRead}); err != nil {
		t.Fatal(err)
	}
	embedJSON(t, h, alice, "POST", embedPath(p, "runs"), start, 403)
}

func TestWorkflowEmbedMappingChangesRoleDisableAndRunBoundary(t *testing.T) {
	h, wf, p, secret := workflowEmbedFixture(t)
	session := embedSession(t, h, p, secret, "employee-a")
	other, err := h.mgr.workflows.put("", workflowInput{Name: "其他流程", Nodes: []workflow.Node{{ID: "review", Kind: workflow.KindHuman, Prompt: "private"}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	owner := identity.LocalScope("alice")
	foreign := workflow.RunRecord{ID: "run_other_workflow", WorkflowID: other.ID, Definition: other.definition(), Revision: 1, Status: workflow.RunCompleted, Owner: &owner, Nodes: map[string]workflow.NodeState{}, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := writeWFRecord(h.mgr.wfruns.recordPath(foreign.ID), foreign); err != nil {
		t.Fatal(err)
	}
	embedJSON(t, h, session, "GET", embedPath(p, "runs/"+foreign.ID), nil, 404)
	// Removing a mapping invalidates both an already redeemed session and an unused code.
	code := embedLaunch(t, h, p, secret, "employee-a")
	p, _ = h.psrv.workflowEmbed.publications.get(p.ID)
	p.Mappings = []workflowPublicationMapping{{Subject: "employee-b", Username: "bob"}}
	if err := h.psrv.workflowEmbed.publications.save(p, p.Revision); err != nil {
		t.Fatal(err)
	}
	embedJSON(t, h, session, "GET", embedPath(p, "session"), nil, 401)
	embedJSON(t, h, "", "POST", embedPath(p, "session"), map[string]string{"code": code, "parent_origin": "https://business.example"}, 401)
	p, _ = h.psrv.workflowEmbed.publications.get(p.ID)
	p.Mappings = append(p.Mappings, workflowPublicationMapping{Subject: "employee-a", Username: "alice"})
	if err := h.psrv.workflowEmbed.publications.save(p, p.Revision); err != nil {
		t.Fatal(err)
	}
	session = embedSession(t, h, p, secret, "employee-a")
	principal := identity.LocalPrincipal(owner, "alice", false)
	if err := taskRunsDB(t, h.mgr).SetAccessRole(principal.ID, "disabled"); err != nil {
		t.Fatal(err)
	}
	embedJSON(t, h, session, "GET", embedPath(p, "session"), nil, 401)
	embedJSON(t, h, secret, "POST", embedPath(p, "launch"), map[string]string{"subject": "employee-a", "origin": "https://business.example"}, 403)
	if err := taskRunsDB(t, h.mgr).SetAccessRole(principal.ID, ""); err != nil {
		t.Fatal(err)
	}
	session = embedSession(t, h, p, secret, "employee-a")
	if err := taskRunsDB(t, h.mgr).SetWorkflowGrant(wf.ID, principal.ID, []string{}); err != nil {
		t.Fatal(err)
	}
	embedJSON(t, h, session, "POST", embedPath(p, "runs"), map[string]any{"inputs": map[string]string{"case_id": "revoked"}}, 403)
}

func TestWorkflowEmbedPageHeadersAndDisabledMapping(t *testing.T) {
	h, _, p, secret := workflowEmbedFixture(t)
	session := embedSession(t, h, p, secret, "employee-a")
	response, err := http.Get(h.srv.URL + workflowEmbedPagePrefix + p.ID)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Referrer-Policy") != "no-referrer" || response.Header.Get("Content-Security-Policy") != "frame-ancestors http://127.0.0.1:5191 https://business.example" {
		t.Fatalf("missing exact embed headers: %d %+v", response.StatusCode, response.Header)
	}
	p, _ = h.psrv.workflowEmbed.publications.get(p.ID)
	p.Enabled = false
	if err := h.psrv.workflowEmbed.publications.save(p, p.Revision); err != nil {
		t.Fatal(err)
	}
	embedJSON(t, h, session, "GET", embedPath(p, "session"), nil, 401)
	rr := httptest.NewRecorder()
	h.psrv.Handler().ServeHTTP(rr, httptest.NewRequest("GET", workflowEmbedPagePrefix+p.ID, nil))
	if rr.Code != 404 || rr.Header().Get("Content-Security-Policy") != "frame-ancestors 'none'" {
		t.Fatal("disabled entry remains embeddable")
	}
	// Even unauthenticated single-user deployments never treat an embed token as operator authority.
	single := &Server{auth: &authStore{}, opts: ServerOptions{}}
	rr = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/config?token="+session, nil)
	single.withToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })).ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatal("embed token fell through single-user compatibility")
	}
	legacy := "wes_legacy-configured-operator-token"
	single.opts.Token = legacy
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/config", nil)
	req.Header.Set("Authorization", "Bearer "+legacy)
	single.withToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })).ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatal("new embed credential prefix broke existing configured operator token")
	}
}
