package web

import (
	"strings"

	"github.com/yosukeno/pogo/skills"
)

// uiSectionName mirrors the skill name in skills/builtin/web-drawing; the two
// are one capability and must move together.
const uiSectionDrawingSkill = "web-drawing"

// uiCapabilitiesSection tells the model what the answer surface can render.
// These are Web UI facts whether or not a routing skill loaded; the skill only
// decides when a richer representation is clearer than prose. Terminal sessions
// never see this section because their fences remain code.
func uiCapabilitiesSection(list []skills.Skill) string {
	var b strings.Builder
	b.WriteString("The answers render in a web UI that draws ```mermaid diagrams and ```echarts interactive charts from JSON options. " +
		"It also renders math ($...$, $$...$$ and ```math), data grids (```csv, ```table and ```json-table), " +
		"and rich Markdown including [TOC], [^1] footnotes, task lists, > [!NOTE] callouts, :::details and diff.\n" +
		"Use ![alt](workspace/path.png) to inline an existing workspace image. Never run scripts or create Base64/HTML embeds just to display an image.\n" +
		"For an interactive HTML artifact, save a real .html or .htm workspace file, then use a ```html-preview fence containing only JSON like {\"path\":\"artifacts/app.html\",\"title\":\"App\"}. Ordinary html fences remain source code.\n")
	if _, ok := skills.Find(list, uiSectionDrawingSkill); ok {
		b.WriteString("When a diagram, chart, formula, data table or long structured document would communicate better than prose, read the " +
			uiSectionDrawingSkill + " skill first and follow its routing; do not ask the user which format to use. Ordinary code examples remain fenced code.")
	} else {
		b.WriteString("Draw pictures only when the user asks for one. Use the other rich formats when they help; otherwise fenced blocks remain code.")
	}
	return b.String()
}
