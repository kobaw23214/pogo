package web

import (
	"net/http"
	"strings"

	"golang.org/x/text/language"
)

// The editor uses ISO language codes, with zh-TW for Traditional Chinese.
// Resolve the preference before signing the configuration: mutating an already
// signed editorConfig in the client would invalidate its document capability.
// https://api.onlyoffice.com/docs/docs-api/usage-api/config/editor/#lang
func officeUILanguage(r *http.Request) string {
	if r == nil || strings.TrimSpace(r.Header.Get("Accept-Language")) == "" {
		return "zh" // preserve the default used by clients predating UI preferences
	}
	tags, quality, err := language.ParseAcceptLanguage(r.Header.Get("Accept-Language"))
	if err != nil {
		return "en"
	}
	for i, tag := range tags {
		if quality[i] == 0 {
			continue
		}
		value := strings.ToLower(tag.String())
		base, _ := tag.Base()
		switch base.String() {
		case "zh":
			if strings.Contains(value, "-hant") || strings.Contains(value, "-tw") || strings.Contains(value, "-hk") || strings.Contains(value, "-mo") {
				return "zh-TW"
			}
			return "zh"
		case "en", "ja", "ko", "de", "fr", "es":
			return base.String()
		}
	}
	return "en"
}
