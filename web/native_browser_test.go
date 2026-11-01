package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/tools"
)

func nativeTestConfig(t *testing.T, h http.HandlerFunc) *nativeBrowserConfig {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer native-test-token" {
			t.Errorf("worker request lacked service authorization")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	cfg, err := parseNativeBrowserConfig(srv.URL, "native-test-token")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func nativeWait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("browser operation did not reach the expected phase")
	}
}

func TestNativeBrowserConfiguration(t *testing.T) {
	for _, tc := range []struct {
		endpoint, token string
		valid           bool
	}{
		{"", "", true}, {"http://127.0.0.1:9999", "token", true}, {"https://browser.example/worker/", "token", true},
		{"", "token", false}, {"http://browser", "", false}, {"file:///tmp/socket", "token", false},
		{"http://user:password@browser", "token", false}, {"https://browser/?token=x", "token", false},
		{"https://browser/#fragment", "token", false}, {"http://browser", "token\ninjected", false},
	} {
		_, err := parseNativeBrowserConfig(tc.endpoint, tc.token)
		if (err == nil) != tc.valid {
			t.Fatalf("configuration validation mismatch for %q: %v", tc.endpoint, err)
		}
	}
}

func TestNativeBrowserGateRunsOnceWithoutSSE(t *testing.T) {
	var executions, creates atomic.Int32
	var seenID atomic.Value
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			creates.Add(1)
			writeJSON(w, 200, map[string]bool{"ok": true})
		case strings.HasSuffix(r.URL.Path, "/execute"):
			executions.Add(1)
			var op map[string]any
			_ = json.NewDecoder(r.Body).Decode(&op)
			id, _ := op["op_id"].(string)
			seenID.Store(id)
			writeJSON(w, 200, tools.BrowserReply{OK: true, URL: "https://example.test/", Text: "native state"})
		default:
			writeJSON(w, 200, map[string]bool{"ok": true})
		}
	})
	b := newNativeBrowser(cfg)
	t.Cleanup(b.Close)
	hub := NewHub()
	g := NewBrowserGate(nil)
	g.executor = b
	if _, err := g.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`)); !errors.Is(err, ErrBrowserNotArmed) {
		t.Fatalf("native backend bypassed task authorization: %v", err)
	}
	g.Arm()
	reply, err := g.Execute(context.Background(), json.RawMessage(`{"action":"snapshot","op_id":"client-chosen"}`))
	if err != nil || !reply.OK || reply.Backend != "native" || executions.Load() != 1 || creates.Load() != 1 || seenID.Load() == "" || seenID.Load() == "client-chosen" {
		t.Fatalf("native execution without subscribers: %+v %v calls=%d", reply, err, executions.Load())
	}
	_, events, stop := hub.Subscribe(0)
	defer stop()
	_, events2, stop2 := hub.Subscribe(0)
	defer stop2()
	if _, err := g.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`)); err != nil {
		t.Fatal(err)
	}
	if executions.Load() != 2 || creates.Load() != 1 {
		t.Fatal("subscribers caused duplicate execution or creation")
	}
	for _, stream := range []<-chan Event{events, events2} {
		for {
			select {
			case event := <-stream:
				if event.Type == "browser_request" {
					t.Fatal("native operation was broadcast to UI executors")
				}
			default:
				goto drained
			}
		}
	drained:
	}
}

func TestNativeBrowserCancelWaitsForACKBeforeNextOperation(t *testing.T) {
	started, cancelling, allowACK := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(allowACK) }) }
	defer release()
	var executions atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/execute"):
			if executions.Add(1) == 1 {
				close(started)
				<-cancelling
				return
			}
			writeJSON(w, 200, tools.BrowserReply{OK: true})
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			select {
			case <-cancelling:
			default:
				close(cancelling)
			}
			<-allowACK
			writeJSON(w, 200, map[string]bool{"ok": true})
		default:
			writeJSON(w, 200, map[string]bool{"ok": true})
		}
	})
	b := newNativeBrowser(cfg)
	t.Cleanup(b.Close)
	ctx, cancel := context.WithCancel(context.Background())
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { _, err := b.Execute(ctx, json.RawMessage(`{"action":"wait","text":"ready"}`)); first <- err }()
	nativeWait(t, started)
	cancel()
	nativeWait(t, cancelling)
	go func() {
		_, err := b.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`))
		second <- err
	}()
	select {
	case err := <-second:
		t.Fatalf("new operation escaped cancel fence: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if executions.Load() != 1 {
		t.Fatal("second worker operation started before ACK")
	}
	release()
	select {
	case err := <-first:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lost cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not finish")
	}
	select {
	case err := <-second:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queue did not resume after ACK")
	}
}

func TestNativeBrowserFailedCancelBlocksFurtherActions(t *testing.T) {
	var executions atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/execute"):
			executions.Add(1)
			http.Error(w, "upstream failed", 502)
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			writeJSON(w, 200, map[string]bool{"ok": false})
		default:
			writeJSON(w, 200, map[string]bool{"ok": true})
		}
	})
	b := newNativeBrowser(cfg)
	t.Cleanup(b.Close)
	if _, err := b.Execute(context.Background(), json.RawMessage(`{"action":"click","target":"e1"}`)); err == nil {
		t.Fatal("uncertain execution succeeded")
	}
	if _, err := b.Execute(context.Background(), json.RawMessage(`{"action":"click","target":"e1"}`)); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("failed cancellation did not fail closed: %v", err)
	}
	if executions.Load() != 1 {
		t.Fatal("uncertain action was retried")
	}
}

func TestNativeBrowserInputCancellationFencesRetryAndHandoff(t *testing.T) {
	for _, acknowledged := range []bool{true, false} {
		t.Run(map[bool]string{true: "acknowledged", false: "unconfirmed"}[acknowledged], func(t *testing.T) {
			started, cancelling, allowACK := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce, cancelOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(allowACK) }) }
			defer release()
			var inputs, handoffs atomic.Int32
			cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/input"):
					if inputs.Add(1) == 1 {
						close(started)
						<-cancelling
						return
					}
					writeJSON(w, 200, map[string]string{"control": "human"})
				case strings.HasSuffix(r.URL.Path, "/cancel"):
					cancelOnce.Do(func() { close(cancelling) })
					<-allowACK
					writeJSON(w, 200, map[string]bool{"ok": acknowledged})
				case strings.HasSuffix(r.URL.Path, "/control"):
					handoffs.Add(1)
					writeJSON(w, 200, nativeSheetsState("t1"))
				default:
					writeJSON(w, 200, map[string]bool{"ok": true})
				}
			})
			b := newNativeBrowser(cfg)
			t.Cleanup(b.Close)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			first, second := make(chan error, 1), make(chan error, 1)
			input := nativeBrowserInput{Type: "key", Key: "Enter", LeaseToken: "lease"}
			go func() { _, err := b.Input(ctx, input); first <- err }()
			nativeWait(t, started)
			cancel()
			nativeWait(t, cancelling)
			go func() { _, err := b.Input(context.Background(), input); second <- err }()
			select {
			case err := <-second:
				t.Fatalf("input retry escaped pending cancellation: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			if inputs.Load() != 1 {
				t.Fatal("input was replayed before cancellation ACK")
			}
			release()
			if err := <-first; !errors.Is(err, context.Canceled) {
				t.Fatalf("lost input cancellation: %v", err)
			}
			err := <-second
			_, handoffErr := b.Control(context.Background(), nativeBrowserControl{Mode: "agent", LeaseToken: "lease", ContextRef: nativeSheetsContext, TabID: "t1"})
			if acknowledged {
				if err != nil || handoffErr != nil || inputs.Load() != 2 || handoffs.Load() != 1 {
					t.Fatalf("acknowledged queue did not resume: %v %v", err, handoffErr)
				}
			} else if err == nil || !strings.Contains(err.Error(), "blocked") || handoffErr == nil || inputs.Load() != 1 || handoffs.Load() != 0 {
				t.Fatalf("unconfirmed input allowed retry or handoff: %v %v", err, handoffErr)
			}
		})
	}
}

func TestNativeBrowserStateRecoversOnlyConfirmedMissingSession(t *testing.T) {
	var exists atomic.Bool
	var stateFailure, creates, executes atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			creates.Add(1)
			exists.Store(true)
			writeJSON(w, 200, map[string]bool{"ok": true})
		case strings.HasSuffix(r.URL.Path, "/state"):
			if status := stateFailure.Load(); status != 0 {
				writeErr(w, int(status), errors.New("state unavailable"))
				return
			}
			if !exists.Load() {
				writeErr(w, 404, errors.New("session expired"))
				return
			}
			writeJSON(w, 200, map[string]any{"control": "agent", "url": "about:blank", "busy": false, "control_epoch": 3})
		case strings.HasSuffix(r.URL.Path, "/execute"):
			executes.Add(1)
			writeErr(w, 404, errors.New("session expired"))
		default:
			writeJSON(w, 200, map[string]bool{"ok": true})
		}
	})
	b := newNativeBrowser(cfg)
	t.Cleanup(b.Close)
	state, err := b.State(context.Background())
	if err != nil || creates.Load() != 1 || state["reset"] != nil || string(state["control_epoch"]) != "3" || string(state["busy"]) != "false" {
		t.Fatalf("initial state: %v %v", state, err)
	}
	exists.Store(false)
	if _, err := b.Execute(context.Background(), json.RawMessage(`{"action":"click","target":"e1"}`)); err == nil || creates.Load() != 1 || executes.Load() != 1 {
		t.Fatal("lost action recreated session or replayed an unsafe operation")
	}
	state, err = b.State(context.Background())
	if err != nil || creates.Load() != 2 || executes.Load() != 1 || string(state["reset"]) != "true" || len(state["reset_reason"]) == 0 {
		t.Fatalf("confirmed expiry did not recover with reset notice: %v %v", state, err)
	}
	stateFailure.Store(502)
	if _, err := b.State(context.Background()); err == nil || creates.Load() != 2 {
		t.Fatal("unconfirmed transport failure recreated session")
	}
}

func TestNativeBrowserClosedSessionWaitsForCleanupACK(t *testing.T) {
	deleting, allowACK := make(chan struct{}), make(chan struct{})
	var deleteOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(allowACK) }) }
	defer release()
	var status, creates, executes atomic.Int32
	status.Store(200)
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			creates.Add(1)
			status.Store(200)
			writeJSON(w, 200, map[string]bool{"ok": true})
		case r.Method == http.MethodDelete:
			deleteOnce.Do(func() { close(deleting) })
			<-allowACK
			status.Store(404)
			writeJSON(w, 200, map[string]bool{"ok": true})
		case strings.HasSuffix(r.URL.Path, "/state"):
			if code := int(status.Load()); code != 200 {
				writeErr(w, code, errors.New("closed session"))
				return
			}
			writeJSON(w, 200, map[string]string{"control": "agent", "url": "about:blank"})
		case strings.HasSuffix(r.URL.Path, "/execute"):
			executes.Add(1)
			writeJSON(w, 200, tools.BrowserReply{OK: true})
		default:
			writeJSON(w, 200, map[string]bool{"ok": true})
		}
	})
	b := newNativeBrowser(cfg)
	t.Cleanup(b.Close)
	if _, err := b.State(context.Background()); err != nil {
		t.Fatal(err)
	}
	status.Store(410)
	resetDone, actionDone := make(chan error, 1), make(chan error, 1)
	go func() {
		state, err := b.State(context.Background())
		if err == nil && (string(state["reset"]) != "true" || !strings.Contains(string(state["reset_reason"]), "登录状态未恢复")) {
			err = errors.New("reset did not report lost page and login state")
		}
		resetDone <- err
	}()
	nativeWait(t, deleting)
	go func() {
		_, err := b.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`))
		actionDone <- err
	}()
	select {
	case err := <-actionDone:
		t.Fatalf("browser action bypassed cleanup: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if creates.Load() != 1 || executes.Load() != 0 {
		t.Fatal("replacement context or action started without cleanup ACK")
	}
	release()
	if err := <-resetDone; err != nil {
		t.Fatal(err)
	}
	if err := <-actionDone; err != nil {
		t.Fatal(err)
	}
	if creates.Load() != 2 || executes.Load() != 1 {
		t.Fatal("state reset replayed an action or created duplicate contexts")
	}
}

func TestNativeBrowserClosedSessionCleanupFailsClosed(t *testing.T) {
	for _, failure := range []string{"http", "false_ack", "malformed_ack"} {
		t.Run(failure, func(t *testing.T) {
			var status, creates, deletes, mutations atomic.Int32
			var allowCleanup atomic.Bool
			status.Store(200)
			cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPut:
					creates.Add(1)
					status.Store(200)
					writeJSON(w, 200, map[string]bool{"ok": true})
				case r.Method == http.MethodDelete:
					deletes.Add(1)
					// Even a later 404 cannot waive an earlier cleanup ACK obligation.
					status.Store(404)
					if !allowCleanup.Load() {
						switch failure {
						case "http":
							writeErr(w, 503, errors.New("context close is still pending"))
						case "false_ack":
							writeJSON(w, 200, map[string]bool{"ok": false})
						case "malformed_ack":
							_, _ = w.Write([]byte("invalid"))
						}
						return
					}
					writeJSON(w, 200, map[string]bool{"ok": true})
				case strings.HasSuffix(r.URL.Path, "/state"):
					if code := int(status.Load()); code != 200 {
						writeErr(w, code, errors.New("closed session"))
						return
					}
					writeJSON(w, 200, nativeSheetsState("t1"))
				case strings.HasSuffix(r.URL.Path, "/cancel"):
					writeJSON(w, 200, map[string]bool{"ok": true})
				default:
					mutations.Add(1)
					writeJSON(w, 200, map[string]any{"ok": true, "lease_token": "unsafe-lease"})
				}
			})
			b := newNativeBrowser(cfg)
			t.Cleanup(b.Close)
			if _, err := b.State(context.Background()); err != nil {
				t.Fatal(err)
			}
			status.Store(410)
			if _, err := b.State(context.Background()); err == nil || creates.Load() != 1 || deletes.Load() != 1 {
				t.Fatal("unconfirmed cleanup allowed context recreation")
			}
			// A no-op cancel ACK does not acknowledge context destruction.
			if err := b.cancelAndConfirm(); err != nil {
				t.Fatal(err)
			}
			if _, err := b.Execute(context.Background(), json.RawMessage(`{"action":"click","target":"#submit"}`)); err == nil {
				t.Fatal("unclean context accepted an agent action")
			}
			if _, err := b.Input(context.Background(), nativeBrowserInput{Type: "key", Key: "Enter", LeaseToken: "lease"}); err == nil {
				t.Fatal("unclean context accepted human input")
			}
			if _, err := b.Control(context.Background(), nativeBrowserControl{Mode: "human", Force: true, ContextRef: nativeSheetsContext, TabID: "t1"}); err == nil {
				t.Fatal("unclean context accepted control takeover")
			}
			if _, err := b.State(context.Background()); err == nil || creates.Load() != 1 || deletes.Load() != 2 || mutations.Load() != 0 {
				t.Fatal("later 404 bypassed unacknowledged cleanup")
			}
			allowCleanup.Store(true)
			state, err := b.State(context.Background())
			if err != nil || string(state["reset"]) != "true" || creates.Load() != 2 || deletes.Load() != 3 || mutations.Load() != 0 {
				t.Fatalf("explicit cleanup retry did not recover without replay: %v %v", state, err)
			}
		})
	}
}

func TestNativeBrowserHumanTakeoverCanFenceActiveOperation(t *testing.T) {
	started, humanFence := make(chan struct{}), make(chan struct{})
	var cancels atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/execute"):
			close(started)
			<-humanFence
			writeJSON(w, 200, tools.BrowserReply{OK: false, Error: "human control took over"})
		case strings.HasSuffix(r.URL.Path, "/control"):
			close(humanFence)
			writeJSON(w, 200, map[string]any{"context_ref": nativeSheetsContext, "viewed_tab": "t1", "control": "human", "lease_token": "human-lease", "url": "https://example.test/login"})
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			cancels.Add(1)
			writeJSON(w, 200, map[string]bool{"ok": true})
		default:
			writeJSON(w, 200, map[string]bool{"ok": true})
		}
	})
	b := newNativeBrowser(cfg)
	t.Cleanup(b.Close)
	done := make(chan error, 1)
	go func() {
		_, err := b.Execute(context.Background(), json.RawMessage(`{"action":"batch","actions":[{"action":"snapshot"}]}`))
		done <- err
	}()
	nativeWait(t, started)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	state, err := b.Control(ctx, nativeBrowserControl{Mode: "human", ContextRef: nativeSheetsContext, TabID: "t1"})
	if err != nil || string(state["lease_token"]) != `"human-lease"` || cancels.Load() != 0 {
		t.Fatalf("takeover failed to preserve active page: %v %v cancel=%d", state, err, cancels.Load())
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestNativeBrowserAPIDisplayOnlyFramePreservesPixelsWithoutAuthority(t *testing.T) {
	var jpegData bytes.Buffer
	if err := jpeg.Encode(&jpegData, image.NewRGBA(image.Rect(0, 0, 2, 3)), nil); err != nil {
		t.Fatal(err)
	}
	var pending atomic.Bool
	var inputs, cancels atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/frame"):
			if pending.Load() {
				writeErr(w, http.StatusConflict, errors.New("frame observation pending"))
				return
			}
			// A worker must not grant coordinate authority by attaching stale
			// optional metadata to an otherwise useful display-only JPEG.
			w.Header().Set("X-Browser-Frame-Identity", "1:t1:1")
			w.Header().Set("X-Browser-Overlay", encodeNativeOverlay(t, nativeOverlayFixture()))
			_, _ = w.Write(jpegData.Bytes())
		case strings.HasSuffix(r.URL.Path, "/input"), strings.HasSuffix(r.URL.Path, "/pick"):
			inputs.Add(1)
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			cancels.Add(1)
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		default:
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "control": "human", "version": "state-version"})
		}
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.mgr.cfg.browser = cfg
	sid := h.createAs(aliceToken)
	base := "/api/sessions/" + sid + "/browser"
	res := h.doAs(aliceToken, http.MethodGet, base+"/frame", "")
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/jpeg" ||
		res.Header.Get("Cache-Control") != "no-store" || !bytes.Equal(raw, jpegData.Bytes()) {
		t.Fatal("display-only frame lost its pixels or image response contract")
	}
	if res.Header.Get("X-Browser-Version") != "" || res.Header.Get("X-Browser-Overlay") != "" {
		t.Fatal("display-only frame retained coordinate or overlay authority")
	}
	if res.Header.Get("X-Browser-Frame-Identity") != "1:t1:1" {
		t.Fatal("display-only pixels lost their top-document identity")
	}
	for _, tc := range []struct{ route, body string }{
		{"/input", `{"type":"click","x":1,"y":2,"lease_token":"human-lease"}`},
		{"/pick", `{"x":1,"y":2,"tab_id":"t1","version":""}`},
	} {
		res = h.doAs(aliceToken, http.MethodPost, base+tc.route, tc.body)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("unversioned coordinate request accepted: %s %d", tc.route, res.StatusCode)
		}
	}
	if inputs.Load() != 0 {
		t.Fatal("unversioned coordinate request reached the worker")
	}
	pending.Store(true)
	res = h.doAs(aliceToken, http.MethodGet, base+"/frame", "")
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatal("read-only frame conflict lost its HTTP classification")
	}
	res = h.doAs(aliceToken, http.MethodGet, base, "")
	res.Body.Close()
	if res.StatusCode != http.StatusOK || cancels.Load() != 0 {
		t.Fatal("frame conflict cancelled the browser or made state unavailable")
	}
}

func TestNativeBrowserAPIIsolationLeaseAndScreenshot(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	deleted := map[string]bool{}
	var inputs, workerRequests atomic.Int32
	accepted := make(chan nativeBrowserInput, 8)
	var pngData, jpegData bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	_ = png.Encode(&pngData, img)
	_ = jpeg.Encode(&jpegData, img, nil)
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		workerRequests.Add(1)
		id := strings.Split(strings.TrimPrefix(r.URL.Path, "/sessions/"), "/")[0]
		mu.Lock()
		seen[id] = true
		if r.Method == http.MethodDelete {
			deleted[id] = true
		}
		mu.Unlock()
		state := map[string]any{"context_ref": nativeSheetsContext, "viewed_tab": "t1", "control": "agent", "url": "https://example.test/", "tabs": []any{}, "version": "v1", "lease_token": "must-not-leak", "session_id": id, "worker_url": "private-worker"}
		switch {
		case strings.HasSuffix(r.URL.Path, "/frame"):
			w.Header().Set("X-Browser-Frame-Identity", "1:t1:1")
			w.Header().Set("X-Browser-Version", "frame-v1")
			overlay := nativeOverlayFixture()
			overlay["version"] = "frame-v1"
			w.Header().Set("X-Browser-Overlay", encodeNativeOverlay(t, overlay))
			w.Write(jpegData.Bytes())
		case strings.HasSuffix(r.URL.Path, "/control"):
			state["control"] = "human"
			state["lease_token"] = "human-lease"
			writeJSON(w, 200, state)
		case strings.HasSuffix(r.URL.Path, "/input"):
			inputs.Add(1)
			var input nativeBrowserInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input.LeaseToken != "human-lease" {
				writeErr(w, 403, errors.New("invalid human lease"))
				return
			}
			accepted <- input
			writeJSON(w, 200, state)
		case strings.HasSuffix(r.URL.Path, "/execute"):
			writeJSON(w, 200, tools.BrowserReply{OK: true, Screenshot: &tools.BrowserScreenshot{Data: base64.StdEncoding.EncodeToString(pngData.Bytes()), Mime: "image/png", Width: 900, Height: 900}})
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			writeJSON(w, 200, map[string]bool{"ok": true})
		default:
			writeJSON(w, 200, state)
		}
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.mgr.cfg.browser = cfg
	alice, bob := h.createAs(aliceToken), h.createAs(bobToken)
	for _, sid := range []string{alice, bob} {
		token := aliceToken
		if sid == bob {
			token = bobToken
		}
		res := h.doAs(token, "GET", "/api/sessions/"+sid+"/browser", "")
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !bytes.Contains(raw, []byte(`"backend":"native"`)) || bytes.Contains(raw, []byte("must-not-leak")) || bytes.Contains(raw, []byte("session_id")) || bytes.Contains(raw, []byte("private-worker")) {
			t.Fatalf("unsafe browser state: %d %s", res.StatusCode, raw)
		}
	}
	beforeRejected := workerRequests.Load()
	res := h.doAs("", "GET", "/api/sessions/"+alice+"/browser", "")
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("browser state did not require authentication: %d", res.StatusCode)
	}
	request, err := http.NewRequest(http.MethodPost, h.srv.URL+"/api/sessions/"+alice+"/browser/control", strings.NewReader(`{"mode":"human"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+aliceToken)
	request.Header.Set("Origin", "https://other-origin.test")
	res, err = h.srv.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin control was not rejected: %d", res.StatusCode)
	}
	for _, route := range []struct{ method, suffix, body string }{
		{"GET", "", ""}, {"GET", "/frame", ""},
		{"POST", "/control", `{"mode":"human"}`},
		{"POST", "/input", `{"type":"key","key":"Enter","lease_token":"human-lease"}`},
	} {
		res := h.doAs(bobToken, route.method, "/api/sessions/"+alice+"/browser"+route.suffix, route.body)
		res.Body.Close()
		if res.StatusCode != 404 {
			t.Fatalf("cross-owner browser access %s: %d", route.suffix, res.StatusCode)
		}
	}
	if workerRequests.Load() != beforeRejected {
		t.Fatal("rejected browser API request reached the worker")
	}
	mu.Lock()
	if len(seen) != 2 || seen[alice] || seen[bob] {
		t.Errorf("worker session mapping leaked or collided: %v", seen)
	}
	mu.Unlock()
	base := "/api/sessions/" + alice + "/browser"
	res = h.doAs(aliceToken, "POST", base+"/control", `{"mode":"human","context_ref":"ctx_1234567890abcdef12345678","tab_id":"t1"}`)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !bytes.Contains(raw, []byte("human-lease")) {
		t.Fatalf("human lease missing: %d %s", res.StatusCode, raw)
	}
	for _, body := range []string{
		`{"type":"click","x":1,"y":1}`,
		`{"type":"click","x":1,"y":1,"lease_token":"human-lease"}`,
		`{"type":"click","x":1,"y":1,"lease_token":"human-lease","worker_url":"https://attacker.test"}`,
		`{"type":"tab","tab_action":"switch","lease_token":"human-lease"}`,
		nativeSheetsInput(`{"type":"tab","tab_action":"new","lease_token":"human-lease"}`),
		`{"type":"tab","tab_action":"close","lease_token":"human-lease"}`,
		`{"type":"tab","tab_action":"new","url":"file:///etc/passwd","lease_token":"human-lease"}`,
	} {
		res = h.doAs(aliceToken, "POST", base+"/input", body)
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatal("unvetted input was forwarded")
		}
	}
	if inputs.Load() != 0 {
		t.Fatal("invalid API input reached worker")
	}
	res = h.doAs(aliceToken, "POST", base+"/input", nativeSheetsInput(`{"type":"click","x":1,"y":1,"version":"v1","lease_token":"wrong"}`))
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("worker lease refusal was hidden")
	}
	h.psrv.opts.Panels = []Panel{{Name: "saved", URL: "https://original.test/app", ProxyMode: "compat"}}
	for _, tc := range []struct {
		body  string
		check func(nativeBrowserInput) bool
	}{
		{`{"type":"scroll","deltaX":21,"lease_token":"human-lease"}`, func(in nativeBrowserInput) bool { return in.DeltaX != nil && *in.DeltaX == 21 }},
		{`{"type":"click","x":1,"y":2,"version":"frame-v1","lease_token":"human-lease"}`, func(in nativeBrowserInput) bool { return in.Version == "frame-v1" }},
	} {
		res = h.doAs(aliceToken, "POST", base+"/input", nativeSheetsInput(tc.body))
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("valid input rejected: %d %s", res.StatusCode, tc.body)
		}
		if !tc.check(<-accepted) {
			t.Fatalf("input contract changed during forwarding: %s", tc.body)
		}
	}
	res = h.doAs(aliceToken, "GET", base+"/frame", "")
	raw, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/jpeg" || res.Header.Get("X-Browser-Version") != "frame-v1" || !bytes.Equal(raw, jpegData.Bytes()) {
		t.Fatal("frame contract lost pixels or version")
	}
	if header := res.Header.Get("X-Browser-Overlay"); header == "" {
		t.Fatal("authenticated frame response lost its sanitized overlay")
	} else if decoded, err := base64.RawURLEncoding.DecodeString(header); err != nil || bytes.Contains(decoded, []byte("must-not-export")) {
		t.Fatal("frame response leaked unknown worker metadata")
	}
	sess, err := h.mgr.Get(alice)
	if err != nil {
		t.Fatal(err)
	}
	sess.browser.Arm()
	reply, err := sess.browser.Execute(context.Background(), json.RawMessage(`{"action":"screenshot"}`))
	if err != nil || reply.Screenshot == nil || reply.Screenshot.Data != "" || reply.Screenshot.ImageRef == "" || reply.Screenshot.Width != 2 || reply.Screenshot.Height != 3 {
		t.Fatalf("screenshot not persisted by reference: %+v %v", reply, err)
	}
	saved, err := os.ReadFile(filepath.Join(sess.cwd, filepath.FromSlash(reply.Screenshot.ImageRef)))
	if err != nil || !bytes.Equal(saved, pngData.Bytes()) {
		t.Fatalf("screenshot bytes lost: %v", err)
	}
	sess.Close()
	mu.Lock()
	nDeleted := len(deleted)
	mu.Unlock()
	if nDeleted != 1 {
		t.Fatalf("session close did not reclaim worker: %d", nDeleted)
	}
}
