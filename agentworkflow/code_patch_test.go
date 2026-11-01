package agentworkflow

import (
	"encoding/json"
	"reflect"
	"testing"
)

func codePatchFixture() Definition {
	return Definition{Name: "代码字段局部修改", Revision: 3, Steps: []Step{{
		ID: "calculate", Kind: "code", Title: "计算", Code: &CodeSpec{
			Language: "python", TimeoutSeconds: 4,
			Source: "def main(inputs):\n    return {'amount': inputs['price'], 'count': inputs['quantity']}\n",
			Inputs: map[string]CodeBinding{
				"price":    {Type: "number", Value: json.RawMessage("12.5")},
				"quantity": {Type: "integer", Value: json.RawMessage("2")},
			},
			Outputs: map[string]ValueSchema{"amount": {Type: "number"}, "count": {Type: "integer"}},
		},
	}}}
}

func TestCodePatchExplicitMapsReplaceWhileScalarsRemainPartial(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch string
		want  func(*CodeSpec)
	}{
		{
			name:  "rename input",
			patch: `{"code":{"source":"def main(inputs):\n    return {'amount': inputs['unit_price'], 'count': inputs['quantity']}\n","inputs":{"unit_price":{"type":"number","value":12.5},"quantity":{"type":"integer","value":2}}}}`,
			want: func(code *CodeSpec) {
				code.Source = "def main(inputs):\n    return {'amount': inputs['unit_price'], 'count': inputs['quantity']}\n"
				code.Inputs["unit_price"] = code.Inputs["price"]
				delete(code.Inputs, "price")
			},
		},
		{
			name:  "delete input",
			patch: `{"code":{"source":"def main(inputs):\n    return {'amount': inputs['price'], 'count': 1}\n","inputs":{"price":{"type":"number","value":12.5}}}}`,
			want: func(code *CodeSpec) {
				code.Source = "def main(inputs):\n    return {'amount': inputs['price'], 'count': 1}\n"
				delete(code.Inputs, "quantity")
			},
		},
		{
			name:  "clear inputs",
			patch: `{"code":{"source":"def main(inputs):\n    return {'amount': 12.5, 'count': 2}\n","inputs":{}}}`,
			want: func(code *CodeSpec) {
				code.Source = "def main(inputs):\n    return {'amount': 12.5, 'count': 2}\n"
				code.Inputs = map[string]CodeBinding{}
			},
		},
		{
			name:  "rename output",
			patch: `{"code":{"source":"def main(inputs):\n    return {'total': inputs['price'], 'count': inputs['quantity']}\n","outputs":{"total":{"type":"number"},"count":{"type":"integer"}}}}`,
			want: func(code *CodeSpec) {
				code.Source = "def main(inputs):\n    return {'total': inputs['price'], 'count': inputs['quantity']}\n"
				code.Outputs["total"] = code.Outputs["amount"]
				delete(code.Outputs, "amount")
			},
		},
		{
			name:  "delete output",
			patch: `{"code":{"source":"def main(inputs):\n    return {'amount': inputs['price']}\n","outputs":{"amount":{"type":"number"}}}}`,
			want: func(code *CodeSpec) {
				code.Source = "def main(inputs):\n    return {'amount': inputs['price']}\n"
				delete(code.Outputs, "count")
			},
		},
		{
			name:  "source only preserves language maps and timeout",
			patch: `{"code":{"source":"def main(inputs):\n    return {'amount': inputs['price'] * 2, 'count': inputs['quantity']}\n"}}`,
			want: func(code *CodeSpec) {
				code.Source = "def main(inputs):\n    return {'amount': inputs['price'] * 2, 'count': inputs['quantity']}\n"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := codePatchFixture()
			before, _ := json.Marshal(d)
			beforeHash := Hash(d)
			// A historical Run freezes its definition; editing the draft must
			// not mutate even a caller sharing that pre-patch value.
			historical := Run{Definition: d}
			historyBefore, _ := json.Marshal(historical)
			wantDefinition := codePatchFixture()
			tc.want(wantDefinition.Steps[0].Code)
			next, err := Patch(d, []Operation{{Op: "update_step", StepID: "calculate", Patch: json.RawMessage(tc.patch)}})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(next.Steps[0].Code, wantDefinition.Steps[0].Code) {
				t.Fatalf("code patch did not preserve partial scalars/replace explicit maps:\n got %#v\nwant %#v", next.Steps[0].Code, wantDefinition.Steps[0].Code)
			}
			after, _ := json.Marshal(d)
			historyAfter, _ := json.Marshal(historical)
			if string(after) != string(before) || Hash(d) != beforeHash || string(historyAfter) != string(historyBefore) {
				t.Fatal("draft patch changed the original definition/hash or historical Run")
			}
			if Hash(next) == beforeHash {
				t.Fatal("effective code modification did not change the draft hash")
			}
		})
	}
}

func TestCodePatchOmittedCodePreservesSpecAndEmptyOutputsRejects(t *testing.T) {
	d := codePatchFixture()
	before, _ := json.Marshal(d)
	beforeHash := Hash(d)
	next, err := Patch(d, []Operation{{Op: "update_step", StepID: "calculate", Patch: json.RawMessage(`{"title":"新的计算标题"}`)}})
	if err != nil || !reflect.DeepEqual(next.Steps[0].Code, d.Steps[0].Code) || next.Steps[0].Title != "新的计算标题" {
		t.Fatalf("omitting code changed its saved specification: %v", err)
	}
	if _, err = Patch(d, []Operation{{Op: "update_step", StepID: "calculate", Patch: json.RawMessage(`{"code":{"outputs":{}}}`)}}); err == nil {
		t.Fatal("empty outputs silently retained the old declarations")
	}
	after, _ := json.Marshal(d)
	if string(after) != string(before) || Hash(d) != beforeHash {
		t.Fatal("rejected empty output contract mutated the original")
	}
}
