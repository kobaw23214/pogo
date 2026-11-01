package llm

import (
	"errors"
	"strings"
	"unicode"
)

// Qwen chat templates (Qwen3-Coder and later) render text calls as
//
//	<tool_call>
//	<function=NAME>
//	<parameter=ARGUMENT>
//	VALUE
//	</parameter>
//	</function>
//	</tool_call>
//
// This is not XML: the name follows "=", and values are raw text the template
// never escapes, so it cannot go through encoding/xml. The observed local
// endpoint mixed it with the declared invoke form: <parameter name="ARGUMENT">
// openings, </invoke> in place of </function>, and no outer </tool_call>.
// Only those variants are tolerated, and only after every parameter and the
// function element itself have closed.

const qwenFunctionOpen = "<function="

var errQwenIncomplete = errors.New("incomplete function call")

// parseQwenFunction reads one function element at the start of s and returns
// the call and the text after its closing tag.
func parseQwenFunction(s string) (textCall, string, error) {
	name, s, err := qwenName(strings.TrimPrefix(s, qwenFunctionOpen), ">")
	if err != nil {
		return textCall{}, "", err
	}
	args := map[string]string{}
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		var key string
		switch {
		case strings.HasPrefix(s, "</function>"):
			return textCall{name: name, xml: args, qwen: true}, s[len("</function>"):], nil
		case strings.HasPrefix(s, "</invoke>"):
			return textCall{name: name, xml: args, qwen: true}, s[len("</invoke>"):], nil
		case strings.HasPrefix(s, "<parameter="):
			key, s, err = qwenName(s[len("<parameter="):], ">")
		case strings.HasPrefix(s, `<parameter name="`):
			key, s, err = qwenName(s[len(`<parameter name="`):], `">`)
		case s == "":
			return textCall{}, "", errQwenIncomplete
		default:
			return textCall{}, "", errors.New("expected parameter or function end")
		}
		if err != nil {
			return textCall{}, "", err
		}
		if _, exists := args[key]; exists {
			return textCall{}, "", errors.New("duplicate parameter")
		}
		end := strings.Index(s, "</parameter>")
		if end < 0 {
			return textCall{}, "", errQwenIncomplete
		}
		// A value that swallowed the next parameter or the function end means
		// a </parameter> was skipped; executing the merged text would be a guess.
		value := s[:end]
		for _, tag := range []string{"<parameter=", "<parameter name=", "</function>", "</invoke>"} {
			if strings.Contains(value, tag) {
				return textCall{}, "", errors.New("unclosed parameter")
			}
		}
		// The template writes one newline on each side of a value; any other
		// whitespace belongs to the value.
		args[key] = strings.TrimSuffix(strings.TrimPrefix(value, "\n"), "\n")
		s = s[end+len("</parameter>"):]
	}
}

// qwenName reads a declared function or parameter name up to end. Whitespace,
// quotes and markup are never part of one, which also rejects extra attributes.
func qwenName(s, end string) (string, string, error) {
	i := strings.Index(s, end)
	if i < 0 {
		return "", "", errQwenIncomplete
	}
	name := s[:i]
	if name == "" || strings.ContainsFunc(name, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(`<>"'=/&`, r)
	}) {
		return "", "", errors.New("invalid function or parameter name")
	}
	return name, s[i+len(end):], nil
}

// qwenBoolean accepts the Python literals Qwen templates render when they
// replay boolean arguments. The schema, never the text, decides the type.
func qwenBoolean(value string, schema map[string]any) string {
	if schema["type"] == "boolean" {
		switch strings.TrimSpace(value) {
		case "True":
			return "true"
		case "False":
			return "false"
		}
	}
	return value
}

// splitLeakedReasoning moves reasoning that an endpoint delivered as content
// back into the thinking block. The observed endpoint ended its reasoning
// channel at the first literal <tool_call> the model merely mentioned while
// thinking, then sent the rest of the reasoning, the model's own </think> line
// and the actual reply as content. Only that shape is handled: a reasoning
// channel was present, and </think> stands alone on a line outside Markdown
// fences. Everything before it stays reasoning, so a call the model only
// drafted while thinking is never executed.
func splitLeakedReasoning(r Response, compat Compatibility) Response {
	if !compat.textCalls() {
		return r
	}
	thinking, text := -1, -1
	for i, b := range r.Message.Content {
		switch {
		case b.Type == BlockThinking && thinking < 0:
			thinking = i
		case b.Type == BlockText && text < 0:
			text = i
		case b.Type == BlockThinking || b.Type == BlockText:
			return r // not the single-channel shape a stream produces
		}
	}
	if thinking < 0 || text < 0 {
		return r
	}
	leaked, reply, ok := cutThinkClose(r.Message.Content[text].Text)
	if !ok {
		return r
	}
	// A fresh slice: the caller may still hold the original response.
	blocks := make([]Block, 0, len(r.Message.Content))
	for i, b := range r.Message.Content {
		switch i {
		case thinking:
			b.Text += strings.TrimRightFunc(leaked, unicode.IsSpace)
		case text:
			if b.Text = strings.TrimLeftFunc(reply, unicode.IsSpace); b.Text == "" {
				continue
			}
		}
		blocks = append(blocks, b)
	}
	r.Message.Content = blocks
	return r
}

// cutThinkClose splits text at its first line that is exactly </think>.
// Inline mentions, indented or quoted lines and fenced examples do not count.
func cutThinkClose(text string) (before, after string, ok bool) {
	var fence byte
	width, offset := 0, 0
	for _, raw := range strings.SplitAfter(text, "\n") {
		line := strings.TrimSpace(raw)
		if mark, n := toolFence(line); n >= 3 {
			if fence == 0 {
				fence, width = mark, n
			} else if fence == mark && n >= width && strings.TrimSpace(line[n:]) == "" {
				fence, width = 0, 0
			}
		} else if fence == 0 && strings.TrimRight(raw, " \t\r\n") == "</think>" {
			return text[:offset], text[offset+len(raw):], true
		}
		offset += len(raw)
	}
	return "", "", false
}
