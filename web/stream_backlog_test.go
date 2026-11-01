package web

import (
	"strconv"
	"testing"
)

func TestStreamBacklogBoundarySeparatesReplayFromLiveEvents(t *testing.T) {
	for _, mode := range []string{"snapshot", "replay", "current"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, scriptedTurns(textTurn("unused")))
			sid := h.createSession()
			hub := h.session(sid).Hub()
			hub.Publish(Event{Type: EvRunStart, RunID: "historical-run"})
			first := hub.Seq()
			hub.Publish(Event{Type: EvToolStart, Name: "browser", CallID: "historical-call"})
			hub.Publish(Event{Type: EvRunEnd})
			cutoff := hub.Seq()
			from := ""
			if mode == "replay" {
				from = strconv.FormatInt(first, 10)
			} else if mode == "current" {
				from = strconv.FormatInt(cutoff, 10)
			}
			stream := h.stream(sid, from, h.session(sid).hub.instanceID)
			defer stream.close()
			if got := stream.resp.Header.Get("X-Pogo-Stream-Backlog-Through"); got != strconv.FormatInt(cutoff, 10) {
				t.Fatalf("backlog cutoff = %q, want %d", got, cutoff)
			}
			if mode == "snapshot" {
				if e := stream.next(t); e.Type != EvSnapshot || e.Seq != cutoff {
					t.Fatalf("initial snapshot = %#v", e)
				}
			} else if mode == "replay" {
				for _, want := range []EventType{EvToolStart, EvRunEnd} {
					if e := stream.next(t); e.Type != want || e.Seq > cutoff {
						t.Fatalf("replayed event = %#v, want %s at or before %d", e, want, cutoff)
					}
				}
			}
			// A publication after subscription is live even if it happens before
			// the client has drained the backlog bytes from its socket.
			hub.Publish(Event{Type: EvRunStart, RunID: "live-run"})
			if e := stream.next(t); e.Type != EvRunStart || e.RunID != "live-run" || e.Seq <= cutoff {
				t.Fatalf("live event = %#v, must follow boundary %d", e, cutoff)
			}
		})
	}
}
