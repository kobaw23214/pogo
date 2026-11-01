package mcpclient

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func boolP(b bool) *bool { return &b }

func TestValidateServerMatrix(t *testing.T) {
	valid := func() *ServerConfig { return &ServerConfig{Command: "srv", Args: []string{"--stdio"}} }
	cases := []struct {
		name    string
		server  string
		cfg     *ServerConfig
		wantErr string // "" means valid
	}{
		{"ok stdio", "srv", valid(), ""},
		{"ok url", "srv", &ServerConfig{URL: "https://example.com/mcp"}, ""},
		{"ok url sse", "srv", &ServerConfig{URL: "http://127.0.0.1:8080/sse", Transport: "sse"}, ""},
		{"bad server name space", "my srv", valid(), "invalid"},
		{"bad server name too long", strings.Repeat("a", 33), valid(), "invalid"},
		{"bad server name dot", "my.srv", valid(), "invalid"}, // dots are legal in tool names, not in the wire-name prefix
		{"both command and url", "srv", &ServerConfig{Command: "x", URL: "https://e.com"}, "exactly one"},
		{"neither command nor url", "srv", &ServerConfig{}, "exactly one"},
		{"url bad scheme", "srv", &ServerConfig{URL: "ftp://e.com/mcp"}, "http"},
		{"url no scheme", "srv", &ServerConfig{URL: "example.com/mcp"}, "http"},
		{"url no host", "srv", &ServerConfig{URL: "http:///mcp"}, "no host"},
		{"bad transport", "srv", &ServerConfig{URL: "https://e.com", Transport: "grpc"}, "transport"},
		{"stdio with transport", "srv", &ServerConfig{Command: "x", Transport: "sse"}, "only apply to url"},
		{"stdio with headers", "srv", &ServerConfig{Command: "x", Headers: map[string]string{"X": "y"}}, "only apply to url"},
		{"stdio with bearer", "srv", &ServerConfig{Command: "x", BearerTokenEnv: "T"}, "only apply to url"},
		{"url with args", "srv", &ServerConfig{URL: "https://e.com", Args: []string{"a"}}, "only apply to command"},
		{"url with env", "srv", &ServerConfig{URL: "https://e.com", Env: map[string]string{"A": "b"}}, "only apply to command"},
		{"url with cwd", "srv", &ServerConfig{URL: "https://e.com", Cwd: "/tmp"}, "only apply to command"},
		{"bearer env is a name", "srv", &ServerConfig{URL: "https://e.com", BearerTokenEnv: "MY_TOKEN"}, ""},
		{"bearer env pasted token", "srv", &ServerConfig{URL: "https://e.com", BearerTokenEnv: "sk-live-abc123"}, "name of an environment variable"},
		{"bearer token inline ok", "srv", &ServerConfig{URL: "https://e.com", BearerToken: "sk-live-abc123"}, ""},
		{"both bearer spellings", "srv", &ServerConfig{URL: "https://e.com", BearerToken: "x", BearerTokenEnv: "T"}, "exactly one"},
		{"bearer token dollar prefix", "srv", &ServerConfig{URL: "https://e.com", BearerToken: "$MY_TOKEN"}, "bearer_token_env"},
		{"bearer token whitespace", "srv", &ServerConfig{URL: "https://e.com", BearerToken: "   "}, "whitespace"},
		{"stdio with inline bearer", "srv", &ServerConfig{Command: "x", BearerToken: "x"}, "only apply to url"},
		{"include wildcard", "srv", &ServerConfig{Command: "x", IncludeTools: []string{"mal.*", "echo"}}, ""},
		{"include bad glob", "srv", &ServerConfig{Command: "x", IncludeTools: []string{"*echo"}}, "invalid"},
		{"include double wildcard", "srv", &ServerConfig{Command: "x", IncludeTools: []string{"a*b"}}, "invalid"},
		{"exclude empty pattern", "srv", &ServerConfig{Command: "x", ExcludeTools: []string{""}}, "invalid"},
		{"null entry", "srv", nil, "null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Config{Servers: map[string]*ServerConfig{tc.server: tc.cfg}}.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestTimeoutDefaultsAndClamps(t *testing.T) {
	c := &ServerConfig{}
	if got := c.StartupTimeout(); got != 10*time.Second {
		t.Fatalf("default startup = %v, want 10s", got)
	}
	if got := c.ToolTimeout(); got != 120*time.Second {
		t.Fatalf("default tool = %v, want 120s", got)
	}
	c.StartupTimeoutMs, c.ToolTimeoutMs = 1, 1<<30
	if got := c.StartupTimeout(); got != time.Second {
		t.Fatalf("clamped startup = %v, want 1s floor", got)
	}
	if got := c.ToolTimeout(); got != time.Hour {
		t.Fatalf("clamped tool = %v, want 1h ceiling", got)
	}
}

func TestIsEnabledAndAllows(t *testing.T) {
	c := &ServerConfig{}
	if !c.IsEnabled() {
		t.Fatal("unset enabled must default to true")
	}
	if (&ServerConfig{Enabled: boolP(false)}).IsEnabled() {
		t.Fatal("explicit false must be disabled")
	}
	c = &ServerConfig{IncludeTools: []string{"mal.*"}, ExcludeTools: []string{"mal.exec"}}
	for tool, want := range map[string]bool{
		"mal.search": true, "mal.exec": false, "echo": false,
	} {
		if got := c.Allows(tool); got != want {
			t.Errorf("Allows(%q) = %v, want %v (exclude wins over include)", tool, got, want)
		}
	}
	// No include list: everything not excluded passes.
	c = &ServerConfig{ExcludeTools: []string{"danger*"}}
	if !c.Allows("echo") || c.Allows("danger_zone") {
		t.Error("exclude-only filter misbehaving")
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Servers != nil {
		t.Fatalf("missing file should load as the empty config, got %#v", c.Servers)
	}
}

func TestLoadStrictAndBroken(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "unknown-field.json")
	if err := os.WriteFile(bad, []byte(`{"servers":{"a":{"command":"x","comand":"typo"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field must be an error (strict decode), got %v", err)
	}

	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte(`{"servers":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(broken); err == nil {
		t.Fatal("corrupt file must be an error, not a silent empty config")
	}

	invalid := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(invalid, []byte(`{"servers":{"a":{"command":"x","url":"https://e.com"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(invalid); err == nil {
		t.Fatal("a file failing Validate must be an error")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "mcp.json")
	in := Config{Servers: map[string]*ServerConfig{
		"local": {Command: "srv", Args: []string{"--stdio"}, Env: map[string]string{"HOME_DIR": "$HOME"},
			ToolTimeoutMs: 5000, IncludeTools: []string{"a.*"}, Trust: true, Required: true},
		"remote": {URL: "https://mcp.example.com/mcp", Transport: "sse",
			Headers: map[string]string{"X-Team": "research"}, BearerTokenEnv: "MCP_TOKEN", Enabled: boolP(false)},
	}}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Servers) != 2 {
		t.Fatalf("round trip lost servers: %#v", out.Servers)
	}
	if got := out.Servers["local"]; got.Command != "srv" || !got.Trust || !got.Required || got.ToolTimeoutMs != 5000 {
		t.Fatalf("local = %#v", got)
	}
	if got := out.Servers["remote"]; got.Transport != "sse" || got.BearerTokenEnv != "MCP_TOKEN" || got.IsEnabled() {
		t.Fatalf("remote = %#v", got)
	}
	// The file holds the deployment's command lines: not world-readable.
	// Windows has no Unix permission bits (ACLs instead), so the mode reads
	// back as 0666 there — assert the 0600 intent only where it exists.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("file mode = %o, want 0600", perm)
		}
	}
}

func TestSaveValidates(t *testing.T) {
	err := Save(filepath.Join(t.TempDir(), "mcp.json"), Config{Servers: map[string]*ServerConfig{"bad name!": {Command: "x"}}})
	if err == nil {
		t.Fatal("Save must validate before writing")
	}
}

func TestPathEnvOverride(t *testing.T) {
	t.Setenv(EnvFile, "/tmp/somewhere/mcp.json")
	if got := Path(); got != "/tmp/somewhere/mcp.json" {
		t.Fatalf("Path() = %q", got)
	}
}

func TestValidateServeSectionMatrix(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *ServeConfig
		wantErr string // "" means valid
	}{
		{"empty section", &ServeConfig{}, ""},
		{"enabled only", &ServeConfig{Enabled: true}, ""},
		{"token inline ok", &ServeConfig{Token: "sk-live-abc123"}, ""},
		{"token env ok", &ServeConfig{TokenEnv: "MY_MCP_TOKEN"}, ""},
		{"both token spellings", &ServeConfig{Token: "x", TokenEnv: "T"}, "exactly one"},
		{"token dollar prefix", &ServeConfig{Token: "$MY_TOKEN"}, "starts with '$'"},
		{"token whitespace", &ServeConfig{Token: "   "}, "whitespace"},
		{"token env pasted token", &ServeConfig{TokenEnv: "sk-live-abc123"}, "name of an environment variable"},
		{"policy strict", &ServeConfig{PolicyRaw: "strict"}, ""},
		{"policy standard", &ServeConfig{PolicyRaw: "standard"}, ""},
		{"policy auto", &ServeConfig{PolicyRaw: "auto"}, ""},
		{"policy bad", &ServeConfig{PolicyRaw: "permissive"}, "policy"},
		{"include whitelist", &ServeConfig{IncludeTools: []string{"mal.*", "echo"}}, ""},
		{"include leading wildcard", &ServeConfig{IncludeTools: []string{"*echo"}}, "invalid"},
		{"include double wildcard", &ServeConfig{IncludeTools: []string{"a*b"}}, "invalid"},
		// Out-of-range caps are not errors: they are clamped by the
		// accessors (the client-side timeout precedent), so Validate stays
		// silent here and TestServeCapsDefaultsAndClamps pins the behavior.
		{"caps accepted as written", &ServeConfig{MaxConcurrentRaw: 999, SubmitRPMRaw: -5, MaxWaitsRaw: 0}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Config{Server: tc.cfg}.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestServeIncludes(t *testing.T) {
	open := &ServeConfig{}
	for _, name := range []string{"echo", "mal.search", "anything"} {
		if !open.Includes(name) {
			t.Errorf("empty whitelist rejected %q — absence means no filter, not 'expose nothing'", name)
		}
	}
	wl := &ServeConfig{IncludeTools: []string{"mal.*", "echo"}}
	for _, name := range []string{"echo", "mal.search", "mal.exec"} {
		if !wl.Includes(name) {
			t.Errorf("whitelist should admit %q", name)
		}
	}
	for _, name := range []string{"echo2", "agent_submit_task", "malware.search"} {
		if wl.Includes(name) {
			t.Errorf("whitelist should refuse %q", name)
		}
	}
}

func TestServeCapsDefaultsAndClamps(t *testing.T) {
	s := &ServeConfig{}
	if got := s.Policy(); got != "standard" {
		t.Fatalf("default policy = %q, want standard", got)
	}
	if got := s.MaxConcurrent(); got != 8 {
		t.Fatalf("default max_concurrent = %d, want 8", got)
	}
	if got := s.SubmitRPM(); got != 60 {
		t.Fatalf("default submit_rpm = %d, want 60", got)
	}
	if got := s.MaxWaits(); got != 32 {
		t.Fatalf("default max_waits = %d, want 32", got)
	}
	s = &ServeConfig{MaxConcurrentRaw: 999, SubmitRPMRaw: -5, MaxWaitsRaw: 100000}
	if got := s.MaxConcurrent(); got != 64 {
		t.Fatalf("clamped max_concurrent = %d, want 64", got)
	}
	if got := s.SubmitRPM(); got != 1 {
		t.Fatalf("clamped submit_rpm = %d, want 1", got)
	}
	if got := s.MaxWaits(); got != 256 {
		t.Fatalf("clamped max_waits = %d, want 256", got)
	}
}

func TestServeSectionRoundTripAndStrictDecode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	in := Config{
		Servers: map[string]*ServerConfig{"local": {Command: "srv"}},
		Server:  &ServeConfig{Enabled: true, TokenEnv: "MY_MCP_TOKEN", PolicyRaw: "auto", MaxConcurrentRaw: 3},
	}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Server == nil || !out.Server.Enabled || out.Server.TokenEnv != "MY_MCP_TOKEN" ||
		out.Server.Policy() != "auto" || out.Server.MaxConcurrent() != 3 {
		t.Fatalf("server section = %#v", out.Server)
	}
	// The strict decoder reaches into the section: a misspelled key is an
	// error, here exactly as in a client entry.
	if err := os.WriteFile(path, []byte(`{"server":{"enabled":true,"tokn":"x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("unknown field inside the server section must fail the load")
	}
}
