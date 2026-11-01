package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeBackgroundTaskBackend struct {
	calls   int
	err     error
	request BackgroundTaskRequest
}

func TestBackgroundTaskScheduleReportUsesHostHistory(t *testing.T) {
	description := (&BackgroundTask{}).Description()
	for _, want := range []string{"host saves each schedule run's final answer", "Do not put watch_decide in a schedule task", "work to complete ONCE"} {
		if !strings.Contains(description, want) {
			t.Fatalf("schedule report contract missing %q", want)
		}
	}
}

func TestBackgroundTaskRetryRetainsSentinelOnlyReviewBoundary(t *testing.T) {
	backend := &fakeBackgroundTaskBackend{}
	tool := &BackgroundTask{Backend: backend}
	valid := json.RawMessage(`{"action":"retry","source":"sentinel","task_id":"sentinel:watch_1"}`)
	if _, err := tool.Execute(context.Background(), valid); err != nil || backend.calls != 1 || backend.request.Action != "retry" || backend.request.Source != "sentinel" {
		t.Fatalf("valid explicit retry did not reach bound backend: %v", err)
	}
	if BackgroundTaskReadOnly(valid) {
		t.Fatal("retry bypassed mutation review")
	}
	for _, raw := range []string{
		`{"action":"retry","source":"schedule","task_id":"schedule:daily"}`,
		`{"action":"retry","source":"sentinel"}`,
		`{"action":"retry","source":"sentinel","task_id":"schedule:daily"}`,
		`{"action":"retry","source":"sentinel","task_id":"sentinel:watch_1","enabled":true}`,
		`{"action":"retry","source":"sentinel","task_id":"sentinel:watch_1","stop_after_seconds":600}`,
		`{"action":"retry","source":"sentinel","task_id":"sentinel:watch_1","script_path":"replacement.py"}`,
	} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err == nil || backend.calls != 1 {
			t.Fatalf("unsupported or expanding retry reached backend: %s", raw)
		}
	}
}

func (b *fakeBackgroundTaskBackend) Task(_ context.Context, raw json.RawMessage) (BackgroundTaskReceipt, error) {
	b.calls++
	_ = json.Unmarshal(raw, &b.request)
	return BackgroundTaskReceipt{Source: b.request.Source, TaskID: "owned-task", Registered: true, Enabled: b.request.Enabled != nil && *b.request.Enabled, Verification: "registration_only"}, b.err
}

func TestBackgroundTaskSchemaKeepsAuthorityAndSourceFieldsExclusive(t *testing.T) {
	base := `{"action":"create","source":"schedule","title":"统计","task":"保存每次统计结果","kind":"every","every_seconds":120,"timezone":"Asia/Shanghai","stop_after_seconds":600}`
	tool := &BackgroundTask{Backend: &fakeBackgroundTaskBackend{}}
	if err := tool.ValidateArgs(json.RawMessage(base)); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{`"owner_id":"other"`, `"agent_id":"other"`, `"workspace":"/private"`, `"model":"other"`, `"approval_mode":"auto"`, `"script_path":"watch.py"`, `"network":true`, `"timeout_seconds":10`, `"enabled":null`, `"expires_at":"2099-01-01T00:00:00Z"`, `"stop_after_seconds":0`} {
		raw := strings.TrimSuffix(base, "}") + "," + extra + "}"
		if err := tool.ValidateArgs(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted foreign/source/conflicting field: %s", extra)
		}
	}
	for _, raw := range []string{`null`, `{}`, `{"action":"list","source":"schedule","enabled":false}`, `{"action":"resume","source":"schedule","task_id":"t1","stop_after_seconds":600}`, `{"action":"status","source":"sentinel","task_id":"../other"}`, `{"action":"status","source":"schedule","task_id":"sentinel:other"}`, `{"action":"status","source":"sentinel","task_id":"schedule:other"}`, `{"action":"status","source":"schedule","task_id":"space id"}`, `{"action":"unknown","source":"schedule"}`, `{"action":"list","source":"http"}`, `{"action":"list","source":"schedule"} {}`} {
		if err := tool.ValidateArgs(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted invalid arguments: %s", raw)
		}
		if BackgroundTaskReadOnly(json.RawMessage(raw)) {
			t.Errorf("invalid arguments bypassed review: %s", raw)
		}
	}
	sentinel := `{"action":"create","source":"sentinel","title":"事件","task":"观察变化","script_path":"watchers/source/main.py","mode":"poll","interval_seconds":120}`
	if err := tool.ValidateArgs(json.RawMessage(sentinel)); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{`"kind":"every"`, `"timezone":"Etc/UTC"`, `"every_seconds":120`} {
		if err := tool.ValidateArgs(json.RawMessage(strings.TrimSuffix(sentinel, "}") + "," + extra + "}")); err == nil {
			t.Errorf("sentinel accepted schedule field %s", extra)
		}
	}
}

func TestBackgroundTaskPartialCreateErrorExplainsWholeSourceContract(t *testing.T) {
	backend := &fakeBackgroundTaskBackend{}
	tool := &BackgroundTask{Backend: backend}
	for _, tc := range []struct {
		name, raw string
		required  []string
		matching  string
		absent    []string
	}{
		{"schedule with only title", `{"action":"create","source":"schedule","title":"Count files"}`, []string{"task", "kind", "timezone", "matching time field"}, "every_seconds", []string{"at", "cron", "script_path", "mode"}},
		{"every missing interval", `{"action":"create","source":"schedule","kind":"every"}`, []string{"title", "task", "timezone", "every_seconds"}, "every_seconds", []string{"at", "cron"}},
		{"at missing timestamp", `{"action":"create","source":"schedule","kind":"at"}`, []string{"title", "task", "timezone", "at"}, "at", []string{"every_seconds", "cron"}},
		{"cron missing expression", `{"action":"create","source":"schedule","kind":"cron"}`, []string{"title", "task", "timezone", "cron"}, "cron", []string{"every_seconds", "at"}},
		{"sentinel missing source entry", `{"action":"create","source":"sentinel"}`, []string{"title", "task", "script_path", "mode"}, "script_path", []string{"every_seconds", "timezone", "kind"}},
		{"stream missing task", `{"action":"create","source":"sentinel","mode":"stream","script_path":"watchers/source.py"}`, []string{"title", "task"}, "script_path", []string{"interval_seconds", "every_seconds", "kind"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := backend.calls
			_, err := tool.Execute(context.Background(), json.RawMessage(tc.raw))
			if err == nil || backend.calls != before {
				t.Fatal("incomplete request reached backend", err)
			}
			message := err.Error()
			start, end := strings.Index(message, "{"), strings.LastIndex(message, "}")
			if start < 0 || end < start {
				t.Fatal("complete JSON shape missing", message)
			}
			missingEnd := strings.Index(message, ". Required:")
			if missingEnd < 0 {
				t.Fatal("aggregate required-field diagnostic missing", message)
			}
			for _, field := range tc.required {
				if !strings.Contains(message[:missingEnd], field) {
					t.Errorf("missing-field list omits %q: %s", field, message)
				}
			}
			var template map[string]any
			if err = json.Unmarshal([]byte(message[start:end+1]), &template); err != nil {
				t.Fatal("shape is not parseable JSON", err)
			}
			if template["action"] != "create" || template["task"] == nil || template["title"] == nil || template[tc.matching] == nil || template["enabled"] != false {
				t.Fatal("incomplete or activating template", template)
			}
			for _, field := range tc.absent {
				if _, exists := template[field]; exists {
					t.Errorf("shape contains mismatching source/time field %q", field)
				}
			}
			for _, authority := range []string{"owner_id", "agent_id", "workspace", "model", "approval_mode", "network"} {
				if _, exists := template[authority]; exists {
					t.Errorf("shape invents authority %q", authority)
				}
			}
			if !strings.Contains(message, "replace placeholders and example timing") || !strings.Contains(message, "No task was registered or enabled") {
				t.Fatal("template implied autofill or completed registration", message)
			}
			if BackgroundTaskReadOnly(json.RawMessage(tc.raw)) {
				t.Fatal("incomplete creation bypassed review")
			}
		})
	}
	schema, _ := json.Marshal(tool.InputSchema())
	if strings.Contains(string(schema), "oneOf") {
		t.Fatal("partial-error assistance changed provider schema portability")
	}
}

func TestBackgroundTaskReadOnlyAndRegistrationHandoff(t *testing.T) {
	backend := &fakeBackgroundTaskBackend{}
	tool := &BackgroundTask{Backend: backend}
	for _, action := range []string{"create", "resume"} {
		raw := `{"action":"resume","source":"schedule","task_id":"owned-task"}`
		if action == "create" {
			raw = `{"action":"create","source":"schedule","title":"统计","task":"保存结果","kind":"every","every_seconds":120,"timezone":"Etc/UTC"}`
		}
		result, err := tool.Execute(context.Background(), json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(result.Text, "Background delegation registered and paused") || !strings.Contains(result.Text, "Do not sleep") || !strings.Contains(result.Text, "not business completion") {
			t.Fatalf("misleading delegation receipt: %s", result.Text)
		}
		details := result.Details.(map[string]any)
		handoff := details["background_handoff"].(map[string]any)
		if handoff["enabled"] != false || handoff["registered"] != true || handoff["verification"] != "registration_only" || handoff["task_id"] != "owned-task" {
			t.Fatalf("handoff=%v", handoff)
		}
		if BackgroundTaskReadOnly(json.RawMessage(raw)) {
			t.Fatal("mutation bypassed review")
		}
	}
	for _, raw := range []string{`{"action":"list","source":"schedule"}`, `{"action":"status","source":"sentinel","task_id":"owned-task"}`} {
		if !BackgroundTaskReadOnly(json.RawMessage(raw)) {
			t.Fatal("valid inspection incorrectly requires review", raw)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := backend.calls
	if _, err := tool.Execute(ctx, json.RawMessage(`{"action":"list","source":"schedule"}`)); !errors.Is(err, context.Canceled) || backend.calls != before {
		t.Fatal("cancelled request executed", err)
	}
}

func TestBackgroundTaskRegistryExplicitWatchGrantDoesNotGrantSchedules(t *testing.T) {
	backend := &fakeBackgroundTaskBackend{}
	watchOnly := []string{WatchName, WatchTestName}
	r := New(Options{Cwd: t.TempDir(), Enabled: &watchOnly, Watches: &fakeWatchBackend{}, BackgroundTasks: backend})
	if _, ok := r.Get(BackgroundTaskName); ok {
		t.Fatal("watch selection escalated to scheduled execution")
	}
	original, ok := r.HostNativeTool(BackgroundTaskName)
	if !ok {
		t.Fatal("host template missing")
	}
	selected := []string{BackgroundTaskName}
	next, commit, err := r.WithNativeSelection(&selected, nil)
	if err != nil {
		t.Fatal(err)
	}
	commit()
	if tool, ok := next.Get(BackgroundTaskName); !ok || tool != original {
		t.Fatal("selected trusted background tool missing or replaced")
	}
	empty := []string{}
	next, commit, err = next.WithNativeSelection(&empty, nil)
	if err != nil {
		t.Fatal(err)
	}
	commit()
	if _, ok := next.Get(BackgroundTaskName); ok {
		t.Fatal("revoked background grant remains")
	}
	for _, options := range []Options{{Cwd: t.TempDir(), ReadOnly: true, BackgroundTasks: backend}, {Cwd: t.TempDir(), Enabled: &selected}} {
		if _, ok := New(options).Get(BackgroundTaskName); ok {
			t.Fatal("invented unavailable capability")
		}
	}
}

func TestBackgroundTaskRegistryRejectsMCPAndSkillImpersonation(t *testing.T) {
	projections := []Tool{&MCPTool{wireName: BackgroundTaskName}}
	empty := []string{}
	r := New(Options{Cwd: t.TempDir(), Enabled: &empty, Synth: []Tool{nameStub{name: BackgroundTaskName}}, MCP: projections}).WithMCP(projections)
	if _, ok := r.Get(BackgroundTaskName); ok {
		t.Fatal("external projection impersonated withheld host tool")
	}
	backend := &fakeBackgroundTaskBackend{}
	r = New(Options{Cwd: t.TempDir(), BackgroundTasks: backend, MCP: projections})
	original, _ := r.Get(BackgroundTaskName)
	next := r.WithMCP(projections)
	if got, ok := next.Get(BackgroundTaskName); !ok || got != original {
		t.Fatal("MCP refresh replaced host capability")
	}
	next, commit, err := next.WithSkillContext(SkillContextUpdate{Cwd: t.TempDir(), Tools: []Tool{nameStub{name: BackgroundTaskName}}})
	if err != nil {
		t.Fatal(err)
	}
	commit()
	if got, ok := next.Get(BackgroundTaskName); !ok || got != original {
		t.Fatal("Skill refresh replaced host capability")
	}
}
