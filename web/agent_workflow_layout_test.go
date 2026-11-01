package web

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/llm"
)

func awLayoutCall(t *testing.T, h *muHarness, token, method, aid, wid string, body any, want int) map[string]json.RawMessage {
	t.Helper()
	raw := ""
	if body != nil {
		raw = studioBody(body)
	}
	r := h.doAs(token, method, "/api/apps/"+aid+"/agent-workflows/"+wid+"/layout", raw)
	defer r.Body.Close()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != want {
		t.Fatalf("layout %s returned %d want %d: %s", method, r.StatusCode, want, data)
	}
	var result map[string]json.RawMessage
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err, string(data))
	}
	return result
}

func TestAgentWorkflowLayoutHTTPIsolationAndLiveRevocation(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { t.Error("layout called a model"); return textTurn("unused"), nil })
	a := awTestAgent(t, h, "画布布局助手")
	other := awTestAgent(t, h, "另一助手")
	d := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "create", "request_id": "layout-flow", "name": "布局权限", "steps": []af.Step{{ID: "review", Kind: "confirm", Prompt: "本人核对"}}}, 200), "workflow")
	get := awLayoutCall(t, h, aliceToken, http.MethodGet, a.ID, d.ID, nil, 200)
	layout := awTestValue[af.Layout](t, get, "layout")
	if layout.LayoutRevision != 0 || layout.DefinitionRevision != d.Revision {
		t.Fatal("layout did not start independently", layout)
	}
	body := af.LayoutSave{RequestID: "layout-change", ExpectedDefinitionRevision: d.Revision, Positions: map[string]af.Position{"review": {X: 300, Y: 150}, af.LayoutInputNode: {X: 20, Y: 150}}}
	layout = awTestValue[af.Layout](t, awLayoutCall(t, h, aliceToken, http.MethodPut, a.ID, d.ID, body, 200), "layout")
	if layout.LayoutRevision != 1 {
		t.Fatal(layout)
	}
	awLayoutCall(t, h, bobToken, http.MethodGet, a.ID, d.ID, nil, 404)
	awLayoutCall(t, h, bobToken, http.MethodPut, a.ID, d.ID, body, 404)
	awLayoutCall(t, h, aliceToken, http.MethodGet, other.ID, d.ID, nil, 404)
	awLayoutCall(t, h, aliceToken, http.MethodPut, other.ID, d.ID, body, 404)
	body.RequestID = "stale-position"
	awLayoutCall(t, h, aliceToken, http.MethodPut, a.ID, d.ID, body, 409)
	bad := map[string]any{"request_id": "identity-override", "expected_layout_revision": 1, "expected_definition_revision": d.Revision, "positions": map[string]af.Position{}, "owner_id": "bob"}
	awLayoutCall(t, h, aliceToken, http.MethodPut, a.ID, d.ID, bad, 400)
	update := studioAgentBody(a.Name, a.Instructions, "ready")
	update["revision"], update["tools"], update["skills"], update["mcp_servers"] = a.Revision, []string{}, []string{}, []string{}
	response := h.doAs(h.token, http.MethodPatch, "/api/manage/agents/"+a.ID, studioBody(update))
	defer response.Body.Close()
	if response.StatusCode != 200 {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("revoke %d: %s", response.StatusCode, data)
	}
	body.ExpectedLayoutRevision = 1
	awLayoutCall(t, h, aliceToken, http.MethodPut, a.ID, d.ID, body, 403)
	retained := awTestValue[af.Layout](t, awLayoutCall(t, h, aliceToken, http.MethodGet, a.ID, d.ID, nil, 200), "layout")
	if retained.LayoutRevision != 1 {
		t.Fatal("revocation changed existing layout")
	}
	unchanged := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "get", "workflow_id": d.ID}, 200), "workflow")
	if unchanged.Revision != d.Revision || unchanged.DefinitionHash != d.DefinitionHash {
		t.Fatal("layout edited semantic workflow")
	}
}

func TestAgentWorkflowLayoutMaintenanceAndArchiveBoundary(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return textTurn("unused"), nil })
	a := awTestAgent(t, h, "布局维护助手")
	d := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "create", "request_id": "maintenance-flow", "name": "维护布局", "steps": []af.Step{{ID: "review", Kind: "confirm", Prompt: "核对"}}}, 200), "workflow")
	marker := h.mgr.releaseMaintenancePath()
	release := strings.Repeat("e", 64)
	if err := os.MkdirAll(filepath.Dir(marker), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte(`{"token":"`+release+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		req, _ := http.NewRequest(method, h.srv.URL+"/api/apps/"+a.ID+"/agent-workflows/"+d.ID+"/layout", strings.NewReader(`{"request_id":"during-maintenance","expected_layout_revision":0,"expected_definition_revision":1,"positions":{}}`))
		req.Header.Set("Authorization", "Bearer "+aliceToken)
		req.Header.Set("X-Pogo-Release", release)
		req.Header.Set("Content-Type", "application/json")
		res, err := h.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		want := 200
		if method == http.MethodPut {
			want = 503
		}
		if res.StatusCode != want {
			t.Fatalf("maintenance %s=%d want%d", method, res.StatusCode, want)
		}
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "archive", "workflow_id": d.ID, "request_id": "archive-layout", "expected_revision": d.Revision}, 200)
	awLayoutCall(t, h, aliceToken, http.MethodPut, a.ID, d.ID, af.LayoutSave{RequestID: "after-archive", ExpectedDefinitionRevision: 2, Positions: map[string]af.Position{}}, 403)
	awLayoutCall(t, h, aliceToken, http.MethodGet, a.ID, d.ID, nil, 200)
}
