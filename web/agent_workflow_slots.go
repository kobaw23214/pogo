package web

import "context"

type awExecutionSlotKey struct{}

// Nested batch work shares the same limit as ordinary graph execution. Ten
// outer containers must never silently become one hundred executing Agents.
func awExecutionSlotsContext(ctx context.Context) context.Context {
	if _, ok := ctx.Value(awExecutionSlotKey{}).(chan struct{}); ok {
		return ctx
	}
	return context.WithValue(ctx, awExecutionSlotKey{}, make(chan struct{}, awGraphConcurrency))
}

func awAcquireExecutionSlot(ctx context.Context) (func(), error) {
	slots, ok := ctx.Value(awExecutionSlotKey{}).(chan struct{})
	if !ok {
		return func() {}, ctx.Err()
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
