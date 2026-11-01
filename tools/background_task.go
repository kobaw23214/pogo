package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

const BackgroundTaskName = "background_task"

// BackgroundTaskBackend is issued by the host for one account, Agent and
// conversation. Arguments never choose identity, workspace or approval policy.
type BackgroundTaskBackend interface {
	Task(context.Context, json.RawMessage) (BackgroundTaskReceipt, error)
}

type BackgroundTaskRequest struct {
	Action           string   `json:"action"`
	Source           string   `json:"source"`
	TaskID           string   `json:"task_id,omitempty"`
	Title            string   `json:"title,omitempty"`
	Task             string   `json:"task,omitempty"`
	Enabled          *bool    `json:"enabled,omitempty"`
	ExpiresAt        string   `json:"expires_at,omitempty"`
	StopAfterSeconds int      `json:"stop_after_seconds,omitempty"`
	MaxTurns         int      `json:"max_turns,omitempty"`
	Kind             string   `json:"kind,omitempty"`
	EverySeconds     int64    `json:"every_seconds,omitempty"`
	At               string   `json:"at,omitempty"`
	Cron             string   `json:"cron,omitempty"`
	Timezone         string   `json:"timezone,omitempty"`
	ScriptPath       string   `json:"script_path,omitempty"`
	Mode             string   `json:"mode,omitempty"`
	Args             []string `json:"args,omitempty"`
	IntervalSeconds  int      `json:"interval_seconds,omitempty"`
	TimeoutSeconds   int      `json:"timeout_seconds,omitempty"`
	DebounceMS       int      `json:"debounce_ms,omitempty"`
	Network          *bool    `json:"network,omitempty"`
}

// Registration is not proof that the first execution or its business goal
// succeeded. This distinction survives the model's shortened result view.
type BackgroundTaskReceipt struct {
	Source       string `json:"source"`
	TaskID       string `json:"task_id,omitempty"`
	SourceID     string `json:"source_id,omitempty"`
	Registered   bool   `json:"registered"`
	Enabled      bool   `json:"enabled"`
	Status       string `json:"status,omitempty"`
	NextRun      string `json:"next_run,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	Verification string `json:"verification"`
	Result       any    `json:"result,omitempty"`
}

type BackgroundTask struct{ Backend BackgroundTaskBackend }

func (*BackgroundTask) Name() string                 { return BackgroundTaskName }
func (*BackgroundTask) ExecutionMode() ExecutionMode { return Sequential }
func (*BackgroundTask) IsReadOnly() bool             { return false }
func (*BackgroundTask) Description() string {
	return `Create and manage durable background tasks for this Agent. Use source=schedule for a task to execute at fixed times, including periodic measurements even when the measured value is unchanged. Use source=sentinel for an existing source-observation script whose meaningful events should wake the Agent. Both use the existing host task queue; neither requires keeping this conversation running.
Actions: prepare/check/create/list/status/pause/resume/retry/remove. Source is always schedule or sentinel; task_id comes from this tool. prepare and check are sentinel-only: prepare takes script_path (Python entry in a dedicated subdirectory) and mode, installs the versioned protocol helper and a guarded starter without overwriting existing files. It does not execute or register a task. Implement the source logic using its returned SDK guide, then check takes script_path/mode/optional args/network, or an owned saved task_id with no script/network overrides. check really executes the script for at most 30 seconds under the existing approval and filesystem/network policy; it is not a static or read-only validation. It never registers/enables a task, commits its saved state/cursor, settles events, or wakes an Agent. A valid first frame verifies the protocol only, not business truth or the full stream ACK cycle. A quiet stream with no frame is unverified, not a passed check. Preserve real execution failures and cancellation; do not replace them with quiet success.
For a check receipt, registered/enabled=false describes this operation's effects, not the current activation of an existing saved task: check never changes it. Use status to inspect actual task state. For schedule create use title, task, kind=every with every_seconds, kind=at with at, or kind=cron with cron, plus an explicit IANA timezone. The first every execution is one full interval after registration, never immediately. For sentinel create supply script_path/mode and its observation options after inspecting a successful check. Registration itself does not certify validation. Use the background-tasks Skill for the full workflow when available; the host tools remain the authority and the Skill grants no permissions.
Example schedule creation: {"action":"create","source":"schedule","title":"Status check","task":"Read the current status once and return a short report","kind":"every","every_seconds":300,"timezone":"Etc/UTC","stop_after_seconds":3600,"enabled":true}.
For schedule, task is the work to complete ONCE at each trigger (for example count the directory files once and save this sample in Pogo). Do not copy the surrounding request to create/recreate a schedule, repeat for ten minutes, wait for expiry, or stop the task into every execution's instruction. Cadence and the lifetime belong only in the saved scheduling fields.
The host saves each schedule run's final answer and execution history in Pogo automatically; instruct it to return the sample/report. Do not put watch_decide in a schedule task: that operation belongs only to a currently dispatched sentinel event batch. Use the receipt's remaining_planned_triggers_before_deadline when present; it counts planned times strictly before the cutoff, not guaranteed successful future runs.
Creation is paused by default. enabled=true or resume authorizes future unattended runs after this chat only when the user requested activation, subject to host review. Set exactly one of expires_at (RFC3339 absolute time) or stop_after_seconds when the user limits the task lifetime. The host stores an absolute deadline, including across restarts. No new execution starts at or after that deadline; reaching it does not undo effects already performed. timeout_seconds limits one sentinel check, not the subscription lifetime. Schedule runs retain bounded turns and a host-chosen approval policy; you cannot change identity, workspace, model or approval grants.
After a successful registration/resume receipt, the delegation step is done: report its ID, saved timing/deadline, and what was actually verified. Continue other requested work or finish this response. Do not hold the foreground open with sleeping, repeated status checks, another waiting process or a Todo waiting for the background lifetime to elapse. A registration_only receipt is not a completed run or a verified business result. pause stops future admission; remove preserves execution history. Neither action promises rollback or cancellation of work already executing. retry is sentinel-only and requires an owned task_id: explicitly requeue interrupted event work after inspecting its status and possible prior effects. It does not create a new watcher, resume paused collection, extend its deadline, or certify successful execution; schedule retry is unsupported.`
}

func (*BackgroundTask) InputSchema() map[string]any {
	p := watchScriptProps()
	for name, value := range map[string]any{
		"action":             map[string]any{"type": "string", "enum": []string{"prepare", "check", "create", "list", "status", "pause", "resume", "retry", "remove"}},
		"source":             map[string]any{"type": "string", "enum": []string{"schedule", "sentinel"}},
		"task_id":            prop("string", "ID returned by this tool; required for status/pause/resume/retry/remove; check may use an owned sentinel ID instead of script fields"),
		"title":              prop("string", "Create: short task name, at most 120 characters"),
		"task":               prop("string", "Create, at most 16000 UTF-8 bytes. schedule: complete this work ONCE per trigger; exclude schedule creation, repeated waiting and lifecycle management, which belong in timing/deadline fields. sentinel: standing event investigation goal"),
		"enabled":            prop("boolean", "Create paused by default; true requires the user's authorization to activate future runs"),
		"expires_at":         prop("string", "Optional RFC3339 absolute deadline; exclusive with stop_after_seconds"),
		"stop_after_seconds": prop("integer", "Optional lifetime from host registration time, 1-31536000 seconds; exclusive with expires_at"),
		"max_turns":          prop("integer", "Optional per-run ceiling, 1-300; can only narrow the host's bounded schedule limit"),
		"kind":               map[string]any{"type": "string", "enum": []string{"every", "at", "cron"}},
		"every_seconds":      prop("integer", "schedule/every: interval in seconds; first run occurs after this interval"),
		"at":                 prop("string", "schedule/at: future timestamp or date-time in the selected timezone"),
		"cron":               prop("string", "schedule/cron: five-field cron expression"),
		"timezone":           prop("string", "schedule: explicit IANA timezone, for example Asia/Shanghai or Etc/UTC"),
		"interval_seconds":   prop("integer", "sentinel/poll cadence, 1-604800 seconds"),
		"timeout_seconds":    prop("integer", "sentinel: check trial is 1-30 seconds; create observation timeout is 1-300 seconds; neither is the task lifetime"),
		"debounce_ms":        prop("integer", "sentinel: event coalescing delay, 0-10000 milliseconds"),
	} {
		p[name] = value
	}
	return watchObject([]string{"action", "source"}, p)
}

func (t *BackgroundTask) ValidateArgs(raw json.RawMessage) error {
	var a BackgroundTaskRequest
	if err := decodeWatch(t.InputSchema(), raw, &a); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	allowed := map[string]bool{"action": true, "source": true}
	if a.Action == "retry" && a.Source != "sentinel" {
		return errors.New("retry only supports an interrupted sentinel event batch; schedule retry is unsupported")
	}
	if a.Action == "prepare" || a.Action == "check" {
		if a.Source != "sentinel" {
			return errors.New("prepare/check require source=sentinel; a schedule is configured directly with create")
		}
		if a.Action == "prepare" {
			allowed["script_path"], allowed["mode"] = true, true
			request, _ := json.Marshal(WatchRequest{Action: "prepare", ScriptPath: a.ScriptPath, Mode: a.Mode})
			if err := (&Watch{}).ValidateArgs(request); err != nil {
				return err
			}
		} else {
			allowed["timeout_seconds"] = true
			if a.TaskID != "" {
				allowed["task_id"] = true
			} else {
				for _, key := range []string{"script_path", "mode", "args", "network"} {
					allowed[key] = true
				}
			}
			request, _ := json.Marshal(WatchTestRequest{WatchID: strings.TrimPrefix(a.TaskID, "sentinel:"), ScriptPath: a.ScriptPath, Mode: a.Mode, Args: a.Args, Network: a.Network, TimeoutSeconds: a.TimeoutSeconds})
			if err := (&WatchTest{}).ValidateArgs(request); err != nil {
				return err
			}
		}
	} else if a.Action != "create" {
		if a.Action != "list" {
			allowed["task_id"] = true
		}
		if a.Action != "list" {
			id := strings.TrimPrefix(a.TaskID, a.Source+":")
			valid := validWatchID(id)
			if a.Source == "schedule" {
				valid = validBackgroundScheduleID(id)
			}
			if !valid {
				return errors.New("task_id must be this source's ID returned by this tool")
			}
		}
	} else {
		for _, k := range []string{"title", "task", "enabled", "expires_at", "stop_after_seconds", "max_turns"} {
			allowed[k] = true
		}
		if err := backgroundTaskMissingCreateFields(a); err != nil {
			return err
		}
		if strings.TrimSpace(a.Title) == "" || utf8.RuneCountInString(a.Title) > 120 || strings.ContainsAny(a.Title, "\x00\r\n") {
			return errors.New("title must be 1-120 characters on one line")
		}
		if strings.TrimSpace(a.Task) == "" || len(a.Task) > 16000 || strings.ContainsRune(a.Task, 0) {
			return errors.New("task must be 1-16000 bytes")
		}
		if _, err := ResolveTaskDeadline(a.ExpiresAt, a.StopAfterSeconds, time.Now()); err != nil {
			return err
		}
		if _, supplied := fields["expires_at"]; supplied && a.ExpiresAt == "" {
			return errors.New("expires_at must not be empty when supplied")
		}
		if _, supplied := fields["stop_after_seconds"]; supplied && a.StopAfterSeconds == 0 {
			return errors.New("stop_after_seconds must be positive when supplied")
		}
		if a.MaxTurns < 0 || a.MaxTurns > 300 {
			return errors.New("max_turns must be 1-300 or omitted")
		}
		if a.Source == "schedule" {
			for _, k := range []string{"kind", "every_seconds", "at", "cron", "timezone"} {
				allowed[k] = true
			}
			if strings.TrimSpace(a.Timezone) == "" {
				return errors.New("schedule requires an explicit IANA timezone")
			}
			switch a.Kind {
			case "every":
				if a.EverySeconds < 1 || a.EverySeconds > 31536000 || a.At != "" || a.Cron != "" {
					return errors.New("every needs every_seconds=1-31536000, without at/cron")
				}
			case "at":
				if a.At == "" || a.EverySeconds != 0 || a.Cron != "" {
					return errors.New("at needs a timestamp, without every_seconds/cron")
				}
			case "cron":
				if a.Cron == "" || a.EverySeconds != 0 || a.At != "" {
					return errors.New("cron needs an expression, without every_seconds/at")
				}
			default:
				return errors.New("schedule kind must be every, at or cron")
			}
		} else {
			for _, k := range []string{"script_path", "mode", "args", "network", "interval_seconds", "timeout_seconds", "debounce_ms"} {
				allowed[k] = true
			}
			if err := validateWatchScript(a.ScriptPath, a.Mode, a.Args); err != nil {
				return err
			}
			if a.IntervalSeconds < 0 || a.IntervalSeconds > 604800 || a.TimeoutSeconds < 0 || a.TimeoutSeconds > 300 || a.DebounceMS < 0 || a.DebounceMS > 10000 || a.Mode == "stream" && a.IntervalSeconds != 0 {
				return errors.New("invalid sentinel observation limits")
			}
		}
	}
	for k := range fields {
		if !allowed[k] {
			return fmt.Errorf("%s/%s does not accept %s", a.Source, a.Action, k)
		}
	}
	return nil
}

// Portable providers receive one flat schema for all actions. Report the whole
// create contract together so correcting a partial call does not require a
// separate failed turn for each omitted field. These are shapes only: nothing
// is filled into the request, persisted or authorized by this diagnostic.
func backgroundTaskMissingCreateFields(a BackgroundTaskRequest) error {
	missing := []string{}
	for _, field := range []struct{ name, value string }{{"title", a.Title}, {"task", a.Task}} {
		if strings.TrimSpace(field.value) == "" {
			missing = append(missing, field.name)
		}
	}
	var required, template string
	if a.Source == "schedule" {
		required = "title, task (work to complete once per trigger), kind, timezone, and its matching time field: every_seconds for every, at for at, or cron for cron"
		if a.Kind == "" {
			missing = append(missing, "kind")
		}
		if strings.TrimSpace(a.Timezone) == "" {
			missing = append(missing, "timezone")
		}
		switch a.Kind {
		case "at":
			if strings.TrimSpace(a.At) == "" {
				missing = append(missing, "at")
			}
			template = `{"action":"create","source":"schedule","title":"<title>","task":"<user-requested work to complete once>","kind":"at","at":"<future date-time>","timezone":"<IANA timezone>","enabled":false}`
		case "cron":
			if strings.TrimSpace(a.Cron) == "" {
				missing = append(missing, "cron")
			}
			template = `{"action":"create","source":"schedule","title":"<title>","task":"<user-requested work to complete once>","kind":"cron","cron":"<five-field cron expression>","timezone":"<IANA timezone>","enabled":false}`
		default:
			if a.Kind == "every" && a.EverySeconds == 0 {
				missing = append(missing, "every_seconds")
			}
			if a.Kind == "" {
				missing = append(missing, "matching time field (every_seconds, at, or cron)")
			}
			template = `{"action":"create","source":"schedule","title":"<title>","task":"<user-requested work to complete once>","kind":"every","every_seconds":120,"timezone":"<IANA timezone>","enabled":false}`
		}
	} else {
		required = "title, task (event investigation goal), script_path, mode"
		if strings.TrimSpace(a.ScriptPath) == "" {
			missing = append(missing, "script_path")
		}
		if a.Mode == "" {
			missing = append(missing, "mode")
		}
		if a.Mode == "stream" {
			template = `{"action":"create","source":"sentinel","title":"<title>","task":"<user-requested event investigation goal>","script_path":"<existing workspace-relative script>","mode":"stream","enabled":false}`
		} else {
			template = `{"action":"create","source":"sentinel","title":"<title>","task":"<user-requested event investigation goal>","script_path":"<existing workspace-relative script>","mode":"poll","interval_seconds":120,"enabled":false}`
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("background_task create is incomplete. Missing or empty: %s. Required: %s. Submit one complete request; do not repeat the partial call. Template (replace placeholders and example timing with the user's request; false means paused): %s. No task was registered or enabled", strings.Join(missing, ", "), required, template)
}

func validBackgroundScheduleID(id string) bool {
	if len(id) == 0 || len(id) > 40 {
		return false
	}
	for i, r := range id {
		letterOrDigit := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if !letterOrDigit && (i == 0 || r != '.' && r != '_' && r != '-') {
			return false
		}
	}
	return true
}

// Invalid requests remain on the reviewed side of the approval boundary.
func BackgroundTaskReadOnly(raw json.RawMessage) bool {
	if (&BackgroundTask{}).ValidateArgs(raw) != nil {
		return false
	}
	var a BackgroundTaskRequest
	_ = json.Unmarshal(raw, &a)
	return a.Action == "list" || a.Action == "status"
}

func (t *BackgroundTask) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := t.ValidateArgs(raw); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if t.Backend == nil || reflect.ValueOf(t.Backend).Kind() == reflect.Pointer && reflect.ValueOf(t.Backend).IsNil() {
		return Result{}, errors.New("background tasks are unavailable in this session")
	}
	r, err := t.Backend.Task(ctx, raw)
	if err != nil {
		return Result{}, err
	}
	var a BackgroundTaskRequest
	_ = json.Unmarshal(raw, &a)
	b, err := json.Marshal(r)
	if err != nil {
		return Result{}, err
	}
	text := string(b)
	details := map[string]any{"type": BackgroundTaskName, "action": a.Action, "result": r}
	if a.Action == "prepare" {
		text = "脚本准备完成；本次操作没有执行脚本、注册或启用后台任务。请按 SDK 指南实现业务代码后再试运行。\n" + text
	} else if a.Action == "check" {
		if r.Verification == "protocol_frame" {
			text = "试运行已收到合法协议帧；业务条件与完整 ACK 循环尚未验证。本次操作未注册任务，也未变更正式任务的启用状态或已保存进度。\n" + text
		} else {
			text = "试运行未完成协议验证，不能视为检查通过。本次操作未注册任务，也未变更正式任务的启用状态或已保存进度；请查看回执原因。\n" + text
		}
	}
	if (a.Action == "create" || a.Action == "resume") && r.Registered {
		state := "registered and paused; it is not running"
		if r.Enabled {
			state = "registered and enabled for background execution"
		}
		text = "Background delegation " + state + ". Registration is confirmed, not business completion. The host owns future execution and its saved deadline. Do not sleep, poll repeatedly or keep a foreground Todo waiting for its lifetime; continue other work or report the saved task to the user.\n" + text
		details["background_handoff"] = map[string]any{"registered": true, "enabled": r.Enabled, "task_id": r.TaskID, "verification": "registration_only"}
	}
	return Result{Text: text, Details: details}, nil
}
