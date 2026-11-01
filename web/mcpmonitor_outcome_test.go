package web

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPMonitorInvalidOutcomeAndPanic(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("ok")))
	for _, name := range []string{"typed_nil", "wrong_result", "panic"} {
		t.Run(name, func(t *testing.T) {
			rec := &observeRecorder{}
			h.mgr.SetMCPRecorder(rec)
			l := newMCPLayer(h.mgr, MCPOptions{}, true, nil)
			handler := l.observeMiddleware()(func(context.Context, string, mcp.Request) (mcp.Result, error) {
				switch name {
				case "typed_nil":
					var result *mcp.CallToolResult
					return result, nil
				case "wrong_result":
					return &mcp.ListToolsResult{}, nil
				default:
					panic("synthetic private panic")
				}
			})
			func() {
				if name == "panic" {
					defer func() {
						if recover() == nil {
							t.Error("middleware swallowed panic")
						}
					}()
				}
				handler(context.Background(), "tools/call", &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "agent_task_list"}})
			}()
			calls := rec.finals()
			if len(calls) != 1 {
				t.Fatalf("calls: %+v", calls)
			}
			want := "error"
			if name == "panic" {
				want = "interrupted"
			}
			if calls[0].Status != want {
				t.Fatalf("invalid outcome: %+v", calls[0])
			}
		})
	}
}
