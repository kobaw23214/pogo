package agentworkflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func definitionFixture() Definition {
	return Definition{Name: "资料报告", Revision: 1, Inputs: []Input{{Key: "topic", Required: true}}, Steps: []Step{{ID: "collect", Kind: "agent", Task: "整理 {{ inputs.topic }}", OutputFile: "notes.md"}, {ID: "review", Kind: "confirm", Prompt: "确认 {{ steps.collect.summary }}"}, {ID: "report", Kind: "agent", Task: "按 {{ steps.review.feedback }} 写报告", OutputFile: "report.md"}}}
}

func TestDefinitionRejectsUnsafeReferencesAndAtomicPatch(t *testing.T) {
	d := definitionFixture()
	if ds := Validate(d); len(ds) > 0 {
		t.Fatal(ds)
	}
	original := Hash(d)
	_, err := Patch(d, []Operation{{Op: "update_step", StepID: "collect", Patch: json.RawMessage(`{"task":"合法修改"}`)}, {Op: "remove_step", StepID: "review"}})
	if err == nil {
		t.Fatal("removed still-referenced confirmation")
	}
	if Hash(d) != original {
		t.Fatal("partial patch mutated original")
	}
	_, err = Patch(d, []Operation{{Op: "update_step", StepID: "collect", Patch: json.RawMessage(`{"id":"other"}`)}})
	if err == nil {
		t.Fatal("stable ID replaced")
	}
	for _, bad := range []string{"../escape.md", "/outside.md", "safe/../escape.md", "C:/outside", "a\\b.md"} {
		d.Steps[0].OutputFile = bad
		if len(Validate(d)) == 0 {
			t.Fatal("accepted output", bad)
		}
	}
	d = definitionFixture()
	d.Steps[0].Task = "{{ steps.report.summary }}"
	if len(Validate(d)) == 0 {
		t.Fatal("accepted future result")
	}
	d = definitionFixture()
	d.Steps[0].Task = "{{ inputs.topic + code() }}"
	if len(Validate(d)) == 0 {
		t.Fatal("accepted expression")
	}
}

func TestRenderUsesCompletedTypedDataAndDoesNotEvaluateNestedInput(t *testing.T) {
	text, err := Render("{{ inputs.topic }} / {{ steps.collect.summary }} / {{ steps.review.feedback }}", map[string]string{"topic": "{{ literal }}"}, []StepRun{{StepID: "collect", Status: Completed, Summary: "有出处"}, {StepID: "review", Status: Completed, Feedback: "继续"}})
	if err != nil || text != "{{ literal }} / 有出处 / 继续" {
		t.Fatalf("%q %v", text, err)
	}
	_, err = Render("{{ steps.collect.summary }}", nil, []StepRun{{StepID: "collect", Status: Running, Summary: "尚未完成"}})
	if err == nil {
		t.Fatal("used uncommitted output")
	}
	d := definitionFixture()
	before := Hash(d)
	d.OwnerID = "private-owner"
	d.EnabledVersion = "enabled"
	d.CreatedAt = 100
	if Hash(d) != before {
		t.Fatal("runtime metadata changed reviewed hash")
	}
	d.Revision++
	if Hash(d) == before {
		t.Fatal("revision not frozen")
	}
	_, err = Render("{{ inputs.topic", map[string]string{"topic": "x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "template") {
		t.Fatal(err)
	}
}
