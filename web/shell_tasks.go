package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/tools"
)

const maxShellTaskHistory = 64
const maxShellOutputPage = 64 << 10

// The directory is derived only from host ownership and conversation identity.
// A request may name a job, never a log path or a different owner's store.
func (m *Manager) shellJobStoreDir(id string, owner *identity.Scope) string {
	digest := sha256.Sum256([]byte(sessionResultScope(id, owner)))
	return filepath.Join(m.cfg.SessionDir, "shell-tasks", hex.EncodeToString(digest[:]))
}

func (s *Session) initShellJobs(ctx context.Context) {
	// Hosted executions settle one bounded Run or workflow step. A detached
	// command could otherwise keep mutating artifacts after that settlement.
	// These identities come from host-created session metadata, never titles.
	if meta := s.store.Meta(); meta != nil && (meta.AgentWorkflow != nil || meta.BackgroundTask != nil || meta.Sentinel != nil) {
		return
	}
	runner, err := tools.NewShellJobRunner(ctx, s.mgr.shellJobStoreDir(s.ID, s.owner), tools.ShellJobHooks{
		OnStart: s.onShellStart, OnOutput: s.onShellOutput, OnComplete: s.onShellComplete,
	}, tools.ShellJobOptions{Scheduler: s.mgr.shellScheduler, Owner: sessionResultOwnerKey(s.owner), Session: s.ID})
	if err != nil {
		s.hub.Publish(Event{Type: EvError, Error: "后台命令暂不可用，命令将继续以前台方式执行：" + err.Error()})
		return
	}
	s.shellJobs = runner
	// Construction can recover a previously running job as interrupted. Seed
	// before publishing this Session, but defer Agent wakeups until registration.
	s.hub.seedShellJobs(runner.List(false))
}

func (s *Session) onShellStart(record tools.ShellJobRecord) {
	s.hub.Publish(Event{Type: EvShellTaskStart, TaskID: record.TaskID, ShellTask: shellJobRecordPtr(record)})
}

func (s *Session) onShellOutput(record tools.ShellJobRecord) {
	s.hub.Publish(Event{Type: EvShellTaskOutput, TaskID: record.TaskID, ShellTask: shellJobRecordPtr(record)})
}

func (s *Session) onShellComplete(record tools.ShellJobRecord) {
	s.hub.Publish(Event{Type: EvShellTaskEnd, TaskID: record.TaskID, ShellTask: shellJobRecordPtr(record)})
	s.notifyShellJob(record)
}

func (s *Session) closeShellJobs() {
	if s.shellJobs != nil {
		if err := s.shellJobs.CloseContext(context.Background()); err != nil {
			s.hub.Publish(Event{Type: EvError, Error: "后台命令收尾超时，任务记录已保留以供恢复：" + err.Error()})
		}
	}
}

func cloneShellJobRecord(record tools.ShellJobRecord) tools.ShellJobRecord {
	if record.ExitCode != nil {
		code := *record.ExitCode
		record.ExitCode = &code
	}
	return record
}

func shellJobRecordPtr(record tools.ShellJobRecord) *tools.ShellJobRecord {
	copy := cloneShellJobRecord(record)
	return &copy
}

func (h *Hub) seedShellJobs(records []tools.ShellJobRecord) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, record := range records {
		if record.Background {
			h.foldShellTaskLocked(record)
		}
	}
}

func (h *Hub) foldShellTaskLocked(record tools.ShellJobRecord) {
	if record.TaskID == "" {
		return
	}
	if h.shellTasks == nil {
		h.shellTasks = map[string]tools.ShellJobRecord{}
	}
	if previous, ok := h.shellTasks[record.TaskID]; ok {
		// A final callback may overtake a coalesced output callback. Never let
		// that older snapshot resurrect a command that already reached a terminal state.
		if previous.Revision > record.Revision || (previous.Status != "running" && record.Status == "running") {
			return
		}
	}
	record = cloneShellJobRecord(record)
	if len(record.OutputTail) > maxPendingOutput {
		record.OutputTail = tailOf(record.OutputTail, maxPendingOutput)
		record.OutputTruncated = true
	}
	h.shellTasks[record.TaskID] = record
	for len(h.shellTasks) > maxShellTaskHistory {
		oldestID := ""
		var oldest int64
		for id, task := range h.shellTasks {
			if task.Status == "running" {
				continue
			}
			if oldestID == "" || task.StartedAt < oldest || (task.StartedAt == oldest && id < oldestID) {
				oldestID, oldest = id, task.StartedAt
			}
		}
		if oldestID == "" {
			break
		}
		delete(h.shellTasks, oldestID)
	}
}

func (s *Server) handleShellTasks(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	activeOnly := false
	if raw := r.URL.Query().Get("active_only"); raw != "" {
		var err error
		activeOnly, err = strconv.ParseBool(raw)
		if err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("active_only 必须为 true 或 false"))
			return
		}
	}
	tasks := []tools.ShellJobRecord{}
	if sess.shellJobs != nil {
		for _, record := range sess.shellJobs.List(activeOnly) {
			if record.Background {
				tasks = append(tasks, record)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) shellTask(w http.ResponseWriter, r *http.Request) (*Session, tools.ShellJobRecord, bool) {
	sess, ok := s.session(w, r)
	if !ok {
		return nil, tools.ShellJobRecord{}, false
	}
	if sess.shellJobs == nil {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return nil, tools.ShellJobRecord{}, false
	}
	record, err := sess.shellJobs.Get(r.PathValue("taskID"))
	if err != nil {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return nil, tools.ShellJobRecord{}, false
	}
	return sess, record, true
}

func (s *Server) handleShellTaskOutput(w http.ResponseWriter, r *http.Request) {
	sess, record, ok := s.shellTask(w, r)
	if !ok {
		return
	}
	var offset int64
	if raw := r.URL.Query().Get("offset"); raw != "" {
		var err error
		offset, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || offset < 0 {
			writeErr(w, http.StatusBadRequest, errors.New("offset 必须为非负整数"))
			return
		}
	}
	limit := maxShellOutputPage
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > maxShellOutputPage {
			writeErr(w, http.StatusBadRequest, errors.New("limit 必须为 1 到 65536"))
			return
		}
	}
	output, err := sess.shellJobs.Output(record.TaskID, offset, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, output)
}

func (s *Server) handleShellTaskStop(w http.ResponseWriter, r *http.Request) {
	sess, record, ok := s.shellTask(w, r)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeBody(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	if len(body.Reason) > 1000 {
		writeErr(w, http.StatusBadRequest, errors.New("停止原因过长"))
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		reason = "用户停止了后台命令"
	}
	if err := sess.shellJobs.StopContext(r.Context(), record.TaskID, reason); err != nil {
		status := http.StatusConflict
		if errors.Is(err, tools.ErrShellStopTimeout) {
			status = http.StatusRequestTimeout
		}
		writeErr(w, status, err)
		return
	}
	// The runner's record is authoritative: requesting a stop does not itself
	// prove the process group has finished shutting down.
	record, err := sess.shellJobs.Get(record.TaskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": record})
}
