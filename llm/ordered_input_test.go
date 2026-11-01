package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOrderedUserInputPreservesMultimodalAndTextFallbackPositions(t *testing.T) {
	for _, visual := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "visual"}[visual], func(t *testing.T) {
			image := Block{Type: BlockImage, ImageRef: "uploads/shot.png", ImageName: "shot.png", ImageAlt: "SCREEN"}
			if visual {
				image.DataURL = "data:image/png;base64,eA=="
			}
			m := Message{Role: RoleUser, InputLayout: "inline", Content: []Block{
				{Type: BlockText, Text: "FIRST "},
				{Type: BlockText, Text: "<page-element source=\"site\" url=\"https://site/\">\nelement: button \"EXPORT\"\n</page-element>", InputElement: true, InputElementFavicon: "data:image/png;base64,LOCAL_FAVICON_MUST_NOT_LEAK"},
				{Type: BlockText, Text: " SECOND "}, image,
				{Type: BlockText, Text: " THIRD "},
				{Type: BlockFile, FilePath: "uploads/FOURTH.xlsx", FileKind: "excel"},
				{Type: BlockText, Text: " FIFTH"},
				{Type: BlockText, Text: " SIXTH_SKILL_HOST_INSTRUCTIONS", InputSkill: "office"},
			}}
			wire := toWireMessages("", []Message{m})
			if len(wire) != 1 {
				t.Fatalf("unexpected messages: %+v", wire)
			}
			raw, _ := json.Marshal(wire)
			if strings.Contains(string(raw), "input_layout") || strings.Contains(string(raw), "input_element") || strings.Contains(string(raw), "input_skill") {
				t.Fatal("local layout metadata leaked to provider")
			}
			if strings.Contains(string(raw), "LOCAL_FAVICON") {
				t.Fatal("display icon bytes leaked to provider")
			}
			if visual {
				parts := wire[0].Parts
				if len(parts) != 3 || parts[0].Type != "text" || parts[1].Type != "image_url" || parts[2].Type != "text" {
					t.Fatalf("order=%s", raw)
				}
				if !strings.Contains(parts[0].Text, "FIRST") || !strings.Contains(parts[0].Text, "EXPORT") || !strings.HasSuffix(parts[0].Text, " SECOND ") {
					t.Fatalf("preceding text reordered: %q", parts[0].Text)
				}
				assertTextOrder(t, parts[2].Text, "THIRD", "FOURTH.xlsx", "FIFTH", "SIXTH_SKILL_HOST_INSTRUCTIONS")
			} else {
				if wire[0].Parts != nil {
					t.Fatal("text model received multimodal content")
				}
				assertTextOrder(t, wire[0].Content, "FIRST", "untrusted observed page data", "EXPORT", "SECOND", "SCREEN", "THIRD", "FOURTH.xlsx", "FIFTH", "SIXTH_SKILL_HOST_INSTRUCTIONS")
			}
		})
	}
}

func assertTextOrder(t *testing.T, text string, values ...string) {
	t.Helper()
	position := 0
	for _, value := range values {
		next := strings.Index(text[position:], value)
		if next < 0 {
			t.Fatalf("%q missing in order after %d: %q", value, position, text)
		}
		position += next + len(value)
	}
}
