package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestWatchPrepareValidatesBeforeWritingAndRequiresReview(t *testing.T) {
	backend := &fakeWatchBackend{}
	tool := &Watch{Backend: backend}
	valid := json.RawMessage(`{"action":"prepare","script_path":"watchers/custom/main.py","mode":"stream"}`)
	if _, err := tool.Execute(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	if WatchReadOnly(valid) || tool.ExecutionModeForArgs(valid) != Sequential {
		t.Fatal("workspace file preparation must remain a reviewed mutation")
	}
	for _, raw := range []string{
		`{"action":"prepare","script_path":"main.py","mode":"poll"}`,
		`{"action":"prepare","script_path":"watchers/pogo_watch.py","mode":"poll"}`,
		`{"action":"prepare","script_path":"watchers/POGO_WATCH.PY","mode":"poll"}`,
		`{"action":"prepare","script_path":"watchers/main.sh","mode":"poll"}`,
		`{"action":"prepare","script_path":"../main.py","mode":"poll"}`,
		`{"action":"prepare","script_path":"watchers/main.py","mode":"rss"}`,
		`{"action":"prepare","script_path":"watchers/main.py","mode":"poll","enabled":true}`,
		`{"action":"prepare","script_path":"watchers/main.py","mode":"poll","network":false}`,
		`{"action":"prepare","script_path":"watchers/main.py","mode":"poll","args":[]}`,
		`{"action":"prepare","script_path":"watchers/main.py","mode":"poll","watch_id":"other"}`,
		`{"action":"prepare","script_path":"watchers/main.py","mode":"poll","owner_id":"other"}`,
	} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Errorf("invalid preparation reached backend: %s", raw)
		}
	}
	if len(backend.calls) != 1 {
		t.Fatalf("invalid calls reached the writer: %v", backend.calls)
	}
}
