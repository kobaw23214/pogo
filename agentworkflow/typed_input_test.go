package agentworkflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func typedInputFixture() Definition {
	return Definition{Name: "类型化报告", Revision: 1, Inputs: []Input{
		{Key: "topic", Required: true},
		{Key: "need_review", Type: "boolean", Required: true, Default: "yes", Options: []InputOption{{Value: "yes", Label: "需要"}, {Value: "no", Label: "不需要"}}},
		{Key: "audience", Type: "select", Default: "team", Options: []InputOption{{Value: "team", Label: "项目组"}, {Value: "management", Label: "管理层"}}},
	}, Steps: []Step{{ID: "report", Kind: "agent", Task: "整理 {{ inputs.topic }}"}}}
}

func TestTypedInputAndResourceFieldsPreserveLegacyHashBytes(t *testing.T) {
	d := Definition{Name: "Legacy", Revision: 7, Inputs: []Input{{Key: "topic", Label: "主题", Required: true}}, Steps: []Step{{ID: "report", Kind: "agent", Task: "Write {{ inputs.topic }}"}}}
	legacy := `{"Name":"Legacy","Description":"","Revision":7,"Inputs":[{"key":"topic","label":"主题","required":true}],"Steps":[{"id":"report","kind":"agent","task":"Write {{ inputs.topic }}"}]}`
	assertHash := func(wire string) {
		t.Helper()
		sum := sha256.Sum256([]byte(wire))
		if got := Hash(d); got != hex.EncodeToString(sum[:]) {
			t.Fatalf("legacy definition hash changed: %s", got)
		}
	}
	assertHash(legacy)
	d.Inputs[0].Options = []InputOption{}
	d.Steps[0].Skills, d.Steps[0].Shortcuts = []string{}, []string{}
	assertHash(legacy)
	d.Graph = &Graph{Version: 1, Edges: []Edge{{Source: GraphInput, Target: "report"}, {Source: "report", Target: GraphOutput}}}
	assertHash(strings.TrimSuffix(legacy, "}") + `,"Graph":{"version":1,"edges":[{"source":"__input","target":"report"},{"source":"report","target":"__output"}]}}`)
	before := Hash(d)
	if _, err := NormalizeInputs(d, map[string]string{"topic": "test"}); err != nil || Hash(d) != before || d.Inputs[0].Type != "" {
		t.Fatalf("reading legacy inputs migrated the definition: %v", err)
	}
	d.Steps[0].Skills = []string{"embedded:office"}
	if Hash(d) == before {
		t.Fatal("explicit resource binding was not included in the reviewed hash")
	}
}

func TestNormalizeInputsUsesCanonicalChoicesAndDoesNotMutateCallers(t *testing.T) {
	d := typedInputFixture()
	if diagnostics := Validate(d); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	submitted := map[string]string{"topic": "季度报告"}
	before := Hash(d)
	normalized, err := NormalizeInputs(d, submitted)
	if err != nil || !reflect.DeepEqual(normalized, map[string]string{"topic": "季度报告", "need_review": "yes", "audience": "team"}) {
		t.Fatal(normalized, err)
	}
	normalized["topic"] = "changed"
	if submitted["topic"] != "季度报告" || len(submitted) != 1 || Hash(d) != before {
		t.Fatal("normalization changed the caller map or definition")
	}
	if err := ValidateInputs(d, map[string]string{"topic": "报告", "need_review": "no", "audience": "management"}); err != nil {
		t.Fatal("canonical selected values were rejected", err)
	}
	for name, values := range map[string]map[string]string{
		"unknown key":              {"topic": "x", "admin": "true"},
		"boolean JSON spelling":    {"topic": "x", "need_review": "true"},
		"display label":            {"topic": "x", "audience": "管理层"},
		"cleared required default": {"topic": "x", "need_review": ""},
		"missing required":         {},
		"oversized value":          {"topic": strings.Repeat("a", 16385)},
		"invalid UTF8":             {"topic": string([]byte{0xff})},
	} {
		t.Run(name, func(t *testing.T) {
			copy := make(map[string]string, len(values))
			for key, value := range values {
				copy[key] = value
			}
			if result, err := NormalizeInputs(d, values); !errors.Is(err, ErrInvalid) || result != nil || !reflect.DeepEqual(copy, values) {
				t.Fatal("invalid runtime values accepted or mutated", result, err)
			}
		})
	}
	legacy := Definition{Inputs: []Input{{Key: "arbitrary"}}}
	if err := ValidateInputs(legacy, map[string]string{"arbitrary": "custom legacy value"}); err != nil {
		t.Fatal("legacy text was treated as a choice", err)
	}
}

func TestTypedInputDefinitionAndDefaultValidation(t *testing.T) {
	for name, input := range map[string]Input{
		"unknown type":           {Key: "choice", Type: "date"},
		"text with choices":      {Key: "choice", Options: []InputOption{{Value: "a"}}},
		"select without choices": {Key: "choice", Type: "select"},
		"duplicate choices":      {Key: "choice", Type: "select", Options: []InputOption{{Value: "a"}, {Value: "a"}}},
		"empty choice":           {Key: "choice", Type: "select", Options: []InputOption{{Value: " "}}},
		"boolean wrong values":   {Key: "choice", Type: "boolean", Options: []InputOption{{Value: "true"}, {Value: "false"}}},
		"boolean incomplete":     {Key: "choice", Type: "boolean", Options: []InputOption{{Value: "yes"}}},
		"boolean bad default":    {Key: "choice", Type: "boolean", Default: "false"},
		"select bad default":     {Key: "choice", Type: "select", Options: []InputOption{{Value: "a"}}, Default: "b"},
		"select blank default":   {Key: "choice", Type: "select", Options: []InputOption{{Value: "a"}}, Default: " "},
		"invalid option UTF8":    {Key: "choice", Type: "select", Options: []InputOption{{Value: string([]byte{0xff})}}},
	} {
		t.Run(name, func(t *testing.T) {
			d := Definition{Name: "test", Inputs: []Input{input}, Steps: []Step{{ID: "report", Kind: "agent", Task: "write"}}}
			if len(Validate(d)) == 0 || ValidateInputs(d, nil) == nil {
				t.Fatal("invalid input definition accepted", input)
			}
		})
	}
	d := typedInputFixture()
	d.Inputs = append(d.Inputs, Input{Key: "topic"})
	if _, err := NormalizeInputs(d, nil); err == nil {
		t.Fatal("duplicate input key accepted")
	}
	d.Inputs = nil
	for index := range 5 {
		d.Inputs = append(d.Inputs, Input{Key: string(rune('a' + index)), Default: strings.Repeat("x", 16384)})
	}
	if _, err := NormalizeInputs(d, nil); err == nil {
		t.Fatal("defaults exceeded the total input budget")
	}
}

func TestNodeResourceReferencesAreScopedAndPatchCopiesNestedData(t *testing.T) {
	d := definitionFixture()
	inputs := []Input{{Key: "topic", Type: "select", Options: []InputOption{{Value: "report", Label: "报告"}}}}
	added := Step{ID: "extra", Kind: "agent", Task: "write", Skills: []string{"embedded:office", "runtime:package/name@1.0"}, Shortcuts: []string{"shortcut:report"}}
	next, err := Patch(d, []Operation{{Op: "set_metadata", Inputs: &inputs}, {Op: "add_step", Step: &added}})
	if err != nil {
		t.Fatal(err)
	}
	inputs[0].Options[0].Label, added.Skills[0], added.Shortcuts[0] = "mutated", "wrong", "wrong"
	if next.Inputs[0].Options[0].Label != "报告" || next.Steps[3].Skills[0] != "embedded:office" || next.Steps[3].Shortcuts[0] != "shortcut:report" {
		t.Fatal("patch result aliases incoming resource or input slices")
	}
	updated, err := Patch(next, []Operation{{Op: "update_step", StepID: "extra", Patch: json.RawMessage(`{"skills":[],"shortcuts":["shortcut:next"]}`)}})
	if err != nil || len(updated.Steps[3].Skills) != 0 || updated.Steps[3].Shortcuts[0] != "shortcut:next" || next.Steps[3].Shortcuts[0] != "shortcut:report" {
		t.Fatal("resource patch did not retain stable IDs or copy prior state", err)
	}
	if _, err := Patch(updated, []Operation{{Op: "update_step", StepID: "review", Patch: json.RawMessage(`{"skills":["embedded:office"]}`)}}); err == nil {
		t.Fatal("confirmation node bound an execution resource")
	}
	for _, ids := range [][]string{{"same", "same"}, {""}, {"space id"}, {"tab\tid"}, {"line\nid"}, {string([]byte{0xff})}, {strings.Repeat("a", 257)}, make([]string, MaxStepResources+1)} {
		if ValidateResourceIDs(ids) == nil {
			t.Fatal("invalid resource IDs accepted", ids)
		}
	}
	if err := ValidateResourceIDs([]string{"embedded:office", "authored:as_example", "runtime:package/name@1.0"}); err != nil {
		t.Fatal(err)
	}
}

func TestResourceOutputNamespaceIsReservedOnlyForExplicitlyBoundDefinitions(t *testing.T) {
	for _, output := range []string{"resources", "resources/report.md", "RESOURCES/report/skill/001-SKILL.md"} {
		t.Run(output, func(t *testing.T) {
			d := definitionFixture()
			d.Steps[0].OutputFile = output
			legacyHash := Hash(d)
			if diagnostics := Validate(d); len(diagnostics) != 0 {
				t.Fatal("legacy output namespace changed", diagnostics)
			}
			// A binding on a later node reserves the namespace for all outputs:
			// its incoming artifacts are materialized beside its frozen resources.
			d.Steps[2].Shortcuts = []string{"shortcut:report"}
			found := false
			for _, diagnostic := range Validate(d) {
				if diagnostic.Code == "reserved_resource_output" && diagnostic.StepID == "collect" && diagnostic.FieldPath == "output_file" {
					found = true
				}
			}
			if !found {
				t.Fatal("bound workflow accepted an artifact/resource collision")
			}
			d.Steps[2].Shortcuts = nil
			if diagnostics := Validate(d); len(diagnostics) != 0 || Hash(d) != legacyHash {
				t.Fatal("removing explicit bindings changed the legacy definition", diagnostics)
			}
		})
	}
}
