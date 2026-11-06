package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/webservice"
)

func TestWebServiceDeleteOwnerAPIIsScopedAndIdempotent(t *testing.T) {
	managed := newWebServiceHostManager(t)
	h := newMultiuserHarness(t, func(int) (llm.Response, error) {
		t.Error("deleting a website contacted the model")
		return llm.Response{}, nil
	}, func(c *Config) { c.WebServices = managed })
	sid, other := h.createAs(aliceToken), h.createAs(aliceToken)
	sess := h.session(sid)
	source := filepath.Join(sess.cwd, "site", "app.py")
	if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("print('fixture')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	scope := webservice.Scope{Owner: sessionResultOwnerKey(sess.owner), Session: sid, Workspace: sess.cwd}
	start := func(name string) string {
		t.Helper()
		data, err := managed.Execute(context.Background(), scope, webservice.Request{Action: "start", OperationID: "start-" + name, Spec: webservice.Spec{Name: name, ProjectRoot: "site", Profile: "python-web", StartArgv: []string{"python", "app.py"}}})
		if err != nil {
			t.Fatal(err)
		}
		var r webservice.Record
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		return r.ServiceID
	}
	id, sibling := start("target"), start("sibling")
	// Human cleanup remains available after removing the Agent's native tool.
	empty := []string{}
	if err := sess.agent.ReplaceNativeSelection(&empty, nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token, sid string
		status     int
	}{
		{bobToken, sid, http.StatusNotFound},
		{aliceToken, other, http.StatusNotFound},
		{aliceToken, sid, http.StatusNoContent},
		{aliceToken, sid, http.StatusNoContent},
	} {
		response := h.doAs(tc.token, http.MethodDelete, "/api/sessions/"+tc.sid+"/web-services/"+id, "")
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != tc.status || response.StatusCode == http.StatusNoContent && len(body) != 0 {
			t.Fatalf("delete owner=%s session=%s: %d %s", tc.token[:4], tc.sid, response.StatusCode, body)
		}
	}
	response := h.doAs(aliceToken, http.MethodGet, "/api/sessions/"+sid+"/web-services", "")
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || strings.Contains(string(body), id) || !strings.Contains(string(body), sibling) {
		t.Fatalf("list after deletion: %d %s", response.StatusCode, body)
	}
	response = h.doAs(aliceToken, http.MethodGet, "/api/sessions/"+sid+"/web-services/"+id, "")
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("deleted service is still readable")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("website deletion removed its workspace source:", err)
	}
	if _, err := h.mgr.Get(sid); err != nil {
		t.Fatal("website deletion removed the conversation:", err)
	}
}

type failingWebServiceDeleteRunner struct{ *webServiceHostRunner }

func (*failingWebServiceDeleteRunner) DeleteData(context.Context, webservice.Record) error {
	return webservice.Fail("STOP_UNCONFIRMED", "test volume is still in use")
}

func TestWebServiceDeleteAPIReportsPendingCleanup(t *testing.T) {
	managed, err := webservice.NewManager(webservice.Options{Root: filepath.Join(t.TempDir(), "private"), Installation: "delete-http", Runner: &failingWebServiceDeleteRunner{&webServiceHostRunner{running: map[string]bool{}}}, Gateway: webServiceHostGateway{}, PollInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return llm.Response{}, nil }, func(c *Config) { c.WebServices = managed })
	sid := h.createAs(aliceToken)
	sess := h.session(sid)
	if err := os.MkdirAll(filepath.Join(sess.cwd, "site"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sess.cwd, "site", "app.py"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := managed.Execute(context.Background(), webservice.Scope{Owner: sessionResultOwnerKey(sess.owner), Session: sid, Workspace: sess.cwd}, webservice.Request{Action: "start", OperationID: "start", Spec: webservice.Spec{Name: "site", ProjectRoot: "site", StartArgv: []string{"python", "app.py"}}})
	if err != nil {
		t.Fatal(err)
	}
	var record webservice.Record
	_ = json.Unmarshal(data, &record)
	response := h.doAs(aliceToken, http.MethodDelete, "/api/sessions/"+sid+"/web-services/"+record.ServiceID, "")
	var pending struct{ Code string }
	if err := json.NewDecoder(response.Body).Decode(&pending); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || pending.Code != "SERVICE_DELETE_PENDING" {
		t.Fatalf("cleanup falsely reported success: %d %+v", response.StatusCode, pending)
	}
	response = h.doAs(aliceToken, http.MethodGet, "/api/sessions/"+sid+"/web-services/"+record.ServiceID, "")
	data, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(data), `"phase":"deleting"`) {
		t.Fatalf("pending deletion disappeared: %d %s", response.StatusCode, data)
	}
}
