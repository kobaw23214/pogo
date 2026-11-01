package identity

import "testing"

func TestPrincipalPreservesLocalScopeAndSeparatesServiceAuthority(t *testing.T) {
	scope := LocalScope("alice")
	p := LocalPrincipal(scope, "alice", false)
	if !p.Valid() || p.Scope == nil || *p.Scope != scope || p.IsOperator() {
		t.Fatalf("local principal changed account ownership: %+v", p)
	}
	if !SingleUserPrincipal().IsOperator() {
		t.Fatal("local single-user operator lost compatibility")
	}
	for _, p := range []Principal{
		{ID: "service:job", Kind: PrincipalService, Operator: true},
		{ID: "external:system:user", Kind: PrincipalExternal, Operator: true},
	} {
		if !p.Valid() || p.IsOperator() {
			t.Fatalf("non-local principal inherited operator authority: %+v", p)
		}
	}
	if (Principal{ID: "single-user", Kind: PrincipalExternal}).Valid() {
		t.Fatal("external identity may collide with local operator")
	}
	if (Principal{ID: "external:system:user", Kind: PrincipalExternal, Scope: &scope}).Valid() ||
		(Principal{ID: "service:job", Kind: PrincipalService, Scope: &scope}).Valid() {
		t.Fatal("non-local identity may borrow legacy local ownership")
	}
}
