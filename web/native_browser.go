package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/tools"
)

const nativeBrowserCancelTimeout = 15 * time.Second
const nativeBrowserResponseLimit = 24 << 20
const nativeBrowserFocusMetadataTimeout = 500 * time.Millisecond

// A public frame identity names only our generation/tab/top-document counters,
// never a Chromium target or session ID. Consumers treat it as an opaque stamp.
var nativeFrameIdentity = regexp.MustCompile(`^[1-9][0-9]{0,15}:t[1-9][0-9]{0,9}:[1-9][0-9]{0,15}$`)

func validNativeFrameIdentity(value string) bool {
	return len(value) <= 64 && nativeFrameIdentity.MatchString(value)
}

type nativeBrowserFrame struct {
	Data       []byte
	Version    string
	Identity   string
	Overlay    string
	ContextRef string
	ViewedTab  string
}

type nativeBrowserConfig struct {
	url, token string
	client     *http.Client
}

func parseNativeBrowserConfig(endpoint, token string) (*nativeBrowserConfig, error) {
	endpoint, token = strings.TrimSpace(endpoint), strings.TrimSpace(token)
	if endpoint == "" {
		if token != "" {
			return nil, errors.New("POGO_BROWSER_TOKEN requires POGO_BROWSER_URL")
		}
		return nil, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("POGO_BROWSER_URL must be an absolute http(s) service URL without credentials, query or fragment")
	}
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("POGO_BROWSER_TOKEN is required and must not contain line breaks")
	}
	return &nativeBrowserConfig{url: strings.TrimRight(u.String(), "/"), token: token, client: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// nativeBrowser owns a worker session. The random worker ID is never accepted
// from a caller or exposed by the API. UI connections are observers, not agents
// competing to execute the same broadcast command.
type nativeBrowser struct {
	config                     *nativeBrowserConfig
	id                         string
	op                         chan struct{}
	mu                         sync.Mutex
	created, attempted, closed bool
	cleanupRequired            bool // A confirmed 410 requires DELETE ACK before another context.
	blocked                    string
	storeScreenshot            func(*tools.BrowserScreenshot) error
	rememberIcons              func([]nativeBrowserIcon)
	// Trusted Go-to-host metadata, never accepted from or returned to the UI.
	// The worker id remains a fresh execution identity; this separate key lets
	// a desktop host retain the same owning session's profile after reconnect.
	clientSessionID, profileKey string
	profileError                error
	downloads                   *nativeBrowserDownloadStore
	uploadRoot                  string                            // trusted conversation cwd; never supplied by the model
	uploadCache                 map[string]nativeUploadCacheEntry // operation lock only
}

func newNativeBrowser(cfg *nativeBrowserConfig) *nativeBrowser {
	b := &nativeBrowser{config: cfg, id: randomID(24), op: make(chan struct{}, 1)}
	b.op <- struct{}{}
	return b
}

// bindSessionProfile is called only during Manager construction, before this
// executor becomes reachable. Moving a transcript store intentionally changes
// its profile namespace; a deployment migration must map that state explicitly.
func (b *nativeBrowser) bindSessionProfile(clientSessionID, storePath string, owner *identity.Scope) {
	if clientSessionID == "" || storePath == "" {
		b.profileError = errors.New("browser profile ownership metadata is unavailable")
		return
	}
	absolute, err := filepath.Abs(storePath)
	if err != nil {
		b.profileError = errors.New("browser profile ownership path cannot be resolved")
		return
	}
	// JSON encodes field boundaries unambiguously. A nil owner is distinct from
	// every account scope, including a zero-valued (invalid) scope.
	scope, _ := json.Marshal(struct {
		Version string          `json:"version"`
		Path    string          `json:"path"`
		Owner   *identity.Scope `json:"owner"`
	}{"pogo-desktop-profile-v1", filepath.Clean(absolute), owner})
	digest := sha256.Sum256(scope)
	b.clientSessionID, b.profileKey = clientSessionID, hex.EncodeToString(digest[:])
}

func (b *nativeBrowser) BrowserBackend() string { return tools.BrowserBackendNative }

type nativeBrowserHTTPError struct {
	status  int
	message string
	code    string
}

func (e *nativeBrowserHTTPError) Error() string { return e.message }

func (b *nativeBrowser) request(ctx context.Context, method, suffix string, body any) ([]byte, error) {
	raw, _, err := b.requestWithHeaders(ctx, method, suffix, body)
	return raw, err
}

func (b *nativeBrowser) requestWithHeaders(ctx context.Context, method, suffix string, body any) ([]byte, http.Header, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.config.url+"/sessions/"+url.PathEscape(b.id)+suffix, reader)
	if err != nil {
		return nil, nil, errors.New("native browser request could not be constructed")
	}
	req.Header.Set("Authorization", "Bearer "+b.config.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := b.config.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, errors.New("native browser service is unreachable; the operation was not retried")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, nativeBrowserResponseLimit+1))
	if err != nil {
		return nil, nil, errors.New("native browser response was interrupted; the operation was not retried")
	}
	if len(raw) > nativeBrowserResponseLimit {
		return nil, nil, errors.New("native browser response exceeds the size limit")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		message := fmt.Sprintf("native browser service returned HTTP %d", res.StatusCode)
		var issue struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if json.Unmarshal(raw, &issue) == nil && issue.Error != "" && len(issue.Error) <= 4096 {
			message += ": " + issue.Error
		}
		code := ""
		switch issue.Code {
		case "TAB_OPERATION_PENDING", "TAB_OPERATION_OUTCOME_UNKNOWN", "TAB_OPERATION_REJECTED", "TAB_OPERATION_ID_CONFLICT", "PICK_OBSERVATION_PENDING", "BROWSER_CONTEXT_CHANGED", "BROWSER_TAB_CLOSED", "BROWSER_SESSION_EXPIRED":
			code = issue.Code
		}
		return nil, nil, &nativeBrowserHTTPError{status: res.StatusCode, message: message, code: code}
	}
	return raw, res.Header, nil
}

func (b *nativeBrowser) ensure(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("browser session is closed")
	}
	if b.profileError != nil {
		return b.profileError
	}
	if b.created {
		return nil
	}
	b.attempted = true
	metadata := struct {
		ClientSessionID string `json:"client_session_id,omitempty"`
		ProfileKey      string `json:"profile_key,omitempty"`
	}{b.clientSessionID, b.profileKey}
	if _, err := b.request(ctx, http.MethodPut, "", metadata); err != nil {
		return err
	}
	b.created = true
	if b.downloads != nil {
		b.downloads.start()
	}
	return nil
}

func (b *nativeBrowser) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.op:
		if err := ctx.Err(); err != nil {
			b.release()
			return err
		}
		return nil
	}
}
func (b *nativeBrowser) release() { b.op <- struct{}{} }

func (b *nativeBrowser) cancelAndConfirm() error {
	ctx, cancel := context.WithTimeout(context.Background(), nativeBrowserCancelTimeout)
	defer cancel()
	raw, err := b.request(ctx, http.MethodPost, "/cancel", struct{}{})
	if err == nil {
		var ack struct {
			OK bool `json:"ok"`
		}
		if json.Unmarshal(raw, &ack) != nil || !ack.OK {
			err = errors.New("native browser cancellation was not acknowledged")
		}
	}
	b.mu.Lock()
	if err != nil {
		b.blocked = "browser cancellation could not be confirmed; further browser actions are blocked until context recovery is confirmed"
	} else if !b.cleanupRequired {
		b.blocked = ""
	}
	b.mu.Unlock()
	return err
}

func (b *nativeBrowser) Execute(ctx context.Context, raw json.RawMessage) (tools.BrowserReply, error) {
	var metadata struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(raw, &metadata) == nil && strings.TrimSpace(metadata.Action) == tools.BrowserActionDownloads {
		query, err := (&tools.Browser{Link: b}).ParseDownloadQuery(raw)
		if err != nil {
			return tools.BrowserReply{}, err
		}
		var result nativeBrowserDownloadList
		if query.DownloadID == "" {
			result, err = b.Downloads(ctx)
		} else {
			result, err = b.waitDownload(ctx, query)
		}
		if err != nil {
			return tools.BrowserReply{}, err
		}
		data, err := json.Marshal(result)
		return tools.BrowserReply{OK: err == nil, Backend: tools.BrowserBackendNative, Data: data}, err
	}
	if err := b.acquire(ctx); err != nil {
		return tools.BrowserReply{}, err
	}
	defer b.release()
	if err := b.ensure(ctx); err != nil {
		return tools.BrowserReply{}, err
	}
	b.mu.Lock()
	blocked := b.blocked
	b.mu.Unlock()
	if blocked != "" {
		return tools.BrowserReply{}, errors.New(blocked)
	}
	var op map[string]json.RawMessage
	if err := json.Unmarshal(raw, &op); err != nil || op == nil {
		return tools.BrowserReply{}, errors.New("invalid browser operation")
	}
	// New binding fields must never be silently ignored by an older worker.
	// Negotiate with a read before staging files or dispatching any new action.
	focus, err := b.pageExecutionMetadata(ctx, raw)
	if err != nil {
		return tools.BrowserReply{}, err
	}
	if metadata.Action == tools.BrowserActionUpload {
		if err := b.prepareUpload(ctx, op); err != nil {
			return tools.BrowserReply{}, err
		}
	}
	op["op_id"], _ = json.Marshal(randomID(16))
	response, err := b.request(ctx, http.MethodPost, "/execute", op)
	if err != nil {
		return tools.BrowserReply{}, b.failedOperation(err)
	}
	var reply tools.BrowserReply
	if err := json.Unmarshal(response, &reply); err != nil {
		_ = b.cancelAndConfirm()
		return tools.BrowserReply{}, errors.New("invalid native browser reply; the operation was not retried")
	}
	if ctx.Err() != nil {
		if err := b.cancelAndConfirm(); err != nil {
			return tools.BrowserReply{}, err
		}
		return tools.BrowserReply{}, ctx.Err()
	}
	reply.Backend = "native"
	if metadata.Action == tools.BrowserActionOpenPages || metadata.Action == tools.BrowserActionObservePages {
		b.sanitizePageCollectionFocus(ctx, &reply, focus, metadata.Action)
	}
	// Worker fields alone never authorize a UI focus. Older workers omit this
	// field; future workers must not contradict the captured context either.
	workerContext := reply.ContextRef
	reply.ContextRef = ""
	if reply.OK && focus.matches(reply) && (workerContext == "" || workerContext == focus.contextRef) {
		reply.ContextRef = focus.contextRef
	}
	if reply.Favicon != "" {
		rawIcon, _ := json.Marshal(reply.Favicon)
		if icon, valid := nativeBrowserFavicon(rawIcon); valid {
			reply.Favicon = icon
		} else {
			reply.Favicon = ""
		}
	}
	if reply.OK && reply.Favicon != "" {
		b.captureIcons([]nativeBrowserIcon{{URL: reply.URL, Favicon: reply.Favicon}})
	}
	if reply.Screenshot != nil && b.storeScreenshot != nil {
		if err := b.storeScreenshot(reply.Screenshot); err != nil {
			return tools.BrowserReply{}, fmt.Errorf("browser screenshot could not be stored: %w", err)
		}
	}
	return reply, nil
}

type nativeBrowserFocusMetadata struct {
	contextRef string
	generation int64
}

// Tab change intent is read from the validated tool operation. Metadata never
// makes a malformed/direct caller executable: the existing worker still
// validates and authorizes every dispatched action and every batch step.
func nativeBrowserTabChange(raw json.RawMessage) bool {
	var action struct {
		Action    string            `json:"action"`
		TabAction string            `json:"tab_action"`
		Actions   []json.RawMessage `json:"actions"`
	}
	if json.Unmarshal(raw, &action) != nil {
		return false
	}
	if action.Action == tools.BrowserActionOpenPages {
		return true
	}
	if action.Action == tools.BrowserActionTab {
		return action.TabAction == "" || action.TabAction == "new" || action.TabAction == "switch" || action.TabAction == "claim"
	}
	if action.Action == tools.BrowserActionBatch {
		for _, step := range action.Actions {
			var name struct {
				Action string `json:"action"`
			}
			if json.Unmarshal(step, &name) == nil && name.Action == tools.BrowserActionTab && nativeBrowserTabChange(step) {
				return true
			}
		}
	}
	return false
}

func (b *nativeBrowser) focusMetadata(ctx context.Context, raw json.RawMessage) nativeBrowserFocusMetadata {
	if !nativeBrowserTabChange(raw) {
		return nativeBrowserFocusMetadata{}
	}
	metadataCtx, cancel := context.WithTimeout(ctx, nativeBrowserFocusMetadataTimeout)
	defer cancel()
	state, err := b.request(metadataCtx, http.MethodGet, "/state", nil)
	if err != nil {
		return nativeBrowserFocusMetadata{}
	}
	var identity struct {
		ContextRef string `json:"context_ref"`
		Generation int64  `json:"generation"`
	}
	if json.Unmarshal(state, &identity) != nil || !validNativeContextRef(identity.ContextRef) || identity.Generation <= 0 || identity.Generation > 1<<53-1 {
		return nativeBrowserFocusMetadata{}
	}
	return nativeBrowserFocusMetadata{contextRef: identity.ContextRef, generation: identity.Generation}
}

func (identity nativeBrowserFocusMetadata) matches(reply tools.BrowserReply) bool {
	if !validNativeContextRef(identity.contextRef) || identity.generation <= 0 || !nativeTabID.MatchString(reply.ActiveTab) || len(reply.Version) > 256 {
		return false
	}
	parts := strings.SplitN(reply.Version, ":", 3)
	if len(parts) != 3 || parts[1] != reply.ActiveTab || parts[2] == "" {
		return false
	}
	generation, err := strconv.ParseInt(parts[0], 10, 64)
	return err == nil && parts[0] == strconv.FormatInt(generation, 10) && generation == identity.generation
}

func (b *nativeBrowser) failedOperation(err error) error {
	var remote *nativeBrowserHTTPError
	// A definite refusal did not start an action. A transport failure or
	// timeout is ambiguous: fence the worker before releasing the queue.
	definite := errors.As(err, &remote) && (remote.status == 400 || remote.status == 401 || remote.status == 403 || remote.status == 404 || remote.status == 409 || remote.status == 410 || remote.status == 429)
	if !definite {
		if cancelErr := b.cancelAndConfirm(); cancelErr != nil {
			return fmt.Errorf("%w; %v", err, cancelErr)
		}
	}
	return err
}

// State allowlists the public shape; worker IDs, endpoints and lease tokens do
// not become observable merely because a worker adds them to its response.
func nativeBrowserState(raw []byte, lease bool) (map[string]json.RawMessage, error) {
	var input map[string]json.RawMessage
	if err := json.Unmarshal(raw, &input); err != nil || input == nil {
		return nil, errors.New("invalid native browser state")
	}
	out := map[string]json.RawMessage{"backend": json.RawMessage(`"native"`)}
	for _, key := range []string{"control", "url", "title", "active_tab", "viewport", "dialog", "generation", "document_version", "version", "busy", "control_epoch", "reset", "reset_reason"} {
		if value, ok := input[key]; ok {
			out[key] = value
		}
	}
	if tabs := nativeBrowserPublicTabs(input["tabs"]); tabs != nil {
		out["tabs"] = tabs
	}
	if favicon, valid := nativeBrowserFavicon(input["favicon"]); valid {
		out["favicon"], _ = json.Marshal(favicon)
	}
	for _, key := range []string{"context_ref", "viewed_tab"} {
		var value string
		if json.Unmarshal(input[key], &value) == nil && ((key == "context_ref" && validNativeContextRef(value)) || (key == "viewed_tab" && (value == "" || nativeTabID.MatchString(value)))) {
			out[key], _ = json.Marshal(value)
		}
	}
	var displayMode string
	if json.Unmarshal(input["display_mode"], &displayMode) == nil && displayMode == "desktop" {
		// This is a rendering capability, never a new execution backend. Old
		// workers omit it; unknown values must not select an unreviewed surface.
		out["display_mode"] = json.RawMessage(`"desktop"`)
	}
	for _, key := range []string{"debugger_paused", "devtools_blocked", "page_loading", "pick_pending", "can_go_back"} {
		if value := input[key]; string(value) == "true" || string(value) == "false" {
			out[key] = value
		}
	}
	var identity string
	if value := input["frame_identity"]; len(value) <= 256 && json.Unmarshal(value, &identity) == nil && validNativeFrameIdentity(identity) {
		out["frame_identity"], _ = json.Marshal(identity)
	}
	// These are product strings, not navigation exceptions. Never forward an
	// arbitrary bounded error: it could include request URLs or credentials.
	var loadError string
	if value := input["page_load_error"]; len(value) <= 256 && json.Unmarshal(value, &loadError) == nil &&
		(loadError == "Page navigation failed." || loadError == "Page navigation was cancelled.") {
		out["page_load_error"], _ = json.Marshal(loadError)
	}
	if activity := sanitizeNativeActivity(input["activity"]); activity != nil {
		out["activity"] = activity
	}
	if lease {
		if value, ok := input["lease_token"]; ok {
			out["lease_token"] = value
		}
	}
	return out, nil
}

func (b *nativeBrowser) State(ctx context.Context, target ...nativeBrowserTarget) (map[string]json.RawMessage, error) {
	if len(target) != 0 && !target[0].empty() {
		binding := target[0]
		if !binding.valid() {
			return nil, invalidNativeTarget()
		}
		// A saved binding must never create or recover a different context.
		raw, err := b.request(ctx, http.MethodGet, binding.suffix("/state"), nil)
		if err != nil {
			return nil, err
		}
		state, err := nativeBrowserState(raw, false)
		if err != nil {
			return nil, err
		}
		if !binding.matches(state) {
			return nil, errors.New("native browser returned a different page binding")
		}
		b.captureStateIcons(state)
		b.addDownloadState(state)
		return state, nil
	}
	if err := b.ensure(ctx); err != nil {
		return nil, err
	}
	raw, err := b.request(ctx, http.MethodGet, "/state", nil)
	var remote *nativeBrowserHTTPError
	missing := errors.As(err, &remote) && (remote.status == http.StatusNotFound || remote.status == http.StatusGone)
	b.mu.Lock()
	cleanupRequired := b.cleanupRequired
	b.mu.Unlock()
	if missing || cleanupRequired {
		// Only a read-only state request may recover an expired worker session.
		// Never replay a possibly completed click/input after losing the worker.
		if err := b.acquire(ctx); err != nil {
			return nil, err
		}
		defer b.release()
		// Another state reader may have restored the session while we queued.
		raw, err = b.request(ctx, http.MethodGet, "/state", nil)
		missing = errors.As(err, &remote) && (remote.status == http.StatusNotFound || remote.status == http.StatusGone)
		b.mu.Lock()
		if missing && remote.status == http.StatusGone {
			b.cleanupRequired = true
			b.blocked = "closed browser context cleanup is not confirmed; reconnect to retry cleanup before further browser actions"
		}
		cleanupRequired = b.cleanupRequired
		b.mu.Unlock()
		if missing || cleanupRequired {
			if cleanupRequired {
				// A terminal context may still have an outstanding close promise.
				// Keep this obligation even if a failed DELETE is followed by 404:
				// only an explicit cleanup ACK permits a replacement context.
				cleanupCtx, cancel := context.WithTimeout(ctx, nativeBrowserCancelTimeout)
				ackRaw, cleanupErr := b.request(cleanupCtx, http.MethodDelete, "", nil)
				cancel()
				var ack struct {
					OK bool `json:"ok"`
				}
				if cleanupErr == nil && (json.Unmarshal(ackRaw, &ack) != nil || !ack.OK) {
					cleanupErr = errors.New("closed browser context cleanup was not acknowledged")
				}
				if cleanupErr != nil {
					return nil, fmt.Errorf("browser session cannot be reset before cleanup: %w", cleanupErr)
				}
			}
			b.mu.Lock()
			b.cleanupRequired = false
			b.created = false
			b.blocked = ""
			b.mu.Unlock()
			if err := b.ensure(ctx); err != nil {
				return nil, err
			}
			raw, err = b.request(ctx, http.MethodGet, "/state", nil)
			if err != nil {
				return nil, err
			}
			state, err := nativeBrowserState(raw, false)
			if err != nil {
				return nil, err
			}
			state["reset"] = json.RawMessage("true")
			state["reset_reason"], _ = json.Marshal("浏览器会话已过期、关闭或服务已重启，已建立新会话；原页面和登录状态未恢复。")
			b.mu.Lock()
			b.blocked = ""
			b.mu.Unlock()
			b.captureStateIcons(state)
			b.addDownloadState(state)
			return state, nil
		}
	}
	if err != nil {
		return nil, err
	}
	state, err := nativeBrowserState(raw, false)
	if err == nil {
		b.captureStateIcons(state)
		b.addDownloadState(state)
	}
	return state, err
}

func (b *nativeBrowser) Frame(ctx context.Context, target ...nativeBrowserTarget) (nativeBrowserFrame, error) {
	var binding nativeBrowserTarget
	if len(target) != 0 {
		binding = target[0]
	}
	if !binding.empty() && !binding.valid() {
		return nativeBrowserFrame{}, invalidNativeTarget()
	}
	if binding.empty() {
		if err := b.ensure(ctx); err != nil {
			return nativeBrowserFrame{}, err
		}
	}
	raw, headers, err := b.requestWithHeaders(ctx, http.MethodGet, binding.suffix("/frame"), nil)
	if err != nil {
		return nativeBrowserFrame{}, err
	}
	if len(raw) < 3 || raw[0] != 0xff || raw[1] != 0xd8 || raw[2] != 0xff {
		return nativeBrowserFrame{}, errors.New("native browser returned an invalid JPEG frame")
	}
	identity := headers.Get("X-Browser-Frame-Identity")
	if !validNativeFrameIdentity(identity) {
		return nativeBrowserFrame{}, errors.New("native browser frame has no valid document identity")
	}
	contextRef, viewedTab := headers.Get("X-Browser-Context-Ref"), headers.Get("X-Browser-Viewed-Tab")
	if !binding.empty() && (contextRef != binding.ContextRef || viewedTab != binding.TabID || strings.Split(identity, ":")[1] != binding.TabID) {
		return nativeBrowserFrame{}, errors.New("native browser frame does not match the selected page")
	}
	if !validNativeContextRef(contextRef) {
		contextRef = ""
	}
	if !nativeTabID.MatchString(viewedTab) {
		viewedTab = ""
	}
	version := headers.Get("X-Browser-Version")
	return nativeBrowserFrame{Data: raw, Version: version, Identity: identity, Overlay: sanitizeNativeOverlay(headers.Get("X-Browser-Overlay"), version), ContextRef: contextRef, ViewedTab: viewedTab}, nil
}

func (b *nativeBrowser) Control(ctx context.Context, input nativeBrowserControl) (map[string]json.RawMessage, error) {
	if !validNativeBrowserControl(input) {
		return nil, invalidNativeTarget()
	}
	// Taking human control must reach the worker while a batch is running:
	// its fence drains the current atomic action and preserves the live page.
	// Waiting on the Go execution lock first would allow the entire batch to run.
	if input.Mode != "human" {
		if err := b.acquire(ctx); err != nil {
			return nil, err
		}
		defer b.release()
	}
	b.mu.Lock()
	cleanupRequired := b.cleanupRequired
	blocked := b.blocked
	b.mu.Unlock()
	if blocked != "" {
		return nil, errors.New(blocked)
	}
	if cleanupRequired {
		return nil, errors.New("closed browser context cleanup is not confirmed; reconnect before taking control")
	}
	raw, err := b.request(ctx, http.MethodPost, "/control", input)
	if err != nil {
		return nil, err
	}
	state, err := nativeBrowserState(raw, input.Mode == "human")
	if err != nil {
		return nil, err
	}
	if !(nativeBrowserTarget{ContextRef: input.ContextRef, TabID: input.TabID}).matches(state) {
		return nil, errors.New("native browser control response has a different page binding")
	}
	if input.Mode == "human" {
		var token string
		if json.Unmarshal(state["lease_token"], &token) != nil || token == "" {
			return nil, errors.New("native browser did not acknowledge the human control lease")
		}
		if input.AdoptIfUnowned {
			var mode string
			var generation, epoch int64
			if json.Unmarshal(state["control"], &mode) != nil || mode != "human" ||
				string(state["generation"]) == "null" || json.Unmarshal(state["generation"], &generation) != nil || generation != *input.Generation ||
				string(state["control_epoch"]) == "null" || json.Unmarshal(state["control_epoch"], &epoch) != nil || epoch != 1 ||
				len(token) > 512 || strings.IndexFunc(token, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
				return nil, errors.New("native browser unowned-page adoption reply has a different control binding")
			}
		}
	}
	return state, nil
}

func (b *nativeBrowser) Input(ctx context.Context, input nativeBrowserInput) (map[string]json.RawMessage, error) {
	if err := b.acquire(ctx); err != nil {
		return nil, err
	}
	defer b.release()
	if input.ContextRef == "" {
		if err := b.ensure(ctx); err != nil {
			return nil, err
		}
	}
	b.mu.Lock()
	blocked := b.blocked
	b.mu.Unlock()
	if blocked != "" {
		return nil, errors.New(blocked)
	}
	raw, err := b.request(ctx, http.MethodPost, "/input", input)
	if err != nil {
		return nil, b.failedOperation(err)
	}
	state, err := nativeBrowserState(raw, false)
	if err != nil {
		return nil, b.failedOperation(err)
	}
	if ctx.Err() != nil {
		return nil, b.failedOperation(ctx.Err())
	}
	return state, nil
}

func (b *nativeBrowser) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	attempted := b.attempted
	b.mu.Unlock()
	if b.downloads != nil {
		b.downloads.stop()
	}
	if !attempted {
		return
	}
	_ = b.cancelAndConfirm()
	ctx, cancel := context.WithTimeout(context.Background(), nativeBrowserCancelTimeout)
	defer cancel()
	_, _ = b.request(ctx, http.MethodDelete, "", nil)
}
