package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSandboxCacheEnablesAfterOffWithoutLosingDeploymentBaseline(t *testing.T) {
	previous := SandboxOverride()
	t.Cleanup(func() { _ = SetSandboxOverride(previous) })
	_ = SetSandboxOverride("")
	var cache sandboxPolicyCache
	reads, probes := 0, 0
	read := func() SandboxPolicy { reads++; return SandboxPolicy{Mode: SandboxOff} }
	probe := func(p SandboxPolicy) SandboxPolicy {
		probes++
		if p.Mode == SandboxOff {
			t.Fatal("off policy reached the active probe")
		}
		p.Bwrap, p.Proc = "/usr/bin/bwrap", true
		return p
	}
	if p := cache.resolve(read, probe); p.Mode != SandboxOff || probes != 0 {
		t.Fatal("off baseline unexpectedly probed")
	}
	_ = SetSandboxOverride(SandboxEnforce)
	if p := cache.resolve(read, probe); !p.Active() || !p.Proc || probes != 1 {
		t.Fatal("enabling after off never discovered working confinement")
	}
	_ = SetSandboxOverride("")
	if p := cache.resolve(read, probe); p.Mode != SandboxOff || p.Active() || p.Bwrap == "" {
		t.Fatal("clearing override lost deployment baseline or observed capability")
	}
	_ = SetSandboxOverride(SandboxDegrade)
	if p := cache.resolve(read, probe); !p.Active() || probes != 1 || reads != 1 {
		t.Fatal("changing operator mode repeated or bypassed the probe")
	}
}

func TestSandboxCacheRestoredOverrideIsAppliedBeforeFirstProbe(t *testing.T) {
	previous := SandboxOverride()
	t.Cleanup(func() { _ = SetSandboxOverride(previous) })
	_ = SetSandboxOverride(SandboxEnforce)
	var cache sandboxPolicyCache
	p := cache.resolve(func() SandboxPolicy { return SandboxPolicy{Mode: SandboxOff} }, func(p SandboxPolicy) SandboxPolicy {
		if p.Mode != SandboxEnforce {
			t.Fatal("startup override was not applied before capability detection")
		}
		p.Bwrap = "/usr/bin/bwrap"
		return p
	})
	if !p.Active() {
		t.Fatal("restored enforce override falsely reports missing bwrap")
	}
}

func TestSandboxCacheConcurrentCallsShareOneCompleteProbe(t *testing.T) {
	previous := SandboxOverride()
	t.Cleanup(func() { _ = SetSandboxOverride(previous) })
	_ = SetSandboxOverride("")
	var cache sandboxPolicyCache
	var reads, probes atomic.Int32
	var wg sync.WaitGroup
	policies := make(chan SandboxPolicy, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			policies <- cache.resolve(func() SandboxPolicy { reads.Add(1); return SandboxPolicy{Mode: SandboxEnforce} }, func(p SandboxPolicy) SandboxPolicy {
				probes.Add(1)
				p.Bwrap, p.Proc = "/usr/bin/bwrap", true
				return p
			})
		}()
	}
	wg.Wait()
	close(policies)
	for p := range policies {
		if !p.Active() || !p.Proc {
			t.Fatal("a concurrent caller observed a partial probe")
		}
	}
	if reads.Load() != 1 || probes.Load() != 1 {
		t.Fatalf("read=%d probe=%d", reads.Load(), probes.Load())
	}
}

func TestPrivateStorageRefusalReportsCauseWithoutOfferingABypass(t *testing.T) {
	for _, tc := range []struct{ mode, bwrap, unavailable, reason string }{
		{SandboxOff, "/usr/bin/bwrap", "", "effective POGO_SANDBOX mode is off"},
		{SandboxEnforce, "", "", "not found on PATH"},
		{SandboxDegrade, "/usr/bin/bwrap", "bwrap: Creating new namespace failed: Operation not permitted", "Creating new namespace failed"},
	} {
		t.Run(tc.mode+tc.reason, func(t *testing.T) {
			p := SandboxPolicy{Mode: tc.mode, Bwrap: tc.bwrap, Unavailable: tc.unavailable, PrivateDirs: []string{"/private-control-canary"}}
			if !p.Refuses() {
				t.Fatal("required storage isolation must fail closed")
			}
			result, err := commandUnavailableResult(p.RefusalError())
			var typed *sandboxUnavailableError
			if !errors.As(err, &typed) {
				t.Fatal("lost typed host capability error")
			}
			var diagnostic CommandUnavailableDetails
			if result.Payload == nil || json.Unmarshal(result.Payload.Structured, &diagnostic) != nil {
				t.Fatal("diagnostic not preserved as structured evidence")
			}
			if diagnostic.Code != "SANDBOX_UNAVAILABLE" || diagnostic.RequestExecuted || diagnostic.Retryable || !strings.Contains(diagnostic.Reason, tc.reason) {
				t.Fatalf("wrong diagnostic: %+v", diagnostic)
			}
			if strings.Contains(result.Text, "/private-control-canary") {
				t.Fatal("private control paths leaked into the tool result")
			}
			if strings.Contains(diagnostic.Recovery, "POGO_SANDBOX=degrade") || strings.Contains(diagnostic.Recovery, "POGO_SANDBOX=off") {
				t.Fatal("private-store refusal suggested weakening confinement")
			}
		})
	}
}

func TestCommandUnavailableResultDoesNotInventAnExitOrReclassifyScriptErrors(t *testing.T) {
	result, err := commandUnavailableResult(fmt.Errorf("host: %w", ErrScopedCommandUnavailable))
	if !errors.Is(err, ErrScopedCommandUnavailable) || result.Payload == nil {
		t.Fatal("session-isolation cause lost")
	}
	var raw map[string]any
	_ = json.Unmarshal(result.Payload.Structured, &raw)
	if raw["request_executed"] != false || raw["retryable"] != false || raw["exit_code"] != nil {
		t.Fatalf("unexecuted request became a command failure: %#v", raw)
	}
	scriptError := errors.New("script exited with code 1")
	result, err = commandUnavailableResult(scriptError)
	if err != scriptError || result.Payload != nil || result.Details != nil || result.Text != "" {
		t.Fatal("script error was misclassified as sandbox capability failure")
	}
}

func TestBashRefusedBeforeExecutionPreservesDiagnosticAndNoSideEffects(t *testing.T) {
	previous := SandboxOverride()
	t.Cleanup(func() { _ = SetSandboxOverride(previous) })
	_ = SetSandboxOverride(SandboxOff)
	t.Setenv(EnvCommandProtectionManifest, "")
	unregister := RegisterHostPrivateDirs([]string{t.TempDir()})
	defer unregister()
	dir := t.TempDir()
	marker := filepath.Join(dir, "must-not-exist")
	raw, _ := json.Marshal(map[string]any{"command": "printf unsafe > must-not-exist"})
	result, err := (&Bash{Cwd: dir}).Execute(context.Background(), raw)
	if err == nil || result.Payload == nil {
		t.Fatal("refused command did not return its capability diagnostic")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("refused command ran")
	}
	if diagnostic, ok := result.Details.(CommandUnavailableDetails); !ok || diagnostic.RequestExecuted || diagnostic.Retryable {
		t.Fatalf("unexpected refusal details: %#v", result.Details)
	}
}

func TestBashTimeoutContractExposesBoundWithoutIncreasingRuntimeCeiling(t *testing.T) {
	bash := &Bash{}
	for _, schema := range []map[string]any{bash.InputSchema(), bash.Schema()} {
		timeout := schema["properties"].(map[string]any)["timeout"].(map[string]any)
		if timeout["maximum"] != MaxBashTimeout.Seconds() || timeout["minimum"] != 0 {
			t.Fatalf("timeout bound missing: %#v", timeout)
		}
	}
	if !strings.Contains(bash.Description(), "searches and retry loops bounded") {
		t.Fatal("generated searches have no bounded-execution guidance")
	}
}
