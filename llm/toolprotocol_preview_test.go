package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

func previewSchemas() []ToolSchema {
	return []ToolSchema{{Name: "write", InputSchema: map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}},
		"required":   []string{"path", "content"},
	}}}
}

func streamPreview(t *testing.T, format string, chunks []string) (Response, []Delta) {
	t.Helper()
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range chunks {
			encoded, _ := json.Marshal(chunk)
			flushFrame(w, `data: {"choices":[{"index":0,"delta":{"content":`+string(encoded)+`},"finish_reason":null}]}`+"\n\n")
		}
		flushFrame(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		flushFrame(w, "data: [DONE]\n\n")
	})
	c.compatibility = Compatibility{ToolCalls: format}
	var deltas []Delta
	resp, err := c.Stream(context.Background(), "sys", []Message{UserText("hi")}, previewSchemas(), func(d Delta) {
		deltas = append(deltas, d)
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	return resp, deltas
}

func previewStarts(deltas []Delta) []Delta {
	var out []Delta
	for _, delta := range deltas {
		if delta.Kind == DeltaToolCallStart {
			out = append(out, delta)
		}
	}
	return out
}

func previewArguments(t *testing.T, deltas []Delta, id string) string {
	var out strings.Builder
	for _, delta := range deltas {
		if delta.Kind == DeltaToolCallArgs && delta.ToolID == id {
			out.WriteString(delta.Text)
			if strings.Contains(delta.Text, "<tool_call>") || strings.Contains(delta.Text, "</parameter>") {
				t.Fatalf("protocol wrapper leaked into preview: %q", delta.Text)
			}
		}
	}
	return out.String()
}

func chunkEvery(value string, size int) []string {
	var out []string
	for len(value) > size {
		end := size
		for end > 0 && !utf8.ValidString(value[:end]) {
			end--
		}
		out = append(out, value[:end])
		value = value[end:]
	}
	return append(out, value)
}

func TestJSONTextToolStreamsLargeArgumentPreview(t *testing.T) {
	content := "<!doctype html>\n<html><body>" + strings.Repeat("大HTML内容\n", 200) + "</body></html>"
	call := `<tool_call>{"name":"write","arguments":{"path":"artifacts/demo.html","content":` + string(mustJSON(content)) + `}}</tool_call>`
	resp, deltas := streamPreview(t, "json", chunkEvery(call, 7))
	starts := previewStarts(deltas)
	if len(starts) != 1 || starts[0].ToolName != "write" {
		t.Fatalf("preview starts = %+v", starts)
	}
	settled := resp.Message.ToolCalls()
	if len(settled) != 1 || settled[0].ID != starts[0].ToolID {
		t.Fatalf("preview and settled IDs differ: %+v / %+v", starts, settled)
	}
	preview := previewArguments(t, deltas, starts[0].ToolID)
	var args map[string]string
	if err := json.Unmarshal([]byte(preview), &args); err != nil {
		t.Fatalf("preview is not complete JSON: %v", err)
	}
	if args["path"] != "artifacts/demo.html" || args["content"] != content {
		t.Fatalf("preview content mismatch: path=%q bytes=%d", args["path"], len(args["content"]))
	}
}

func TestJSONTextToolPreviewLargeHTML(t *testing.T) {
	content := strings.Repeat("<section>大 HTML</section>\n", 5000)
	call := `<tool_call>{"name":"write","arguments":{"path":"artifacts/large.html","content":` + string(mustJSON(content)) + `}}</tool_call>`
	resp, deltas := streamPreview(t, "json", chunkEvery(call, 128))
	starts := previewStarts(deltas)
	if len(starts) != 1 {
		t.Fatalf("preview starts = %+v", starts)
	}
	settled := resp.Message.ToolCalls()
	if len(settled) != 1 || settled[0].ID != starts[0].ToolID {
		t.Fatalf("preview and settled IDs differ")
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(previewArguments(t, deltas, starts[0].ToolID)), &args); err != nil {
		t.Fatalf("large preview is not JSON: %v", err)
	}
	if len(args["content"]) != len(content) || args["path"] != "artifacts/large.html" {
		t.Fatalf("large preview mismatch: got %d bytes, want %d", len(args["content"]), len(content))
	}
}

func TestXMLTextToolStreamsDecodedJSONPreview(t *testing.T) {
	call := `<tool_call><invoke name="write"><parameter name="path">artifacts/demo.html</parameter><parameter name="content">&lt;!doctype html&gt;
&lt;p&gt;Tom &amp; Jerry &lt;span&gt;ok&lt;/span&gt;&lt;/p&gt;</parameter></invoke></tool_call>`
	resp, deltas := streamPreview(t, "xml", chunkEvery(call, 5))
	starts := previewStarts(deltas)
	if len(starts) != 1 {
		t.Fatalf("preview starts = %+v", starts)
	}
	settled := resp.Message.ToolCalls()
	if len(settled) != 1 || settled[0].ID != starts[0].ToolID {
		t.Fatalf("preview and settled IDs differ: %+v / %+v", starts, settled)
	}
	var args map[string]string
	preview := previewArguments(t, deltas, starts[0].ToolID)
	if err := json.Unmarshal([]byte(preview), &args); err != nil {
		t.Fatalf("XML preview is not JSON: %v", err)
	}
	want := "<!doctype html>\n<p>Tom & Jerry <span>ok</span></p>"
	if args["path"] != "artifacts/demo.html" || args["content"] != want {
		t.Fatalf("preview = %+v", args)
	}
}

func TestQwenXMLTextToolStreamsParametersPreview(t *testing.T) {
	call := `<tool_call><tool_name>write</tool_name><parameters>{"path":"artifacts/qwen.html","content":"&lt;h1&gt;hello&lt;/h1&gt;"}</parameters></tool_call>`
	resp, deltas := streamPreview(t, "xml", chunkEvery(call, 6))
	starts := previewStarts(deltas)
	if len(starts) != 1 {
		t.Fatalf("preview starts = %+v", starts)
	}
	settled := resp.Message.ToolCalls()
	if len(settled) != 1 || settled[0].ID != starts[0].ToolID {
		t.Fatalf("preview and settled IDs differ: %+v / %+v; issue=%+v", starts, settled, resp.ProtocolIssue)
	}
	preview := previewArguments(t, deltas, starts[0].ToolID)
	if preview != `{"path":"artifacts/qwen.html","content":"<h1>hello</h1>"}` {
		t.Fatalf("preview = %q", preview)
	}
}

func TestTextToolPreviewIDsStayUniqueAcrossCalls(t *testing.T) {
	call := `<tool_call>{"name":"write","arguments":{"path":"one.html","content":"one"}}</tool_call><tool_call>{"name":"write","arguments":{"path":"two.html","content":"two"}}</tool_call>`
	resp, deltas := streamPreview(t, "json", chunkEvery(call, 11))
	starts := previewStarts(deltas)
	settled := resp.Message.ToolCalls()
	if len(starts) != 2 || len(settled) != 2 || starts[0].ToolID == starts[1].ToolID {
		t.Fatalf("preview calls = %+v, settled = %+v", starts, settled)
	}
	for i := range starts {
		if starts[i].ToolID != settled[i].ID {
			t.Errorf("call %d preview/settled ID mismatch", i)
		}
	}
}

func mustJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
