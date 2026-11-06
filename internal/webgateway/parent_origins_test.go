package webgateway

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func configuredAliasRouter(t *testing.T) *routerFixture {
	t.Helper()
	f := testRouter(t, nil, nil, func(options *RouterOptions) {
		original, _ := url.Parse(options.PublicOrigin)
		port := original.Port()
		options.PublicOrigin = "http://192.168.100.241:" + port
		options.PreviewMode, options.PreviewDomain = "path", ""
		options.MainHosts = []string{"192.168.100.241:" + port, "LOCALHOST:" + port, "127.0.0.1:" + port, "localhost:" + port}
	}, false)
	f.public = f.router.origin.String()
	return f
}

func cspDirective(policy, name string) []string {
	for _, directive := range strings.Split(policy, ";") {
		fields := strings.Fields(directive)
		if len(fields) > 0 && fields[0] == name {
			return fields[1:]
		}
	}
	return nil
}

func TestWebsiteParentOriginsComeOnlyFromConfiguredAuthorities(t *testing.T) {
	f := configuredAliasRouter(t)
	record := f.record()
	record.LeaseID = "alias-compatible-lease"
	grant := applyOpen(t, f, record)
	port := f.router.port
	want := []string{"http://127.0.0.1:" + port, "http://192.168.100.241:" + port, "http://localhost:" + port}
	sort.Strings(want)
	if grant.CanonicalOrigin != f.public || !reflect.DeepEqual(grant.AllowedParentOrigins, want) {
		t.Fatalf("unexpected configured parent origins: canonical=%s parents=%v", grant.CanonicalOrigin, grant.AllowedParentOrigins)
	}
	if !strings.HasPrefix(grant.PreviewURL, f.public+previewPathPrefix) {
		t.Fatal("aliases changed the canonical website address")
	}
	// A consumer cannot mutate the router's trusted configuration through a
	// returned Go slice, just as a browser cannot supply entries through headers.
	grant.AllowedParentOrigins[0] = "http://evil.invalid:" + port
	opened, err := f.router.Open(context.Background(), record)
	if err != nil || !reflect.DeepEqual(opened.AllowedParentOrigins, want) || opened.PreviewURL != grant.PreviewURL {
		t.Fatal("opening or receipt mutation changed configured origins or address")
	}
	for _, address := range want {
		u, _ := url.Parse(grant.PreviewURL)
		alias, _ := url.Parse(address)
		u.Host = alias.Host
		response, _ := response(t, f.client, http.MethodGet, u.String(), nil, map[string]string{
			"Origin": "http://evil.invalid:" + port, "X-Forwarded-Host": "evil.invalid:" + port, "Forwarded": "host=evil.invalid",
		})
		if response.StatusCode != http.StatusOK {
			t.Fatalf("configured alias %s returned %d", address, response.StatusCode)
		}
		policy := response.Header.Get("Content-Security-Policy")
		if parents := cspDirective(policy, "frame-ancestors"); !reflect.DeepEqual(parents, want) {
			t.Fatalf("request headers changed parent policy: %v", parents)
		}
		if !strings.Contains(policy, "sandbox allow-scripts allow-forms;") || strings.Contains(policy, "allow-same-origin") || strings.Contains(policy, "evil.invalid") {
			t.Fatalf("opaque isolation or configured parent boundary changed: %s", policy)
		}
		ws := strings.Replace(u.String(), "http://", "ws://", 1)
		if sources := cspDirective(policy, "connect-src"); !reflect.DeepEqual(sources, []string{u.String(), ws}) {
			t.Fatalf("application API/WS sources escaped its path: %v", sources)
		}
	}
	unknown, _ := url.Parse(grant.PreviewURL)
	unknown.Host = "evil.invalid:" + port
	resp, _ := response(t, f.client, http.MethodGet, unknown.String(), nil, map[string]string{"Origin": f.public})
	if resp.StatusCode != http.StatusNotFound || strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors http://evil.invalid") {
		t.Fatal("an unconfigured request authority was admitted as a parent")
	}
}

func TestConfiguredParentOriginsUseBrowserOriginSerialization(t *testing.T) {
	for _, test := range []struct {
		name, public, canonical string
		hosts, parents          []string
	}{
		{"http-default", "http://LOCALHOST:80/", "http://localhost", []string{"LOCALHOST:80", "127.0.0.1:80", "[0:0:0:0:0:0:0:1]:80"}, []string{"http://127.0.0.1", "http://[::1]", "http://localhost"}},
		{"https-default", "https://POGO.EXAMPLE.TEST:443", "https://pogo.example.test", []string{"POGO.EXAMPLE.TEST:443", "LOCALHOST:443"}, []string{"https://localhost", "https://pogo.example.test"}},
		{"mapped-ipv6", "http://[::ffff:192.168.1.2]:80", "http://[::ffff:c0a8:102]", []string{"[::ffff:192.168.1.2]:80"}, []string{"http://[::ffff:c0a8:102]"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			router, err := NewRouter(RouterOptions{MainUpstream: "http://pogo:7777", PublicOrigin: test.public, MainHosts: test.hosts, PreviewMode: "path", AllowInsecureHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			defer router.Close()
			if router.origin.String() != test.canonical || !reflect.DeepEqual(router.parentOrigins, test.parents) {
				t.Fatalf("not in browser origin form: canonical=%s parents=%v", router.origin.String(), router.parentOrigins)
			}
		})
	}
}

func TestParentOriginsRejectMalformedOrUnboundedConfiguration(t *testing.T) {
	for _, hosts := range [][]string{
		{"localhost:19770", "evil.invalid:19770; frame-ancestors *"},
		{"localhost:19770", "http://evil.invalid:19770"},
		{"localhost:19770", "evil.invalid:19770/path"},
		{"localhost:19770", "evil.invalid:19770\r\nHeader: injected"},
		strings.Split(strings.Repeat("localhost:19770,", 256)+"localhost:19770", ","),
	} {
		if router, err := NewRouter(RouterOptions{MainUpstream: "http://pogo:7777", PublicOrigin: "http://localhost:19770", MainHosts: hosts, PreviewMode: "path", AllowInsecureHTTP: true}); err == nil {
			router.Close()
			t.Fatal("unsafe or oversized configured parent list accepted")
		}
	}
}
