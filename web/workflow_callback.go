package web

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/workflow"
)

const workflowCallbackMaxFailures = 8

// Callback destinations are administrator configuration, never launch/request
// input. HTTP is only a loopback development convenience; metadata/link-local
// destinations and redirects are rejected even for authenticated callers.
func validateWorkflowCallbackURL(raw string, required bool) error {
	if raw == "" && !required {
		return nil
	}
	if len(raw) > 4096 || strings.ContainsAny(raw, "\r\n\t ") {
		return errors.New("回调地址无效")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Opaque != "" {
		return errors.New("回调地址须为完整 HTTPS 地址，不包含凭据、查询参数或片段")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && callbackLoopbackHost(u.Hostname())) {
		return errors.New("回调使用 HTTPS；本机开发可使用 loopback HTTP")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && callbackUnsafeIP(ip) {
		return errors.New("不允许该回调目标地址")
	}
	if u.Port() != "" {
		p, err := strconv.Atoi(u.Port())
		if err != nil || p < 1 || p > 65535 {
			return errors.New("回调端口无效")
		}
	}
	return nil
}
func callbackLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()
}
func callbackUnsafeIP(ip net.IP) bool {
	return ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}
func workflowCallbackSecret() string { return "wcs_" + rand.Text() + rand.Text() }

// Called before validation/save. The local private file retains this signing
// secret; HTTP only returns it once when it is first created or rotated.
func (s *Server) prepareWorkflowPublicationCallback(p *workflowPublication, old *workflowPublication) (string, error) {
	p.SigningSecret = ""
	if old != nil {
		p.SigningSecret = old.SigningSecret
	}
	if p.Callback == nil {
		return "", nil
	}
	p.Callback.URL = strings.TrimSpace(p.Callback.URL)
	if err := validateWorkflowCallbackURL(p.Callback.URL, p.Callback.Enabled); err != nil {
		return "", err
	}
	if p.Callback.Enabled && p.SigningSecret == "" {
		p.SigningSecret = workflowCallbackSecret()
		return p.SigningSecret, nil
	}
	return "", nil
}
func workflowPublicationCallbackTarget(p workflowPublication) *taskqueue.WorkflowCallbackTarget {
	if p.Callback == nil || !p.Callback.Enabled || p.Callback.URL == "" || p.SigningSecret == "" {
		return nil
	}
	return &taskqueue.WorkflowCallbackTarget{PublicationID: p.ID, URL: p.Callback.URL, SigningSecret: p.SigningSecret}
}

// The signature covers both message identity and the exact immutable payload.
// A recipient checks the fresh timestamp, verifies this MAC, and deduplicates ID.
func workflowCallbackSignature(secret, eventID, timestamp string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = io.WriteString(mac, eventID+"."+timestamp+".")
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

type workflowCallbackSchemeKey struct{}

type workflowCallbackTransport struct{ *http.Transport }

func (t workflowCallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := validateWorkflowCallbackURL(req.URL.String(), true); err != nil {
		return nil, err
	}
	ctx := context.WithValue(req.Context(), workflowCallbackSchemeKey{}, req.URL.Scheme)
	return t.Transport.RoundTrip(req.WithContext(ctx))
}

func newWorkflowCallbackHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{Proxy: nil, MaxIdleConns: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("callback host has no address")
		}
		for _, item := range ips {
			if callbackUnsafeIP(item.IP) || item.IP.IsLoopback() && !callbackLoopbackHost(host) || ctx.Value(workflowCallbackSchemeKey{}) == "http" && !item.IP.IsLoopback() {
				return nil, errors.New("unsafe callback resolved address")
			}
		}
		var last error
		for _, item := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(item.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		return nil, last
	}
	return &http.Client{Transport: workflowCallbackTransport{tr}, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type workflowCallbackRuntime struct {
	s      *Server
	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	done   chan struct{}
	now    func() time.Time
	client *http.Client
}

func newWorkflowCallbackRuntime(s *Server) *workflowCallbackRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	return &workflowCallbackRuntime{s: s, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), now: time.Now, client: newWorkflowCallbackHTTPClient()}
}
func (s *Server) startWorkflowCallbacks() {
	if s.workflowCallbacks != nil {
		return
	}
	r := newWorkflowCallbackRuntime(s)
	s.workflowCallbacks = r
	go r.loop()
}
func (s *Server) stopWorkflowCallbacks() {
	if r := s.workflowCallbacks; r != nil {
		r.cancel()
		<-r.done
		r.client.CloseIdleConnections()
	}
}
func (r *workflowCallbackRuntime) notify() {
	if r == nil {
		return
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}
func (r *workflowCallbackRuntime) loop() {
	defer close(r.done)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		r.pump()
		select {
		case <-r.ctx.Done():
			return
		case <-r.s.mgr.stop:
			return
		case <-tick.C:
		case <-r.wake:
		}
	}
}
func workflowCallbackRecordStatus(rec workflow.RunRecord) string {
	switch rec.Status {
	case "queued", workflow.RunCompleted, workflow.RunFailed, workflow.RunCancelled, "interrupted":
		return rec.Status
	}
	if rec.Status == workflow.RunWorking {
		waiting, running := false, false
		for _, n := range rec.Nodes {
			waiting = waiting || n.Status == workflow.NodeWaitingHuman
			running = running || n.Status == workflow.NodeRunning
		}
		if waiting && !running {
			return "waiting_human"
		}
		return "working"
	}
	return rec.Status
}
func (r *workflowCallbackRuntime) record(rec workflow.RunRecord) error {
	if r == nil || r.ctx.Err() != nil || rec.Origin == nil || rec.Origin.PublicationID == "" {
		return nil
	}
	select {
	case <-r.s.mgr.stop:
		return nil
	default:
	}
	db, err := r.s.mgr.taskRuns.database(false)
	if err != nil {
		p, ok := r.s.workflowEmbed.publications.get(rec.Origin.PublicationID)
		if !ok || p.Callback == nil {
			return nil
		}
		return err
	}
	if db == nil {
		return nil
	}
	meta, err := db.GetWorkflowRun(rec.ID)
	if errors.Is(err, taskqueue.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if meta.Callback == nil {
		return nil
	}
	at := r.now().UnixMilli()
	if rec.EndedAt != "" {
		if t, e := time.Parse(time.RFC3339Nano, rec.EndedAt); e == nil {
			at = t.UnixMilli()
		}
	}
	if err := db.RecordWorkflowCallbackState(rec.ID, workflowCallbackRecordStatus(rec), rec.StateRevision, at); err != nil {
		return err
	}
	return nil
}
func (r *workflowCallbackRuntime) reconcile(db *taskqueue.Store) error {
	subscriptions, err := db.ListWorkflowCallbackSubscriptions()
	if err != nil {
		return err
	}
	for _, sub := range subscriptions {
		if sub.LastStatus == workflow.RunCompleted || sub.LastStatus == workflow.RunFailed || sub.LastStatus == workflow.RunCancelled || sub.LastStatus == "interrupted" {
			continue
		}
		if rec, ok := r.s.mgr.wfruns.snapshot(sub.RunID); ok {
			if err := r.record(rec); err != nil {
				return err
			}
		}
	}
	return nil
}
func (r *workflowCallbackRuntime) pump() {
	if r.ctx.Err() != nil {
		return
	}
	db, err := r.s.mgr.taskRuns.database(false)
	if err != nil || db == nil {
		return
	}
	if err := r.reconcile(db); err != nil {
		return
	}
	deliveries, err := db.DueWorkflowCallbacks(r.now().UnixMilli(), 20)
	if err != nil {
		return
	}
	for _, d := range deliveries {
		if r.ctx.Err() != nil {
			return
		}
		p, exists := r.s.workflowEmbed.publications.get(d.PublicationID)
		if !exists || p.Callback == nil || !p.Callback.Enabled {
			_ = db.HoldWorkflowCallback(d.ID, "paused", "回调已停用", r.now().UnixMilli())
			continue
		}
		if p.Callback.URL != d.TargetURL || p.SigningSecret != d.SigningSecret {
			_ = db.HoldWorkflowCallback(d.ID, "blocked", "回调目标或签名密钥已改变，请确认使用当前配置重试", r.now().UnixMilli())
			continue
		}
		if err := validateWorkflowCallbackURL(d.TargetURL, true); err != nil {
			_ = db.HoldWorkflowCallback(d.ID, "blocked", "回调目标无效", r.now().UnixMilli())
			continue
		}
		at := r.now().UnixMilli()
		claim := rand.Text()
		claimed, err := db.ClaimWorkflowCallback(d.ID, claim, at, at+20000)
		if err != nil {
			continue
		}
		timestamp := strconv.FormatInt(r.now().Unix(), 10)
		req, err := http.NewRequestWithContext(r.ctx, http.MethodPost, claimed.TargetURL, bytes.NewReader(claimed.Payload))
		status, reason := 0, "回调请求未完成"
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Pogo-Event-Id", claimed.ID)
			req.Header.Set("X-Pogo-Timestamp", timestamp)
			req.Header.Set("X-Pogo-Signature", workflowCallbackSignature(claimed.SigningSecret, claimed.ID, timestamp, claimed.Payload))
			req.Header.Set("User-Agent", "Pogo-Workflow-Callback/1")
			response, e := r.client.Do(req)
			if e == nil {
				status = response.StatusCode
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
				_ = response.Body.Close()
				reason = ""
				if status < 200 || status >= 300 {
					reason = fmt.Sprintf("HTTP %d", status)
				}
			}
		}
		// If shutdown raced the acknowledgement, a replay is intentional: the
		// recipient's stable event ID protects its business-side write.
		next := r.now().Add(workflowCallbackBackoff(claimed.Failures)).UnixMilli()
		_ = db.FinishWorkflowCallback(claimed.ID, claim, status, reason, r.now().UnixMilli(), next, workflowCallbackMaxFailures)
	}
}
func workflowCallbackBackoff(failures int) time.Duration {
	if failures < 0 {
		failures = 0
	}
	if failures > 6 {
		failures = 6
	}
	return time.Duration(1<<failures) * time.Second
}

func (s *Server) registerWorkflowCallbackRoutes() {
	s.mux.HandleFunc("GET /api/manage/workflows/{wid}/publications/{pid}/deliveries", s.handleWorkflowCallbackDeliveries)
	s.mux.HandleFunc("POST /api/manage/workflows/{wid}/publications/{pid}/deliveries/{eid}/retry", s.handleWorkflowCallbackRetry)
	s.mux.HandleFunc("POST /api/manage/workflows/{wid}/publications/{pid}/rotate-callback-secret", s.handleWorkflowCallbackRotate)
}
func (s *Server) handleWorkflowCallbackDeliveries(w http.ResponseWriter, r *http.Request) {
	p, ok := s.publicationForManagement(w, r)
	if !ok {
		return
	}
	db, err := s.mgr.taskRuns.database(false)
	if err != nil {
		writeErr(w, 503, errors.New("回调记录暂时不可用"))
		return
	}
	out := []taskqueue.WorkflowCallbackDelivery{}
	if db != nil {
		out, err = db.ListWorkflowCallbackDeliveries(p.ID, 200)
		if err != nil {
			writeErr(w, 503, errors.New("回调记录暂时不可用"))
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"deliveries": out})
}
func (s *Server) handleWorkflowCallbackRetry(w http.ResponseWriter, r *http.Request) {
	p, ok := s.publicationForManagement(w, r)
	if !ok {
		return
	}
	target := workflowPublicationCallbackTarget(p)
	if target == nil {
		writeErr(w, 409, errors.New("请先启用并配置回调"))
		return
	}
	var body struct {
		UseCurrentTarget bool `json:"use_current_target"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	db, err := s.mgr.taskRuns.database(false)
	if err != nil || db == nil {
		writeErr(w, 503, errors.New("回调记录暂时不可用"))
		return
	}
	if err = db.RetryWorkflowCallback(r.PathValue("eid"), p.ID, *target, body.UseCurrentTarget, time.Now().UnixMilli()); err != nil {
		status := 409
		if errors.Is(err, taskqueue.ErrNotFound) {
			status = 404
		}
		writeErr(w, status, err)
		return
	}
	if s.workflowCallbacks != nil {
		s.workflowCallbacks.notify()
	}
	writeJSON(w, 200, map[string]bool{"queued": true})
}
func (s *Server) handleWorkflowCallbackRotate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.publicationForManagement(w, r)
	if !ok {
		return
	}
	if p.Callback == nil {
		writeErr(w, 409, errors.New("请先配置回调"))
		return
	}
	p.SigningSecret = workflowCallbackSecret()
	if err := s.workflowEmbed.publications.save(p, p.Revision); err != nil {
		writeErr(w, 409, err)
		return
	}
	p.Revision++
	if s.workflowCallbacks != nil {
		s.workflowCallbacks.notify()
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"publication": p, "callback_secret": p.SigningSecret})
}
