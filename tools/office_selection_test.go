package tools

import "testing"

func TestOfficeSelectionReadNeverRequiresMutationSemantics(t *testing.T) {
	if !officeReadOperation("selection.read") {
		t.Fatal("selection capture was classified as an edit")
	}
	if !officeSupportedOperation("selection.read") {
		t.Fatal("fixed selection read was not recognized")
	}
}
