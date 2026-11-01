package agent

import (
	"context"
	"fmt"

	"github.com/yosukeno/pogo/llm"
)

// ContinuationConfig permits a bounded completion after a provider output cap.
// It never changes the model configuration or starts a fresh run. The zero
// value is disabled, so embedding callers retain their existing policy.
type ContinuationConfig struct {
	// MaxAttempts counts additional model requests after max_tokens. Values
	// above one are capped at one; transport failures are not covered here.
	MaxAttempts int
}

func (c ContinuationConfig) normalized() ContinuationConfig {
	if c.MaxAttempts < 0 {
		c.MaxAttempts = 0
	}
	if c.MaxAttempts > 1 {
		c.MaxAttempts = 1
	}
	return c
}

type outputContinuation struct {
	limit int
	used  int
}

// continueOutput is called only after the truncated assistant response (and any
// rejected tool-call results) has settled. A returned terminal event uses the
// ordinary run limit, rather than announcing a continuation that cannot happen.
// No response or tool is replayed: the next request sees the current transcript.
func (a *Agent) continueOutput(ctx context.Context, turn int, state *outputContinuation, out chan<- Event) (bool, *Event) {
	if ctx.Err() != nil {
		return false, &Event{Kind: EventAgentEnd, EndReason: EndAborted, StopReason: llm.StopAborted, Usage: a.usage}
	}
	if state.used >= state.limit {
		return false, nil
	}
	if reason, detail := a.checkBudgets(); reason != "" {
		return false, &Event{Kind: EventAgentEnd, EndReason: reason, Usage: a.usage, Err: fmt.Errorf("budget exceeded: %s", detail)}
	}
	if !a.hasTurnCapacity(turn + 1) {
		limit := a.effectiveTurnLimit()
		return false, &Event{Kind: EventAgentEnd, EndReason: EndTurnLimit, Usage: a.usage,
			Err: fmt.Errorf("stopped after %d turns without a final answer", limit)}
	}
	state.used++
	notice := fmt.Sprintf("[pogo] 上一段输出达到本次上限，正在补全剩余内容（%d/%d）。沿用已有结果，不要重做已完成的工具操作；直接完成下一项必要操作或给出完整答复。", state.used, state.limit)
	a.messages = append(a.messages, llm.UserText(notice))
	a.emit(ctx, out, Event{Kind: EventSteer, Text: notice})
	return true, nil
}
