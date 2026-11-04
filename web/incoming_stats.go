package web

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/yosukeno/pogo/diff"
)

const (
	incomingStatsIntervalMS = 1000
	maxIncomingStatsBytes   = 256 << 10
)

func updateIncomingPreviewStats(call *IncomingCall, ts int64) (int, int) {
	if call.Name != "write" && call.Name != "edit" {
		return 0, 0
	}
	if len(call.raw) > maxIncomingStatsBytes {
		return call.PreviewAdded, call.PreviewRemoved
	}
	if call.statAt != 0 && ts-call.statAt < incomingStatsIntervalMS {
		return call.PreviewAdded, call.PreviewRemoved
	}
	call.statAt = ts
	if call.Name == "write" {
		content, _ := partialJSONField(call.raw, "content")
		call.PreviewAdded, call.PreviewRemoved = diff.Stat("", content)
		return call.PreviewAdded, call.PreviewRemoved
	}
	oldValues := partialJSONFields(call.raw, "oldText")
	newValues := partialJSONFields(call.raw, "newText")
	call.PreviewAdded, call.PreviewRemoved = 0, 0
	for i, oldText := range oldValues {
		newText := ""
		if i < len(newValues) {
			newText = newValues[i]
		}
		added, removed := diff.Stat(oldText, newText)
		call.PreviewAdded += added
		call.PreviewRemoved += removed
	}
	return call.PreviewAdded, call.PreviewRemoved
}

func partialJSONFields(raw, key string) []string {
	var out []string
	for offset := 0; offset < len(raw); {
		value, end, ok := partialJSONFieldAt(raw, key, offset)
		if !ok {
			break
		}
		out = append(out, value)
		if end <= offset {
			break
		}
		offset = end
	}
	return out
}

func partialJSONField(raw, key string) (string, bool) {
	value, _, ok := partialJSONFieldAt(raw, key, 0)
	return value, ok
}

func partialJSONFieldAt(raw, key string, offset int) (string, int, bool) {
	pattern := regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `"\s*:\s*"`)
	match := pattern.FindStringIndex(raw[offset:])
	if match == nil {
		return "", offset, false
	}
	start := offset + match[1]
	escaped := false
	closed := false
	end := len(raw)
	var encoded strings.Builder
	for i := start; i < len(raw); i++ {
		char := raw[i]
		if escaped {
			encoded.WriteByte('\\')
			encoded.WriteByte(char)
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char == '"' {
			closed = true
			end = i + 1
			break
		}
		encoded.WriteByte(char)
	}
	if escaped {
		encoded.WriteByte('\\')
	}
	value := encoded.String()
	if !closed {
		value = trimDanglingJSONEscape(value)
	}
	if decoded, ok := decodeJSONString(value); ok {
		return decoded, end, true
	}
	return decodeJSONStringBestEffort(value), end, true
}

func decodeJSONString(value string) (string, bool) {
	var decoded string
	if err := json.Unmarshal([]byte(`"`+value+`"`), &decoded); err != nil {
		return "", false
	}
	return decoded, true
}

var danglingJSONUnicode = regexp.MustCompile(`\\u[0-9a-fA-F]{0,3}$`)

func trimDanglingJSONEscape(value string) string {
	value = danglingJSONUnicode.ReplaceAllString(value, "")
	if len(value) > 0 && value[len(value)-1] == '\\' {
		value = value[:len(value)-1]
	}
	return value
}

func decodeJSONStringBestEffort(value string) string {
	return strings.NewReplacer(
		`\n`, "\n",
		`\r`, "\r",
		`\t`, "\t",
		`\"`, `"`,
		`\\`, `\`,
	).Replace(value)
}
