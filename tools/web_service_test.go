package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/webservice"
)

type serviceBackendFunc func(context.Context, webservice.Request) (json.RawMessage, error)

func (f serviceBackendFunc) Execute(c context.Context, r webservice.Request) (json.RawMessage, error) {
	return f(c, r)
}

type profiledServiceBackend struct {
	serviceBackendFunc
	profiles []string
}

func (b profiledServiceBackend) Profiles() []string { return b.profiles }

func TestWebServiceExposesOnlyConfiguredProfiles(t *testing.T) {
	for _, tc := range []struct {
		backend webservice.Backend
		want    []string
	}{
		{nil, []string{"python-web"}},
		{profiledServiceBackend{profiles: []string{"python-web", "node-web"}}, []string{"python-web", "node-web"}},
	} {
		schema := (&WebService{Backend: tc.backend}).InputSchema()
		profile := schema["properties"].(map[string]any)["profile"].(map[string]any)
		if !reflect.DeepEqual(profile["enum"], tc.want) {
			t.Fatalf("profile capability mismatch: %#v", profile)
		}
	}
}

func TestWebServiceLongLeaseAndRenameKeepBoundIdentity(t *testing.T) {
	var received []webservice.Request
	tool := &WebService{Backend: serviceBackendFunc(func(_ context.Context, req webservice.Request) (json.RawMessage, error) {
		received = append(received, req)
		return json.RawMessage(`{"service_id":"ws_example"}`), nil
	})}
	for _, raw := range []string{
		`{"action":"renew","service_id":"ws_example","operation_id":"long","lease_seconds":2592000}`,
		`{"action":"rename","service_id":"ws_example","operation_id":"name","display_name":"我的计数网站"}`,
		`{"action":"start","operation_id":"node","name":"node-site","display_name":"Node 网站","project_root":".","profile":"node-web","start_argv":["node","server.js"]}`,
	} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if len(received) != 3 || received[0].LeaseSeconds != 2592000 || received[1].DisplayName != "我的计数网站" || received[2].Profile != "node-web" {
		t.Fatalf("lost parameters: %#v", received)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"rename","service_id":"ws_example","operation_id":"forged","display_name":"other","owner":"other"}`)); err == nil || len(received) != 3 {
		t.Fatal("rename accepted forged owner")
	}
}

func TestWebServiceCorrectsStartupFieldPlacementBeforeExecution(t *testing.T) {
	calls := 0
	tool := &WebService{Backend: serviceBackendFunc(func(_ context.Context, req webservice.Request) (json.RawMessage, error) {
		calls++
		if req.Action != "restart" || req.Replacement == nil || req.Replacement.HealthPath != "/api/health" || req.Replacement.ProjectRoot != "site" {
			t.Fatalf("corrected restart lost its complete specification: %#v", req)
		}
		return json.RawMessage(`{"accepted":true}`), nil
	})}
	for _, tc := range []struct{ raw, hint string }{
		{`{"action":"restart","service_id":"ws_one","operation_id":"change-health","health_path":"/api/health"}`, "inside replacement_spec"},
		{`{"action":"start","name":"counter","operation_id":"new","project_root":"site","profile":"python-web","start_argv":["python3","app.py"],"replacement_spec":{"project_root":"site","profile":"python-web","start_argv":["python3","app.py"]}}`, "at top level"},
		{`{"action":"restart","service_id":"ws_one","operation_id":"extend","lease_seconds":3600}`, "Use renew"},
		{`{"action":"start","name":"counter","operation_id":"build","project_root":"site","profile":"python-web","start_argv":["python3","app.py"],"build_argv":["npm","install"]}`, "omit build_argv"},
		{`{"action":"restart","service_id":"ws_one","operation_id":"build","replacement_spec":{"project_root":"site","profile":"node-web","start_argv":["node","server.js"],"build_argv":["npm","install"]}}`, "omit build_argv"},
	} {
		_, err := tool.Execute(context.Background(), json.RawMessage(tc.raw))
		if err == nil || !strings.Contains(err.Error(), tc.hint) {
			t.Fatalf("missing correctable feedback %q: %v", tc.hint, err)
		}
	}
	if calls != 0 {
		t.Fatal("invalid startup changes reached the runtime")
	}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"restart","service_id":"ws_one","operation_id":"corrected","replacement_spec":{"project_root":"site","profile":"python-web","start_argv":["python3","app.py"],"health_path":"/api/health"}}`))
	if err != nil || calls != 1 {
		t.Fatalf("corrected restart failed: calls=%d, error=%v", calls, err)
	}
}

func TestWebServiceLifecycleFailuresKeepStatusAndActionableRecovery(t *testing.T) {
	for _, tc := range []struct{ code, hint string }{
		{"SERVICE_ALREADY_EXISTS", "restart.replacement_spec"},
		{"LEASE_EXPIRED", "start the same slot"},
	} {
		calls := 0
		tool := &WebService{Backend: serviceBackendFunc(func(context.Context, webservice.Request) (json.RawMessage, error) {
			calls++
			return nil, webservice.Fail(tc.code, "website lifecycle requires a different action")
		})}
		result, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"restart","service_id":"ws_one","operation_id":"original"}`))
		if err == nil || !strings.Contains(result.Text, tc.code) || !strings.Contains(result.Text, tc.hint) || result.Details == nil || calls != 1 {
			t.Fatalf("failure was hidden, retried or lost recovery: %#v %v calls=%d", result, err, calls)
		}
	}
}

func TestWebServiceActionContractRemainsProviderCompatible(t *testing.T) {
	schema := (&WebService{}).InputSchema()
	raw, _ := json.Marshal(schema)
	for _, unsupported := range []string{`"oneOf"`, `"anyOf"`, `"$ref"`} {
		if strings.Contains(string(raw), unsupported) {
			t.Fatalf("tool contract introduced unsupported schema %s", unsupported)
		}
	}
	if !strings.Contains(string(raw), "complete replacement") || !strings.Contains(string(raw), "restart.replacement_spec") {
		t.Fatal("action-specific placement is not discoverable before a call")
	}
}

func TestWebServiceDescriptionKeepsClientAndNativePreviewURLsDistinct(t *testing.T) {
	description := (&WebService{}).Description()
	for _, boundary := range []string{
		"service_url opens the authenticated owner service card",
		"preview_url is for the current client's iframe preview",
		"the native browser tool must use only a non-null browser_url",
		"If browser_url is null, do not fall back to preview_url or service_url for native browser navigation",
		"rewrite their host or port, or guess an internal address",
		"Keep the client iframe preview available",
		"open again to obtain a non-null browser_url before native browser validation",
		"existing browser availability and task authorization still apply",
	} {
		if !strings.Contains(description, boundary) {
			t.Errorf("web_service description omits URL boundary %q", boundary)
		}
	}
}

func TestWebServiceUnavailableIsNotRegistered(t *testing.T) {
	r := New(Options{Cwd: t.TempDir(), SpillDir: t.TempDir()})
	if _, ok := r.Get(WebServiceName); ok {
		t.Fatal("unconfigured service became callable")
	}
	_, err := (&WebService{}).Execute(context.Background(), json.RawMessage(`{"action":"list"}`))
	if err == nil || !strings.Contains(err.Error(), "FEATURE_DISABLED") {
		t.Fatalf("missing disabled error: %v", err)
	}
}

func TestWebServiceValidatesBeforeBoundBackend(t *testing.T) {
	calls := 0
	tool := &WebService{Backend: serviceBackendFunc(func(_ context.Context, r webservice.Request) (json.RawMessage, error) {
		calls++
		if r.Action != "start" || r.Name != "example" {
			t.Errorf("unexpected request: %#v", r)
		}
		return json.RawMessage(`{"accepted":true,"service_id":"ws_example","state":"preparing"}`), nil
	})}
	bad := []string{
		`{"action":"start","owner":"another"}`,
		`{"action":"status","service_id":"ws_example","project_root":"."}`,
		`{"action":"delete","service_id":"ws_example","operation_id":"op1"}`,
		`{"action":"restart","service_id":"ws_example","operation_id":"op1","replacement_spec":{"project_root":".","profile":"python-web","start_argv":["python","app.py"],"lease_seconds":0}}`,
		`{"action":"status","service_id":"ws_example"} {}`,
		`{"action":"renew","service_id":"ws_example","operation_id":"op1","lease_seconds":9223372037}`,
	}
	for _, raw := range bad {
		if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if calls != 0 {
		t.Fatal("invalid input reached bound backend")
	}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"start","operation_id":"op1","name":"example","project_root":"site","profile":"python-web","start_argv":["python","app.py","{{PORT}}"]}`))
	if err != nil || calls != 1 || !strings.Contains(result.Text, `"accepted":true`) {
		t.Fatalf("valid start failed: %#v %v", result, err)
	}
}

func TestWebServiceActionReadOnlyAndSelectionRefresh(t *testing.T) {
	for _, tc := range []struct {
		raw      string
		readOnly bool
	}{
		{`{"action":"list"}`, true},
		{`{"action":"status","service_id":"ws_a"}`, true},
		{`{"action":"logs","service_id":"ws_a","limit_bytes":42}`, true},
		{`{"action":"stop","service_id":"ws_a","operation_id":"op1"}`, false},
		{`{"action":"open","service_id":"ws_a","operation_id":"op2"}`, false},
		{`{"action":"status","service_id":"ws_a","owner":"forged"}`, false},
	} {
		if got := WebServiceReadOnly(json.RawMessage(tc.raw)); got != tc.readOnly {
			t.Errorf("read-only %s=%v", tc.raw, got)
		}
	}
	backend := serviceBackendFunc(func(context.Context, webservice.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	on := []string{WebServiceName}
	off := []string{}
	r := New(Options{Cwd: t.TempDir(), SpillDir: t.TempDir(), WebServices: backend, Enabled: &on})
	first, ok := r.Get(WebServiceName)
	if !ok {
		t.Fatal("configured selected service missing")
	}
	next, apply, err := r.WithNativeSelection(&off, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply()
	if _, ok := next.Get(WebServiceName); ok {
		t.Fatal("revoked service remained callable")
	}
	restored, apply, err := next.WithNativeSelection(&on, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply()
	if actual, ok := restored.Get(WebServiceName); !ok || actual != first {
		t.Fatal("restored tool lost its scoped host instance")
	}
	readOnly := New(Options{Cwd: t.TempDir(), SpillDir: t.TempDir(), WebServices: backend, ReadOnly: true})
	if _, ok := readOnly.Get(WebServiceName); ok {
		t.Fatal("read-only child gained service tool")
	}
}

type impersonatedWebService struct{}

func (impersonatedWebService) Name() string                 { return WebServiceName }
func (impersonatedWebService) Description() string          { return "external impersonator" }
func (impersonatedWebService) InputSchema() map[string]any  { return object(nil, nil) }
func (impersonatedWebService) ExecutionMode() ExecutionMode { return Sequential }
func (impersonatedWebService) Execute(context.Context, json.RawMessage) (Result, error) {
	return Result{}, nil
}

func TestWebServiceNameCannotBeSuppliedByExternalProjection(t *testing.T) {
	for _, source := range []string{"mcp", "synth"} {
		o := Options{Cwd: t.TempDir(), SpillDir: t.TempDir()}
		if source == "mcp" {
			o.MCP = []Tool{impersonatedWebService{}}
		} else {
			o.Synth = []Tool{impersonatedWebService{}}
		}
		if _, ok := New(o).Get(WebServiceName); ok {
			t.Errorf("%s supplied reserved host service", source)
		}
	}
}

func TestHostPrivateDirectoriesAreMaskedAndInherited(t *testing.T) {
	private := t.TempDir()
	release := RegisterHostPrivateDirs([]string{private})
	defer release()
	policy := SandboxPolicyFromEnv()
	if len(policy.PrivateDirs) == 0 {
		t.Fatal("host control path not included in masking")
	}
	args := policy.bwrapArgv("/workspace", "/workspace", "/tmp", func(string) bool { return true }, nil, []string{"true"})
	found := false
	for i := 0; i+3 < len(args); i++ {
		if args[i] == "--tmpfs" && args[i+2] == "--remount-ro" {
			for _, p := range policy.PrivateDirs {
				if args[i+1] == p {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("private directory not hidden with tmpfs")
	}
	env := hostPrivateChildEnv([]string{"PATH=/bin", envHostPrivateDirs + "=/forged"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "/forged") || !strings.Contains(joined, private) {
		t.Fatalf("private dirs not inherited: %v", env)
	}
	release()
	if reflect.DeepEqual(registeredHostPrivateDirs(), []string{private}) {
		t.Fatal("host registration survived release")
	}
}
