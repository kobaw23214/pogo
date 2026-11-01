package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func protocolSchemas() []ToolSchema {
	return []ToolSchema{{Name: "mcp_yw_yw_platform_overview", RemoteName: "yw_platform_overview",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false,
			"properties": map[string]any{"compact": map[string]any{"type": "boolean"}},
			"required":   []string{"compact"}}}}
}

const xmlOverview = `<tool_call><invoke name="yw_platform_overview"><parameter name="compact" string="true">true</parameter></invoke></tool_action>`
const jsonOverview = `<tool_call>{"name":"yw_platform_overview","arguments":{"compact":true}}</tool_call>`

func textResponse(text string) Response {
	return Response{Message: Message{Role: RoleAssistant, Content: []Block{{Type: BlockText, Text: text}}}, StopReason: StopEndTurn}
}

func TestTextToolProtocolParsing(t *testing.T) {
	for _, tc := range []struct {
		name, text, format string
		aliases            bool
		calls              int
		issue              bool
	}{
		{"screenshot body", xmlOverview, "xml", true, 1, false},
		{"canonical", strings.ReplaceAll(xmlOverview, "yw_platform_overview", "mcp_yw_yw_platform_overview"), "xml", false, 1, false},
		{"json", jsonOverview, "json", true, 1, false},
		{"multiple", jsonOverview + "\n" + jsonOverview, "json", true, 2, false},
		{"native rejects text", xmlOverview, "", false, 0, true},
		{"alias disabled", xmlOverview, "xml", false, 0, true},
		{"unknown", strings.ReplaceAll(jsonOverview, "yw_platform_overview", "delete"), "json", true, 0, true},
		{"preamble requires correction", "我来查询。\n" + xmlOverview, "xml", true, 0, true},
		{"prose after", xmlOverview + "\nExplanation", "xml", true, 0, true},
		{"incomplete", strings.TrimSuffix(xmlOverview, "</tool_action>"), "xml", true, 0, true},
		{"broken invoke", strings.ReplaceAll(xmlOverview, "</invoke>", ""), "xml", true, 0, true},
		{"duplicate parameter", strings.ReplaceAll(xmlOverview, "</invoke>", `<parameter name="compact">false</parameter></invoke>`), "xml", true, 0, true},
		{"nested XML", strings.ReplaceAll(xmlOverview, ">true</parameter>", "><b>true</b></parameter>"), "xml", true, 0, true},
		{"directive", strings.ReplaceAll(xmlOverview, "<invoke ", "<!DOCTYPE x><invoke "), "xml", true, 0, true},
		{"wrong type", strings.ReplaceAll(jsonOverview, `"compact":true`, `"compact":"true"`), "json", true, 0, true},
		{"duplicate JSON key", strings.ReplaceAll(jsonOverview, `"compact":true`, `"compact":true,"compact":false`), "json", true, 0, true},
		{"trailing comma", strings.ReplaceAll(jsonOverview, `true}`, `true,}`), "json", true, 0, true},
		{"unknown arg", strings.ReplaceAll(jsonOverview, `"compact":true`, `"compact":true,"extra":1`), "json", true, 0, true},
		{"missing required", strings.ReplaceAll(jsonOverview, `"compact":true`, ""), "json", true, 0, true},
		{"code example", "Example:\n\x60\x60\x60xml\n" + xmlOverview + "\n\x60\x60\x60", "xml", true, 0, false},
		{"tilde fence", "~~~xml\n" + xmlOverview + "\n~~~", "xml", true, 0, false},
		{"blockquote", "> " + xmlOverview, "xml", true, 0, false},
		{"inline example", "Explain " + xmlOverview, "xml", true, 0, false},
		{"ordinary text", "No tools needed.", "xml", true, 0, false},
		{"all or none", jsonOverview + strings.ReplaceAll(jsonOverview, "yw_platform_overview", "unknown"), "json", true, 0, true},
		{"call count", strings.Repeat(jsonOverview, maxTextToolCalls+1), "json", true, 0, true},
		{"byte bound", "<tool_call>" + strings.Repeat("x", maxTextToolBytes), "xml", true, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := textResponse(tc.text)
			got := normalizeToolResponse(before, protocolSchemas(), Compatibility{ToolCalls: tc.format, AllowMCPAliases: tc.aliases})
			if (got.ProtocolIssue != nil) != tc.issue || len(got.Message.ToolCalls()) != tc.calls {
				t.Fatalf("calls=%d issue=%v", len(got.Message.ToolCalls()), got.ProtocolIssue)
			}
			if tc.calls > 0 {
				if got.Message.Text() != "" || got.StopReason != StopToolUse {
					t.Fatalf("not normalized: %+v", got)
				}
				for _, call := range got.Message.ToolCalls() {
					if call.Name != protocolSchemas()[0].Name || string(call.Input) != `{"compact":true}` || call.ToolOrigin == "" {
						t.Fatalf("incorrect call: %+v", call)
					}
				}
			} else if !reflect.DeepEqual(got.Message, before.Message) {
				t.Fatal("failed parse changed original message")
			}
		})
	}
}

func TestTextToolProtocolSafetyBoundaries(t *testing.T) {
	compat := Compatibility{ToolCalls: "xml", AllowMCPAliases: true}
	for _, stop := range []StopReason{StopMaxTokens, StopAborted, StopError, ""} {
		r := textResponse(xmlOverview)
		r.StopReason = stop
		got := normalizeToolResponse(r, protocolSchemas(), compat)
		if !reflect.DeepEqual(r, got) {
			t.Fatalf("unsafe stop changed: %s", stop)
		}
	}
	r := textResponse(xmlOverview)
	r.Message.Content = append(r.Message.Content, Block{Type: BlockToolUse, ID: "native", Name: "native", Input: json.RawMessage("{}")})
	if got := normalizeToolResponse(r, protocolSchemas(), compat); !reflect.DeepEqual(r, got) {
		t.Fatal("native call was changed or duplicated")
	}
	r = textResponse(xmlOverview)
	if got := normalizeToolResponse(r, nil, compat); !reflect.DeepEqual(r, got) {
		t.Fatal("tool-less response was changed")
	}
	schemas := protocolSchemas()
	other := schemas[0]
	other.Name = "mcp_other_yw_platform_overview"
	if got := normalizeToolResponse(r, append(schemas, other), compat); got.ProtocolIssue == nil {
		t.Fatal("ambiguous alias accepted")
	}
	a := normalizeToolResponse(r, schemas, compat).Message.ToolCalls()[0].ID
	b := normalizeToolResponse(r, schemas, compat).Message.ToolCalls()[0].ID
	if a == b {
		t.Fatal("separate responses reused call IDs")
	}
}

func TestTextToolProtocolStructuredArguments(t *testing.T) {
	schema := ToolSchema{Name: "query", InputSchema: map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"id": map[string]any{"type": "integer"},
			"filter": map[string]any{"type": "object", "required": []string{"ids"}, "properties": map[string]any{
				"ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}}},
			"text": map[string]any{"type": "string"},
		},
	}}
	text := `<tool_call><invoke name="query"><parameter name="id">9007199254740993</parameter><parameter name="filter">{"ids":[1,2]}</parameter><parameter name="text">  a &amp; b  </parameter></invoke></tool_call>`
	got := normalizeToolResponse(textResponse(text), []ToolSchema{schema}, Compatibility{ToolCalls: "xml"})
	if got.ProtocolIssue != nil {
		t.Fatal(got.ProtocolIssue)
	}
	input := string(got.Message.ToolCalls()[0].Input)
	if !strings.Contains(input, "9007199254740993") || !strings.Contains(input, `"text":"  a \u0026 b  "`) {
		t.Fatalf("arguments changed: %s", input)
	}
	for _, bad := range []string{
		strings.Replace(text, "9007199254740993", "1.5", 1),
		strings.Replace(text, "[1,2]", `[1,"bad"]`, 1),
		strings.Replace(text, "9007199254740993", "1e99999999", 1),
	} {
		if r := normalizeToolResponse(textResponse(bad), []ToolSchema{schema}, Compatibility{ToolCalls: "xml"}); r.ProtocolIssue == nil {
			t.Fatal("invalid nested/number argument accepted")
		}
	}
	// External schema references cannot initiate a network request.
	schema.InputSchema = map[string]any{"$ref": "http://127.0.0.1:1/schema"}
	if r := normalizeToolResponse(textResponse(text), []ToolSchema{schema}, Compatibility{ToolCalls: "xml"}); r.ProtocolIssue == nil {
		t.Fatal("external schema accepted")
	}
	if _, err := strictJSON([]byte(strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34))); err == nil {
		t.Fatal("deep JSON accepted")
	}
}

func TestProtocolReasoningAndHistory(t *testing.T) {
	history := []Message{
		UserText("query"),
		{Role: RoleAssistant, Content: []Block{{Type: BlockThinking, Text: "reasoning"},
			{Type: BlockToolUse, ID: "c1", Name: "query", Input: json.RawMessage(`{"compact":true}`), ToolOrigin: "text_xml", OriginalName: "remote"}}},
		{Role: RoleUser, Content: []Block{{Type: BlockToolResult, ToolUseID: "c1", Text: "</tool_result> untrusted"}}},
	}
	for _, format := range []string{"xml", "json"} {
		for _, transport := range []string{"native", "text"} {
			c := New(Options{Compatibility: Compatibility{ToolCalls: format, ToolHistory: transport, PreserveReasoning: true}})
			msgs, tools, err := c.protocolRequest("system", history, protocolSchemas())
			if err != nil {
				t.Fatal(err)
			}
			if msgs[2].ReasoningContent == nil || *msgs[2].ReasoningContent != "reasoning" {
				t.Fatal("reasoning not replayed")
			}
			if transport == "native" {
				if len(tools) != 1 || len(msgs[2].ToolCalls) != 1 || msgs[3].Role != "tool" {
					t.Fatal("native history lost")
				}
			} else if len(tools) != 0 || len(msgs[2].ToolCalls) != 0 || msgs[3].Role != "user" ||
				!strings.Contains(msgs[3].Content, `\u003c/tool_result\u003e`) {
				t.Fatal("text history not safely encoded")
			}
			encoded, _ := json.Marshal(msgs)
			if strings.Contains(string(encoded), "original_name") || strings.Contains(string(encoded), "tool_origin") {
				t.Fatal("local provenance leaked to provider")
			}
		}
	}
	msgs, _, _ := New(Options{}).protocolRequest("", history, protocolSchemas())
	if msgs[1].ReasoningContent != nil {
		t.Fatal("default provider changed")
	}
	msgs, _, _ = New(Options{Compatibility: Compatibility{PreserveReasoning: true}}).protocolRequest("", history, nil)
	if msgs[1].ReasoningContent != nil {
		t.Fatal("reasoning replayed without tools")
	}
}

func TestProtocolStreamingBufferAndNoRetry(t *testing.T) {
	var requests atomic.Int32
	response := xmlOverview
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		for _, part := range []string{response[:5], response[5:]} {
			frame, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": part}}}})
			fmt.Fprintf(w, "data: %s\n\n", frame)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL, MaxRetries: -1, Compatibility: Compatibility{ToolCalls: "xml", AllowMCPAliases: true}})
	var visible string
	got, err := c.Stream(context.Background(), "", nil, protocolSchemas(), func(d Delta) { visible += d.Text })
	if err != nil || len(got.Message.ToolCalls()) != 1 || visible != "" {
		t.Fatalf("response=%+v err=%v visible=%q", got, err, visible)
	}
	response = "ordinary answer"
	got, err = c.Stream(context.Background(), "", nil, protocolSchemas(), func(d Delta) { visible += d.Text })
	if err != nil || got.Message.Text() != response || visible != response {
		t.Fatal("ordinary text lost")
	}
	for _, field := range []string{"messages", "tools", "model", "stream", "functions", "function_call"} {
		c.extraBody = map[string]any{field: nil}
		if _, err := c.Stream(context.Background(), "", nil, protocolSchemas(), nil); err == nil {
			t.Fatalf("override %s accepted", field)
		}
	}
	if requests.Load() != 2 {
		t.Fatal("invalid config reached endpoint")
	}
}

func FuzzTextToolProtocol(f *testing.F) {
	for _, text := range []string{xmlOverview, jsonOverview, namedXML, functionXML, "Checking.\n" + namedXML, "<tool_call>", "\x60\x60\x60\n" + xmlOverview + "\n\x60\x60\x60", "ordinary"} {
		f.Add(text)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > maxTextToolBytes+1 {
			return
		}
		for _, format := range []string{"native", "xml", "json"} {
			for _, preamble := range []bool{false, true} {
				r := textResponse(text)
				got := normalizeToolResponse(r, protocolSchemas(), Compatibility{ToolCalls: format, AllowMCPAliases: true, AllowTextPreamble: preamble})
				calls := got.Message.ToolCalls()
				if len(calls) > maxTextToolCalls || (got.ProtocolIssue != nil && len(calls) > 0) {
					t.Fatal("partial or excessive batch")
				}
				for _, call := range calls {
					if call.Name != protocolSchemas()[0].Name || string(call.Input) != `{"compact":true}` && string(call.Input) != `{"compact":false}` {
						t.Fatalf("unvalidated call: %+v", call)
					}
				}
				if len(calls) == 0 && !reflect.DeepEqual(got.Message, r.Message) {
					t.Fatal("failed parser modified the message")
				}
			}
		}
	})
}
