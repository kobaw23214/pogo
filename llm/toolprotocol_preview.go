package llm

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

type textPreviewCall struct {
	id          string
	name        string
	emitted     bool
	argsStart   int
	argsSent    int
	args        strings.Builder
	param       string
	first       bool
	jsonScanned int
	jsonDepth   int
	jsonQuoted  bool
	jsonEscaped bool
	jsonDone    int
}

type textToolPreview struct {
	format  string
	schemas map[string]bool
	buffer  string
	pos     int
	calls   []*textPreviewCall
	current *textPreviewCall
}

func newTextToolPreview(format string, schemas []ToolSchema) *textToolPreview {
	known := make(map[string]bool, len(schemas))
	for _, schema := range schemas {
		known[schema.Name] = true
	}
	return &textToolPreview{format: format, schemas: known}
}

func (p *textToolPreview) Write(text string, emit func(Delta)) {
	p.buffer += text
	for {
		if p.current == nil {
			if !p.beginCall(emit) {
				return
			}
		}
		if p.format == "xml" {
			if !p.advanceXML(emit) {
				return
			}
		} else if !p.advanceRawJSON(emit) {
			return
		}
	}
}

func (p *textToolPreview) beginCall(emit func(Delta)) bool {
	marker := "<tool_call>"
	index := strings.Index(p.buffer[p.pos:], marker)
	if index < 0 {
		return false
	}
	start := p.pos + index + len(marker)
	call := &textPreviewCall{id: "text_" + uuid.NewString(), argsStart: -1, first: true, jsonScanned: -1, jsonDone: -1}
	p.calls = append(p.calls, call)
	p.current = call
	p.pos = start
	return true
}

func (p *textToolPreview) startCall(call *textPreviewCall, emit func(Delta)) {
	if call.emitted || !p.schemas[call.name] {
		return
	}
	call.emitted = true
	emit(Delta{Kind: DeltaToolCallStart, ToolID: call.id, ToolName: call.name})
}

func (p *textToolPreview) emitRawArgs(call *textPreviewCall, end int, emit func(Delta)) {
	if !call.emitted || call.argsStart < 0 || end <= call.argsStart {
		return
	}
	next := end - call.argsStart
	if next <= call.argsSent {
		return
	}
	fragment := p.buffer[call.argsStart+call.argsSent : end]
	call.argsSent = next
	emit(Delta{Kind: DeltaToolCallArgs, ToolID: call.id, Text: fragment})
}

func (p *textToolPreview) emitBuiltArgs(call *textPreviewCall, emit func(Delta)) {
	if !call.emitted || call.args.Len() <= call.argsSent {
		return
	}
	value := call.args.String()
	fragment := value[call.argsSent:]
	call.argsSent = len(value)
	emit(Delta{Kind: DeltaToolCallArgs, ToolID: call.id, Text: fragment})
}

func (p *textToolPreview) advanceRawJSON(emit func(Delta)) bool {
	call := p.current
	body := p.buffer[p.pos:]
	if call.name == "" {
		if name, ok := jsonStringField(body, "name"); ok {
			call.name = name
		} else if !strings.Contains(body, "<tool_name>") {
			return false
		}
	}
	if p.format == "xml" && call.name == "" {
		if name, ok := xmlTextElement(body, "tool_name"); ok {
			call.name = name
		} else {
			return false
		}
	}
	p.startCall(call, emit)
	if call.argsStart < 0 {
		var relative int
		var ok bool
		if p.format == "json" {
			relative, ok = jsonFieldValueStart(body, "arguments", '{')
		} else {
			marker := strings.Index(body, "<parameters>")
			if marker >= 0 {
				start := marker + len("<parameters>")
				for start < len(body) && (body[start] == ' ' || body[start] == '\n' || body[start] == '\r' || body[start] == '\t') {
					start++
				}
				if start < len(body) && body[start] == '{' {
					relative, ok = start, true
				}
			}
		}
		if !ok {
			return false
		}
		call.argsStart = p.pos + relative
		call.jsonScanned = call.argsStart
	}
	if p.format == "xml" {
		return p.advanceQwenParameters(emit)
	}
	done := p.scanJSON(call)
	available := p.buffer[call.argsStart:]
	end := len(available)
	complete := done >= 0
	if complete {
		end = done - call.argsStart
	} else if index := strings.Index(available, "</tool_call>"); index >= 0 {
		end = index
		complete = true
	}
	p.emitRawArgs(call, call.argsStart+end, emit)
	if !complete {
		return false
	}
	p.pos = call.argsStart + end
	p.current = nil
	return true
}

func (p *textToolPreview) advanceQwenParameters(emit func(Delta)) bool {
	call := p.current
	done := p.scanJSON(call)
	available := p.buffer[call.argsStart:]
	end := len(available)
	complete := done >= 0
	if complete {
		end = done - call.argsStart
	} else if index := strings.Index(available, "</parameters>"); index >= 0 {
		end = index
		complete = true
	}
	if complete {
		call.args.WriteString(html.UnescapeString(available[:end]))
		p.emitBuiltArgs(call, emit)
		p.pos = call.argsStart + end
		p.current = nil
		return true
	}
	if len(available) <= 32 {
		return false
	}
	size := safeXMLValuePrefix(available[:len(available)-32])
	for size > 0 && !utf8.ValidString(available[:size]) {
		size--
	}
	if size == 0 {
		return false
	}
	call.args.WriteString(html.UnescapeString(available[:size]))
	call.argsStart += size
	p.emitBuiltArgs(call, emit)
	return true
}

func (p *textToolPreview) advanceXML(emit func(Delta)) bool {
	if strings.HasPrefix(strings.TrimSpace(p.buffer[p.pos:]), "<tool_name>") || strings.Contains(p.buffer[p.pos:], "<parameters>") {
		return p.advanceRawJSON(emit)
	}
	call := p.current
	if call.name == "" {
		index := strings.Index(p.buffer[p.pos:], "<invoke")
		if index < 0 {
			return false
		}
		start := p.pos + index
		end := strings.Index(p.buffer[start:], ">")
		if end < 0 {
			return false
		}
		end += start + 1
		name, ok := xmlAttribute(p.buffer[start:end], "name")
		if !ok {
			return false
		}
		call.name = html.UnescapeString(name)
		p.startCall(call, emit)
		call.args.WriteString("{")
		p.emitBuiltArgs(call, emit)
		p.pos = end
	}
	if call.param != "" {
		return p.advanceXMLParameter(emit)
	}
	remaining := p.buffer[p.pos:]
	invokeEnd := strings.Index(remaining, "</invoke>")
	paramStart := strings.Index(remaining, "<parameter")
	if paramStart < 0 || (invokeEnd >= 0 && invokeEnd < paramStart) {
		if invokeEnd < 0 {
			return false
		}
		call.args.WriteString("}")
		p.emitBuiltArgs(call, emit)
		p.pos += invokeEnd + len("</invoke>")
		p.current = nil
		return true
	}
	start := p.pos + paramStart
	end := strings.Index(p.buffer[start:], ">")
	if end < 0 {
		return false
	}
	end += start + 1
	key, ok := xmlAttribute(p.buffer[start:end], "name")
	if !ok {
		return false
	}
	call.param = html.UnescapeString(key)
	if !call.first {
		call.args.WriteString(",")
	}
	call.first = false
	encoded, _ := json.Marshal(call.param)
	call.args.Write(encoded)
	call.args.WriteString(`:"`)
	p.emitBuiltArgs(call, emit)
	p.pos = end
	return p.advanceXMLParameter(emit)
}

func (p *textToolPreview) advanceXMLParameter(emit func(Delta)) bool {
	call := p.current
	remaining := p.buffer[p.pos:]
	if index := strings.Index(remaining, "</parameter>"); index >= 0 {
		writeJSONStringChunk(&call.args, html.UnescapeString(remaining[:index]))
		call.args.WriteString(`"`)
		p.emitBuiltArgs(call, emit)
		p.pos += index + len("</parameter>")
		call.param = ""
		return true
	}
	if len(remaining) <= 32 {
		return false
	}
	end := safeXMLValuePrefix(remaining[:len(remaining)-32])
	for end > 0 && !utf8.ValidString(remaining[:end]) {
		end--
	}
	if end == 0 {
		return false
	}
	writeJSONStringChunk(&call.args, html.UnescapeString(remaining[:end]))
	p.emitBuiltArgs(call, emit)
	p.pos += end
	return true
}

func (p *textToolPreview) bind(message *Message) {
	var emitted []*textPreviewCall
	for _, call := range p.calls {
		if call.emitted {
			emitted = append(emitted, call)
		}
	}
	index := 0
	for i := range message.Content {
		block := &message.Content[i]
		if block.Type != BlockToolUse || block.ToolOrigin != "text_"+p.format || index >= len(emitted) {
			continue
		}
		block.ID = emitted[index].id
		index++
	}
}

func jsonFieldValueStart(body, field string, want byte) (int, bool) {
	pattern := regexp.MustCompile(`"` + regexp.QuoteMeta(field) + `"\s*:\s*`)
	match := pattern.FindStringIndex(body)
	if match == nil || match[1] >= len(body) || body[match[1]] != want {
		return 0, false
	}
	return match[1], true
}

func jsonStringField(body, field string) (string, bool) {
	start, ok := jsonFieldValueStart(body, field, '"')
	if !ok {
		return "", false
	}
	escaped := false
	for i := start + 1; i < len(body); i++ {
		if escaped {
			escaped = false
			continue
		}
		if body[i] == '\\' {
			escaped = true
			continue
		}
		if body[i] != '"' {
			continue
		}
		var value string
		if json.Unmarshal([]byte(body[start:i+1]), &value) != nil {
			return "", false
		}
		return value, true
	}
	return "", false
}

func (p *textToolPreview) scanJSON(call *textPreviewCall) int {
	if call.jsonDone >= 0 {
		return call.jsonDone
	}
	for i := call.jsonScanned; i < len(p.buffer); i++ {
		char := p.buffer[i]
		if call.jsonQuoted {
			if call.jsonEscaped {
				call.jsonEscaped = false
			} else if char == '\\' {
				call.jsonEscaped = true
			} else if char == '"' {
				call.jsonQuoted = false
			}
			continue
		}
		switch char {
		case '"':
			call.jsonQuoted = true
		case '{':
			call.jsonDepth++
		case '}':
			call.jsonDepth--
			if call.jsonDepth == 0 {
				call.jsonDone = i + 1
				call.jsonScanned = call.jsonDone
				return call.jsonDone
			}
		}
	}
	call.jsonScanned = len(p.buffer)
	return -1
}

func xmlAttribute(tag, name string) (string, bool) {
	pattern := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(name) + `\s*=\s*("([^"]*)"|'([^']*)')`)
	match := pattern.FindStringSubmatch(tag)
	if match == nil {
		return "", false
	}
	if match[2] != "" {
		return match[2], true
	}
	return match[3], true
}

func xmlTextElement(body, name string) (string, bool) {
	start := strings.Index(body, "<"+name+">")
	if start < 0 {
		return "", false
	}
	start += len(name) + 2
	end := strings.Index(body[start:], "</"+name+">")
	if end < 0 {
		return "", false
	}
	return strings.TrimSpace(html.UnescapeString(body[start : start+end])), true
}

func safeXMLValuePrefix(value string) int {
	if ampersand, semicolon := strings.LastIndex(value, "&"), strings.LastIndex(value, ";"); ampersand > semicolon {
		return ampersand
	}
	if angle := strings.LastIndex(value, "<"); angle >= 0 && (strings.HasPrefix("</parameter>", value[angle:]) || strings.HasPrefix("</parameters>", value[angle:])) {
		return angle
	}
	return len(value)
}

func writeJSONStringChunk(builder *strings.Builder, value string) {
	if value == "" {
		return
	}
	encoded, _ := json.Marshal(value)
	builder.Write(encoded[1 : len(encoded)-1])
}
