package web

// The settings memory manager works without creating a chat. Its scopes are
// resolved from authenticated identity and trusted configuration; a client can
// select a catalog ID, never a filesystem root or another owner's workspace.
import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yosukeno/pogo/memory"
	"github.com/yosukeno/pogo/tools"
)

type managedMemoryScope struct {
	ID        string        `json:"id"`
	Scope     string        `json:"scope"`
	AgentID   string        `json:"agent_id,omitempty"`
	Label     string        `json:"label,omitempty"`
	Directory string        `json:"directory"`
	Notes     []memory.Note `json:"notes"`
	Writable  bool          `json:"writable"`
}

type managedMemoryCatalog struct {
	Scopes               []managedMemoryScope `json:"scopes"`
	AutoGenerate         bool                 `json:"auto_generate"`
	AutoGenerateEditable bool                 `json:"auto_generate_editable"`
}

func (s *Server) memoryManagementRoutes() {
	s.mux.HandleFunc("GET /api/memory/catalog", s.handleMemoryCatalog)
	s.mux.HandleFunc("PUT /api/memory/notes", s.handleManagedMemoryNote)
	s.mux.HandleFunc("DELETE /api/memory/notes", s.handleManagedMemoryNote)
}

func (s *Server) managedMemoryScopes(w http.ResponseWriter, r *http.Request) ([]managedMemoryScope, bool) {
	w.Header().Set("Cache-Control", "no-store")
	owner, scoped := s.ownerScope(r)
	if s.multiuser() && (!scoped || !owner.Valid()) {
		writeErr(w, http.StatusUnauthorized, errors.New("authentication is required to manage memory"))
		return nil, false
	}
	base := s.mgr.Cwd()
	if scoped {
		base = s.mgr.UserRoot(owner)
	}
	result := []managedMemoryScope{}
	seen := map[string]bool{}
	add := func(id, kind, aid, label, root string) {
		if seen[root] {
			return
		}
		seen[root] = true
		result = append(result, managedMemoryScope{ID: id, Scope: kind, AgentID: aid, Label: label, Directory: root, Notes: []memory.Note{}, Writable: true})
	}
	if scoped {
		// Owned sessions use this root instead of the host's configured memory.
		// Resolve also rejects a memory-directory symlink into another user.
		if root, err := tools.Resolve(base, memory.ProjectDir(base)); err == nil {
			add("user", memory.ScopeUser, "", "", root)
		}
	} else if store := s.mgr.cfg.Memory; store != nil {
		userDir, _ := memory.UserDir()
		for _, root := range store.Roots() {
			kind := memory.ScopeProject
			if memorySamePath(root, userDir) {
				kind = memory.ScopeUser
			}
			id := kind
			for _, previous := range result {
				if previous.ID == id {
					hash := sha256.Sum256([]byte(root))
					id += ":" + hex.EncodeToString(hash[:8])
					break
				}
			}
			add(id, kind, "", "", root)
		}
	}

	// Ready definitions are available to ordinary users before their first chat.
	// Other definitions are operator-visible, but old directories under this
	// owner's root remain manageable even after a definition is unpublished or
	// deleted. Such directories reveal only the owner's own previously saved data.
	agents := map[string]string{}
	for _, definition := range s.mgr.studio.list(s.adminRequest(r)) {
		if validAgentDirName(definition.ID) {
			agents[definition.ID] = definition.Name
		}
	}
	if root, err := tools.Resolve(base, "agents"); err == nil {
		if entries, err := os.ReadDir(root); err == nil {
			for _, entry := range entries {
				if entry.IsDir() && validAgentDirName(entry.Name()) {
					if _, known := agents[entry.Name()]; !known {
						agents[entry.Name()] = entry.Name()
					}
				}
			}
		}
	}
	ids := make([]string, 0, len(agents))
	for id := range agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		root, err := tools.Resolve(base, memory.ProjectDir(filepath.Join(base, "agents", id)))
		if err == nil {
			add("agent:"+id, memory.ScopeAgent, id, agents[id], root)
		}
	}
	return result, true
}

func memorySamePath(a, b string) bool {
	if b == "" {
		return false
	}
	a, _ = filepath.Abs(a)
	b, _ = filepath.Abs(b)
	return filepath.Clean(a) == filepath.Clean(b)
}

func (s *Server) handleMemoryCatalog(w http.ResponseWriter, r *http.Request) {
	scopes, ok := s.managedMemoryScopes(w, r)
	if !ok {
		return
	}
	for i := range scopes {
		unlock := memory.LockNoteRoot(scopes[i].Directory)
		notes, err := memory.ListNotes(scopes[i].Directory)
		unlock()
		if err != nil {
			writeMemoryError(w, err)
			return
		}
		sort.SliceStable(notes, func(i, j int) bool {
			if !notes[i].Modified.Equal(notes[j].Modified) {
				return notes[i].Modified.After(notes[j].Modified)
			}
			return notes[i].Path < notes[j].Path
		})
		scopes[i].Notes = notes
	}
	writeJSON(w, http.StatusOK, managedMemoryCatalog{Scopes: scopes, AutoGenerate: s.mgr.MemoryAutoEnabled(), AutoGenerateEditable: s.adminRequest(r)})
}

func (s *Server) handleManagedMemoryNote(w http.ResponseWriter, r *http.Request) {
	scopes, ok := s.managedMemoryScopes(w, r)
	if !ok {
		return
	}
	var root string
	for _, candidate := range scopes {
		if candidate.ID == r.URL.Query().Get("scope_id") && candidate.Writable {
			root = candidate.Directory
			break
		}
	}
	if root == "" {
		writeErr(w, http.StatusNotFound, errors.New("memory scope not found"))
		return
	}
	unlock := memory.LockNoteRoot(root)
	defer unlock()
	switch r.Method {
	case http.MethodPut:
		var input struct {
			Path             string          `json:"path"`
			Type             memory.NoteType `json:"type"`
			Content          string          `json:"content"`
			ExpectedModified *string         `json:"expected_modified,omitempty"`
			CreateOnly       bool            `json:"create_only,omitempty"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, memory.MaxManagedNoteBytes+16<<10))
		decoder.DisallowUnknownFields()
		var extra any
		if decoder.Decode(&input) != nil || decoder.Decode(&extra) != io.EOF {
			writeErr(w, http.StatusBadRequest, errors.New("invalid memory note request"))
			return
		}
		path := strings.TrimSpace(input.Path)
		if !managedMemoryVersionMatches(w, root, path, input.ExpectedModified, input.CreateOnly) {
			return
		}
		note, err := memory.SaveNote(root, memory.Note{Path: path, Type: input.Type, Content: input.Content, Origin: "manual"}, true)
		if err != nil {
			writeMemoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, note)
	case http.MethodDelete:
		query := r.URL.Query()
		var expected *string
		if query.Has("expected_modified") {
			value := query.Get("expected_modified")
			expected = &value
		}
		if !managedMemoryVersionMatches(w, root, query.Get("path"), expected, false) {
			return
		}
		if err := memory.DeleteNote(root, query.Get("path")); err != nil {
			writeMemoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// Called with the root lock held, so another managed or automatic write cannot
// replace a note between its version check and mutation. Optional preconditions
// preserve compatibility for existing callers of this new management API.
func managedMemoryVersionMatches(w http.ResponseWriter, root, path string, expected *string, createOnly bool) bool {
	if expected == nil && !createOnly {
		return true
	}
	var expectedTime time.Time
	if expected != nil {
		var err error
		expectedTime, err = time.Parse(time.RFC3339Nano, *expected)
		if err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("invalid memory note version"))
			return false
		}
	}
	current, err := memory.ReadNote(root, path)
	if err != nil && !errors.Is(err, memory.ErrNoteNotFound) {
		writeMemoryError(w, err)
		return false
	}
	exists := err == nil
	if createOnly && exists || expected != nil && (!exists || current.Modified.UTC().Format(time.RFC3339Nano) != expectedTime.UTC().Format(time.RFC3339Nano)) {
		writeErr(w, http.StatusConflict, errors.New("memory note changed"))
		return false
	}
	return true
}
