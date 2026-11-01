package taskqueue

// Graph execution uses the same durable logical Run and queue claims as v1.
// Workers merge one node at a time: independent completions and confirmations
// never overwrite another node's receipt.
import (
	"bytes"
	"database/sql"
	"encoding/json"
	"math"

	af "github.com/yosukeno/pogo/agentworkflow"
)

func awGraphClaim(tx *sql.Tx, owner, agent, wid, rid, eid string, claim int64) (af.Run, storedRun, error) {
	r, err := awRun(tx, owner, agent, wid, rid)
	if err != nil {
		return r, storedRun{}, err
	}
	q, err := get(tx, eid)
	if err != nil {
		return r, q, err
	}
	if r.Definition.Graph == nil && r.NodeTest == nil || q.Source != "agent_workflow" || q.WorkflowRunID != rid || q.WorkflowID != wid || q.OwnerID != owner || q.AgentID != agent || q.Status != Running || q.Revision != claim || r.Revision == math.MaxInt64 {
		return r, q, ErrConflict
	}
	return r, q, nil
}
func awGraphStepIndex(r af.Run, id string) int {
	for i, st := range r.Steps {
		if st.StepID == id {
			return i
		}
	}
	return -1
}
func awGraphProgress(r *af.Run) {
	r.CurrentStep = 0
	for _, st := range r.Steps {
		if st.Status == af.Completed || st.Status == af.Skipped {
			r.CurrentStep++
		}
	}
}
func awGraphSameStep(a, b af.StepRun) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// AWGraphSaveStep checks the dispatcher lease and the exact old node receipt,
// then merges only that node into the current record. The logical revision may
// have advanced because another node was confirmed while a model was working.
func (s *Store) AWGraphSaveStep(owner, agent, wid, rid, eid string, claim int64, before, after af.StepRun) (af.Run, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return af.Run{}, err
	}
	defer tx.Rollback()
	r, q, err := awGraphClaim(tx, owner, agent, wid, rid, eid, claim)
	if err != nil {
		return r, err
	}
	i := awGraphStepIndex(r, before.StepID)
	if i < 0 || after.StepID != before.StepID || !awGraphSameStep(r.Steps[i], before) || awTerminal(r.Status) {
		return r, ErrConflict
	}
	node := r.Definition.Steps[i]
	if r.NodeTest != nil && node.ID != r.NodeTest.StepID || !awValidCodeReceipt(node, after) || !awValidReceiptTransition(r, i, before, after) {
		return r, ErrConflict
	}
	if !validText(after.Summary, 128<<10, false) || !validText(after.Feedback, 64<<10, false) || after.Feedback != before.Feedback || !validText(after.SessionID, 256, false) || !validText(after.ActiveRunID, 256, false) || after.StartedAt < 0 || after.FinishedAt < 0 {
		return r, ErrConflict
	}
	if after.Artifact != "" && (node.OutputFile != after.Artifact || !af.SafePath(after.Artifact) || !af.ValidHash(after.ArtifactSHA256)) || after.Artifact == "" && after.ArtifactSHA256 != "" {
		return r, ErrConflict
	}
	if before.Status == af.Completed || before.Status == af.Skipped || before.Status == af.Failed || before.Status == af.Cancelled || before.Status == af.Interrupted {
		return r, ErrConflict
	}
	if before.SessionID != "" && before.SessionID != after.SessionID || before.ActiveRunID != "" && before.ActiveRunID != after.ActiveRunID || before.StartedAt != 0 && before.StartedAt != after.StartedAt {
		return r, ErrConflict
	}
	if len(before.CodeInputs) != 0 && !bytes.Equal(before.CodeInputs, after.CodeInputs) {
		return r, ErrConflict
	}
	if before.Status == af.Queued {
		if r.CancelRequested || q.CancelRequested {
			return r, ErrConflict
		}
		allowed := r.NodeTest != nil && node.ID == r.NodeTest.StepID && after.Status != af.Skipped
		if r.NodeTest == nil {
			plan, e := af.PlanGraph(r.Definition, r.Steps)
			if e != nil {
				return r, ErrConflict
			}
			candidates := plan.Ready
			if after.Status == af.Skipped {
				candidates = plan.Skipped
			}
			for _, id := range candidates {
				if id == node.ID {
					allowed = true
				}
			}
		}
		if !allowed {
			return r, ErrConflict
		}
		switch after.Status {
		case af.Running:
			if node.Kind != "agent" && node.Kind != "code" && node.Kind != "iteration" && node.Kind != "loop" || node.Kind == "agent" && after.SessionID == "" || after.StartedAt == 0 {
				return r, ErrConflict
			}
		case af.WaitingConfirmation:
			if node.Kind != "confirm" || after.StartedAt == 0 {
				return r, ErrConflict
			}
		case af.Completed:
			if node.Kind != "condition" && node.Kind != "parallel" && node.Kind != "join" {
				return r, ErrConflict
			}
			if after.StartedAt == 0 || after.FinishedAt < after.StartedAt {
				return r, ErrConflict
			}
		case af.Skipped:
			if after.SessionID != "" || after.ActiveRunID != "" || after.Artifact != "" {
				return r, ErrConflict
			}
		case af.Failed:
			// A ready control node can fail to evaluate its input, or an Agent
			// node can fail during preparation before any model was launched.
			if after.StartedAt == 0 || after.FinishedAt < after.StartedAt {
				return r, ErrConflict
			}
		default:
			return r, ErrConflict
		}
	} else if before.Status == af.Running {
		if node.Kind != "agent" && node.Kind != "code" && node.Kind != "iteration" && node.Kind != "loop" {
			return r, ErrConflict
		}
		switch after.Status {
		case af.Running, af.Completed, af.Failed, af.Interrupted, af.Cancelled:
		default:
			return r, ErrConflict
		}
		if after.Status != af.Running && after.FinishedAt < after.StartedAt {
			return r, ErrConflict
		}
	} else {
		return r, ErrConflict
	}
	if node.Kind == "condition" && after.Status == af.Completed {
		result, e := af.EvaluateCondition(node, r.Inputs, r.Steps)
		if e != nil {
			return r, ErrConflict
		}
		want := "false"
		if result {
			want = "true"
		}
		if after.Branch != want {
			return r, ErrConflict
		}
	} else if after.Branch != "" {
		return r, ErrConflict
	}
	if node.Kind != "agent" && (after.SessionID != "" || after.ActiveRunID != "" || after.Artifact != "") {
		return r, ErrConflict
	}
	if after.Status == af.Completed && node.Kind == "agent" && node.OutputFile != "" && after.Artifact == "" {
		return r, ErrConflict
	}
	r.Steps[i] = after
	if r.ExecutionSessionID == "" && after.SessionID != "" {
		r.ExecutionSessionID = after.SessionID
	}
	if r.ExecutionSessionID == "" && after.Repeat != nil {
		for _, item := range after.Repeat.Items {
			if item.SessionID != "" {
				r.ExecutionSessionID = item.SessionID
				break
			}
		}
	}
	r.Status = af.Running
	r.CancelRequested = r.CancelRequested || q.CancelRequested
	if after.Status == af.Completed && after.Summary != "" {
		r.Summary = after.Summary
	}
	awGraphProgress(&r)
	r.Revision++
	r.UpdatedAt = now()
	if err = awSaveRun(tx, r); err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	return r, nil
}

// AWGraphSettle is a whole-run CAS used only at coordinator boundaries. A
// concurrent confirmation advances Revision and forces a fresh scheduling pass.
func (s *Store) AWGraphSettle(owner, agent, wid, rid, eid string, claim, expected int64, status, message string) (af.Run, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return af.Run{}, err
	}
	defer tx.Rollback()
	r, q, err := awGraphClaim(tx, owner, agent, wid, rid, eid, claim)
	if err != nil {
		return r, err
	}
	if r.Revision != expected || awTerminal(r.Status) || !validText(message, 64<<10, false) {
		return r, ErrConflict
	}
	r.CancelRequested = r.CancelRequested || q.CancelRequested
	if r.CancelRequested && status == af.Completed {
		status, message = af.Cancelled, "已取消；已完成的节点结果保留"
	}
	switch status {
	case af.Running:
		if r.CancelRequested {
			return r, ErrConflict
		}
	case af.WaitingConfirmation:
		if r.NodeTest != nil {
			return r, ErrConflict
		}
		plan, e := af.PlanGraph(r.Definition, r.Steps)
		if e != nil || len(plan.Ready) > 0 || len(plan.Skipped) > 0 || r.CancelRequested {
			return r, ErrConflict
		}
		waiting := false
		for _, st := range r.Steps {
			if st.Status == af.Running {
				return r, ErrConflict
			}
			waiting = waiting || st.Status == af.WaitingConfirmation
		}
		if !waiting {
			return r, ErrConflict
		}
	case af.Completed:
		if r.NodeTest != nil {
			i := awGraphStepIndex(r, r.NodeTest.StepID)
			if i < 0 || r.Steps[i].Status != af.Completed {
				return r, ErrConflict
			}
		} else {
			plan, e := af.PlanGraph(r.Definition, r.Steps)
			if e != nil || !plan.Done {
				return r, ErrConflict
			}
		}
	case af.Cancelled, af.Failed, af.Interrupted:
		for i := range r.Steps {
			st := &r.Steps[i]
			switch st.Status {
			case af.Running, af.WaitingConfirmation:
				st.Status = status
				st.FinishedAt = now()
				awStopRepeatItems(st, status, message)
			case af.Queued:
				st.Status = af.Skipped
				st.FinishedAt = now()
			}
		}
	default:
		return r, ErrConflict
	}
	r.Status = status
	r.Error = message
	awGraphProgress(&r)
	r.Revision++
	r.UpdatedAt = now()
	if err = awSaveRun(tx, r); err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	return r, nil
}

func (s *Store) awGraphConfirmTx(tx *sql.Tx, r af.Run, key, feedback string, expected []string) (af.Run, Run, bool, error) {
	if len(expected) != 1 {
		return r, Run{}, false, ErrConflict
	}
	fingerprint := digest([]byte(feedback), nil)
	var oldFingerprint, eid, oldStep string
	err := tx.QueryRow(`SELECT fingerprint,execution_id,step_id FROM agent_workflow_commands WHERE run_id=? AND command_key=?`, r.ID, key).Scan(&oldFingerprint, &eid, &oldStep)
	if err == nil {
		if oldFingerprint != fingerprint || oldStep != expected[0] {
			return r, Run{}, false, ErrConflict
		}
		q, e := get(tx, eid)
		return r, q.Run, true, e
	}
	if err != sql.ErrNoRows {
		return r, Run{}, false, err
	}
	i := awGraphStepIndex(r, expected[0])
	if i < 0 || r.Definition.Steps[i].Kind != "confirm" || r.Steps[i].Status != af.WaitingConfirmation || r.CancelRequested || awTerminal(r.Status) || r.Revision == math.MaxInt64 {
		return r, Run{}, false, ErrConflict
	}
	r.Steps[i].Status = af.Completed
	r.Steps[i].Feedback = feedback
	r.Steps[i].FinishedAt = now()
	// Every confirmation creates a durable wake. The queue serializes dispatcher
	// claims per logical Run; a worker still in flight keeps its Running status.
	if r.Status != af.Running {
		r.Status = af.Queued
	}
	r.Error = ""
	r.Revision++
	r.UpdatedAt = now()
	awGraphProgress(&r)
	q, _, err := enqueueTx(tx, awRequest(r, "confirmation", "confirm:"+expected[0]))
	if err != nil {
		return r, Run{}, false, err
	}
	if err = awLink(tx, r.ID, q.ID); err != nil {
		return r, Run{}, false, err
	}
	_, err = tx.Exec(`INSERT INTO agent_workflow_commands(run_id,step_id,command_key,fingerprint,execution_id) VALUES(?,?,?,?,?)`, r.ID, expected[0], key, fingerprint, q.ID)
	if err != nil {
		return r, Run{}, false, err
	}
	if err = awSaveRun(tx, r); err != nil {
		return r, Run{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return r, Run{}, false, err
	}
	return r, q, false, nil
}

// A confirmation queued while a sibling was running must never hide a crashed
// worker. Interrupt unknown graph work and fence every queued continuation.
func (s *Store) awGraphRecover() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT r.record FROM agent_workflow_runs r WHERE EXISTS(SELECT 1 FROM agent_workflow_executions e JOIN taskqueue_runs q ON q.id=e.execution_id WHERE e.run_id=r.id AND q.status='interrupted')`)
	if err != nil {
		return err
	}
	var runs []af.Run
	for rows.Next() {
		r, e := awDecodeRun(rows)
		if e != nil {
			rows.Close()
			return e
		}
		if r.Definition.Graph != nil && !awTerminal(r.Status) {
			runs = append(runs, r)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, r := range runs {
		unknown := false
		for _, st := range r.Steps {
			unknown = unknown || st.Status == af.Running
		}
		if !unknown && r.Status != af.Running {
			continue
		}
		r.Status = af.Interrupted
		r.Error = "并行流程执行期间服务中断，外部操作结果待核查；未自动重跑"
		for i := range r.Steps {
			st := &r.Steps[i]
			if st.Status == af.Running {
				st.Status = af.Interrupted
				st.FinishedAt = now()
				awStopRepeatItems(st, af.Interrupted, r.Error)
			} else if st.Status == af.Queued {
				st.Status = af.Skipped
				st.FinishedAt = now()
			}
		}
		r.Revision++
		r.UpdatedAt = now()
		awGraphProgress(&r)
		if err = awSaveRun(tx, r); err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE taskqueue_runs SET status='cancelled',cancel_requested=1,revision=revision+1,finished_at=?,end_reason='graph_restart_fence' WHERE status='queued' AND id IN (SELECT execution_id FROM agent_workflow_executions WHERE run_id=?)`, now(), r.ID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
