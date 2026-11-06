package webgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/webservice"
)

type adminRequest struct {
	Action   string            `json:"action"`
	ClientID string            `json:"client_id"`
	Record   webservice.Record `json:"record"`
	GrantID  string            `json:"grant_id,omitempty"`
}
type adminResponse struct {
	Result json.RawMessage   `json:"result,omitempty"`
	Error  *webservice.Error `json:"error,omitempty"`
}

// AdminServer is reachable only through a private Unix socket. It must never be
// mounted on a public HTTP router or into an Agent execution environment.
type AdminServer struct {
	listener   net.Listener
	server     *http.Server
	router     *Router
	path       string
	mu         sync.Mutex
	controller string
	closed     bool
	done       chan struct{}
	limit      chan struct{}
}

func privateSocketParent(path string) error {
	if err := socketManagementSupported(); err != nil {
		return err
	}
	if !filepath.IsAbs(path) {
		return errors.New("gateway: admin socket path must be absolute")
	}
	parent := filepath.Dir(filepath.Clean(path))
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(parent, string(filepath.Separator)), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("gateway: admin socket directory must not traverse symlinks")
		}
	}
	info, err := os.Stat(parent)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("gateway: admin socket directory must be private (0700)")
	}
	if err := socketOwnedByProcess(info); err != nil {
		return err
	}
	return nil
}

func ListenAdmin(path string, router *Router) (*AdminServer, error) {
	if router == nil {
		return nil, errors.New("gateway: router is required")
	}
	if err := privateSocketParent(path); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("gateway: refusing to replace a non-socket admin file")
		}
		if err := socketOwnedByProcess(info); err != nil {
			return nil, err
		}
		c, err := net.DialTimeout("unix", path, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return nil, errors.New("gateway: admin socket is already active")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	s := &AdminServer{listener: listener, router: router, path: path, done: make(chan struct{}), limit: make(chan struct{}, 32)}
	s.server = &http.Server{Handler: http.HandlerFunc(s.serveHTTP), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() { defer close(s.done); _ = s.server.Serve(listener) }()
	return s, nil
}

func (s *AdminServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/control" {
		http.NotFound(w, r)
		return
	}
	select {
	case s.limit <- struct{}{}:
		defer func() { <-s.limit }()
	default:
		http.Error(w, "control capacity reached", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request adminRequest
	if err := decoder.Decode(&request); err != nil || request.ClientID == "" || len(request.ClientID) > 128 {
		adminReply(w, nil, webservice.Fail("INVALID_REQUEST", "invalid control request"))
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		adminReply(w, nil, webservice.Fail("INVALID_REQUEST", "one control request is required"))
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		adminReply(w, nil, webservice.Fail("GATEWAY_UNAVAILABLE", "management server is closed"))
		return
	}
	if request.Action == "preflight" {
		if s.controller != request.ClientID {
			s.router.replaceController()
		}
		s.controller = request.ClientID
	}
	allowed := s.controller == request.ClientID
	if !allowed {
		s.mu.Unlock()
		adminReply(w, nil, webservice.Fail("CONTROL_SUPERSEDED", "management session changed; preflight is required"))
		return
	}
	ctx := r.Context()
	// Serialize authority transitions: a late heartbeat must not resume the
	// management plane after a concurrently accepted close.
	if request.Action == "preflight" || request.Action == "heartbeat" {
		err := s.router.Preflight(ctx)
		s.mu.Unlock()
		adminReply(w, nil, err)
		return
	}
	if request.Action == "close" {
		s.controller = ""
		s.router.suspendControl()
		s.mu.Unlock()
		adminReply(w, nil, nil)
		return
	}
	// Keep the authority lock through each in-memory route/grant mutation.
	// Otherwise an old controller can pass the check, pause, and publish after
	// a new controller has already taken over. Probe's network I/O is separate.
	switch request.Action {
	case "apply":
		err := s.router.Apply(ctx, request.Record)
		s.mu.Unlock()
		adminReply(w, nil, err)
	case "revoke":
		err := s.router.Revoke(ctx, request.Record)
		s.mu.Unlock()
		adminReply(w, nil, err)
	case "probe":
		s.mu.Unlock()
		result, err := s.router.Probe(ctx, request.Record)
		s.mu.Lock()
		if s.closed || s.controller != request.ClientID {
			err = webservice.Fail("CONTROL_SUPERSEDED", "management session changed during probe")
		}
		s.mu.Unlock()
		adminReply(w, result, err)
	case "open":
		result, err := s.router.Open(ctx, request.Record)
		s.mu.Unlock()
		adminReply(w, result, err)
	case "close_grant":
		err := s.router.CloseGrant(ctx, request.Record, request.GrantID)
		s.mu.Unlock()
		adminReply(w, nil, err)
	default:
		s.mu.Unlock()
		adminReply(w, nil, webservice.Fail("INVALID_REQUEST", "unknown control action"))
	}
}

func adminReply(w http.ResponseWriter, value any, err error) {
	w.Header().Set("Content-Type", "application/json")
	response := adminResponse{}
	if err != nil {
		var typed *webservice.Error
		if errors.As(err, &typed) {
			response.Error = typed
		} else {
			response.Error = &webservice.Error{Code: "GATEWAY_UNAVAILABLE", Message: "gateway operation failed", Retryable: true}
		}
	} else if value != nil {
		response.Result, _ = json.Marshal(value)
	}
	_ = json.NewEncoder(w).Encode(response)
}

func (s *AdminServer) Close() error {
	s.mu.Lock()
	s.closed = true
	s.controller = ""
	s.router.suspendControl()
	s.mu.Unlock()
	err := s.server.Close()
	<-s.done
	return err
}

// AdminClient is owned by the application host, never by an individual Session.
// Preflight starts heartbeats; Close stops management authority without stopping
// runtime containers. Dynamic public requests then fail closed.
type AdminClient struct {
	client  *http.Client
	id      string
	initErr error
	mu      sync.Mutex
	started bool
	closed  bool
	done    chan struct{}
	wg      sync.WaitGroup
}

var _ webservice.Gateway = (*AdminClient)(nil)

// LifecycleURLs advertises lease-scoped stable addresses on this control API.
func (*AdminClient) LifecycleURLs() bool { return true }

func NewAdminClient(socket string) *AdminClient {
	id, err := randomHex(16)
	if err == nil {
		err = socketManagementSupported()
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
	}, MaxIdleConns: 4, MaxIdleConnsPerHost: 4, IdleConnTimeout: 15 * time.Second}
	return &AdminClient{client: &http.Client{Transport: transport, Timeout: 10 * time.Second}, id: id, initErr: err, done: make(chan struct{})}
}

func (c *AdminClient) raw(ctx context.Context, action string, record webservice.Record, grantID string, result any) error {
	if c.initErr != nil {
		return c.initErr
	}
	request := adminRequest{Action: action, ClientID: c.id, Record: record, GrantID: grantID}
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/control", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return webservice.Fail("GATEWAY_UNAVAILABLE", "private gateway connection unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return webservice.Fail("GATEWAY_UNAVAILABLE", "private gateway rejected the control request")
	}
	var response adminResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&response); err != nil {
		return webservice.Fail("GATEWAY_UNAVAILABLE", "invalid private gateway response")
	}
	if response.Error != nil {
		return response.Error
	}
	if result != nil {
		if len(response.Result) == 0 {
			return webservice.Fail("GATEWAY_UNAVAILABLE", "missing gateway result")
		}
		return json.Unmarshal(response.Result, result)
	}
	return nil
}

func (c *AdminClient) Preflight(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return webservice.Fail("GATEWAY_UNAVAILABLE", "gateway client is closed")
	}
	if err := c.raw(ctx, "preflight", webservice.Record{}, "", nil); err != nil {
		return err
	}
	if !c.started {
		c.started = true
		c.wg.Add(1)
		go c.heartbeat()
	}
	return nil
}

func (c *AdminClient) ensure(ctx context.Context) error {
	c.mu.Lock()
	started, closed := c.started, c.closed
	c.mu.Unlock()
	if closed {
		return webservice.Fail("GATEWAY_UNAVAILABLE", "gateway client is closed")
	}
	if started {
		return nil
	}
	return c.Preflight(ctx)
}

func (c *AdminClient) call(ctx context.Context, action string, record webservice.Record, grant string, result any) error {
	if err := c.ensure(ctx); err != nil {
		return err
	}
	return c.raw(ctx, action, record, grant, result)
}

func (c *AdminClient) heartbeat() {
	defer c.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = c.raw(ctx, "heartbeat", webservice.Record{}, "", nil)
			cancel()
		}
	}
}
func (c *AdminClient) Apply(ctx context.Context, r webservice.Record) error {
	return c.call(ctx, "apply", r, "", nil)
}
func (c *AdminClient) Revoke(ctx context.Context, r webservice.Record) error {
	return c.call(ctx, "revoke", r, "", nil)
}
func (c *AdminClient) Probe(ctx context.Context, r webservice.Record) (webservice.Health, error) {
	var h webservice.Health
	err := c.call(ctx, "probe", r, "", &h)
	return h, err
}
func (c *AdminClient) Open(ctx context.Context, r webservice.Record) (webservice.Grant, error) {
	var g webservice.Grant
	err := c.call(ctx, "open", r, "", &g)
	return g, err
}
func (c *AdminClient) CloseGrant(ctx context.Context, r webservice.Record, id string) error {
	return c.call(ctx, "close_grant", r, id, nil)
}
func (c *AdminClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.done)
	started := c.started
	c.mu.Unlock()
	c.wg.Wait()
	if started {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = c.raw(ctx, "close", webservice.Record{}, "", nil)
		cancel()
	}
	c.client.CloseIdleConnections()
	return nil
}
