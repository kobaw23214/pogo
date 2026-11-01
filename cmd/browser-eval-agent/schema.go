package main

import (
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// The registry and its audit bytes come from the same instances consumed by
// agent.New. ToolsWireJSON also renders the actual provider's native tool array,
// including descriptions; JSON-map re-encoding would not preserve that order.
func browserRegistry(link tools.BrowserLink, spill string, seesImages bool) (*tools.Registry, []byte, error) {
	return browserRegistryForProfile(link, spill, seesImages, evaluationProfile{id: generalWebProfile})
}

func browserRegistryForProfile(link tools.BrowserLink, spill string, seesImages bool, selected evaluationProfile) (*tools.Registry, []byte, error) {
	if selected.auth || selected.id == authProfile || selected.id == authAutoProfile {
		return nil, nil, errRetiredAuthProfile
	}
	mode := tools.BrowserImageNone
	if seesImages {
		mode = tools.BrowserImageDirect
	}
	browser := &tools.Browser{Link: link, SpillDir: spill, ImageMode: mode}
	var exposed tools.Tool = browser
	if selected.id != generalWebProfile && selected.id != browserUseV2Profile {
		// Historical scores must not acquire new diagnosis or script execution
		// simply because the production browser gained those capabilities.
		exposed = &historicalBrowser{Browser: browser}
	}
	registry := tools.NewRegistry(exposed)
	schemas := make([]llm.ToolSchema, 0, len(registry.All()))
	for _, tool := range registry.All() {
		schemas = append(schemas, llm.ToolSchema{Name: tool.Name(), Description: tool.Description(), InputSchema: tool.InputSchema()})
	}
	wire, err := llm.ToolsWireJSON(schemas)
	return registry, wire, err
}
