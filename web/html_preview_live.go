package web

// One bounded observer belongs to each already-authorized HTML capability.
// It reuses the conversation's existing Hub rather than changing tools or the
// agent loop, and only exposes opaque asset revisions to the sandboxed page.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	htmlPreviewLiveScript = "__pogo_live.js"
	htmlPreviewLiveEvents = "__pogo_events__"
	htmlPreviewLiveRevision = "__pogo_revision__"
	htmlPreviewWatchedAssets = 256
	htmlPreviewOwnerStreams = 2
	htmlPreviewLiveDebounce = 120 * time.Millisecond
	htmlPreviewLivePoll = 2 * time.Second
)

type htmlPreviewLiveState struct {
	mu sync.Mutex
	assets map[string]os.FileInfo
	revision uint64
	stableRevision uint64
	observedRevision uint64
	observedAt time.Time
	done chan struct{}
	stopOnce sync.Once
}

func newHTMLPreviewLiveState(entry string, info os.FileInfo) *htmlPreviewLiveState {
	return &htmlPreviewLiveState{assets: map[string]os.FileInfo{entry: info}, done: make(chan struct{})}
}

func (state *htmlPreviewLiveState) close() {
	if state != nil {
		state.stopOnce.Do(func() { close(state.done) })
	}
}

func sameHTMLPreviewAsset(before, after os.FileInfo) bool {
	if before == nil || after == nil {
		return before == nil && after == nil
	}
	return os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime()) && before.Mode() == after.Mode()
}

func (state *htmlPreviewLiveState) noteAsset(relative string, info os.FileInfo) uint64 {
	if state == nil {
		return 0
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	before, known := state.assets[relative]
	if !known {
		if len(state.assets) < htmlPreviewWatchedAssets {
			state.assets[relative] = info
		}
	} else if !sameHTMLPreviewAsset(before, info) {
		state.assets[relative] = info
		state.revision++
	}
	return state.revision
}

func (state *htmlPreviewLiveState) currentRevision() uint64 {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.revision
}

func (state *htmlPreviewLiveState) inspectRevision(ctx context.Context, root *os.Root) (uint64, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	changed := false
	for relative, before := range state.assets {
		if err := ctx.Err(); err != nil {
			return state.revision, err
		}
		select {
		case <-state.done:
			return state.revision, context.Canceled
		default:
		}
		file, after, err := openHTMLPreviewFile(root, relative)
		if err != nil {
			after = nil
		} else {
			file.Close()
		}
		if !sameHTMLPreviewAsset(before, after) {
			state.assets[relative] = after
			changed = true
		}
	}
	if changed {
		state.revision++
	}
	return state.revision, nil
}

func serveHTMLPreviewWithLiveScript(w http.ResponseWriter, r *http.Request, file *os.File, info os.FileInfo, token string, revision uint64, expires time.Time) {
	// A transformed response must not inherit a 304 from the source HTML's
	// mtime: a CSS/JS change can require a fresh live revision with identical
	// HTML bytes. The original file and ordinary source/download APIs stay as-is.
	data, err := io.ReadAll(io.NewSectionReader(file, 0, info.Size()))
	if err != nil || int64(len(data)) != info.Size() {
		writeErr(w, http.StatusConflict, fmt.Errorf("HTML artifact changed during preview; refresh to retry"))
		return
	}
	ttl := max(int64(0), time.Until(expires).Milliseconds())
	script := "\n<script src=\"" + htmlPreviewPrefix + token + "/" + htmlPreviewLiveScript + "?revision=" + strconv.FormatUint(revision, 10) + "&amp;ttl_ms=" + strconv.FormatInt(ttl, 10) + "\"></script>\n"
	data = append(data, script...)
	http.ServeContent(w, r, info.Name(), time.Time{}, bytes.NewReader(data))
}

const htmlPreviewLiveJavaScript = `(function () {
  "use strict";
  var script = document.currentScript;
  if (!script) return;
  var source = new URL(script.src, location.href);
  var revision = source.searchParams.get("revision") || "0";
  var ttl = Math.min(Number(source.searchParams.get("ttl_ms")) || 0, 30 * 60 * 1000);
  if (ttl <= 0) return;
  var pollURL = new URL(source.href);
  pollURL.pathname = pollURL.pathname.slice(0, -"__pogo_live.js".length) + "__pogo_revision__";
  pollURL.search = "";
  source.pathname = source.pathname.slice(0, -"__pogo_live.js".length) + "__pogo_events__";
  source.search = "?revision=" + encodeURIComponent(revision);
  var stream = null;
  var refresh = null;
  var pollTimer = null;
  var polling = false;
  var stopped = false;
  var expiryTimer = setTimeout(stop, ttl);
  function stop() {
    stopped = true;
    if (refresh !== null) clearTimeout(refresh);
    if (pollTimer !== null) clearTimeout(pollTimer);
    clearTimeout(expiryTimer);
    if (stream) stream.close();
  }
  function changed(next) {
    if (stopped || Number(next) <= Number(revision) || refresh !== null) return;
    refresh = setTimeout(function () { stop(); location.reload(); }, 30);
  }
  function schedulePoll(delay) {
    if (!stopped) pollTimer = setTimeout(poll, delay);
  }
  function poll() {
    if (stopped) return;
    pollTimer = null;
    fetch(pollURL.href, { mode: "cors", credentials: "omit", cache: "no-store", referrerPolicy: "no-referrer" }).then(function (response) {
      if (response.status === 404 || response.status === 410) { stop(); return null; }
      if (!response.ok) throw new Error("preview temporarily unavailable");
      return response.json();
    }).then(function (value) {
      if (stopped || !value) return;
      changed(value.revision);
      schedulePoll(value.waiting ? 150 : 2000);
    }).catch(function () { schedulePoll(2000); });
  }
  function fallback() {
    if (stopped || polling) return;
    polling = true;
    if (stream) stream.close();
    schedulePoll(120);
  }
  if (typeof EventSource === "function") {
    stream = new EventSource(source.href, { withCredentials: false });
    stream.addEventListener("changed", function (event) {
      try { changed(JSON.parse(event.data).revision); } catch (_) {}
    });
    stream.addEventListener("closed", stop);
    stream.onerror = fallback;
  } else {
    fallback();
  }
  addEventListener("pagehide", stop, { once: true });
})();
`

func serveHTMLPreviewLiveScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	http.ServeContent(w, r, htmlPreviewLiveScript, time.Time{}, bytes.NewReader([]byte(htmlPreviewLiveJavaScript)))
}

func htmlPreviewSessionBusy(sess *Session) bool {
	sess.mu.Lock()
	busy := sess.active != nil || sess.compacting
	sess.mu.Unlock()
	if busy {
		return true
	}
	hub := sess.Hub()
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for _, task := range hub.bgTasks {
		if task.Status == "running" {
			return true
		}
	}
	return false
}

func htmlPreviewWriteEvent(w http.ResponseWriter, name string, value any) bool {
	payload, err := json.Marshal(value)
	if err != nil {
		return false
	}
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, payload); err != nil {
		return false
	}
	return controller.Flush() == nil
}

func (s *Server) claimHTMLPreviewStream(capability *htmlPreviewCapability) (func(), bool) {
	owner, _ := json.Marshal(capability.owner)
	key := string(owner)
	s.htmlPreviewMu.Lock()
	defer s.htmlPreviewMu.Unlock()
	if s.htmlPreviewClosed || s.htmlPreviews[capability.token] != capability {
		return nil, false
	}
	if s.htmlPreviewStreamCounts == nil {
		s.htmlPreviewStreamCounts = make(map[string]int)
	}
	if s.htmlPreviewStreamCounts[key] >= htmlPreviewOwnerStreams {
		return nil, false
	}
	s.htmlPreviewStreamCounts[key]++
	return func() {
		s.htmlPreviewMu.Lock()
		defer s.htmlPreviewMu.Unlock()
		if s.htmlPreviewStreamCounts[key] <= 1 {
			delete(s.htmlPreviewStreamCounts, key)
		} else {
			s.htmlPreviewStreamCounts[key]--
		}
	}, true
}

func (s *Server) serveHTMLPreviewRevision(w http.ResponseWriter, r *http.Request, capability *htmlPreviewCapability, root *os.Root) {
	sess, err := s.mgr.Get(capability.sessionID)
	if err != nil || capability.live == nil {
		http.NotFound(w, r)
		return
	}
	state := capability.live
	if htmlPreviewSessionBusy(sess) {
		state.mu.Lock()
		state.observedAt = time.Time{}
		revision := state.stableRevision
		state.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"revision": revision, "busy": true, "waiting": false})
		return
	}
	revision, err := state.inspectRevision(r.Context(), root)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	state.mu.Lock()
	now := time.Now()
	if state.observedRevision != revision || state.observedAt.IsZero() {
		state.observedRevision, state.observedAt = revision, now
	} else if now.Sub(state.observedAt) >= htmlPreviewLiveDebounce {
		state.stableRevision = revision
	}
	stable := state.stableRevision
	state.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"revision": stable, "busy": false, "waiting": stable < revision})
}

func (s *Server) serveHTMLPreviewEvents(w http.ResponseWriter, r *http.Request, capability *htmlPreviewCapability, root *os.Root) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	if capability.live == nil {
		http.NotFound(w, r)
		return
	}
	release, admitted := s.claimHTMLPreviewStream(capability)
	if !admitted {
		w.Header().Set("Retry-After", "2")
		writeErr(w, http.StatusTooManyRequests, fmt.Errorf("HTML preview is using short revision polling"))
		return
	}
	defer release()
	sess, err := s.mgr.Get(capability.sessionID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	hub := sess.Hub()
	hub.mu.Lock()
	sequence := hub.seq
	hub.mu.Unlock()
	_, events, unsubscribe := hub.Subscribe(sequence)
	defer unsubscribe()
	lastSent := capability.live.currentRevision()
	if prior, err := strconv.ParseUint(r.URL.Query().Get("revision"), 10, 64); err == nil {
		lastSent = prior
	}
	if !htmlPreviewWriteEvent(w, "connected", struct{}{}) {
		return
	}
	poll := time.NewTicker(htmlPreviewLivePoll)
	defer poll.Stop()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	debounce := time.NewTimer(htmlPreviewLiveDebounce)
	defer debounce.Stop()
	pending := debounce.C
	var candidate uint64
	hasCandidate := false
	schedule := func() {
		if pending == nil {
			debounce.Reset(htmlPreviewLiveDebounce)
			pending = debounce.C
		}
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-capability.live.done:
			htmlPreviewWriteEvent(w, "closed", struct{}{})
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			switch event.Type {
			case EvRunEnd, EvSubagentEnd, EvRewound:
				schedule()
			}
		case <-poll.C:
			schedule()
		case <-pending:
			pending = nil
			if !s.htmlPreviewSessionValid(r.Context(), capability) {
				s.revokeHTMLPreview(capability.token)
				return
			}
			// run_end is published before Session.finish clears its active
			// slot. Wait for that final persistence and every background child
			// rather than replacing a page during a partially written tool run.
			if htmlPreviewSessionBusy(sess) {
				hasCandidate = false
				schedule()
				continue
			}
			revision, err := capability.live.inspectRevision(r.Context(), root)
			if err != nil {
				return
			}
			if revision == lastSent {
				hasCandidate = false
				continue
			}
			// A second stable observation coalesces a burst of external saves
			// and avoids reloading on the first half of an atomic replacement.
			if !hasCandidate || candidate != revision {
				candidate, hasCandidate = revision, true
				schedule()
				continue
			}
			if !htmlPreviewWriteEvent(w, "changed", struct {
				Revision uint64 `json:"revision"`
			}{revision}) {
				return
			}
			lastSent, hasCandidate = revision, false
		case <-keepalive.C:
			controller := http.NewResponseController(w)
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.WriteString(w, ": preview keepalive\n\n"); err != nil || controller.Flush() != nil {
				return
			}
		}
	}
}
