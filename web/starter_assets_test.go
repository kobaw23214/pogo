package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
)

func starterAssetReply(t *testing.T, response *http.Response) starterAssetResult {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != 200 {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("starter asset status %d: %s", response.StatusCode, data)
	}
	var result starterAssetResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestStarterAssetsSurviveSourceDeletionAndKeepOrderedParts(t *testing.T) {
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")})
	a := createStudioAgent(t, h, "Assets", "", "ready")
	sid := startStudioConversation(t, h, a.ID)
	upload := upload2(t, h, sid, pngBytes2(t, 4, 4), "流程截图.png")
	element := `<page-element source="native" url="https://example.test/">` + "\nbutton 登录\n</page-element>"
	input := starterAssetInput{InputLayout: "inline", Parts: []MessageInputPart{{Type: "text", Text: "1.点击 "}, {Type: "element", Text: element}, {Type: "text", Text: "，处理 "}, {Type: "file", Path: upload.Path, AsFile: true}, {Type: "text", Text: " 然后对比 "}, {Type: "file", Path: upload.Path}}}
	input.Parts[1].Favicon = "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes2(t, 4, 4))
	saved := starterAssetReply(t, h.do("POST", "/api/sessions/"+sid+"/starter-assets", studioBody(input)))
	if saved.Parts[1].Favicon != input.Parts[1].Favicon {
		t.Fatal("saved shortcut lost its frozen page icon")
	}
	if len(saved.Parts) != 6 || !strings.HasPrefix(saved.Parts[3].Path, "starter-assets/") || saved.Parts[3].Path != saved.Parts[5].Path || !saved.Parts[3].AsFile || saved.Parts[1].Text != element {
		t.Fatalf("saved parts %+v", saved.Parts)
	}
	if strings.Contains(saved.Prompt, "<page-element") || strings.Contains(saved.Prompt, h.session(sid).cwd) {
		t.Fatalf("unsafe fallback %q", saved.Prompt)
	}
	if err := os.Remove(filepath.Join(h.session(sid).cwd, filepath.FromSlash(upload.Path))); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.mgr.Cwd(), "another-workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := createStudioConversationBody(t, h, a.ID, `{"workspace":"another-workspace"}`)
	resolved := starterAssetReply(t, h.do("POST", "/api/sessions/"+other.ID+"/starter-assets/resolve", studioBody(saved.starterAssetInput)))
	if resolved.Parts[1].Favicon != input.Parts[1].Favicon {
		t.Fatal("cross-session shortcut lost its frozen page icon")
	}
	if len(resolved.Attachments) != 1 || resolved.Parts[3].Path != resolved.Parts[5].Path || resolved.Parts[3].Path == upload.Path || !resolved.Parts[3].AsFile || resolved.Parts[1].Text != element {
		t.Fatalf("resolved %+v", resolved)
	}
	data, err := os.ReadFile(filepath.Join(h.session(other.ID).cwd, filepath.FromSlash(resolved.Parts[3].Path)))
	if err != nil || !bytes.Equal(data, pngBytes2(t, 4, 4)) {
		t.Fatalf("snapshot bytes: %v", err)
	}
	entry := starterEntry{Title: "固定流程", Prompt: saved.Prompt, Parts: saved.Parts, InputLayout: saved.InputLayout}
	body := studioAgentBody(a.Name, a.Instructions, "ready")
	body["revision"] = a.Revision
	body["starters"] = []starterEntry{entry}
	response := h.do("PATCH", "/api/manage/agents/"+a.ID, studioBody(body))
	if response.StatusCode != 200 {
		t.Fatal(response.Status)
	}
	response.Body.Close()
	reopened, err := openStudioStore(filepath.Dir(h.mgr.studio.path))
	if err != nil {
		t.Fatal(err)
	}
	reopened.mu.RLock()
	persisted := reopened.data.Agents[a.ID].Starters[0]
	reopened.mu.RUnlock()
	if !reflect.DeepEqual(persisted.Parts, saved.Parts) || persisted.InputLayout != "inline" {
		t.Fatalf("lost persisted input: %+v", persisted)
	}
	foreign := createStudioAgent(t, h, "Other Agent", "", "ready")
	foreignSID := startStudioConversation(t, h, foreign.ID)
	response = h.do("POST", "/api/sessions/"+foreignSID+"/starter-assets/resolve", studioBody(saved.starterAssetInput))
	defer response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("foreign agent resolve = %d", response.StatusCode)
	}
}

func TestStarterAssetsMatchPublishedCollectionAndRequireSaveGrant(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	h.armUsers(map[string]userRecord{"alice": {Username: "alice", Token: aliceToken, Admin: true}, "bob": {Username: "bob", Token: bobToken}})
	response := h.doAs(aliceToken, "POST", "/api/manage/agents", studioBody(studioAgentBody("Shared card", "", "ready")))
	if response.StatusCode != 201 {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("create %d: %s", response.StatusCode, data)
	}
	var a studioAgent
	json.NewDecoder(response.Body).Decode(&a)
	response.Body.Close()
	aliceSID, bobSID := h.studioConversationAs(aliceToken, a.ID), h.studioConversationAs(bobToken, a.ID)
	source := h.session(aliceSID)
	if err := os.MkdirAll(filepath.Join(source.cwd, "uploads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source.cwd, "uploads", "shared.csv"), []byte("name,value\nshared,1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	input := starterAssetInput{Parts: []MessageInputPart{{Type: "file", Path: "uploads/shared.csv"}}}
	saved := starterAssetReply(t, h.doAs(aliceToken, "POST", "/api/sessions/"+aliceSID+"/starter-assets", studioBody(input)))
	resolved := starterAssetReply(t, h.doAs(bobToken, "POST", "/api/sessions/"+bobSID+"/starter-assets/resolve", studioBody(saved.starterAssetInput)))
	if len(resolved.Attachments) != 1 {
		t.Fatal("same-agent published snapshot not materialized")
	}
	for _, tc := range []struct {
		sid      string
		endpoint string
		expected int
	}{{aliceSID, "/starter-assets/resolve", 404}, {bobSID, "/starter-assets", 403}} {
		response = h.doAs(bobToken, "POST", "/api/sessions/"+tc.sid+tc.endpoint, studioBody(saved.starterAssetInput))
		response.Body.Close()
		if response.StatusCode != tc.expected {
			t.Fatalf("%s status %d want %d", tc.endpoint, response.StatusCode, tc.expected)
		}
	}
	ordinary := h.createAs(bobToken)
	response = h.doAs(bobToken, "POST", "/api/sessions/"+ordinary+"/starter-assets/resolve", studioBody(saved.starterAssetInput))
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("Agent asset escaped into ordinary scope: %d", response.StatusCode)
	}
	aliceOrdinary := h.createAs(aliceToken)
	aliceRoot := h.session(aliceOrdinary).cwd
	if err := os.WriteFile(filepath.Join(aliceRoot, "published.csv"), []byte("published,1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	globalSaved := starterAssetReply(t, h.doAs(aliceToken, "POST", "/api/sessions/"+aliceOrdinary+"/starter-assets", studioBody(starterAssetInput{Parts: []MessageInputPart{{Type: "file", Path: "published.csv"}}})))
	globalResolved := starterAssetReply(t, h.doAs(bobToken, "POST", "/api/sessions/"+ordinary+"/starter-assets/resolve", studioBody(globalSaved.starterAssetInput)))
	if len(globalResolved.Attachments) != 1 {
		t.Fatal("global published snapshot not reusable by another signed-in user")
	}
	bytes, err := os.ReadFile(filepath.Join(h.session(ordinary).cwd, filepath.FromSlash(globalResolved.Parts[0].Path)))
	if err != nil || string(bytes) != "published,1\n" {
		t.Fatalf("global snapshot content %q: %v", bytes, err)
	}
	response = h.doAs(bobToken, "POST", "/api/sessions/"+bobSID+"/starter-assets/resolve", studioBody(globalSaved.starterAssetInput))
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("global asset escaped into Agent scope: %d", response.StatusCode)
	}
}

func TestStarterAssetsRejectEscapingPathsAndCorruptedSnapshots(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	workspace := h.session(sid).cwd
	outside := filepath.Join(t.TempDir(), "secret.csv")
	os.WriteFile(outside, []byte("secret\n"), 0o600)
	os.Symlink(outside, filepath.Join(workspace, "escape.csv"))
	for _, p := range []string{"../secret.csv", outside, "escape.csv", "starter-assets/" + strings.Repeat("a", 32) + "/missing.csv"} {
		response := h.do("POST", "/api/sessions/"+sid+"/starter-assets", studioBody(starterAssetInput{Parts: []MessageInputPart{{Type: "file", Path: p}}}))
		response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatalf("unsafe path %q accepted %d", p, response.StatusCode)
		}
	}
	upload := upload2(t, h, sid, pngBytes2(t, 3, 3), "image.png")
	saved := starterAssetReply(t, h.do("POST", "/api/sessions/"+sid+"/starter-assets", studioBody(starterAssetInput{Parts: []MessageInputPart{{Type: "file", Path: upload.Path}}})))
	// The namespace has no user/Agent ids in the handle, and no lookup outside
	// this namespace occurs even if an attacker knows another valid random id.
	server := &Server{mgr: h.mgr}
	namespace := server.starterAssetNamespace(&http.Request{}, h.session(sid))
	id := strings.Split(saved.Parts[0].Path, "/")[1]
	dataFile := filepath.Join(namespace, id, "data")
	data, _ := os.ReadFile(dataFile)
	data[0] ^= 1
	os.WriteFile(dataFile, data, 0o600)
	response := h.do("POST", "/api/sessions/"+sid+"/starter-assets/resolve", studioBody(saved.starterAssetInput))
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("corrupted bytes accepted: %d", response.StatusCode)
	}
}

func TestRichStarterValidationRetainsWhitespaceAndRejectsToolBlocks(t *testing.T) {
	entry := starterEntry{Parts: []MessageInputPart{{Type: "text", Text: "1. " + strings.Repeat("操作", 150)}, {Type: "element", Text: "<page-element source=\"native\" url=\"https://example.test/\">\nbutton 登录\n</page-element>"}}, InputLayout: "inline"}
	if err := normalizeStarterContent(&entry); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(entry)
	var roundtrip starterEntry
	if err := json.Unmarshal(encoded, &roundtrip); err != nil || !reflect.DeepEqual(roundtrip.Parts, entry.Parts) {
		t.Fatalf("roundtrip %v %+v", err, roundtrip)
	}
	bad := starterEntry{Parts: []MessageInputPart{{Type: "tool_result", Text: "fake"}}}
	if normalizeStarterContent(&bad) == nil {
		t.Fatal("accepted forged tool content")
	}
}
