package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
	"github.com/yosukeno/pogo/webservice"
)

func TestWebServiceOwnerLinkReachesNativeToolAndOwnerAPI(t *testing.T) {
	managed := newWebServiceHostManager(t)
	service := make(chan string, 1)
	h := newHarness(t, func(turn int) (llm.Response, error) {
		if turn == 1 {
			return toolTurn(tools.WebServiceName, studioBody(map[string]any{"action": "open", "operation_id": "native-open", "service_id": <-service})), nil
		}
		return textTurn("opened"), nil
	}, func(c *Config) { c.WebServices = managed })
	definition := postStudioAgent(t, h, studioAgentCapsBody("Website owner links", "ready", map[string]any{"tools": []string{tools.WebServiceName}}), http.StatusCreated)
	sid := startStudioConversation(t, h, definition.ID)
	sess := h.session(sid)
	sess.Policy().Set(ModeAuto, 0)
	if err := os.MkdirAll(filepath.Join(sess.cwd, "site"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sess.cwd, "site", "app.py"), []byte("print('fixture')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	response := h.do(http.MethodPost, "/api/sessions/"+sid+"/web-services", `{"operation_id":"owner-start","name":"api","project_root":"site","profile":"python-web","start_argv":["python","app.py"]}`)
	var receipt webservice.Record
	if err := json.NewDecoder(response.Body).Decode(&receipt); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || receipt.ServiceID == "" {
		t.Fatalf("start status=%d service=%q", response.StatusCode, receipt.ServiceID)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		response = h.do(http.MethodGet, "/api/sessions/"+sid+"/web-services/"+receipt.ServiceID, "")
		var current webservice.Record
		if err := json.NewDecoder(response.Body).Decode(&current); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		if current.State == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("service did not become ready: %+v", current)
		}
		time.Sleep(time.Millisecond)
	}

	service <- receipt.ServiceID
	studioAsk(t, h, sid, "open the website")
	sess.wait()
	var native webservice.Grant
	for _, message := range sess.agent.Messages() {
		for _, block := range message.Content {
			if block.Type == llm.BlockToolResult {
				var result webservice.Grant
				if json.Unmarshal(block.Details, &result) == nil && result.PreviewURL != "" {
					native = result
				}
			}
		}
	}
	want := "/a/" + definition.ID + "?" + url.Values{"c": {sid}, "web_service": {receipt.ServiceID}}.Encode()
	if native.ServiceURL != want || native.PreviewURL == "" {
		t.Fatalf("native tool did not receive owner link: %+v, want %s", native, want)
	}

	// Proxy/header input cannot choose the link's hostname, protocol or scope.
	request, err := http.NewRequest(http.MethodPost, h.srv.URL+"/api/sessions/"+sid+"/web-services/"+receipt.ServiceID+"/previews", strings.NewReader(`{"operation_id":"owner-open"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+h.token)
	request.Host = "attacker.invalid"
	request.Header.Set("X-Forwarded-Host", "attacker.invalid")
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("Forwarded", "host=attacker.invalid;proto=https")
	response, err = h.srv.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var owner webservice.Grant
	if err := json.NewDecoder(response.Body).Decode(&owner); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || owner.ServiceURL != want || strings.Contains(owner.ServiceURL, "attacker.invalid") {
		t.Fatalf("owner API link was not host-bound: status=%d grant=%+v", response.StatusCode, owner)
	}

	// The authenticated reference is retained in a public snapshot, while the
	// temporary bearer URL is removed with the existing share policy.
	shared, _ := json.Marshal(map[string]any{"service_url": owner.ServiceURL, "preview_url": owner.PreviewURL})
	clean, err := redactSharedServiceSnapshot(shared, managed.RedactPreviewURLs)
	var redacted webservice.Grant
	if err != nil || json.Unmarshal(clean, &redacted) != nil || redacted.PreviewURL == owner.PreviewURL || redacted.ServiceURL != want {
		t.Fatalf("share removed owner reference or retained bearer URL: %s %v", clean, err)
	}

	// Once revoked, the old operation must provide a machine-readable recovery
	// code rather than an apparently usable cached URL or a string-only error.
	response = h.do(http.MethodDelete, "/api/sessions/"+sid+"/web-services/"+receipt.ServiceID+"/previews/"+owner.GrantID, "")
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("revoke status=%d", response.StatusCode)
	}
	response = h.do(http.MethodPost, "/api/sessions/"+sid+"/web-services/"+receipt.ServiceID+"/previews", `{"operation_id":"owner-open"}`)
	var failure struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(response.Body).Decode(&failure); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || failure.Code != "GRANT_EXPIRED" || !strings.Contains(failure.Error, "GRANT_EXPIRED") {
		t.Fatalf("expired open response: status=%d error=%+v", response.StatusCode, failure)
	}
}

type fixedWebServiceLinkBackend struct{ data json.RawMessage }

func (b fixedWebServiceLinkBackend) Execute(context.Context, webservice.Request) (json.RawMessage, error) {
	return b.data, nil
}

func TestWebServiceOwnerLinkPreservesPendingAndOtherReceipts(t *testing.T) {
	for _, data := range []json.RawMessage{
		json.RawMessage(`{"service_id":"ws_test","operation_status":"accepted","accepted":true,"generation":9007199254740993}`),
		json.RawMessage(`{"services":[]}`),
	} {
		backend := &webServiceOwnerLinks{Backend: fixedWebServiceLinkBackend{data}, agentID: "agent", sessionID: "session"}
		result, err := backend.Execute(context.Background(), webservice.Request{Action: "open", ServiceID: "ws_test"})
		if err != nil || !bytes.Equal(result, data) {
			t.Fatalf("non-grant receipt changed: %s %v", result, err)
		}
	}
	// Other actions never gain a link just because an opaque result contains a
	// field named preview_url.
	data := json.RawMessage(`{"preview_url":"https://preview.invalid/","generation":9007199254740993}`)
	backend := &webServiceOwnerLinks{Backend: fixedWebServiceLinkBackend{data}, agentID: "agent", sessionID: "session"}
	result, err := backend.Execute(context.Background(), webservice.Request{Action: "status", ServiceID: "ws_test"})
	if err != nil || !bytes.Equal(result, data) {
		t.Fatalf("non-open receipt changed: %s %v", result, err)
	}
}

func TestWebServiceNonStudioOpenDoesNotInventOwnerLink(t *testing.T) {
	managed := newWebServiceHostManager(t)
	h := newHarness(t, idleRespond, func(c *Config) { c.WebServices = managed })
	sid := h.createSession()
	sess := h.session(sid)
	if err := os.MkdirAll(filepath.Join(sess.cwd, "site"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sess.cwd, "site", "app.py"), []byte("print('fixture')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	backend := h.mgr.webServiceBackend(sess)
	ctx := context.Background()
	data, err := backend.Execute(ctx, webservice.Request{Action: "start", OperationID: "start", Spec: webservice.Spec{Name: "site", Profile: "python-web", ProjectRoot: "site", StartArgv: []string{"python", "app.py"}}})
	if err != nil {
		t.Fatal(err)
	}
	var record webservice.Record
	if err := json.Unmarshal(data, &record); err != nil || record.ServiceID == "" {
		t.Fatalf("invalid start receipt: %s %v", data, err)
	}
	id := record.ServiceID
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err = backend.Execute(ctx, webservice.Request{Action: "status", ServiceID: id})
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		if record.State == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("non-Studio service did not become ready: %s", data)
		}
		time.Sleep(time.Millisecond)
	}
	data, err = backend.Execute(ctx, webservice.Request{Action: "open", OperationID: "open", ServiceID: id})
	var grant webservice.Grant
	if err != nil || json.Unmarshal(data, &grant) != nil || grant.PreviewURL == "" || grant.ServiceURL != "" {
		t.Fatalf("non-Studio open invented a UI location: %s %v", data, err)
	}
}
