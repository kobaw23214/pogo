package web

// Activity is an ephemeral, read-only projection of an internal workflow Run.
// One connection covers every parallel node; model text and tool arguments never
// enter this contract or a new history store.
import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/taskqueue"
)

const awActivityInterval = 500 * time.Millisecond

type awNodeActivity struct {
	StepID           string `json:"step_id"`
	SessionID        string `json:"session_id,omitempty"`
	ActiveRunID      string `json:"active_run_id,omitempty"`
	Status           string `json:"status"`
	Phase            string `json:"phase"`
	Observed         bool   `json:"observed"`
	Turn             int    `json:"turn,omitempty"`
	ToolName         string `json:"tool_name,omitempty"`
	ToolCount        *int   `json:"tool_count,omitempty"`
	PendingToolCount int    `json:"pending_tool_count,omitempty"`
	ApprovalCount    int    `json:"approval_count,omitempty"`
	UpdatedAt        int64  `json:"updated_at"`
}

type awRunActivity struct {
	AgentID    string           `json:"agent_id"`
	WorkflowID string           `json:"workflow_id"`
	RunID      string           `json:"run_id"`
	Status     string           `json:"status"`
	UpdatedAt  int64            `json:"updated_at"`
	Nodes      []awNodeActivity `json:"nodes"`
}

type awActivitySample struct {
	node awNodeActivity
	seq  int64
}

func (s *Server) registerAgentWorkflowActivityRoutes() {
	s.mux.HandleFunc("GET /api/apps/{aid}/agent-workflows/{wid}/runs/{rid}/activity", s.handleAgentWorkflowActivity)
}

// Revalidate on every tick: a long-lived response must not retain an account or
// session credential that was revoked after the initial HTTP admission.
func (s *Server) agentWorkflowActivityRun(r *http.Request) (af.Run, error) {
	if r.Context().Err() != nil {
		return af.Run{}, r.Context().Err()
	}
	if active, _ := s.mgr.releaseMaintenance(); active {
		return af.Run{}, &awHTTPError{http.StatusServiceUnavailable, "服务维护中，请稍后查看运行活动"}
	}
	owner, ok := s.taskCenterOwner(r)
	aid, wid, rid := r.PathValue("aid"), r.PathValue("wid"), r.PathValue("rid")
	if !ok || !validID(aid) || !validID(wid) || !validID(rid) {
		return af.Run{}, ErrNotFound
	}
	if s.opts.Token != "" || s.multiuser() {
		fresh, authenticated := s.authenticate(r)
		if !authenticated || owner != nil && fresh.Scope != *owner {
			return af.Run{}, &awHTTPError{http.StatusUnauthorized, "会话授权已失效"}
		}
	}
	s.mgr.mu.Lock()
	valid, closed := s.mgr.scheduleOwnerValid, s.mgr.closed
	s.mgr.mu.Unlock()
	if closed || valid == nil || !valid(owner) {
		return af.Run{}, awDenied("当前账号不可用")
	}
	db, err := s.mgr.taskRuns.database(false)
	if err != nil {
		return af.Run{}, err
	}
	if db == nil {
		return af.Run{}, ErrNotFound
	}
	key := sessionResultOwnerKey(owner)
	run, err := db.AWRun(key, aid, wid, rid)
	if err == nil && (run.OwnerID != key || run.AgentID != aid || run.WorkflowID != wid || run.ID != rid) {
		return af.Run{}, ErrNotFound
	}
	return run, err
}

func awActivityStatus(err error) int {
	var denied *awHTTPError
	if errors.As(err, &denied) {
		return denied.status
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, taskqueue.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func awActivityTerminal(status string) bool {
	return status == af.Completed || status == af.Failed || status == af.Cancelled || status == af.Interrupted
}

func (s *Server) handleAgentWorkflowActivity(w http.ResponseWriter, r *http.Request) {
	run, err := s.agentWorkflowActivityRun(r)
	if err != nil {
		writeErr(w, awActivityStatus(err), err)
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		writeErr(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	write := func(kind string, value any) bool {
		raw, e := json.Marshal(value)
		if e != nil {
			return false
		}
		// A stalled reader must not pin a handler forever. Every reconnect gets
		// a fresh authoritative snapshot, not a partial token-event replay.
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, e = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw); e != nil {
			return false
		}
		return controller.Flush() == nil
	}
	cache := make(map[string]awActivitySample)
	var previous []byte
	lastWrite := time.Now()
	ticker := time.NewTicker(awActivityInterval)
	defer ticker.Stop()
	for {
		activity := s.agentWorkflowActivity(run, cache, time.Now().UnixMilli())
		raw, _ := json.Marshal(activity)
		if !bytes.Equal(raw, previous) {
			if !write("snapshot", activity) {
				return
			}
			previous, lastWrite = raw, time.Now()
		} else if time.Since(lastWrite) >= 10*time.Second {
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil || controller.Flush() != nil {
				return
			}
			lastWrite = time.Now()
		}
		if awActivityTerminal(run.Status) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
		run, err = s.agentWorkflowActivityRun(r)
		if err != nil {
			if r.Context().Err() == nil {
				write("error", map[string]any{"error": "运行活动暂不可用，请重新连接", "code": awActivityStatus(err)})
			}
			return
		}
	}
}

func (s *Server) agentWorkflowActivity(run af.Run, cache map[string]awActivitySample, now int64) awRunActivity {
	out := awRunActivity{AgentID: run.AgentID, WorkflowID: run.WorkflowID, RunID: run.ID, Status: run.Status, UpdatedAt: run.UpdatedAt, Nodes: make([]awNodeActivity, 0, len(run.Steps))}
	active := 0
	for _, step := range run.Steps {
		codeNode := false
		for _, definition := range run.Definition.Steps {
			if definition.ID == step.StepID {
				codeNode = definition.Kind == "code" || definition.Kind == "iteration" || definition.Kind == "loop"
				break
			}
		}
		// Older sequential records may only bind the shared execution session.
		// This is a read projection: never backfill or rewrite historical rows.
		if !codeNode && run.Definition.Graph == nil && step.SessionID == "" {
			step.SessionID = run.ExecutionSessionID
		}
		phase := step.Status
		if phase == "" {
			phase = af.Queued
		} else if phase == af.WaitingConfirmation {
			phase = "approval"
		} else if phase == af.Running {
			phase = "preparing"
		}
		node := awNodeActivity{StepID: step.StepID, SessionID: step.SessionID, ActiveRunID: step.ActiveRunID, Status: step.Status, Phase: phase, UpdatedAt: max(step.StartedAt, step.FinishedAt)}
		if codeNode {
			node.Observed = true
			if step.Status == af.Running {
				node.Phase = af.Running
			}
			out.UpdatedAt = max(out.UpdatedAt, node.UpdatedAt)
			out.Nodes = append(out.Nodes, node)
			continue
		}
		if step.Status == af.WaitingConfirmation {
			node.ApprovalCount = 1
		}
		key := step.StepID + ":" + step.SessionID + ":" + step.ActiveRunID
		old, known := cache[key]
		if step.Status == af.Running && step.SessionID != "" && step.ActiveRunID != "" && active < awGraphConcurrency {
			active++
			if hub := s.agentWorkflowActivityHub(run, step); hub != nil {
				if sample, ok := hub.agentWorkflowActivity(step.ActiveRunID); ok {
					node.Observed, node.Turn, node.ToolName = true, sample.node.Turn, sample.node.ToolName
					node.Phase, node.ToolCount = sample.node.Phase, sample.node.ToolCount
					node.PendingToolCount, node.ApprovalCount = sample.node.PendingToolCount, sample.node.ApprovalCount
					node.UpdatedAt = now
					if known && old.seq == sample.seq {
						node.UpdatedAt = old.node.UpdatedAt
					}
					cache[key] = awActivitySample{node: node, seq: sample.seq}
				}
			}
		} else if known && old.node.Observed {
			// Facts observed on this connection may remain on the terminal card.
			// A fresh connection to an old Run leaves them absent rather than
			// loading whole transcripts or attributing another sequential step.
			node.Observed, node.Turn, node.ToolCount = true, old.node.Turn, old.node.ToolCount
			node.UpdatedAt = max(node.UpdatedAt, old.node.UpdatedAt)
		}
		out.UpdatedAt = max(out.UpdatedAt, node.UpdatedAt)
		out.Nodes = append(out.Nodes, node)
	}
	return out
}

func (s *Server) agentWorkflowActivityHub(run af.Run, step af.StepRun) *Hub {
	// Do not hydrate historical sessions: only executing nodes can have live
	// activity. This also bounds sampling to the runtime's ten active nodes.
	s.mgr.mu.Lock()
	sess := s.mgr.sessions[step.SessionID]
	s.mgr.mu.Unlock()
	if sess == nil || !s.mgr.backgroundTaskSessionOwned(step.SessionID, run.AgentID, run.OwnerID) {
		return nil
	}
	marker := s.mgr.agentWorkflowMarker(step.SessionID)
	if marker == nil || marker.OwnerID != run.OwnerID || marker.AgentID != run.AgentID || marker.WorkflowID != run.WorkflowID || marker.RunID != run.ID || marker.SourceSessionID != run.SourceSessionID {
		return nil
	}
	return sess.Hub()
}

// Read scalar activity under the Hub lock, without cloning text, arguments,
// results, frames or complete snapshots. The opening user-message RunID is the
// boundary for legacy sequential workflows sharing an execution session.
func (h *Hub) agentWorkflowActivity(activeRunID string) (awActivitySample, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || !h.live.Active || activeRunID == "" || h.live.RunID != activeRunID {
		return awActivitySample{}, false
	}
	count := 0
	start := len(h.msgs)
	for i := len(h.msgs) - 1; i >= 0; i-- {
		if h.msgs[i].Role == string(llm.RoleUser) && h.msgs[i].RunID != "" {
			if h.msgs[i].RunID != activeRunID {
				return awActivitySample{}, false
			}
			start = i + 1
			break
		}
	}
	for _, message := range h.msgs[start:] {
		for _, block := range message.Content {
			if block.Type == llm.BlockToolUse {
				count++
			}
		}
	}
	node := awNodeActivity{Observed: true, Phase: "model", Turn: h.live.Turn, ToolCount: &count, PendingToolCount: len(h.live.PendingTools)}
	approvalTool := ""
	for _, gate := range h.live.PendingGates {
		// Background-child gates belong to the session rather than this node's
		// active turn; they must not be attributed to a later sequential step.
		if gate.Origin == "" {
			if node.ApprovalCount == 0 {
				approvalTool = gate.Tool
			}
			node.ApprovalCount++
		}
	}
	switch {
	case node.ApprovalCount > 0:
		node.Phase, node.ToolName = "approval", approvalTool
	case len(h.live.PendingTools) > 0:
		node.Phase, node.ToolName = "tool", h.live.PendingTools[0].Name
	case len(h.live.Incoming) > 0:
		node.Phase, node.ToolName = "tool_input", h.live.Incoming[0].Name
	case h.live.Text != "":
		node.Phase = "answer"
	case h.live.Thinking != "":
		node.Phase = "thinking"
	}
	return awActivitySample{node: node, seq: h.seq}, true
}
