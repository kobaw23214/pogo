package webgateway

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

func TestManagedWebsiteLocalImagesKeepTheirBytesAndScopedPolicy(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"path", "subdomain"} {
		t.Run(mode, func(t *testing.T) {
			app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/assets/images/spring.png" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(png)
			})
			var f *routerFixture
			if mode == "path" {
				f = testPathRouter(t, app, nil, false)
			} else {
				f = testRouter(t, app, nil, nil, true)
			}
			grant := applyOpen(t, f, f.record())
			response, body := response(t, f.client, http.MethodGet, grant.PreviewURL+"assets/images/spring.png", nil, nil)
			if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "image/png" || body != string(png) {
				t.Fatalf("local image was altered: status=%d type=%q bytes=%d", response.StatusCode, response.Header.Get("Content-Type"), len(body))
			}
			policy := cspDirective(response.Header.Get("Content-Security-Policy"), "img-src")
			if len(policy) != 3 || policy[0] != "data:" || policy[1] != "blob:" || (policy[2] != grant.PreviewURL && policy[2] != strings.TrimSuffix(grant.PreviewURL, "/")) {
				t.Fatalf("image policy escaped the website scope: %v", policy)
			}
			if !strings.Contains(strings.Join(grant.Limitations, " "), "images must be local project assets") {
				t.Fatal("open receipt did not explain the local image requirement")
			}
		})
	}
}
