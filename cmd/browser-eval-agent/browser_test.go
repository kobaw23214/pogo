package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func TestWorkerProtocolFailureNeverRetriesAndDoesNotFollowRedirects(t *testing.T) {
	for _, kind := range []string{"redirect", "malformed", "oversized", "invalid_image", "cancel_unacknowledged"} {
		t.Run(kind, func(t *testing.T) {
			var redirected atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirected.Add(1)
				if r.Header.Get("Authorization") != "" {
					t.Error("credential followed worker redirect")
				}
				w.WriteHeader(200)
			}))
			defer destination.Close()
			worker := newWorkerFixture(t)
			if kind == "cancel_unacknowledged" {
				worker.cancelStatus = 503
			}
			worker.execute = func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "redirect":
					http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
				case "malformed", "cancel_unacknowledged":
					w.Write([]byte(`{"invalid"`))
				case "oversized":
					w.Write([]byte(strings.Repeat(" ", maxReplyBytes+1)))
				case "invalid_image":
					json.NewEncoder(w).Encode(tools.BrowserReply{OK: true, Screenshot: &tools.BrowserScreenshot{Data: "not-image", Mime: "image/jpeg", Width: 100, Height: 100}})
				}
			}
			client := &fakeClient{t: t, run: func(context.Context, int, []llm.Message) (llm.Response, error) {
				return toolResponse("screenshot", llm.Usage{Input: 10, Output: 5}), nil
			}}
			code, events, raw := runFixture(t, defaultInput(worker.server.URL), client, config.Resolved{Model: "fixture-model", SeesImages: true})
			if code != 1 {
				t.Fatal(raw)
			}
			end := assertEnd(t, events, "browser_error")
			_, actions, cancels := worker.counts()
			if actions != 1 || cancels != 1 || redirected.Load() != 0 {
				t.Fatalf("unexpected replay/redirect: %d %d %d", actions, cancels, redirected.Load())
			}
			if kind == "cancel_unacknowledged" && !strings.Contains(end["error"].(string), "not acknowledged") {
				t.Fatal("failed cancel reported as confirmed", end)
			}
		})
	}
}

func TestEveryBrowserOperationHasUniqueID(t *testing.T) {
	worker := newWorkerFixture(t)
	client := &fakeClient{t: t, run: func(_ context.Context, n int, _ []llm.Message) (llm.Response, error) {
		if n < 3 {
			return toolResponse("snapshot", llm.Usage{Input: 10, Output: 5}), nil
		}
		return answerResponse("Done", llm.Usage{Input: 10, Output: 2}), nil
	}}
	code, _, raw := runFixture(t, defaultInput(worker.server.URL), client, config.Resolved{Model: "fixture-model"})
	if code != 0 {
		t.Fatal(raw)
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if len(worker.actions) != 2 || worker.actions[0]["op_id"] == worker.actions[1]["op_id"] {
		t.Fatal("operation IDs reused")
	}
}
