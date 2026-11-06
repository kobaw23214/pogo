package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOfficeUILanguageMatchesSupportedInterfacePreferences(t *testing.T) {
	for _, tc := range []struct{ preference, want string }{
		{"", "zh"}, {"zh-CN", "zh"}, {"zh-Hans", "zh"},
		{"zh-TW", "zh-TW"}, {"zh-Hant", "zh-TW"}, {"zh-HK", "zh-TW"},
		{"en-US", "en"}, {"ja-JP", "ja"}, {"ko-KR", "ko"},
		{"de-DE", "de"}, {"fr-CA", "fr"}, {"es-MX", "es"},
		{"it-IT, fr;q=0.9", "fr"}, {"de;q=0, ja;q=0.8", "ja"},
		{"fr;q=0.2, ko;q=0.9", "ko"}, {"xx", "en"}, {"../zh-TW", "en"},
	} {
		t.Run(tc.preference, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Header.Set("Accept-Language", tc.preference)
			if got := officeUILanguage(r); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOfficeConfigurationLanguageIsIncludedInSignedPayload(t *testing.T) {
	service, document, _ := officeTestService(t)
	server := &Server{office: service}
	for _, preference := range []string{"zh-CN", "zh-TW", "en", "ja", "ko", "de", "fr", "es"} {
		r := httptest.NewRequest(http.MethodGet, "http://pogo.example/config", nil)
		r.Header.Set("Origin", "http://pogo.example")
		r.Header.Set("Accept-Language", preference)
		w := httptest.NewRecorder()
		server.writeOfficeConfigRequest(w, r, document, "locale-preserves-editor-instance", [32]byte{}, document.panelPath)
		if w.Code != http.StatusOK {
			t.Fatalf("%s config: %d %s", preference, w.Code, w.Body.String())
		}
		var reply struct {
			Config map[string]any `json:"config"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		expected := officeUILanguage(r)
		if reply.Config["editorConfig"].(map[string]any)["lang"] != expected {
			t.Fatalf("%s: response language does not match request", preference)
		}
		claims, err := service.verifyJWT(reply.Config["token"].(string))
		if err != nil {
			t.Fatal(err)
		}
		var signedEditor map[string]any
		if err := json.Unmarshal(claims["editorConfig"], &signedEditor); err != nil || signedEditor["lang"] != expected {
			t.Fatalf("%s: signed language does not match configuration: %v", preference, err)
		}
	}
}
