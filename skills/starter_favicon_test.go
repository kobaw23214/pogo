package skills

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestElementFaviconSnapshotsAreBoundedRasterBytes(t *testing.T) {
	icon := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("raster fixture"))
	element := "<page-element source=\"site\" url=\"https://site/\">\nelement: button\n</page-element>"
	if !ValidInputElementFavicon(icon) || ValidateStarterParts([]StarterInputPart{{Type: "element", Text: element, Favicon: icon}}, "inline") != "" {
		t.Fatal("bounded snapshot rejected")
	}
	for _, invalid := range []string{"https://site/icon.png", "file:///tmp/icon.png", "data:image/svg+xml;base64,PHN2Zy8+", "data:text/html;base64,AAAA", "data:image/png;base64,YR==", "data:image/png;base64,", "data:image/png;base64,YQ==\n", "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, (64<<10)+1))} {
		if ValidInputElementFavicon(invalid) || ValidateStarterParts([]StarterInputPart{{Type: "element", Text: element, Favicon: invalid}}, "inline") == "" {
			t.Fatalf("unsafe snapshot accepted: %.70s", invalid)
		}
	}
	for _, part := range []StarterInputPart{{Type: "text", Text: "x", Favicon: icon}, {Type: "skill", Name: "office", Favicon: icon}, {Type: "file", Path: "starter-assets/0123456789abcdef0123456789abcdef/a.csv", Favicon: icon}} {
		if ValidateStarterParts([]StarterInputPart{part}, "inline") == "" {
			t.Fatal("snapshot accepted on a non-element part")
		}
	}
	large := "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, 64<<10))
	if !ValidInputElementFavicon(large) {
		t.Fatal("64 KiB boundary rejected")
	}
	if !strings.Contains(ValidateStarterParts([]StarterInputPart{{Type: "element", Text: element, Favicon: large}, {Type: "element", Text: element, Favicon: large}, {Type: "element", Text: element, Favicon: large}}, "inline"), "256 KiB") {
		t.Fatal("snapshots did not count toward total size")
	}
}
