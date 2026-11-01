package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOfficeOpenWaitBudgetsCoverFiveMinuteStages(t *testing.T) {
	if officeOpenWaitBudget < 5*time.Minute || officeOpenHTTPBudget <= officeOpenWaitBudget || officeOpenProbeBudget <= 2*time.Second {
		t.Fatal("open budget is shorter than the requested stage or the upstream health sample")
	}
	secret := filepath.Join(t.TempDir(), "office-secret")
	if err := os.WriteFile(secret, []byte("1234567890123456789012345678901234567890"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseOfficeConfig(&OfficeOptions{ServiceURL: "http://editor.invalid", PublicURL: "http://pogo.invalid", JWTSecretFile: secret})
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := cfg.client.Transport.(*http.Transport)
	if !ok || cfg.client.Timeout < 5*time.Minute || transport.ResponseHeaderTimeout < 5*time.Minute || transport.TLSHandshakeTimeout < 5*time.Minute {
		t.Fatal("a hidden short HTTP deadline can still truncate opening")
	}
	defer transport.CloseIdleConnections()
	gateway := newOfficeGateway(&Server{office: &officeService{cfg: cfg}})
	defer gateway.close()
	if gateway.transport.ResponseHeaderTimeout < 5*time.Minute || gateway.transport.TLSHandshakeTimeout < 5*time.Minute {
		t.Fatal("gateway retained a short opening deadline")
	}
	if cfg.commandTimeout != 35*time.Second || cfg.saveTimeout != 35*time.Second {
		t.Fatal("opening changes unexpectedly altered command/save deadlines")
	}
}

func TestOfficeOpenWaitRecoversTransientFailure(t *testing.T) {
	var calls atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]bool{"ready": false})
			return
		}
		writeJSON(w, http.StatusOK, officeReadinessTestBody())
	}))
	defer engine.Close()
	o := officeReadinessTestService(engine)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := o.waitEditorReady(ctx); err != nil || calls.Load() != 2 {
		t.Fatalf("temporary 503 was treated as a terminal open failure: calls=%d error=%v", calls.Load(), err)
	}
}

func TestOfficeOpenWaitIgnoresNegativeLightweightCache(t *testing.T) {
	var healthy atomic.Bool
	var calls atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body := officeReadinessTestBody()
		body["ready"] = healthy.Load()
		writeJSON(w, http.StatusOK, body)
	}))
	defer engine.Close()
	o := officeReadinessTestService(engine)
	if o.editorReady(context.Background()) {
		t.Fatal("synthetic unavailable sample was accepted")
	}
	healthy.Store(true)
	if err := o.waitEditorReady(context.Background()); err != nil || calls.Load() != 2 {
		t.Fatalf("negative lightweight cache blocked a recoverable open: calls=%d error=%v", calls.Load(), err)
	}
}

func TestOfficeOpenWaitAllowsHealthSlowerThanLightweightProbe(t *testing.T) {
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(officeReadinessTimeout + 200*time.Millisecond):
			writeJSON(w, http.StatusOK, officeReadinessTestBody())
		}
	}))
	defer engine.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := officeReadinessTestService(engine).waitEditorReady(ctx); err != nil {
		t.Fatalf("actual opening retained the 800ms health limit: %v", err)
	}
}

func TestOfficeOpenWaitSharesProbeAndHonorsCancellation(t *testing.T) {
	var calls atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(30 * time.Millisecond)
		writeJSON(w, http.StatusOK, officeReadinessTestBody())
	}))
	defer engine.Close()
	o := officeReadinessTestService(engine)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := o.waitEditorReady(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent opens did not share the same probe: %d", calls.Load())
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := o.waitEditorReady(canceled); !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatal("canceled opening consumed cached readiness or issued another request")
	}
}

func TestOfficeOpenWaitDoesNotSpinTransientFailuresForConcurrentCallers(t *testing.T) {
	var calls atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusServiceUnavailable, map[string]bool{"ready": false})
	}))
	defer engine.Close()
	o := officeReadinessTestService(engine)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := o.waitEditorReady(ctx); err == nil {
				t.Error("unavailable engine reported ready")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("waiting callers bypassed the shared retry delay: %d requests", calls.Load())
	}
}

func TestOfficeOpenWaitReportsPermanentConfigurationFailure(t *testing.T) {
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "operator-only upstream detail", http.StatusForbidden)
	}))
	defer engine.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := officeReadinessTestService(engine).waitEditorReady(ctx)
	officeExpectError(t, err, "OFFICE_CONFIG_INVALID")
}

func TestOfficeOpenWaitRetriesTruncatedHealthResponse(t *testing.T) {
	var calls atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{"))
			return
		}
		writeJSON(w, http.StatusOK, officeReadinessTestBody())
	}))
	defer engine.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := officeReadinessTestService(engine).waitEditorReady(ctx); err != nil || calls.Load() != 2 {
		t.Fatalf("truncated network response was misclassified as permanent configuration failure: calls=%d error=%v", calls.Load(), err)
	}
}
