package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/tools"
)

func TestNativeBrowserObservationFaviconIsBoundedUIDisplayData(t *testing.T) {
	icon := "data:image/svg+xml;base64,PHN2Zz48L3N2Zz4="
	for _, value := range []string{icon, "https://site.test/favicon.ico", "data:text/html;base64,PGgxLz4=", "data:image/png;base64," + strings.Repeat("A", base64.StdEncoding.EncodedLen(nativeBrowserFaviconLimit)+4)} {
		cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, tools.BrowserReply{OK: true, URL: "https://site.test", Text: "Original observed text", Favicon: value})
		})
		browser := newNativeBrowser(cfg)
		reply, err := browser.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`))
		if err != nil || !reply.OK || reply.Text != "Original observed text" {
			t.Fatalf("display metadata changed observation: %+v, %v", reply, err)
		}
		if value == icon && reply.Favicon != icon || value != icon && reply.Favicon != "" {
			t.Fatalf("favicon byte allowlist failed for %.50s", value)
		}
	}
}

func TestNativeBrowserFaviconState(t *testing.T) {
	icon := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`))
	raw, _ := json.Marshal(map[string]any{"favicon": icon, "tabs": []map[string]any{{"id": "t1", "url": "https://site.test", "favicon": icon}, {"id": "t2", "url": "https://site.test", "favicon": "https://site.test/favicon.ico"}, {"id": "t3", "url": "about:blank", "favicon": icon}}})
	state, err := nativeBrowserState(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	var selected string
	_ = json.Unmarshal(state["favicon"], &selected)
	if selected != icon {
		t.Fatal("selected page favicon was not forwarded")
	}
	var tabs []map[string]any
	_ = json.Unmarshal(state["tabs"], &tabs)
	if len(tabs) != 3 || tabs[0]["favicon"] != icon || tabs[1]["favicon"] != nil || tabs[2]["favicon"] != nil {
		t.Fatal("tab favicon display allowlist changed")
	}
	for _, value := range []string{"https://site.test/icon.svg", "javascript:alert(1)", "data:text/html;base64,PGgxLz4=", "data:image/png;base64,YQ=", "data:image/png;base64,YQ==\n", "data:image/png;base64," + strings.Repeat("A", base64.StdEncoding.EncodedLen(nativeBrowserFaviconLimit)+4)} {
		encoded, _ := json.Marshal(value)
		if _, valid := nativeBrowserFavicon(encoded); valid {
			t.Fatalf("unsafe favicon forwarded: %.80s", value)
		}
	}
}
