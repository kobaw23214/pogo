package webservice

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type rpcTestRunner struct {
	calls  int
	err    error
	record Record
}

func (r *rpcTestRunner) Preflight(context.Context) error { r.calls++; return r.err }
func (r *rpcTestRunner) Start(_ context.Context, l Launch) (Instance, error) {
	r.calls++
	r.record = l.Record
	return Instance{ID: "runtime-one", Upstream: "http://runtime:8080"}, r.err
}
func (r *rpcTestRunner) Inspect(context.Context, Record) (RuntimeStatus, error) {
	return RuntimeStatus{Running: true, Exists: true}, r.err
}
func (r *rpcTestRunner) Stop(context.Context, Record) error  { return r.err }
func (r *rpcTestRunner) Renew(context.Context, Record) error { return r.err }
func (r *rpcTestRunner) Logs(context.Context, Record, string, int) (LogPage, error) {
	return LogPage{Text: "hello"}, r.err
}
func (r *rpcTestRunner) DeleteData(context.Context, Record) error { return r.err }

type profilesTestRunner struct{ rpcTestRunner }

func (*profilesTestRunner) Profiles(context.Context) ([]string, error) {
	return []string{"python-web", "node-web"}, nil
}

func TestRunnerRPCProfilesAreAuthenticatedAndUseRuntimeCapabilities(t *testing.T) {
	for _, tc := range []struct {
		runner Runner
		want   string
	}{{&rpcTestRunner{}, "python-web"}, {&profilesTestRunner{}, "python-web,node-web"}} {
		token := strings.Repeat("c", 32)
		server := httptest.NewServer(NewRunnerRPCHandler(tc.runner, token))
		client, err := NewRunnerRPCClient(server.URL, token)
		if err != nil {
			t.Fatal(err)
		}
		profiles, err := client.Profiles(context.Background())
		server.Close()
		if err != nil || strings.Join(profiles, ",") != tc.want {
			t.Fatal(profiles, err)
		}
	}
}

func TestRunnerRPCRequiresPrivateAuthenticationAndStrictPayload(t *testing.T) {
	backend := &rpcTestRunner{}
	token := strings.Repeat("a", 32)
	server := httptest.NewServer(NewRunnerRPCHandler(backend, token))
	defer server.Close()
	for _, tc := range []struct {
		auth, body string
		status     int
	}{{"", "{}", 401}, {"Bearer wrong", "{}", 401}, {"Bearer " + token, `{"unknown":true}`, 400}, {"Bearer " + token, "{} {}", 400}} {
		req, _ := http.NewRequest("POST", server.URL+"/v1/runner/preflight", strings.NewReader(tc.body))
		req.Header.Set("Authorization", tc.auth)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.status {
			t.Fatalf("expected %d got %d", tc.status, res.StatusCode)
		}
	}
	if backend.calls != 0 {
		t.Fatal("unauthorized payload executed runner")
	}
	client, err := NewRunnerRPCClient(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := client.Start(context.Background(), Launch{Record: runnerRecord(), Archive: []byte("archive")})
	if err != nil || instance.ID != "runtime-one" || backend.record.Owner != "owner-one" || backend.record.Installation != "qa-install" {
		t.Fatal(instance, backend.record, err)
	}
	status, err := client.Inspect(context.Background(), runnerRecord())
	if err != nil || !status.Running {
		t.Fatal(status, err)
	}
}

func TestRunnerRPCPreservesStructuredFailuresWithoutInternalLeaks(t *testing.T) {
	backend := &rpcTestRunner{err: Fail("QUOTA_EXCEEDED", "capacity reached")}
	token := strings.Repeat("b", 32)
	server := httptest.NewServer(NewRunnerRPCHandler(backend, token))
	defer server.Close()
	client, _ := NewRunnerRPCClient(server.URL, token)
	err := client.Preflight(context.Background())
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != "QUOTA_EXCEEDED" {
		t.Fatal(err)
	}
	backend.err = errors.New("secret host internals")
	err = client.Preflight(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret") || !errors.As(err, &typed) || typed.Code != "RUNNER_UNAVAILABLE" {
		t.Fatal("internal error exposed", err)
	}
}
