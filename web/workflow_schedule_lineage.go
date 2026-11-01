package web

// A schedule wrapper and its workflow continuations have different queue task
// keys. Follow the logical retry lineage rather than rewriting old receipts or
// treating the existence of an unrelated queue retry as acknowledgement.
import (
	"errors"
	"fmt"

	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/workflow"
)

type workflowScheduleGuard struct {
	r        *taskRunRuntime
	db       *taskqueue.Store
	families map[string]map[string][]taskqueue.WorkflowRunMeta
}

func newWorkflowScheduleGuard(r *taskRunRuntime, db *taskqueue.Store) *workflowScheduleGuard {
	return &workflowScheduleGuard{r: r, db: db, families: map[string]map[string][]taskqueue.WorkflowRunMeta{}}
}

// blocked preserves the old guard until a wrapper has a logical binding or a
// trustworthy imported native run with managed continuations/explicit retries.
func (g *workflowScheduleGuard) blocked(row taskqueue.Run) (bool, bool, error) {
	if row.WorkflowID == "" {
		return false, false, nil
	}
	meta, err := g.db.WorkflowRunForExecution(row.ID)
	bound := err == nil
	if errors.Is(err, taskqueue.ErrNotFound) {
		// Imported historical JSON can become managed through a new retry or
		// continuation without mutating its already-terminal wrapper request.
		if row.SessionID == "" || !validID(row.SessionID) {
			return false, false, nil
		}
		meta, err = g.db.GetWorkflowRun(row.SessionID)
		if errors.Is(err, taskqueue.ErrNotFound) {
			return false, false, nil
		}
	}
	if err != nil {
		return false, true, err
	}
	if meta.WorkflowID != row.WorkflowID || meta.OwnerID != row.OwnerID {
		if !bound {
			return false, false, nil
		}
		return false, true, fmt.Errorf("%w: 工作流与定时执行归属不一致", taskqueue.ErrConflict)
	}
	familyKey := meta.SpaceID + "\x00" + meta.OwnerID + "\x00" + meta.WorkflowID
	children, ok := g.families[familyKey]
	if !ok {
		all, err := g.db.ListWorkflowRuns(taskqueue.WorkflowRunFilter{SpaceID: meta.SpaceID, OwnerID: meta.OwnerID, WorkflowID: meta.WorkflowID})
		if err != nil {
			return false, true, err
		}
		children = map[string][]taskqueue.WorkflowRunMeta{}
		for _, candidate := range all {
			if candidate.Source == "retry" && candidate.RetryOf != "" && candidate.SourceID == candidate.RetryOf {
				children[candidate.RetryOf] = append(children[candidate.RetryOf], candidate)
			}
		}
		g.families[familyKey] = children
	}
	if !bound && len(children[meta.ID]) == 0 {
		segments, err := g.db.ListWorkflowExecutions(meta.ID)
		if err != nil {
			return false, true, err
		}
		managed := false
		for _, segment := range segments {
			managed = managed || segment.Source == "workflow"
		}
		if !managed {
			return false, false, nil
		}
	}
	if row.Status == taskqueue.Queued || row.Status == taskqueue.Running {
		return true, true, nil
	}
	blocked, err := g.nodeBlocked(meta, children, map[string]bool{}, row.Status == taskqueue.Interrupted)
	return blocked, true, err
}

func (g *workflowScheduleGuard) nodeBlocked(meta taskqueue.WorkflowRunMeta, children map[string][]taskqueue.WorkflowRunMeta, visiting map[string]bool, forceUnknown bool) (bool, error) {
	if visiting[meta.ID] {
		return true, fmt.Errorf("%w: 工作流重试链存在循环", taskqueue.ErrConflict)
	}
	visiting[meta.ID] = true
	defer delete(visiting, meta.ID)
	executions, err := g.db.ListWorkflowExecutions(meta.ID)
	if err != nil {
		return true, err
	}
	for _, execution := range executions {
		if execution.Status == taskqueue.Queued || execution.Status == taskqueue.Running {
			return true, nil
		}
	}
	if g.r.m.wfruns == nil {
		return true, nil
	}
	rec, exists := g.r.m.wfruns.snapshot(meta.ID)
	unknown := forceUnknown || !exists
	if exists {
		switch rec.Status {
		case "queued", workflow.RunWorking, "running", "waiting_human":
			return true, nil
		case workflow.RunCompleted, workflow.RunFailed, workflow.RunCancelled:
		case taskqueue.Interrupted:
			unknown = true
		default:
			unknown = true
		}
		unknown = unknown || rec.Error == "process restarted" || rec.PersistenceError != ""
	}
	if len(executions) > 0 {
		latest := executions[0]
		for _, candidate := range executions {
			if candidate.CreatedAt > latest.CreatedAt || candidate.CreatedAt == latest.CreatedAt && (candidate.FinishedAt > latest.FinishedAt || candidate.FinishedAt == latest.FinishedAt && candidate.Status == taskqueue.Interrupted) {
				latest = candidate
			}
		}
		// The state file identifies its last applied segment more precisely than
		// admission timestamps when several segments were created in one ms.
		if exists && rec.ExecutionID != "" {
			for _, candidate := range executions {
				if candidate.ID == rec.ExecutionID {
					latest = candidate
					break
				}
			}
		}
		unknown = unknown || latest.Status == taskqueue.Interrupted
	}
	retries := children[meta.ID]
	for _, child := range retries {
		blocked, err := g.nodeBlocked(child, children, visiting, false)
		if err != nil || blocked {
			return true, err
		}
	}
	// Each ambiguous branch requires an explicit retry with a known terminal
	// result (possibly after further explicit retries). Cancellation intent on
	// an unknown run alone never clears its ambiguous receipt.
	return unknown && len(retries) == 0, nil
}
