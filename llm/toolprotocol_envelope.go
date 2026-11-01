package llm

import "strings"

// Detection is intentionally broader than execution: malformed/unsupported
// envelopes deserve one correction, not a successful end-of-turn.
func toolTagMarker(line string) bool {
	for _, tag := range []string{"<tool_call", "<tool_calls", "<function_calls", "</tool_call", "</tool_calls", "</function_calls", "</tool_action"} {
		if strings.HasPrefix(line, tag) {
			if len(line) == len(tag) || strings.ContainsRune(">/ \t\r", rune(line[len(tag)])) {
				return true
			}
		}
	}
	return false
}

func toolFence(line string) (byte, int) {
	if line == "" || (line[0] != '`' && line[0] != '~') {
		return 0, 0
	}
	n := 0
	for n < len(line) && line[n] == line[0] {
		n++
	}
	return line[0], n
}

// Preamble support is an endpoint opt-in, never an inference from prose.
// Even when enabled, the remainder must consist entirely of complete calls.
// Fenced, quoted, indented and inline examples are not call envelopes.
func textCallBody(text string, compat Compatibility) (body, preamble string, ok bool) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "<tool_call>") || strings.HasPrefix(text, "<function_calls>") {
		return text, "", true
	}
	if !compat.AllowTextPreamble {
		return "", "", false
	}
	offset := 0
	for _, raw := range strings.SplitAfter(text, "\n") {
		line := strings.TrimSpace(raw)
		if strings.ContainsAny(line, "<>`~") || strings.HasPrefix(raw, "    ") || strings.HasPrefix(raw, "\t") {
			if offset > 0 && offset <= 4096 && !strings.HasPrefix(raw, "    ") && !strings.HasPrefix(raw, "\t") &&
				(strings.HasPrefix(line, "<tool_call>") || strings.HasPrefix(line, "<function_calls>")) {
				return strings.TrimSpace(text[offset:]), strings.TrimSpace(text[:offset]), true
			}
			return "", "", false
		}
		offset += len(raw)
		if offset > 4096 {
			return "", "", false
		}
	}
	return "", "", false
}
