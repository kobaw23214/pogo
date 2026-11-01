package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestResolveTaskDeadlineIsHostAnchored(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 123000000, time.UTC)
	got, err := ResolveTaskDeadline("", 600, now)
	if err != nil || got != now.Add(10*time.Minute).UnixMilli() {
		t.Fatalf("relative deadline=%d %v", got, err)
	}
	got, err = ResolveTaskDeadline("2026-10-26T20:10:00.123+08:00", 0, now)
	if err != nil || got != now.Add(10*time.Minute).UnixMilli() {
		t.Fatalf("absolute normalization=%d %v", got, err)
	}
	if got, err = ResolveTaskDeadline("", 0, now); err != nil || got != 0 {
		t.Fatalf("legacy lifetime=%d %v", got, err)
	}
	for _, tc := range []struct {
		at      string
		seconds int
	}{
		{"invalid", 0}, {"2026-10-26T12:00:00.123Z", 0}, {"2026-10-26T11:59:59Z", 0},
		{"2026-10-26T12:10:00Z", 600}, {"", -1}, {"", 31536001},
	} {
		if _, err = ResolveTaskDeadline(tc.at, tc.seconds, now); err == nil {
			t.Fatalf("invalid deadline accepted: %+v", tc)
		}
	}
}

func TestWatchDeadlineArgumentsAreCreateOnlyAndValidatedBeforeBackend(t *testing.T) {
	base := `"action":"create","title":"bounded","task":"observe","mode":"poll","script_path":"watchers/main.py"`
	backend := &fakeWatchBackend{}
	tool := &Watch{Backend: backend}
	for _, fields := range []string{`"stop_after_seconds":0`, `"stop_after_seconds":-1`, `"stop_after_seconds":31536001`, `"stop_after_seconds":1.5`, `"expires_at":""`, `"expires_at":"bad"`, `"expires_at":"2000-01-01T00:00:00Z"`, `"expires_at":"2100-01-01T00:00:00Z","stop_after_seconds":600`} {
		if _, err := tool.Execute(context.Background(), json.RawMessage("{"+base+","+fields+"}")); err == nil {
			t.Fatalf("accepted %s", fields)
		}
	}
	for _, raw := range []string{`{"action":"resume","watch_id":"w1","stop_after_seconds":600}`, `{"action":"prepare","mode":"poll","script_path":"watchers/main.py","expires_at":"2100-01-01T00:00:00Z"}`} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("unexpected lifetime rewrite: %s", raw)
		}
	}
	if len(backend.calls) != 0 {
		t.Fatal("invalid lifetime reached backend")
	}
	for _, field := range []string{`"stop_after_seconds":600`, `"expires_at":"2100-01-01T00:00:00Z"`} {
		if _, err := tool.Execute(context.Background(), json.RawMessage("{"+base+","+field+"}")); err != nil {
			t.Fatal(err)
		}
	}
}

type handoffWatchBackend struct {
	fakeWatchBackend
	value any
}

func (b *handoffWatchBackend) Watch(context.Context, json.RawMessage) (any, error) {
	return b.value, nil
}

func TestWatchHandoffIsFirstAndDoesNotClaimCollectionSucceeded(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		value := map[string]any{"spec": map[string]any{"id": "w1", "enabled": enabled, "expires_at": int64(9000), "task": strings.Repeat("standing goal ", 100)}, "state": nil, "status": "starting"}
		backend := &handoffWatchBackend{value: value}
		result, err := (&Watch{Backend: backend}).Execute(context.Background(), json.RawMessage(`{"action":"create","title":"task","task":"observe","script_path":"watchers/main.py","mode":"poll"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(result.Text, `{"notice":`) {
			t.Fatal("handoff was buried behind the full specification")
		}
		var parsed struct {
			Spec struct {
				ID string `json:"id"`
			} `json:"spec"`
			Notice  string `json:"notice"`
			Handoff struct {
				Registered   bool   `json:"registered"`
				Enabled      bool   `json:"enabled"`
				TaskID       string `json:"task_id"`
				Verification string `json:"verification"`
				ExpiresUTC   string `json:"expires_at_utc"`
			} `json:"background_handoff"`
		}
		if err = json.Unmarshal([]byte(result.Text), &parsed); err != nil || parsed.Spec.ID != "w1" {
			t.Fatalf("legacy Watch JSON shape lost: %v", err)
		}
		if !parsed.Handoff.Registered || parsed.Handoff.Enabled != enabled || parsed.Handoff.TaskID != "sentinel:w1" || parsed.Handoff.Verification != "registration_only" {
			t.Fatalf("invalid receipt %+v", parsed.Handoff)
		}
		if !strings.Contains(parsed.Notice, "注册") || !strings.Contains(parsed.Notice, "不要sleep") {
			t.Fatal("missing explicit foreground handoff")
		}
		deadline := time.UnixMilli(9000).UTC().Format(time.RFC3339Nano)
		if parsed.Handoff.ExpiresUTC != deadline || !strings.Contains(parsed.Notice, deadline) {
			t.Fatal("receipt must expose the exact host deadline without model timestamp arithmetic")
		}
		details := result.Details.(map[string]any)
		if details["background_handoff"] == nil || details["result"] == nil || details["type"] != "watch" {
			t.Fatalf("details lost native result: %+v", details)
		}
	}
}
