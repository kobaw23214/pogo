package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

type mcpBoundaryClient struct {
	entered chan struct{}
	release chan struct{}
}

func (*mcpBoundaryClient) Model() string { return "fixture" }
func (c *mcpBoundaryClient) Stream(ctx context.Context, _ string, _ []llm.Message, _ []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	close(c.entered)
	select {
	case <-c.release:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "done"}}}, StopReason: llm.StopEndTurn}, nil
}

func TestAgentMCPRefreshOverheadAndRunGuard(t *testing.T) {
	client := &mcpBoundaryClient{entered: make(chan struct{}), release: make(chan struct{})}
	a := New(Config{Client: client, Registry: tools.New(tools.Options{Cwd: t.TempDir(), Todo: true}), SystemPrompt: "system", ContextEdit: ContextEditConfig{Trigger: 10000}})
	a.SetMessages([]llm.Message{llm.UserText("previous conversation")})
	a.SetUsage(llm.Usage{Input: 17}, llm.Usage{})
	a.lastInput, a.measuredThrough = 100, len(a.messages)
	before := a.OverheadTokens()
	tool, err := tools.NewMCPTool("fixture", &mcp.Tool{Name: "new", Description: strings.Repeat("new capability ", 20), InputSchema: map[string]any{"type": "object"}}, 0,
		func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ReplaceMCP([]tools.Tool{tool}); err != nil {
		t.Fatal(err)
	}
	if a.OverheadTokens() <= before || a.Usage().Input != 17 || len(a.Messages()) != 1 {
		t.Fatal("overhead not updated or conversation state changed")
	}
	a.messages = append(a.messages, llm.UserText("next prompt"))
	if got, want := a.promptTokens(), llm.EstimateTokens(a.messages)+a.fixedCostTokens; got != want {
		t.Fatalf("new schema used old measured prompt baseline: got %d want %d", got, want)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := a.Run(ctx, "start")
	done := make(chan struct{})
	go func() {
		for range events {
		}
		close(done)
	}()
	select {
	case <-client.entered:
	case <-ctx.Done():
		t.Fatal("model did not start")
	}
	if err := a.ReplaceMCP(nil); err == nil {
		t.Fatal("active run allowed registry replacement")
	}
	close(client.release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("run did not finish")
	}
	if err := a.ReplaceMCP(nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.registry.Get(tool.Name()); ok {
		t.Fatal("next run still has removed tool")
	}
}
