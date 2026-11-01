package skillruntime

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/yosukeno/pogo/skillruntime/core"
)

func TestSkillSelectionDependencyClosure(t *testing.T) {
	s := newStubService(t)
	installed, _ := installSkillFixture(t, s)
	selection := Selection{SkillIDs: []string{PackageSkillID(installed.Ref)}}
	plan, err := s.PlanSelection(selection)
	if err != nil || !plan.Ready {
		t.Fatalf("file-only selection: %+v %v", plan, err)
	}
	b, err := core.DecodeBinding(plan.Binding)
	if err != nil || len(b.Skills) != 1 || len(b.Capabilities) != 1 || len(b.Resources) != 1 || b.Resources[0].ResourceRef != "files:current-session" {
		t.Fatalf("incorrect minimal closure: %+v %v", b, err)
	}
	if err := s.ValidateBinding(plan.Binding); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(plan)
	if bytes.Contains(raw, []byte("binding")) || bytes.Contains(raw, []byte(s.Root)) {
		t.Fatal("preview exposed internal binding or host path")
	}
	s.Executor = nil
	plan, err = s.PlanSelection(selection)
	if err != nil || plan.Ready || len(plan.Binding) == 0 || plan.Issues[0].Code != "EXECUTOR_REQUIRED" {
		t.Fatalf("management-only draft failed: %+v %v", plan, err)
	}
	if err := s.Revoke(installed.Ref.PackageDigest); err != nil {
		t.Fatal(err)
	}
	plan, err = s.PlanSelection(selection)
	if err == nil {
		t.Fatalf("revocation missing: %+v %v", plan, err)
	}
	if s.SelectionCatalog().Skills[0].State != "revoked" {
		t.Fatal("cached metadata hid current revocation")
	}
}
