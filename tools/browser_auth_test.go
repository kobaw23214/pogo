package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRetiredBrowserActionsNeverAdvertiseOrDispatch(t *testing.T) {
	for _, backend := range []string{BrowserBackendNative, "legacy", "", "future-executor"} {
		for _, action := range []string{"auth_status", "api_read", "eval"} {
			for _, batch := range []bool{false, true} {
				link := &capabilityBrowserLink{backend: backend}
				browser := &Browser{Link: link}
				schema, _ := json.Marshal(browser.InputSchema())
				for _, retired := range []string{`"auth_status"`, `"api_read"`, `"eval"`, `"storage_key"`, `"path"`, `"fields"`, `"expression"`} {
					if strings.Contains(string(schema), retired) || strings.Contains(browser.Description(), retired) {
						t.Fatalf("retired contract exposed: %s", retired)
					}
				}
				raw := `{"action":"` + action + `"}`
				if action == "api_read" {
					raw = `{"action":"api_read","path":"/api/orders","storage_key":"access_token"}`
				}
				if action == "eval" {
					raw = `{"action":"eval","expression":"fetch('/api/orders')"}`
				}
				if batch {
					raw = `{"action":"batch","actions":[{"action":"click","target":"e1"},` + raw + `]}`
				}
				if err := browser.ValidateArgs(json.RawMessage(raw)); err == nil {
					t.Fatalf("retired operation validated: %s", raw)
				}
				if _, err := browser.Execute(context.Background(), json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), "retired") || len(link.ops) != 0 {
					t.Fatalf("retired operation dispatched: %s err=%v ops=%s", raw, err, link.ops)
				}
			}
		}
	}
}
func TestUnadvertisedBrowserExecutorCannotDispatch(t *testing.T) {
	for _, backend := range []string{"legacy", "", "future-executor"} {
		link := &capabilityBrowserLink{backend: backend}
		if _, err := (&Browser{Link: link}).Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`)); err == nil || !strings.Contains(err.Error(), "unavailable") || len(link.ops) != 0 {
			t.Fatalf("unsupported executor dispatched: %q %v", backend, err)
		}
	}
}
