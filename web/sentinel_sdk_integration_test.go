package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/sentinelexec"
	"github.com/yosukeno/pogo/sentinelsdk"
	"github.com/yosukeno/pogo/tools"
)

type sentinelSDKPrepareReceipt struct {
	SDKVersion    string `json:"sdk_version"`
	SDKPath       string `json:"sdk_path"`
	ScriptPath    string `json:"script_path"`
	Mode          string `json:"mode"`
	EntryCreated  bool   `json:"entry_created"`
	SDKCreated    bool   `json:"sdk_created"`
	Documentation string `json:"documentation"`
}

func sentinelSDKExecute(t *testing.T, h *harness, s *Session, request map[string]any) (tools.Result, error) {
	t.Helper()
	return (&tools.Watch{Backend: h.mgr.sentinelBackend(s)}).Execute(context.Background(), json.RawMessage(studioBody(request)))
}

func sentinelSDKPrepare(t *testing.T, h *harness, s *Session, script, mode string) sentinelSDKPrepareReceipt {
	t.Helper()
	result, err := sentinelSDKExecute(t, h, s, map[string]any{"action": "prepare", "script_path": script, "mode": mode})
	if err != nil {
		t.Fatal(err)
	}
	var out sentinelSDKPrepareReceipt
	if err := json.Unmarshal([]byte(result.Text), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func sentinelSDKAssertNoWatching(t *testing.T, h *harness, capture *studioCapture) {
	t.Helper()
	db, err := h.mgr.sentinels.database(false)
	if err != nil {
		t.Fatal(err)
	}
	if db != nil {
		watches, err := db.ListAll()
		if err != nil || len(watches) != 0 {
			t.Fatalf("prepare created a watch: watches=%+v err=%v", watches, err)
		}
	}
	if sentinelModelCalls(capture) != 0 {
		t.Fatal("prepare called a model")
	}
}

func sentinelSDKCreate(t *testing.T, h *harness, s *Session, script, mode string) sentinel.Watch {
	t.Helper()
	result, err := sentinelSDKExecute(t, h, s, map[string]any{"action": "create", "script_path": script, "mode": mode, "title": "SDK-backed custom source", "task": "Investigate meaningful source events and save a Pogo report."})
	if err != nil {
		t.Fatal(err)
	}
	var watch sentinel.Watch
	if err := json.Unmarshal([]byte(result.Text), &watch); err != nil {
		t.Fatal(err)
	}
	return watch
}

func TestSentinelSDKPrepareIsIdempotentAndDoesNotStartWatching(t *testing.T) {
	h, capture := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	const script = "watchers/custom/main.py"
	first := sentinelSDKPrepare(t, h, s, script, "stream")
	if first.SDKVersion != sentinelsdk.Version || first.ScriptPath != script || first.SDKPath != "watchers/custom/"+sentinelsdk.FileName || first.Mode != "stream" || !first.EntryCreated || !first.SDKCreated || strings.TrimSpace(first.Documentation) == "" {
		t.Fatalf("invalid preparation receipt: %+v", first)
	}
	entry, err := os.ReadFile(filepath.Join(s.cwd, script))
	if err != nil || len(entry) == 0 {
		t.Fatalf("missing starter: %q %v", entry, err)
	}
	sdk, err := os.ReadFile(filepath.Join(s.cwd, first.SDKPath))
	if err != nil || !bytes.Equal(sdk, sentinelsdk.Source()) {
		t.Fatalf("prepared helper is not the trusted SDK: %v", err)
	}
	second := sentinelSDKPrepare(t, h, s, script, "stream")
	if second.EntryCreated || second.SDKCreated {
		t.Fatalf("repeated prepare rewrote existing files: %+v", second)
	}
	if after, err := os.ReadFile(filepath.Join(s.cwd, script)); err != nil || !bytes.Equal(after, entry) {
		t.Fatal("repeated prepare changed the entry", err)
	}
	sentinelSDKAssertNoWatching(t, h, capture)
}

func TestSentinelSDKConcurrentPreparePublishesEachFileOnce(t *testing.T) {
	h, capture := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	const writers = 12
	type outcome struct {
		receipt sentinelSDKPrepareReceipt
		err     error
	}
	start := make(chan struct{})
	results := make(chan outcome, writers)
	for i := 0; i < writers; i++ {
		go func() {
			<-start
			result, err := (&tools.Watch{Backend: h.mgr.sentinelBackend(s)}).Execute(context.Background(), json.RawMessage(studioBody(map[string]any{"action": "prepare", "script_path": "watchers/concurrent/main.py", "mode": "poll"})))
			var receipt sentinelSDKPrepareReceipt
			if err == nil {
				err = json.Unmarshal([]byte(result.Text), &receipt)
			}
			results <- outcome{receipt: receipt, err: err}
		}()
	}
	close(start)
	entries, helpers := 0, 0
	for i := 0; i < writers; i++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent preparation failed: %v", result.err)
		}
		if result.receipt.EntryCreated {
			entries++
		}
		if result.receipt.SDKCreated {
			helpers++
		}
	}
	if entries != 1 || helpers != 1 {
		t.Fatalf("concurrent prepare published files more than once: entries=%d helpers=%d", entries, helpers)
	}
	starter, err := sentinelsdk.Starter("poll")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.cwd, "watchers", "concurrent")
	if data, err := os.ReadFile(filepath.Join(dir, "main.py")); err != nil || !bytes.Equal(data, starter) {
		t.Fatal("concurrent preparation corrupted the entry", err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, sentinelsdk.FileName)); err != nil || !bytes.Equal(data, sentinelsdk.Source()) {
		t.Fatal("concurrent preparation corrupted the helper", err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 2 {
		t.Fatalf("preparation left partial files: files=%v err=%v", files, err)
	}
	sentinelSDKAssertNoWatching(t, h, capture)
}

func TestSentinelSDKEntryPublishNeverReplacesCompetingWriter(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	const writers = 16
	type outcome struct {
		index   int
		created bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan outcome, writers)
	for i := 0; i < writers; i++ {
		go func(index int) {
			<-start
			created, err := sentinelSDKPublish(context.Background(), root, "main.py", []byte(fmt.Sprintf("# user entry from writer %d\n", index)), false)
			results <- outcome{index: index, created: created, err: err}
		}(i)
	}
	close(start)
	winningIndex, created := -1, 0
	for i := 0; i < writers; i++ {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.created {
			winningIndex, created = result.index, created+1
		}
	}
	if created != 1 {
		t.Fatalf("competing writers replaced each other: %d successful publications", created)
	}
	expected := []byte(fmt.Sprintf("# user entry from writer %d\n", winningIndex))
	if data, err := os.ReadFile(filepath.Join(dir, "main.py")); err != nil || !bytes.Equal(data, expected) {
		t.Fatalf("winning entry was clobbered: got=%q expected=%q err=%v", data, expected, err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("competing publications left partial files: files=%v err=%v", files, err)
	}
}

func TestSentinelSDKPreparePreservesExistingUserEntry(t *testing.T) {
	h, capture := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	dir := filepath.Join(s.cwd, "watchers", "existing")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	entry := []byte("# User's existing implementation, not a generated template.\n")
	if err := os.WriteFile(filepath.Join(dir, "main.py"), entry, 0600); err != nil {
		t.Fatal(err)
	}
	out := sentinelSDKPrepare(t, h, s, "watchers/existing/main.py", "poll")
	if out.EntryCreated || !out.SDKCreated {
		t.Fatalf("incorrect existing-entry receipt: %+v", out)
	}
	if after, err := os.ReadFile(filepath.Join(dir, "main.py")); err != nil || !bytes.Equal(entry, after) {
		t.Fatal("prepare overwrote the user's entry", err)
	}
	sentinelSDKAssertNoWatching(t, h, capture)
}

func TestSentinelSDKRejectsModifiedHelperWithoutOverwritingIt(t *testing.T) {
	h, capture := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	out := sentinelSDKPrepare(t, h, s, "watchers/tampered/main.py", "poll")
	bad := []byte("# User-modified helper cannot claim the platform SDK version.\n")
	if err := os.Chmod(filepath.Join(s.cwd, out.SDKPath), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.cwd, out.SDKPath), bad, 0600); err != nil {
		t.Fatal(err)
	}
	entryBefore, err := os.ReadFile(filepath.Join(s.cwd, out.ScriptPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []map[string]any{
		{"action": "prepare", "script_path": out.ScriptPath, "mode": "poll"},
		{"action": "create", "script_path": out.ScriptPath, "mode": "poll", "title": "Untrusted SDK", "task": "Observe a custom source."},
	} {
		if _, err := sentinelSDKExecute(t, h, s, request); err == nil {
			t.Fatalf("accepted modified helper for %v", request["action"])
		}
	}
	called := false
	h.mgr.sentinels.runner = func(context.Context, sentinelexec.Config, func(sentinel.Frame) error) error {
		called = true
		return nil
	}
	if _, err := (&tools.WatchTest{Backend: h.mgr.sentinelBackend(s)}).Execute(context.Background(), json.RawMessage(studioBody(map[string]any{"script_path": out.ScriptPath, "mode": "poll"}))); err == nil || called {
		t.Fatalf("dry run executed modified helper: err=%v executed=%v", err, called)
	}
	if after, err := os.ReadFile(filepath.Join(s.cwd, out.SDKPath)); err != nil || !bytes.Equal(after, bad) {
		t.Fatal("rejected prepare overwrote the modified helper", err)
	}
	if after, err := os.ReadFile(filepath.Join(s.cwd, out.ScriptPath)); err != nil || !bytes.Equal(after, entryBefore) {
		t.Fatal("rejected prepare overwrote the entry", err)
	}
	sentinelSDKAssertNoWatching(t, h, capture)
}

func TestSentinelSDKCreateSealsTrustedDependencyAndKeepsRawProtocolCompatible(t *testing.T) {
	h, capture := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	out := sentinelSDKPrepare(t, h, s, "watchers/sealed/main.py", "poll")
	entry := []byte("from pogo_watch import Watcher\nwith Watcher(mode='poll') as w:\n    w.checkpoint(state={'checked': True})\n")
	if err := os.WriteFile(filepath.Join(s.cwd, out.ScriptPath), entry, 0600); err != nil {
		t.Fatal(err)
	}
	w := sentinelSDKCreate(t, h, s, out.ScriptPath, "poll")
	if w.Spec.Enabled {
		t.Fatal("SDK registration unexpectedly enabled the watch")
	}
	sealedHelper := filepath.Join(w.Spec.ScriptDir, sentinelsdk.FileName)
	if data, err := os.ReadFile(sealedHelper); err != nil || !bytes.Equal(data, sentinelsdk.Source()) {
		t.Fatal("trusted SDK was not sealed with the entry", err)
	}
	if err := os.Chmod(filepath.Join(s.cwd, out.SDKPath), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.cwd, out.SDKPath), []byte("changed SDK source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.cwd, out.ScriptPath), []byte("changed entry\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifySentinelBundle(w.Spec.ScriptDir); err != nil {
		t.Fatal("editing workspace changed the sealed version", err)
	}
	if data, err := os.ReadFile(filepath.Join(w.Spec.ScriptDir, "main.py")); err != nil || !bytes.Equal(data, entry) {
		t.Fatal("workspace entry change leaked into sealed version", err)
	}
	if data, err := os.ReadFile(sealedHelper); err != nil || !bytes.Equal(data, sentinelsdk.Source()) {
		t.Fatal("workspace SDK change leaked into sealed version", err)
	}
	// Existing hand-written scripts still use the same Pogo v1 runner. The SDK
	// is a dependency helper, not a second mandatory execution path.
	raw := sentinelTestBundle(t, s)
	rawWatch := sentinelSDKCreate(t, h, s, raw, "poll")
	if _, err := os.Stat(filepath.Join(rawWatch.Spec.ScriptDir, sentinelsdk.FileName)); !os.IsNotExist(err) {
		t.Fatalf("raw protocol script unexpectedly needs an injected SDK: %v", err)
	}
	if sentinelModelCalls(capture) != 0 {
		t.Fatal("preparing or sealing scripts called a model")
	}
}

func TestSentinelSDKPrepareRejectsUnsafePathsAndIrrelevantArguments(t *testing.T) {
	h, capture := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	absolute := filepath.Join(t.TempDir(), "outside-sentinel.py")
	for _, script := range []string{"../escape/main.py", absolute, "watchers/../main.py", "main.py", "watchers/" + sentinelsdk.FileName, "watchers/bad\\main.py"} {
		if _, err := sentinelSDKExecute(t, h, s, map[string]any{"action": "prepare", "script_path": script, "mode": "poll"}); err == nil {
			t.Errorf("prepare accepted unsafe path %q", script)
		}
	}
	for key, value := range map[string]any{"enabled": true, "network": true, "title": "unexpected", "watch_id": "foreign_watch", "interval_seconds": 2} {
		req := map[string]any{"action": "prepare", "script_path": "watchers/valid/main.py", "mode": "poll", key: value}
		if _, err := sentinelSDKExecute(t, h, s, req); err == nil {
			t.Errorf("prepare accepted irrelevant %s", key)
		}
	}
	sentinelSDKAssertNoWatching(t, h, capture)
}

func TestSentinelSDKPrepareRejectsSymlinkComponents(t *testing.T) {
	for _, variant := range []string{"outside_parent", "inside_parent", "entry", "sdk", "inside_entry", "inside_sdk"} {
		t.Run(variant, func(t *testing.T) {
			h, capture := newSentinelHarness(t, nil)
			s := sentinelTestSession(t, h)
			outside := t.TempDir()
			original := []byte("must not be overwritten\n")
			if variant == "sdk" || variant == "inside_sdk" {
				// Even a byte-identical trusted SDK must not turn a symlink into
				// a permissible preparation target.
				original = sentinelsdk.Source()
			}
			outsideFile := filepath.Join(outside, "outside.py")
			if err := os.WriteFile(outsideFile, original, 0600); err != nil {
				t.Fatal(err)
			}
			script := "watchers/link/main.py"
			if err := os.MkdirAll(filepath.Join(s.cwd, "watchers"), 0700); err != nil {
				t.Fatal(err)
			}
			switch variant {
			case "outside_parent":
				if err := os.Symlink(outside, filepath.Join(s.cwd, "watchers", "link")); err != nil {
					t.Fatal(err)
				}
			case "inside_parent":
				real := filepath.Join(s.cwd, "inside")
				if err := os.MkdirAll(real, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(real, filepath.Join(s.cwd, "watchers", "link")); err != nil {
					t.Fatal(err)
				}
			default:
				dir := filepath.Join(s.cwd, "watchers", "link")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				name := "main.py"
				if variant == "sdk" || variant == "inside_sdk" {
					name = sentinelsdk.FileName
				}
				target := outsideFile
				if strings.HasPrefix(variant, "inside_") {
					target = filepath.Join(s.cwd, "inside-target.py")
					if err := os.WriteFile(target, original, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := sentinelSDKExecute(t, h, s, map[string]any{"action": "prepare", "script_path": script, "mode": "stream"}); err == nil {
				t.Fatalf("prepare followed %s symlink", variant)
			}
			if data, err := os.ReadFile(outsideFile); err != nil || !bytes.Equal(data, original) {
				t.Fatal("prepare changed a symlink destination", err)
			}
			if _, err := os.Stat(filepath.Join(outside, sentinelsdk.FileName)); !os.IsNotExist(err) {
				t.Fatal("prepare wrote an SDK outside its workspace", err)
			}
			sentinelSDKAssertNoWatching(t, h, capture)
		})
	}
}

func TestSentinelSDKPrepareRejectsFIFOsWithoutBlocking(t *testing.T) {
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("mkfifo is required for the Unix FIFO safety regression")
	}
	for _, name := range []string{"main.py", sentinelsdk.FileName} {
		t.Run(name, func(t *testing.T) {
			h, capture := newSentinelHarness(t, nil)
			s := sentinelTestSession(t, h)
			dir := filepath.Join(s.cwd, "watchers", "fifo")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, name)
			setupCtx, cancelSetup := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancelSetup()
			if output, err := exec.CommandContext(setupCtx, mkfifo, path).CombinedOutput(); err != nil {
				t.Fatalf("create FIFO fixture: %v %s", err, output)
			}
			done := make(chan error, 1)
			go func() {
				_, err := (&tools.Watch{Backend: h.mgr.sentinelBackend(s)}).Execute(context.Background(), json.RawMessage(studioBody(map[string]any{"action": "prepare", "script_path": "watchers/fifo/main.py", "mode": "poll"})))
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("prepare accepted a FIFO as an entry/helper")
				}
			case <-time.After(2 * time.Second):
				// If a regression opened the pipe before checking its type,
				// unblock it before failing, rather than leaving a stuck reader.
				if file, err := os.OpenFile(path, os.O_RDWR, 0600); err == nil {
					file.Close()
				}
				select {
				case <-done:
				case <-time.After(time.Second):
				}
				t.Fatal("prepare blocked while opening a FIFO")
			}
			sentinelSDKAssertNoWatching(t, h, capture)
		})
	}
}

func TestSentinelSDKPreparedStarterFailsBeforeAnyProtocolSuccess(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required to execute a prepared SDK starter")
	}
	for _, mode := range []string{"poll", "stream"} {
		t.Run(mode, func(t *testing.T) {
			h, _ := newSentinelHarness(t, nil)
			s := sentinelTestSession(t, h)
			out := sentinelSDKPrepare(t, h, s, "watchers/unimplemented/main.py", mode)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, python, "-u", filepath.Join(s.cwd, out.ScriptPath))
			cmd.Dir = filepath.Join(s.cwd, filepath.Dir(out.ScriptPath))
			cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
			cmd.Stdin = strings.NewReader(studioBody(map[string]any{"v": 1, "kind": "boot", "mode": mode, "workspace": s.cwd, "state": nil, "cursor": nil}) + "\n")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err == nil || ctx.Err() != nil || stdout.Len() != 0 {
				t.Fatalf("unimplemented starter claimed readiness or hung: err=%v context=%v stdout=%q stderr=%q", err, ctx.Err(), stdout.String(), stderr.String())
			}
		})
	}
}

func TestSentinelSDKSealedPythonEntryImportsAndEmitsHostValidFrame(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for sealed SDK import integration")
	}
	h, capture := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	out := sentinelSDKPrepare(t, h, s, "watchers/import/main.py", "poll")
	entry := "from pogo_watch import Watcher, event\nwith Watcher(mode='poll') as w:\n    w.emit([event('custom:item:v1', 'observed', {'value': 7})], state={'checked': True}, cursor={'last': 7})\n"
	if err := os.WriteFile(filepath.Join(s.cwd, out.ScriptPath), []byte(entry), 0600); err != nil {
		t.Fatal(err)
	}
	w := sentinelSDKCreate(t, h, s, out.ScriptPath, "poll")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-u", filepath.Join(w.Spec.ScriptDir, "main.py"))
	cmd.Dir = w.Spec.ScriptDir
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	cmd.Stdin = strings.NewReader(studioBody(map[string]any{"v": 1, "kind": "boot", "mode": "poll", "workspace": s.cwd, "state": nil, "cursor": nil}) + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("sealed SDK entry failed: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	var frame sentinel.Frame
	if err := json.Unmarshal(stdout.Bytes(), &frame); err != nil {
		t.Fatalf("SDK did not produce exactly one frame: %v output=%q", err, stdout.String())
	}
	if err := sentinel.ValidateFrame(frame); err != nil {
		t.Fatalf("SDK emitted a host-invalid frame: %v", err)
	}
	var state struct {
		Checked bool `json:"checked"`
	}
	var cursor struct {
		Last int `json:"last"`
	}
	stateErr := json.Unmarshal(frame.State, &state)
	cursorErr := json.Unmarshal(frame.Cursor, &cursor)
	if len(frame.Events) != 1 || frame.Events[0].Key != "custom:item:v1" || stateErr != nil || cursorErr != nil || !state.Checked || cursor.Last != 7 {
		t.Fatalf("SDK corrupted the script's observation/state: %+v", frame)
	}
	if sentinelModelCalls(capture) != 0 {
		t.Fatal("sealed script test called a model")
	}
}
