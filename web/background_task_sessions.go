package web

// Scheduled executions retain their real transcripts and ordinary Agent
// runtime, but are task records rather than user-created conversations.
import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/taskqueue"
)

type backgroundTaskMarkerCache struct {
	identity *session.BackgroundTaskIdentity
}

func validBackgroundTaskIdentity(identity *session.BackgroundTaskIdentity) bool {
	return identity != nil && identity.Source == "schedule" && schedule.JobNameShape.MatchString(identity.SourceID) &&
		identity.TaskID == "schedule:"+identity.SourceID && validID(identity.AgentID) && identity.OwnerID != "" &&
		(identity.RunID == "" || validID(identity.RunID))
}

func (m *Manager) backgroundTaskMarker(id string) *session.BackgroundTaskIdentity {
	if !validID(id) {
		return nil
	}
	if cached, ok := m.backgroundTaskMarkers.Load(id); ok {
		if identity := cached.(backgroundTaskMarkerCache).identity; identity != nil {
			copy := *identity
			return &copy
		}
		return nil
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
	var identity *session.BackgroundTaskIdentity
	if record.Meta.Runtime == studioWebRuntime && validBackgroundTaskIdentity(record.Meta.BackgroundTask) {
		copy := *record.Meta.BackgroundTask
		identity = &copy
	}
	m.backgroundTaskMarkers.Store(id, backgroundTaskMarkerCache{identity})
	return identity
}

// A retained admission/history link never borrows the current task definition's
// owner or a title. Both the immutable Agent binding and catalog must agree.
func (m *Manager) backgroundTaskSessionOwned(id, agentID, ownerID string) bool {
	if !validID(id) || !validID(agentID) || ownerID == "" || isBrowserAuthoringID(id) {
		return false
	}
	binding, ok := m.studio.binding(id)
	if !ok || binding.runtime() != studioWebRuntime || binding.Agent.ID != agentID || sessionResultOwnerKey(binding.Owner) != ownerID {
		return false
	}
	if !m.Isolation() {
		return ownerID == sessionResultOwnerKey(nil)
	}
	if m.cfg.Catalog == nil {
		return false
	}
	record, err := m.cfg.Catalog.OwnerOf(context.Background(), id)
	if err != nil || !record.Scope.Valid() || sessionResultOwnerKey(&record.Scope) != ownerID {
		return false
	}
	_, err = m.cfg.Catalog.Get(context.Background(), record.Scope, id)
	return err == nil
}

type scheduleSessionScope struct {
	agentID, ownerID string
	ambiguous        bool
}

// All queue rows and all retained per-job runs participate: a UI page limit or
// Job.SessionID (only the newest execution) cannot classify old task records.
func (m *Manager) scheduleSessionIdentities(infos []session.Info) (map[string]session.BackgroundTaskIdentity, error) {
	wanted := make(map[string]bool, len(infos))
	out := make(map[string]session.BackgroundTaskIdentity)
	if len(infos) == 0 {
		return out, nil
	}
	for _, info := range infos {
		wanted[info.ID] = true
		if marker := m.backgroundTaskMarker(info.ID); marker != nil {
			// The first host-issued record hides the result even in the window
			// before its binding/catalog/queue pointer has been committed. Direct
			// result access below separately requires the complete owned binding.
			out[info.ID] = *marker
		} else if cached, ok := m.backgroundTaskLegacy.Load(info.ID); ok {
			identity := cached.(session.BackgroundTaskIdentity)
			if m.backgroundTaskSessionOwned(info.ID, identity.AgentID, identity.OwnerID) {
				out[info.ID] = identity
			}
		}
	}
	scopes := map[string]scheduleSessionScope{}
	rememberScope := func(sourceID, agentID, ownerID string) {
		if previous, ok := scopes[sourceID]; ok && (previous.agentID != agentID || previous.ownerID != ownerID) {
			previous.ambiguous = true
			scopes[sourceID] = previous
			return
		}
		if _, ok := scopes[sourceID]; !ok {
			scopes[sourceID] = scheduleSessionScope{agentID: agentID, ownerID: ownerID}
		}
	}
	add := func(id string, identity session.BackgroundTaskIdentity) error {
		if !wanted[id] || !validBackgroundTaskIdentity(&identity) || !m.backgroundTaskSessionOwned(id, identity.AgentID, identity.OwnerID) {
			return nil
		}
		if previous, ok := out[id]; ok {
			if previous.SourceID != identity.SourceID || previous.AgentID != identity.AgentID || previous.OwnerID != identity.OwnerID {
				return errors.New("background conversation has conflicting task identities")
			}
			if previous.RunID != "" || identity.RunID == "" {
				return nil
			}
		}
		out[id] = identity
		m.backgroundTaskLegacy.Store(id, identity)
		return nil
	}
	if m.taskRuns != nil {
		db, err := m.taskRuns.database(false)
		if err == nil && db != nil {
			runs, err := db.List(taskqueue.Filter{}) // Limit=0 is explicitly all history.
			// A task-store fault must not take ordinary conversations offline.
			// First-record roles and previously confirmed owned links still hide
			// their records; unknown history is never cached or guessed.
			if err == nil {
				for _, run := range runs {
					if run.Source != "schedule" || !schedule.JobNameShape.MatchString(run.SourceID) || run.TaskID != "schedule:"+run.SourceID || !validID(run.AgentID) || run.WorkflowID != "" || run.OwnerID == "" {
						continue
					}
					rememberScope(run.SourceID, run.AgentID, run.OwnerID)
					identity := session.BackgroundTaskIdentity{Source: "schedule", TaskID: run.TaskID, SourceID: run.SourceID, AgentID: run.AgentID, OwnerID: run.OwnerID, RunID: run.ID}
					if err := add(run.SessionID, identity); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	store := schedule.NewStore(schedule.DefaultDir())
	jobs, err := store.Load()
	if err != nil {
		return out, nil
	}
	for _, job := range jobs {
		if !schedule.JobNameShape.MatchString(job.ID) || !validID(job.AgentID) || job.WorkflowID != "" || (job.Owner != nil && !job.Owner.Valid()) || (m.Isolation() && job.Owner == nil) {
			continue
		}
		ownerID := sessionResultOwnerKey(job.Owner)
		rememberScope(job.ID, job.AgentID, ownerID)
		if err := add(job.SessionID, session.BackgroundTaskIdentity{Source: "schedule", TaskID: "schedule:" + job.ID, SourceID: job.ID, AgentID: job.AgentID, OwnerID: ownerID}); err != nil {
			return nil, err
		}
	}
	for sourceID, scope := range scopes {
		if scope.ambiguous {
			continue // Never guess an owner/Agent for an old history-only link.
		}
		runs, err := store.Runs(sourceID, 0)
		if err != nil {
			continue
		}
		for _, run := range runs {
			if err := add(run.SessionID, session.BackgroundTaskIdentity{Source: "schedule", TaskID: "schedule:" + sourceID, SourceID: sourceID, AgentID: scope.agentID, OwnerID: scope.ownerID}); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func (m *Manager) scheduleIdentity(id string) (*session.BackgroundTaskIdentity, error) {
	if !validID(id) {
		return nil, nil
	}
	if marker := m.backgroundTaskMarker(id); marker != nil {
		if !m.backgroundTaskSessionOwned(id, marker.AgentID, marker.OwnerID) {
			return nil, errors.New("background conversation owner or Agent binding mismatch")
		}
		return marker, nil
	}
	if cached, ok := m.backgroundTaskLegacy.Load(id); ok {
		identity := cached.(session.BackgroundTaskIdentity)
		if m.backgroundTaskSessionOwned(id, identity.AgentID, identity.OwnerID) {
			return &identity, nil
		}
	}
	identities, err := m.scheduleSessionIdentities([]session.Info{{ID: id}})
	if err != nil {
		return nil, err
	}
	identity, ok := identities[id]
	if !ok {
		return nil, nil
	}
	if !m.backgroundTaskSessionOwned(id, identity.AgentID, identity.OwnerID) {
		return nil, errors.New("background conversation owner or Agent binding mismatch")
	}
	return &identity, nil
}

func (m *Manager) backgroundWorkerSet(infos []session.Info) (map[string]string, error) {
	if len(infos) == 0 {
		return map[string]string{}, nil
	}
	workers, err := m.sentinelWorkerSet(infos)
	if err != nil {
		return nil, err
	}
	identities, err := m.scheduleSessionIdentities(infos)
	if err != nil {
		return nil, err
	}
	for id, identity := range identities {
		workers[id] = identity.TaskID
	}
	for _, info := range infos {
		if marker := m.agentWorkflowMarker(info.ID); marker != nil {
			workers[info.ID] = "agent_workflow:" + marker.RunID
		}
	}
	return workers, nil
}

func (m *Manager) rejectBackgroundConversation(id string) error {
	if marker := m.agentWorkflowMarker(id); marker != nil {
		return errors.New("内部流程执行会话由流程控制，请在工作流面板处理确认与工具审批")
	}
	if identity, err := m.sentinelIdentity(id); err != nil {
		return err
	} else if identity != nil {
		return errors.New("哨兵会话是后台运行记录，请在主会话发送消息；审批仍可在此处理")
	}
	if identity, err := m.scheduleIdentity(id); err != nil {
		return err
	} else if identity != nil {
		return errors.New("定时任务会话是后台运行记录，请在普通会话发送消息；结果、审批与取消仍可在后台任务中处理")
	}
	return nil
}

func (s *Server) rejectBackgroundTaskWrite(w http.ResponseWriter, id string) bool {
	if err := s.mgr.rejectBackgroundConversation(id); err != nil {
		writeErr(w, http.StatusConflict, err)
		return true
	}
	return false
}
