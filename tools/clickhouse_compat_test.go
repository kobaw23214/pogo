//go:build !windows

package tools

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The compatibility wrapper is a shell script; these tests drive it exactly
// as the image does, with a stub native client and a stub HTTP server
// standing in for ClickHouse. The real binary and the real server cannot
// run on every test host — and the native-versus-HTTP split is precisely
// what varies between hosts, so the stubs are not a simplification but the
// only way to see both halves from one machine.

const compatScript = "../scripts/clickhouse-client-compat"

// stubNative writes a /bin/sh script standing in for the official binary.
// mode "ok" behaves like a working client (records argv, prints a marker);
// mode "crash" dies like a SIGILL binary, recording each probe.
func stubNative(t *testing.T, dir, mode string) string {
	t.Helper()
	p := filepath.Join(dir, "clickhouse-client")
	body := "#!/bin/sh\nprintf probe >>\"$STUB_MARK\"\nexit 132\n"
	if mode == "ok" {
		body = "#!/bin/sh\nprintf '%s\\n' \"$*\" >>\"$STUB_MARK\"\necho stub-native-ok\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// compatServe records every request (URL and body) and answers with the
// given status and body.
func compatServe(t *testing.T, status int, body string) (*httptest.Server, func() ([]http.Request, []string)) {
	t.Helper()
	var mu sync.Mutex
	var reqs []http.Request
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		mu.Lock()
		reqs = append(reqs, *r)
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() ([]http.Request, []string) {
		mu.Lock()
		defer mu.Unlock()
		return append([]http.Request(nil), reqs...), append([]string(nil), bodies...)
	}
}

func compatFirstRequest(snapshot func() ([]http.Request, []string)) http.Request {
	requests, _ := snapshot()
	return requests[0]
}

func compatFirstBody(snapshot func() ([]http.Request, []string)) string {
	_, bodies := snapshot()
	return bodies[0]
}

func compatRequestCount(snapshot func() ([]http.Request, []string)) int {
	requests, _ := snapshot()
	return len(requests)
}

type compatRun struct {
	stdout, stderr string
	exitCode       int
}

func runCompat(t *testing.T, env map[string]string, stdin string, args ...string) compatRun {
	t.Helper()
	argv := append([]string{compatScript}, args...)
	cmd := exec.Command("sh", argv...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	err := cmd.Run()
	run := compatRun{stdout: out.String(), stderr: errb.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		run.exitCode = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run wrapper: %v", err)
	}
	return run
}

// compatEnv points the wrapper at a stub binary, a private probe cache and
// a shared probe/argv marker inside one test directory.
func compatEnv(t *testing.T, real string) (map[string]string, *string) {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "mark")
	env := map[string]string{
		"POGO_CLICKHOUSE_REAL":  real,
		"POGO_CLICKHOUSE_STATE": filepath.Join(dir, "state"),
		"STUB_MARK":             marker,
	}
	return env, &marker
}

func readCompatFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestClickhouseCompatNativeFastPath(t *testing.T) {
	real := stubNative(t, t.TempDir(), "ok")
	env, marker := compatEnv(t, real)
	run := runCompat(t, env, "", "--host", "h1", "-q", "SELECT 42")
	if run.exitCode != 0 || run.stdout != "stub-native-ok\n" {
		t.Fatalf("native exec passthrough: exit=%d stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	lines := strings.Split(strings.TrimSpace(readCompatFile(t, *marker)), "\n")
	want := []string{"--version", "--host h1 -q SELECT 42"}
	if fmt.Sprint(lines) != fmt.Sprint(want) {
		t.Fatalf("stub argv = %v, want %v", lines, want)
	}
	if got := strings.TrimSpace(readCompatFile(t, env["POGO_CLICKHOUSE_STATE"])); got != "native" {
		t.Fatalf("cached mode = %q, want native", got)
	}
}

func TestClickhouseCompatHTTPFallbackQuery(t *testing.T) {
	real := stubNative(t, t.TempDir(), "crash")
	env, marker := compatEnv(t, real)
	srv, snapshot := compatServe(t, http.StatusOK, "42\n")
	port := portOf(t, srv)

	run := runCompat(t, env, "", "--host", "127.0.0.1", "--port", port,
		"-u", "alice", "--password", "s3cret", "-q", "SELECT 42")
	if run.exitCode != 0 || run.stdout != "42\n" {
		t.Fatalf("fallback query: exit=%d stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	if !strings.Contains(run.stderr, "cannot run on this CPU") ||
		!strings.Contains(run.stderr, "forwarded over HTTP") {
		t.Fatalf("notice missing CPU explanation: %q", run.stderr)
	}
	r := compatFirstRequest(snapshot)
	if r.Method != "POST" || compatFirstBody(snapshot) != "SELECT 42" {
		t.Fatalf("request = %s %q, want POST with query as body", r.Method, compatFirstBody(snapshot))
	}
	if r.Header.Get("X-ClickHouse-User") != "alice" || r.Header.Get("X-ClickHouse-Key") != "s3cret" {
		t.Fatalf("auth headers missing: %+v", r.Header)
	}
	if r.URL.RawQuery != "" {
		t.Fatalf("plain query must not use URL params, got %q", r.URL.RawQuery)
	}
	if probes := strings.Count(readCompatFile(t, *marker), "probe"); probes != 1 {
		t.Fatalf("probe ran %d times, want exactly one (cached after)", probes)
	}

	// Second call reuses the cached verdict: another request, no new probe.
	if run := runCompat(t, env, "", "--host", "127.0.0.1", "--port", port, "-q", "SELECT 43"); run.exitCode != 0 {
		t.Fatalf("second call: exit=%d stderr=%q", run.exitCode, run.stderr)
	}
	if compatRequestCount(snapshot) != 2 {
		t.Fatalf("second request missing: %d recorded", compatRequestCount(snapshot))
	}
	if probes := strings.Count(readCompatFile(t, *marker), "probe"); probes != 1 {
		t.Fatalf("cache did not prevent a second probe: %q", readCompatFile(t, *marker))
	}
}

func TestClickhouseCompatHTTPInsertCarriesDataInBody(t *testing.T) {
	real := stubNative(t, t.TempDir(), "crash")
	env, _ := compatEnv(t, real)
	srv, snapshot := compatServe(t, http.StatusOK, "")

	run := runCompat(t, env, "{\"a\":1}\n", "--host", "127.0.0.1", "--port", portOf(t, srv),
		"-q", "  INSERT INTO t FORMAT JSONEachRow")
	if run.exitCode != 0 {
		t.Fatalf("insert: exit=%d stderr=%q", run.exitCode, run.stderr)
	}
	r := compatFirstRequest(snapshot)
	if got := r.URL.Query().Get("query"); got != "  INSERT INTO t FORMAT JSONEachRow" {
		t.Fatalf("insert statement not in query param: %q", got)
	}
	if compatFirstBody(snapshot) != "{\"a\":1}\n" {
		t.Fatalf("insert body = %q, want stdin data", compatFirstBody(snapshot))
	}
}

func TestClickhouseCompatHTTPParams(t *testing.T) {
	real := stubNative(t, t.TempDir(), "crash")
	env, _ := compatEnv(t, real)
	srv, snapshot := compatServe(t, http.StatusOK, "1\n")

	run := runCompat(t, env, "", "--host", "127.0.0.1", "--port", portOf(t, srv),
		"-d", "mydb", "-f", "CSV", "-n", "-q", "SELECT 1")
	if run.exitCode != 0 {
		t.Fatalf("params: exit=%d stderr=%q", run.exitCode, run.stderr)
	}
	q := compatFirstRequest(snapshot).URL.Query()
	if q.Get("database") != "mydb" || q.Get("default_format") != "CSV" || q.Get("multiquery") != "1" {
		t.Fatalf("params = %q (db=%q format=%q multi=%q)", compatFirstRequest(snapshot).URL.RawQuery,
			q.Get("database"), q.Get("default_format"), q.Get("multiquery"))
	}
	if compatFirstBody(snapshot) != "SELECT 1" {
		t.Fatalf("body = %q, want the query", compatFirstBody(snapshot))
	}
}

func TestClickhouseCompatHTTPStdinBatchSetsMultiquery(t *testing.T) {
	real := stubNative(t, t.TempDir(), "crash")
	env, _ := compatEnv(t, real)
	srv, snapshot := compatServe(t, http.StatusOK, "")

	run := runCompat(t, env, "SELECT 1; SELECT 2", "--host", "127.0.0.1", "--port", portOf(t, srv))
	if run.exitCode != 0 {
		t.Fatalf("stdin batch: exit=%d stderr=%q", run.exitCode, run.stderr)
	}
	if compatFirstBody(snapshot) != "SELECT 1; SELECT 2" {
		t.Fatalf("body = %q, want stdin statements", compatFirstBody(snapshot))
	}
	if got := compatFirstRequest(snapshot).URL.Query().Get("multiquery"); got != "1" {
		t.Fatalf("batch mode must set multiquery, got %q", got)
	}
}

func TestClickhouseCompatHTTPNativePortMapsToHTTPPort(t *testing.T) {
	real := stubNative(t, t.TempDir(), "crash")
	env, _ := compatEnv(t, real)
	ln, err := net.Listen("tcp", "127.0.0.1:8123")
	if err != nil {
		// Someone else holds 8123 on this host: the mapping cannot be
		// observed without talking to a stranger's server.
		t.Skipf("port 8123 unavailable: %v", err)
	}
	var hits int
	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	})}}
	srv.Start()
	t.Cleanup(srv.Close)

	run := runCompat(t, env, "", "--host", "127.0.0.1", "--port", "9000", "-q", "SELECT 1")
	if run.exitCode != 0 {
		t.Fatalf("9000→8123 mapping: exit=%d stderr=%q", run.exitCode, run.stderr)
	}
	if hits != 1 {
		t.Fatalf("native port 9000 reached the HTTP listener %d times", hits)
	}
}

func TestClickhouseCompatUnsupportedFlagRejected(t *testing.T) {
	real := stubNative(t, t.TempDir(), "crash")
	env, _ := compatEnv(t, real)
	run := runCompat(t, env, "", "--stage", "1", "-q", "SELECT 1")
	if run.exitCode != 64 {
		t.Fatalf("unsupported flag exit=%d, want 64 (EX_USAGE); stderr=%q", run.exitCode, run.stderr)
	}
	if !strings.Contains(run.stderr, "--stage") {
		t.Fatalf("error must name the rejected flag: %q", run.stderr)
	}
}

func TestClickhouseCompatHTTPServerErrorPassesThrough(t *testing.T) {
	real := stubNative(t, t.TempDir(), "crash")
	env, _ := compatEnv(t, real)
	srv, _ := compatServe(t, http.StatusInternalServerError, "DB::Exception: boom")

	run := runCompat(t, env, "", "--host", "127.0.0.1", "--port", portOf(t, srv), "-q", "SELECT 1")
	if run.exitCode != 1 || run.stdout != "" || !strings.Contains(run.stderr, "DB::Exception: boom") {
		t.Fatalf("server error: exit=%d stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
}

func TestClickhouseCompatVersionFallsBackWithoutServer(t *testing.T) {
	real := stubNative(t, t.TempDir(), "crash")
	env, _ := compatEnv(t, real)
	run := runCompat(t, env, "", "--version")
	if run.exitCode != 0 || !strings.Contains(run.stdout, "HTTP compatibility fallback") {
		t.Fatalf("version fallback: exit=%d stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
}

func portOf(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	parts := strings.Split(srv.URL, ":")
	if len(parts) != 3 {
		t.Fatalf("server url %q", srv.URL)
	}
	return parts[2]
}
