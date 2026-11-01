package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/mcpobserve"
)

// Exercise the real authenticated routes, not just the Store's predicates:
// richer error explanations must not create a cross-account diagnostic oracle.
func TestMCPDiagnosticScopeSafetyHTTPPaginationSearchAndDetails(t *testing.T) {
	t.Setenv("POGO_MCP_CALLS", "1")
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	create := func(name string) studioAgent {
		t.Helper()
		a, err := h.mgr.studio.put("", studioInput{Name: name, Instructions: "synthetic fixture", Model: "k3", Status: "ready"}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	a, other := create("Diagnostic Alpha"), create("Diagnostic Beta")
	owner := identity.LocalScope("alice").SubjectID
	foreignOwner := identity.LocalScope("bob").SubjectID
	now := time.Now().Add(-time.Minute).UnixMilli()
	makeCall := func(index int, agent, owner, session, summary, status string) mcpobserve.Call {
		return mcpobserve.Call{
			ID: mcpobserve.NewID(), AgentID: agent, OwnerID: owner, SessionID: session,
			Direction: "outbound", Server: "synthetic-diagnostic", Tool: "lookup", StartedAt: now + int64(index),
			Status: status, ErrorKind: "tool_error", ErrorSummary: summary, ErrorCode: "ACCESS_DENIED",
		}
	}
	calls := []mcpobserve.Call{
		makeCall(1, a.ID, owner, "first-session", "OwnDiagnosticOne permission denied password=HTTPPasswordCanary", "error"),
		makeCall(2, a.ID, owner, "second-session", "OwnDiagnosticTwo permission denied https://fixture.invalid/?token=HTTPQueryCanary", "error"),
		makeCall(3, a.ID, foreignOwner, "bob-session", "ForeignOwnerDiagnosticCanary", "error"),
		makeCall(4, other.ID, owner, "other-agent-session", "ForeignAgentDiagnosticCanary", "error"),
		makeCall(5, a.ID, "", "unowned-session", "UnownedDiagnosticCanary", "error"),
		makeCall(6, a.ID, owner, "success-session", "SuccessResultPayloadCanary", "ok"),
		makeCall(7, a.ID, owner, "running-session", "RunningResultPayloadCanary", "running"),
	}
	for _, c := range calls {
		h.psrv.mcpCalls.Record(c)
	}
	if err := h.psrv.mcpCalls.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	get := func(token, path string) (int, []byte) {
		t.Helper()
		response := h.doAs(token, http.MethodGet, path, "")
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, raw
	}
	assertAbsent := func(raw []byte, forbidden ...string) {
		t.Helper()
		for _, value := range forbidden {
			if strings.Contains(string(raw), value) {
				t.Fatalf("HTTP response leaked synthetic protected diagnostic %q", value)
			}
		}
	}
	foreign := []string{"ForeignOwnerDiagnosticCanary", "ForeignAgentDiagnosticCanary", "UnownedDiagnosticCanary"}
	secrets := []string{"HTTPPasswordCanary", "HTTPQueryCanary", "SuccessResultPayloadCanary", "RunningResultPayloadCanary"}
	prefix := "/api/apps/" + a.ID + "/mcp/calls"
	path := prefix + "?status=error&limit=1"
	seen := map[string]bool{}
	for pages := 0; ; pages++ {
		if pages > 2 {
			t.Fatal("diagnostic pagination failed to terminate")
		}
		code, raw := get(aliceToken, path)
		assertAbsent(raw, append(foreign, secrets...)...)
		var page mcpobserve.Page
		if err := json.Unmarshal(raw, &page); err != nil || code != 200 {
			t.Fatalf("scoped diagnostic page %d: %s, %v", code, raw, err)
		}
		for _, c := range page.Calls {
			if c.OwnerID != owner || c.AgentID != a.ID || c.ErrorCode != "ACCESS_DENIED" || c.ErrorSummary == "" || seen[c.ID] {
				t.Fatalf("scoped diagnostic page lost identity/reason or repeated a row: %+v", c)
			}
			seen[c.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		path = prefix + "?status=error&limit=1&before=" + url.QueryEscape(page.NextCursor)
	}
	if len(seen) != 2 || !seen[calls[0].ID] || !seen[calls[1].ID] {
		t.Fatalf("same Agent diagnostics did not span the two allowed sessions: %v", seen)
	}
	for index, c := range calls {
		code, raw := get(aliceToken, prefix+"/"+c.ID)
		assertAbsent(raw, append(foreign, secrets...)...)
		if index >= 2 && index <= 4 {
			if code != http.StatusNotFound {
				t.Fatalf("foreign diagnostic detail %d returned %d: %s", index, code, raw)
			}
			continue
		}
		var payload struct {
			Call mcpobserve.Call `json:"call"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil || code != 200 {
			t.Fatalf("allowed detail %d: %d %s, %v", index, code, raw, err)
		}
		detail := payload.Call
		if index < 2 {
			if detail.ErrorSummary == "" || detail.ErrorCode != "ACCESS_DENIED" {
				t.Fatalf("safe failure diagnostic missing from detail: %+v", detail)
			}
		} else if detail.ErrorSummary != "" || detail.ErrorCode != "" {
			t.Fatalf("success/running result was exposed as a failure diagnostic: %+v", detail)
		}
	}
	for _, query := range foreign {
		code, raw := get(aliceToken, prefix+"?q="+url.QueryEscape(query))
		var page mcpobserve.Page
		if err := json.Unmarshal(raw, &page); err != nil || code != 200 || len(page.Calls) != 0 {
			t.Fatalf("diagnostic search became a foreign-owner oracle: %d %s, %v", code, raw, err)
		}
		assertAbsent(raw, foreign...)
	}
	code, raw := get(aliceToken, prefix+"?q=OwnDiagnosticOne")
	var ownSearch mcpobserve.Page
	if err := json.Unmarshal(raw, &ownSearch); err != nil || code != 200 || len(ownSearch.Calls) != 1 || ownSearch.Calls[0].ID != calls[0].ID {
		t.Fatalf("safe summary search is not scoped/searchable: %d %s, %v", code, raw, err)
	}
	code, raw = get(aliceToken, prefix+"/stats?status=error&limit=1")
	var stats mcpobserve.Stats
	if err := json.Unmarshal(raw, &stats); err != nil || code != 200 || stats.Total != 2 || stats.Errors != 2 {
		t.Fatalf("diagnostic scope stats leaked foreign counts or page size: %d %s, %v", code, raw, err)
	}
	for _, path := range []string{
		prefix + "?owner_id=" + url.QueryEscape(foreignOwner),
		prefix + "/" + calls[0].ID + "?owner_id=" + url.QueryEscape(foreignOwner),
		prefix + "/stats?agent_id=" + other.ID,
	} {
		if code, raw := get(aliceToken, path); code != http.StatusBadRequest {
			t.Fatalf("model/request-supplied diagnostic scope was accepted: %d %s", code, raw)
		} else {
			assertAbsent(raw, append(foreign, secrets...)...)
		}
	}
	for _, tc := range []struct {
		token, path string
		status      int
	}{
		{"", prefix, http.StatusUnauthorized},
		{aliceToken, "/api/mcp/calls", http.StatusForbidden},
	} {
		code, raw := get(tc.token, tc.path)
		if code != tc.status {
			t.Fatalf("diagnostic authentication boundary returned %d, want %d: %s", code, tc.status, raw)
		}
		assertAbsent(raw, append(foreign, secrets...)...)
	}
}
