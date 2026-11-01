package web

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/workflow"
)

func businessEmbedSession(t *testing.T, h *muHarness, p workflowPublication, secret, subject, business, rid string, want int) string {
	t.Helper()
	launch := embedJSON(t, h, secret, "POST", embedPath(p, "launch"), map[string]any{
		"subject": subject, "origin": "https://business.example", "business_id": business, "run_id": rid,
		"inputs": map[string]string{"case_id": business},
	}, want)
	if want != http.StatusCreated {
		return ""
	}
	code := embedString(t, launch, "launch_code")
	return embedString(t, embedJSON(t, h, "", "POST", embedPath(p, "session"), map[string]string{"code": code, "parent_origin": "https://business.example"}, 201), "session_token")
}

func businessEmbedStart(t *testing.T, h *muHarness, p workflowPublication, token, value, key string) string {
	t.Helper()
	receipt := embedJSON(t, h, token, "POST", embedPath(p, "runs"), map[string]any{
		"inputs": map[string]string{"case_id": value}, "request_key": key,
		// Browser-owned fields cannot replace the credential's business scope.
		"business_id": "browser-cannot-switch-this",
	}, 201)
	rid := embedString(t, receipt, "id")
	awaitWorkflowService(t, h.mgr, rid, func(r workflow.RunRecord) bool { return r.Nodes["review"].Status == workflow.NodeWaitingHuman })
	return rid
}

func businessEmbedRuns(t *testing.T, h *muHarness, p workflowPublication, token, query string) []workflow.RunRecord {
	t.Helper()
	response := embedJSON(t, h, token, "GET", embedPath(p, "runs")+query, nil, 200)
	var runs []workflow.RunRecord
	if err := json.Unmarshal(response["runs"], &runs); err != nil {
		t.Fatal(err)
	}
	return runs
}

func TestWorkflowBusinessScopeSeparatesSameUserAcrossBusinessAndPublication(t *testing.T) {
	h, wf, p, secret := workflowEmbedFixture(t)
	if p.HistoryScope != "business" {
		t.Fatalf("new publication default scope = %q", p.HistoryScope)
	}
	for _, business := range []string{"", " ", " ORDER-A", "ORDER-A\tB", strings.Repeat("x", 257)} {
		businessEmbedSession(t, h, p, secret, "employee-a", business, "", 400)
	}
	a := businessEmbedSession(t, h, p, secret, "employee-a", "ORDER-A", "", 201)
	b := businessEmbedSession(t, h, p, secret, "employee-a", "ORDER-B", "", 201)
	a1 := businessEmbedStart(t, h, p, a, "A first", "same-intent")
	a2 := businessEmbedStart(t, h, p, a, "A second", "A-second")
	b1 := businessEmbedStart(t, h, p, b, "B first", "same-intent")
	if a1 == b1 || businessEmbedStart(t, h, p, a, "A first", "same-intent") != a1 {
		t.Fatal("business request intents were not independent and idempotent")
	}
	meta, err := taskRunsDB(t, h.mgr).GetWorkflowRun(a1)
	if err != nil || meta.BusinessID != "ORDER-A" || meta.PublicationID != p.ID {
		t.Fatalf("browser changed immutable business scope: %+v %v", meta, err)
	}
	allA := businessEmbedRuns(t, h, p, a, "?business_id=ORDER-B")
	if len(allA) != 2 || !((allA[0].ID == a1 && allA[1].ID == a2) || (allA[0].ID == a2 && allA[1].ID == a1)) {
		t.Fatalf("business A history leaked another business: %+v", allA)
	}
	for offset := range 2 {
		page := businessEmbedRuns(t, h, p, a, "?limit=1&offset="+strconv.Itoa(offset))
		if len(page) != 1 || page[0].ID != allA[offset].ID {
			t.Fatalf("pagination occurred before business filtering: %+v", page)
		}
	}
	if got := businessEmbedRuns(t, h, p, a, "?limit=1&offset=2"); len(got) != 0 {
		t.Fatalf("another business displaced the last page: %+v", got)
	}
	allB := businessEmbedRuns(t, h, p, b, "")
	if len(allB) != 1 || allB[0].ID != b1 {
		t.Fatalf("business B history = %+v", allB)
	}
	// Every record-specific route checks business attribution before its usual
	// capability check, even though this same platform user owns both records.
	for _, request := range []struct{ method, suffix string }{
		{"GET", ""}, {"POST", "/cancel"}, {"POST", "/retry"},
		{"POST", "/nodes/review/resume"}, {"GET", "/nodes/review/artifact"},
	} {
		embedJSON(t, h, a, request.method, embedPath(p, "runs/"+b1+request.suffix), map[string]any{"action": "approve", "fields": map[string]string{"note": "must not apply"}}, 404)
	}
	businessEmbedSession(t, h, p, secret, "employee-a", "ORDER-A", b1, 404)
	businessEmbedSession(t, h, p, secret, "employee-a", "ORDER-A", a1, 201)

	// The same business id in another publication is a different boundary.
	other := p
	other.Name = "另一业务入口"
	created := embedJSON(t, h, h.token, "POST", "/api/manage/workflows/"+wf.ID+"/publications", other, 201)
	var p2 workflowPublication
	if err := json.Unmarshal(created["publication"], &p2); err != nil {
		t.Fatal(err)
	}
	secret2 := embedString(t, created, "client_secret")
	p2a := businessEmbedSession(t, h, p2, secret2, "employee-a", "ORDER-A", "", 201)
	if got := businessEmbedRuns(t, h, p2, p2a, ""); len(got) != 0 {
		t.Fatalf("same business id crossed publications: %+v", got)
	}
	embedJSON(t, h, p2a, "GET", embedPath(p2, "runs/"+a1), nil, 404)

	// A public record's Origin must never substitute for catalog attribution.
	owner := identity.LocalScope("alice")
	spoof := workflow.RunRecord{ID: "run_business_origin_spoof", WorkflowID: wf.ID, Definition: wf.definition(), Status: workflow.RunCompleted,
		Owner: &owner, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Nodes: map[string]workflow.NodeState{},
		Origin: &workflow.RunOrigin{PublicationID: p.ID, BusinessID: "ORDER-A"}}
	if err := writeWFRecord(h.mgr.wfruns.recordPath(spoof.ID), spoof); err != nil {
		t.Fatal(err)
	}
	spoofMeta := workflowMeta(spoof, "legacy", "")
	if err := taskRunsDB(t, h.mgr).ImportWorkflowRun(spoofMeta); err != nil {
		t.Fatal(err)
	}
	embedJSON(t, h, a, "GET", embedPath(p, "runs/"+spoof.ID), nil, 404)
	status, _ := workflowServiceRequest(t, h.srv.URL, aliceToken, "GET", "/api/workflows/"+wf.ID+"/runs/"+spoof.ID, "", "")
	if status != 200 {
		t.Fatal("legacy history became inaccessible through its original API")
	}
	if len(businessEmbedRuns(t, h, p, a, "")) != 2 {
		t.Fatal("unattributed legacy history entered the business list")
	}
}

func TestWorkflowBusinessScopeRetainsNodePermissionsAndRetryAttribution(t *testing.T) {
	h, _, p, secret := workflowEmbedFixture(t)
	a := businessEmbedSession(t, h, p, secret, "employee-a", "ORDER-A", "", 201)
	b := businessEmbedSession(t, h, p, secret, "employee-a", "ORDER-B", "", 201)
	a1 := businessEmbedStart(t, h, p, a, "original A", "first")
	b1 := businessEmbedStart(t, h, p, b, "original B", "first")
	bobA := businessEmbedSession(t, h, p, secret, "employee-b", "ORDER-A", "", 201)
	bobB := businessEmbedSession(t, h, p, secret, "employee-b", "ORDER-B", "", 201)
	bobID := identity.LocalPrincipal(identity.LocalScope("bob"), "bob", false).ID
	for _, rid := range []string{a1, b1} {
		if err := taskRunsDB(t, h.mgr).SetRunGrant(rid, bobID, "review", []string{wfRunRead, wfRunApprove, wfArtifactRead}); err != nil {
			t.Fatal(err)
		}
	}
	embedJSON(t, h, bobA, "GET", embedPath(p, "runs/"+a1), nil, 200)
	embedJSON(t, h, bobA, "POST", embedPath(p, "runs/"+a1+"/cancel"), nil, 404)
	embedJSON(t, h, bobB, "GET", embedPath(p, "runs/"+a1), nil, 404)
	embedJSON(t, h, bobB, "POST", embedPath(p, "runs/"+a1+"/nodes/review/resume"), map[string]any{"action": "approve", "fields": map[string]string{"note": "cross-business"}}, 404)
	embedJSON(t, h, bobA, "POST", embedPath(p, "runs/"+a1+"/nodes/review/resume"), map[string]any{"action": "approve", "fields": map[string]string{"note": "approved within A"}}, 200)
	awaitWorkflowService(t, h.mgr, a1, func(r workflow.RunRecord) bool { return r.Status == workflow.RunCompleted })
	// The reviewer can read the allowed artifact only in its bound business.
	status, raw := workflowServiceRequest(t, h.srv.URL, bobA, "GET", embedPath(p, "runs/"+a1+"/nodes/review/artifact"), "", "")
	if status != 200 || !strings.Contains(string(raw), "approved within A") {
		t.Fatalf("node artifact grant lost: %d %s", status, raw)
	}
	embedJSON(t, h, bobB, "GET", embedPath(p, "runs/"+a1+"/nodes/review/artifact"), nil, 404)
	if got := businessEmbedRuns(t, h, p, bobB, ""); len(got) != 1 || got[0].ID != b1 {
		t.Fatalf("node grants widened a business boundary: %+v", got)
	}

	aCancel := businessEmbedStart(t, h, p, a, "A retry input", "cancel")
	for _, entry := range []struct{ token, rid, business string }{{a, aCancel, "ORDER-A"}, {b, b1, "ORDER-B"}} {
		embedJSON(t, h, entry.token, "POST", embedPath(p, "runs/"+entry.rid+"/cancel"), nil, 200)
		awaitWorkflowService(t, h.mgr, entry.rid, func(r workflow.RunRecord) bool { return r.Status == workflow.RunCancelled })
		// The same caller-chosen retry key is isolated by business and source Run.
		code, raw := workflowServiceRequest(t, h.srv.URL, entry.token, "POST", embedPath(p, "runs/"+entry.rid+"/retry"), "", "same-retry-intent")
		if code != 201 {
			t.Fatalf("retry: %d %s", code, raw)
		}
		var receipt map[string]json.RawMessage
		_ = json.Unmarshal(raw, &receipt)
		retry := embedString(t, receipt, "id")
		meta, err := taskRunsDB(t, h.mgr).GetWorkflowRun(retry)
		if err != nil || meta.BusinessID != entry.business || meta.PublicationID != p.ID || meta.RetryOf != entry.rid {
			t.Fatalf("retry lost business attribution: %+v %v", meta, err)
		}
		code, raw = workflowServiceRequest(t, h.srv.URL, entry.token, "POST", embedPath(p, "runs/"+entry.rid+"/retry"), "", "same-retry-intent")
		_ = json.Unmarshal(raw, &receipt)
		if code != 201 || embedString(t, receipt, "id") != retry {
			t.Fatalf("retry response loss caused duplicate Run: %d %s", code, raw)
		}
	}
	embedJSON(t, h, b, "POST", embedPath(p, "runs/"+aCancel+"/retry"), nil, 404)
}

func TestWorkflowBusinessScopePreservesLegacyPublicationAndHistory(t *testing.T) {
	h, wf, p, secret := workflowEmbedFixture(t)
	path := h.psrv.workflowEmbed.publications.path
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	var publications map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data["publications"], &publications); err != nil {
		t.Fatal(err)
	}
	delete(publications[p.ID], "history_scope")
	data["publications"], _ = json.Marshal(publications)
	legacyRaw, _ := json.Marshal(data)
	if err := os.WriteFile(path, legacyRaw, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := openWorkflowPublications(h.mgr.cfg.SessionDir)
	if err != nil {
		t.Fatal(err)
	}
	legacy, ok := reopened.get(p.ID)
	if !ok || legacy.HistoryScope != "workflow" {
		t.Fatalf("legacy publication default scope = %+v", legacy)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(legacyRaw) {
		t.Fatal("loading legacy publication rewrote its file")
	}
	// Loading a legacy row changes no grants or Run ids. Explicit workflow
	// scope also remains available for integrations that require that history.
	if err := h.psrv.workflowEmbed.publications.save(legacy, legacy.Revision); err != nil {
		t.Fatal(err)
	}
	legacy, _ = h.psrv.workflowEmbed.publications.get(p.ID)
	session := businessEmbedSession(t, h, legacy, secret, "employee-a", "", "", 201)
	owner := identity.LocalScope("alice")
	rec := workflow.RunRecord{ID: "run_preserved_legacy_business", WorkflowID: wf.ID, Definition: wf.definition(), Status: workflow.RunCompleted, Owner: &owner,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Nodes: map[string]workflow.NodeState{}}
	if err := writeWFRecord(h.mgr.wfruns.recordPath(rec.ID), rec); err != nil {
		t.Fatal(err)
	}
	if err := taskRunsDB(t, h.mgr).ImportWorkflowRun(workflowMeta(rec, "legacy", "")); err != nil {
		t.Fatal(err)
	}
	embedJSON(t, h, session, "GET", embedPath(legacy, "runs/"+rec.ID), nil, 200)
	if got := businessEmbedRuns(t, h, legacy, session, ""); len(got) != 1 || got[0].ID != rec.ID {
		t.Fatalf("legacy workflow history changed: %+v", got)
	}
	// Older clients omitting the added field must not silently change scope.
	legacy.HistoryScope = ""
	updated := embedJSON(t, h, h.token, "PUT", "/api/manage/workflows/"+wf.ID+"/publications/"+p.ID, legacy, 200)
	if embedString(t, updated, "history_scope") != "workflow" {
		t.Fatal("legacy update silently changed the integration scope")
	}
}
