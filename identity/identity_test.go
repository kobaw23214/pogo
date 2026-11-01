package identity

import "testing"

func TestLocalScopeDerivation(t *testing.T) {
	alice := LocalScope("alice")
	if !alice.Valid() {
		t.Fatalf("local scope must be valid: %+v", alice)
	}
	if alice.AppID != "pogo" || alice.TenantID != "local" || alice.ProjectID != "default" {
		t.Fatalf("local scope fixed fields drifted: %+v", alice)
	}
	if alice.SubjectID != LocalSubject("alice") {
		t.Fatal("LocalScope and LocalSubject disagree on the subject id")
	}
}

func TestLocalSubjectsDoNotCollide(t *testing.T) {
	// Same-named accounts must never share a subject id: the workspace and
	// memory roots key on it alone.
	if LocalScope("alice").SubjectID == LocalScope("bob").SubjectID {
		t.Fatal("two local accounts collided on one subject id")
	}
	// A differently derived "alice" (another app/tenant axis) stays distinct.
	if LocalScope("alice").SubjectID == DeriveSubject("other-app", "local", "local", "alice") {
		t.Fatal("local subject collided with a differently derived subject")
	}
}

func TestDeriveSubjectPreimageIsUnambiguous(t *testing.T) {
	// Length prefixes keep ("a","bc") and ("ab","c") apart.
	if DeriveSubject("a", "bc", "x", "y") == DeriveSubject("ab", "c", "x", "y") {
		t.Fatal("subject derivation mixed up length splits")
	}
	if DeriveSubject("pogo", "local", "local", "alice") != DeriveSubject("pogo", "local", "local", "alice") {
		t.Fatal("subject derivation is not stable")
	}
}

func TestScopeValidRejectsBadFields(t *testing.T) {
	for _, bad := range []Scope{
		{},              // everything missing
		{AppID: "pogo"}, // rest missing
		{AppID: "*", TenantID: "l", SubjectID: "s", ProjectID: "p"},   // wildcard
		{AppID: "a", TenantID: "l", SubjectID: "\ts", ProjectID: "p"}, // control character
	} {
		if bad.Valid() {
			t.Fatalf("scope %+v must not be valid", bad)
		}
	}
}
