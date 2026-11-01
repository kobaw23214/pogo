package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

type argumentModeTool struct{ *fakeTool }

func (*argumentModeTool) ExecutionModeForArgs(args json.RawMessage) tools.ExecutionMode {
	var input struct {
		Write bool `json:"write"`
	}
	if json.Unmarshal(args, &input) != nil || input.Write {
		return tools.Sequential
	}
	return tools.Parallel
}

// A writable operation is a barrier even when its tool also offers parallel
// reads. Approval rewrites must not revive the original parallel permission.
func TestArgumentExecutionModeSerializesWrites(t *testing.T) {
	for _, rewrite := range []bool{false, true} {
		name := "original"
		if rewrite {
			name = "approval-rewrite"
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			active, writers, overlap, calls := 0, 0, false, 0
			tool := &argumentModeTool{&fakeTool{name: "operation", mode: tools.Parallel}}
			tool.run = func(_ context.Context, args json.RawMessage) (tools.Result, error) {
				var input struct {
					Write bool `json:"write"`
				}
				_ = json.Unmarshal(args, &input)
				mu.Lock()
				active++
				calls++
				if input.Write {
					writers++
				}
				if active > 1 && writers > 0 {
					overlap = true
				}
				mu.Unlock()
				time.Sleep(15 * time.Millisecond)
				mu.Lock()
				active--
				if input.Write {
					writers--
				}
				mu.Unlock()
				return tools.Result{Text: "done"}, nil
			}
			response := toolCalls("operation", "operation", "operation")
			for i := range response.Message.Content {
				response.Message.Content[i].Input = json.RawMessage(`{}`)
			}
			if !rewrite {
				response.Message.Content[1].Input = json.RawMessage(`{"write":true}`)
			}
			cfg := Config{Client: &fakeClient{responses: []llm.Response{response}}, Registry: tools.NewRegistry(tool)}
			if rewrite {
				cfg.Gate = &fakeGate{decide: func(req GateRequest) GateDecision {
					if req.CallID == response.Message.Content[1].ID {
						return GateDecision{Allow: true, Args: json.RawMessage(`{"write":true}`)}
					}
					return Allow
				}}
			}
			drain(New(cfg).Run(context.Background(), "go"))
			if calls != 3 || overlap {
				t.Fatalf("calls=%d writable overlap=%v", calls, overlap)
			}
		})
	}
}

func TestArgumentExecutionModeKeepsReadsParallel(t *testing.T) {
	tool := &argumentModeTool{&fakeTool{name: "read-operation", mode: tools.Sequential,
		run: func(context.Context, json.RawMessage) (tools.Result, error) {
			time.Sleep(20 * time.Millisecond)
			return tools.Result{Text: "read"}, nil
		}}}
	response := toolCalls("read-operation", "read-operation", "read-operation")
	a := New(Config{Client: &fakeClient{responses: []llm.Response{response}}, Registry: tools.NewRegistry(tool)})
	drain(a.Run(context.Background(), "go"))
	if tool.maxSeen.Load() < 2 {
		t.Fatal("read-only operations no longer overlap")
	}
}
