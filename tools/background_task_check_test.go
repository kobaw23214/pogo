package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestBackgroundTaskPreparationAndCheckUseBoundReviewedCapability(t *testing.T) {
	backend := &fakeBackgroundTaskBackend{}
	tool := &BackgroundTask{Backend: backend}
	for _, raw := range []string{
		`{"action":"prepare","source":"sentinel","script_path":"watchers/source/main.py","mode":"poll"}`,
		`{"action":"prepare","source":"sentinel","script_path":"watchers/source/main.py","mode":"stream"}`,
		`{"action":"check","source":"sentinel","script_path":"watchers/source/main.py","mode":"poll","args":["--read-current"],"network":true,"timeout_seconds":30}`,
		`{"action":"check","source":"sentinel","script_path":"watchers/source/main.sh","mode":"stream"}`,
		`{"action":"check","source":"sentinel","task_id":"sentinel:owned-watch","timeout_seconds":10}`,
		`{"action":"check","source":"sentinel","task_id":"owned-watch"}`,
	} {
		before := backend.calls
		result, err := tool.Execute(context.Background(), json.RawMessage(raw))
		if err != nil || backend.calls != before+1 {
			t.Fatalf("valid capability request rejected: %s: %v", raw, err)
		}
		if BackgroundTaskReadOnly(json.RawMessage(raw)) || tool.ExecutionMode() != Sequential {
			t.Fatal("script preparation/trial must stay on the reviewed execution boundary")
		}
		if _, ok := result.Details.(map[string]any)["background_handoff"]; ok {
			t.Fatal("preparation or trial claimed a registered background delegation")
		}
	}
}

func TestBackgroundTaskCheckRejectsSourceOverridesAndAuthorityBeforeBackend(t *testing.T) {
	backend := &fakeBackgroundTaskBackend{}
	tool := &BackgroundTask{Backend: backend}
	for _, raw := range []string{
		`{"action":"prepare","source":"schedule","script_path":"watchers/main.py","mode":"poll"}`,
		`{"action":"check","source":"schedule","task_id":"job"}`,
		`{"action":"prepare","source":"sentinel","script_path":"main.py","mode":"poll"}`,
		`{"action":"prepare","source":"sentinel","script_path":"watchers/main.py","mode":"poll","enabled":true}`,
		`{"action":"prepare","source":"sentinel","script_path":"watchers/main.py","mode":"poll","network":false}`,
		`{"action":"check","source":"sentinel"}`,
		`{"action":"check","source":"sentinel","task_id":""}`,
		`{"action":"check","source":"sentinel","task_id":"schedule:job"}`,
		`{"action":"check","source":"sentinel","task_id":"sentinel:../../other"}`,
		`{"action":"check","source":"sentinel","task_id":"owned","script_path":"watchers/main.py","mode":"poll"}`,
		`{"action":"check","source":"sentinel","task_id":"owned","args":[]}`,
		`{"action":"check","source":"sentinel","task_id":"owned","network":false}`,
		`{"action":"check","source":"sentinel","script_path":"watchers/main.py","mode":"poll","timeout_seconds":31}`,
		`{"action":"check","source":"sentinel","script_path":"../main.py","mode":"poll"}`,
		`{"action":"check","source":"sentinel","script_path":"watchers/main.py","mode":"poll","stop_after_seconds":600}`,
		`{"action":"check","source":"sentinel","script_path":"watchers/main.py","mode":"poll","enabled":false}`,
		`{"action":"check","source":"sentinel","script_path":"watchers/main.py","mode":"poll","timeout_seconds":null}`,
		`{"action":"check","source":"sentinel","script_path":"watchers/main.py","mode":"poll","capability":"watch_test"}`,
		`{"action":"check","source":"sentinel","script_path":"watchers/main.py","mode":"poll","workspace":"/private"}`,
	} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Errorf("invalid preparation/trial accepted: %s", raw)
		}
		if BackgroundTaskReadOnly(json.RawMessage(raw)) {
			t.Errorf("invalid request bypassed review: %s", raw)
		}
	}
	if backend.calls != 0 {
		t.Fatal("invalid request reached the host backend")
	}
}
