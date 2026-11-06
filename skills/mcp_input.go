package skills

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var inputMCPServer = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// ValidInputMCPReference is a shape check only. Neither saved snapshots nor
// syntactically valid names can enable a method absent from the live registry.
func ValidInputMCPReference(server, name string) bool {
	if !inputMCPServer.MatchString(server) || name == "" || len(name) > 512 || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || strings.ContainsRune("\"'`<>\\", r) {
			return false
		}
	}
	return true
}

// MCPInputText is a readable, unambiguous fallback, never a call instruction.
func MCPInputText(server, name string) string {
	// PathEscape leaves +, : and @ unescaped, unlike encodeURIComponent.
	// QueryEscape with a literal-space fix produces the same identifiers for
	// the supported shape; the parser decodes exactly once using PathUnescape.
	encode := func(s string) string {
		return strings.NewReplacer("+", "%20", "%21", "!", "%7E", "~", "%2A", "*", "%28", "(", "%29", ")").Replace(url.QueryEscape(s))
	}
	return "/mcp:" + encode(server) + "/" + encode(name)
}
