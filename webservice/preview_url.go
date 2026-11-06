package webservice

import (
	"net"
	"net/url"
	"regexp"
	"strings"
)

// PathPreviewPrefix is reserved by the gateway, never a Pogo API route.
const PathPreviewPrefix = "/_pogo/web/"

var canonicalPathPreview = regexp.MustCompile(`^/_pogo/web/[a-f0-9]{48}/$`)

// Titles can truncate a capability before its full token; redact its reserved
// hexadecimal prefix too. This is intentionally broader than receipt parsing.
var privatePathPreview = regexp.MustCompile(`(?i)/_pogo/web/[a-f0-9]+(?:/[^\s"'<>)\]]*)?`)

// A tool result can itself contain JSON text with escaped slashes, even after
// the outer session snapshot has been decoded. Remove the bearer token without
// reparsing or rewriting unrelated JSON numbers and formatting.
var escapedPrivatePathPreview = regexp.MustCompile(`(?i)\\/_pogo\\/web\\/[a-f0-9]+(?:\\/)?`)

// IsPathPreviewURL accepts only the canonical root URL issued over the trusted
// gateway control channel. Paths for assets, query strings and encoded aliases
// are not open receipts. This does not authorize any URL or choose an upstream.
func IsPathPreviewURL(u *url.URL) bool {
	return u != nil && (u.Scheme == "http" || u.Scheme == "https") &&
		u.Hostname() != "" && u.User == nil && u.Opaque == "" &&
		u.RawPath == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" &&
		canonicalPathPreview.MatchString(u.Path)
}

func validManagedPreviewURL(u *url.URL) bool {
	if IsPathPreviewURL(u) {
		return true
	}
	return u != nil && (u.Scheme == "http" || u.Scheme == "https") &&
		u.User == nil && u.Opaque == "" && strings.Contains(u.Hostname(), ".") &&
		net.ParseIP(u.Hostname()) == nil && (u.Path == "" || u.Path == "/") &&
		u.RawPath == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

func containsPathPreview(path string) bool {
	if !strings.HasPrefix(path, PathPreviewPrefix) {
		return false
	}
	rest := strings.TrimPrefix(path, PathPreviewPrefix)
	if rest == "" {
		return false
	}
	c := rest[0]
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// RedactPathPreviewURLs needs no live Manager or remembered DNS suffix. A
// reserved path capability must not reach public shares after restart or when
// the feature is disabled. No bearer URL/token is persisted to make this work.
func RedactPathPreviewURLs(text string) string {
	text = escapedPrivatePathPreview.ReplaceAllString(text, "web-service:preview-redacted")
	text = previewURLPattern.ReplaceAllStringFunc(text, func(raw string) string {
		u, err := url.Parse(raw)
		if err == nil && containsPathPreview(u.Path) {
			return "web-service:preview-redacted"
		}
		return raw
	})
	return privatePathPreview.ReplaceAllString(text, "web-service:preview-redacted")
}
