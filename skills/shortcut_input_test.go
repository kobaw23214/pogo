package skills

import "testing"

func TestShortcutLanguageIsOnlyValidatedDisplayMetadata(t *testing.T) {
	good := StarterInputPart{Type: "shortcut", ShortcutID: "ref", Title: "数据", Text: "/compact\nSELECT 1;"}
	expected := ShortcutInputText(good)
	for _, language := range []string{"", "text", "go", "ts", "java", "c", "python", "rust", "sql", "yara", "css", "xml", "json", "shell", "yaml", "markdown"} {
		part := good
		part.Language = language
		if !ValidInputShortcut(part) || ShortcutInputText(part) != expected {
			t.Fatalf("syntax changed model data or unavailable: %q", language)
		}
		if got := ValidateStarterParts([]StarterInputPart{part}, "inline"); got != "" {
			t.Fatal(got)
		}
	}
	bad := good
	bad.Language = "<script>"
	if ValidInputShortcut(bad) || ValidateStarterParts([]StarterInputPart{bad}, "inline") == "" {
		t.Fatal("unknown language accepted")
	}
	for _, kind := range []string{"text", "file", "element", "skill"} {
		if got := ValidateStarterParts([]StarterInputPart{{Type: kind, Text: "body", Language: "sql"}}, "inline"); got != "shortcut metadata requires a shortcut snapshot" {
			t.Fatalf("syntax metadata attached to %s", kind)
		}
	}
}
