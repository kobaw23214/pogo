package agentworkflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestWorkflowToolApprovalLegacyHashAndFrozenPatch(t *testing.T) {
	for _, graph := range []bool{false, true} {
		d := Definition{Name: "review", Revision: 3, Inputs: []Input{}, Steps: []Step{{ID: "task", Kind: "agent", Task: "do the task"}}}
		var legacy []byte
		if graph {
			d.Graph = &Graph{Version: 1, Edges: []Edge{{Source: GraphInput, Target: "task"}, {Source: "task", Target: GraphOutput}}}
			legacy, _ = json.Marshal(struct {
				Name, Description string
				Revision          int
				Inputs            []Input
				Steps             []Step
				Graph             *Graph
			}{d.Name, d.Description, d.Revision, d.Inputs, d.Steps, d.Graph})
		} else {
			legacy, _ = json.Marshal(struct {
				Name, Description string
				Revision          int
				Inputs            []Input
				Steps             []Step
			}{d.Name, d.Description, d.Revision, d.Inputs, d.Steps})
		}
		hash := sha256.Sum256(legacy)
		if Hash(d) != hex.EncodeToString(hash[:]) {
			t.Fatal("legacy hash changed", graph)
		}
		if d.EffectiveToolApprovalMode() != ToolApprovalStandard {
			t.Fatal("legacy must remain standard")
		}
		raw, _ := json.Marshal(d)
		if bytes.Contains(raw, []byte("tool_approval_mode")) {
			t.Fatal("legacy field was backfilled")
		}
		frozen := Run{Definition: d}
		old, _ := json.Marshal(frozen)
		mode := ToolApprovalAuto
		next, err := Patch(d, []Operation{{Op: "set_metadata", ToolApprovalMode: &mode}})
		if err != nil || next.ToolApprovalMode != mode || Hash(next) == Hash(d) {
			t.Fatal("approval edit not reviewed/hash-bound", err)
		}
		current, _ := json.Marshal(frozen)
		if !bytes.Equal(old, current) {
			t.Fatal("draft edit changed a frozen Run")
		}
		keep, err := Patch(next, []Operation{{Op: "set_metadata", Name: &d.Name}})
		if err != nil || keep.ToolApprovalMode != mode {
			t.Fatal("unrelated edit dropped mode", err)
		}
	}
}

func TestWorkflowToolApprovalValidatesOnlyExistingReviewModes(t *testing.T) {
	for _, mode := range []string{"", ToolApprovalStandard, ToolApprovalStrict, ToolApprovalAuto} {
		d := Definition{Name: "review", ToolApprovalMode: mode, Steps: []Step{{ID: "task", Kind: "agent", Task: "do it"}}}
		if ds := Validate(d); len(ds) != 0 {
			t.Fatal("valid approval mode rejected", mode, ds)
		}
	}
	for _, mode := range []string{"full", "AUTO", " auto", "none"} {
		d := Definition{Name: "review", ToolApprovalMode: mode, Steps: []Step{{ID: "task", Kind: "agent", Task: "do it"}}}
		found := false
		for _, diagnostic := range Validate(d) {
			if diagnostic.Code == "invalid_tool_approval_mode" {
				found = true
			}
		}
		if !found {
			t.Fatal("invalid approval mode accepted", mode)
		}
	}
}
