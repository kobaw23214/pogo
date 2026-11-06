package webgateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/webservice"
	"nhooyr.io/websocket"
)

func TestLeaseWebsiteURLFollowsRenewalAndRuntimeGeneration(t *testing.T) {
	f := testRouter(t, nil, nil, nil, false)
	rec := f.record()
	rec.LeaseID = "lease-one"
	rec.LeaseExpiresAt = time.Now().Add(24 * time.Hour)
	first := applyOpen(t, f, rec)
	if !first.ExpiresAt.Equal(rec.LeaseExpiresAt) {
		t.Fatal("website URL did not use the service lifetime")
	}
	for i := 0; i < 10; i++ {
		rec.OperationID += "x"
		opened, err := f.router.Open(context.Background(), rec)
		if err != nil || opened.PreviewURL != first.PreviewURL || opened.GrantID != first.GrantID {
			t.Fatalf("opening the same website created a different URL: %v", err)
		}
	}
	rec.Revision++
	rec.LeaseExpiresAt = rec.LeaseExpiresAt.Add(48 * time.Hour)
	if err := f.router.Apply(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	opened, err := f.router.Open(context.Background(), rec)
	if err != nil || opened.PreviewURL != first.PreviewURL || !opened.ExpiresAt.Equal(rec.LeaseExpiresAt) {
		t.Fatalf("renewal changed URL or retained an earlier expiry: %v", err)
	}
	assertWebsiteStatus(t, f.router, first.PreviewURL, http.StatusOK)
	// A rolling resource/settings restart changes the process generation, while
	// the same lease's published address remains useful after recovery.
	rec.State = "stopping"
	rec.Revision++
	if err := f.router.Revoke(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	assertWebsiteStatus(t, f.router, first.PreviewURL, http.StatusNotFound)
	rec.State = "starting"
	rec.Generation++
	rec.Revision++
	rec.Instance.ID = "replacement-container"
	if err := f.router.Apply(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	assertWebsiteStatus(t, f.router, first.PreviewURL, http.StatusServiceUnavailable)
	rec.State = "ready"
	rec.Revision++
	if err := f.router.Apply(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	assertWebsiteStatus(t, f.router, first.PreviewURL, http.StatusOK)
	opened, err = f.router.Open(context.Background(), rec)
	if err != nil || opened.PreviewURL != first.PreviewURL || opened.Generation != rec.Generation {
		t.Fatalf("replacement process lost the lease address: %v", err)
	}
	// A new lease is an intentional new publication, even for the same slot.
	rec.LeaseID = "lease-two"
	rec.Generation++
	rec.Revision++
	if err := f.router.Apply(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	assertWebsiteStatus(t, f.router, first.PreviewURL, http.StatusNotFound)
	newLease, err := f.router.Open(context.Background(), rec)
	if err != nil || newLease.PreviewURL == first.PreviewURL {
		t.Fatalf("a new lease revived a previous lease URL: %v", err)
	}
}

func TestLeaseWebsiteURLRestoresAfterManagerAndGatewayRestart(t *testing.T) {
	state := privateAccessStatePath(t)
	f := testRouter(t, nil, nil, func(o *RouterOptions) { o.StatePath = state }, false)
	rec := f.record()
	rec.LeaseID = "durable-lease"
	first := applyOpen(t, f, rec)
	f.router.replaceController()
	if err := f.router.Preflight(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertWebsiteStatus(t, f.router, first.PreviewURL, http.StatusNotFound)
	if err := f.router.Apply(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	assertWebsiteStatus(t, f.router, first.PreviewURL, http.StatusOK)
	if err := f.router.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewRouter(f.router.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	// Apply from the recovered Manager restores access without an Open call.
	if err := reloaded.Apply(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	assertWebsiteStatus(t, reloaded, first.PreviewURL, http.StatusOK)
	opened, err := reloaded.Open(context.Background(), rec)
	if err != nil || opened.PreviewURL != first.PreviewURL || opened.GrantID != first.GrantID {
		t.Fatalf("gateway restart lost the lease URL: %v", err)
	}
	info, err := os.Stat(state)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("durable signing state is not private: %v", err)
	}
}

func TestLeaseWebsiteWebSocketSurvivesRenewalAndReconnectsAfterControllerRecovery(t *testing.T) {
	f := testRouter(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/ws" {
			_, _ = io.WriteString(w, "website")
			return
		}
		conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			kind, payload, err := conn.Read(req.Context())
			if err != nil {
				return
			}
			if err := conn.Write(req.Context(), kind, payload); err != nil {
				return
			}
		}
	}), nil, nil, false)
	rec := f.record()
	rec.LeaseID = "websocket-lease"
	grant := applyOpen(t, f, rec)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	address := strings.Replace(grant.PreviewURL, "http://", "ws://", 1) + "ws"
	connect := func() *websocket.Conn {
		conn, _, err := websocket.Dial(ctx, address, &websocket.DialOptions{HTTPClient: f.client})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.CloseNow() })
		return conn
	}
	echo := func(conn *websocket.Conn, value string) {
		if err := conn.Write(ctx, websocket.MessageText, []byte(value)); err != nil {
			t.Fatal(err)
		}
		_, got, err := conn.Read(ctx)
		if err != nil || string(got) != value {
			t.Fatalf("websocket did not echo after lifecycle change: %q, %v", got, err)
		}
	}
	conn := connect()
	echo(conn, "before-renewal")
	rec.LeaseExpiresAt = time.Now().Add(24 * time.Hour)
	rec.Revision++
	if err := f.router.Apply(ctx, rec); err != nil {
		t.Fatal(err)
	}
	echo(conn, "after-renewal")
	f.router.replaceController()
	closedCtx, closedCancel := context.WithTimeout(ctx, time.Second)
	defer closedCancel()
	if _, _, err := conn.Read(closedCtx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a replaced controller retained an old active connection: %v", err)
	}
	if err := f.router.Preflight(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.router.Apply(ctx, rec); err != nil {
		t.Fatal(err)
	}
	echo(connect(), "same-address-after-recovery")
}

func TestLeaseExplicitRevocationIsDurableAndRenewable(t *testing.T) {
	state := privateAccessStatePath(t)
	f := testRouter(t, nil, nil, func(o *RouterOptions) { o.StatePath = state }, false)
	rec := f.record()
	rec.LeaseID = "revocable-lease"
	first := applyOpen(t, f, rec)
	if err := f.router.CloseGrant(context.Background(), rec, first.GrantID); err != nil {
		t.Fatal(err)
	}
	assertWebsiteStatus(t, f.router, first.PreviewURL, http.StatusNotFound)
	reopened, err := f.router.Open(context.Background(), rec)
	if err != nil || reopened.PreviewURL == first.PreviewURL {
		t.Fatalf("explicit revocation did not rotate website access: %v", err)
	}
	rec.LeaseExpiresAt = time.Now().Add(48 * time.Hour)
	rec.Revision++
	if err := f.router.Apply(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if err := f.router.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewRouter(f.router.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if err := reloaded.Apply(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	assertWebsiteStatus(t, reloaded, first.PreviewURL, http.StatusNotFound)
	assertWebsiteStatus(t, reloaded, reopened.PreviewURL, http.StatusOK)
	receipt, err := reloaded.Open(context.Background(), rec)
	if err != nil || receipt.PreviewURL != reopened.PreviewURL || !receipt.ExpiresAt.Equal(rec.LeaseExpiresAt) {
		t.Fatalf("renewal/restart lost explicit access rotation: %v", err)
	}
}

func TestLeaseRevocationSurvivesRenewalNotPublishedBeforeGatewayOutage(t *testing.T) {
	for _, rotateOtherLease := range []bool{false, true} {
		name := "gateway-restart"
		if rotateOtherLease {
			name = "gateway-restart-and-unrelated-revocation"
		}
		t.Run(name, func(t *testing.T) { checkLeaseRevocationAfterRenewalOutage(t, rotateOtherLease) })
	}
}

func checkLeaseRevocationAfterRenewalOutage(t *testing.T, rotateOtherLease bool) {
	t.Helper()
	statePath := privateAccessStatePath(t)
	f := testRouter(t, nil, nil, func(o *RouterOptions) { o.StatePath = statePath }, false)
	rec := f.record()
	rec.LeaseID = "renewed-during-outage"
	first := applyOpen(t, f, rec)
	if err := f.router.CloseGrant(context.Background(), rec, first.GrantID); err != nil {
		t.Fatal(err)
	}
	current, err := f.router.Open(context.Background(), rec)
	if err != nil || current.PreviewURL == first.PreviewURL {
		t.Fatalf("explicit access rotation failed: %v", err)
	}
	if err := f.router.Close(); err != nil {
		t.Fatal(err)
	}
	// Runner.Renew and the Manager's durable save precede Gateway.Apply. The
	// gateway may be down across its last recorded expiry, while the recovered
	// Manager already owns a longer lease. Age only the gateway's saved clock
	// boundary to model that outage without a timing-dependent sleep.
	rec.LeaseExpiresAt = time.Now().Add(48 * time.Hour)
	rec.Revision++
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var saved accessState
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	version := saved.Leases[leaseAccessKey(rec)]
	if version.Version == 0 {
		t.Fatal("fixture has no persisted explicit revocation")
	}
	version.ExpiresAt = time.Now().Add(-time.Hour)
	saved.Leases[leaseAccessKey(rec)] = version
	data, err = json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewRouter(f.router.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if rotateOtherLease {
		other := rec
		other.ServiceID += "-other"
		other.LeaseID += "-other"
		other.Instance.ID += "-other"
		if err := reloaded.Apply(context.Background(), other); err != nil {
			t.Fatal(err)
		}
		grant, err := reloaded.Open(context.Background(), other)
		if err != nil {
			t.Fatal(err)
		}
		if err := reloaded.CloseGrant(context.Background(), other, grant.GrantID); err != nil {
			t.Fatal(err)
		}
	}
	if err := reloaded.Apply(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	assertWebsiteStatus(t, reloaded, first.PreviewURL, http.StatusNotFound)
	assertWebsiteStatus(t, reloaded, current.PreviewURL, http.StatusOK)
}

func TestGatewayAccessStateRejectsUnsafeOrCorruptStorage(t *testing.T) {
	for _, kind := range []string{"public", "corrupt", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			state := privateAccessStatePath(t)
			switch kind {
			case "public":
				if err := os.WriteFile(state, []byte("{}"), 0644); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(state, []byte("{broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(t.TempDir(), "target"), state); err != nil {
					t.Fatal(err)
				}
			}
			r := &Router{opts: RouterOptions{StatePath: state}}
			if err := r.loadAccessState(); err == nil {
				t.Fatal("unsafe/corrupt access state accepted")
			}
		})
	}
}

func TestGatewayRuntimePortUsesTrustedRecordConfiguration(t *testing.T) {
	r := &Router{}
	rec := webservice.Record{ServiceID: "site", Owner: "owner", Session: "chat", Installation: "installation", Generation: 1, Revision: 1, Instance: webservice.Instance{ID: "container", NetworkID: "network", Upstream: "10.1.2.3:9091"}}
	rec.Runtime.InternalPort = 9091
	if _, err := r.checkedUpstream(rec); err != nil {
		t.Fatalf("configured application port rejected: %v", err)
	}
	rec.Instance.Upstream = "10.1.2.3:8080"
	if _, err := r.checkedUpstream(rec); err == nil {
		t.Fatal("an unrelated private port was accepted")
	}
}

func assertWebsiteStatus(t *testing.T, router *Router, address string, status int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, address, nil)
	w := httptest.NewRecorder()
	router.Handler().ServeHTTP(w, req)
	if w.Code != status {
		t.Fatalf("website status = %d, want %d: %s", w.Code, status, strings.TrimSpace(w.Body.String()))
	}
}

func privateAccessStatePath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "gateway-state.json")
}
