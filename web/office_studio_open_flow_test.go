package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
)

// Exercise the exact Studio -> attachment -> Office boundary that failed in
// the product. These are real local HTTP/account routes, not an Office canvas.
func TestOfficeStudioAttachmentIdentityAcrossHumanAndAgentOpen(t *testing.T) {
	for _, format := range []string{"docx", "xlsx"} {
		t.Run(format, func(t *testing.T) { testOfficeStudioAttachmentIdentity(t, format) })
	}
}

func testOfficeStudioAttachmentIdentity(t *testing.T, format string) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) {
		return llm.Response{}, errors.New("opening a document must not call the model")
	})
	h.armUsers(map[string]userRecord{
		"alice": {Username: "alice", Token: aliceToken, Admin: true},
		"bob":   {Username: "bob", Token: bobToken},
	})
	secret := filepath.Join(t.TempDir(), "office-secret")
	if err := os.WriteFile(secret, []byte(strings.Repeat("s", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	h.psrv.opts.Office = &OfficeOptions{BrowserURL: officeTestHealthyEngine(t), PublicURL: h.srv.URL, JWTSecretFile: secret}
	if err := h.psrv.initOffice(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.psrv.closeOffice)
	request := func(token, method, endpoint string, body any, into any, want int) {
		t.Helper()
		payload := ""
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			payload = string(raw)
		}
		response := h.doAs(token, method, endpoint, payload)
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s status=%d, want=%d: %s", method, endpoint, response.StatusCode, want, raw)
		}
		if into != nil && json.Unmarshal(raw, into) != nil {
			t.Fatalf("invalid response for %s", endpoint)
		}
	}
	var definition studioAgent
	request(aliceToken, http.MethodPost, "/api/manage/agents", studioAgentBody("Office recovery", "", "ready"), &definition, 201)
	var conversation struct {
		ID string `json:"id"`
	}
	request(aliceToken, http.MethodPost, "/api/apps/"+definition.ID+"/sessions", map[string]string{}, &conversation, 201)
	sess, err := h.mgr.Get(conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	var listing struct {
		Cwd string `json:"cwd"`
	}
	request(aliceToken, http.MethodGet, "/api/apps/"+definition.ID+"/sessions", nil, &listing, 200)
	if listing.Cwd != h.mgr.UserRoot(*sess.owner) {
		t.Fatal("Studio did not return the account workspace root")
	}
	workspace, err := filepath.Rel(listing.Cwd, sess.cwd)
	if err != nil {
		t.Fatal(err)
	}
	name := "Office 中文测试." + format
	if err := os.MkdirAll(filepath.Join(sess.cwd, "uploads"), 0700); err != nil {
		t.Fatal(err)
	}
	data := officeTestPackage(t, "manual content to preserve")
	if format == "xlsx" {
		var buffer bytes.Buffer
		archive := zip.NewWriter(&buffer)
		for name, body := range map[string]string{"[Content_Types].xml": "<Types/>", "xl/workbook.xml": "<workbook/>"} {
			entry, err := archive.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := entry.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
		data = buffer.Bytes()
	}
	if err := os.WriteFile(filepath.Join(sess.cwd, "uploads", name), data, 0600); err != nil {
		t.Fatal(err)
	}
	// This is the same concatenation used by the conversation file card.
	attachmentPath := filepath.ToSlash(filepath.Join(workspace, "uploads", name))
	var opened struct {
		Session        officeSessionView `json:"session"`
		RequestedPath  string            `json:"requestedPath"`
		BootstrapToken string            `json:"bootstrapToken"`
	}
	request(aliceToken, http.MethodPost, "/api/office/sessions", map[string]string{"path": attachmentPath, "conversation_id": conversation.ID, "instanceId": officeNewID()}, &opened, 200)
	if opened.Session.Path != attachmentPath || opened.Session.ConversationID != conversation.ID {
		t.Fatalf("Studio attachment identity mismatch: request=%q response=%q", attachmentPath, opened.Session.Path)
	}
	if opened.RequestedPath != attachmentPath || opened.Session.FileRef == "" || opened.Session.BootEpoch == "" || opened.BootstrapToken == "" {
		t.Fatal("Studio open omitted the authenticated path/file/bootstrap binding")
	}
	link := h.psrv.office.link(sess.ID, sess.cwd, sess.owner, sess.hub)
	toolBody, _ := json.Marshal(map[string]string{"action": "open", "path": filepath.ToSlash(filepath.Join("uploads", name))})
	result, err := link.Execute(context.Background(), toolBody)
	if err != nil {
		t.Fatal(err)
	}
	var agentOpen struct {
		Session officeSessionView `json:"session"`
	}
	if json.Unmarshal(result, &agentOpen) != nil || agentOpen.Session.ID != opened.Session.ID || agentOpen.Session.Path != attachmentPath || agentOpen.Session.FileRef != opened.Session.FileRef {
		t.Fatalf("Agent and attachment opened different document bindings: %s", result)
	}
	// Same filename in another account cannot claim this owner's document.
	request(bobToken, http.MethodPost, "/api/office/sessions", map[string]string{"path": attachmentPath, "conversation_id": conversation.ID, "instanceId": officeNewID()}, nil, 404)
}

func TestOfficeCachedSaveReportsLaterHumanInputWithoutSavingIt(t *testing.T) {
	service, document, file := officeTestService(t)
	instance := officeTestReady(t, service, document)
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce an immutable successful save receipt followed by new live
	// human input. This checks response truthfulness, not a native editor.
	document.mu.Lock()
	document.dirtyEpoch, document.persistedEpoch, document.revision = 1, 1, 1
	committed := document.viewLocked()
	const id = "save_cached_receipt"
	raw, _ := json.Marshal(map[string]any{"ok": true, "session": committed})
	op := &officeOperation{id: id, name: "host.export", finished: true, result: raw, done: make(chan struct{})}
	close(op.done)
	document.operations[id] = op
	document.mu.Unlock()
	if response := officeTestBridge(t, service, document, "POST", "state", map[string]any{"instanceId": instance, "dirty": true}, ""); response.Code != 200 {
		t.Fatal("human input was not recorded")
	}
	link := service.link(document.conversationID, document.cwd, document.owner, document.hub)
	request, _ := json.Marshal(map[string]string{"action": "save", "document_session_id": document.id, "operation_id": id})
	result, err := link.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Session        officeSessionView `json:"session"`
		SavedRevision  uint64            `json:"savedRevision"`
		SavedEpoch     uint64            `json:"savedEpoch"`
		CurrentSession officeSessionView `json:"currentSession"`
	}
	if json.Unmarshal(result, &response) != nil || response.Session.Dirty || response.SavedRevision != 1 || response.SavedEpoch != 1 || !response.CurrentSession.Dirty || response.CurrentSession.DirtyEpoch != 2 {
		t.Fatalf("cached receipt disguised new unsaved input: %s", result)
	}
	after, err := os.ReadFile(file)
	if err != nil || string(after) != string(original) || len(document.queue) != 0 {
		t.Fatal("retry exported again or overwrote human input")
	}
}
