package tools

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

type fdCapabilityLink struct {
	recordingLink
	backend string
}

func (l *fdCapabilityLink) BrowserBackend() string { return l.backend }

func TestBrowserFDBackendSurvivesRealChildChannel(t *testing.T) {
	if os.Getenv("POGO_TEST_BROWSER_CAPABILITY_CHILD") == "1" {
		child := OpenFDBrowser()
		if child == nil {
			os.Exit(3)
		}
		fmt.Print(BrowserBackendOf(child))
		os.Exit(0)
	}
	for _, backend := range []string{BrowserBackendNative, "legacy", ""} {
		t.Run("backend-"+backend, func(t *testing.T) {
			s := &Subagent{Browser: &fdCapabilityLink{backend: backend}}
			cmd := exec.Command(os.Args[0], "-test.run=^TestBrowserFDBackendSurvivesRealChildChannel$")
			cmd.Env = append(os.Environ(), "POGO_TEST_BROWSER_CAPABILITY_CHILD=1", EnvBrowserBackend+"=stale-inherited-value")
			cleanup, err := s.wireBrowser(cmd, childSpec{id: "capability-child"})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			output, err := cmd.Output()
			if err != nil || strings.TrimSpace(string(output)) != BrowserBackendOf(&fdCapabilityLink{backend: backend}) {
				t.Fatalf("backend propagation failed: output=%q err=%v", output, err)
			}
		})
	}
}

func TestRegistryBrowserImageModeRequiresRealHelper(t *testing.T) {
	for _, helper := range []bool{false, true} {
		o := Options{Cwd: t.TempDir(), Browser: &fdCapabilityLink{backend: BrowserBackendNative}, BrowserImageMode: BrowserImageNone}
		if helper {
			o.Images = &ImageAnalyze{}
		}
		tool, ok := New(o).Get("browser")
		if !ok {
			t.Fatal("browser missing")
		}
		mode := tool.(*Browser).ImageMode
		if helper && mode != BrowserImageHelper || !helper && mode != BrowserImageNone {
			t.Fatalf("helper=%v got mode=%q", helper, mode)
		}
	}
}
