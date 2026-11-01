//go:build linux

package sentinelexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/sentinelsdk"
)

func linuxFixture(t *testing.T, code, mode string) Config {
	t.Helper()
	if os.Getenv("SENTINEL_REQUIRE_SANDBOX") != "1" {
		t.Skip("set SENTINEL_REQUIRE_SANDBOX=1 in isolated Linux test container")
	}
	dir, workspace := t.TempDir(), t.TempDir()
	path := filepath.Join(dir, "source.py")
	if err := os.WriteFile(path, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	return Config{Command: []string{"/opt/venv/bin/python3", "-u", path}, ScriptDir: dir, Workspace: workspace, Mode: mode, Timeout: 5 * time.Second}
}

func TestLinuxReadOnlySourceAndBidirectionalStream(t *testing.T) {
	cfg := linuxFixture(t, `import sys,json,pathlib,errno,os
b=json.loads(sys.stdin.readline())
assert b['kind']=='boot' and b['mode']=='stream'
for root in (pathlib.Path.cwd(),pathlib.Path(b['workspace'])):
 try:
  (root/'forbidden').write_text('x')
  raise RuntimeError('readonly mount writable')
 except OSError as e: assert e.errno in (errno.EROFS,errno.EACCES)
assert os.environ.get('SENTINEL_REQUIRE_SANDBOX') is None
assert not any(k.startswith('POGO_') for k in os.environ)
print(json.dumps({'v':1,'id':'first','events':[{'key':'first','summary':'observed'}]}),flush=True)
a=json.loads(sys.stdin.readline())
assert a=={'v':1,'kind':'accepted','id':'first'}
print(json.dumps({'v':1,'id':'second','events':[{'key':'second','summary':'ack received'}]}),flush=True)
sys.stdin.readline()
`, "stream")
	stop := errors.New("stream probe complete")
	n := 0
	err := (&Runner{}).Run(context.Background(), cfg, func(f sentinel.Frame) error {
		n++
		if n == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || n != 2 {
		t.Fatalf("stream: %v frames=%d", err, n)
	}
	for _, root := range []string{cfg.ScriptDir, cfg.Workspace} {
		if _, err := os.Stat(filepath.Join(root, "forbidden")); !os.IsNotExist(err) {
			t.Fatal("script modified readonly source/workspace")
		}
	}
}

func TestLinuxSDKCapacityFailureRetainsOnlyACKedProgress(t *testing.T) {
	for _, mode := range []string{"poll", "stream"} {
		t.Run(mode, func(t *testing.T) {
			cfg := linuxFixture(t, `from pogo_watch import Watcher, event
with Watcher() as watcher:
    watcher.emit([event("accepted-first", "first observation")], state={"n": 1}, cursor={"n": 1})
    # A poll's proposal is not committed yet. A stream's first frame was ACKed.
    try:
        if watcher.mode == "poll":
            watcher.fail("capacity_exceeded")
        else:
            watcher.checkpoint(state={"too_large": "x" * (16 * 1024)})
    except BaseException:
        pass
    # Even a deliberately caught capacity failure cannot become quiet success.
    watcher.checkpoint(state={"n": 999}, cursor={"n": 999})
`, mode)
			if err := os.WriteFile(filepath.Join(cfg.ScriptDir, sentinelsdk.FileName), sentinelsdk.Source(), 0600); err != nil {
				t.Fatal(err)
			}
			accepted := 0
			last := "boot"
			err := (&Runner{}).Run(context.Background(), cfg, func(f sentinel.Frame) error {
				accepted++
				last = string(f.Cursor)
				return nil
			})
			wantAccepted, wantCursor := 0, "boot"
			if mode == "stream" {
				wantAccepted, wantCursor = 1, `{"n":1}`
			}
			if err == nil || !strings.Contains(err.Error(), "capacity_exceeded:") || accepted != wantAccepted || last != wantCursor {
				t.Fatalf("capacity failure changed unconfirmed progress: err=%v accepted=%d cursor=%s", err, accepted, last)
			}
		})
	}
}

func TestLinuxSampledRSSLimit(t *testing.T) {
	cfg := linuxFixture(t, `import sys,json,time
json.loads(sys.stdin.readline())
a=bytearray(80*1024*1024)
time.sleep(10)
`, "stream")
	cfg.MemoryMiB = 32
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := (&Runner{}).Run(ctx, cfg, func(sentinel.Frame) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "RSS") {
		t.Fatalf("RSS budget not supervised: %v", err)
	}
}

func TestLinuxSampledCPUBudget(t *testing.T) {
	cfg := linuxFixture(t, `import sys,json
json.loads(sys.stdin.readline())
while True: pass
`, "stream")
	cfg.CPUMilliCores = 50
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	err := (&Runner{}).Run(ctx, cfg, func(sentinel.Frame) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "CPU") {
		t.Fatalf("CPU budget not supervised: %v", err)
	}
}
