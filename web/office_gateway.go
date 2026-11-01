package web

// The editor is a same-origin client of Pogo. The gateway grants only the
// editor assets and documents that an authenticated open/config has admitted;
// it is not a general DocumentServer proxy or a second account-token channel.
import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/identity"
)

const officeGatewayPrefix = "/office/editor"
const officeGatewayCookie = "pogo_office_gateway"
const officeGatewayLogoutCookie = "pogo_office_logout"
const officeGatewayTTL = 8 * time.Hour
const officeGatewayMaxGrants = 1024
const officeGatewayMaxDocuments = 64

var officeEngineVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[.-](?:[0-9]+|[a-f0-9]{32}))?(?:-pogo-[a-f0-9]{64})?$`)

type officeGatewayGrant struct {
	token                    string
	origin                   string
	owner                    *identity.Scope
	username, credentialKind string
	credentialHash           [32]byte
	expires                  time.Time
	documents                map[string]string // engine key -> Pogo document session id
	connections              map[*officeGatewayConnection]struct{}
}

type officeGateway struct {
	mu        sync.Mutex
	server    *Server
	grants    map[string]*officeGatewayGrant
	closed    bool
	proxy     *httputil.ReverseProxy
	transport *http.Transport
}

type officeGatewayRequest struct {
	grant               string
	document            string
	origin              string
	engineAuthorization string
}
type officeGatewayRequestKey struct{}

func newOfficeGateway(s *Server) *officeGateway {
	g := &officeGateway{server: s, grants: map[string]*officeGatewayGrant{}}
	upstream, _ := url.Parse(s.office.cfg.serviceURL)
	g.transport = officeUpstreamTransport()
	g.transport.MaxIdleConns = 64
	g.transport.MaxIdleConnsPerHost = 32
	g.transport.ResponseHeaderTimeout = officeOpenHTTPBudget
	g.proxy = &httputil.ReverseProxy{
		Transport:     g.transport,
		FlushInterval: -1,
		Rewrite: func(p *httputil.ProxyRequest) {
			binding, _ := p.In.Context().Value(officeGatewayRequestKey{}).(officeGatewayRequest)
			public, _ := url.Parse(binding.origin)
			p.SetURL(upstream)
			p.Out.URL.Path = strings.TrimPrefix(p.In.URL.Path, officeGatewayPrefix)
			p.Out.URL.RawPath = ""
			p.Out.Host = upstream.Host
			// Pogo credentials and client-controlled forwarding claims never cross
			// the container boundary, including on a WebSocket handshake.
			for _, name := range []string{"Authorization", "Cookie", "Forwarded", "X-Real-IP", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Prefix", "X-Pogo-Office-Origin", "X-Pogo-Office-Gateway", "X-Pogo-Release", "X-Pogo-Browser-Ticket"} {
				p.Out.Header.Del(name)
			}
			if binding.engineAuthorization != "" {
				p.Out.Header.Set("Authorization", binding.engineAuthorization)
			}
			q := p.Out.URL.Query()
			// Engine JWTs may legitimately use ?token= on editor resources. Only
			// strip a value that resolves to a durable Pogo account credential.
			if value := q.Get("token"); value != "" {
				_, accountToken := s.auth.userByToken(value)
				if accountToken || (s.opts.Token != "" && officeEqual(value, s.opts.Token)) {
					q.Del("token")
				}
			}
			p.Out.URL.RawQuery = q.Encode()
			host := public.Host + officeGatewayPrefix
			p.Out.Header.Set("X-Forwarded-Host", host)
			p.Out.Header.Set("X-Forwarded-Proto", public.Scheme)
			p.Out.Header.Set("X-Forwarded-Prefix", officeGatewayPrefix)
			mac := hmac.New(sha256.New, s.office.cfg.secret)
			mac.Write([]byte("pogo-office-gateway/v1\n" + public.Scheme + "\n" + host + "\n" + officeGatewayPrefix))
			p.Out.Header.Set("X-Pogo-Office-Gateway", base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
		},
		ModifyResponse: func(response *http.Response) error {
			binding, _ := response.Request.Context().Value(officeGatewayRequestKey{}).(officeGatewayRequest)
			public, _ := url.Parse(binding.origin)
			if location := response.Header.Get("Location"); location != "" {
				u, err := url.Parse(location)
				if err != nil || u.User != nil {
					return errors.New("invalid editor redirect")
				}
				if u.Host != "" && ((u.Host != upstream.Host && u.Host != public.Host) || u.IsAbs() && u.Scheme+"://"+u.Host != s.office.cfg.serviceOrigin && u.Scheme+"://"+u.Host != binding.origin) {
					return errors.New("external editor redirect rejected")
				}
				if u.Path != "" && !strings.HasPrefix(u.Path, officeGatewayPrefix+"/") {
					u.Path = officeGatewayPrefix + "/" + strings.TrimPrefix(u.Path, "/")
				}
				u.Scheme, u.Host, u.RawPath = "", "", ""
				response.Header.Set("Location", u.String())
			}
			// DocumentServer uses its signed editor JWT for dynamic resources, not
			// Pogo's cookie. Do not let an upstream set account or global cookies.
			response.Header.Del("Set-Cookie")
			response.Header.Del("Service-Worker-Allowed")
			if path.Base(response.Request.URL.Path) == "document_editor_service_worker.js" {
				// Each verified engine build keeps its own registration scope.
				// Never grant a worker control over Pogo or another Office runtime.
				response.Header.Set("Service-Worker-Allowed", officeGatewayPrefix+strings.TrimSuffix(path.Dir(response.Request.URL.Path), "/")+"/")
			}
			response.Header.Set("Referrer-Policy", "same-origin")
			if response.StatusCode == http.StatusSwitchingProtocols {
				binding, _ := response.Request.Context().Value(officeGatewayRequestKey{}).(officeGatewayRequest)
				stream, ok := response.Body.(io.ReadWriteCloser)
				if !ok {
					return errors.New("editor upgrade has no duplex stream")
				}
				connection := &officeGatewayConnection{ReadWriteCloser: stream, done: make(chan struct{}), document: binding.document}
				connection.finished = func() {
					g.mu.Lock()
					if current := g.grants[binding.grant]; current != nil {
						delete(current.connections, connection)
					}
					g.mu.Unlock()
				}
				g.mu.Lock()
				grant := g.grants[binding.grant]
				if g.closed || grant == nil {
					g.mu.Unlock()
					stream.Close()
					return errors.New("editor authorization revoked")
				}
				grant.connections[connection] = struct{}{}
				g.mu.Unlock()
				response.Body = connection
				go g.watchConnection(binding, connection)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			officeHTTPError(w, officeFail(502, "EDITOR_UNAVAILABLE", "文档编辑器连接暂不可用，请保留当前内容后重试。"))
		},
	}
	return g
}

func officeGatewayCookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// issue is called while the document mutex is held, only after ownership and
// instance checks. Credentials are remembered as fingerprints, never copies.
func (g *officeGateway) issue(w http.ResponseWriter, r *http.Request, d *officeDocumentSession) error {
	origin := d.browserOrigin
	if origin == "" {
		var allowed bool
		origin, allowed = officeRequestOrigin(r)
		if !allowed {
			return officeFail(403, "EDITOR_ORIGIN_REJECTED", "编辑器必须从当前 Pogo 页面打开。")
		}
	} else if !officeBoundOrigin(r, origin) {
		return officeFail(403, "EDITOR_ORIGIN_REJECTED", "该编辑器属于另一个 Pogo 页面来源，请回到原页面操作。")
	}
	id, ok := identityFrom(r)
	if !ok {
		authenticated, valid := g.server.authenticate(r)
		if valid {
			id = &authenticated
			ok = true
		}
	}
	if !ok && (g.server.opts.Token != "" || g.server.auth.Enabled()) {
		return officeFail(401, "UNAUTHORIZED", "请重新登录后打开文档。")
	}
	if id == nil {
		id = &requestIdentity{}
	}
	var owner *identity.Scope
	if id.Local {
		owner = officeCopyOwner(&id.Scope)
	}
	if !officeSameOwner(owner, d.owner) {
		return officeFail(403, "DOCUMENT_NOT_FOUND", "此文档不属于当前账号。")
	}
	kind, fingerprint := "anonymous", [32]byte{}
	if credential := bearerOrQueryToken(r); credential != "" {
		kind = "bearer"
		fingerprint = sha256.Sum256([]byte(credential))
	} else if credential := officeGatewayCookieValue(r, sessionCookie); credential != "" {
		kind = "session"
		fingerprint = sha256.Sum256([]byte(credential))
	}
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return officeFail(503, "EDITOR_UNAVAILABLE", "编辑服务正在关闭。")
	}
	for token, grant := range g.grants {
		if now.After(grant.expires) {
			g.revokeLocked(token)
		}
	}
	grant := g.grants[officeGatewayCookieValue(r, officeGatewayCookieName(officeGatewayCookie, origin))]
	if grant != nil && (grant.origin != origin || !officeSameOwner(grant.owner, owner) || grant.credentialKind != kind || grant.credentialHash != fingerprint) {
		g.revokeLocked(grant.token)
		grant = nil
	}
	if grant == nil {
		if len(g.grants) >= officeGatewayMaxGrants {
			return officeFail(429, "EDITOR_LIMIT", "打开的编辑器过多，请关闭不再使用的窗口。")
		}
		grant = &officeGatewayGrant{token: randomID(32), origin: origin, owner: owner, username: id.Username, credentialKind: kind, credentialHash: fingerprint, documents: map[string]string{}, connections: map[*officeGatewayConnection]struct{}{}}
		g.grants[grant.token] = grant
	}
	if len(grant.documents) >= officeGatewayMaxDocuments && grant.documents[d.key] == "" {
		return officeFail(429, "EDITOR_LIMIT", "同一浏览器打开的文档过多，请关闭不再使用的文档。")
	}
	grant.documents[d.key] = d.id
	grant.expires = now.Add(officeGatewayTTL)
	if kind == "session" {
		if expiry := officeGatewaySessionExpiry(officeGatewayCookieValue(r, sessionCookie)); expiry.Before(grant.expires) {
			grant.expires = expiry
		}
	}
	d.browserOrigin = origin
	g.cookies(w, grant.token, origin, int(time.Until(grant.expires).Seconds()))
	return nil
}

func officeGatewayCookieName(base, origin string) string {
	digest := sha256.Sum256([]byte(origin))
	return fmt.Sprintf("%s_%x", base, digest[:8])
}

func (g *officeGateway) cookies(w http.ResponseWriter, token, origin string, maxAge int) {
	for _, cookie := range []struct{ name, path string }{{officeGatewayCookie, officeGatewayPrefix + "/"}, {officeGatewayCookie, "/api/office/"}, {officeGatewayLogoutCookie, "/api/logout"}} {
		http.SetCookie(w, &http.Cookie{Name: officeGatewayCookieName(cookie.name, origin), Value: token, Path: cookie.path, HttpOnly: true, Secure: strings.HasPrefix(origin, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
	}
}

// Different localhost ports share a cookie jar. Separate cookie names and a
// grant-bound origin prevent a second tunnel from replacing the first editor.
func (g *officeGateway) requestGrant(r *http.Request, cookieBase string) *officeGatewayGrant {
	var selected *officeGatewayGrant
	for _, scheme := range []string{"http", "https"} {
		origin := scheme + "://" + r.Host
		if !officeBoundOrigin(r, origin) {
			continue
		}
		grant := g.grant(officeGatewayCookieValue(r, officeGatewayCookieName(cookieBase, origin)))
		if grant != nil && grant.origin == origin {
			if selected != nil && selected.origin != grant.origin {
				// A scheme-less internal hop must never choose between HTTP and
				// HTTPS grants by cookie order. Real requests carry a referrer or
				// Origin; bootstrap API requests also carry an authenticated hint.
				return nil
			}
			selected = grant
		}
	}
	return selected
}

// Status is already an authenticated client request. Renew a near-expiry
// grant there, without adding timers, model calls or a second UI polling loop.
func (g *officeGateway) renew(w http.ResponseWriter, r *http.Request, d *officeDocumentSession) {
	grant := g.requestGrant(r, officeGatewayCookie)
	if grant == nil || grant.documents[d.key] != d.id || time.Until(grant.expires) > officeGatewayTTL-15*time.Minute {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed {
		_ = g.issue(w, r, d)
	}
}

func (g *officeGateway) revokeLocked(token string) {
	grant := g.grants[token]
	delete(g.grants, token)
	if grant != nil {
		for connection := range grant.connections {
			go connection.Close()
		}
	}
}

// retireLocked already owns the service/document locks. This helper only
// acquires the grant ledger, never another document. Its WS close callbacks
// reacquire only that ledger, after this helper has released its lock.
func (g *officeGateway) revokeDocument(key string) {
	g.mu.Lock()
	var connections []*officeGatewayConnection
	for _, grant := range g.grants {
		delete(grant.documents, key)
		for connection := range grant.connections {
			if connection.document == key {
				connections = append(connections, connection)
			}
		}
	}
	g.mu.Unlock()
	for _, connection := range connections {
		connection.Close()
	}
}

func (s *Server) revokeOfficeGateway(w http.ResponseWriter, r *http.Request) {
	if s.office == nil || s.office.gateway == nil {
		return
	}
	g := s.office.gateway
	grant := g.requestGrant(r, officeGatewayLogoutCookie)
	origin, valid := officeRequestOrigin(r)
	if grant != nil {
		origin, valid = grant.origin, true
		g.mu.Lock()
		g.revokeLocked(grant.token)
		g.mu.Unlock()
	}
	if valid {
		g.cookies(w, "", origin, -1)
	}
}

func (g *officeGateway) credentialValid(grant *officeGatewayGrant) bool {
	s := g.server
	if grant.credentialKind == "anonymous" {
		return s.opts.Token == "" && !s.auth.Enabled()
	}
	if grant.credentialKind == "bearer" {
		if s.opts.Token != "" && grant.credentialHash == sha256.Sum256([]byte(s.opts.Token)) {
			return grant.username == "" || grant.username == bootstrapUser
		}
		if !s.auth.Enabled() {
			return false
		}
		s.auth.mu.RLock()
		defer s.auth.mu.RUnlock()
		record, exists := s.auth.users[grant.username]
		return exists && record.Token != "" && grant.credentialHash == sha256.Sum256([]byte(record.Token))
	}
	if !s.auth.Enabled() {
		return false
	}
	s.auth.mu.RLock()
	_, exists := s.auth.users[grant.username]
	s.auth.mu.RUnlock()
	return exists
}

func (g *officeGateway) grant(token string) *officeGatewayGrant {
	g.mu.Lock()
	defer g.mu.Unlock()
	grant := g.grants[token]
	if g.closed || grant == nil || time.Now().After(grant.expires) {
		return nil
	}
	copy := *grant
	copy.documents = make(map[string]string, len(grant.documents))
	for key, id := range grant.documents {
		copy.documents[key] = id
	}
	return &copy
}

func (g *officeGateway) liveDocument(grant *officeGatewayGrant, key string) bool {
	id := grant.documents[key]
	if id == "" {
		return false
	}
	o := g.server.office
	o.mu.Lock()
	d := o.sessions[id]
	closed := o.closed
	o.mu.Unlock()
	if closed || d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.closed && d.role != nil && !d.role.revoked.Load() && d.key == key && d.browserOrigin == grant.origin && officeSameOwner(d.owner, grant.owner)
}

// Versioned assets are public editor code, but dynamic document/cache paths
// carry a key admitted to this browser grant. Never proxy service/admin APIs.
func officeGatewayPath(r *http.Request, grant *officeGatewayGrant) (documentKey string, allowed bool) {
	if !strings.HasPrefix(r.URL.Path, officeGatewayPrefix+"/") || strings.Contains(r.URL.Path, "\\") || strings.ContainsRune(r.URL.Path, 0) || strings.Contains(strings.ToLower(r.URL.EscapedPath()), "%2f") || strings.Contains(strings.ToLower(r.URL.EscapedPath()), "%5c") {
		return "", false
	}
	resource := strings.TrimPrefix(r.URL.Path, officeGatewayPrefix)
	if path.Clean(resource) != strings.TrimSuffix(resource, "/") {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(resource, "/"), "/")
	if len(parts) > 1 && officeEngineVersion.MatchString(parts[0]) {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return "", false
	}
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	switch parts[0] {
	case "web-apps", "sdkjs", "sdkjs-plugins", "fonts", "dictionaries":
		return "", read
	case "document_editor_service_worker.js":
		return "", read && len(parts) == 1
	case "plugins.json", "themes.json":
		return "", read && len(parts) == 1
	case "meta":
		return "", read && len(parts) == 2 && parts[1] == "formats"
	case "doc":
		if len(parts) < 3 || (!read && r.Method != http.MethodPost) {
			return "", false
		}
		key := parts[1]
		return key, grant.documents[key] != ""
	case "downloadas", "upload", "savefile", "downloadfile":
		if len(parts) != 2 || r.Method != http.MethodPost {
			return "", false
		}
		key := parts[1]
		if grant.documents[key] == "" {
			return "", false
		}
		// Native save/export posts the current canvas to downloadas. The
		// command identity must agree with both the path and admitted document.
		if cmd := r.URL.Query().Get("cmd"); cmd != "" || parts[0] == "downloadas" {
			var command struct {
				ID string `json:"id"`
			}
			if len(cmd) > 16<<10 || json.Unmarshal([]byte(cmd), &command) != nil || command.ID != key {
				return "", false
			}
		}
		return key, true
	case "cache":
		if len(parts) < 4 || parts[1] != "files" || !read {
			return "", false
		}
		fileKey := parts[2]
		if fileKey == "data" {
			if len(parts) < 5 {
				return "", false
			}
			fileKey = parts[3]
		}
		for key := range grant.documents {
			if officeEngineFileKeyMatches(fileKey, key) {
				return key, true
			}
		}
	}
	return "", false
}

func officeEngineFileKeyMatches(resourceKey, documentKey string) bool {
	if documentKey == "" || strings.ContainsAny(documentKey, "/\\") {
		return false
	}
	return resourceKey == documentKey || strings.HasPrefix(resourceKey, documentKey+"_") || resourceKey == "conv_"+documentKey || strings.HasPrefix(resourceKey, "conv_"+documentKey+"_")
}

func (o *officeService) officeGatewayEngineBearer(authorization, key string) string {
	if !strings.HasPrefix(authorization, "Bearer ") || key == "" {
		return ""
	}
	claims, err := o.verifyJWT(strings.TrimPrefix(authorization, "Bearer "))
	if err != nil {
		return ""
	}
	var rootKey string
	_ = json.Unmarshal(claims["key"], &rootKey)
	if rootKey == key {
		return authorization
	}
	for _, field := range []string{"document", "payload"} {
		var document struct {
			Key string `json:"key"`
		}
		if json.Unmarshal(claims[field], &document) == nil && document.Key == key {
			return authorization
		}
	}
	return ""
}

func (s *Server) handleOfficeGateway(w http.ResponseWriter, r *http.Request) {
	if !s.officeAvailable(w) {
		return
	}
	g := s.office.gateway
	if g == nil {
		officeHTTPError(w, officeFail(503, "EDITOR_UNAVAILABLE", "编辑服务暂不可用。"))
		return
	}
	grant := g.requestGrant(r, officeGatewayCookie)
	if grant == nil || !g.credentialValid(grant) {
		officeHTTPError(w, officeFail(401, "EDITOR_AUTH_REQUIRED", "请重新打开文档以恢复编辑器连接。"))
		return
	}
	// A different signed-in account cannot inherit a former account's editor
	// grant. Token-only clients use the issued cookie without leaking their token.
	if caller, ok := s.authenticate(r); ok {
		var owner *identity.Scope
		if caller.Local {
			owner = &caller.Scope
		}
		if !officeSameOwner(owner, grant.owner) {
			officeHTTPError(w, officeFail(403, "DOCUMENT_NOT_FOUND", "此文档不属于当前账号。"))
			return
		}
	} else if grant.credentialKind == "session" {
		officeHTTPError(w, officeFail(401, "EDITOR_AUTH_REQUIRED", "登录已失效，请重新登录。"))
		return
	}
	key, allowed := officeGatewayPath(r, grant)
	if !allowed || key != "" && !g.liveDocument(grant, key) {
		http.NotFound(w, r)
		return
	}
	if r.ContentLength > officeMaxBytes {
		officeHTTPError(w, officeFail(413, "REQUEST_LIMIT", "编辑器请求过大。"))
		return
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, officeMaxBytes)
	}
	// Native image upload uses an Office session JWT in Authorization. Preserve
	// only a verified engine token bound to this very document, never an account
	// token. Other endpoints use JWTs inside their native command payloads.
	engineAuthorization := ""
	if strings.Contains(r.URL.Path, "/upload/") {
		engineAuthorization = s.office.officeGatewayEngineBearer(r.Header.Get("Authorization"), key)
	}
	request := r.WithContext(context.WithValue(r.Context(), officeGatewayRequestKey{}, officeGatewayRequest{grant: grant.token, document: key, origin: grant.origin, engineAuthorization: engineAuthorization}))
	g.proxy.ServeHTTP(w, request)
}

type officeGatewayConnection struct {
	io.ReadWriteCloser
	document string
	once     sync.Once
	done     chan struct{}
	finished func()
}

func (c *officeGatewayConnection) Close() error {
	var err error
	c.once.Do(func() {
		err = c.ReadWriteCloser.Close()
		close(c.done)
		if c.finished != nil {
			c.finished()
		}
	})
	return err
}

func (g *officeGateway) watchConnection(binding officeGatewayRequest, connection *officeGatewayConnection) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-connection.done:
			return
		case <-ticker.C:
			grant := g.grant(binding.grant)
			maintenance, _ := g.server.mgr.releaseMaintenance()
			if grant == nil || maintenance || !g.credentialValid(grant) || binding.document != "" && !g.liveDocument(grant, binding.document) {
				connection.Close()
				return
			}
		}
	}
}
func (g *officeGateway) close() {
	g.mu.Lock()
	g.closed = true
	var connections []*officeGatewayConnection
	for _, grant := range g.grants {
		for connection := range grant.connections {
			connections = append(connections, connection)
		}
	}
	g.grants = map[string]*officeGatewayGrant{}
	g.mu.Unlock()
	for _, connection := range connections {
		connection.Close()
	}
	g.transport.CloseIdleConnections()
}

// Keep the expiry parser local: it reads only an already verified cookie and
// stores no account credential in the gateway's grant ledger.
func officeGatewaySessionExpiry(cookie string) time.Time {
	payload, _, _ := strings.Cut(cookie, ".")
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return time.Time{}
	}
	_, value, _ := strings.Cut(string(raw), "|")
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}
