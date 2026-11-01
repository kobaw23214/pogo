package web

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/skills"
)

func TestNestedShortcutHTTPUsesActualChildrenOnceInOrderAndFreezesHistory(t *testing.T) {
	sk := writeTestSkill(t, t.TempDir(), "allowed", "allowed", "TRUSTED_SKILL_BODY")
	var received []llm.Message
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() }, func(c *Config) {
		c.Skills = []skills.Skill{sk}
		original := c.NewClient
		c.NewClient = func(cfg config.Resolved, retry func(llm.RetryInfo)) llm.Client {
			return &orderedCaptureClient{inner: original(cfg, retry), capture: func(ms []llm.Message) { received = ms }}
		}
	})
	sid := h.createSession()
	file := upload2(t, h, sid, []byte("a,b\n1,2"), "nested.csv").Path
	o := &inputref.OfficeSnapshot{Path: "uploads/book.xlsx", Title: "未保存选区", Editor: "cell", Sheet: "Data", Range: "A1:B2", Text: "CELL_SNAPSHOT", CanvasVersion: "hash-original", Dirty: true, DirtyEpoch: 2, SourceSession: sid}
	material := MessageInputPart{Type: "shortcut", ShortcutID: "root", Title: "资料合集", Text: "FALLBACK_MUST_NOT_BE_SENT", Parts: []MessageInputPart{{Type: "text", Text: "INNER_A /skill:missing ordinary data\n"}, {Type: "shortcut", ShortcutID: "child", Title: "子资料", Text: "NESTED_B"}, {Type: "element", Text: orderedTestElement}, {Type: "file", Path: file}, {Type: "skill", Name: "allowed"}, {Type: "office", Office: o}, {Type: "text", Text: "INNER_Z"}}}
	parts := []MessageInputPart{{Type: "text", Text: "OUTSIDE_FIRST "}, material, {Type: "text", Text: " OUTSIDE_LAST"}}
	stream := h.stream(sid, "")
	defer stream.close()
	stream.next(t)
	h.post("/api/sessions/"+sid+"/messages", studioBody(map[string]any{"parts": parts, "input_layout": "inline"}), 202)
	live := stream.wait(t, EvUserMessage)
	stream.wait(t, EvRunEnd)
	waitFor(t, 5*time.Second, func() bool { return !h.session(sid).Active() })
	check := func(bs []llm.Block) {
		t.Helper()
		if len(bs) != 10 || bs[0].Text != "OUTSIDE_FIRST " || bs[1].Text != "[资料：资料合集]\n" || bs[1].InputShortcut == nil || bs[1].InputShortcut.BlockCount != 7 || !reflect.DeepEqual(bs[1].InputShortcut.Parts, material.Parts) || bs[2].Text != material.Parts[0].Text || bs[3].Text != "[资料：子资料]\nNESTED_B" || !bs[4].InputElement || bs[5].FilePath != file || bs[6].InputSkill != "allowed" || !strings.Contains(bs[6].Text, "TRUSTED_SKILL_BODY") || bs[7].InputOffice == nil || bs[7].InputOffice.DirtyEpoch != 2 || !strings.Contains(bs[7].Text, "CELL_SNAPSHOT") || bs[8].Text != "INNER_Z" || bs[9].Text != " OUTSIDE_LAST" {
			t.Fatalf("structured model order or frozen metadata changed: %+v", bs)
		}
		var text strings.Builder
		for _, b := range bs {
			text.WriteString(b.Text)
		}
		if strings.Contains(text.String(), "FALLBACK_MUST_NOT_BE_SENT") || strings.Count(text.String(), "NESTED_B") != 1 || strings.Count(text.String(), "CELL_SNAPSHOT") != 1 || !strings.Contains(bs[7].Text, "引用时快照；版本 hash-original；包含未保存编辑") {
			t.Fatal("fallback or duplicate model body emitted")
		}
	}
	check(live.Content)
	if len(received) == 0 {
		t.Fatal("actual model client not reached")
	}
	check(received[0].Content)
	if title := session.UserInputTitle(received[0]); title != "OUTSIDE_FIRST 〔资料合集〕 OUTSIDE_LAST" {
		t.Fatalf("private child body leaked into title: %q", title)
	}
	store, err := session.Open(h.session(sid).Path())
	if err != nil {
		t.Fatal(err)
	}
	check(store.Messages("")[0].Content)
	h.mgr.evictAll()
	check(h.session(sid).Hub().Snapshot().Messages[0].Content)
}

func TestPrivateShortcutAssetsRecursiveDurableAndOwnerAgentScoped(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	h.armUsers(map[string]userRecord{"alice": {Username: "alice", Token: aliceToken, Admin: true}, "bob": {Username: "bob", Token: bobToken}})
	create := func(name string) studioAgent {
		res := h.doAs(aliceToken, "POST", "/api/manage/agents", studioBody(studioAgentBody(name, "", "ready")))
		defer res.Body.Close()
		var a studioAgent
		if res.StatusCode != 201 || json.NewDecoder(res.Body).Decode(&a) != nil {
			t.Fatal("agent create failed")
		}
		return a
	}
	a, b := create("Private A"), create("Private B")
	sid := h.studioConversationAs(aliceToken, a.ID)
	source := h.session(sid)
	if err := os.MkdirAll(filepath.Join(source.cwd, "uploads"), 0755); err != nil {
		t.Fatal(err)
	}
	data := []byte("OWNER_PRIVATE_BYTES\n")
	if err := os.WriteFile(filepath.Join(source.cwd, "uploads", "secret.csv"), data, 0600); err != nil {
		t.Fatal(err)
	}
	input := starterAssetInput{InputLayout: "inline", Parts: []MessageInputPart{{Type: "text", Text: "/compact is ordinary saved material data"}, {Type: "shortcut", ShortcutID: "child", Title: "Child", Parts: []MessageInputPart{{Type: "file", Path: "uploads/secret.csv", AsFile: true}, {Type: "text", Text: "then"}, {Type: "file", Path: "uploads/secret.csv"}}}}}
	saved := starterAssetReply(t, h.doAs(aliceToken, "POST", "/api/sessions/"+sid+"/shortcuts/assets", studioBody(input)))
	handle := saved.Parts[1].Parts[0].Path
	if !strings.HasPrefix(handle, "shortcut-assets/") || handle != saved.Parts[1].Parts[2].Path {
		t.Fatal("nested private attachment was not durably deduplicated")
	}
	resaved := starterAssetReply(t, h.doAs(aliceToken, "POST", "/api/sessions/"+sid+"/shortcuts/assets", studioBody(saved.starterAssetInput)))
	if !reflect.DeepEqual(saved.Parts, resaved.Parts) {
		t.Fatal("existing same-scope asset copied or changed")
	}
	entry := userShortcut{ID: "root", Title: "Private", Parts: saved.Parts}
	if _, err := h.mgr.replaceUserShortcuts(source.owner, a.ID, 0, []userShortcut{entry}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(source.cwd, "uploads", "secret.csv")); err != nil {
		t.Fatal(err)
	}
	h.mgr.evictAll()
	view, err := h.mgr.userShortcuts(source.owner, a.ID)
	if err != nil || !reflect.DeepEqual(view.Shortcuts[0].Parts, saved.Parts) {
		t.Fatal("frozen structured library did not reopen", err)
	}
	for _, target := range []struct {
		token, sid string
		want       int
	}{{aliceToken, h.studioConversationAs(aliceToken, a.ID), 200}, {bobToken, h.studioConversationAs(bobToken, a.ID), 400}, {aliceToken, h.studioConversationAs(aliceToken, b.ID), 400}, {bobToken, sid, 404}} {
		res := h.doAs(target.token, "POST", "/api/sessions/"+target.sid+"/shortcuts/resolve", studioBody(saved.starterAssetInput))
		if res.StatusCode != target.want {
			raw, _ := io.ReadAll(res.Body)
			res.Body.Close()
			t.Fatalf("scope status %d want %d: %s", res.StatusCode, target.want, raw)
		}
		if target.want != 200 {
			raw, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if bytes.Contains(raw, data) {
				t.Fatal("other scope error leaked file bytes")
			}
			continue
		}
		resolved := starterAssetReply(t, res)
		if len(resolved.Attachments) != 1 || !strings.HasPrefix(resolved.Parts[1].Parts[0].Path, "uploads/shortcuts/") || resolved.Parts[1].Parts[0].Path != resolved.Parts[1].Parts[2].Path {
			t.Fatal("recursive materialization order or deduplication lost")
		}
		got, err := os.ReadFile(filepath.Join(h.session(target.sid).cwd, filepath.FromSlash(resolved.Parts[1].Parts[0].Path)))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal("private bytes changed after source deletion/reopen", err)
		}
	}
	bobSID := h.studioConversationAs(bobToken, a.ID)
	bobOwner := h.session(bobSID).owner
	if _, err := h.mgr.replaceUserShortcuts(bobOwner, a.ID, 0, []userShortcut{entry}); err == nil {
		t.Fatal("library accepted a foreign owner's handle")
	}
	if _, err := h.mgr.replaceUserShortcuts(source.owner, b.ID, 0, []userShortcut{entry}); err == nil {
		t.Fatal("library accepted another Agent's handle")
	}
}

func TestNestedShortcutSkillPrecheckRejectsEntireMessageAndQueue(t *testing.T) {
	allowed := writeTestSkill(t, t.TempDir(), "allowed", "allowed", "ALLOWED_INSTRUCTIONS")
	other := writeTestSkill(t, t.TempDir(), "other", "other", "UNSELECTED_INSTRUCTIONS")
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) { c.Skills = []skills.Skill{allowed, other} })
	def := postStudioAgent(t, h, studioAgentCapsBody("restricted", "ready", map[string]any{"skills": []string{skillIDOf(allowed)}}), 201)
	sid := startStudioConversation(t, h, def.ID)
	file := upload2(t, h, sid, []byte("bytes"), "file.csv").Path
	for _, name := range []string{"missing", "other"} {
		parts := []MessageInputPart{{Type: "shortcut", ShortcutID: "root", Title: "Frozen", Parts: []MessageInputPart{{Type: "skill", Name: "allowed"}, {Type: "file", Path: file}, {Type: "skill", Name: name}}}}
		h.post("/api/sessions/"+sid+"/messages", studioBody(map[string]any{"parts": parts, "input_layout": "inline"}), 400)
		if _, err := h.session(sid).ReceiveParts("rejected-"+name, "", parts, "inline", "queued", ""); err == nil {
			t.Fatal("unselected nested Skill accepted by queue")
		}
	}
	if len(h.session(sid).Hub().Snapshot().Messages) != 0 || h.session(sid).Browser().Armed() {
		t.Fatal("rejected nested reference changed transcript or browser grant")
	}
}

func TestNestedShortcutQueueEditSteerPreservesSnapshotAndSelectedSkill(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	sk := writeTestSkill(t, t.TempDir(), "allowed", "allowed", "TRUSTED_STEERING_SKILL")
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			<-release
		}
		return plainTurn()
	}, func(c *Config) { c.Skills = []skills.Skill{sk} })
	sid := h.createSession()
	stream := h.start(sid, "working")
	defer stream.close()
	parts := []MessageInputPart{{Type: "shortcut", ShortcutID: "root", Title: "Root", Parts: []MessageInputPart{{Type: "text", Text: "original"}, {Type: "skill", Name: "allowed"}, {Type: "shortcut", ShortcutID: "child", Title: "Child", Text: "old child"}}}}
	item := inboxRequest(t, h, sid, map[string]any{"action": "inbox_send", "id": "nested-steer", "parts": parts, "input_layout": "inline", "mode": "queued"}, 200)
	var stored inboxFile
	raw, _ := os.ReadFile(inboxPath(h.session(sid).Path()))
	if json.Unmarshal(raw, &stored) != nil || !reflect.DeepEqual(stored.Items[0].Parts, parts) {
		t.Fatal("recursive queue snapshot not persisted")
	}
	parts[0].Parts[2].Text = "edited frozen child"
	item = inboxRequest(t, h, sid, map[string]any{"action": "inbox_edit", "id": item.ID, "revision": item.Revision, "parts": parts, "input_layout": "inline"}, 200)
	item = inboxRequest(t, h, sid, map[string]any{"action": "inbox_steer", "id": item.ID, "revision": item.Revision}, 200)
	if item.Status != "steering" {
		t.Fatal("selected nested Skill could not steer existing task")
	}
	once.Do(func() { close(release) })
	stream.wait(t, EvRunEnd)
	snap := waitInboxState(t, h.session(sid), func(sn Snapshot) bool { return len(sn.Inbox) == 1 && sn.Inbox[0].Status == "delivered" })
	found := false
	for _, m := range snap.Messages {
		if m.InboxID != item.ID {
			continue
		}
		found = true
		if !m.Steering || len(m.Content) != 4 || m.Content[0].InputShortcut == nil || m.Content[0].InputShortcut.BlockCount != 3 || m.Content[2].InputSkill != "allowed" || !strings.Contains(m.Content[2].Text, "TRUSTED_STEERING_SKILL") || m.Content[3].InputShortcut.Text != "edited frozen child" {
			t.Fatalf("steer order or snapshot lost: %+v", m)
		}
	}
	if !found {
		t.Fatal("recursive steering not delivered")
	}
}

func TestNestedStarterFilesStayPrivateWhenMaterialEmbedded(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	h.armUsers(map[string]userRecord{"alice": {Username: "alice", Token: aliceToken, Admin: true}, "bob": {Username: "bob", Token: bobToken}})
	res := h.doAs(aliceToken, "POST", "/api/manage/agents", studioBody(studioAgentBody("Published starter", "", "ready")))
	var a studioAgent
	json.NewDecoder(res.Body).Decode(&a)
	res.Body.Close()
	aliceSID, bobSID := h.studioConversationAs(aliceToken, a.ID), h.studioConversationAs(bobToken, a.ID)
	s := h.session(aliceSID)
	os.MkdirAll(filepath.Join(s.cwd, "uploads"), 0755)
	os.WriteFile(filepath.Join(s.cwd, "uploads", "private.csv"), []byte("private material bytes"), 0600)
	input := starterAssetInput{Parts: []MessageInputPart{{Type: "shortcut", ShortcutID: "private", Title: "Private", Parts: []MessageInputPart{{Type: "file", Path: "uploads/private.csv"}}}}}
	saved := starterAssetReply(t, h.doAs(aliceToken, "POST", "/api/sessions/"+aliceSID+"/starter-assets", studioBody(input)))
	if !strings.HasPrefix(saved.Parts[0].Parts[0].Path, "shortcut-assets/") {
		t.Fatal("nested material was published into global starter file namespace")
	}
	starterAssetReply(t, h.doAs(aliceToken, "POST", "/api/sessions/"+aliceSID+"/starter-assets/resolve", studioBody(saved.starterAssetInput)))
	res = h.doAs(bobToken, "POST", "/api/sessions/"+bobSID+"/starter-assets/resolve", studioBody(saved.starterAssetInput))
	defer res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatal("published starter granted another owner's private material file")
	}
}

func TestNestedInvalidTreeHTTPRejectsBeforePersistingOrCopying(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	s := h.session(sid)
	cycle := MessageInputPart{Type: "shortcut", ShortcutID: "root", Title: "Root", Parts: []MessageInputPart{{Type: "file", Path: "uploads/no-read.csv"}, {Type: "shortcut", ShortcutID: "root", Title: "Cycle", Text: "body"}}}
	deep := MessageInputPart{Type: "text", Text: "leaf"}
	for _, id := range []string{"f", "e", "d", "c", "b", "a"} {
		deep = MessageInputPart{Type: "shortcut", ShortcutID: id, Title: id, Parts: []MessageInputPart{deep}}
	}
	wide := MessageInputPart{Type: "shortcut", ShortcutID: "wide", Title: "Wide", Parts: make([]MessageInputPart, 512)}
	for i := range wide.Parts {
		wide.Parts[i] = MessageInputPart{Type: "text", Text: "x"}
	}
	large := MessageInputPart{Type: "shortcut", ShortcutID: "large", Title: "Large", Parts: []MessageInputPart{{Type: "text", Text: strings.Repeat("x", 256<<10)}}}
	for _, part := range []MessageInputPart{cycle, deep, wide, large} {
		body := studioBody(starterAssetInput{Parts: []MessageInputPart{part}, InputLayout: "inline"})
		h.post("/api/sessions/"+sid+"/messages", body, 400)
		h.post("/api/sessions/"+sid+"/shortcuts/assets", body, 400)
		h.post("/api/sessions/"+sid+"/shortcuts/resolve", body, 400)
		if _, err := h.mgr.replaceUserShortcuts(nil, "", 0, []userShortcut{{ID: "root", Title: "Root", Parts: []MessageInputPart{part}}}); err == nil {
			t.Fatal("invalid tree entered library")
		}
	}
	if len(s.Hub().Snapshot().Messages) != 0 {
		t.Fatal("invalid tree changed transcript")
	}
	assetDir, _ := h.mgr.shortcutAssetNamespace(nil, "")
	if _, err := os.Stat(assetDir); !os.IsNotExist(err) {
		t.Fatal("invalid structure touched persistent assets")
	}
	file, _ := shortcutPath(h.mgr.cfg.SessionDir, nil, "")
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("invalid structure changed saved library")
	}
}
