//go:build linux

package sentinelexec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/sentinelsdk"
)

func TestLinuxDiagnosticsSafeAcrossPersistentRuntimeError(t *testing.T) {
	for _, mode := range []string{"stderr-secret", "unknown-field-secret"} {
		t.Run(mode, func(t *testing.T) {
			code := "import sys,json\njson.loads(sys.stdin.readline())\n"
			if mode == "stderr-secret" {
				code += "print('Authorization: Bearer " + diagnosticCanary + "', file=sys.stderr)\nsys.exit(7)\n"
			} else {
				code += "print(json.dumps({'v':1,'id':'x','events':[],'" + diagnosticCanary + "':True}),flush=True)\n"
			}
			cfg := linuxFixture(t, code, "poll")
			db, err := sentinel.Open(filepath.Join(t.TempDir(), "events.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			w, err := db.Create(sentinel.Spec{AgentID: "isolated", Workspace: cfg.Workspace, ScriptDir: cfg.ScriptDir, Command: cfg.Command, Title: "diagnostic probe", Task: "test only", Mode: "poll", IntervalSeconds: 1, TimeoutSeconds: 5, MaxTurns: 5, Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			err = (&Runner{}).Run(context.Background(), cfg, func(f sentinel.Frame) error { return db.Accept(w.Spec.ID, w.Spec.Generation, f) })
			if err == nil || strings.Contains(err.Error(), diagnosticCanary) {
				t.Fatalf("real sandbox diagnostic exposed source content: %v", err)
			}
			if e := db.SetRuntimeGeneration(w.Spec.ID, w.Spec.Generation, "error", err.Error(), 1, 1); e != nil {
				t.Fatal(e)
			}
			w, err = db.Get(w.Spec.ID)
			if err != nil || strings.Contains(w.Error, diagnosticCanary) || w.Pending != 0 || len(w.Cursor) != 0 {
				t.Fatalf("persistent diagnostic contains source content or advanced progress: %+v err=%v", w, err)
			}
		})
	}
}

func TestLinuxDiagnosticProtectionPreservesOpaqueProtocolDataAndACK(t *testing.T) {
	cfg := linuxFixture(t, `import sys
from pogo_watch import Watcher,event
with Watcher(mode="stream") as w:
    # State and source data are intentionally unchanged, and are not a vault.
    w.emit([event("record:1", "test data", {"literal":"SYNTHETIC_SOURCE_CREDENTIAL_7bd32a9f"})], state={"opaque":"SYNTHETIC_SOURCE_CREDENTIAL_7bd32a9f"}, cursor="cursor-token-is-data")
    assert w.cursor == "cursor-token-is-data"
    print("password=SYNTHETIC_SOURCE_CREDENTIAL_7bd32a9f", file=sys.stderr)
    w.checkpoint(cursor="next")
`, "stream")
	if err := os.WriteFile(filepath.Join(cfg.ScriptDir, sentinelsdk.FileName), sentinelsdk.Source(), 0600); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("probe finished")
	calls := 0
	err := (&Runner{}).Run(context.Background(), cfg, func(f sentinel.Frame) error {
		calls++
		if calls == 1 {
			if string(f.Cursor) != `"cursor-token-is-data"` || !strings.Contains(string(f.State), diagnosticCanary) || !strings.Contains(string(f.Events[0].Data), diagnosticCanary) {
				t.Error("diagnostic policy changed source payload")
			}
			return nil
		}
		if len(f.Events) != 0 || string(f.Cursor) != `"next"` || !json.Valid(f.State) {
			t.Error("accepted ACK did not advance the next SDK proposal")
		}
		return stop
	})
	if !errors.Is(err, stop) || calls != 2 {
		t.Fatalf("safe diagnostic handling broke bidirectional SDK transport: %v calls=%d", err, calls)
	}
}
