package skills

import (
	"strings"
	"testing"
)

func TestInformationInputFilesRequireStrictInformationAssetsWithoutWideningOldModes(t *testing.T) {
	asset := "information-assets/" + strings.Repeat("a", 64) + "/中文 report.xlsx"
	if msg := ValidateInputParts([]StarterInputPart{{Type: "file", Path: asset}}, "inline", "information"); msg != "" {
		t.Fatal(msg)
	}
	for _, p := range []string{"uploads/private.xlsx", "shortcut-assets/" + strings.Repeat("a", 32) + "/f.xlsx", "information-assets/" + strings.Repeat("a", 63) + "/f.xlsx", "information-assets/" + strings.Repeat("A", 64) + "/f.xlsx", "information-assets/" + strings.Repeat("a", 64) + "/../f.xlsx"} {
		if msg := ValidateInputParts([]StarterInputPart{{Type: "file", Path: p}}, "", "information"); msg == "" {
			t.Fatal("unsafe library asset", p)
		}
	}
	for _, mode := range []string{"starter", "shortcut"} {
		if msg := ValidateInputParts([]StarterInputPart{{Type: "file", Path: asset}}, "", mode); msg == "" {
			t.Fatal("old asset namespace widened", mode)
		}
	}
}
