// Package identity defines the ownership scope of a locally managed account
// (web password login, users.json).
//
// Every local user shares one app/tenant/project, so the subject is the only
// varying axis and the workspace and memory roots key on it alone. SubjectID
// is sha256 over a length-prefixed encoding of app, issuer, tenant and
// upstream sub. The length prefixes keep the preimage unambiguous:
// ("a","bc") and ("ab","c") must not mix.
package identity

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

// maxIDLen is the sanity limit for every identifier-shaped field.
const maxIDLen = 255

// Scope identifies the one subject in the one project an account owns.
type Scope struct {
	AppID     string `json:"app_id"`
	TenantID  string `json:"tenant_id"`
	SubjectID string `json:"subject_id"`
	ProjectID string `json:"project_id"`
}

// Valid reports whether every field is present and of a sane size.
func (s Scope) Valid() bool {
	return validID(s.AppID) && validID(s.TenantID) && validID(s.SubjectID) && validID(s.ProjectID)
}

// DeriveSubject is the exported form of the internal subject derivation.
// Callers that need to predict an account's subject id — tests, workspace
// tooling — use it rather than re-implementing the encoding.
func DeriveSubject(app, issuer, tenant, upstream string) string {
	return subjectID(app, issuer, tenant, upstream)
}

// LocalSubject derives the internal subject id for a locally managed account.
func LocalSubject(username string) string {
	return DeriveSubject("pogo", "local", "local", username)
}

// LocalScope is the ownership scope of a locally managed account.
func LocalScope(username string) Scope {
	return Scope{
		AppID: "pogo", TenantID: "local",
		SubjectID: LocalSubject(username), ProjectID: "default",
	}
}

// subjectID derives the internal subject identity: sha256 over app, issuer,
// tenant and upstream subject, each length-prefixed. Hashing keeps same-named
// users under different derivations distinct; the prefixes make the preimage
// unambiguous, so no split of one encoding equals another.
func subjectID(appID, issuerName, tenantID, upstreamSubject string) string {
	h := sha256.New()
	var prefix [8]byte
	for _, part := range [...]string{appID, issuerName, tenantID, upstreamSubject} {
		binary.BigEndian.PutUint64(prefix[:], uint64(len(part)))
		h.Write(prefix[:])
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// validID is the shared sanity rule for every identifier-shaped field:
// present, not the wildcard, not absurdly long, valid UTF-8, and free of
// control characters — the classic way to make two strings look alike.
func validID(s string) bool {
	if strings.TrimSpace(s) == "" || s == "*" || len(s) > maxIDLen || !utf8.ValidString(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return false
		}
	}
	return true
}
