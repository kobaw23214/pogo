// Test program to verify RFC B1 overhead metrics implementation
package main

import (
	"context"
	"fmt"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// mockClient is a simple test client that returns a fixed response
type mockClient struct{}

func (m *mockClient) Model() string { return "test-model" }

func (m *mockClient) Stream(
	ctx context.Context,
	systemPrompt string,
	history []llm.Message,
	tools []llm.ToolSchema,
	onDelta func(llm.Delta),
) (llm.Response, error) {
	// Simulate a response with some token usage
	return llm.Response{
		Message: llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.Block{{Type: llm.BlockText, Text: "Test response"}},
		},
		Usage: llm.Usage{
			Input:  2000, // Simulate input tokens
			Output: 100,  // Simulate output tokens
		},
		StopReason: llm.StopEndTurn,
	}, nil
}

func main() {
	fmt.Println("RFC B1 Overhead Metrics Test")
	fmt.Println("============================")

	// Create a tool registry
	registry := tools.NewRegistry(
		&tools.Bash{},
		&tools.Edit{},
		&tools.Find{},
		&tools.Grep{},
		&tools.Ls{},
		&tools.Read{},
		&tools.Write{},
	)

	systemPrompt := agent.SystemPrompt("/tmp/test")

	// Create an agent with the mock client
	agent := agent.New(agent.Config{
		Client:       &mockClient{},
		Registry:     registry,
		SystemPrompt: systemPrompt,
		MaxTurns:     1,
	})

	// Run a simple prompt
	ctx := context.Background()
	eventChan := agent.Run(ctx, "test prompt")

	// Collect events
	var overheadFound bool
	for event := range eventChan {
		if event.OverheadMetrics != nil {
			overheadFound = true
			fmt.Printf("\nOverhead Metrics Found:\n")
			fmt.Printf("  Fixed Cost Tokens:    %d\n", event.OverheadMetrics.FixedCostTokens)
			fmt.Printf("  Total Input Tokens:    %d\n", event.OverheadMetrics.TotalInputTokens)
			fmt.Printf("  Overhead Ratio:       %.2f%%\n", event.OverheadMetrics.OverheadRatio*100)
			fmt.Printf("  Event Type:           %s\n", event.Kind)
		}

		if string(event.Kind) == "agent_end" {
			fmt.Printf("\nAgent run completed.\n")
			if event.OverheadMetrics != nil {
				fmt.Printf("  Final Overhead Ratio: %.2f%%\n", event.OverheadMetrics.OverheadRatio*100)
			}
		}
	}

	if !overheadFound {
		fmt.Println("\n⚠ No overhead metrics found in events")
	} else {
		fmt.Println("\n✓ Overhead metrics successfully implemented!")
	}

	// Show the fixed cost breakdown
	fmt.Printf("\nFixed Cost Breakdown:\n")
	fmt.Printf("  System Prompt: ~%d bytes → ~%d tokens\n", len(systemPrompt), len(systemPrompt)/4)
	// Measured rather than quoted. The two numbers that used to be here (4474 bytes,
	// 1118 tokens) were a snapshot of the seven file and shell built-ins taken before
	// todo and subagent existed, so this program has been reporting roughly half the
	// real schema cost — and reporting it with the confidence of "(from measurement)".
	// A stale constant in a diagnostic is worse than no diagnostic.
	schemas := make([]llm.ToolSchema, 0, len(registry.All()))
	for _, t := range registry.All() {
		schemas = append(schemas, llm.ToolSchema{
			Name: t.Name(), Description: t.Description(), InputSchema: t.InputSchema(),
		})
	}
	wire, err := llm.ToolsWireJSON(schemas)
	if err != nil {
		fmt.Printf("  Tool Schemas:  do not marshal: %v\n", err)
		return
	}
	fmt.Printf("  Tool Schemas:  %d bytes → ~%d tokens (%d tools, measured)\n",
		len(wire), len(wire)/llm.BytesPerToken, len(schemas))
	fmt.Printf("  Total Fixed:   ~%d tokens\n",
		len(systemPrompt)/llm.BytesPerToken+len(wire)/llm.BytesPerToken)
	fmt.Printf("\nNote: cmd/measure-schema reports the whole registry a browser session gets,\n")
	fmt.Printf("including todo and subagent. This program registers only the built-ins.\n")
}
