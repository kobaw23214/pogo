package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

// Opt-in manual browser acceptance against the actual Go proxy, with no
// production server, user credentials, model calls or third-party system.
// POGO_PANEL_BROWSER_FIXTURE is the path to the local fixture metadata file.
func TestPanelCompatBrowserFixture(t *testing.T) {
	metadata := os.Getenv("POGO_PANEL_BROWSER_FIXTURE")
	if metadata == "" {
		t.Skip("set POGO_PANEL_BROWSER_FIXTURE to run browser acceptance")
	}
	var mu sync.Mutex
	var requests []string
	makeApp := func(id string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			requests = append(requests, id+" "+r.Method+" "+r.URL.RequestURI())
			mu.Unlock()
			switch r.URL.Path {
			case "/assets/main.js":
				w.Header().Set("Content-Type", "text/javascript")
				io.WriteString(w, strings.ReplaceAll(panelBrowserScript, "__APP__", id))
			case "/assets/export.js":
				w.Header().Set("Content-Type", "text/javascript")
				io.WriteString(w, `export {value} from '/assets/value.js'`)
			case "/assets/value.js":
				w.Header().Set("Content-Type", "text/javascript")
				fmt.Fprintf(w, `export const value = %q`, id)
			case "/assets/lazy.js":
				w.Header().Set("Content-Type", "text/javascript")
				io.WriteString(w, `export const lazy = 'lazy-ok'`)
			case "/assets/main.css":
				w.Header().Set("Content-Type", "text/css")
				io.WriteString(w, `@import "/assets/theme.css"; body{background: #f3f8f5 url(/assets/dot.svg) no-repeat right top; font:14px system-ui} h2{color:rgb(22, 120, 70)}`)
			case "/assets/theme.css":
				w.Header().Set("Content-Type", "text/css")
				io.WriteString(w, `pre{white-space:pre-wrap; padding:16px; border:1px solid rgb(22,120,70)}`)
			case "/assets/dot.svg":
				w.Header().Set("Content-Type", "image/svg+xml")
				io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"><circle cx="8" cy="8" r="6" fill="green"/></svg>`)
			case "/api/login":
				w.Header().Add("Set-Cookie", "app_session="+id+"; Path=/; HttpOnly; SameSite=Lax")
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"app":%q}`, id)
			case "/api/echo", "/app/relative":
				body, _ := io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"app": id, "body": string(body), "cookie": r.Header.Get("Cookie"), "query": r.URL.RawQuery, "origin": r.Header.Get("Origin")})
			case "/api/events":
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s-events\n\n", id)
				w.(http.Flusher).Flush()
			case "/api/ws":
				c, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer c.Close(websocket.StatusNormalClosure, "")
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				c.Write(ctx, websocket.MessageText, []byte(id+"-ws"))
			case "/redirect":
				http.Redirect(w, r, "/app/?redirected=1#/kept", http.StatusFound)
			default:
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; connect-src 'self'; style-src 'self'")
				fmt.Fprintf(w, `<!doctype html><html><head><title>System %s</title><link rel="stylesheet" href="/assets/main.css"><script type="module" src="/assets/main.js"></script></head><body><h2>System %s loading</h2><pre id="result">Loading</pre><img id="dynamic" alt="dynamic resource"><a href="/redirect">Redirect within this system</a></body></html>`, id, id)
			}
		}))
	}
	a, b := makeApp("A"), makeApp("B")
	defer a.Close()
	defer b.Close()
	mux := http.NewServeMux()
	mux.Handle("/panels/one/", newPanelProxy(Panel{Name: "one", URL: a.URL + "/app/?entry=A#/home", ProxyMode: "compat"}))
	mux.Handle("/panels/two/", newPanelProxy(Panel{Name: "two", URL: b.URL + "/app/?entry=B#/home", ProxyMode: "compat"}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<!doctype html><html><head><title>Pogo proxy browser acceptance</title></head><body style="font:16px system-ui"><h1>Two independent systems through Pogo compatibility proxies</h1><p>Each frame exercises modules, styles, POST bodies, login cookies, dynamic URLs, history, SSE and WebSocket.</p><iframe title="System A" src="/panels/one/app/?entry=A#/home" style="width:48%;height:750px"></iframe><iframe title="System B" src="/panels/two/app/?entry=B#/home" style="width:48%;height:750px"></iframe></body></html>`)
	})
	mux.HandleFunc("/__requests", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(requests)
	})
	stop := make(chan struct{})
	var once sync.Once
	mux.HandleFunc("POST /__stop", func(w http.ResponseWriter, r *http.Request) { once.Do(func() { close(stop) }); w.WriteHeader(204) })
	front := httptest.NewServer(mux)
	defer front.Close()
	data, _ := json.MarshalIndent(map[string]string{"url": front.URL, "sourceA": a.URL, "sourceB": b.URL}, "", "  ")
	if err := os.WriteFile(metadata, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("browser acceptance at %s", front.URL)
	select {
	case <-stop:
	case <-time.After(20 * time.Minute):
		t.Fatal("browser fixture timed out")
	}
}

const panelBrowserScript = `
import {value} from '/assets/export.js';
const result = {app: value, entry: location.search, hash: location.hash};
const put = (key, data) => {result[key]=data; document.querySelector('#result').textContent=JSON.stringify(result,null,2)};
try {
  const mod = await import('/assets/lazy.js'); put('module',mod.lazy);
  await fetch('/api/login', {method:'POST'});
  document.cookie = 'js_session=__APP__; Path=/; SameSite=Lax';
  const req = new Request(new URL('/api/echo?request=1',location.origin), {method:'POST',body:'request-body',headers:{'X-Fixture':'yes'},credentials:'include'});
  put('request', await (await fetch(req)).json());
  put('relative',await (await fetch('./relative')).json());
  put('xhr',await new Promise((resolve,reject)=>{const x=new XMLHttpRequest(); x.open('POST','/api/echo?xhr=1');x.onload=()=>resolve(JSON.parse(x.responseText)); x.onerror=reject;x.send('xhr-body')}));
  put('sse',await new Promise((resolve,reject)=>{const e=new EventSource('/api/events');e.onmessage=x=>{e.close();resolve(x.data)};e.onerror=()=>{e.close();reject(Error('SSE failed'))}}));
  put('ws',await new Promise((resolve,reject)=>{const ws=new WebSocket('ws://'+location.host+'/api/ws');ws.onmessage=e=>{resolve(e.data);ws.close()};ws.onerror=()=>reject(Error('WS failed'))}));
  const image=document.querySelector('#dynamic'); image.src='/assets/dot.svg';
  await new Promise((resolve,reject)=>{image.onload=resolve;image.onerror=reject});
  put('dynamicImage', image.naturalWidth);
  history.replaceState({},'', '/app/dashboard?history=1#preserved');
  put('history',location.pathname+location.search+location.hash);
  put('style',getComputedStyle(document.querySelector('h2')).color);
  const ok = value==='__APP__' && result.request.app===value && result.request.body==='request-body' && result.request.cookie.includes('app_session='+value) && result.request.cookie.includes('js_session='+value) && result.xhr.body==='xhr-body' && result.sse===value+'-events' && result.ws===value+'-ws' && result.dynamicImage===16 && result.style==='rgb(22, 120, 70)' && result.history.includes('/panels/') && result.module==='lazy-ok';
  document.querySelector('h2').textContent='System '+value+(ok?' PASS':' FAIL');
} catch(e) { put('error',String(e)); document.querySelector('h2').textContent='System __APP__ FAIL'; }
`
