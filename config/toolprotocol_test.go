package config

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/yosukeno/pogo/llm"
)

func TestCompatibilityConfigurationRoundTrip(t *testing.T) {
	savedP, savedM := providers, catalog
	t.Cleanup(func() { providers, catalog = savedP, savedM })
	for _, compat := range []llm.Compatibility{
		{}, {PreserveReasoning: true},
		{ToolCalls: "xml", AllowMCPAliases: true, PreserveReasoning: true},
		{ToolCalls: "json", ToolHistory: "text"},
		{ToolCalls: "xml", AllowTextPreamble: true},
	} {
		f := fileConfig{Providers: map[string]providerFile{"p": {BaseURL: "http://127.0.0.1:1"}},
			Models: []modelFile{{ID: "m", Provider: "p", Compatibility: compat}}}
		raw, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		var decoded fileConfig
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		ps, ms, _, _, _, err := merge(map[string]Provider{}, nil, decoded)
		if err != nil {
			t.Fatal(err)
		}
		providers, catalog = ps, ms
		resolved, err := Resolve("m")
		if err != nil || !reflect.DeepEqual(resolved.Compatibility, compat) {
			t.Fatalf("lost compatibility: %+v, %v", resolved.Compatibility, err)
		}
	}
	for _, compat := range []llm.Compatibility{
		{ToolCalls: "guess"}, {ToolHistory: "text"}, {ToolHistory: "guess"}, {AllowMCPAliases: true},
		{AllowTextPreamble: true},
	} {
		_, _, _, _, _, err := merge(map[string]Provider{}, nil, fileConfig{
			Providers: map[string]providerFile{"p": {BaseURL: "http://127.0.0.1:1"}},
			Models:    []modelFile{{ID: "m", Provider: "p", Compatibility: compat}},
		})
		if err == nil {
			t.Fatalf("accepted invalid compatibility: %+v", compat)
		}
	}
}
