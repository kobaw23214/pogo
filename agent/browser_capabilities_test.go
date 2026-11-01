package agent

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

type capabilityBrowserLink struct{}

func (capabilityBrowserLink) BrowserBackend() string { return tools.BrowserBackendNative }
func (capabilityBrowserLink) Execute(context.Context, json.RawMessage) (tools.BrowserReply, error) {
	return tools.BrowserReply{OK: true}, nil
}

func TestBrowserModelVisionRefreshesActualSchemasAndAccounting(t *testing.T) {
	b := &tools.Browser{Link: capabilityBrowserLink{}, ImageMode: tools.BrowserImageNone}
	client := &viewClient{}
	a := New(Config{Client: client, Registry: tools.NewRegistry(b)})
	a.SetMessages([]llm.Message{llm.UserText("keep history")})
	a.SetUsage(llm.Usage{Input: 17}, llm.Usage{})
	a.lastInput, a.measuredThrough = 100, len(a.messages)
	before := a.OverheadTokens()
	if err := a.SetBrowserModelVision(true); err != nil {
		t.Fatal(err)
	}
	if a.OverheadTokens() <= before || a.measuredThrough != math.MaxInt || len(a.Messages()) != 1 || a.Usage().Input != 17 {
		t.Fatal("model switch lost history/usage or retained stale schema accounting")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for range a.Run(ctx, "inspect capability") {
	}
	wire, _ := llm.ToolsWireJSON(client.schemas[0])
	if !strings.Contains(string(wire), `"screenshot"`) || strings.Contains(string(wire), `"eval"`) || !strings.Contains(string(wire), `"devtools"`) {
		t.Fatalf("model received wrong native visual capabilities: %s", wire)
	}
	if err := a.SetBrowserModelVision(false); err != nil {
		t.Fatal(err)
	}
	wire, _ = llm.ToolsWireJSON(a.schemas)
	if strings.Contains(string(wire), `"screenshot"`) || strings.Contains(string(wire), `"x":`) {
		t.Fatal("text-only switch retained image-only operations")
	}
}

func TestBrowserVisionUsesOnlyRegisteredHelperAndRefusesActiveRunChanges(t *testing.T) {
	for _, helper := range []bool{false, true} {
		b := &tools.Browser{Link: capabilityBrowserLink{}, ImageMode: tools.BrowserImageDirect}
		registry := tools.NewRegistry(b)
		if helper {
			registry = tools.NewRegistry(b, &tools.ImageAnalyze{})
		}
		a := New(Config{Client: &viewClient{}, Registry: registry})
		a.running = true
		if err := a.SetBrowserModelVision(false); err == nil || b.ImageMode != tools.BrowserImageDirect {
			t.Fatal("active run allowed changing the browser contract")
		}
		a.running = false
		if err := a.SetBrowserModelVision(false); err != nil {
			t.Fatal(err)
		}
		want := tools.BrowserImageNone
		if helper {
			want = tools.BrowserImageHelper
		}
		if b.ImageMode != want {
			t.Fatalf("helper=%v got mode %q want %q", helper, b.ImageMode, want)
		}
	}
}
