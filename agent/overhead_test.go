package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// The itemized fixed cost (overhead.go) is only useful if it describes the
// request that is sent: same view, same units as OverheadTokens, labels that
// cover exactly the text they name.

func TestSystemPromptPartsJoinToTheSamePrompt(t *testing.T) {
	cwd := t.TempDir()
	sections := []string{"ui section", strings.Repeat("skills catalog ", 100), "", "  citations  "}
	parts := SystemPromptParts(cwd,
		PromptPart{Key: PromptPartUI, Text: sections[0]},
		PromptPart{Key: PromptPartSkills, Text: sections[1]},
		PromptPart{Key: PromptPartMemory, Text: sections[2]},
		PromptPart{Key: PromptPartCitations, Text: sections[3]})
	if got, want := JoinPromptParts(parts), SystemPrompt(cwd, sections...); got != want {
		t.Fatalf("labeled prompt differs from SystemPrompt:\n%q\n%q", got, want)
	}
	var keys []string
	for _, p := range parts {
		keys = append(keys, p.Key)
	}
	// An empty section contributes nothing, so it gets no part.
	if strings.Join(keys, ",") != "base,ui,skills,citations" {
		t.Fatalf("part keys: %v", keys)
	}
}

func TestOverheadBreakdownItemizesPromptSectionsToolsAndGroups(t *testing.T) {
	reg, sk, cwd := embeddedSkillRegistry(t, func(o *tools.Options) {
		o.MCP = synthCatalog(t, &viewCaller{}, 5)
	})
	parts := SystemPromptParts(cwd,
		PromptPart{Key: PromptPartSkills, Text: strings.Repeat("skills catalog ", 100)},
		PromptPart{Key: PromptPartCitations, Text: strings.Repeat("cite ", 40)})
	a := New(Config{Client: &viewClient{}, Registry: reg, SystemPrompt: JoinPromptParts(parts), SystemPromptParts: parts})
	a.SetMessages(append([]llm.Message{llm.UserText("go")}, readSkillTurn("r1", sk.Path, "body")...))

	total, got := a.OverheadBreakdown()
	if total != a.OverheadTokens() {
		t.Fatalf("breakdown total %d, OverheadTokens %d", total, a.OverheadTokens())
	}
	byKey := map[string]OverheadPart{}
	var sum int64
	for _, p := range got {
		byKey[p.Kind+":"+p.Name] = p
		sum += p.Tokens
	}
	// Per-part integer division and the array delimiters account for the
	// small gap; the parts never claim more than the whole.
	if sum > total || total-sum > int64(len(got)+8) {
		t.Fatalf("parts sum to %d of %d: %+v", sum, total, got)
	}
	for _, key := range []string{"prompt:base", "prompt:skills", "prompt:citations", "prompt:skill_tools", "tool:read", "tool:todo", "skill_tools:agent-workflows", "mcp:synthetic"} {
		if _, ok := byKey[key]; !ok {
			t.Fatalf("breakdown lacks %s: %+v", key, got)
		}
	}
	if byKey["skill_tools:agent-workflows"].Count != 4 || byKey["mcp:synthetic"].Count != 5 {
		t.Fatalf("group counts: %+v %+v", byKey["skill_tools:agent-workflows"], byKey["mcp:synthetic"])
	}
	// The agent-workflows group is the declaration weight that motivated the
	// breakdown; it has to be visible as one part, not four.
	for _, name := range tools.SkillBindingTools("agent-workflows") {
		if _, ok := byKey["tool:"+name]; ok {
			t.Fatalf("%s listed apart from its group", name)
		}
	}
}

func TestOverheadBreakdownKeepsOnlyLabelsThatDescribeThePrompt(t *testing.T) {
	parts := SystemPromptParts(t.TempDir(), PromptPart{Key: PromptPartSkills, Text: "catalog"})
	for _, prompt := range []string{JoinPromptParts(parts) + "x", strings.Repeat("p", 400)} {
		a := New(Config{Client: &viewClient{}, Registry: tools.NewRegistry(), SystemPrompt: prompt, SystemPromptParts: parts})
		_, got := a.OverheadBreakdown()
		if len(got) != 1 || got[0].Kind != OverheadPrompt || got[0].Name != PromptPartBase || got[0].Tokens != int64(len(prompt)/llm.BytesPerToken) {
			t.Fatalf("stale labels itemized %q: %+v", fmt.Sprintf("%.20s", prompt), got)
		}
	}
}
