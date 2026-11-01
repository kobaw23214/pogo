package workflowcode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecuteTimeFunctionsRemainBoundedBySandbox(t *testing.T) {
	result, err := run(t, "python", "import time\ndef main(inputs):\n    before=time.monotonic_ns()\n    time.sleep(0.01)\n    return {'elapsed': time.monotonic_ns()-before}\n", `{}`)
	if err != nil || !strings.Contains(string(result.Output), "elapsed") {
		t.Fatal("time standard library unavailable", result, err)
	}
}

func requireRuntime(t *testing.T, language string) {
	t.Helper()
	for _, status := range Capabilities() {
		if status.Language == language && status.Available {
			return
		}
	}
	t.Skip("isolated " + language + " runtime unavailable on this host")
}

func run(t *testing.T, language, source string, input string) (Result, error) {
	t.Helper()
	requireRuntime(t, language)
	return Execute(context.Background(), Request{Language: language, Source: source, Inputs: json.RawMessage(input)})
}

func TestExecuteRealPythonAndJavascript(t *testing.T) {
	for _, tc := range []struct{ language, source string }{
		{"python", "import math\ndef main(inputs):\n    print('calculating')\n    return {'total': sum(inputs['prices']), 'rounded': math.ceil(2.3)}"},
		{"javascript", "async function main(inputs) { console.log('calculating'); return {total:inputs.prices.reduce((a,b)=>a+b,0),rounded:Math.ceil(2.3)}; }"},
	} {
		t.Run(tc.language, func(t *testing.T) {
			result, err := run(t, tc.language, tc.source, `{"prices":[2,3,4]}`)
			if err != nil {
				t.Fatalf("execute: %v; stderr=%s", err, result.Stderr)
			}
			var output map[string]any
			json.Unmarshal(result.Output, &output)
			if output["total"] != float64(9) || output["rounded"] != float64(3) {
				t.Fatalf("wrong output %s", result.Output)
			}
			if !strings.Contains(result.Stdout, "calculating") || result.Runtime == "" || len(result.SourceSHA256) != 64 || result.ExitCode == nil || *result.ExitCode != 0 {
				t.Fatalf("missing receipt %+v", result)
			}
		})
	}
}

func TestExecuteErrorsAndFiniteJSON(t *testing.T) {
	for _, tc := range []struct{ language, source string }{
		{"python", "def main(inputs):\n    raise ValueError('expected error')"},
		{"python", "def main(inputs):\n    return {'bad': float('nan')}"},
		{"python", "def main(inputs):\n    return [1,2]"},
		{"javascript", "function main(inputs) {throw new Error('expected error')}"},
		{"javascript", "function main(inputs) {return {bad:NaN}}"},
		{"javascript", "function main(inputs) {return [1,2]}"},
		{"javascript", "function main(inputs) {return {bad:undefined}}"},
	} {
		t.Run(tc.language+tc.source, func(t *testing.T) {
			result, err := run(t, tc.language, tc.source, `{}`)
			if err == nil || result.ErrorCode == "" || len(result.Output) != 0 || result.Stderr == "" {
				t.Fatalf("expected visible failure: %+v %v", result, err)
			}
		})
	}
}

func TestExecutePythonStandardLibrary(t *testing.T) {
	result, err := run(t, "python", "import hashlib, decimal, datetime, re, csv, io, statistics\ndef main(inputs):\n    return {'hash': hashlib.sha256(b'workflow').hexdigest(), 'decimal': str(decimal.Decimal('0.1') + decimal.Decimal('0.2')), 'mean': statistics.mean([1,2,3])}", `{}`)
	if err != nil {
		t.Fatalf("stdlib unavailable: %v %s", err, result.Stderr)
	}
	var output map[string]any
	json.Unmarshal(result.Output, &output)
	if output["decimal"] != "0.3" || output["mean"] != float64(2) {
		t.Fatalf("wrong stdlib result %s", result.Output)
	}
}

func TestExecuteTimeoutAndCancellation(t *testing.T) {
	for _, tc := range []struct{ language, source string }{{"python", "def main(inputs):\n    while True: pass"}, {"javascript", "function main(inputs) {while(true) {}}"}} {
		t.Run(tc.language, func(t *testing.T) {
			requireRuntime(t, tc.language)
			started := time.Now()
			result, err := Execute(context.Background(), Request{Language: tc.language, Source: tc.source, Inputs: json.RawMessage(`{}`), TimeoutSeconds: 1})
			if err == nil || !result.TimedOut || result.ErrorCode != "timeout" || time.Since(started) > 3*time.Second {
				t.Fatalf("timeout failed %+v %v", result, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(100*time.Millisecond, cancel)
			result, err = Execute(ctx, Request{Language: tc.language, Source: tc.source, Inputs: json.RawMessage(`{}`), TimeoutSeconds: 10})
			if err == nil || result.ErrorCode != "cancelled" || result.TimedOut {
				t.Fatalf("cancel failed %+v %v", result, err)
			}
		})
	}
}

func TestExecuteLogsAreBounded(t *testing.T) {
	for _, tc := range []struct{ language, source string }{{"python", "def main(inputs):\n    print('x' * 70000)\n    return {'ok':True}"}, {"javascript", "function main(inputs) {console.log('x'.repeat(70000));return {ok:true}}"}} {
		result, err := run(t, tc.language, tc.source, `{}`)
		if err != nil || !result.StdoutTruncated || len(result.Stdout) > MaxLogBytes {
			t.Fatalf("unbounded log %+v %v", result, err)
		}
	}
}

func TestExecuteUntrustedPythonCannotReadHostOrSpawn(t *testing.T) {
	requireRuntime(t, "python")
	directory := t.TempDir()
	hostFile := filepath.Join(directory, "private.txt")
	if err := os.WriteFile(hostFile, []byte("independent-test-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POGO_CODE_TEST_SECRET", "never-inherit-this")
	input, _ := json.Marshal(map[string]string{"path": hostFile, "write_path": filepath.Join(directory, "written.txt")})
	// Deliberately recover unrestricted Python built-ins through an allowed
	// standard module. Import filtering is not the asserted security boundary.
	source := `import json
def main(inputs):
    original = json.__builtins__
    loader = original['__import__']
    os = loader('os')
    subprocess = loader('subprocess')
    sys = loader('sys')
    socket = loader('socket')
    out = {'secret_absent': 'POGO_CODE_TEST_SECRET' not in os.environ}
    for name, action in [
        ('metadata_blocked', lambda: os.stat(inputs['path'])),
        ('read_blocked', lambda: original['open'](inputs['path']).read()),
        ('write_blocked', lambda: original['open'](inputs['write_path'], 'w').write('bad')),
        ('spawn_blocked', lambda: subprocess.run([sys.executable, '-I', '-S', '-c', 'pass'], check=True)),
        ('network_blocked', lambda: socket.socket(socket.AF_INET, socket.SOCK_STREAM).connect(('127.0.0.1', 9)))
    ]:
        try:
            action()
            out[name] = False
        except (OSError, RuntimeError):
            out[name] = True
    return out
`
	result, err := Execute(context.Background(), Request{Language: "python", Source: source, Inputs: input})
	if err != nil {
		t.Fatalf("isolation probe failed %v %s", err, result.Stderr)
	}
	var output map[string]bool
	if json.Unmarshal(result.Output, &output) != nil || len(output) != 6 {
		t.Fatalf("bad probe output %s", result.Output)
	}
	for key, value := range output {
		if !value {
			t.Errorf("boundary failed: %s", key)
		}
	}
	if _, err := os.Stat(filepath.Join(directory, "written.txt")); !os.IsNotExist(err) {
		t.Fatal("host write escaped")
	}
}

func TestExecuteUntrustedJavascriptCannotReadHostOrSpawn(t *testing.T) {
	requireRuntime(t, "javascript")
	directory := t.TempDir()
	hostFile := filepath.Join(directory, "private.txt")
	if err := os.WriteFile(hostFile, []byte("independent-js-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POGO_CODE_TEST_SECRET", "never-inherit-this")
	input, _ := json.Marshal(map[string]string{"path": hostFile})
	// Calling a host function's constructor is a known vm escape. OS policy
	// must hold even if a future V8 escape reveals process/module access.
	source := `function main(inputs) {
  let escaped;
  try { escaped = console.log.constructor('return process')(); }
  catch (e) { return {vm_escape_blocked:true}; }
  const out = {secret_absent:!escaped.env.POGO_CODE_TEST_SECRET};
  try { escaped.getBuiltinModule('fs').readFileSync(inputs.path);out.read_blocked=false; }
  catch(e) {out.read_blocked=true;}
  try { const child=escaped.getBuiltinModule('child_process').spawnSync(escaped.execPath,['-e','']);out.spawn_blocked=!!child.error; }
  catch(e) {out.spawn_blocked=true;}
  return out;
}`
	result, err := Execute(context.Background(), Request{Language: "javascript", Source: source, Inputs: input})
	if err != nil {
		t.Fatalf("JS isolation probe failed %v %s", err, result.Stderr)
	}
	var output map[string]bool
	if json.Unmarshal(result.Output, &output) != nil {
		t.Fatalf("bad probe %s", result.Output)
	}
	if output["vm_escape_blocked"] {
		return
	}
	if !output["secret_absent"] || !output["read_blocked"] || !output["spawn_blocked"] {
		t.Fatalf("OS boundary failed %s", result.Output)
	}
}

func TestExecuteRejectsInvalidRequests(t *testing.T) {
	for _, request := range []Request{{Language: "ruby", Source: "x", Inputs: json.RawMessage(`{}`)}, {Language: "python", Source: "", Inputs: json.RawMessage(`{}`)}, {Language: "python", Source: "x", Inputs: json.RawMessage(`[1]`)}, {Language: "javascript", Source: "x", Inputs: json.RawMessage(`{}`), TimeoutSeconds: 61}, {Language: "python", Source: "x", Inputs: json.RawMessage(`{"bad":1e9999}`)}} {
		result, err := Execute(context.Background(), request)
		if err == nil || result.ErrorCode == "" || result.ExitCode != nil {
			t.Fatalf("invalid request executed %+v", result)
		}
	}
}

func TestBoundedLogsDoNotExpandInvalidUTF8(t *testing.T) {
	log := &boundedLog{}
	data := []byte(strings.Repeat("\xffx", MaxLogBytes/2))
	log.Write(data)
	text, truncated := log.text()
	if !truncated || len(text) > MaxLogBytes || !json.Valid(mustMarshal(text)) {
		t.Fatal("invalid UTF-8 expanded the log budget")
	}
	log = &boundedLog{}
	log.Write([]byte(strings.Repeat("x", MaxLogBytes)))
	if _, truncated = log.text(); truncated {
		t.Fatal("exactly-full log is not truncated")
	}
}

func TestReadableNULTextRemainsBounded(t *testing.T) {
	log := &boundedLog{}
	log.Write([]byte(strings.Repeat("\x00", MaxLogBytes)))
	text, truncated := log.text()
	if !truncated || len(text) != MaxLogBytes || strings.ContainsRune(text, 0) || !strings.HasPrefix(text, `\0\0`) {
		t.Fatalf("NUL log sanitization did not retain its bound: length=%d truncated=%v", len(text), truncated)
	}
	if text := limitText(strings.Repeat("\x00", 2048), 2048); len(text) != 2048 || strings.ContainsRune(text, 0) {
		t.Fatal("NUL error text exceeded its bound")
	}
}

func TestExecuteNULLogsAndErrorsKeepJSONUnchanged(t *testing.T) {
	for _, tc := range []struct{ language, success, failure string }{
		{"python", "def main(inputs):\n    print('before\\x00after')\n    return {'value':'\\x00'}", "def main(inputs):\n    raise ValueError('before\\x00after')"},
		{"javascript", "function main(inputs) {console.log('before\\0after');return {value:'\\0'}}", "function main(inputs) {throw new Error('before\\0after')}"},
	} {
		t.Run(tc.language, func(t *testing.T) {
			result, err := run(t, tc.language, tc.success, `{}`)
			if err != nil || strings.ContainsRune(result.Stdout, 0) || !strings.Contains(result.Stdout, `before\0after`) {
				t.Fatalf("NUL stdout was not made visible: %+v %v", result, err)
			}
			var output map[string]string
			if json.Unmarshal(result.Output, &output) != nil || output["value"] != "\x00" {
				t.Fatalf("human-text sanitation changed JSON output: %s", result.Output)
			}
			result, err = run(t, tc.language, tc.failure, `{}`)
			if err == nil || strings.ContainsRune(err.Error(), 0) || strings.ContainsRune(result.Stderr, 0) || !strings.Contains(err.Error(), `before\0after`) {
				t.Fatalf("NUL error text was not made visible: %+v %v", result, err)
			}
		})
	}
}
