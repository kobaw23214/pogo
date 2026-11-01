package skillruntime

import (
	"regexp"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/skillruntime/core"
)

var skillWireNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func TestSkillToolNameIsReadable(t *testing.T) {
	d := describeContract(core.Contract{ID: "malware.sample.search"})
	if d.Name != "skill_malware_sample_search" {
		t.Fatalf("Name = %q, want the readable contract ID under the skill_ prefix", d.Name)
	}
	if !skillWireNameRE.MatchString(d.Name) {
		t.Fatalf("Name %q violates the wire alphabet", d.Name)
	}
}

func TestSkillToolNameTruncatesLongIDs(t *testing.T) {
	id := strings.Repeat("a", 100) + ".tail"
	first := skillToolName(id)
	if len(first) != wireNameMaxLength {
		t.Fatalf("len(Name) = %d, want %d", len(first), wireNameMaxLength)
	}
	if !skillWireNameRE.MatchString(first) {
		t.Fatalf("truncated name %q violates the wire alphabet", first)
	}
	// Deterministic, and distinct from a neighbour sharing the 55-char prefix.
	if again := skillToolName(id); again != first {
		t.Fatalf("skillToolName not deterministic: %q vs %q", first, again)
	}
	other := skillToolName(strings.Repeat("a", 100) + ".rest")
	if other == first {
		t.Fatalf("distinct long IDs collapsed to %q", first)
	}
}

func TestSkillToolNameAvoidsReservedSkillRead(t *testing.T) {
	name := skillToolName("read")
	if name == "skill_read" {
		t.Fatal("contract ID \"read\" must not take the reserved skill_read tool name")
	}
	if !strings.HasPrefix(name, "skill_read_") || !skillWireNameRE.MatchString(name) {
		t.Fatalf("Name = %q, want a skill_read_ digest variant on the wire alphabet", name)
	}
}

func TestDescribeContractCarriesEffectFlags(t *testing.T) {
	plain := describeContract(core.Contract{ID: "a.b"})
	if !plain.ReadOnly || plain.FileInputs || plain.WritesArtifacts {
		t.Fatalf("plain contract flags = %+v, want read-only with no file/artifact effects", plain)
	}
	full := describeContract(core.Contract{
		ID:         "a.c",
		FileInputs: []string{"/sample"},
		Effects:    core.Effects{ReadsUserFiles: true, WritesArtifacts: true},
	})
	if full.ReadOnly || !full.FileInputs || !full.WritesArtifacts {
		t.Fatalf("effect-bearing contract flags = %+v, want not read-only with fileInputs and writesArtifacts", full)
	}
}

func TestResolveDescriptorNamesDisambiguates(t *testing.T) {
	descriptors := []Descriptor{
		// Past the schema's ID alphabet, two distinct IDs can sanitize to the
		// same name ("a/b" and "a.b" both become skill_a_b); resolution is the
		// defence for contracts that never passed validation.
		describeContract(core.Contract{ID: "a/b"}),
		describeContract(core.Contract{ID: "a.b"}),
		describeContract(core.Contract{ID: "unique.id"}),
	}
	resolveDescriptorNames(descriptors)
	seen := map[string]bool{}
	for _, d := range descriptors {
		if seen[d.Name] {
			t.Fatalf("duplicate tool name %q after resolution", d.Name)
		}
		seen[d.Name] = true
		if !skillWireNameRE.MatchString(d.Name) {
			t.Fatalf("Name %q violates the wire alphabet", d.Name)
		}
	}
	// Both colliders are suffixed — uniqueness must not depend on which one
	// the set happened to iterate first.
	if descriptors[0].Name == "skill_a_b_c" || descriptors[1].Name == "skill_a_b_c" {
		t.Fatalf("a colliding contract kept the bare name: %q, %q", descriptors[0].Name, descriptors[1].Name)
	}
	if descriptors[2].Name != "skill_unique_id" {
		t.Fatalf("non-colliding name changed: %q", descriptors[2].Name)
	}
}

// The descriptor's bundle attribution is what the observation layer binds
// identity from; it must agree between the catalog view (SkillTools) and the
// bound session view (Descriptors), and it never renames the wire tool.
func TestDescriptorCarriesBundleAttribution(t *testing.T) {
	s := newStubService(t)
	installed, refs := installSkillFixture(t, s)
	s.Executor = &recordingExecutor{operation: "inspect", contract: ContractIdentity{ID: "test.file.inspect", Version: "1.0.0", Digest: refs["inspect"].Digest}}
	skillID := PackageSkillID(installed.Ref)

	listed := s.SkillTools(skillID)
	if len(listed) != 1 {
		t.Fatalf("SkillTools = %+v", listed)
	}
	if listed[0].SkillID != skillID || listed[0].SkillName != "file-analysis" {
		t.Fatalf("listed attribution = %+v, want %s / file-analysis", listed[0], skillID)
	}

	plan, err := s.PlanSelection(Selection{SkillIDs: []string{skillID}})
	if err != nil || !plan.Ready {
		t.Fatalf("fixture selection not ready: %+v %v", plan, err)
	}
	lock, err := s.MakeLock(plan.Binding, "sess-attr")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := s.Session(plan.Binding, lock, "sess-attr")
	if err != nil {
		t.Fatal(err)
	}
	descriptors := bound.Descriptors()
	if len(descriptors) != 1 {
		t.Fatalf("Descriptors = %+v", descriptors)
	}
	d := descriptors[0]
	if d.SkillID != skillID || d.SkillName != "file-analysis" {
		t.Fatalf("bound attribution = %+v, want %s / file-analysis", d, skillID)
	}
	if d.ID != listed[0].ID || d.Name != listed[0].Name {
		t.Fatalf("descriptor drifted between views: %+v vs %+v", d, listed[0])
	}
}
