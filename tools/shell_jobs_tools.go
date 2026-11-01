package tools

import (
	"context"
	"encoding/json"
	"errors"
)

// These helpers observe or stop existing jobs. They cannot start commands and
// are installed only alongside the host's selected Bash capability.
type ShellTasks struct{ Jobs *ShellJobRunner }
type ShellOutput struct{ Jobs *ShellJobRunner }
type ShellStop struct{ Jobs *ShellJobRunner }

func (*ShellTasks) Name() string { return ShellTasksName }
func (*ShellTasks) Description() string {
	return "List managed shell tasks belonging to this session, including completed tasks after a restart. Does not start or repeat a command."
}
func (*ShellTasks) ExecutionMode() ExecutionMode { return Parallel }
func (*ShellTasks) IsReadOnly() bool             { return true }
func (*ShellTasks) InputSchema() map[string]any {
	return object(nil, map[string]any{"active_only": prop("boolean", "Only running tasks; defaults to true"), "limit": prop("integer", "Maximum tasks to return, 1 to 100; defaults to 20")})
}
func (t *ShellTasks) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if t.Jobs == nil {
		return Result{}, errors.New("shell task runner is unavailable")
	}
	var args struct {
		ActiveOnly *bool `json:"active_only"`
		Limit      int   `json:"limit"`
	}
	if err := unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	if args.Limit == 0 {
		args.Limit = 20
	}
	if args.Limit < 1 || args.Limit > 100 {
		return Result{}, errors.New("limit must be between 1 and 100")
	}
	active := true
	if args.ActiveOnly != nil {
		active = *args.ActiveOnly
	}
	records := t.Jobs.List(active)
	if len(records) > args.Limit {
		records = records[len(records)-args.Limit:]
	}
	return shellObservation(map[string]any{"tasks": records}), nil
}
func (*ShellOutput) Name() string { return ShellOutputName }
func (*ShellOutput) Description() string {
	return "Read a bounded snapshot of a managed shell task's output by task ID. Offset and next_offset are byte positions; reading output never repeats the command. Completion is reported automatically, so avoid polling."
}
func (*ShellOutput) ExecutionMode() ExecutionMode { return Parallel }
func (*ShellOutput) IsReadOnly() bool             { return true }
func (*ShellOutput) InputSchema() map[string]any {
	return object([]string{"task_id"}, map[string]any{"task_id": prop("string", "Exact shell task ID from Bash or shell_tasks"), "offset": prop("integer", "Nonnegative byte offset; defaults to 0"), "limit": prop("integer", "Maximum bytes to read, up to 50000; defaults to 16384")})
}
func (t *ShellOutput) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if t.Jobs == nil {
		return Result{}, errors.New("shell task runner is unavailable")
	}
	var args struct {
		TaskID string `json:"task_id"`
		Offset int64  `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	if args.Limit < 0 {
		return Result{}, errors.New("limit must be positive")
	}
	out, err := t.Jobs.Output(args.TaskID, args.Offset, args.Limit)
	if err != nil {
		return Result{}, err
	}
	return shellObservation(out), nil
}
func (*ShellStop) Name() string { return ShellStopName }
func (*ShellStop) Description() string {
	return "Stop one managed shell task in this session and wait for its process group and output collector to settle before returning its final state. Repeated cancellation is safe."
}
func (*ShellStop) ExecutionMode() ExecutionMode { return Sequential }
func (*ShellStop) IsReadOnly() bool             { return false }
func (*ShellStop) InputSchema() map[string]any {
	return object([]string{"task_id"}, map[string]any{"task_id": prop("string", "Exact shell task ID"), "reason": prop("string", "Reason for stopping the task")})
}
func (t *ShellStop) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if t.Jobs == nil {
		return Result{}, errors.New("shell task runner is unavailable")
	}
	var args struct {
		TaskID string `json:"task_id"`
		Reason string `json:"reason"`
	}
	if err := unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	if err := t.Jobs.StopContext(ctx, args.TaskID, args.Reason); err != nil {
		return Result{}, err
	}
	record, err := t.Jobs.Get(args.TaskID)
	if err != nil {
		return Result{}, err
	}
	return shellObservation(map[string]any{"stop_requested": true, "task": record}), nil
}
func shellObservation(value any) Result {
	data, _ := json.Marshal(value)
	return Result{Text: string(data), Details: value}
}
func shellJobTools(jobs *ShellJobRunner) []Tool {
	if jobs == nil {
		return nil
	}
	return []Tool{&ShellTasks{Jobs: jobs}, &ShellOutput{Jobs: jobs}, &ShellStop{Jobs: jobs}}
}
