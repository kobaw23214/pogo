package main

import (
	"context"
	"sync"

	"github.com/yosukeno/pogo/llm"
)

// This is accounting around the existing client, not another agent loop. Stop
// before executing tool calls from a response that has exhausted the allowance.
// Provider-reported token/cost budgets can overshoot by the last model call;
// the wall-clock context remains a hard interrupt during that call.
type measuredClient struct {
	client      llm.Client
	tokenBudget int64
	costBudget  float64
	price       *llm.Price
	mu          sync.Mutex
	usage       llm.Usage
	measured    bool
}

func (c *measuredClient) Model() string { return c.client.Model() }
func (c *measuredClient) Stream(ctx context.Context, system string, history []llm.Message, schemas []llm.ToolSchema, delta func(llm.Delta)) (llm.Response, error) {
	response, err := c.client.Stream(ctx, system, history, schemas, delta)
	c.mu.Lock()
	defer c.mu.Unlock()
	if response.Usage.Input+response.Usage.Output > 0 {
		c.measured = true
		c.usage.Input += response.Usage.Input
		c.usage.Output += response.Usage.Output
		c.usage.CacheRead += response.Usage.CacheRead
		c.usage.Reasoning += response.Usage.Reasoning
	}
	if err == nil && ((c.tokenBudget > 0 && c.usage.Input+c.usage.Output >= c.tokenBudget) || (c.costBudget > 0 && c.price != nil && c.price.Cost(c.usage) >= c.costBudget)) {
		response.StopReason = llm.StopAborted
	}
	return response, err
}
func (c *measuredClient) totals() (llm.Usage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.usage, c.measured
}
