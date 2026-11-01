package skillruntime

import (
	"github.com/yosukeno/pogo/skillruntime/core"
	"strings"
	"testing"
)

func TestContractRegistrationIsImmutableAcrossPackages(t *testing.T) {
	old := []core.ManifestContractEntry{{Ref: core.ContractRef{ID: "malware.file.yara.scan", Version: "1.0.0", Digest: "sha256:" + strings.Repeat("a", 64)}}}
	next := append([]core.ManifestContractEntry{}, old...)
	if err := consistentContracts(old, next); err != nil {
		t.Fatal(err)
	}
	next[0].Ref.Digest = "sha256:" + strings.Repeat("b", 64)
	if err := consistentContracts(old, next); err == nil {
		t.Fatal("another Skill redefined an accepted contract version")
	}
	next[0].Ref.Version = "2.0.0"
	if err := consistentContracts(old, next); err != nil {
		t.Fatal("explicit new version should be accepted")
	}
}

func TestRuntimeArgumentsRejectAmbiguousJSON(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`{"fileId":"a","fileId":"b"}`), []byte(`{} {}`), {'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}} {
		if _, err := strictValue(raw); err == nil {
			t.Fatalf("invalid input accepted: %q", raw)
		}
	}
}
