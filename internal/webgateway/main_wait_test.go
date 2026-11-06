package webgateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type mainProxyResult struct {
	status int
	body   string
	err    error
}

func mainProxyRequest(client *http.Client, req *http.Request) mainProxyResult {
	resp, err := client.Do(req)
	if err != nil {
		return mainProxyResult{err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return mainProxyResult{status: resp.StatusCode, body: string(body), err: err}
}

// Exercise a real delayed response beyond the old ten-second cutoff. Both
// routing modes must keep the main request alive while the website's original
// header timeout remains independent.
func TestRouterMainWaitsPastWebsiteHeaderTimeout(t *testing.T) {
	for _, mode := range []string{"path", "subdomain"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			mainStarted, appCancelled := make(chan struct{}), make(chan struct{})
			main := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				close(mainStarted)
				select {
				case <-time.After(11 * time.Second):
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"compacted":true}`)
				case <-req.Context().Done():
				}
			})
			app := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/slow-headers" {
					_, _ = io.WriteString(w, "ready")
					return
				}
				select {
				case <-req.Context().Done():
					close(appCancelled)
				case <-time.After(15 * time.Second):
					_, _ = io.WriteString(w, "late website response")
				}
			})
			var f *routerFixture
			if mode == "path" {
				f = testPathRouter(t, app, main, false)
			} else {
				f = testRouter(t, app, main, nil, false)
			}
			grant := applyOpen(t, f, f.record())
			client := *f.client
			client.Timeout = 20 * time.Second
			req, err := http.NewRequest(http.MethodPost, f.public+"/api/sessions/example/control", strings.NewReader(`{"action":"compact"}`))
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan mainProxyResult, 1)
			go func() { done <- mainProxyRequest(&client, req) }()
			select {
			case <-mainStarted:
			case <-time.After(3 * time.Second):
				t.Fatal("main request did not reach the upstream")
			}
			resp, body := response(t, &client, http.MethodGet, grant.PreviewURL+"slow-headers", nil, nil)
			if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "application unavailable") {
				t.Fatalf("website response policy changed: status=%d body=%q", resp.StatusCode, body)
			}
			select {
			case <-appCancelled:
			case <-time.After(2 * time.Second):
				t.Fatal("website timeout did not cancel its upstream request")
			}
			select {
			case result := <-done:
				if result.err != nil || result.status != http.StatusOK || result.body != `{"compacted":true}` {
					t.Fatalf("long main request failed: %+v", result)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("long main request did not complete")
			}
		})
	}
}

func TestRouterMainWaitStillHonorsClientCancellation(t *testing.T) {
	for _, mode := range []string{"path", "subdomain"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			started, cancelled := make(chan struct{}), make(chan struct{})
			main := http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
				close(started)
				<-req.Context().Done()
				close(cancelled)
			})
			var f *routerFixture
			if mode == "path" {
				f = testPathRouter(t, nil, main, false)
			} else {
				f = testRouter(t, nil, main, nil, false)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.public+"/api/sessions/example/control", nil)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan mainProxyResult, 1)
			go func() { done <- mainProxyRequest(f.client, req) }()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("main request did not reach the upstream")
			}
			cancel()
			select {
			case <-cancelled:
			case <-time.After(2 * time.Second):
				t.Fatal("client cancellation did not reach the main service")
			}
			select {
			case result := <-done:
				if !errors.Is(result.err, context.Canceled) {
					t.Fatalf("client did not observe cancellation: %+v", result)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancelled client request did not finish")
			}
		})
	}
}
