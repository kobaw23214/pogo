package web

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

const nativeBrowserFaviconLimit = 64 << 10

// Display icons are image bytes from the worker's page context. Never forward
// arbitrary page URLs, filesystem schemes or active document data to the UI.
func nativeBrowserFavicon(raw json.RawMessage) (string, bool) {
	if len(raw) > base64.StdEncoding.EncodedLen(nativeBrowserFaviconLimit)+80 {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	prefix, encoded, ok := strings.Cut(value, ";base64,")
	if !ok {
		return "", false
	}
	switch prefix {
	case "data:image/png", "data:image/jpeg", "data:image/gif", "data:image/webp", "data:image/x-icon", "data:image/vnd.microsoft.icon", "data:image/svg+xml":
	default:
		return "", false
	}
	if strings.ContainsAny(encoded, "\r\n") || len(encoded) > base64.StdEncoding.EncodedLen(nativeBrowserFaviconLimit) {
		return "", false
	}
	bytes, err := base64.StdEncoding.Strict().DecodeString(encoded)
	return value, err == nil && len(bytes) > 0 && len(bytes) <= nativeBrowserFaviconLimit
}
