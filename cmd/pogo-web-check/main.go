// pogo-web-check is an isolated acceptance fixture, not a production Pogo
// server. It binds one fixed QA owner/session and never reads real accounts.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/yosukeno/pogo/internal/webgateway"
	"github.com/yosukeno/pogo/tools"
	"github.com/yosukeno/pogo/webservice"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "isolated website check:", err)
		os.Exit(1)
	}
}
func run() error {
	token := os.Getenv("POGO_WEB_RUNNER_TOKEN")
	qaToken := os.Getenv("POGO_WEB_QA_TOKEN")
	if len(qaToken) < 32 {
		return errors.New("explicit QA token is required")
	}
	root := os.Getenv("POGO_WEB_SERVICE_ROOT")
	workspace := os.Getenv("POGO_WEB_QA_WORKSPACE")
	if root == "" || workspace == "" {
		return errors.New("explicit private root/workspace are required")
	}
	project := filepath.Join(workspace, "app")
	if err := os.MkdirAll(project, 0755); err != nil {
		return err
	}
	source := filepath.Join(project, "app.py")
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(source, []byte(pythonApp), 0644); err != nil {
			return err
		}
	}
	runner, err := webservice.NewRunnerRPCClient(os.Getenv("POGO_WEB_SERVICE_RUNNER_URL"), token)
	if err != nil {
		return err
	}
	gateway := webgateway.NewAdminClient(os.Getenv("POGO_WEB_SERVICE_GATEWAY_SOCKET"))
	defer gateway.Close()
	manager, err := webservice.NewManager(webservice.Options{Root: root, Installation: os.Getenv("POGO_WEB_SERVICE_INSTALLATION"), Runner: runner, Gateway: gateway})
	if err != nil {
		return err
	}
	defer manager.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	preflight, done := context.WithTimeout(ctx, 15*time.Second)
	err = manager.Preflight(preflight)
	done()
	if err != nil {
		return err
	}
	scope := webservice.Scope{Owner: "isolated-qa-owner", Session: "isolated-qa-session", Workspace: workspace}
	tool := &tools.WebService{Backend: manager.Bind(scope)}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/health" {
			_, _ = io.WriteString(w, `{"ready":true,"fixture":true}`)
			return
		}
		if subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")), []byte(qaToken)) != 1 {
			http.Error(w, "QA authentication required", 401)
			return
		}
		if r.Method != "POST" {
			http.Error(w, "POST required", 405)
			return
		}
		if r.URL.Path == "/qa/edit" {
			data, err := os.ReadFile(source)
			if err == nil {
				err = os.WriteFile(source, []byte(strings.ReplaceAll(string(data), "version-one", "version-two")), 0644)
			}
			if err != nil {
				http.Error(w, "edit failed", 500)
				return
			}
			_, _ = io.WriteString(w, `{"edited":true}`)
			return
		}
		if r.URL.Path == "/qa/delete-session" {
			if err := manager.DeleteSession(r.Context(), scope.Owner, scope.Session); err != nil {
				http.Error(w, "cleanup was not confirmed", 500)
				return
			}
			_, _ = io.WriteString(w, `{"deleted":true}`)
			return
		}
		if r.URL.Path != "/qa/execute" {
			http.NotFound(w, r)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
		if err != nil {
			http.Error(w, "invalid QA body", 400)
			return
		}
		result, err := tool.Execute(r.Context(), raw)
		if err != nil {
			w.WriteHeader(400)
			var typed *webservice.Error
			if errors.As(err, &typed) {
				_ = json.NewEncoder(w).Encode(map[string]any{"error": typed})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
			}
			return
		}
		_, _ = io.WriteString(w, result.Text)
	})
	server := &http.Server{Addr: "0.0.0.0:7777", Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	errors := make(chan error, 1)
	go func() { errors <- server.ListenAndServe() }()
	fmt.Fprintln(os.Stdout, "QA_MANAGER_READY")
	select {
	case err := <-errors:
		return err
	case <-ctx.Done():
	}
	shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	return server.Shutdown(shutdown)
}

const pythonApp = `import json, os, sqlite3
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

VERSION = "version-one"
DATA = os.path.join(os.environ.get("POGO_DATA_DIR", "/data"), "counter.sqlite")

def counter(increment=False):
    with sqlite3.connect(DATA) as db:
        db.execute("create table if not exists counter (id integer primary key, value integer)")
        db.execute("insert or ignore into counter values (1, 0)")
        if increment:
            db.execute("update counter set value=value+1 where id=1")
        return db.execute("select value from counter where id=1").fetchone()[0]

class Handler(BaseHTTPRequestHandler):
    def reply(self, data, kind="application/json"):
        self.send_response(200)
        self.send_header("Content-Type", kind)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)
    def do_GET(self):
        if self.path == "/api/counter":
            self.reply(json.dumps({"version": VERSION, "count": counter()}).encode())
        else:
            self.reply(('''<!doctype html><html lang="zh"><meta charset="utf-8"><title>Pogo Python 服务验收</title>
<style>body{font:18px system-ui;max-width:760px;margin:60px auto;padding:24px;background:#f5f7fb;color:#203046}button{font:inherit;padding:12px 24px;border:0;border-radius:10px;background:#3665d6;color:white}pre{padding:20px;background:white;border:1px solid #ccd4e0;border-radius:12px}</style>
<h1>Pogo Python 服务验收</h1><p>独立容器 · 网关入口 · 持久计数</p><p id="version">'''+VERSION+'''</p><button id="increment">计数 +1</button><pre id="result">正在读取…</pre>
<script>async function load(method){try{const r=await fetch('/api/counter',{method:method||'GET'});document.querySelector('#result').textContent=JSON.stringify(await r.json(),null,2)}catch(e){document.querySelector('#result').textContent=String(e)}}document.querySelector('#increment').onclick=()=>load('POST');load();</script></html>''').encode(), "text/html; charset=utf-8")
    def do_POST(self):
        if self.path != "/api/counter":
            self.send_error(404)
        else:
            self.reply(json.dumps({"version": VERSION, "count": counter(True)}).encode())
    def log_message(self, fmt, *args):
        print("qa-http", self.command, self.path, flush=True)

ThreadingHTTPServer((os.environ.get("HOST", "0.0.0.0"), int(os.environ.get("PORT", "8080"))), Handler).serve_forever()
`
