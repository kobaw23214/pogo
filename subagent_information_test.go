package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/information"
	"github.com/yosukeno/pogo/tools"
)

func TestInformationCapabilityOnlyReachesTrustedChildModes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "shared")
	scope := strings.Repeat("a", 64)
	if _, err := information.Open(root, scope); err != nil {
		t.Fatal(err)
	}
	t.Setenv(tools.EnvInformationRoot, root)
	t.Setenv(tools.EnvInformationScope, scope)
	for _, depth := range []string{"", "1"} {
		for _, readonly := range []string{"", "1"} {
			t.Setenv(tools.EnvSubagentDepth, depth)
			t.Setenv(tools.EnvSubagentReadOnly, readonly)
			options := toolOptions(t.TempDir(), "test", nil, nil, nil, nil, nil)
			registry := tools.New(options)
			_, available := registry.Get(tools.InformationToolName)
			if available != (depth == "1") {
				t.Fatalf("depth=%q readonly=%q information=%v", depth, readonly, available)
			}
		}
	}
}
