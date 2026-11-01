package web

import (
	"strings"
	"testing"

	"github.com/yosukeno/pogo/skills"
)

func TestUICapabilitiesSection(t *testing.T) {
	with := uiCapabilitiesSection([]skills.Skill{{Name: "web-drawing", Description: "draws"}})
	for _, want := range []string{"```mermaid", "```echarts", "```math", "```csv", "```table", "```json-table", "[TOC]", "[^1]", ":::details", "web-drawing skill", "do not ask the user"} {
		if !strings.Contains(with, want) {
			t.Errorf("section with the skill loaded is missing %q:\n%s", want, with)
		}
	}

	bare := uiCapabilitiesSection(nil)
	if strings.Contains(bare, "read the web-drawing") {
		t.Errorf("section without the skill must not tell the model to read it:\n%s", bare)
	}
	for _, want := range []string{"only when the user asks", "Use the other rich formats when they help", "```math", "```csv"} {
		if !strings.Contains(bare, want) {
			t.Errorf("bare variant is missing %q:\n%s", want, bare)
		}
	}
}
