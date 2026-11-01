package llm

import (
	"strings"
	"testing"
)

const namedXML = `<tool_call><tool_name>yw_platform_overview</tool_name><parameters>{"compact":true}</parameters></tool_call>`
const functionXML = `<function_calls><invoke name="yw_platform_overview"><parameter name="compact">true</parameter></invoke></function_calls>`

func TestTextToolObservedDialects(t *testing.T) {
	for _, body := range []string{xmlOverview, namedXML, functionXML} {
		for _, preamble := range []string{"", "我来查询 Yw 威胁情报分析系统的任务汇聚数据。\n", "Let me first check the platform overview.\n\n"} {
			for _, allow := range []bool{false, true} {
				compat := Compatibility{ToolCalls: "xml", AllowMCPAliases: true, AllowTextPreamble: allow}
				r := normalizeToolResponse(textResponse(preamble+body), protocolSchemas(), compat)
				if preamble != "" && !allow {
					if r.ProtocolIssue == nil || len(r.Message.ToolCalls()) != 0 {
						t.Fatal("default accepted preamble")
					}
					continue
				}
				if r.ProtocolIssue != nil || len(r.Message.ToolCalls()) != 1 {
					t.Fatalf("%s: %v", preamble+body, r.ProtocolIssue)
				}
				if r.Message.Text() != strings.TrimSpace(preamble) || string(r.Message.ToolCalls()[0].Input) != `{"compact":true}` {
					t.Fatalf("lost preamble or types: %+v", r.Message)
				}
			}
		}
		if r := normalizeToolResponse(textResponse("我来查询。\n"+body), protocolSchemas(), Compatibility{}); r.ProtocolIssue == nil || r.ProtocolIssue.Code != "text_in_native" {
			t.Fatalf("native missed %s", body)
		}
	}
	// The second screenshot used an explicitly empty, complete invoke.
	r := normalizeToolResponse(textResponse(`<function_calls><invoke name="empty"></invoke></function_calls>`), []ToolSchema{{Name: "empty", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}}, Compatibility{ToolCalls: "xml"})
	if r.ProtocolIssue != nil || string(r.Message.ToolCalls()[0].Input) != "{}" {
		t.Fatalf("empty invoke failed: %+v", r)
	}
}

func TestTextToolDialectRejection(t *testing.T) {
	compat := Compatibility{ToolCalls: "xml", AllowMCPAliases: true, AllowTextPreamble: true}
	for _, text := range []string{
		strings.Replace(namedXML, `{"compact":true}`, `{"compact":true,"compact":false}`, 1),
		strings.Replace(namedXML, `{"compact":true}`, `{"compact":"true"}`, 1),
		strings.Replace(namedXML, `{"compact":true}`, `[]`, 1),
		strings.Replace(namedXML, `{"compact":true}`, `{}`, 1),
		strings.Replace(namedXML, `</parameters>`, `</parameters><parameters>{}</parameters>`, 1),
		strings.Replace(namedXML, `<tool_name>`, `<tool_name name="extra">`, 1),
		strings.Replace(namedXML, `</tool_name>`, "", 1),
		strings.Replace(namedXML, `yw_platform_overview`, `unknown`, 1),
		functionXML + strings.Replace(namedXML, `yw_platform_overview`, `unknown`, 1),
		functionXML + "\nDone.",
		strings.TrimSuffix(functionXML, "</function_calls>"),
		"```text\nexample\n```\nI will query.\n" + namedXML,
		"> quoted explanation\n" + namedXML,
		strings.Repeat("x", 4097) + "\n" + namedXML,
		"text\n    " + namedXML + "\n" + namedXML,
		`<tool_calls>` + namedXML + `</tool_calls>`,
		`</tool_calls>`,
		`<function_calls broken>`,
	} {
		r := normalizeToolResponse(textResponse(text), protocolSchemas(), compat)
		if r.ProtocolIssue == nil || len(r.Message.ToolCalls()) != 0 {
			t.Fatalf("accepted invalid dialect: %s => %+v", text, r)
		}
	}
	// A four-character fence must not be closed by a three-character fence.
	for _, text := range []string{
		"````xml\n```\n" + namedXML + "\n````",
		"~~~xml\n" + functionXML + "\n~~~",
		"> " + functionXML,
		"    " + namedXML,
		"Explain " + namedXML,
		"<tool_callback>ordinary tag</tool_callback>",
	} {
		r := normalizeToolResponse(textResponse(text), protocolSchemas(), compat)
		if r.ProtocolIssue != nil || len(r.Message.ToolCalls()) != 0 || r.Message.Text() != text {
			t.Fatalf("acted on example: %+v", r)
		}
	}
}

func TestTextToolJSONPreamble(t *testing.T) {
	r := normalizeToolResponse(textResponse("Checking now.\n"+jsonOverview), protocolSchemas(), Compatibility{ToolCalls: "json", AllowMCPAliases: true, AllowTextPreamble: true})
	if r.ProtocolIssue != nil || len(r.Message.ToolCalls()) != 1 || r.Message.Text() != "Checking now." {
		t.Fatalf("JSON preamble: %+v", r)
	}
}
