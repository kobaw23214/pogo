package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestCompactionRequestProfileIsPurposeAndModelSpecific(t *testing.T) {
	for _, test := range []struct {
		name, model string
		extra       map[string]any
		purpose     RequestPurpose
		applied     bool
		reason      string
	}{
		{name: "documented GLM default", model: "glm-5.3", purpose: PurposeCompaction, applied: true, reason: "compaction_reasoning_default"},
		{name: "explicit thinking enabled", model: "glm-5.3", extra: map[string]any{"thinking": map[string]any{"type": "enabled"}}, purpose: PurposeCompaction, applied: true, reason: "compaction_reasoning_default"},
		{name: "ordinary task unchanged", model: "glm-5.3"},
		{name: "explicit effort wins", model: "glm-5.3", extra: map[string]any{"reasoning_effort": "max"}, purpose: PurposeCompaction, reason: "explicit_reasoning_effort_preserved"},
		{name: "explicit null effort wins", model: "glm-5.3", extra: map[string]any{"reasoning_effort": nil}, purpose: PurposeCompaction, reason: "explicit_reasoning_effort_preserved"},
		{name: "disabled thinking wins", model: "glm-5.3", extra: map[string]any{"thinking": map[string]any{"type": "disabled"}}, purpose: PurposeCompaction, reason: "explicit_thinking_preserved"},
		{name: "unfamiliar mode preserved", model: "glm-5.3", extra: map[string]any{"thinking": "enabled"}, purpose: PurposeCompaction, reason: "explicit_thinking_preserved"},
		{name: "explicit null thinking preserved", model: "glm-5.3", extra: map[string]any{"thinking": nil}, purpose: PurposeCompaction, reason: "explicit_thinking_preserved"},
		{name: "Kimi defaults unchanged", model: "k3", purpose: PurposeCompaction, reason: "model_defaults_preserved"},
		{name: "other GLM unchanged", model: "glm-5.2", purpose: PurposeCompaction, reason: "model_defaults_preserved"},
		{name: "other purpose unchanged", model: "glm-5.3", purpose: RequestPurpose("other"), reason: "unsupported_purpose"},
	} {
		t.Run(test.name, func(t *testing.T) {
			original, _ := json.Marshal(test.extra)
			var bodies []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				bodies = append(bodies, body)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, okStream)
			}))
			defer server.Close()
			client := New(Options{BaseURL: server.URL, Model: test.model, ExtraBody: test.extra, MaxTokens: 16384, MaxRetries: -1, Timeout: time.Second})
			ctx := context.Background()
			if test.purpose != "" {
				ctx = WithRequestPurpose(ctx, test.purpose)
			}
			out, err := client.Stream(ctx, "same system", []Message{UserText("same history")}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if test.purpose == "" {
				if out.RequestProfile != nil {
					t.Fatalf("ordinary call acquired profile: %+v", out.RequestProfile)
				}
			} else if out.RequestProfile == nil || out.RequestProfile.Purpose != test.purpose || out.RequestProfile.Applied != test.applied || out.RequestProfile.Reason != test.reason {
				t.Fatalf("profile = %+v", out.RequestProfile)
			}
			if got := bodies[0]["max_tokens"]; got != float64(16384) {
				t.Fatalf("output cap changed: %v", got)
			}
			if test.applied && bodies[0]["reasoning_effort"] != "low" {
				t.Fatalf("missing low effort: %v", bodies[0]["reasoning_effort"])
			}
			if _, hadThinking := test.extra["thinking"]; !hadThinking {
				if _, sentThinking := bodies[0]["thinking"]; sentThinking {
					t.Fatal("profile added a thinking mode switch")
				}
			}
			// The shared client is immediately reused for an ordinary task. A
			// purpose-specific default must not mutate shared provider options.
			if _, err := client.Stream(context.Background(), "same system", []Message{UserText("same history")}, nil, nil); err != nil {
				t.Fatal(err)
			}
			for key, value := range test.extra {
				if !reflect.DeepEqual(bodies[1][key], value) {
					t.Fatalf("ordinary provider field %q changed: got %v want %v", key, bodies[1][key], value)
				}
			}
			if _, explicit := test.extra["reasoning_effort"]; !explicit {
				if _, leaked := bodies[1]["reasoning_effort"]; leaked {
					t.Fatal("compaction effort leaked into the next ordinary call")
				}
			}
			unchanged, _ := json.Marshal(test.extra)
			if string(original) != string(unchanged) {
				t.Fatal("shared extra_body was mutated")
			}
		})
	}
}

func TestRequestPurposePreservesCancellationAndDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	ctx := WithRequestPurpose(parent, PurposeCompaction)
	parentDeadline, _ := parent.Deadline()
	deadline, ok := ctx.Deadline()
	if !ok || deadline != parentDeadline || RequestPurposeFromContext(ctx) != PurposeCompaction {
		t.Fatal("purpose changed the caller's deadline or was lost")
	}
	cancel()
	if ctx.Err() != context.Canceled {
		t.Fatalf("caller cancellation was lost: %v", ctx.Err())
	}
}
