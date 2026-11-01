package web

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/tools"
)

func TestBackgroundTaskSentinelRegistrationCannotRaiseHostTurnBudget(t *testing.T) {
	for _, route := range []string{"watch", "background_task"} {
		for _, tc := range []struct {
			name               string
			host, caller, want int
		}{
			{"host below default", 5, 0, 5},
			{"caller attempts raise", 5, 300, 5},
			{"caller narrows", 5, 2, 2},
			{"finite default", 300, 0, 32},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				h, cap := newSentinelHarness(t, nil)
				h.mgr.SetMaxTurnsOverride(tc.host)
				s := sentinelTestSession(t, h)
				script := sentinelTestBundle(t, s)
				body := map[string]any{"action": "create", "title": "bounded turns", "task": "investigate one source observation", "mode": "poll", "script_path": script}
				if tc.caller > 0 {
					body["max_turns"] = tc.caller
				}
				var id string
				if route == "watch" {
					result, err := (&tools.Watch{Backend: h.mgr.sentinelBackend(s)}).Execute(context.Background(), json.RawMessage(studioBody(body)))
					if err != nil {
						t.Fatal(err)
					}
					var w sentinel.Watch
					if err = json.Unmarshal([]byte(result.Text), &w); err != nil {
						t.Fatal(err)
					}
					id = w.Spec.ID
				} else {
					body["source"] = "sentinel"
					id = backgroundTestRequest(t, h.mgr.backgroundTaskBackend(s), body).SourceID
				}
				db, err := h.mgr.sentinels.database(false)
				if err != nil {
					t.Fatal(err)
				}
				w, err := db.Get(id)
				if err != nil {
					t.Fatal(err)
				}
				if w.Spec.MaxTurns != tc.want {
					t.Fatalf("registered limit=%d want%d host%d caller%d", w.Spec.MaxTurns, tc.want, tc.host, tc.caller)
				}
				if h.mgr.effectiveMaxTurns() != tc.host || sentinelModelCalls(cap) != 0 {
					t.Fatal("registration changed host configuration or invoked model")
				}
				// A later setting change affects new registration, not this record.
				h.mgr.SetMaxTurnsOverride(1)
				old, err := db.Get(id)
				if err != nil || old.Spec.MaxTurns != tc.want {
					t.Fatal("saved task was silently rewritten", old, err)
				}
			})
		}
	}
}
