//go:build linux

package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCommandOutputQuotaStopsTheProcess(t *testing.T) {
	command := fmt.Sprintf("head -c %d /dev/zero; sleep 30", MaxCommandOutputBytes+1)
	for _, kind := range []string{"bash", "declared"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			started := time.Now()
			var result Result
			var err error
			if kind == "bash" {
				result, err = (&Bash{Cwd: t.TempDir(), SpillDir: dir}).Execute(ctx, args(t, bashArgs{Command: command}))
				if details, ok := result.Details.(BashDetails); !ok || details.FullOutputPath != "" || details.TimedOut {
					t.Fatalf("incorrect failure details: %+v", result.Details)
				}
			} else {
				tool := &SynthTool{Cwd: t.TempDir(), SpillDir: dir, info: SynthInfo{WireName: "output-probe"}, exec: synthExec{argv: []string{"sh", "-c", command}, timeout: 60}}
				result, err = tool.Execute(ctx, []byte(`{}`))
				if details, ok := result.Details.(SynthDetails); !ok || details.FullOutputPath != "" || details.TimedOut {
					t.Fatalf("incorrect failure details: %+v", result.Details)
				}
			}
			if err == nil || !strings.Contains(err.Error(), "output exceeded") || ctx.Err() != nil || time.Since(started) >= 20*time.Second {
				t.Fatalf("command not stopped by output quota: elapsed=%v context=%v", time.Since(started), ctx.Err())
			}
			files, readErr := os.ReadDir(dir)
			if readErr != nil || len(files) != 0 {
				t.Fatalf("incomplete spill leaked: files=%v err=%v", files, readErr)
			}
		})
	}
}
