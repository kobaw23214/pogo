package main

import (
	"testing"

	"github.com/yosukeno/pogo/agent"
)

// The retry decision the two drivers share (see run's consumeOverflowed). Each
// line is one way the answer is no, because every false here is a behaviour
// guarantee: an unarmed -compact never gains a retry, a tripped breaker never
// loops, and nothing but an overflow re-sends a prompt.
func TestOverflowRetryable(t *testing.T) {
	const circuit = 3
	cases := []struct {
		name    string
		lastEnd agent.EndReason
		armed   bool
		fails   int
		want    bool
	}{
		{"overflow, armed, healthy breaker", agent.EndContextOverflow, true, 0, true},
		{"overflow, armed, breaker one short", agent.EndContextOverflow, true, circuit - 1, true},
		{"overflow, unarmed", agent.EndContextOverflow, false, 0, false},
		{"overflow, tripped breaker", agent.EndContextOverflow, true, circuit, false},
		{"transport error never retries", agent.EndTransportError, true, 0, false},
		{"a completed run never retries", agent.EndCompleted, true, 0, false},
		{"a turn cap is the chain's business", agent.EndTurnLimit, true, 0, false},
	}
	for _, tc := range cases {
		if got := overflowRetryable(tc.lastEnd, tc.armed, tc.fails, circuit); got != tc.want {
			t.Errorf("%s: overflowRetryable = %v, want %v", tc.name, got, tc.want)
		}
	}
}
