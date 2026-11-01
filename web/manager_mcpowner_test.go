package web

// Per-account MCP credential tests: users.json "mcp_tokens" must give an
// account's sessions a pool of their own, while everyone else — other
// accounts without overrides, owner-less sessions — keeps the shared pool
// and its credential untouched.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/mcpclient"
)

func disabledServer(url string) *mcpclient.ServerConfig {
	off := false
	return &mcpclient.ServerConfig{Enabled: &off, URL: url}
}

func newOwnerTestManager(t *testing.T, servers map[string]*mcpclient.ServerConfig) *Manager {
	t.Helper()
	m := &Manager{mcpPool: mcpclient.NewPool(nil), mcpBase: mcpclient.Config{Servers: servers}}
	m.mcpOwnerPools = map[string]*mcpclient.Pool{}
	return m
}

func TestOwnerMCPConfigOverridesOnlyNamedServers(t *testing.T) {
	base := mcpclient.Config{
		Servers: map[string]*mcpclient.ServerConfig{
			"yw":  {URL: "http://yw/mcp", BearerTokenEnv: "YW_TOKEN", Args: []string{"--x"}},
			"std": {URL: "http://std/mcp", BearerToken: "shared-secret"},
		},
	}
	out := ownerMCPConfig(base, map[string]string{"yw": "account-token"})

	yw := out.Servers["yw"]
	if yw == base.Servers["yw"] {
		t.Fatal("overridden server must be a clone, not the shared pointer")
	}
	if yw.BearerToken != "account-token" || yw.BearerTokenEnv != "" {
		t.Fatalf("override not applied: bearer=%q env=%q", yw.BearerToken, yw.BearerTokenEnv)
	}
	yw.Args[0] = "--mutated"
	if base.Servers["yw"].Args[0] == "--mutated" {
		t.Fatal("clone shares mutable state with the base config")
	}
	if out.Servers["std"] != base.Servers["std"] {
		t.Fatal("servers the account does not name must be shared verbatim")
	}
	if base.Servers["yw"].BearerToken != "" || base.Servers["yw"].BearerTokenEnv != "YW_TOKEN" {
		t.Fatal("base config was mutated by an owner clone")
	}
}

func TestPoolForSelectsOwnerPoolOnlyForOverriddenAccounts(t *testing.T) {
	m := newOwnerTestManager(t, map[string]*mcpclient.ServerConfig{
		"yw": disabledServer("http://yw/mcp"),
	})
	withTokens := identity.LocalScope("alice")
	withoutTokens := identity.LocalScope("bob")

	if got := m.poolFor(nil); got != m.mcpPool {
		t.Fatal("owner-less session must use the shared pool")
	}
	if got := m.poolFor(&withoutTokens); got != m.mcpPool {
		t.Fatal("account without overrides must use the shared pool")
	}

	m.SetMCPUserTokens(func() map[identity.Scope]map[string]string {
		return map[identity.Scope]map[string]string{
			withTokens: {"yw": "alice-token"},
		}
	})

	first := m.poolFor(&withTokens)
	if first == nil || first == m.mcpPool {
		t.Fatal("account with overrides must get its own pool")
	}
	if again := m.poolFor(&withTokens); again != first {
		t.Fatal("pool selection must be stable across sessions")
	}
	if got := m.poolFor(&withoutTokens); got != m.mcpPool {
		t.Fatal("other accounts must still use the shared pool")
	}

	// Dropping the override (account edited) must retire the owner pool.
	m.SetMCPUserTokens(func() map[identity.Scope]map[string]string {
		return nil
	})
	m.mcpOwnerMu.Lock()
	pools := len(m.mcpOwnerPools)
	m.mcpOwnerMu.Unlock()
	if pools != 0 {
		t.Fatalf("owner pool survived the loss of its override: %d left", pools)
	}
}

func TestReconcileMCPReachesOwnerPools(t *testing.T) {
	m := newOwnerTestManager(t, map[string]*mcpclient.ServerConfig{
		"yw": disabledServer("http://yw/mcp"),
	})
	alice := identity.LocalScope("alice")
	m.SetMCPUserTokens(func() map[identity.Scope]map[string]string {
		return map[identity.Scope]map[string]string{alice: {"yw": "alice-token"}}
	})

	// A saved mcp.json edit swaps the base everywhere; the owner pool must be
	// reconciled against the new base, not left on the old one.
	updated := map[string]*mcpclient.ServerConfig{
		"yw":    disabledServer("http://yw-v2/mcp"),
		"other": disabledServer("http://other/mcp"),
	}
	m.ReconcileMCP(context.Background(), mcpclient.Config{Servers: updated})

	m.mcpOwnerMu.Lock()
	pool := m.mcpOwnerPools[alice.SubjectID]
	m.mcpOwnerMu.Unlock()
	if pool == nil {
		t.Fatal("owner pool missing after reconcile")
	}
	if m.mcpBase.Servers["yw"] != updated["yw"] {
		t.Fatal("base config not updated")
	}
}

func TestAuthStoreMCPTokenAccessors(t *testing.T) {
	dir := t.TempDir()
	auth, err := newAuthStore(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatalf("newAuthStore: %v", err)
	}
	auth.mu.Lock()
	auth.users = map[string]userRecord{
		"alice": {Username: "alice", MCPTokens: map[string]string{"yw": "alice-token"}},
		"bob":   {Username: "bob"},
	}
	auth.mu.Unlock()

	if got := auth.mcpTokensFor("alice"); got["yw"] != "alice-token" || len(got) != 1 {
		t.Fatalf("mcpTokensFor(alice) = %v", got)
	}
	if got := auth.mcpTokensFor("bob"); got != nil {
		t.Fatalf("mcpTokensFor(bob) = %v, want nil", got)
	}

	all := auth.allMcpTokens()
	scope := identity.LocalScope("alice")
	tokens, ok := all[scope]
	if !ok || tokens["yw"] != "alice-token" {
		t.Fatalf("allMcpTokens missing alice under her local scope: %v", all)
	}
	if len(all) != 1 {
		t.Fatalf("accounts without overrides must not appear: %v", all)
	}

	// The returned map is a copy: callers cannot mutate the store's view.
	all[scope]["yw"] = "mutated"
	if auth.mcpTokensFor("alice")["yw"] != "alice-token" {
		t.Fatal("allMcpTokens leaked a mutable reference into the store")
	}
}
