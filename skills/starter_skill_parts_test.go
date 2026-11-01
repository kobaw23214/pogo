package skills

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestStarterSkillAtomsRoundTripAndRejectBodies(t *testing.T) {
	parts := []StarterInputPart{{Type: "text", Text: "先 "}, {Type: "skill", Name: "office"}, {Type: "text", Text: " 再 "}, {Type: "skill", Name: "browser-use"}}
	input, err := json.Marshal(map[string]any{"cards": []StarterCard{{Title: "固定流程", Prompt: "快捷操作", Parts: parts, InputLayout: "inline"}}})
	if err != nil {
		t.Fatal(err)
	}
	starters, diags := parseStarters(input, "input.json")
	if len(diags) != 0 || len(starters.Cards) != 1 || !reflect.DeepEqual(starters.Cards[0].Parts, parts) {
		t.Fatalf("skill atoms changed in saved shortcut: %+v %+v", starters, diags)
	}
	for _, invalid := range []StarterInputPart{{Type: "skill", Name: "compact"}, {Type: "skill", Name: "office", Text: "forged skill body"}, {Type: "skill", Name: "office", Path: "starter-assets/0123456789abcdef0123456789abcdef/a.txt"}, {Type: "element", Name: "office", Text: `<page-element source="x" url="x">\ny\n</page-element>`}} {
		if ValidateStarterParts([]StarterInputPart{invalid}, "inline") == "" {
			t.Fatalf("accepted invalid atom: %+v", invalid)
		}
	}
}

func TestInputSkillNamesKeepPreviouslyLoadableExternalNames(t *testing.T) {
	for _, name := range []string{"office", "Bad--Name", "analysis_v2", "pdf.read", "中文技能", strings.Repeat("x", 128)} {
		if !ValidInputSkillName(name) {
			t.Errorf("previously selectable external name rejected: %q", name)
		}
	}
	for _, name := range []string{"", "compact", "has space", "line\nbreak", "office/other", `office\other`, "has\"quote", "has'quote", "has`quote", "<office>", "\ufeffoffice", strings.Repeat("x", 129), strings.Repeat("😀", 65)} {
		if ValidInputSkillName(name) {
			t.Errorf("unsafe/unbounded input name accepted: %q", name)
		}
	}
}
