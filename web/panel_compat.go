package web

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const panelRewriteLimit = 8 << 20

func panelMode(p Panel) string {
	if p.ProxyMode == "compat" {
		return "compat"
	}
	return "passthrough"
}

func panelSource(p Panel) *url.URL {
	raw := p.SourceURL
	if raw == "" {
		raw = p.URL
	}
	u, _ := url.Parse(raw)
	return u
}

func panelEntryPath(p Panel) string {
	u := panelSource(p)
	prefix := "/panels/" + url.PathEscape(p.Name) + "/"
	if p.ProxyMode != "compat" {
		return prefix + fragmentSuffix(u)
	}
	return strings.TrimSuffix(prefix, "/") + nonemptyPath(u.EscapedPath()) + querySuffix(u) + fragmentSuffix(u)
}

func nonemptyPath(s string) string {
	if s == "" {
		return "/"
	}
	return s
}
func querySuffix(u *url.URL) string {
	if u.RawQuery != "" || u.ForceQuery {
		return "?" + u.RawQuery
	}
	return ""
}
func fragmentSuffix(u *url.URL) string {
	if u.Fragment != "" {
		return "#" + u.EscapedFragment()
	}
	return ""
}

// A compatible mount represents the upstream origin, not its landing page.
// Entry paths and query strings belong to navigation, never every asset/API.
type panelURLMapper struct {
	prefix         string
	source, target *url.URL
}

func newPanelURLMapper(p Panel) panelURLMapper {
	target, _ := url.Parse(p.URL)
	return panelURLMapper{"/panels/" + url.PathEscape(p.Name), panelSource(p), target}
}

func (m panelURLMapper) owns(u *url.URL) bool {
	return (u.Scheme == "http" || u.Scheme == "https") &&
		(panelOrigin(u) == panelOrigin(m.source) || panelOrigin(u) == panelOrigin(m.target))
}

func panelOrigin(u *url.URL) string {
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host
}

func (m panelURLMapper) rewrite(raw string, base *url.URL) string {
	if raw == "" || strings.HasPrefix(raw, "#") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return raw
	}
	// An existing local mount is already translated. Do not double-prefix it.
	if u.Host == "" && (u.Path == m.prefix || strings.HasPrefix(u.EscapedPath(), m.prefix+"/")) {
		return raw
	}
	u = base.ResolveReference(u)
	if !m.owns(u) {
		return raw
	}
	return m.prefix + nonemptyPath(u.EscapedPath()) + querySuffix(u) + fragmentSuffix(u)
}

func newCompatiblePanelProxy(p Panel) *httputil.ReverseProxy {
	m := newPanelURLMapper(p)
	origin := *m.target
	origin.Path, origin.RawPath, origin.RawQuery, origin.Fragment = "", "", "", ""
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	// A container loopback substitution changes the dial address, not the
	// virtual host or the TLS certificate identity the user registered.
	if m.source.Hostname() != m.target.Hostname() {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		}
		transport.TLSClientConfig.ServerName = m.source.Hostname()
	}
	return &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetXForwarded()
			segments := strings.SplitN(pr.In.URL.EscapedPath(), "/", 4)
			tail := "/"
			if len(segments) == 4 {
				tail += segments[3]
			}
			decoded, err := url.PathUnescape(tail)
			if err != nil {
				decoded = tail
			}
			pr.Out.URL.Path, pr.Out.URL.RawPath = decoded, tail
			pr.SetURL(&origin)
			pr.Out.Host = m.source.Host
			pr.Out.Header.Set("X-Forwarded-Prefix", m.prefix)
			// Prefer uncompressed text. A backend that still returns gzip is
			// decoded below with the same bounded output limit.
			pr.Out.Header.Set("Accept-Encoding", "identity")
			// Transformed representations must not reuse upstream validators.
			pr.Out.Header.Del("If-None-Match")
			pr.Out.Header.Del("If-Modified-Since")
			// Pogo's login cookie is never an upstream application's credential.
			pr.Out.Header.Del("Cookie")
			for _, c := range pr.In.Cookies() {
				if c.Name != sessionCookie {
					pr.Out.AddCookie(c)
				}
			}
			if o, err := url.Parse(pr.In.Header.Get("Origin")); err == nil && o.Host == pr.In.Host && (o.Scheme == "http" || o.Scheme == "https") {
				pr.Out.Header.Set("Origin", m.source.Scheme+"://"+m.source.Host)
			}
			if ref, err := url.Parse(pr.In.Referer()); err == nil && ref.Host == pr.In.Host && strings.HasPrefix(ref.EscapedPath(), m.prefix+"/") {
				ref.Scheme, ref.Host = m.source.Scheme, m.source.Host
				ref.Path = strings.TrimPrefix(ref.Path, "/panels/"+p.Name)
				ref.RawPath = ""
				pr.Out.Header.Set("Referer", ref.String())
			}
		},
		ModifyResponse: func(res *http.Response) error { return adaptPanelResponse(res, m) },
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// No upstream address, query, credentials or response body in errors.
			code := "PANEL_UPSTREAM_UNAVAILABLE"
			message := "The upstream connection failed. Check server reachability and TLS."
			if e, ok := err.(*panelCompatError); ok {
				code, message = e.code, e.message
			}
			w.Header().Set("X-Pogo-Panel-Error", code)
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, "%s: %s\n", code, message)
		},
	}
}

type panelCompatError struct{ code, message string }

func (e *panelCompatError) Error() string { return e.code + ": " + e.message }
func panelUnsupported(message string) error {
	return &panelCompatError{"PANEL_COMPAT_UNSUPPORTED", message}
}

func adaptPanelResponse(res *http.Response, m panelURLMapper) error {
	base := *res.Request.URL
	base.Scheme, base.Host = m.source.Scheme, m.source.Host
	res.Header.Set("X-Pogo-Panel-Mode", "compat")
	if location := res.Header.Get("Location"); location != "" {
		res.Header.Set("Location", m.rewrite(location, &base))
	}
	if err := adaptPanelCookies(res, m); err != nil {
		res.Body.Close()
		return err
	}
	if res.Request.Method == http.MethodHead || res.StatusCode == 204 || res.StatusCode == 304 {
		return nil
	}
	typ, params, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	dest := res.Request.Header.Get("Sec-Fetch-Dest")
	if res.StatusCode >= 400 && (dest == "script" || dest == "style") {
		res.Header.Set("X-Pogo-Panel-Error", "PANEL_UPSTREAM_RESOURCE_ERROR")
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 && typ == "text/html" && (dest == "script" || dest == "style") {
		res.Body.Close()
		return &panelCompatError{"PANEL_RESOURCE_TYPE_MISMATCH", "A script or stylesheet returned HTML. Check upstream routing or login redirects."}
	}
	if typ != "text/html" && typ != "text/css" && typ != "application/javascript" && typ != "text/javascript" {
		return nil
	}
	if strings.Contains(strings.ToLower(res.Header.Get("Content-Disposition")), "attachment") {
		return nil
	}
	if res.StatusCode == http.StatusPartialContent || strings.Contains(strings.ToLower(res.Header.Get("Cache-Control")), "no-transform") {
		res.Body.Close()
		return panelUnsupported("Partial or no-transform text cannot be safely adapted. Use original forwarding for this resource.")
	}
	if charset := strings.ToLower(params["charset"]); charset != "" && charset != "utf-8" && charset != "us-ascii" {
		res.Body.Close()
		return panelUnsupported("Only UTF-8 text can be adapted. Use original forwarding or a real browser for this page.")
	}
	defer res.Body.Close()
	var reader io.Reader = res.Body
	switch strings.ToLower(res.Header.Get("Content-Encoding")) {
	case "", "identity":
	case "gzip":
		gz, err := gzip.NewReader(res.Body)
		if err != nil {
			return panelUnsupported("Invalid compressed upstream response.")
		}
		defer gz.Close()
		reader = gz
	default:
		return panelUnsupported("The upstream ignored identity encoding. Use original forwarding for this encoding.")
	}
	body, err := io.ReadAll(io.LimitReader(reader, panelRewriteLimit+1))
	if err != nil {
		return err
	}
	if len(body) > panelRewriteLimit {
		return panelUnsupported("The response exceeds the 8 MiB text adaptation limit.")
	}
	if !utf8.Valid(body) {
		return panelUnsupported("The upstream text is not UTF-8. Use original forwarding for this page.")
	}
	var out []byte
	switch typ {
	case "text/html":
		var nonceBytes [18]byte
		if _, err = rand.Read(nonceBytes[:]); err != nil {
			return err
		}
		nonce := base64.StdEncoding.EncodeToString(nonceBytes[:])
		out, err = rewritePanelHTML(body, m, &base, nonce)
		if err == nil {
			for _, name := range []string{"Content-Security-Policy", "Content-Security-Policy-Report-Only"} {
				values := res.Header.Values(name)
				res.Header.Del(name)
				for _, value := range values {
					res.Header.Add(name, panelCSPNonce(value, nonce))
				}
			}
		}
	case "text/css":
		out, err = rewritePanelCSS(body, m, &base)
	default:
		out, err = rewritePanelJS(body, m, &base)
	}
	if err != nil {
		return err
	}
	res.Body = io.NopCloser(bytes.NewReader(out))
	res.ContentLength = int64(len(out))
	res.Header.Del("Content-Encoding")
	res.Header.Del("Content-Length")
	res.Header.Del("ETag")
	res.Header.Del("Last-Modified")
	res.Header.Del("Content-MD5")
	res.Header.Set("Cache-Control", "no-store")
	return nil
}

func adaptPanelCookies(res *http.Response, m panelURLMapper) error {
	values := res.Header.Values("Set-Cookie")
	if len(values) == 0 {
		return nil
	}
	res.Header.Del("Set-Cookie")
	for _, value := range values {
		c, err := http.ParseSetCookie(value)
		if err != nil {
			return panelUnsupported("The upstream returned an invalid cookie.")
		}
		if c.Name == sessionCookie || strings.HasPrefix(c.Name, "__Host-") || strings.HasPrefix(c.Name, "__Host-Http-") {
			return panelUnsupported("This login cookie requires an independent site origin. Use original forwarding or a real browser.")
		}
		if c.Domain != "" {
			domain := strings.TrimPrefix(strings.ToLower(c.Domain), ".")
			host := strings.ToLower(m.source.Hostname())
			if domain != host && !strings.HasSuffix(host, "."+domain) {
				continue
			}
			c.Domain = ""
		}
		path := c.Path
		if path == "" || !strings.HasPrefix(path, "/") {
			path = res.Request.URL.Path
			if i := strings.LastIndex(path, "/"); i >= 0 {
				path = nonemptyPath(path[:i])
			} else {
				path = "/"
			}
		}
		c.Path = m.prefix + path
		res.Header.Add("Set-Cookie", c.String())
	}
	return nil
}

func panelCSPNonce(policy, nonce string) string {
	parts := strings.Split(policy, ";")
	found := false
	var defaults string
	for i, part := range parts {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "default-src" {
			defaults = strings.Join(fields[1:], " ")
		}
		if fields[0] == "script-src" || fields[0] == "script-src-elem" {
			parts[i] = panelScriptSources(strings.TrimSpace(part), nonce)
			found = true
		}
	}
	if !found && defaults != "" {
		parts = append(parts, panelScriptSources("script-src "+defaults, nonce))
	}
	return strings.Join(parts, ";")
}

func panelScriptSources(directive, nonce string) string {
	// Adding a nonce to an unsafe-inline-only policy disables the upstream's
	// inline scripts. In that case the bootstrap is already permitted.
	if strings.Contains(directive, "'unsafe-inline'") && !strings.Contains(directive, "'nonce-") && !strings.Contains(directive, "'sha") {
		return directive
	}
	return directive + " 'nonce-" + nonce + "'"
}

func panelBootstrap(m panelURLMapper, base *url.URL, nonce string) string {
	config, _ := json.Marshal(map[string]string{"prefix": m.prefix, "source": panelOrigin(m.source), "target": panelOrigin(m.target)})
	return `<script nonce="` + nonce + `">` + strings.Replace(panelCompatBootstrap, "__POGO_PANEL_CONFIG__", string(config), 1) + `</script>`
}
