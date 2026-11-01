package web

import (
	"net/http"
	"testing"

	"github.com/yosukeno/pogo/prefs"
	"github.com/yosukeno/pogo/tools"
)

// The runtime switch is a deliberate escape hatch: the setting has to round
// trip through the endpoint, land in prefs.json, and take effect immediately —
// and a bad value has to bounce off without touching the installed one.
func TestSandboxEndpointRoundTrip(t *testing.T) {
	sweepEnv(t) // temp POGO_PREFS_FILE + spill dir
	front := prefsTestServer(t)
	t.Cleanup(func() { _ = tools.SetSandboxOverride("") })

	// InitSandbox caches the startup policy. The test may run in a process
	// whose baseline is enforce; clearing an override must restore that same
	// effective policy rather than assume an unset environment.
	code, got := prefsRequest(t, front, "GET", "/api/sandbox", "")
	if code != http.StatusOK || got["override"] != "" || got["env_mode"] != tools.SandboxPolicyFromEnv().Mode {
		t.Fatalf("baseline: %d %+v", code, got)
	}

	baselineEffective := got["effective"]

	// Turning it off takes effect at once and persists.
	code, got = prefsRequest(t, front, "PUT", "/api/sandbox", `{"mode":"off"}`)
	if code != http.StatusOK || got["override"] != "off" || got["effective"] != "off" {
		t.Fatalf("put off: %d %+v", code, got)
	}
	if got["active"] != false {
		t.Fatalf("off must report inactive: %+v", got)
	}
	p, err := prefs.Load(prefs.Path())
	if err != nil || p.SandboxMode != "off" {
		t.Fatalf("the choice must land in prefs.json: %+v (%v)", p, err)
	}

	// An unknown mode is rejected and the installed override survives.
	code, _ = prefsRequest(t, front, "PUT", "/api/sandbox", `{"mode":"paranoid"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("invalid mode: %d, want 400", code)
	}
	if tools.SandboxOverride() != "off" {
		t.Fatalf("a rejected mode must not clobber the installed one: %q", tools.SandboxOverride())
	}

	// Clearing returns to the env baseline.
	code, got = prefsRequest(t, front, "PUT", "/api/sandbox", `{"mode":""}`)
	if code != http.StatusOK || got["override"] != "" || got["effective"] != baselineEffective {
		t.Fatalf("clear: %d %+v", code, got)
	}
}
