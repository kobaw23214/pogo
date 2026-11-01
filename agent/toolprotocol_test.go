package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

const protocolXML = `<tool_call><invoke name="yw_platform_overview"><parameter name="compact" string="true">true</parameter></invoke></tool_action>`

type protocolFrame struct {
	text, reason string
	native       bool
}

func protocolEndpoint(t *testing.T, frames []protocolFrame, inspect func(int, map[string]any)) *httptest.Server {
	t.Helper()
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(count.Add(1))
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if inspect != nil {
			inspect(n, body)
		}
		f := protocolFrame{text: "done", reason: "stop"}
		if n <= len(frames) {
			f = frames[n-1]
		}
		if f.reason == "" {
			f.reason = "stop"
		}
		delta := map[string]any{"content": f.text, "reasoning_content": "protocol reasoning"}
		if f.native {
			delta["tool_calls"] = []any{map[string]any{"index": 0, "id": "native1", "type": "function",
				"function": map[string]string{"name": "mcp_yw_yw_platform_overview", "arguments": `{"compact":true}`}}}
		}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": f.reason}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func protocolMCP(t *testing.T, executed *atomic.Int32) *tools.MCPTool {
	t.Helper()
	tool, err := tools.NewMCPTool("yw", &mcp.Tool{Name: "yw_platform_overview",
		InputSchema: map[string]any{"type": "object", "required": []string{"compact"},
			"properties": map[string]any{"compact": map[string]any{"type": "boolean"}}}},
		time.Second, func(_ context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
			executed.Add(1)
			if name != "yw_platform_overview" || string(args) != `{"compact":true}` {
				t.Errorf("wrong remote call %s %s", name, args)
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "overview-evidence"}}}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func TestToolProtocolLoopHistoryAndReload(t *testing.T) {
	var executed atomic.Int32
	var mu sync.Mutex
	var observed []map[string]any
	srv := protocolEndpoint(t, []protocolFrame{
		{text: "我来查询。\n" + protocolXML},
		{text: protocolXML},
	}, func(_ int, body map[string]any) {
		mu.Lock()
		observed = append(observed, body)
		mu.Unlock()
	})
	client := llm.New(llm.Options{BaseURL: srv.URL, MaxRetries: -1,
		Compatibility: llm.Compatibility{ToolCalls: "xml", AllowMCPAliases: true, PreserveReasoning: true}})
	reg := tools.NewRegistry(protocolMCP(t, &executed))
	a := New(Config{Client: client, Registry: reg, MaxTurns: 6})
	disk := collectPersister(t, a, "overview")
	if executed.Load() != 1 || !reflect.DeepEqual(disk, a.Messages()) {
		t.Fatalf("executed=%d persisted history mismatch=%v", executed.Load(), !reflect.DeepEqual(disk, a.Messages()))
	}
	encoded, err := json.Marshal(disk)
	if err != nil {
		t.Fatal(err)
	}
	var loaded []llm.Message
	if err := json.Unmarshal(encoded, &loaded); err != nil {
		t.Fatal(err)
	}
	var callID string
	for i, m := range loaded {
		for _, call := range m.ToolCalls() {
			callID = call.ID
			if call.Name != reg.All()[0].Name() || call.OriginalName != "yw_platform_overview" || call.ToolOrigin != "text_xml" {
				t.Fatalf("wrong normalized call: %+v", call)
			}
			if i+1 >= len(loaded) || loaded[i+1].Content[0].ToolUseID != call.ID {
				t.Fatal("unpaired tool result after reload")
			}
		}
	}
	if callID == "" {
		t.Fatal("no persisted tool call")
	}
	reloaded := New(Config{Client: client, Registry: reg})
	reloaded.SetMessages(loaded)
	if end := endOf(t, drain(reloaded.Run(context.Background(), "continue"))); end.EndReason != EndCompleted || executed.Load() != 1 {
		t.Fatalf("reload replayed tool: %+v, executed=%d", end, executed.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(observed) != 4 {
		t.Fatalf("model requests=%d, want 4", len(observed))
	}
	wire, _ := json.Marshal(observed[2]["messages"])
	if !strings.Contains(string(wire), `"role":"tool"`) || !strings.Contains(string(wire), callID) ||
		!strings.Contains(string(wire), `"reasoning_content":"protocol reasoning"`) {
		t.Fatalf("next request missing results/reasoning: %s", wire)
	}
}

func TestToolProtocolLoopFailuresAndNativePriority(t *testing.T) {
	for _, tc := range []struct {
		name      string
		frames    []protocolFrame
		compat    llm.Compatibility
		deny      bool
		wantCalls int32
		wantEnd   EndReason
	}{
		{"native correction", []protocolFrame{{text: protocolXML}, {native: true, reason: "tool_calls"}}, llm.Compatibility{}, false, 1, EndCompleted},
		{"function_calls native correction", []protocolFrame{{text: "我来查询。\n<function_calls><invoke name=\"yw_platform_overview\"><parameter name=\"compact\">true</parameter></invoke></function_calls>"}, {native: true, reason: "tool_calls"}}, llm.Compatibility{}, false, 1, EndCompleted},
		{"native failure explicit", []protocolFrame{{text: protocolXML}, {text: protocolXML}}, llm.Compatibility{}, false, 0, EndToolProtocol},
		{"native plus text", []protocolFrame{{native: true, text: protocolXML, reason: "tool_calls"}}, llm.Compatibility{ToolCalls: "xml", AllowMCPAliases: true}, false, 1, EndCompleted},
		{"length", []protocolFrame{{text: protocolXML, reason: "length"}}, llm.Compatibility{ToolCalls: "xml", AllowMCPAliases: true}, false, 0, EndMaxTokens},
		{"approval denied", []protocolFrame{{text: protocolXML}}, llm.Compatibility{ToolCalls: "xml", AllowMCPAliases: true}, true, 0, EndCompleted},
		{"quoted example", []protocolFrame{{text: "\x60\x60\x60xml\n" + protocolXML + "\n\x60\x60\x60"}}, llm.Compatibility{ToolCalls: "xml", AllowMCPAliases: true}, false, 0, EndCompleted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var executed atomic.Int32
			srv := protocolEndpoint(t, tc.frames, nil)
			gate := &fakeGate{decide: func(GateRequest) GateDecision {
				if tc.deny {
					return Deny("test denied")
				}
				return Allow
			}}
			a := New(Config{Client: llm.New(llm.Options{BaseURL: srv.URL, MaxRetries: -1, Compatibility: tc.compat}),
				Registry: tools.NewRegistry(protocolMCP(t, &executed)), Gate: gate, MaxTurns: 6})
			events := drain(a.Run(context.Background(), "overview"))
			end := endOf(t, events)
			if executed.Load() != tc.wantCalls || end.EndReason != tc.wantEnd {
				t.Fatalf("calls=%d end=%+v", executed.Load(), end)
			}
			if tc.wantEnd == EndToolProtocol && (end.Err == nil || end.EndReason.Disposition() != DispositionIntervene) {
				t.Fatal("protocol failure presented as success/retry")
			}
		})
	}
}

func TestToolProtocolFullScreenshotDialects(t *testing.T) {
	for _, body := range []string{
		protocolXML,
		`<function_calls><invoke name="yw_platform_overview"><parameter name="compact">true</parameter></invoke></function_calls>`,
		`<tool_call><tool_name>yw_platform_overview</tool_name><parameters>{"compact":true}</parameters></tool_call>`,
	} {
		for _, deny := range []bool{false, true} {
			var executed atomic.Int32
			preamble := "我来查询 Yw 威胁情报分析系统的任务汇聚数据。"
			var receivedResult bool
			srv := protocolEndpoint(t, []protocolFrame{{text: preamble + "\n" + body}}, func(n int, request map[string]any) {
				if n != 2 {
					return
				}
				wire, _ := json.Marshal(request["messages"])
				receivedResult = strings.Contains(string(wire), `"role":"tool"`) && strings.Contains(string(wire), preamble)
			})
			gate := &fakeGate{decide: func(GateRequest) GateDecision {
				if deny {
					return Deny("fixture deny")
				}
				return Allow
			}}
			a := New(Config{Client: llm.New(llm.Options{BaseURL: srv.URL, MaxRetries: -1, Compatibility: llm.Compatibility{ToolCalls: "xml", AllowMCPAliases: true, AllowTextPreamble: true}}), Registry: tools.NewRegistry(protocolMCP(t, &executed)), Gate: gate, MaxTurns: 5})
			disk := collectPersister(t, a, "读取任务汇聚数据")
			want := int32(1)
			if deny {
				want = 0
			}
			if executed.Load() != want || !receivedResult || !reflect.DeepEqual(disk, a.Messages()) {
				t.Fatalf("body=%s deny=%v executions=%d result=%v", body, deny, executed.Load(), receivedResult)
			}
			for _, m := range disk {
				for _, call := range m.ToolCalls() {
					if call.ToolOrigin != "text_xml" {
						t.Fatal("lost compatibility provenance")
					}
				}
			}
		}
	}
}

func TestToolProtocolDoesNotActivateHiddenMCPTool(t *testing.T) {
	caller := &viewCaller{}
	reg := viewRegistry(t, synthCatalog(t, caller, 60))
	text := `<tool_call>{"name":"op-010","arguments":{}}</tool_call>`
	srv := protocolEndpoint(t, []protocolFrame{{text: text}, {text: text}}, nil)
	a := New(Config{Client: llm.New(llm.Options{BaseURL: srv.URL, MaxRetries: -1,
		Compatibility: llm.Compatibility{ToolCalls: "json", AllowMCPAliases: true}}), Registry: reg})
	if !a.mcpDeferred {
		t.Fatal("fixture must use deferred tools")
	}
	end := endOf(t, drain(a.Run(context.Background(), "hidden")))
	if caller.called("op-010") || end.EndReason != EndToolProtocol {
		t.Fatalf("hidden tool activated: %+v", end)
	}
}

func TestToolProtocolMultipleCallsPairAndKeepUniqueIDs(t *testing.T) {
	var executed atomic.Int32
	srv := protocolEndpoint(t, []protocolFrame{{text: protocolXML + "\n" + protocolXML}}, nil)
	a := New(Config{Client: llm.New(llm.Options{BaseURL: srv.URL, MaxRetries: -1,
		Compatibility: llm.Compatibility{ToolCalls: "xml", AllowMCPAliases: true}}),
		Registry: tools.NewRegistry(protocolMCP(t, &executed))})
	disk := collectPersister(t, a, "overview twice")
	if executed.Load() != 2 || len(disk) != 4 {
		t.Fatalf("calls=%d messages=%d", executed.Load(), len(disk))
	}
	calls := disk[1].ToolCalls()
	if len(calls) != 2 || len(disk[2].Content) != 2 || calls[0].ID == calls[1].ID {
		t.Fatal("batch lost identity or results")
	}
	for i, call := range calls {
		if disk[2].Content[i].ToolUseID != call.ID {
			t.Fatal("results paired in the wrong order")
		}
	}
}
