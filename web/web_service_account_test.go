package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/webservice"
)

type accountServiceRunner struct {
	*webServiceHostRunner
	stopError bool
	deleted   map[string]bool
}

func (r *accountServiceRunner) Stop(ctx context.Context, v webservice.Record) error {
	if r.stopError {
		return errors.New("fixture runner unavailable")
	}
	return r.webServiceHostRunner.Stop(ctx, v)
}
func (r *accountServiceRunner) DeleteData(_ context.Context, v webservice.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted[v.ServiceID] = true
	return nil
}

type accountServiceGateway struct {
	webServiceHostGateway
	mu      sync.Mutex
	revoked map[string]bool
}

func (g *accountServiceGateway) Revoke(_ context.Context, v webservice.Record) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.revoked[v.ServiceID] = true
	return nil
}

func TestWebServiceAccountDeletionFencesBackendBeforeCredentialRemoval(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "cleanup-confirmed"
		if pending {
			name = "cleanup-pending"
		}
		t.Run(name, func(t *testing.T) {
			runner := &accountServiceRunner{webServiceHostRunner: &webServiceHostRunner{running: map[string]bool{}}, stopError: pending, deleted: map[string]bool{}}
			gateway := &accountServiceGateway{revoked: map[string]bool{}}
			managed, err := webservice.NewManager(webservice.Options{Root: filepath.Join(t.TempDir(), "private-web"), Installation: "account-test", Runner: runner, Gateway: gateway, PollInterval: 10 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = managed.Close() })
			h := newMultiuserHarness(t, func(int) (llm.Response, error) {
				t.Error("account cleanup contacted model")
				return llm.Response{}, nil
			}, func(c *Config) { c.WebServices = managed })
			sid := h.createAs(bobToken)
			sess := h.session(sid)
			project := filepath.Join(sess.cwd, "account-site")
			if err := os.MkdirAll(project, 0700); err != nil {
				t.Fatal(err)
			}
			original := []byte("print('account runtime fixture')\n")
			if err := os.WriteFile(filepath.Join(project, "app.py"), original, 0600); err != nil {
				t.Fatal(err)
			}
			backend := h.mgr.webServiceBackend(sess)
			start, err := backend.Execute(context.Background(), webservice.Request{Action: "start", OperationID: "account-start", Spec: webservice.Spec{Name: "account", ProjectRoot: "account-site", Profile: "python-web", StartArgv: []string{"python", "app.py"}}})
			if err != nil {
				t.Fatal(err)
			}
			var accepted struct {
				ID string `json:"service_id"`
			}
			if err := json.Unmarshal(start, &accepted); err != nil || accepted.ID == "" {
				t.Fatalf("start missing identity: %s %v", start, err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				data, err := backend.Execute(context.Background(), webservice.Request{Action: "status", ServiceID: accepted.ID})
				if err != nil {
					t.Fatal(err)
				}
				var status struct {
					State string `json:"state"`
				}
				_ = json.Unmarshal(data, &status)
				if status.State == "ready" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("service not ready: %s", data)
				}
				time.Sleep(time.Millisecond)
			}
			if _, err := backend.Execute(context.Background(), webservice.Request{Action: "open", ServiceID: accepted.ID, OperationID: "account-open"}); err != nil {
				t.Fatal(err)
			}
			const adminToken = "account-cleanup-admin-token-32chars"
			h.psrv.auth.mu.Lock()
			h.psrv.auth.users["admin"] = userRecord{Username: "admin", Admin: true, Token: adminToken}
			h.psrv.auth.path = filepath.Join(t.TempDir(), "users.json")
			err = h.psrv.auth.save()
			h.psrv.auth.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			response := h.doAs(adminToken, http.MethodDelete, "/api/accounts/bob", "")
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			want := http.StatusOK
			if pending {
				want = http.StatusServiceUnavailable
			}
			if response.StatusCode != want {
				t.Fatalf("delete=%d want %d: %s", response.StatusCode, want, body)
			}
			h.psrv.auth.mu.RLock()
			_, credentialsRemain := h.psrv.auth.users["bob"]
			_, aliceRemains := h.psrv.auth.users["alice"]
			h.psrv.auth.mu.RUnlock()
			if credentialsRemain != pending || !aliceRemains {
				t.Fatal("credential cleanup affected the wrong account or misreported completion")
			}
			if pending && !strings.Contains(string(body), "credentials retained") {
				t.Fatalf("pending cleanup lacks actionable result: %s", body)
			}
			// A backend accepted before deletion cannot admit new work even while
			// cleanup is unavailable and the account credentials are retained.
			for _, request := range []webservice.Request{
				{Action: "start", OperationID: "late-start", Spec: webservice.Spec{Name: "late", ProjectRoot: "account-site", Profile: "python-web", StartArgv: []string{"python", "app.py"}}},
				{Action: "restart", OperationID: "late-restart", ServiceID: accepted.ID},
				{Action: "open", OperationID: "late-open", ServiceID: accepted.ID},
			} {
				if _, err := backend.Execute(context.Background(), request); err == nil {
					t.Errorf("old backend admitted %s after account fence", request.Action)
				}
			}
			gateway.mu.Lock()
			revoked := gateway.revoked[accepted.ID]
			gateway.mu.Unlock()
			runner.mu.Lock()
			dataDeleted := runner.deleted[accepted.ID]
			running := runner.running[accepted.ID]
			runner.mu.Unlock()
			if !revoked || dataDeleted == pending || running != pending {
				t.Fatalf("cleanup facts: revoked=%v dataDeleted=%v running=%v pending=%v", revoked, dataDeleted, running, pending)
			}
			if _, err := os.Stat(sess.Path()); err != nil {
				t.Fatalf("old transcript removed: %v", err)
			}
			content, err := os.ReadFile(filepath.Join(project, "app.py"))
			if err != nil || string(content) != string(original) {
				t.Fatalf("source workspace changed: %s %v", content, err)
			}
		})
	}
}
