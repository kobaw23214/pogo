package tools

import (
	"os"
	"strings"
	"testing"
)

func TestHostPrivateDirectoriesStripControlSecretEnv(t *testing.T) {
	const serviceCanary = "test-only-service-control-canary"
	const runnerCanary = "test-only-runner-control-canary"
	const providerCanary = "test-only-model-provider-canary"
	t.Setenv("POGO_WEB_SERVICE_RUNNER_TOKEN", serviceCanary)
	t.Setenv("POGO_WEB_RUNNER_TOKEN", runnerCanary)
	t.Setenv("KIMI_API_KEY", providerCanary)
	child := hostPrivateChildEnv([]string{
		"PATH=/bin",
		"POGO_WEB_SERVICE_RUNNER_TOKEN=" + serviceCanary,
		"POGO_WEB_RUNNER_TOKEN=" + runnerCanary,
		"KIMI_API_KEY=" + providerCanary,
	})
	providerPresent := false
	for _, entry := range child {
		name, value, _ := strings.Cut(entry, "=")
		if isHostControlSecretEnv(name) || value == serviceCanary || value == runnerCanary {
			t.Fatal("host control credential reached a child environment")
		}
		if name == "KIMI_API_KEY" && value == providerCanary {
			providerPresent = true
		}
	}
	if !providerPresent {
		t.Fatal("existing child model-provider environment was removed")
	}
	if os.Getenv("POGO_WEB_SERVICE_RUNNER_TOKEN") != serviceCanary || os.Getenv("POGO_WEB_RUNNER_TOKEN") != runnerCanary {
		t.Fatal("parent host control environment was changed")
	}
}

func TestSandboxControlSecretsCannotBeWhitelisted(t *testing.T) {
	const serviceCanary = "test-only-sandbox-service-canary"
	const runnerCanary = "test-only-sandbox-runner-canary"
	const providerCanary = "test-only-sandbox-provider-canary"
	t.Setenv("POGO_WEB_SERVICE_RUNNER_TOKEN", serviceCanary)
	t.Setenv("POGO_WEB_RUNNER_TOKEN", runnerCanary)
	t.Setenv("KIMI_API_KEY", providerCanary)
	pass := sandboxEnvPass("JAVA_HOME,POGO_WEB_SERVICE_RUNNER_TOKEN,POGO_WEB_RUNNER_TOKEN,KIMI_API_KEY")
	for _, name := range pass {
		if isHostControlSecretEnv(name) {
			t.Fatal("host control credential can be added to the sandbox allowlist")
		}
	}
	// Bypass the normal allowlist builder to verify the actual launch and
	// override layers independently reject the two reserved secret names.
	pass = append(pass, "POGO_WEB_SERVICE_RUNNER_TOKEN", "POGO_WEB_RUNNER_TOKEN")
	env := sandboxEnv(pass, map[string]string{"POGO_WEB_SERVICE_RUNNER_TOKEN": "attempted-override", "POGO_WEB_RUNNER_TOKEN": "attempted-override"})
	providerPresent := false
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		if isHostControlSecretEnv(name) || value == serviceCanary || value == runnerCanary || value == "attempted-override" {
			t.Fatal("host control credential reached the sandbox launcher")
		}
		if name == "KIMI_API_KEY" && value == providerCanary {
			providerPresent = true
		}
	}
	if !providerPresent {
		t.Fatal("unrelated administrator environment allowance changed")
	}
	policy := SandboxPolicy{Mode: SandboxEnforce, Bwrap: "/usr/bin/bwrap", Net: true, Proc: true, EnvPass: pass}
	args := policy.bwrapArgv("/workspace", "/workspace", "/tmp", func(string) bool { return true }, nil, []string{"env"})
	for i := 0; i+2 < len(args); i++ {
		if args[i] == "--setenv" && (isHostControlSecretEnv(args[i+1]) || args[i+2] == serviceCanary || args[i+2] == runnerCanary) {
			t.Fatal("host control credential reached a bwrap setenv argument")
		}
	}
	// The stricter scoped executor retains its existing fixed whitelist too.
	scoped := scopedEnvironment([]string{"POGO_WEB_SERVICE_RUNNER_TOKEN=" + serviceCanary, "POGO_WEB_RUNNER_TOKEN=" + runnerCanary}, "/workspace")
	for _, entry := range scoped {
		name, _, _ := strings.Cut(entry, "=")
		if isHostControlSecretEnv(name) {
			t.Fatal("host control credential reached a scoped command")
		}
	}
	scopedArgs := scopedBwrapArgs(commandScopeSpec{cwd: "/workspace", workdir: "/workspace", env: []string{"POGO_WEB_SERVICE_RUNNER_TOKEN=" + serviceCanary, "POGO_WEB_RUNNER_TOKEN=" + runnerCanary}}, nil, []string{"env"})
	for i := 0; i+2 < len(scopedArgs); i++ {
		if scopedArgs[i] == "--setenv" && (isHostControlSecretEnv(scopedArgs[i+1]) || scopedArgs[i+2] == serviceCanary || scopedArgs[i+2] == runnerCanary) {
			t.Fatal("host control credential reached a scoped bwrap setenv argument")
		}
	}
}
