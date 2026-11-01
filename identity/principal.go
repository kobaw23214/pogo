package identity

import (
	"fmt"
	"strings"
)

// Principal describes the caller of a platform action. It is separate from
// Scope: adding resource permissions must not change existing account hashes,
// workspace directories or conversation ownership.
type Principal struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Name     string `json:"name,omitempty"`
	Scope    *Scope `json:"scope,omitempty"`
	Operator bool   `json:"-"`
}

const (
	PrincipalLocal    = "local_user"
	PrincipalOperator = "local_operator"
	PrincipalExternal = "external_user"
	PrincipalService  = "service"
	DefaultSpace      = "default"
)

// LocalPrincipal uses the existing admission owner key verbatim. A local
// account therefore keeps its old records without a data-directory migration.
func LocalPrincipal(scope Scope, name string, operator bool) Principal {
	if !scope.Valid() {
		return Principal{}
	}
	copy := scope
	return Principal{ID: fmt.Sprintf("%q/%q/%q/%q", scope.AppID, scope.TenantID, scope.SubjectID, scope.ProjectID), Kind: PrincipalLocal, Name: name, Scope: &copy, Operator: operator}
}

func SingleUserPrincipal() Principal {
	return Principal{ID: "single-user", Kind: PrincipalOperator, Name: "本地操作者", Operator: true}
}

func (p Principal) Valid() bool {
	if p.ID == "" || len(p.ID) > 512 {
		return false
	}
	switch p.Kind {
	case PrincipalLocal:
		return p.Scope != nil && p.Scope.Valid() && LocalPrincipal(*p.Scope, p.Name, false).ID == p.ID
	case PrincipalOperator:
		return p.ID == "single-user" && p.Scope == nil
	case PrincipalExternal:
		return p.Scope == nil && validID(p.ID) && strings.HasPrefix(p.ID, "external:")
	case PrincipalService:
		return p.Scope == nil && validID(p.ID) && strings.HasPrefix(p.ID, "service:")
	default:
		return false
	}
}

// IsOperator never elevates external/service callers, even if a malformed
// adapter accidentally carries a local operator flag.
func (p Principal) IsOperator() bool {
	return p.Valid() && p.Operator && (p.Kind == PrincipalLocal || p.Kind == PrincipalOperator)
}
