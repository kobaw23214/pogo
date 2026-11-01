package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func TestMCPDescribeBadSelectorThenRecovery(t *testing.T) {
	caller := &viewCaller{}
	reg := viewRegistry(t, synthCatalog(t, caller, 47))
	client := &viewClient{responses: []llm.Response{
		toolUseResp("bad", tools.MCPDescribeName, `{"tool_name":"op-010"}`),
		toolUseResp("detail", tools.MCPDescribeName, `{"tool":"op-010"}`),
		toolUseResp("execute", "mcp_synthetic_op-010", `{"query":"count"}`),
	}}
	a := New(Config{Client: client, Registry: reg, MaxTurns: 6})
	query := "Find the requested task count"
	events := drain(a.Run(context.Background(), query))
	if endOf(t, events).EndReason != EndCompleted || !caller.called("op-010") {
		t.Fatal("corrected call did not execute")
	}
	caller.mu.Lock()
	executions := len(caller.calls)
	caller.mu.Unlock()
	if executions != 1 {
		t.Fatalf("executed %d times", executions)
	}
	if schemaNames(client.requestSchemas(1))["mcp_synthetic_op-010"] {
		t.Fatal("invalid discovery activated tool")
	}
	if !schemaNames(client.requestSchemas(2))["mcp_synthetic_op-010"] {
		t.Fatal("correct detail did not expose tool")
	}
	var user, failed, detail, result bool
	for _, m := range a.Messages() {
		if m.Role == llm.RoleUser && m.Text() == query {
			user = true
		}
		for _, b := range m.Content {
			if b.Type != llm.BlockToolResult {
				continue
			}
			switch b.ToolUseID {
			case "bad":
				failed = b.IsError && strings.Contains(b.Text, `use "tool"`) && !strings.Contains(b.Text, "MCP tools in this session")
			case "detail":
				detail = !b.IsError && strings.Contains(b.Text, "mcp_synthetic_op-010")
			case "execute":
				result = !b.IsError && strings.Contains(b.Text, "called op-010")
			}
		}
	}
	if !user || !failed || !detail || !result {
		t.Fatalf("history user=%v failed=%v detail=%v result=%v", user, failed, detail, result)
	}
}
