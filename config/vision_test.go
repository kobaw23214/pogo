package config

import (
	"strings"
	"testing"
)

// The tri-state rule: an explicit flag wins in both directions, and an id
// without a flag is read against the name-part list.
func TestSeesImagesFlagOverridesHeuristic(t *testing.T) {
	no, yes := false, true
	cases := []struct {
		id       string
		vision   *bool
		seeImage bool
	}{
		{"glm-4v-flash", nil, true},
		{"glm-5.2", nil, false},
		{"moonshot-v1-8k-vision-preview", nil, true},
		{"gemma4:12b", nil, true},
		{"qwen3-coder-plus", nil, false},
		{"gpt-4o", nil, true},
		{"claude-opus-4-1", nil, true},
		{"kimi-k3", nil, true},
		// The override cases: an id that lies in both directions.
		{"secret-vision-clone", &no, false},
		{"plain-name", &yes, true},
		{"glm-5.2", &yes, true},
	}
	for _, c := range cases {
		m := Model{ID: c.id, Vision: c.vision}
		if got := m.SeesImages(); got != c.seeImage {
			t.Errorf("SeesImages(%q, %v) = %v, want %v", c.id, c.vision, got, c.seeImage)
		}
	}
}

// vision_model is validated the same way subagent_model is: unknown names are
// dropped with a warning, because the models it would help still run.
func TestMergeDropsUnknownVisionModelWithWarning(t *testing.T) {
	ps, ms := baseFixture()
	_, gotMs, _, vm, warns, err := merge(ps, ms, fileConfig{VisionModel: "no-such-model"})
	if err != nil {
		t.Fatalf("merge refused an unknown vision_model: %v", err)
	}
	if vm != "" {
		t.Errorf("vision model = %q, want dropped", vm)
	}
	if len(gotMs) == 0 {
		t.Fatal("models lost")
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "vision_model") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one naming vision_model", warns)
	}
}

// A known vision_model passes through untouched.
func TestMergeKeepsKnownVisionModel(t *testing.T) {
	ps, ms := baseFixture()
	_, _, _, vm, warns, err := merge(ps, ms, fileConfig{
		Models:      []modelFile{{ID: "eyes", Provider: "vendor", ContextWindow: 8000}},
		VisionModel: "eyes",
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if vm != "eyes" {
		t.Errorf("vision model = %q, want eyes", vm)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
}
