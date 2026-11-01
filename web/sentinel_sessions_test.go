package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/session"
)

func sentinelSessionWorker(t *testing.T, m *Manager, parent *Session, watch sentinel.Watch, marked bool) *Session {
	t.Helper()
	binding, ok := m.studio.binding(parent.ID)
	if !ok {
		t.Fatal("missing parent binding")
	}
	base := m.cfg.Cwd
	if parent.owner != nil {
		base = m.UserRoot(*parent.owner)
	}
	rel, err := filepath.Rel(base, parent.cwd)
	if err != nil {
		t.Fatal(err)
	}
	var marker *session.SentinelIdentity
	if marked {
		marker = &session.SentinelIdentity{WatchID: watch.Spec.ID, ParentSessionID: parent.ID, AgentID: watch.Spec.AgentID}
	}
	worker, err := m.createStudioBoundConversationKind(binding.Agent, binding.Owner, false, marker, rel)
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func sentinelSessionBindWorker(t *testing.T, m *Manager, watch sentinel.Watch, worker *Session) sentinel.Watch {
	t.Helper()
	db, err := m.sentinels.database(false)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.UpdateSession(watch.Spec.ID, worker.ID); err != nil {
		t.Fatal(err)
	}
	watch, err = db.Get(watch.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	return watch
}

func sentinelSessionAppend(t *testing.T, s *Session, messages ...llm.Message) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.AppendAll(messages); err != nil {
		t.Fatal(err)
	}
}

func sentinelSessionRegistration(t *testing.T, parent *Session, watch sentinel.Watch, name string) {
	t.Helper()
	args, result := `{"action":"create"}`, any(watch)
	if name == "background_task" {
		args = `{"action":"create","source":"sentinel"}`
		watch.Spec.OwnerID, watch.Spec.Owner = "", nil // native unified receipt hides owner data
		result = map[string]any{"source": "sentinel", "result": watch}
	}
	details, _ := json.Marshal(map[string]any{"type": name, "action": "create", "result": result})
	callID := "create-" + watch.Spec.ID
	sentinelSessionAppend(t, parent,
		llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: callID, Name: name, Input: json.RawMessage(args)}}},
		llm.Message{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: callID, Details: details, Text: "watch registered"}}},
	)
}

func sentinelSessionLegacy(t *testing.T, h *harness, parent *Session) sentinel.Watch {
	t.Helper()
	seed := sentinelTestCreate(t, h, parent)
	db, err := h.mgr.sentinels.database(false)
	if err != nil {
		t.Fatal(err)
	}
	spec := seed.Spec
	spec.ID, spec.ParentSessionID, spec.SessionID = "", "", ""
	w, err := db.Create(spec)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func sentinelSessionResponse(t *testing.T, h *harness, path string) map[string]json.RawMessage {
	t.Helper()
	response := h.do("GET", path, "")
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 {
		t.Fatalf("GET %s: %d %s", path, response.StatusCode, raw)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSentinelSessionHostLineageAndReadOnlyEmptyView(t *testing.T) {
	h, cap := newSentinelHarness(t, nil)
	parent := sentinelTestSession(t, h)
	w := sentinelTestCreate(t, h, parent)
	if w.Spec.ParentSessionID != parent.ID {
		t.Fatalf("host did not bind creation parent: %+v", w.Spec)
	}
	copy := w.Spec
	copy.ParentSessionID, copy.SessionID = "different-parent", "different-worker"
	if sentinelTaskHash(w.Spec) != sentinelTaskHash(copy) {
		t.Fatal("navigation changed a standing task's execution hash")
	}
	before, err := session.List(h.mgr.cfg.SessionDir)
	if err != nil {
		t.Fatal(err)
	}
	view := sentinelSessionResponse(t, h, "/api/tasks/sentinel/"+w.Spec.ID+"/session")
	if string(view["session"]) != "null" || !bytes.Contains(view["watch"], []byte(parent.ID)) {
		t.Fatalf("pre-event view must not fabricate a conversation: %s", view)
	}
	linked := sentinelSessionResponse(t, h, "/api/sessions/"+parent.ID+"/sentinels")
	var summaries []sentinelSessionSummary
	if err = json.Unmarshal(linked["watches"], &summaries); err != nil || len(summaries) != 1 || summaries[0].ID != w.Spec.ID || summaries[0].State != "paused" {
		t.Fatalf("parent projection: %+v %v", summaries, err)
	}
	after, err := session.List(h.mgr.cfg.SessionDir)
	if err != nil || !reflect.DeepEqual(before, after) || sentinelModelCalls(cap) != 0 {
		t.Fatal("read-only view created a conversation or woke the model", err)
	}
}

func TestSentinelSessionMarkerIsDurableBeforeWatchPointerCommit(t *testing.T) {
	h, _ := newSentinelHarness(t, nil)
	parent := sentinelTestSession(t, h)
	w := sentinelTestCreate(t, h, parent)
	worker := sentinelSessionWorker(t, h.mgr, parent, w, true)
	// Simulate restart before UpdateSession: only the first creation record
	// and Studio binding know the role, never an appended migration record.
	h.mgr.sentinelMarkers = sync.Map{}
	marker := h.mgr.sentinelSessionMarker(worker.ID)
	if marker == nil || marker.ParentSessionID != parent.ID || marker.WatchID != w.Spec.ID || worker.store.Meta().Runtime != studioWebRuntime {
		t.Fatalf("first-record role was not durable: %+v", marker)
	}
	list, err := h.mgr.List()
	if err != nil || slices.ContainsFunc(list, func(info session.Info) bool { return info.ID == worker.ID }) {
		t.Fatal("unpublished child appeared as ordinary chat", err)
	}
	view := sentinelSessionResponse(t, h, "/api/sessions/"+worker.ID)
	if !bytes.Contains(view["sentinel"], []byte(parent.ID)) {
		t.Fatalf("worker deep link lacks parent identity: %s", view)
	}
	h.post("/api/sessions/"+worker.ID+"/messages", `{"prompt":"ordinary input"}`, 409)
}

func TestSentinelSessionImportedRecordsCannotCarryNativeRoleOrCreationProof(t *testing.T) {
	marker := session.Record{Type: "meta", Meta: &session.Meta{Runtime: studioWebRuntime, Sentinel: &session.SentinelIdentity{WatchID: "watch", ParentSessionID: "parent", AgentID: "agent"}}}
	stripImportedResultGrants(&marker)
	if marker.Meta.Sentinel != nil {
		t.Fatal("import retained a native worker marker")
	}
	receipt := session.Record{Type: "message", Message: &llm.Message{Role: llm.RoleUser, WakeID: "host-wake", Content: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "create", Details: json.RawMessage(`{"type":"watch","action":"create","result":{"spec":{"id":"watch"}}}`), Text: "portable report"}}}}
	stripImportedResultGrants(&receipt)
	if receipt.Message.WakeID != "" || len(receipt.Message.Content[0].Details) != 0 || receipt.Message.Content[0].Text != "portable report" {
		t.Fatal("import kept trusted creation proof or lost the portable report")
	}
}

func TestSentinelSessionLegacyReceiptViewsNeverRewriteHistory(t *testing.T) {
	for _, name := range []string{"watch", "background_task"} {
		t.Run(name, func(t *testing.T) {
			h, cap := newSentinelHarness(t, nil)
			parent := sentinelTestSession(t, h)
			watch := sentinelSessionLegacy(t, h, parent)
			worker := sentinelSessionWorker(t, h.mgr, parent, watch, false)
			watch = sentinelSessionBindWorker(t, h.mgr, watch, worker)
			sentinelSessionRegistration(t, parent, watch, name)
			sentinelSessionAppend(t, worker, llm.Message{Role: llm.RoleUser, WakeID: "host-wake", Content: []llm.Block{{Type: llm.BlockText, Text: "legacy report matchingneedle"}}})
			beforeParent, _ := os.ReadFile(parent.store.Path())
			beforeWorker, _ := os.ReadFile(worker.store.Path())
			db, _ := h.mgr.sentinels.database(false)
			before, _ := db.Get(watch.Spec.ID)
			for i := 0; i < 2; i++ {
				if got := h.mgr.sentinelParentSession(watch); got != parent.ID {
					t.Fatalf("native %s creator not recovered: %q", name, got)
				}
				view := sentinelSessionResponse(t, h, "/api/tasks/sentinel/"+watch.Spec.ID+"/session")
				if !bytes.Contains(view["session"], []byte(worker.ID)) || !bytes.Contains(view["watch"], []byte(parent.ID)) {
					t.Fatalf("wrong retained session view: %s", view)
				}
				view = sentinelSessionResponse(t, h, "/api/sessions/"+worker.ID)
				if !bytes.Contains(view["sentinel"], []byte(watch.Spec.ID)) || !bytes.Contains(view["sentinel"], []byte(parent.ID)) {
					t.Fatalf("legacy direct navigation not classified: %s", view)
				}
			}
			after, _ := db.Get(watch.Spec.ID)
			afterParent, _ := os.ReadFile(parent.store.Path())
			afterWorker, _ := os.ReadFile(worker.store.Path())
			if !reflect.DeepEqual(before, after) || !bytes.Equal(beforeParent, afterParent) || !bytes.Equal(beforeWorker, afterWorker) || sentinelModelCalls(cap) != 0 {
				t.Fatal("legacy projection rewrote an existing definition/transcript or started a model")
			}
		})
	}
}

func TestSentinelSessionLegacyParentRequiresExactUnambiguousNativeReceipt(t *testing.T) {
	h, _ := newSentinelHarness(t, nil)
	parent := sentinelTestSession(t, h)
	w := sentinelSessionLegacy(t, h, parent)
	worker := sentinelSessionWorker(t, h.mgr, parent, w, false)
	w = sentinelSessionBindWorker(t, h.mgr, w, worker)
	sentinelSessionAppend(t, parent, llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "I created " + w.Spec.ID + " in this session"}}})
	if got := h.mgr.sentinelParentSession(w); got != "" {
		t.Fatalf("prose guessed a parent: %q", got)
	}
	wrong := w
	wrong.Spec.OwnerID = "wrong-owner"
	sentinelSessionRegistration(t, parent, wrong, "watch")
	if got := h.mgr.sentinelParentSession(w); got != "" {
		t.Fatalf("mismatched native owner guessed parent: %q", got)
	}
	sentinelSessionRegistration(t, parent, w, "watch")
	if got := h.mgr.sentinelParentSession(w); got != parent.ID {
		t.Fatalf("valid receipt not found after a previously cached absence: %q", got)
	}
	other := sentinelSessionWorker(t, h.mgr, parent, w, false) // ordinary chat, same Agent and workspace
	sentinelSessionRegistration(t, other, w, "watch")
	if got := h.mgr.sentinelParentSession(w); got != "" {
		t.Fatalf("ambiguous receipts arbitrarily selected parent %q", got)
	}
}

func TestSentinelSessionOrdinaryListsSearchAndRemovedHistory(t *testing.T) {
	h, _ := newSentinelHarness(t, nil)
	parent := sentinelTestSession(t, h)
	sentinelSessionAppend(t, parent, llm.Message{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockText, Text: "ordinary matchingneedle conversation"}}})
	if err := parent.store.AppendMeta(&session.Meta{Title: new("哨兵 matchingneedle 普通会话")}); err != nil {
		t.Fatal(err)
	}
	w := sentinelSessionLegacy(t, h, parent)
	worker := sentinelSessionWorker(t, h.mgr, parent, w, false)
	w = sentinelSessionBindWorker(t, h.mgr, w, worker)
	sentinelSessionRegistration(t, parent, w, "watch")
	sentinelSessionAppend(t, worker, llm.Message{Role: llm.RoleUser, WakeID: "host", Content: []llm.Block{{Type: llm.BlockText, Text: strings.Repeat("matchingneedle ", 20)}}})
	if err := worker.store.AppendMeta(&session.Meta{Title: new("matchingneedle")}); err != nil {
		t.Fatal(err)
	}
	db, _ := h.mgr.sentinels.database(false)
	if err := db.Remove(w.Spec.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/sessions", "/api/sessions/search?q=matchingneedle&limit=1", "/api/apps/" + parent.studioID + "/sessions"} {
		out := sentinelSessionResponse(t, h, path)
		raw, _ := json.Marshal(out)
		if bytes.Contains(raw, []byte(worker.ID)) || !bytes.Contains(raw, []byte(parent.ID)) {
			t.Fatalf("ordinary lookup leaked removed worker or lost ordinary result: %s %s", path, raw)
		}
	}
	view := sentinelSessionResponse(t, h, "/api/tasks/sentinel/"+w.Spec.ID+"/session")
	if !bytes.Contains(view["watch"], []byte(`"state":"removed"`)) || !bytes.Contains(view["session"], []byte(worker.ID)) {
		t.Fatalf("removed report history no longer readable: %s", view)
	}
}

func TestSentinelSessionHTTPMutationsBlockedButApprovalsRemain(t *testing.T) {
	h, cap := newSentinelHarness(t, nil)
	parent := sentinelTestSession(t, h)
	w := sentinelTestCreate(t, h, parent)
	worker := sentinelSessionWorker(t, h.mgr, parent, w, true)
	sentinelSessionBindWorker(t, h.mgr, w, worker)
	before, _ := os.ReadFile(worker.store.Path())
	h.post("/api/sessions/"+worker.ID+"/messages", `{"prompt":"do not mix this input"}`, 409)
	for _, action := range []string{"inbox_send", "inbox_steer", "inbox_edit", "inbox_withdraw", "steer", "rewind", "compact", "set_model", "set_policy"} {
		h.post("/api/sessions/"+worker.ID+"/control", studioBody(map[string]any{"action": action, "text": "mix"}), 409)
	}
	for _, method := range []string{"PATCH", "DELETE"} {
		response := h.do(method, "/api/sessions/"+worker.ID, `{"title":"hijack"}`)
		response.Body.Close()
		if response.StatusCode != 409 {
			t.Fatalf("%s managed worker: %d", method, response.StatusCode)
		}
	}
	after, _ := os.ReadFile(worker.store.Path())
	if !bytes.Equal(before, after) || sentinelModelCalls(cap) != 0 {
		t.Fatal("rejected input mutated worker context")
	}
	worker.SetPolicy(ModeStrict, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan agent.GateDecision, 1)
	go func() {
		done <- worker.Gate().Review(ctx, agent.GateRequest{CallID: "sentinel-approval", ToolName: "bash", Args: json.RawMessage(`{"command":"synthetic"}`)})
	}()
	gateID := ""
	for gateID == "" && ctx.Err() == nil {
		for _, gate := range worker.Hub().Snapshot().Live.PendingGates {
			gateID = gate.GateID
		}
		if gateID == "" {
			time.Sleep(time.Millisecond)
		}
	}
	if gateID == "" {
		t.Fatal("pending approval was not published")
	}
	h.post("/api/sessions/"+worker.ID+"/control", studioBody(map[string]any{"action": "gate_decide", "gate_id": gateID, "allow": true}), 200)
	if decision := <-done; !decision.Allow {
		t.Fatalf("managed worker approval blocked: %+v", decision)
	}
	h.post("/api/sessions/"+worker.ID+"/control", `{"action":"cancel"}`, 200)
}

func TestSentinelSessionDetailAndParentViewsAreOwnerBound(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { t.Error("read-only view called model"); return llm.Response{}, nil }, func(c *Config) {
		c.Cwd, _ = filepath.EvalSymlinks(c.Cwd)
		c.SessionDir, _ = filepath.EvalSymlinks(c.SessionDir)
	})
	alice := identity.LocalScope("alice")
	// Use the existing Studio create route to retain its real ownership rules.
	h.psrv.auth.mu.Lock()
	account := h.psrv.auth.users["alice"]
	account.Admin = true
	h.psrv.auth.users["alice"] = account
	h.psrv.auth.mu.Unlock()
	response := h.doAs(aliceToken, "POST", "/api/manage/agents", studioBody(map[string]any{"name": "Alice sentinel", "instructions": "observe", "model": "k3", "status": "ready"}))
	defer response.Body.Close()
	var created studioAgent
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil || response.StatusCode != 201 || created.ID == "" {
		t.Fatalf("create Agent: %d %+v %v", response.StatusCode, created, err)
	}
	parent, err := h.mgr.createStudioConversation(created, &alice)
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := json.Marshal(alice)
	db, err := h.mgr.sentinels.database(true)
	if err != nil {
		t.Fatal(err)
	}
	w, err := db.Create(sentinel.Spec{OwnerID: sessionResultOwnerKey(&alice), Owner: owner, AgentID: parent.studioID, ParentSessionID: parent.ID, Workspace: parent.cwd, ScriptDir: t.TempDir(), Command: []string{"/bin/false"}, Mode: "poll", IntervalSeconds: 60, TimeoutSeconds: 10, MaxTurns: 5, Title: "Alice condition", Task: "observe"})
	if err != nil {
		t.Fatal(err)
	}
	worker := sentinelSessionWorker(t, h.mgr, parent, w, true)
	sentinelSessionBindWorker(t, h.mgr, w, worker)
	for _, path := range []string{"/api/sessions/" + parent.ID + "/sentinels", "/api/tasks/sentinel/" + w.Spec.ID + "/session", "/api/sessions/" + worker.ID} {
		for token, want := range map[string]int{aliceToken: 200, bobToken: 404} {
			r := h.doAs(token, "GET", path, "")
			raw, _ := io.ReadAll(r.Body)
			r.Body.Close()
			if r.StatusCode != want {
				t.Fatalf("owner isolation %s: %d want %d %s", path, r.StatusCode, want, raw)
			}
		}
	}
	for _, token := range []string{aliceToken, bobToken} {
		if h.listIDs(token)[worker.ID] {
			t.Fatal("account list exposed managed worker")
		}
	}
}
