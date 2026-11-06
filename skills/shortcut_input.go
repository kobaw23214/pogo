package skills

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxInputShortcutTextBytes = 64 << 10

var inputShortcutID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)
var inputShortcutIcon = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// ValidShortcutLanguage accepts exactly the existing UI syntax scanners. Empty
// is the backwards-compatible plain-text default. No language changes model text.
func ValidShortcutLanguage(value string) bool {
	switch value {
	case "", "text", "go", "ts", "java", "c", "python", "rust", "sql", "yara", "css", "xml", "json", "shell", "bash", "yaml", "yml", "properties", "markdown":
		return true
	default:
		return false
	}
}

// ValidInputShortcut validates only user data, not any executable reference.
// Newlines and tabs in the body are meaningful (for example connection data).
func ValidInputShortcut(part StarterInputPart) bool {
	if part.Type != "shortcut" || !inputShortcutID.MatchString(part.ShortcutID) ||
		part.Path != "" || part.Name != "" || part.Server != "" || part.AsFile || part.Favicon != "" ||
		!utf8.ValidString(part.Title) || strings.TrimSpace(part.Title) == "" || utf8.RuneCountInString(part.Title) > 120 ||
		!utf8.ValidString(part.Text) || (part.Parts == nil && strings.TrimSpace(part.Text) == "") || (part.Parts == nil && len(part.Text) > MaxInputShortcutTextBytes) ||
		part.Office != nil ||
		part.Icon != "" && !inputShortcutIcon.MatchString(part.Icon) || !ValidShortcutLanguage(part.Language) {
		return false
	}
	for _, r := range part.Title {
		if unicode.IsControl(r) {
			return false
		}
	}
	for _, r := range part.Text {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

// ShortcutInputText stays ordinary user text. In particular a body beginning
// /compact or /skill: cannot become a host command just by being referenced.
func ShortcutInputText(part StarterInputPart) string {
	if part.Parts != nil {
		return "[资料：" + part.Title + "]\n"
	}
	return "[资料：" + part.Title + "]\n" + part.Text
}
