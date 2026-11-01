package web

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/yosukeno/pogo/identity"
)

func TestWorkflowEditorCapabilitiesAreScopedMetadata(t *testing.T) {
	h, wf, _ := workflowAccessFixture(t)
	path := "/api/manage/workflows/" + wf.ID + "/capabilities"
	request := func(token, path string, want int) []byte {
		t.Helper()
		resp := h.doAs(token, http.MethodGet, path, "")
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != want {
			t.Fatalf("GET %s: %d %s %v", path, resp.StatusCode, raw, err)
		}
		return raw
	}
	request(aliceToken, path, http.StatusForbidden)
	alice := identity.LocalPrincipal(identity.LocalScope("alice"), "alice", false)
	if err := taskRunsDB(t, h.mgr).SetAccessRole(alice.ID, "builder"); err != nil {
		t.Fatal(err)
	}
	request(aliceToken, "/api/manage/capabilities", http.StatusForbidden)
	raw := request(aliceToken, path, http.StatusOK)
	var catalog map[string]json.RawMessage
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 1 || catalog["builtin_tools"] == nil {
		t.Fatalf("workflow editor received admin metadata: %s", raw)
	}
	var entries []map[string]any
	if err := json.Unmarshal(catalog["builtin_tools"], &entries); err != nil || len(entries) == 0 {
		t.Fatalf("empty workflow tool picker: %s %v", raw, err)
	}
	for _, entry := range entries {
		if entry["name"] == "" || entry["description"] == "" || len(entry) != 4 {
			t.Fatalf("unsafe or incomplete metadata: %+v", entry)
		}
		for key := range entry {
			if key != "name" && key != "description" && key != "read_only" && key != "protected" {
				t.Fatalf("unexpected editor field %q", key)
			}
		}
	}
	if err := taskRunsDB(t, h.mgr).SetWorkflowGrant(wf.ID, alice.ID, []string{wfRead}); err != nil {
		t.Fatal(err)
	}
	request(aliceToken, path, http.StatusForbidden)
	request(h.token, path, http.StatusOK)
}
