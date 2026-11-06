package web

import (
	"context"
	"io"

	"github.com/yosukeno/pogo/schedule"
)

// scheduleJobRunner preserves normal Agent and legacy prompt execution.
// A retired platform binding must never fall through to a prompt child.
func (m *Manager) scheduleJobRunner(ctx context.Context, job schedule.Job, logs schedule.LogPaths, mirror io.Writer) schedule.RunOutcome {
	if job.WorkflowID != "" {
		return schedule.RunOutcome{Err: "平台工作流已移除，此计划不可执行", ExitCode: 1}
	}
	if job.AgentID != "" {
		return m.runAgentSchedule(ctx, job, logs, mirror)
	}
	return schedRunner(ctx, job, logs, mirror)
}
