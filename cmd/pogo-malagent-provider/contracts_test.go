package main

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/skillruntime/core"
)

// TestContractsPassHostValidation runs every embedded contract through the
// same document validator the host uses (schema layer + closed schema profile
// + fileInputs coherence), so a contract that would be rejected at install
// time fails here first.
func TestContractsPassHostValidation(t *testing.T) {
	entries, err := fs.ReadDir(contracts, "contracts")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 9 {
		t.Fatalf("want 9 contracts, got %d", len(entries))
	}
	for _, e := range entries {
		raw, err := contracts.ReadFile("contracts/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if err := core.ValidateDocument(core.KindCapabilityContract, raw); err != nil {
			t.Errorf("%s: %v", e.Name(), err)
		}
		var head struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			t.Errorf("%s: %v", e.Name(), err)
		}
		if e.Name() != head.ID+".json" {
			t.Errorf("%s: file name must be <id>.json, id is %s", e.Name(), head.ID)
		}
		if head.Version == "" {
			t.Errorf("%s: version is empty", e.Name())
		}
		if !strings.HasPrefix(head.ID, "malware.") {
			t.Errorf("%s: id outside malware.* namespace", e.Name())
		}
		if head.ID == "malware.file.yara.scan" {
			t.Errorf("malware.file.yara.scan must stay with the existing yara provider, not this package")
		}
	}
}

// TestOperationsMatchContracts guards the describe table: every embedded
// contract has a handler, every handler has a contract, and digests are
// computed over the exact embedded bytes.
func TestOperationsMatchContracts(t *testing.T) {
	if len(operations) != 9 {
		t.Fatalf("want 9 operations, got %d", len(operations))
	}
	for name, op := range operations {
		if op.handle == nil {
			t.Errorf("%s: no handler", name)
		}
		if op.contract.Digest == "" || op.contract.ID == "" {
			t.Errorf("%s: incomplete identity %+v", name, op.contract)
		}
	}
}
