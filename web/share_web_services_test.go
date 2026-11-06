package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/webservice"
)

func TestSharedWebServiceSnapshotRedactsEscapedURLsWithoutLosingNumbers(t *testing.T) {
	url := "https://temporary.preview.test:19770/"
	redact := func(s string) string { return strings.ReplaceAll(s, url, "web-service:preview-redacted") }
	data := []byte(`{"m":[],"r":{"call":{"text":"https:\/\/temporary.preview.test:19770\/api","details":{"id":9007199254740993,"normal":"https://pogo.example.test:19770/"}}}}`)
	clean, err := redactSharedServiceSnapshot(data, redact)
	if err != nil || bytes.Contains(clean, []byte("temporary.preview.test")) || !bytes.Contains(clean, []byte("9007199254740993")) || !bytes.Contains(clean, []byte("https://pogo.example.test:19770/")) {
		t.Fatalf("unsafe or lossy share redaction: %s %v", clean, err)
	}
	original := []byte(`{"normal":"https://pogo.example.test:19770/", "id":9007199254740993}`)
	clean, err = redactSharedServiceSnapshot(original, redact)
	if err != nil || !bytes.Equal(original, clean) {
		t.Fatalf("unrelated snapshot changed: %s %v", clean, err)
	}
}

func TestShareRemovesManagedPreviewGrantFromQuestionAnswerAndToolHistory(t *testing.T) {
	managed := newWebServiceHostManager(t)
	h := newHarness(t, idleRespond, func(c *Config) { c.WebServices = managed })
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "site"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "site", "app.py"), []byte("print('fixture')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := session.Create(h.mgr.sessionDir(), workspace, "k3")
	if err != nil {
		t.Fatal(err)
	}
	sid := strings.TrimSuffix(filepath.Base(store.Path()), ".jsonl")
	scope := webservice.Scope{Owner: "single-user", Session: sid, Workspace: workspace}
	data, err := managed.Execute(context.Background(), scope, webservice.Request{Action: "start", OperationID: "share-start", Spec: webservice.Spec{Name: "share", ProjectRoot: "site", Profile: "python-web", StartArgv: []string{"python", "app.py"}}})
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		ID string `json:"service_id"`
	}
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, err := managed.Execute(context.Background(), scope, webservice.Request{Action: "status", ServiceID: receipt.ID})
		if err != nil {
			t.Fatal(err)
		}
		var v struct {
			State string `json:"state"`
		}
		_ = json.Unmarshal(status, &v)
		if v.State == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("service not ready: %s", status)
		}
		time.Sleep(time.Millisecond)
	}
	grant, err := managed.Execute(context.Background(), scope, webservice.Request{Action: "open", OperationID: "share-open", ServiceID: receipt.ID})
	if err != nil {
		t.Fatal(err)
	}
	var opened webservice.Grant
	if err := json.Unmarshal(grant, &opened); err != nil || opened.PreviewURL == "" {
		t.Fatalf("missing grant: %s %v", grant, err)
	}
	normal := "https://pogo.example.test:19770/?service_id=" + receipt.ID
	if err := store.AppendAll([]llm.Message{
		llm.UserText("Please review " + opened.PreviewURL),
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: "open-call", Name: "web_service", Input: json.RawMessage(`{"action":"open"}`)}}},
		{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "open-call", Text: string(grant)}}},
		llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "Website: " + opened.PreviewURL + " Owner link: " + normal}}},
	}); err != nil {
		t.Fatal(err)
	}
	qid := questionID(t, h, sid, "Please review")
	var created struct {
		ID string `json:"id"`
	}
	if err := decodeJSONBody(h.do(http.MethodPost, "/api/sessions/"+sid+"/share", `{"message_id":"`+qid+`"}`), &created); err != nil {
		t.Fatal(err)
	}
	response := h.get("/api/share/" + created.ID)
	defer response.Body.Close()
	var view shareEntry
	if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	packed, _ := json.Marshal(view)
	if bytes.Contains(packed, []byte("temporary.preview.test")) || !bytes.Contains(packed, []byte("web-service:preview-redacted")) || !bytes.Contains(packed, []byte(normal)) {
		t.Fatalf("public share leaked or damaged links: %s", packed)
	}
	ownerQuestion, ownerAnswer, found := h.session(sid).store.ExchangeSpan(1)
	if !found || !strings.Contains(ownerQuestion, opened.PreviewURL) || !strings.Contains(ownerAnswer, opened.PreviewURL) {
		t.Fatal("public-share redaction rewrote the private owner transcript")
	}
}

func TestShareRedactsIPPathGrantWhenRuntimeDisabled(t *testing.T) {
	h := newHarness(t, idleRespond)
	store, err := session.Create(h.mgr.sessionDir(), t.TempDir(), "k3")
	if err != nil {
		t.Fatal(err)
	}
	sid := strings.TrimSuffix(filepath.Base(store.Path()), ".jsonl")
	token := strings.Repeat("ab", 24)
	preview := "http://192.168.1.20:19770/_pogo/web/" + token + "/"
	owner := "http://192.168.1.20:19770/a/agent?c=session&web_service=ws_123"
	receipt := `{"preview_url":"http:\/\/192.168.1.20:19770\/_pogo\/web\/` + token + `\/","id":9007199254740993}`
	if err := store.AppendAll([]llm.Message{
		llm.UserText("Review " + preview),
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: "open-ip", Name: "web_service", Input: json.RawMessage(`{"action":"open","record":` + receipt + `}`)}}},
		{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "open-ip", Text: "Preview " + preview}}},
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "Website " + preview + " owner " + owner}}},
	}); err != nil {
		t.Fatal(err)
	}
	qid := questionID(t, h, sid, "Review ")
	var created struct {
		ID string `json:"id"`
	}
	if err := decodeJSONBody(h.do(http.MethodPost, "/api/sessions/"+sid+"/share", `{"message_id":"`+qid+`"}`), &created); err != nil {
		t.Fatal(err)
	}
	response := h.get("/api/share/" + created.ID)
	defer response.Body.Close()
	var view shareEntry
	if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	packed, _ := json.Marshal(view)
	if bytes.Contains(packed, []byte(token)) || strings.Contains(view.Title, "/_pogo/web/") || !strings.Contains(view.Answer, owner) || !bytes.Contains(packed, []byte("web-service:preview-redacted")) || !bytes.Contains(packed, []byte("9007199254740993")) {
		t.Fatalf("disabled-runtime path sharing leaked or changed data: %s", packed)
	}
	question, answer, ok := h.session(sid).store.ExchangeSpan(1)
	if !ok || !strings.Contains(question, preview) || !strings.Contains(answer, preview) {
		t.Fatal("sharing rewrote the owner's private transcript")
	}
}
