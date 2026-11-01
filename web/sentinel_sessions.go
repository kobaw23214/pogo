package web

// A hosted Sentinel keeps its own retained execution context. These helpers
// expose that context as a child record of the creating conversation, never as
// another ordinary chat, and never infer authority from a model-written title.
import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/session"
)

type sentinelSessionSummary struct {
	ID              string `json:"id"`
	TaskID          string `json:"task_id"`
	AgentID         string `json:"agent_id"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	Title           string `json:"title"`
	State           string `json:"state"`
	Enabled         bool   `json:"enabled"`
	ExpiresAt       int64  `json:"expires_at,omitempty"`
	PendingEvents   int    `json:"pending_events"`
	Busy            bool   `json:"busy"`
}

func (s *Server) registerSentinelSessionRoutes() {
	s.mux.HandleFunc("GET /api/sessions/{sid}/sentinels", s.handleSessionSentinels)
	s.mux.HandleFunc("GET /api/tasks/sentinel/{id}/session", s.handleSentinelSession)
}

func (m *Manager) sentinelSessionRecords() ([]sentinel.Watch, map[string]string, error) {
	if m.sentinels == nil {
		return nil, map[string]string{}, nil
	}
	db, err := m.sentinels.database(false)
	if err != nil || db == nil {
		return nil, map[string]string{}, err
	}
	rows, err := db.SessionRecords()
	if err != nil {
		return nil, nil, err
	}
	workers, err := db.WorkerSessionLinks()
	if err != nil {
		return nil, nil, err
	}
	for _, w := range rows {
		if validID(w.Spec.SessionID) {
			workers[w.Spec.SessionID] = w.Spec.ID
		}
	}
	return rows, workers, nil
}

// Creation metadata is bounded and read without adopting a session or loading
// its history. The marker is in the first fsynced record, before publication.
func (m *Manager) sentinelSessionMarker(id string) *session.SentinelIdentity {
	if !validID(id) {
		return nil
	}
	if cached, ok := m.sentinelMarkers.Load(id); ok {
		marker := cached.(sentinelMarkerCache).identity
		if marker == nil {
			return nil
		}
		copy := *marker
		return &copy
	}
	f, err := os.Open(m.transcriptPath(id))
	if err != nil {
		return nil
	}
	defer f.Close()
	var record session.Record
	if json.NewDecoder(io.LimitReader(f, 16<<20)).Decode(&record) != nil || record.Type != "meta" || record.Meta == nil {
		return nil
	}
	if record.Meta.Runtime != studioWebRuntime || record.Meta.Sentinel == nil {
		m.sentinelMarkers.Store(id, sentinelMarkerCache{cwd: record.Meta.Cwd, runtime: record.Meta.Runtime})
		return nil
	}
	identity := *record.Meta.Sentinel
	if !validID(identity.WatchID) || !validID(identity.AgentID) {
		return nil
	}
	if identity.ParentSessionID != "" && !validID(identity.ParentSessionID) {
		identity.ParentSessionID = ""
	}
	m.sentinelMarkers.Store(id, sentinelMarkerCache{identity: &identity, cwd: record.Meta.Cwd, runtime: record.Meta.Runtime})
	return &identity
}

type sentinelMarkerCache struct {
	identity     *session.SentinelIdentity
	cwd, runtime string
}
type sentinelParentCache struct{ fingerprint, parent string }

func (m *Manager) sentinelWorkerSet(infos []session.Info) (map[string]string, error) {
	_, workers, err := m.sentinelSessionRecords()
	if err != nil {
		// A Sentinel storage fault must not disable ordinary conversations.
		// Durable creation/wake markers still hide actual background records.
		workers = map[string]string{}
	}
	for _, info := range infos {
		if marker := m.sentinelSessionMarker(info.ID); marker != nil {
			workers[info.ID] = marker.WatchID
		} else if m.nativeSentinelWakeSession(info.ID) {
			if workers[info.ID] == "" {
				workers[info.ID] = "native-wake"
			}
		}
	}
	return workers, nil
}

// Legacy event conversations already carry a host-only WakeID on their first
// user input. This is a role marker, not a guessed task/parent identity. A
// title or assistant text never qualifies, and an imported unbound file cannot
// become an Agent conversation merely by containing this field.
func (m *Manager) nativeSentinelWakeSession(id string) bool {
	if cached, ok := m.sentinelWakeSessions.Load(id); ok {
		return cached.(bool)
	}
	binding, ok := m.studio.binding(id)
	if !ok || binding.runtime() != studioWebRuntime {
		return false
	}
	f, err := os.Open(m.transcriptPath(id))
	if err != nil {
		return false
	}
	defer f.Close()
	scan := bufio.NewScanner(io.LimitReader(f, 64<<20))
	scan.Buffer(make([]byte, 64<<10), 16<<20)
	for scan.Scan() {
		var record session.Record
		if json.Unmarshal(scan.Bytes(), &record) != nil || record.Message == nil || record.Message.Role != llm.RoleUser {
			continue
		}
		for _, block := range record.Message.Content {
			if block.Type != llm.BlockText {
				continue
			}
			worker := record.Message.WakeID != ""
			m.sentinelWakeSessions.Store(id, worker)
			return worker
		}
	}
	return false
}

func (m *Manager) sentinelIdentity(id string) (*session.SentinelIdentity, error) {
	marker := m.sentinelSessionMarker(id)
	rows, workers, err := m.sentinelSessionRecords()
	if err != nil {
		if marker != nil {
			return marker, nil
		}
		if m.nativeSentinelWakeSession(id) {
			binding, _ := m.studio.binding(id)
			return &session.SentinelIdentity{AgentID: binding.Agent.ID}, nil
		}
		return nil, nil
	}
	watchID := workers[id]
	if watchID == "" && marker != nil {
		watchID = marker.WatchID
	}
	for _, w := range rows {
		if w.Spec.ID != watchID {
			continue
		}
		binding, bound := m.studio.binding(id)
		if !bound || binding.Agent.ID != w.Spec.AgentID || sessionResultOwnerKey(binding.Owner) != w.Spec.OwnerID {
			return nil, errors.New("sentinel conversation binding mismatch")
		}
		meta, haveMeta := m.sentinelMarkers.Load(id)
		if !haveMeta || meta.(sentinelMarkerCache).cwd != w.Spec.Workspace || meta.(sentinelMarkerCache).runtime != binding.runtime() {
			return nil, errors.New("sentinel conversation workspace mismatch")
		}
		parent := m.sentinelParentSession(w)
		return &session.SentinelIdentity{WatchID: w.Spec.ID, ParentSessionID: parent, AgentID: w.Spec.AgentID}, nil
	}
	// An unpublished/removed-before-pointer-commit child stays a special role.
	// Its marker grants no task access; the task endpoint still checks a record.
	if marker == nil && m.nativeSentinelWakeSession(id) {
		binding, _ := m.studio.binding(id)
		return &session.SentinelIdentity{AgentID: binding.Agent.ID}, nil
	}
	return marker, nil
}

func (m *Manager) sentinelCreationParent(source *Session) string {
	identity, err := m.sentinelIdentity(source.ID)
	if err != nil {
		return ""
	}
	if identity != nil {
		return identity.ParentSessionID
	}
	return source.ID
}

func (m *Manager) sentinelParentSession(w sentinel.Watch) string {
	if m.validSentinelParent(w.Spec, w.Spec.ParentSessionID) {
		return w.Spec.ParentSessionID
	}
	if marker := m.sentinelSessionMarker(w.Spec.SessionID); marker != nil && marker.WatchID == w.Spec.ID && marker.AgentID == w.Spec.AgentID && m.validSentinelParent(w.Spec, marker.ParentSessionID) {
		return marker.ParentSessionID
	}
	// Legacy watches did not record their creator. Read only native registration
	// receipts in sessions bound to this same owner, Agent and workspace. Neither
	// a title nor an assistant's prose is evidence of a parent relationship.
	infos, err := session.List(m.cfg.SessionDir)
	if err != nil {
		return ""
	}
	_, workers, err := m.sentinelSessionRecords()
	if err != nil {
		return ""
	}
	candidates := []session.Info{}
	fingerprint := sha256.New()
	for _, info := range infos {
		if info.Cwd != w.Spec.Workspace || info.ID == w.Spec.SessionID || workers[info.ID] != "" || m.sentinelSessionMarker(info.ID) != nil {
			continue
		}
		binding, bound := m.studio.binding(info.ID)
		if !bound || binding.Agent.ID != w.Spec.AgentID || sessionResultOwnerKey(binding.Owner) != w.Spec.OwnerID {
			continue
		}
		stat, e := os.Stat(info.Path)
		if e != nil {
			continue
		}
		data, _ := json.Marshal([]any{info.ID, stat.Size(), stat.ModTime().UnixNano()})
		fingerprint.Write(data)
		candidates = append(candidates, info)
	}
	key := w.Spec.ID + "\x00" + w.Spec.OwnerID + "\x00" + w.Spec.AgentID + "\x00" + w.Spec.Workspace
	digest := hex.EncodeToString(fingerprint.Sum(nil))
	if cached, ok := m.sentinelParents.Load(key); ok {
		value := cached.(sentinelParentCache)
		if value.fingerprint == digest {
			return value.parent
		}
	}
	parent := ""
	for _, info := range candidates {
		if !m.nativeSentinelRegistration(info.ID, w.Spec) {
			continue
		}
		if parent != "" && parent != info.ID {
			parent = ""
			break
		} // ambiguous provenance
		parent = info.ID
	}
	m.sentinelParents.Store(key, sentinelParentCache{fingerprint: digest, parent: parent})
	return parent
}

func (m *Manager) validSentinelParent(spec sentinel.Spec, id string) bool {
	if !validID(id) || id == spec.SessionID {
		return false
	}
	binding, bound := m.studio.binding(id)
	if !bound {
		return true
	} // retained host lineage can outlive a deleted parent
	if binding.Agent.ID != spec.AgentID || sessionResultOwnerKey(binding.Owner) != spec.OwnerID {
		return false
	}
	_ = m.sentinelSessionMarker(id)
	value, ok := m.sentinelMarkers.Load(id)
	if !ok {
		return false
	}
	meta := value.(sentinelMarkerCache)
	return meta.cwd == spec.Workspace && meta.runtime == binding.runtime()
}

func (m *Manager) nativeSentinelRegistration(sid string, spec sentinel.Spec) bool {
	f, err := os.Open(m.transcriptPath(sid))
	if err != nil {
		return false
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.Size() > 64<<20 {
		return false
	}
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64<<10), 16<<20)
	calls := map[string]string{}
	matched := false
	for scan.Scan() {
		var record session.Record
		if json.Unmarshal(scan.Bytes(), &record) != nil || record.Message == nil {
			continue
		}
		message := record.Message
		for _, block := range message.Content {
			if message.Role == llm.RoleAssistant && block.Type == llm.BlockToolUse && (block.Name == "watch" || block.Name == "background_task") {
				var args struct{ Action, Source string }
				if json.Unmarshal(block.Input, &args) == nil && args.Action == "create" && (block.Name == "watch" || args.Source == "sentinel") {
					calls[block.ID] = block.Name
				}
			}
			name := calls[block.ToolUseID]
			if message.Role != llm.RoleUser || block.Type != llm.BlockToolResult || block.IsError || name == "" || len(block.Details) == 0 {
				continue
			}
			var detail struct {
				Type, Action string
				Result       json.RawMessage
			}
			if json.Unmarshal(block.Details, &detail) != nil || detail.Type != name || detail.Action != "create" {
				continue
			}
			var saved sentinel.Spec
			if name == "watch" {
				var value struct{ Spec sentinel.Spec }
				if json.Unmarshal(detail.Result, &value) != nil {
					continue
				}
				saved = value.Spec
			} else {
				var value struct {
					Source string
					Result struct{ Spec sentinel.Spec }
				}
				if json.Unmarshal(detail.Result, &value) != nil || value.Source != "sentinel" {
					continue
				}
				saved = value.Result.Spec
			}
			ownerMatches := saved.OwnerID == spec.OwnerID || name == "background_task" && saved.OwnerID == ""
			if saved.ID == spec.ID && ownerMatches && saved.AgentID == spec.AgentID && saved.Workspace == spec.Workspace {
				matched = true
			}
		}
	}
	return matched && scan.Err() == nil
}

func (s *Server) sentinelSessionSummary(w sentinel.Watch) sentinelSessionSummary {
	state := w.Status
	if state == "" {
		state = "listening"
	}
	if w.Status == "removed" {
		state = "removed"
	} else if w.Spec.Expired(time.Now().UnixMilli()) {
		state = "expired"
	} else if !w.Spec.Enabled {
		state = "paused"
	}
	return sentinelSessionSummary{ID: w.Spec.ID, TaskID: "sentinel:" + w.Spec.ID, AgentID: w.Spec.AgentID, ParentSessionID: s.mgr.sentinelParentSession(w), SessionID: w.Spec.SessionID, Title: w.Spec.Title, State: state, Enabled: w.Spec.Enabled && !w.Spec.Expired(time.Now().UnixMilli()) && state != "removed", ExpiresAt: w.Spec.ExpiresAt, PendingEvents: w.Pending, Busy: s.mgr.BusyIDs()[w.Spec.SessionID]}
}

func (s *Server) handleSessionSentinels(w http.ResponseWriter, r *http.Request) {
	parent, ok := s.session(w, r)
	if !ok {
		return
	}
	rows, _, err := s.mgr.sentinelSessionRecords()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	items := []sentinelSessionSummary{}
	for _, record := range rows {
		if record.Spec.AgentID != parent.studioID || record.Spec.OwnerID != sessionResultOwnerKey(parent.owner) || !s.taskCenterSentinelVisible(r, record.Spec) {
			continue
		}
		summary := s.sentinelSessionSummary(record)
		if summary.ParentSessionID == parent.ID && (summary.State != "removed" || summary.SessionID != "") {
			items = append(items, summary)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"watches": items})
}

func (s *Server) handleSentinelSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rows, _, err := s.mgr.sentinelSessionRecords()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	var found *sentinel.Watch
	for i := range rows {
		if rows[i].Spec.ID == id && s.taskCenterSentinelVisible(r, rows[i].Spec) {
			found = &rows[i]
			break
		}
	}
	if found == nil {
		writeErr(w, 404, ErrNotFound)
		return
	}
	summary := s.sentinelSessionSummary(*found)
	var view any
	if summary.SessionID != "" {
		worker, err := s.mgr.Get(summary.SessionID)
		if err != nil {
			writeErr(w, 404, errors.New("sentinel conversation is unavailable"))
			return
		}
		worker.mu.Lock()
		valid := worker.studioID == found.Spec.AgentID && sessionResultOwnerKey(worker.owner) == found.Spec.OwnerID && worker.cwd == found.Spec.Workspace
		cwd, model := worker.cwd, worker.cfg.Model
		worker.mu.Unlock()
		if !valid {
			writeErr(w, 404, ErrNotFound)
			return
		}
		view = map[string]any{"id": worker.ID, "cwd": cwd, "model": model, "snapshot": worker.Hub().Snapshot()}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"watch": summary, "session": view})
}

func (s *Server) rejectSentinelChat(w http.ResponseWriter, r *http.Request, sess *Session) bool {
	return s.rejectSentinelID(w, sess.ID)
}

func (s *Server) rejectSentinelID(w http.ResponseWriter, id string) bool {
	// Keep existing callers on one managed-execution boundary. Schedule results
	// have their own native role; they are never labeled as Sentinels.
	return s.rejectBackgroundTaskWrite(w, id)
}

// Keep the helper separate so tests and the ordinary list/search paths share
// exactly the same role exclusion rather than guessing from display titles.
func (m *Manager) ordinarySessionInfos(infos []session.Info) ([]session.Info, error) {
	workers, err := m.backgroundWorkerSet(infos)
	if err != nil {
		return nil, err
	}
	out := make([]session.Info, 0, len(infos))
	for _, info := range infos {
		if workers[info.ID] == "" && !(info.Created == 0 && info.Cwd == "" && info.Messages == 0 && info.Title == "") {
			out = append(out, info)
		}
	}
	return out, nil
}
