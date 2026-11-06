package web

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// The built front end is compiled into the binary, so `pogo -web` needs nothing
// on disk beside itself. That single-file property is the main reason this is
// written in Go, and giving it up for a static file directory would be a poor
// trade.
//
// Building requires web/ui/dist to exist:
//
//	cd web/ui && npm ci && npm run build
//
// The committed placeholder keeps `go build` working before the first npm build;
// serveIndex detects it and serves the API listing instead of a broken page.
//
//go:embed all:ui/dist
var uiFS embed.FS

// placeholderMarker appears in the committed stand-in index.html only.
const placeholderMarker = "pogo-ui-not-built"

func uiRoot() (fs.FS, bool) {
	sub, err := fs.Sub(uiFS, "ui/dist")
	if err != nil {
		return nil, false
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return nil, false
	}
	return sub, !strings.Contains(string(index), placeholderMarker)
}

// handleUI serves the built app, falling back to index.html for any path the
// bundle does not contain. There is no router in the app yet, but the fallback
// costs nothing and stops a stray URL from returning a bare 404.
func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	// Retired platform routes must not fall through to the SPA or dev proxy.
	// Agent chat embedding and Agent-private workflow routes remain separate.
	for _, prefix := range []string{"/manage/workflows", "/embed/workflows", "/api/manage/workflows", "/api/workflows", "/api/workflow-embed"} {
		if r.URL.Path == prefix || strings.HasPrefix(r.URL.Path, prefix+"/") {
			http.NotFound(w, r)
			return
		}
	}
	// "/" is the home address: the client route table maps it to the manage
	// surface (我的 Agents), same view as /manage. Any ?token= query rides on
	// the URL untouched — the app reads it wherever it lands.
	if s.proxy != nil {
		// Development: vite owns everything that is not /api.
		s.proxy.ServeHTTP(w, r)
		return
	}

	root, built := uiRoot()
	if !built {
		s.handleAPIListing(w, r)
		return
	}

	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	f, err := root.Open(name)
	if err != nil {
		// A missing bundle asset is not a route: answering index.html here
		// sends HTML where a module was asked for, and the browser reports
		// "failed to fetch dynamically imported module" for a request the
		// server answered 200. That is exactly what a tab left open across a
		// redeploy does — every hashed name it still references is gone.
		// 404 says the truth; the app reloads itself on that error.
		if strings.HasPrefix(name, "assets/") {
			http.Error(w, "no such asset: "+name, http.StatusNotFound)
			return
		}
		// Unknown path: hand back the app shell rather than a 404.
		name = "index.html"
		if f, err = root.Open(name); err != nil {
			http.Error(w, "ui not available", http.StatusNotFound)
			return
		}
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.Error(w, "ui not available", http.StatusNotFound)
		return
	}
	// Hashed asset names make them safe to cache hard; index.html must not be.
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	// Files from an embed.FS are seekable, which is what lets ServeContent handle
	// range requests and content types for us.
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "ui not available", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, name, st.ModTime(), rs)
}
