package web

import (
	"context"
	"testing"

	"github.com/yosukeno/pogo/llm"
)

func TestWebContinuesOutputLimitWithinTheSameRun(t *testing.T) {
	limited := llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
		{Type: llm.BlockThinking, Text: "Planning was cut off before an answer."},
	}}, StopReason: llm.StopMaxTokens, Usage: llm.Usage{Input: 10, Output: 20}}
	h := newHarness(t, scriptedTurns(limited, textTurn("Work completed.")))
	sid := h.createSession()
	s := h.start(sid, "Complete the authorized task.")
	defer s.close()
	starts := 0
	for {
		e := s.next(t)
		if e.Type == EvRunStart {
			starts++
		}
		if e.Type == EvRunEnd {
			if e.EndReason != "completed" || h.calls() != 2 || starts != 1 {
				t.Fatalf("continuation replaced or failed the run: starts=%d calls=%d end=%+v", starts, h.calls(), e)
			}
			break
		}
	}
	if got := h.session(sid).Hub().Snapshot().Run.Active; got {
		t.Fatal("completed continuation left the session active")
	}
}

func TestWebStopsAfterOneOutputLimitContinuation(t *testing.T) {
	limited := textTurn("Partial output")
	limited.StopReason = llm.StopMaxTokens
	h := newHarness(t, scriptedTurns(limited))
	sid := h.createSession()
	s := h.start(sid, "Complete the authorized task.")
	defer s.close()
	end := s.wait(t, EvRunEnd)
	if end.EndReason != "max_tokens" || h.calls() != 2 {
		t.Fatalf("unbounded or incorrectly completed continuation: calls=%d end=%+v", h.calls(), end)
	}
}

func TestWebDoesNotAutomaticallyResumeModelTransportTimeout(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return llm.Response{}, context.DeadlineExceeded })
	sid := h.createSession()
	s := h.start(sid, "Complete the authorized task.")
	defer s.close()
	end := s.wait(t, EvRunEnd)
	if end.EndReason != "transport_error" || end.Error == "" || h.calls() != 1 {
		t.Fatalf("transport failure was replayed or hidden: calls=%d end=%+v", h.calls(), end)
	}
}
