package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/memory"
)

func decodeManagedMemoryCatalog(t *testing.T, response *http.Response) managedMemoryCatalog {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("memory catalog status = %d (%s)", response.StatusCode, body)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("memory catalog may be cached")
	}
	var catalog managedMemoryCatalog
	if err := json.NewDecoder(response.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	return catalog
}

func managedMemoryScopeByID(t *testing.T, catalog managedMemoryCatalog, id string) managedMemoryScope {
	t.Helper()
	for _, scope := range catalog.Scopes {
		if scope.ID == id {
			return scope
		}
	}
	t.Fatalf("missing scope %q in %+v", id, catalog.Scopes)
	return managedMemoryScope{}
}

func TestMemoryManagementWithoutSessionAndScopeCRUD(t *testing.T) {
	userDir, projectRoot := realTempDir(t), realTempDir(t)
	t.Setenv(memory.DirEnv, userDir)
	store, diagnostics := memory.Load(memory.Options{User: true, Project: true, Cwd: projectRoot})
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	h := newHarness(t, scriptedTurns(textTurn("done")), func(cfg *Config) { cfg.Memory = store })
	a := createStudioAgent(t, h, "Research", "", "ready")
	response := h.do(http.MethodGet, "/api/memory/catalog", "")
	catalog := decodeManagedMemoryCatalog(t, response)
	if !catalog.AutoGenerateEditable || catalog.AutoGenerate {
		t.Fatalf("auto generation flags = %+v", catalog)
	}
	if len(h.mgr.sessions) != 0 {
		t.Fatal("memory catalog created a conversation")
	}
	for _, id := range []string{"user", "project", "agent:" + a.ID} {
		scope := managedMemoryScopeByID(t, catalog, id)
		if !scope.Writable || scope.Notes == nil || len(scope.Notes) != 0 {
			t.Fatalf("initial scope = %+v", scope)
		}
		response = h.do(http.MethodPut, "/api/memory/notes?scope_id="+url.QueryEscape(id), `{"path":"preference.md","type":"user","content":"Prefer concise answers."}`)
		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(response.Body)
			t.Fatalf("save %s: %d (%s)", id, response.StatusCode, body)
		}
		response.Body.Close()
		note, err := memory.ReadNote(scope.Directory, "preference.md")
		if err != nil || note.Content != "Prefer concise answers.\n" || note.Origin != "manual" || note.SourceSession != "" {
			t.Fatalf("saved note in %s = %+v / %v", id, note, err)
		}
	}
	agentScope := managedMemoryScopeByID(t, catalog, "agent:"+a.ID)
	if agentScope.Scope != memory.ScopeAgent || agentScope.AgentID != a.ID || agentScope.Label != "Research" {
		t.Fatalf("Agent scope = %+v", agentScope)
	}
	if got := managedMemoryScopeByID(t, catalog, "user").Directory; got != userDir {
		t.Fatalf("personal directory = %s", got)
	}
	if got := managedMemoryScopeByID(t, catalog, "project").Directory; got != memory.ProjectDir(projectRoot) {
		t.Fatalf("project directory = %s", got)
	}

	// A settings edit keeps the original chat provenance rather than inventing
	// a new chat ID or losing the source of an automatically created memory.
	_, err := memory.SaveNote(agentScope.Directory, memory.Note{Path: "source.md", Type: memory.NoteProject, Content: "Old fact", SourceSession: "original-chat", Origin: "auto-distill"}, true)
	if err != nil {
		t.Fatal(err)
	}
	response = h.do(http.MethodPut, "/api/memory/notes?scope_id="+url.QueryEscape(agentScope.ID), `{"path":"source.md","type":"project","content":"Corrected fact"}`)
	response.Body.Close()
	note, err := memory.ReadNote(agentScope.Directory, "source.md")
	if response.StatusCode != http.StatusOK || err != nil || note.SourceSession != "original-chat" || note.Origin != "manual" {
		t.Fatalf("edited provenance = %+v / %v", note, err)
	}
	response = h.do(http.MethodDelete, "/api/memory/notes?scope_id="+url.QueryEscape(agentScope.ID)+"&path=source.md", "")
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d", response.StatusCode)
	}
	if _, err := memory.ReadNote(agentScope.Directory, "source.md"); err == nil {
		t.Fatal("deleted memory still exists")
	}
}

func TestMemoryManagementRejectsClientRootsAndUnsafeNotes(t *testing.T) {
	userDir := realTempDir(t)
	t.Setenv(memory.DirEnv, userDir)
	store, _ := memory.Load(memory.Options{User: true})
	h := newHarness(t, scriptedTurns(textTurn("done")), func(cfg *Config) { cfg.Memory = store })
	for _, test := range []struct {
		query, body string
		status      int
	}{
		{"scope_id=../../outside", `{"path":"note.md","type":"project","content":"hello"}`, http.StatusNotFound},
		{"scope_id=agent:unknown", `{"path":"note.md","type":"project","content":"hello"}`, http.StatusNotFound},
		{"scope_id=user", `{"path":"../outside.md","type":"project","content":"hello"}`, http.StatusBadRequest},
		{"scope_id=user", `{"path":"note.md","type":"project","content":"api_key = \"abcdefghijklmnop\""}`, http.StatusBadRequest},
		{"scope_id=user", `{"path":"note.md","type":"project","content":"hello","directory":"/tmp/other"}`, http.StatusBadRequest},
		{"scope_id=user", `{"path":"note.md","type":"project","content":"hello"}{}`, http.StatusBadRequest},
	} {
		response := h.do(http.MethodPut, "/api/memory/notes?"+test.query, test.body)
		response.Body.Close()
		if response.StatusCode != test.status {
			t.Fatalf("%s body %s: status %d, want %d", test.query, test.body, response.StatusCode, test.status)
		}
	}
	if notes, err := memory.ListNotes(userDir); err != nil || len(notes) != 0 {
		t.Fatalf("invalid requests wrote notes: %+v / %v", notes, err)
	}
	response := h.do(http.MethodDelete, "/api/memory/notes?scope_id=user&path=../outside.md", "")
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("delete traversal = %d", response.StatusCode)
	}
}

func TestMemoryManagementOwnerIsolationAndSymlinkBoundary(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("done")))
	a, err := h.mgr.studio.put("", studioInput{Name: "Shared", Model: "k3", Status: "ready"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	aliceRoot := h.mgr.UserRoot(h.psrv.localIdentity("alice").Scope)
	bobRoot := h.mgr.UserRoot(h.psrv.localIdentity("bob").Scope)
	for root, fact := range map[string]string{aliceRoot: "ALICE_PRIVATE", bobRoot: "BOB_PRIVATE"} {
		for _, dir := range []string{memory.ProjectDir(root), memory.ProjectDir(filepath.Join(root, "agents", a.ID))} {
			if _, err := memory.SaveNote(dir, memory.Note{Path: "fact.md", Type: memory.NoteUser, Content: fact}, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	catalog := decodeManagedMemoryCatalog(t, h.doAs(aliceToken, http.MethodGet, "/api/memory/catalog", ""))
	if catalog.AutoGenerateEditable {
		t.Fatal("ordinary user can change the server-wide auto-generation policy")
	}
	if len(catalog.Scopes) != 2 {
		t.Fatalf("owner catalog included host or another owner's scopes: %+v", catalog.Scopes)
	}
	for _, scope := range catalog.Scopes {
		if !strings.HasPrefix(scope.Directory, aliceRoot+string(filepath.Separator)) || len(scope.Notes) != 1 || scope.Notes[0].Content != "ALICE_PRIVATE\n" {
			t.Fatalf("private catalog boundary = %+v", scope)
		}
	}
	response := h.doAs(aliceToken, http.MethodPut, "/api/memory/notes?scope_id=user", `{"path":"fact.md","type":"user","content":"ALICE_UPDATED"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("owner update = %d", response.StatusCode)
	}
	if note, err := memory.ReadNote(memory.ProjectDir(bobRoot), "fact.md"); err != nil || note.Content != "BOB_PRIVATE\n" {
		t.Fatalf("Alice overwrote Bob: %+v / %v", note, err)
	}

	// An Agent root symlink cannot turn an otherwise valid scope ID into a
	// route to another owner's notes.
	linked := filepath.Join(aliceRoot, "agents", "linked-agent")
	if err := os.Symlink(filepath.Join(bobRoot, "agents", a.ID), linked); err != nil {
		t.Fatal(err)
	}
	catalog = decodeManagedMemoryCatalog(t, h.doAs(aliceToken, http.MethodGet, "/api/memory/catalog", ""))
	for _, scope := range catalog.Scopes {
		if scope.ID == "agent:linked-agent" {
			t.Fatal("catalog followed an Agent symlink across owner roots")
		}
	}
	response = h.doAs(aliceToken, http.MethodDelete, "/api/memory/notes?scope_id=agent:linked-agent&path=fact.md", "")
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("symlink mutation = %d", response.StatusCode)
	}
	if note, err := memory.ReadNote(memory.ProjectDir(filepath.Join(bobRoot, "agents", a.ID)), "fact.md"); err != nil || note.Content != "BOB_PRIVATE\n" {
		t.Fatalf("symlink request affected Bob: %+v / %v", note, err)
	}
	// Also check a real, published Agent ID whose memory directory alone is
	// redirected. Filtering directory entries by IsDir is not sufficient here.
	aliceAgentMemory := memory.ProjectDir(filepath.Join(aliceRoot, "agents", a.ID))
	if err := os.RemoveAll(aliceAgentMemory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(memory.ProjectDir(filepath.Join(bobRoot, "agents", a.ID)), aliceAgentMemory); err != nil {
		t.Fatal(err)
	}
	catalog = decodeManagedMemoryCatalog(t, h.doAs(aliceToken, http.MethodGet, "/api/memory/catalog", ""))
	for _, scope := range catalog.Scopes {
		if scope.ID == "agent:"+a.ID {
			t.Fatal("catalog followed a published Agent memory symlink across owner roots")
		}
	}
	response = h.doAs(aliceToken, http.MethodPut, "/api/memory/notes?scope_id="+url.QueryEscape("agent:"+a.ID), `{"path":"fact.md","type":"user","content":"ESCAPED"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("published Agent memory symlink mutation = %d", response.StatusCode)
	}
	if note, err := memory.ReadNote(memory.ProjectDir(filepath.Join(bobRoot, "agents", a.ID)), "fact.md"); err != nil || note.Content != "BOB_PRIVATE\n" {
		t.Fatalf("published Agent memory symlink affected Bob: %+v / %v", note, err)
	}
}

func TestMemoryManagementDoesNotEnableDisabledPersonalMemory(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("done")))
	catalog := decodeManagedMemoryCatalog(t, h.do(http.MethodGet, "/api/memory/catalog", ""))
	if len(catalog.Scopes) != 0 {
		t.Fatalf("unconfigured scopes = %+v", catalog.Scopes)
	}
	response := h.do(http.MethodPut, "/api/memory/notes?scope_id=user", `{"path":"note.md","type":"user","content":"hello"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled personal memory save = %d", response.StatusCode)
	}
	if _, err := os.Stat(memory.ProjectDir(h.mgr.Cwd())); !os.IsNotExist(err) {
		t.Fatalf("catalog created a disabled memory directory: %v", err)
	}
}

func TestMemoryManagementVersionGuardsAgainstBackgroundUpdates(t *testing.T) {
	userDir := realTempDir(t)
	t.Setenv(memory.DirEnv, userDir)
	store, _ := memory.Load(memory.Options{User: true})
	h := newHarness(t, scriptedTurns(textTurn("done")), func(cfg *Config) { cfg.Memory = store })
	initial, err := memory.SaveNote(userDir, memory.Note{Path: "fact.md", Type: memory.NoteProject, Content: "Original", Modified: time.Date(2026, 11, 5, 1, 0, 0, 123456789, time.UTC)}, true)
	if err != nil {
		t.Fatal(err)
	}
	// The manager was opened before a background distillation updated the note.
	oldVersion := initial.Modified.UTC().Format(time.RFC3339Nano)
	unlock := memory.LockNoteRoot(userDir)
	background, err := memory.SaveNote(userDir, memory.Note{Path: "fact.md", Type: memory.NoteProject, Content: "New background fact", Modified: initial.Modified.Add(time.Nanosecond), Origin: "auto-distill"}, true)
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	response := h.do(http.MethodPut, "/api/memory/notes?scope_id=user", studioBody(map[string]any{"path": "fact.md", "type": "project", "content": "Stale editor text", "expected_modified": oldVersion}))
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("stale edit status = %d", response.StatusCode)
	}
	var conflict struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&conflict); err != nil || conflict.Error != "memory note changed" {
		t.Fatalf("conflict = %+v / %v", conflict, err)
	}
	response.Body.Close()
	response = h.do(http.MethodDelete, "/api/memory/notes?scope_id=user&path=fact.md&expected_modified="+url.QueryEscape(oldVersion), "")
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("stale delete status = %d", response.StatusCode)
	}
	if current, err := memory.ReadNote(userDir, "fact.md"); err != nil || current.Content != background.Content || !current.Modified.Equal(background.Modified) {
		t.Fatalf("stale operation changed background memory: %+v / %v", current, err)
	}
	// Equivalent RFC3339 offsets represent the same version. A fresh edit and
	// subsequent delete succeed when the caller really has the current note.
	versionWithOffset := background.Modified.In(time.FixedZone("UTC+8", 8*60*60)).Format(time.RFC3339Nano)
	response = h.do(http.MethodPut, "/api/memory/notes?scope_id=user", studioBody(map[string]any{"path": "fact.md", "type": "project", "content": "Reviewed correction", "expected_modified": versionWithOffset}))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("fresh edit status = %d", response.StatusCode)
	}
	var saved memory.Note
	if err := json.NewDecoder(response.Body).Decode(&saved); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	response = h.do(http.MethodDelete, "/api/memory/notes?scope_id=user&path=fact.md&expected_modified="+url.QueryEscape(saved.Modified.UTC().Format(time.RFC3339Nano)), "")
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("fresh delete status = %d", response.StatusCode)
	}
	// A deleted note must not be silently recreated by an editor holding an
	// earlier version, or reported as a successful stale deletion.
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		endpoint, body := "/api/memory/notes?scope_id=user", ""
		if method == http.MethodPut {
			body = studioBody(map[string]any{"path": "fact.md", "type": "project", "content": "Resurrected", "expected_modified": oldVersion})
		} else {
			endpoint += "&path=fact.md&expected_modified=" + url.QueryEscape(oldVersion)
		}
		response = h.do(method, endpoint, body)
		response.Body.Close()
		if response.StatusCode != http.StatusConflict {
			t.Fatalf("missing note %s with expected version = %d", method, response.StatusCode)
		}
	}
}

func TestMemoryManagementCreateOnlyAndInvalidVersions(t *testing.T) {
	userDir := realTempDir(t)
	t.Setenv(memory.DirEnv, userDir)
	store, _ := memory.Load(memory.Options{User: true})
	h := newHarness(t, scriptedTurns(textTurn("done")), func(cfg *Config) { cfg.Memory = store })
	create := `{"path":"new-note.md","type":"user","content":"First fact","create_only":true}`
	response := h.do(http.MethodPut, "/api/memory/notes?scope_id=user", create)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("initial create-only status = %d", response.StatusCode)
	}
	response = h.do(http.MethodPut, "/api/memory/notes?scope_id=user", `{"path":"new-note.md","type":"user","content":"Colliding fact","create_only":true}`)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("colliding create-only status = %d", response.StatusCode)
	}
	for _, version := range []string{"", "invalid-version"} {
		response = h.do(http.MethodPut, "/api/memory/notes?scope_id=user", studioBody(map[string]any{"path": "new-note.md", "type": "user", "content": "Invalid version fact", "expected_modified": version}))
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid edit version %q = %d", version, response.StatusCode)
		}
		response = h.do(http.MethodDelete, "/api/memory/notes?scope_id=user&path=new-note.md&expected_modified="+url.QueryEscape(version), "")
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid delete version %q = %d", version, response.StatusCode)
		}
	}
	if current, err := memory.ReadNote(userDir, "new-note.md"); err != nil || current.Content != "First fact\n" {
		t.Fatalf("collision or invalid version changed note: %+v / %v", current, err)
	}
}
