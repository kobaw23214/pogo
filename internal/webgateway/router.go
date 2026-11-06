package webgateway

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/webservice"
)

// RouterOptions is trusted administrator configuration. ProbeClient may supply
// private DNS routing, but must still validate the configured public TLS name.
type RouterOptions struct {
	MainUpstream          string
	MainHosts             []string
	PublicOrigin          string
	PreviewDomain         string
	PreviewMode           string
	ProbeClient           *http.Client
	AllowInsecureHTTP     bool
	AllowLoopbackUpstream bool
	ControlTimeout        time.Duration
	BrowserURLVerified    bool
	// StatePath persists the private signing key and explicit access rotations.
	// Empty is intended for isolated in-memory adapters/tests only.
	StatePath string
}

type route struct {
	record    webservice.Record
	upstream  *url.URL
	revoked   bool
	confirmed bool
}

type accessGrant struct {
	id         string
	service    string
	owner      string
	session    string
	generation int
	expires    time.Time
	probePath  string
	probeQuery string
	active     map[*activeRequest]struct{}
}

type activeRequest struct{ cancel context.CancelFunc }

type openOperation struct {
	service    string
	generation int
	grantHash  [32]byte
}

// Router is a closed registry proxy. Only records received from the private
// management plane can become upstreams; public requests cannot choose one.
type Router struct {
	opts           RouterOptions
	origin         *url.URL
	domain         string
	port           string
	mainHosts      map[string]bool
	parentOrigins  []string
	mainProxy      *httputil.ReverseProxy
	mainTransport  *http.Transport
	appTransport   *http.Transport
	mu             sync.Mutex
	routes         map[string]*route
	grants         map[[32]byte]*accessGrant
	grantIDs       map[string][32]byte
	openOperations map[[32]byte]openOperation
	grantSecret    [32]byte
	leaseAccess    map[string]leaseAccessVersion
	lastControl    time.Time
	suspended      bool
	closed         bool
	done           chan struct{}
	wg             sync.WaitGroup
}

var _ webservice.Gateway = (*Router)(nil)

// LifecycleURLs advertises the durable lease URL protocol to Manager adapters.
func (*Router) LifecycleURLs() bool { return true }

const previewPathPrefix = "/_pogo/web/"

func NewRouter(opts RouterOptions) (*Router, error) {
	if opts.PreviewMode == "" {
		opts.PreviewMode = "subdomain"
	}
	if opts.PreviewMode != "subdomain" && opts.PreviewMode != "path" {
		return nil, errors.New("gateway: preview mode must be subdomain or path")
	}
	origin, err := url.Parse(opts.PublicOrigin)
	if err != nil || origin.Hostname() == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") || (origin.Scheme != "https" && !(origin.Scheme == "http" && opts.AllowInsecureHTTP)) {
		return nil, webservice.Fail("PREVIEW_ORIGIN_UNCONFIGURED", "a public HTTPS origin is required; plaintext HTTP requires explicit permission")
	}
	if opts.PreviewMode == "path" && origin.Scheme == "http" && !privateHTTPHost(origin.Hostname()) {
		return nil, webservice.Fail("PREVIEW_ORIGIN_UNCONFIGURED", "HTTP path previews require a private, loopback or link-local IP, or localhost")
	}
	origin.Path = ""
	domain := strings.ToLower(strings.TrimSuffix(opts.PreviewDomain, "."))
	if opts.PreviewMode == "subdomain" && (!validDNSName(domain) || len(domain) > 180 || net.ParseIP(domain) != nil) {
		return nil, webservice.Fail("PREVIEW_ORIGIN_UNCONFIGURED", "a bounded preview DNS domain is required")
	}
	upstream, err := url.Parse(opts.MainUpstream)
	if err != nil || upstream.Host == "" || upstream.User != nil || upstream.RawQuery != "" || upstream.Fragment != "" || (upstream.Scheme != "http" && upstream.Scheme != "https") || (upstream.Path != "" && upstream.Path != "/") {
		return nil, errors.New("gateway: a fixed main upstream origin is required")
	}
	port := origin.Port()
	if port == "" {
		if origin.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return nil, errors.New("gateway: invalid public port")
	}
	if opts.ControlTimeout < 0 {
		return nil, errors.New("gateway: invalid control timeout")
	}
	if opts.ControlTimeout == 0 {
		opts.ControlTimeout = 15 * time.Second
	}
	r := &Router{opts: opts, origin: origin, domain: domain, port: port, mainHosts: map[string]bool{}, routes: map[string]*route{}, grants: map[[32]byte]*accessGrant{}, grantIDs: map[string][32]byte{}, lastControl: time.Now(), done: make(chan struct{})}
	r.openOperations = make(map[[32]byte]openOperation)
	if err := r.loadAccessState(); err != nil {
		return nil, err
	}
	if len(opts.MainHosts) == 0 {
		return nil, errors.New("gateway: explicit main host allowlist is required")
	}
	if len(opts.MainHosts) > 256 {
		return nil, errors.New("gateway: main host allowlist is limited to 256 entries")
	}
	parentOrigins := map[string]bool{}
	for _, host := range opts.MainHosts {
		a, name, err := r.authority(strings.TrimSpace(host))
		if err != nil {
			return nil, errors.New("gateway: invalid main host allowlist")
		}
		if opts.PreviewMode == "subdomain" && (name == domain || strings.HasSuffix(name, "."+domain)) {
			return nil, errors.New("gateway: main and preview hosts must be disjoint")
		}
		r.mainHosts[a] = true
		parentOrigins[normalizedBrowserOrigin(origin.Scheme, strings.TrimSpace(host), port)] = true
	}
	canonical, _, err := r.authority(origin.Host)
	if err != nil || !r.mainHosts[canonical] {
		return nil, errors.New("gateway: canonical public origin must be a main host")
	}
	// Keep the published URL and the capability metadata in URL.origin form.
	// MainHosts remains the exact authority boundary for incoming requests.
	canonicalOrigin, _ := url.Parse(normalizedBrowserOrigin(origin.Scheme, origin.Host, port))
	r.origin = canonicalOrigin
	parentOrigins[r.origin.String()] = true
	if len(parentOrigins) > 256 {
		return nil, errors.New("gateway: canonical and main origins are limited to 256 entries")
	}
	for parent := range parentOrigins {
		r.parentOrigins = append(r.parentOrigins, parent)
	}
	sort.Strings(r.parentOrigins)
	r.mainProxy = httputil.NewSingleHostReverseProxy(upstream)
	// Main API calls such as compaction may wait for a complete model result
	// before sending headers. Let the request context and backend govern their
	// lifetime, rather than imposing another response deadline at the gateway.
	r.mainTransport = &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext, MaxIdleConns: 64, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second}
	r.appTransport = r.mainTransport.Clone()
	r.appTransport.ResponseHeaderTimeout = 10 * time.Second
	r.mainProxy.Transport = r.mainTransport
	director := r.mainProxy.Director
	r.mainProxy.Director = func(req *http.Request) {
		publicHost := req.Host
		proto := "http"
		if req.TLS != nil {
			proto = "https"
		}
		stripForwarded(req.Header)
		director(req)
		req.Host = publicHost
		req.Header.Set("X-Forwarded-Host", publicHost)
		req.Header.Set("X-Forwarded-Proto", proto)
	}
	r.mainProxy.ErrorLog = log.New(io.Discard, "", 0)
	r.mainProxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "main service unavailable", http.StatusBadGateway)
	}
	r.wg.Add(1)
	go r.expireLoop()
	return r, nil
}

func privateHTTPHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" || ip.IsMulticast() {
		return false
	}
	ip = ip.Unmap()
	return !ip.IsUnspecified() && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}

func validDNSName(name string) bool {
	if len(name) < 1 || len(name) > 253 || strings.ContainsAny(name, "/:@?#\\") {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func (r *Router) authority(raw string) (string, string, error) {
	if raw == "" || strings.ContainsAny(raw, "/@?#\\\r\n\t ") {
		return "", "", errors.New("invalid authority")
	}
	u, err := url.Parse("//" + raw)
	if err != nil || u.User != nil || u.Hostname() == "" {
		return "", "", errors.New("invalid authority")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if !validDNSName(host) && net.ParseIP(host) == nil {
		return "", "", errors.New("invalid host")
	}
	host = normalizedBrowserHost(host)
	port := u.Port()
	if port == "" {
		port = r.port
	}
	if port != r.port {
		return "", "", errors.New("unexpected port")
	}
	return net.JoinHostPort(host, port), host, nil
}

// These helpers only receive authorities already accepted by authority().
// Browsers omit default ports, lowercase DNS names and serialize IPv6 hosts.
func normalizedBrowserHost(host string) string {
	host = strings.ToLower(host)
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Is4In6() {
			bytes := addr.As16()
			return "::ffff:" + strconv.FormatUint(uint64(bytes[12])<<8|uint64(bytes[13]), 16) + ":" + strconv.FormatUint(uint64(bytes[14])<<8|uint64(bytes[15]), 16)
		}
		return addr.String()
	}
	return host
}

func normalizedBrowserOrigin(scheme, authority, fallbackPort string) string {
	u, _ := url.Parse("//" + authority)
	host := normalizedBrowserHost(u.Hostname())
	port := u.Port()
	if port == "" {
		port = fallbackPort
	}
	if scheme == "http" && port == "80" || scheme == "https" && port == "443" {
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		return scheme + "://" + host
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

func stripForwarded(h http.Header) {
	for key := range h {
		lower := strings.ToLower(key)
		if lower == "forwarded" || lower == "x-real-ip" || strings.HasPrefix(lower, "x-forwarded-") {
			h.Del(key)
		}
	}
}

func routeIdentity(a, b webservice.Record) bool {
	return a.Owner == b.Owner && a.Session == b.Session && a.Installation == b.Installation && a.ServiceID == b.ServiceID
}

func (r *Router) checkedUpstream(rec webservice.Record) (*url.URL, error) {
	if rec.ServiceID == "" || len(rec.ServiceID) > 128 || rec.Owner == "" || rec.Session == "" || rec.Installation == "" || rec.Generation < 1 || rec.Revision < 1 || rec.Instance.ID == "" || rec.Instance.NetworkID == "" {
		return nil, webservice.Fail("INVALID_ROUTE", "managed identity and runtime are required")
	}
	host, port, err := net.SplitHostPort(rec.Instance.Upstream)
	if err != nil {
		return nil, webservice.Fail("INVALID_ROUTE", "upstream must be a private runtime IP and port")
	}
	ip, err := netip.ParseAddr(host)
	expectedPort := rec.Runtime.InternalPort
	if expectedPort == 0 {
		expectedPort = 8080
	}
	if err != nil || (!ip.IsPrivate() && !(r.opts.AllowLoopbackUpstream && ip.IsLoopback())) || (port != strconv.Itoa(expectedPort) && !r.opts.AllowLoopbackUpstream) {
		return nil, webservice.Fail("INVALID_ROUTE", "upstream must be a private runtime on its configured application port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, webservice.Fail("INVALID_ROUTE", "invalid runtime port")
	}
	return &url.URL{Scheme: "http", Host: net.JoinHostPort(ip.String(), port)}, nil
}

func (r *Router) Preflight(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return webservice.Fail("GATEWAY_UNAVAILABLE", "gateway is closed")
	}
	r.lastControl = time.Now()
	r.suspended = false
	return nil
}

func (r *Router) Apply(ctx context.Context, rec webservice.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	upstream, err := r.checkedUpstream(rec)
	if err != nil {
		return err
	}
	if rec.LeaseExpiresAt.IsZero() || !time.Now().Before(rec.LeaseExpiresAt) {
		return webservice.Fail("LEASE_EXPIRED", "service lease expired")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return webservice.Fail("GATEWAY_UNAVAILABLE", "gateway is closed")
	}
	if old := r.routes[rec.ServiceID]; old != nil {
		if !routeIdentity(old.record, rec) {
			return webservice.Fail("PERMISSION_DENIED", "route identity does not match")
		}
		if rec.Generation < old.record.Generation || rec.Revision < old.record.Revision {
			return webservice.Fail("STALE_ROUTE", "stale generation or revision")
		}
		if rec.Generation > old.record.Generation && rec.Revision <= old.record.Revision {
			return webservice.Fail("STALE_ROUTE", "new generations require a newer revision")
		}
		if old.revoked && rec.Generation <= old.record.Generation {
			return webservice.Fail("STALE_ROUTE", "a revoked generation cannot be reactivated")
		}
		if rec.Generation == old.record.Generation && rec.Revision == old.record.Revision {
			if old.revoked || old.record.State != rec.State || old.record.Instance != rec.Instance || !old.record.LeaseExpiresAt.Equal(rec.LeaseExpiresAt) || old.record.SpecDigest != rec.SpecDigest || old.record.LeaseID != rec.LeaseID {
				return webservice.Fail("STALE_ROUTE", "conflicting route revision")
			}
			// A replacement controller must explicitly republish its checked
			// snapshot, even if the durable route revision did not change.
			old.confirmed = true
			if rec.LeaseID != "" {
				return r.installLeaseAccessLocked(rec)
			}
			return nil
		}
		if rec.Generation != old.record.Generation || terminalRoute(rec.State) {
			r.revokeServiceLocked(rec.ServiceID, 0)
		} else if rec.State != "ready" {
			for _, g := range r.grants {
				if g.service == rec.ServiceID {
					for active := range g.active {
						active.cancel()
					}
				}
			}
		}
	}
	r.routes[rec.ServiceID] = &route{record: rec, upstream: upstream, revoked: terminalRoute(rec.State), confirmed: true}
	for _, grant := range r.grants {
		if grant.service == rec.ServiceID && grant.generation == rec.Generation && grant.probePath == "" {
			grant.expires = rec.LeaseExpiresAt
		}
	}
	r.lastControl = time.Now()
	if rec.LeaseID != "" && !terminalRoute(rec.State) {
		return r.installLeaseAccessLocked(rec)
	}
	return nil
}

func (r *Router) Revoke(ctx context.Context, rec webservice.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.routes[rec.ServiceID]
	if old == nil {
		return nil
	}
	if !routeIdentity(old.record, rec) {
		return webservice.Fail("PERMISSION_DENIED", "route identity does not match")
	}
	if rec.Generation < old.record.Generation || rec.Revision < old.record.Revision {
		return webservice.Fail("STALE_ROUTE", "stale route revocation")
	}
	r.revokeServiceLocked(rec.ServiceID, 0)
	old.record = rec
	old.revoked = true
	r.lastControl = time.Now()
	return nil
}

func randomHex(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func terminalRoute(state string) bool {
	return state == "stopping" || state == "stopped" || state == "expired" || state == "failed" || state == "interrupted"
}

func operationKey(rec webservice.Record) [32]byte {
	data, _ := json.Marshal([]string{rec.Owner, rec.Session, rec.OperationID})
	return sha256.Sum256(data)
}
func (r *Router) operationToken(key [32]byte) string {
	h := hmac.New(sha256.New, r.grantSecret[:])
	_, _ = h.Write([]byte("preview-token\x00"))
	_, _ = h.Write(key[:])
	return hex.EncodeToString(h.Sum(nil)[:24])
}

func (r *Router) grantView(rec webservice.Record, g *accessGrant, token string) webservice.Grant {
	u := *r.origin
	if r.opts.PreviewMode == "path" {
		u.Path = previewPathPrefix + token + "/"
	} else {
		u.Host = net.JoinHostPort(token+"."+r.domain, r.port)
		u.Path = "/"
	}
	preview := u.String()
	grant := webservice.Grant{ServiceID: g.service, Generation: g.generation, GrantID: g.id, PreviewURL: preview, ExpiresAt: g.expires.UTC(), LeaseExpiresAt: rec.LeaseExpiresAt.UTC(), Limitations: []string{"bearer URL valid for the website lifetime", "sandboxed opaque origin", "no app login, cookies, localStorage, workers or external APIs", "images must be local project assets; external image URLs are blocked"}}
	grant.CanonicalOrigin = r.origin.String()
	grant.AllowedParentOrigins = append([]string(nil), r.parentOrigins...)
	if r.opts.PreviewMode == "path" {
		grant.Limitations = append(grant.Limitations, "path preview: use relative resource and API URLs within this grant; root-absolute URLs target the main site and are blocked")
	}
	if r.opts.BrowserURLVerified {
		grant.BrowserURL = &preview
	} else {
		grant.Limitations = append(grant.Limitations, "NATIVE_BROWSER_UNREACHABLE: built-in browser route has not been verified")
	}
	return grant
}

func (r *Router) issueLocked(rec webservice.Record, probePath, probeQuery string) (webservice.Grant, [32]byte, error) {
	var zero [32]byte
	now := time.Now()
	r.expireLocked(now)
	if !r.controlAliveLocked(now) {
		return webservice.Grant{}, zero, webservice.Fail("GATEWAY_UNAVAILABLE", "management connection unavailable")
	}
	current := r.routes[rec.ServiceID]
	if current == nil || current.revoked || !routeIdentity(current.record, rec) || current.record.Generation != rec.Generation || current.record.Revision != rec.Revision {
		return webservice.Grant{}, zero, webservice.Fail("STALE_ROUTE", "route changed")
	}
	if !current.confirmed {
		return webservice.Grant{}, zero, webservice.Fail("SERVICE_NOT_READY", "current manager has not republished this route")
	}
	if probePath == "" && current.record.State != "ready" {
		return webservice.Grant{}, zero, webservice.Fail("SERVICE_NOT_READY", "service is not ready")
	}
	if !now.Before(current.record.LeaseExpiresAt) {
		return webservice.Grant{}, zero, webservice.Fail("LEASE_EXPIRED", "service lease expired")
	}
	if probePath == "" && rec.LeaseID != "" {
		if strings.TrimSpace(rec.OperationID) == "" || len(rec.OperationID) > 128 {
			return webservice.Grant{}, zero, webservice.Fail("INVALID_OPERATION_ID", "open requires a bounded operation_id")
		}
		if err := r.installLeaseAccessLocked(current.record); err != nil {
			return webservice.Grant{}, zero, err
		}
		token, _ := r.leaseToken(current.record)
		key := sha256.Sum256([]byte(token))
		return r.grantView(current.record, r.grants[key], token), key, nil
	}
	if probePath == "" {
		if strings.TrimSpace(rec.OperationID) == "" || len(rec.OperationID) > 128 {
			return webservice.Grant{}, zero, webservice.Fail("INVALID_OPERATION_ID", "open requires a bounded operation_id")
		}
		opKey := operationKey(rec)
		if previous, ok := r.openOperations[opKey]; ok {
			if previous.service != rec.ServiceID {
				return webservice.Grant{}, zero, webservice.Fail("IDEMPOTENCY_CONFLICT", "open operation belongs to another service")
			}
			g := r.grants[previous.grantHash]
			if g == nil || previous.generation != rec.Generation {
				return webservice.Grant{}, zero, webservice.Fail("GRANT_EXPIRED", "the original open grant expired or was revoked")
			}
			return r.grantView(current.record, g, r.operationToken(opKey)), previous.grantHash, nil
		}
		if len(r.openOperations) >= 8192 {
			return webservice.Grant{}, zero, webservice.Fail("GRANT_QUOTA_EXCEEDED", "open operation history capacity reached")
		}
		serviceCount, ownerCount, total := 0, 0, 0
		for _, g := range r.grants {
			if g.probePath != "" {
				continue
			}
			total++
			if g.service == rec.ServiceID {
				serviceCount++
			}
			if g.owner == rec.Owner {
				ownerCount++
			}
		}
		if serviceCount >= 4 || ownerCount >= 16 || total >= 128 {
			return webservice.Grant{}, zero, webservice.Fail("GRANT_QUOTA_EXCEEDED", "active preview limit reached")
		}
	}
	token, err := randomHex(24)
	if err != nil {
		return webservice.Grant{}, zero, err
	}
	if probePath == "" {
		token = r.operationToken(operationKey(rec))
	}
	id, err := randomHex(16)
	if err != nil {
		return webservice.Grant{}, zero, err
	}
	expires := current.record.LeaseExpiresAt
	if probePath != "" && now.Add(10*time.Second).Before(expires) {
		expires = now.Add(10 * time.Second)
	}
	key := sha256.Sum256([]byte(token))
	r.grants[key] = &accessGrant{id: id, service: rec.ServiceID, owner: rec.Owner, session: rec.Session, generation: rec.Generation, expires: expires, probePath: probePath, probeQuery: probeQuery, active: map[*activeRequest]struct{}{}}
	r.grantIDs[id] = key
	if probePath == "" {
		r.openOperations[operationKey(rec)] = openOperation{service: rec.ServiceID, generation: rec.Generation, grantHash: key}
	}
	grant := r.grantView(current.record, r.grants[key], token)
	return grant, key, nil
}

func (r *Router) Open(ctx context.Context, rec webservice.Record) (webservice.Grant, error) {
	if err := ctx.Err(); err != nil {
		return webservice.Grant{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	grant, _, err := r.issueLocked(rec, "", "")
	return grant, err
}

func (r *Router) CloseGrant(ctx context.Context, rec webservice.Record, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key, ok := r.grantIDs[id]
	if !ok {
		return nil
	}
	g := r.grants[key]
	if g == nil {
		return nil
	}
	if g.service != rec.ServiceID || g.owner != rec.Owner || g.session != rec.Session || g.generation != rec.Generation {
		return webservice.Fail("PERMISSION_DENIED", "grant identity does not match")
	}
	if rec.LeaseID != "" && g.probePath == "" {
		return r.rotateLeaseAccessLocked(rec)
	}
	r.revokeGrantLocked(key)
	return nil
}

func validHealthPath(raw string) (*url.URL, error) {
	if raw == "" {
		raw = "/"
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\r\n\\") {
		return nil, webservice.Fail("INVALID_HEALTH_PATH", "health path must remain inside the application")
	}
	return u, nil
}

func (r *Router) Probe(ctx context.Context, rec webservice.Record) (webservice.Health, error) {
	h := webservice.Health{Generation: rec.Generation, CheckedAt: time.Now().UTC(), RelativePath: rec.Spec.HealthPath, Result: "failed"}
	path, err := validHealthPath(rec.Spec.HealthPath)
	if err != nil {
		return h, err
	}
	if r.opts.PreviewMode == "path" && !validPreviewPath(path) {
		return h, webservice.Fail("INVALID_HEALTH_PATH", "health path must use an unambiguous application path")
	}
	h.RelativePath = path.RequestURI()
	r.mu.Lock()
	current := r.routes[rec.ServiceID]
	if current == nil || !routeIdentity(current.record, rec) || current.record.Generation != rec.Generation {
		r.mu.Unlock()
		return h, webservice.Fail("STALE_ROUTE", "route changed")
	}
	upstream := *current.upstream
	grant, key, err := r.issueLocked(rec, path.Path, path.RawQuery)
	r.mu.Unlock()
	if err != nil {
		return h, err
	}
	defer func() { r.mu.Lock(); r.revokeGrantLocked(key); r.mu.Unlock() }()
	upstream.Path = path.Path
	upstream.RawPath = path.RawPath
	upstream.RawQuery = path.RawQuery
	upstreamClient := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer upstreamClient.CloseIdleConnections()
	h.UpstreamStatus, err = getStatus(ctx, upstreamClient, upstream.String())
	if err != nil || h.UpstreamStatus < 200 || h.UpstreamStatus >= 300 {
		return h, webservice.Fail("HEALTH_CHECK_FAILED", "application HTTP check failed")
	}
	public, err := url.Parse(grant.PreviewURL)
	if err != nil {
		return h, err
	}
	if r.opts.PreviewMode == "path" {
		basePath := public.Path
		public.Path = basePath + strings.TrimPrefix(path.Path, "/")
		if path.RawPath != "" {
			public.RawPath = basePath + strings.TrimPrefix(path.RawPath, "/")
		}
	} else {
		public.Path = path.Path
		public.RawPath = path.RawPath
	}
	public.RawQuery = path.RawQuery
	client := r.opts.ProbeClient
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxIdleConns: 1, IdleConnTimeout: 5 * time.Second}}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copyClient.Timeout <= 0 || copyClient.Timeout > 3*time.Second {
		copyClient.Timeout = 3 * time.Second
	}
	h.GatewayStatus, err = getStatus(ctx, &copyClient, public.String())
	if err != nil || h.GatewayStatus < 200 || h.GatewayStatus >= 300 {
		return h, webservice.Fail("GATEWAY_UNAVAILABLE", "public gateway HTTP check failed")
	}
	h.Result = "passed"
	return h, nil
}

func getStatus(ctx context.Context, client *http.Client, address string) (int, error) {
	if client == nil {
		return 0, errors.New("gateway probe client is unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return 0, err
	}
	// Every self-probe has a fresh grant hostname. Keeping one idle TCP/TLS
	// connection per hostname eventually consumes the shared entry's entire
	// connection budget and rejects unrelated main-site traffic. Probes are
	// deliberately one-shot, even when a caller injects a pooling client.
	req.Close = true
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	if resp == nil || resp.Body == nil {
		return 0, errors.New("gateway probe returned no response body")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

func (r *Router) controlAliveLocked(now time.Time) bool {
	return !r.closed && !r.suspended && now.Sub(r.lastControl) <= r.opts.ControlTimeout
}
func (r *Router) suspendControl() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.suspended = true
	for _, g := range r.grants {
		for active := range g.active {
			active.cancel()
		}
	}
}

// replaceController fences the previous Manager's observations and bearer
// grants before a new Manager becomes authoritative. Runtime ownership and
// terminal generation/revision fences are retained; no main/TURN listener is
// touched. The new controller must Apply its verified snapshot before Open.
func (r *Router) replaceController() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.grants {
		r.revokeGrantLocked(key)
	}
	for _, current := range r.routes {
		current.confirmed = false
	}
	r.suspended = true
}

func (r *Router) revokeGrantLocked(key [32]byte) {
	g := r.grants[key]
	if g == nil {
		return
	}
	for active := range g.active {
		active.cancel()
	}
	delete(r.grantIDs, g.id)
	delete(r.grants, key)
}
func (r *Router) revokeServiceLocked(id string, generation int) {
	for key, g := range r.grants {
		if g.service == id && (generation == 0 || g.generation == generation) {
			r.revokeGrantLocked(key)
		}
	}
}
func (r *Router) expireLocked(now time.Time) {
	alive := r.controlAliveLocked(now)
	for key, g := range r.grants {
		current := r.routes[g.service]
		if !now.Before(g.expires) || current == nil || current.revoked || current.record.Generation != g.generation || !now.Before(current.record.LeaseExpiresAt) {
			r.revokeGrantLocked(key)
			continue
		}
		if !alive {
			for active := range g.active {
				active.cancel()
			}
		}
	}
}
func (r *Router) expireLoop() {
	defer r.wg.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.done:
			return
		case now := <-ticker.C:
			r.mu.Lock()
			r.expireLocked(now)
			r.mu.Unlock()
		}
	}
}

func (r *Router) Close() error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.done)
		for key := range r.grants {
			r.revokeGrantLocked(key)
		}
	}
	r.mu.Unlock()
	r.wg.Wait()
	r.mainTransport.CloseIdleConnections()
	r.appTransport.CloseIdleConnections()
	return nil
}
func (r *Router) Handler() http.Handler { return http.HandlerFunc(r.serveHTTP) }

func (r *Router) serveHTTP(w http.ResponseWriter, req *http.Request) {
	authority, host, err := r.authority(req.Host)
	if err != nil {
		http.NotFound(w, req)
		return
	}
	pathMode := r.opts.PreviewMode == "path"
	publicOrigin := r.origin.Scheme + "://" + req.Host
	grantPrefix := ""
	setHeaders := func(h http.Header) {
		if pathMode {
			source := grantPrefix
			if source == "" {
				source = previewPathPrefix + "invalid/"
			}
			setDynamicHeaders(h, publicOrigin+source, strings.Join(r.parentOrigins, " "))
		} else {
			setDynamicHeaders(h, publicOrigin, strings.Join(r.parentOrigins, " "))
		}
	}
	localError := func(status int, message string) {
		setHeaders(w.Header())
		http.Error(w, message, status)
	}
	var token string
	if pathMode {
		if !r.mainHosts[authority] {
			localError(http.StatusNotFound, "404 page not found")
			return
		}
		if !previewNamespaceRequest(req.URL) {
			r.mainProxy.ServeHTTP(w, req)
			return
		}
		if !validPreviewPath(req.URL) {
			localError(http.StatusNotFound, "404 page not found")
			return
		}
		if !strings.HasPrefix(req.URL.EscapedPath(), previewPathPrefix) {
			localError(http.StatusNotFound, "404 page not found")
			return
		}
		parts := strings.SplitN(strings.TrimPrefix(req.URL.EscapedPath(), previewPathPrefix), "/", 2)
		if len(parts) != 2 {
			localError(http.StatusNotFound, "404 page not found")
			return
		}
		token = parts[0]
		grantPrefix = previewPathPrefix + token + "/"
		// Work on a clone: the raw escaped application path and query survive
		// proxying, while the grant token never reaches the private runtime.
		appURL := *req.URL
		appURL.Path = "/" + strings.TrimPrefix(req.URL.Path, grantPrefix)
		appURL.RawPath = ""
		if req.URL.RawPath != "" {
			appURL.RawPath = "/" + parts[1]
		}
		req = req.Clone(req.Context())
		req.URL = &appURL
	} else {
		if r.mainHosts[authority] {
			r.mainProxy.ServeHTTP(w, req)
			return
		}
		suffix := "." + r.domain
		if !strings.HasSuffix(host, suffix) {
			localError(http.StatusNotFound, "404 page not found")
			return
		}
		token = strings.TrimSuffix(host, suffix)
	}
	if req.Method == http.MethodConnect || req.Method == http.MethodTrace {
		localError(http.StatusMethodNotAllowed, "method not supported")
		return
	}
	if len(token) != 48 || strings.Contains(token, ".") {
		localError(http.StatusNotFound, "404 page not found")
		return
	}
	if _, err := hex.DecodeString(token); err != nil {
		localError(http.StatusNotFound, "404 page not found")
		return
	}
	key := sha256.Sum256([]byte(token))
	now := time.Now()
	r.mu.Lock()
	r.expireLocked(now)
	grant := r.grants[key]
	if grant == nil {
		r.mu.Unlock()
		localError(http.StatusNotFound, "404 page not found")
		return
	}
	current := r.routes[grant.service]
	if !r.controlAliveLocked(now) || current == nil || current.revoked || !current.confirmed || (grant.probePath == "" && current.record.State != "ready") {
		r.mu.Unlock()
		localError(http.StatusServiceUnavailable, "service temporarily unavailable")
		return
	}
	if grant.probePath != "" && (req.Method != http.MethodGet && req.Method != http.MethodHead || req.URL.Path != grant.probePath || req.URL.RawQuery != grant.probeQuery) {
		r.mu.Unlock()
		localError(http.StatusNotFound, "404 page not found")
		return
	}
	upstream := *current.upstream
	ctx, cancel := context.WithCancel(req.Context())
	active := &activeRequest{cancel: cancel}
	grant.active[active] = struct{}{}
	r.mu.Unlock()
	defer func() { cancel(); r.mu.Lock(); delete(grant.active, active); r.mu.Unlock() }()
	if req.Method == http.MethodOptions {
		setHeaders(w.Header())
		w.WriteHeader(http.StatusNoContent)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(&upstream)
	proxy.Transport = r.appTransport
	director := proxy.Director
	proxy.Director = func(out *http.Request) {
		director(out)
		out.Host = upstream.Host
		out.Header.Del("Cookie")
		out.Header.Del("Authorization")
		out.Header.Del("Proxy-Authorization")
		stripForwarded(out.Header)
		out.Header["X-Forwarded-For"] = nil
	}
	proxy.ErrorLog = log.New(io.Discard, "", 0)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		setHeaders(w.Header())
		http.Error(w, "application unavailable", http.StatusBadGateway)
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		stripApplicationHeaders(resp.Header, pathMode)
		if location := resp.Header.Get("Location"); location != "" {
			if pathMode {
				rewritten, err := rewritePathLocation(location, req.URL, &upstream, req.Host, grantPrefix, r.origin.Scheme)
				if err != nil {
					return err
				}
				resp.Header.Set("Location", rewritten)
			} else {
				u, err := url.Parse(location)
				if err != nil || u.User != nil || strings.ContainsAny(location, "\\\r\n") || (u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https") {
					return errors.New("invalid redirect")
				}
				if u.Host != "" {
					if u.Host != upstream.Host && u.Host != req.Host {
						return errors.New("external redirect refused")
					}
					u.Scheme = r.origin.Scheme
					u.Host = req.Host
					resp.Header.Set("Location", u.String())
				}
			}
		}
		// ReverseProxy copies upstream headers with Add. Keep the real
		// ResponseWriter untouched until that copy: pre-setting the same policy
		// there would emit duplicate ACAO/CSP values and break browser CORS.
		setHeaders(resp.Header)
		return nil
	}
	if pathMode {
		// Informational responses (for example 103 Early Hints) bypass
		// ModifyResponse, so filter their headers at the writer as well.
		w = &previewResponseWriter{ResponseWriter: w, setHeaders: setHeaders}
	}
	proxy.ServeHTTP(w, req.WithContext(ctx))
}

func setDynamicHeaders(h http.Header, origin, mainOrigin string) {
	wsOrigin := strings.Replace(origin, "https://", "wss://", 1)
	if strings.HasPrefix(wsOrigin, "http://") {
		wsOrigin = strings.Replace(wsOrigin, "http://", "ws://", 1)
	}
	h.Set("Content-Security-Policy", "sandbox allow-scripts allow-forms; default-src 'none'; script-src 'unsafe-inline' 'unsafe-eval' blob: "+origin+"; style-src 'unsafe-inline' "+origin+"; img-src data: blob: "+origin+"; font-src data: "+origin+"; media-src blob: "+origin+"; connect-src "+origin+" "+wsOrigin+"; form-action "+origin+"; base-uri 'none'; object-src 'none'; frame-src 'none'; worker-src 'none'; manifest-src 'none'; frame-ancestors "+mainOrigin)
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type, Range")
	h.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
}

// previewNamespaceRequest identifies the reserved namespace before any main
// proxy sees it, including paths another layer could decode or normalize.
// Ordinary main-site paths retain their existing handling.
func previewNamespaceRequest(u *url.URL) bool {
	value := u.EscapedPath()
	for depth := 0; depth < 16; depth++ {
		normalized := path.Clean(strings.ReplaceAll(value, "\\", "/"))
		if normalized == "/_pogo" || strings.HasPrefix(normalized, "/_pogo/") {
			return true
		}
		decoded, err := url.PathUnescape(value)
		if err != nil || decoded == value {
			return false
		}
		value = decoded
	}
	// Excessive nested encoding is not a supported main-site URL and must
	// not bypass the bounded namespace inspection above.
	return true
}

// validPreviewPath rejects ambiguous browser/server normalization, including
// nested escapes. Query strings are deliberately outside this path policy.
func validPreviewPath(u *url.URL) bool {
	if u == nil || !strings.HasPrefix(u.Path, "/") {
		return false
	}
	raw := u.EscapedPath()
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") || strings.Contains(lower, "%25") {
		return false
	}
	for _, value := range []string{raw, u.Path} {
		if strings.Contains(value, "//") || strings.ContainsAny(value, "\\\r\n\x00") {
			return false
		}
		for _, segment := range strings.Split(value, "/") {
			if segment == "." || segment == ".." {
				return false
			}
		}
		for _, c := range value {
			if c < 0x20 || c == 0x7f {
				return false
			}
		}
	}
	return true
}

func rewritePathLocation(location string, appRequest, upstream *url.URL, publicHost, prefix, scheme string) (string, error) {
	u, err := url.Parse(location)
	if err != nil || u.User != nil || u.Opaque != "" || strings.ContainsAny(location, "\\\r\n") || (u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https") || (u.Scheme != "" && u.Host == "") {
		return "", errors.New("invalid redirect")
	}
	if u.Host != "" && u.Host != upstream.Host {
		if !strings.EqualFold(u.Host, publicHost) || (u.Scheme != "" && u.Scheme != scheme) || !strings.HasPrefix(u.EscapedPath(), prefix) {
			return "", errors.New("external or main-site redirect refused")
		}
		u.Path = "/" + strings.TrimPrefix(u.Path, prefix)
		if u.RawPath != "" {
			u.RawPath = "/" + strings.TrimPrefix(u.RawPath, prefix)
		}
	} else if strings.HasPrefix(u.Path, strings.TrimSuffix(previewPathPrefix, "/")) {
		// A root-relative URL must never switch grants or reenter the public
		// namespace through the application's own upstream.
		if !strings.HasPrefix(u.EscapedPath(), prefix) {
			return "", errors.New("cross-grant redirect refused")
		}
		u.Path = "/" + strings.TrimPrefix(u.Path, prefix)
		if u.RawPath != "" {
			u.RawPath = "/" + strings.TrimPrefix(u.RawPath, prefix)
		}
	}
	u.Scheme, u.Host = "", ""
	base := *appRequest
	base.Scheme, base.Host = upstream.Scheme, upstream.Host
	// Reject encoded traversal before ResolveReference cleans dot segments.
	refPath := *u
	if !strings.HasPrefix(refPath.Path, "/") {
		refPath.Path = "/" + refPath.Path
		if refPath.RawPath != "" {
			refPath.RawPath = "/" + refPath.RawPath
		}
	}
	if strings.Contains(refPath.EscapedPath(), "%") && !validPreviewPath(&refPath) {
		return "", errors.New("ambiguous redirect path")
	}
	target := base.ResolveReference(u)
	if !validPreviewPath(target) {
		return "", errors.New("ambiguous redirect path")
	}
	out := &url.URL{Path: prefix + strings.TrimPrefix(target.Path, "/"), RawQuery: target.RawQuery, ForceQuery: target.ForceQuery, Fragment: target.Fragment, RawFragment: target.RawFragment}
	if target.RawPath != "" {
		out.RawPath = prefix + strings.TrimPrefix(target.RawPath, "/")
	}
	return out.String(), nil
}

func stripApplicationHeaders(h http.Header, pathMode bool) {
	for name := range h {
		lower := strings.ToLower(name)
		strip := strings.HasPrefix(lower, "access-control-") || strings.HasPrefix(lower, "content-security-policy") || strings.HasPrefix(lower, "cross-origin-")
		switch lower {
		case "set-cookie", "clear-site-data", "refresh", "x-frame-options", "www-authenticate", "proxy-authenticate":
			strip = true
		}
		if pathMode {
			// An application shares the public main authority in path mode, so
			// it must not set origin-wide state or preload unscoped main URLs.
			switch lower {
			case "link", "content-location", "alt-svc", "report-to", "reporting-endpoints", "nel",
				"strict-transport-security", "public-key-pins", "public-key-pins-report-only", "expect-ct",
				"service-worker-allowed", "accept-ch", "critical-ch", "sourcemap", "x-sourcemap":
				strip = true
			}
		}
		if strip {
			h.Del(name)
		}
	}
}

type previewResponseWriter struct {
	http.ResponseWriter
	setHeaders func(http.Header)
}

func (w *previewResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *previewResponseWriter) WriteHeader(status int) {
	stripApplicationHeaders(w.Header(), true)
	w.setHeaders(w.Header())
	w.ResponseWriter.WriteHeader(status)
}
