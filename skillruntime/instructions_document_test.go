package skillruntime

import (
	"strings"
	"testing"

	"github.com/yosukeno/pogo/skills"
)

func TestBoundSkillDocumentPreviewUsesSelectedMetadataReader(t *testing.T) {
	s := newStubService(t)
	installed, _ := installSkillFixture(t, s)
	id := PackageSkillID(installed.Ref)
	plan, err := s.PlanSelection(Selection{SkillIDs: []string{id}})
	if err != nil || !plan.Ready {
		t.Fatalf("selection: %+v %v", plan, err)
	}
	lock, err := s.MakeLock(plan.Binding, "document-preview")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := s.Session(plan.Binding, lock, "document-preview")
	if err != nil {
		t.Fatal(err)
	}
	list := bound.Skills()
	if len(list) != 1 || list[0].ReadDocument == nil || !strings.HasPrefix(list[0].Dir, "skill-runtime://") {
		t.Fatalf("private reader missing: %+v", list)
	}
	data, truncated, err := skills.ReadDocument(list[0], "SKILL.md")
	if err != nil || truncated || !strings.Contains(string(data), "bound inspection tool") {
		t.Fatalf("metadata: %q %v %v", data, truncated, err)
	}
	for _, relative := range []string{"contracts/inspect.json", "payload/rt.zip", "../catalog.json", "references/missing.md"} {
		if data, _, err := skills.ReadDocument(list[0], relative); err == nil || len(data) > 0 {
			t.Fatalf("private path %q leaked %q (%v)", relative, data, err)
		}
	}
	if data, _, err := s.SkillStaticDocument(id, "contracts/inspect.json"); err != nil || !strings.Contains(string(data), "CapabilityContract") {
		t.Fatalf("administrator cannot inspect package contract source: %q %v", data, err)
	}
	if _, _, err := s.SkillDocument(id, "contracts/inspect.json"); err == nil {
		t.Fatal("administrator source preview widened Service metadata access")
	}
	if err := s.Revoke(installed.Ref.PackageDigest); err != nil {
		t.Fatal(err)
	}
	if data, _, err := skills.ReadDocument(list[0], "SKILL.md"); err == nil || len(data) > 0 {
		t.Fatalf("revoked Skill leaked %q (%v)", data, err)
	}
}
