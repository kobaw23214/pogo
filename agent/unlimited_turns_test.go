package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func TestExplicitUnlimitedTurnsPassesDefaultCapAndFinishes(t *testing.T) {
	responses := make([]llm.Response, DefaultMaxTurns+41)
	for i := range responses {
		responses[i] = toolCalls("step")
	}
	client := &fakeClient{responses: responses}
	a := New(Config{Client: client, Registry: tools.NewRegistry(&fakeTool{name: "step"}), SoftTurns: 100})
	a.SetUnlimitedTurns()
	end := endOf(t, drain(a.Run(context.Background(), "complete all steps")))
	if end.EndReason != EndCompleted || end.Err != nil || client.callCount() != len(responses)+1 {
		t.Fatalf("unlimited run stopped at %d calls: %+v", client.callCount(), end)
	}
	if notice, ok := a.softCheckpoint(401); !ok || !strings.Contains(notice, "no hard turn limit") || strings.Contains(notice, "hard cap -1") {
		t.Fatalf("invalid unlimited checkpoint %q", notice)
	}
}

func TestExplicitUnlimitedTurnsStillCancelsPastDefaultCap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	responses := make([]llm.Response, DefaultMaxTurns+40)
	for i := range responses {
		responses[i] = toolCalls("step")
	}
	client := &fakeClient{responses: responses, onStream: func(n int) {
		if n == DefaultMaxTurns+10 {
			cancel()
		}
	}}
	a := New(Config{Client: client, Registry: tools.NewRegistry(&fakeTool{name: "step"})})
	a.SetUnlimitedTurns()
	end := endOf(t, drain(a.Run(ctx, "continue until cancelled")))
	if end.EndReason != EndAborted || client.callCount() != DefaultMaxTurns+10 {
		t.Fatalf("cancellation not retained: calls=%d end=%+v", client.callCount(), end)
	}
}

func TestUnlimitedIsExplicitAndFiniteSetterRestoresCap(t *testing.T) {
	a := New(Config{MaxTurns: 0, Client: &fakeClient{}, Registry: tools.NewRegistry()})
	if a.maxTurns.Load() != DefaultMaxTurns {
		t.Fatal("ordinary zero changed meaning")
	}
	a = New(Config{MaxTurns: -1, Client: &fakeClient{}, Registry: tools.NewRegistry()})
	if a.maxTurns.Load() != DefaultMaxTurns {
		t.Fatal("ordinary negative enabled unlimited")
	}
	client := &fakeClient{responses: []llm.Response{toolCalls("step"), toolCalls("step"), toolCalls("step"), toolCalls("step")}}
	a = New(Config{Client: client, Registry: tools.NewRegistry(&fakeTool{name: "step"})})
	a.SetUnlimitedTurns()
	a.SetMaxTurns(3)
	if a.effectiveTurnLimit() != -1 {
		t.Fatal("default preference replaced explicit unlimited mode")
	}
	a.SetTurnLimitOverride(0)
	end := endOf(t, drain(a.Run(context.Background(), "finite again")))
	if end.EndReason != EndTurnLimit || client.callCount() != 3 {
		t.Fatalf("finite setter ignored: %+v", end)
	}
}

func TestExplicitSessionTurnLimitSurvivesDefaultUpdates(t *testing.T) {
	a := New(Config{MaxTurns: 300, Client: &fakeClient{}, Registry: tools.NewRegistry()})
	a.SetTurnLimitOverride(75)
	a.SetMaxTurns(2)
	if a.effectiveTurnLimit() != 75 {
		t.Fatal("finite task limit followed global default")
	}
	a.SetUnlimitedTurns()
	a.SetMaxTurns(300)
	if a.effectiveTurnLimit() != -1 {
		t.Fatal("unlimited task followed global default")
	}
	a.SetTurnLimitOverride(-2)
	if a.effectiveTurnLimit() != -1 {
		t.Fatal("unsupported sentinel changed override")
	}
	a.SetTurnLimitOverride(0)
	if a.effectiveTurnLimit() != 300 {
		t.Fatal("clearing override did not restore latest ordinary default")
	}
}
