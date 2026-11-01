package web

import (
	"net/http"
	"net/url"
	"strings"
)

// Office browser origins are per editor instance, never the private upstream.
// The browser's Origin and an unchanged Host are the same contract used by the
// native browser viewer. Forwarded headers are deliberately not authority.
func officeRequestOrigin(r *http.Request) (string, bool) {
	if r == nil || r.Host == "" {
		return "", false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return "", false
	}
	hint := r.Header.Get("X-Pogo-Office-Origin")
	if hint != "" {
		if _, ok := officeCheckedOrigin(r, hint); !ok {
			return "", false
		}
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		if hint != "" && hint != origin {
			return "", false
		}
		return officeCheckedOrigin(r, origin)
	}
	if hint != "" {
		return hint, true
	}
	// Same-origin GET/config requests need not carry Origin. A same-host
	// referrer preserves HTTPS through a TLS-terminating proxy without trusting
	// arbitrary X-Forwarded-* values. Existing instances reuse their binding.
	if referrer := r.Header.Get("Referer"); referrer != "" {
		if u, err := url.Parse(referrer); err == nil && u.User == nil && u.Host == r.Host {
			if origin, ok := officeCheckedOrigin(r, u.Scheme+"://"+u.Host); ok {
				return origin, true
			}
		}
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return officeCheckedOrigin(r, scheme+"://"+r.Host)
}

func officeCheckedOrigin(r *http.Request, origin string) (string, bool) {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Host != r.Host || u.User != nil || origin != u.Scheme+"://"+u.Host || strings.ContainsAny(u.Host, "\\/\x00\r\n\t ") {
		return "", false
	}
	if r.TLS != nil && u.Scheme != "https" {
		return "", false
	}
	return origin, true
}

// Origin-less asset, iframe and status requests keep the admitted browser
// scheme, even when Pogo is behind a TLS terminator. Cookie ports are not
// isolated by browsers, so both the Host and any Origin must match the grant.
func officeBoundOrigin(r *http.Request, bound string) bool {
	if r == nil || bound == "" {
		return false
	}
	if _, ok := officeCheckedOrigin(r, bound); !ok {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != bound) ||
		(r.Header.Get("X-Pogo-Office-Origin") != "" && r.Header.Get("X-Pogo-Office-Origin") != bound) {
		return false
	}
	if r.Header.Get("Origin") == "" && r.Header.Get("X-Pogo-Office-Origin") == "" {
		if referrer := r.Header.Get("Referer"); referrer != "" {
			u, err := url.Parse(referrer)
			if err != nil || u.User != nil || u.Scheme+"://"+u.Host != bound {
				return false
			}
		}
	}
	return true
}
