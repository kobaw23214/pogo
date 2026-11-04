package llm

import "strings"

const CompactionContinuationOpening = "This session continues from an earlier conversation."

// IsCompactionContinuation identifies legacy model-only continuations so UI
// projections never mistake them for something a person typed.
func IsCompactionContinuation(m *Message) bool {
	if m == nil || m.Role != RoleUser {
		return false
	}
	text := m.Text()
	return strings.Contains(text, CompactionContinuationOpening) &&
		strings.Contains(text, "<summary>\n") && strings.Contains(text, "\n</summary>")
}
