package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/tools"
)

type profileBrowserLink struct{ ops []json.RawMessage }

func (*profileBrowserLink) BrowserBackend() string { return tools.BrowserBackendNative }
func (l *profileBrowserLink) Execute(_ context.Context, raw json.RawMessage) (tools.BrowserReply, error) {
	l.ops = append(l.ops, raw)
	return tools.BrowserReply{OK: true}, nil
}

func TestHistoricalProfilesKeepTheirBrowserActionBoundary(t *testing.T) {
	for _, id := range []string{profile, generalWebProfile, browserUseV2Profile} {
		t.Run(id, func(t *testing.T) {
			link := &profileBrowserLink{}
			registry, wire, err := browserRegistryForProfile(link, t.TempDir(), false, evaluationProfile{id: id})
			if err != nil {
				t.Fatal(err)
			}
			live := id == generalWebProfile || id == browserUseV2Profile
			assertNativeWire(t, wire, false, live)
			browser, _ := registry.Get("browser")
			props := browser.InputSchema()["properties"].(map[string]any)
			for _, field := range []string{"method", "params"} {
				if _, exists := props[field]; exists != live {
					t.Fatalf("profile %s exposes %s incorrectly", id, field)
				}
			}
			if !live && (strings.Contains(browser.Description(), "devtools") || strings.Contains(browser.Description(), "Runtime.evaluate")) {
				t.Fatal("historical tool description advertises new diagnostics")
			}
			for _, raw := range []string{`{"action":"devtools","method":"help"}`, `{"action":"eval","expression":"document.title"}`} {
				before := len(link.ops)
				err := tools.ValidateArgs(browser, json.RawMessage(raw))
				supported := live && !strings.Contains(raw, `"eval"`)
				if (err == nil) != supported {
					t.Fatalf("profile validation did not match declaration: %s / %v", raw, err)
				}
				_, err = browser.Execute(context.Background(), json.RawMessage(raw))
				if supported {
					if err != nil || len(link.ops) != before+1 {
						t.Fatalf("general web lost production diagnostics: %v", err)
					}
				} else if err == nil || len(link.ops) != before {
					t.Fatal("historical profile dispatched a forbidden operation")
				}
				batch := json.RawMessage(`{"action":"batch","actions":[{"action":"click","target":"e1"},` + raw + `]}`)
				before = len(link.ops)
				if err := tools.ValidateArgs(browser, batch); err == nil {
					t.Fatal("profile accepted diagnostics inside a native batch")
				}
				if _, err := browser.Execute(context.Background(), batch); err == nil || len(link.ops) != before {
					t.Fatal("profile partly dispatched a diagnostic batch")
				}
			}
			for _, action := range []string{tools.BrowserActionSnapshot, tools.BrowserActionCalculate} {
				if !slices.Contains(props["action"].(map[string]any)["enum"].([]string), action) {
					t.Fatalf("existing action %s was lost", action)
				}
			}
			if _, err := browser.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`)); err != nil {
				t.Fatalf("ordinary browser operation broke: %v", err)
			}
		})
	}
}

func TestRetiredAuthProfilesCannotConstructBrowserRegistry(t *testing.T) {
	for _, id := range []string{authProfile, authAutoProfile} {
		link := &profileBrowserLink{}
		registry, wire, err := browserRegistryForProfile(link, t.TempDir(), false, evaluationProfile{id: id})
		if !errors.Is(err, errRetiredAuthProfile) || registry != nil || wire != nil || len(link.ops) != 0 {
			t.Fatalf("retired profile constructed an executable registry: %s %v", id, err)
		}
	}
}
